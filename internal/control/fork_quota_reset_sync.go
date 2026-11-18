package control

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"gorm.io/gorm"

	"gpt-load/internal/accessquota"
	"gpt-load/internal/pricing"
	"gpt-load/internal/state"
	stateloader "gpt-load/internal/state/loader"
	"gpt-load/internal/storage/models"
)

type quotaResetSeed struct {
	AccessKeyID uint                      `json:"access_key_id"`
	Rule        accessquota.Rule          `json:"rule"`
	State       accessquota.RestoredState `json:"state"`
}

func quotaResetSingletonGroupMap(groups map[uint]struct{}, id uint) bool {
	_, ok := groups[id]
	return len(groups) == 1 && ok
}

func quotaResetLedgerSum(tx *gorm.DB, id uint, from, to int64) (int64, error) {
	var rows []struct {
		EstimatedCostNanoUSD int64
		DecisionCostNanoUSD  int64
		AuditCostNanoUSD     int64
	}
	if err := tx.Model(&models.RequestLog{}).Select("estimated_cost_nano_usd", "decision_cost_nano_usd", "audit_cost_nano_usd").Where("access_key_id = ? AND completed_at_ms >= ? AND completed_at_ms < ?", id, from, to).Find(&rows).Error; err != nil {
		return 0, err
	}
	var sum pricing.NanoUSD
	for _, row := range rows {
		cost, ok := pricing.CheckedAddNanoUSD(pricing.NanoUSD(row.EstimatedCostNanoUSD), pricing.NanoUSD(row.DecisionCostNanoUSD))
		if !ok {
			return 0, errForkKeyCycleLedgerNotReady
		}
		cost, ok = pricing.CheckedAddNanoUSD(cost, pricing.NanoUSD(row.AuditCostNanoUSD))
		if !ok {
			return 0, errForkKeyCycleLedgerNotReady
		}
		sum, ok = pricing.CheckedAddNanoUSD(sum, cost)
		if !ok {
			return 0, errForkKeyCycleLedgerNotReady
		}
	}
	return int64(sum), nil
}

func quotaResetRetryReason(reason string) bool {
	return reason == "pending_fresh" || reason == "keys_busy" || reason == "ledger_not_ready" || reason == "proof_missing" || reason == "publication_pending"
}

func (s *Service) quotaResetFence(ctx context.Context, ids []uint) (func(), bool) {
	if s.manager == nil || len(ids) == 0 {
		return func() {}, false
	}
	release, ok := s.manager.Concurrency().BeginAccessKeyFence(ids)
	if !ok {
		return func() {}, false
	}
	timeout, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		drained := true
		for _, id := range ids {
			if s.manager.Concurrency().AccessKeyCurrent(id) != 0 {
				drained = false
				break
			}
		}
		if drained {
			return release, true
		}
		select {
		case <-timeout.Done():
			release()
			return func() {}, false
		case <-ticker.C:
		}
	}
}

func (s *Service) quotaResetKeyIDs(event quotaResetEvent, allowlist []uint) []uint {
	var ids []uint
	current := s.manager.Current()
	if current == nil {
		return nil
	}
	nowMS := s.now().UnixMilli()
	for id, key := range current.AccessKeysByID {
		if key.Status != state.AccessKeyStatusActive || !quotaResetSingletonGroupMap(key.Filters.Groups, event.GroupID) || key.ExpiresAtMS != nil && *key.ExpiresAtMS <= nowMS || len(allowlist) > 0 && !slices.Contains(allowlist, id) {
			continue
		}
		for _, rule := range key.CostLimitRules {
			if rule.Kind == accessquota.KindPeriodic && rule.PeriodSeconds == event.Period {
				ids = append(ids, id)
				break
			}
		}
	}
	slices.Sort(ids)
	return ids
}

// A narrow durable writer: fence/drain affected ACTIVE keys, prove the retained
// ledger under the same DB snapshot as the mutation, commit revision+seed+receipt,
// stage seeds, then publish. Never route an unstaged committed seed through the
// generic writeConfig failure recovery (which would publish a zero revision).
func (s *Service) syncQuotaReset(ctx context.Context, event quotaResetEvent) (quotaResetSyncResult, error) {
	digest, err := hex.DecodeString(event.Digest)
	if err != nil || len(digest) != 32 || event.ID != quotaResetUUID(event.Digest) || event.Period < accessquota.MinPeriodSeconds || event.Period > accessquota.MaxPeriodSeconds {
		return quotaResetSyncResult{}, errors.New("invalid quota reset event")
	}
	if previous, exists, err := s.quotaResetReceipt(ctx, event); err != nil {
		return previous, err
	} else if exists {
		return previous, s.recoverQuotaResetSeeds(ctx, previous)
	}
	result := quotaResetSyncResult{Reason: "sync_off"}
	current := s.manager.Current()
	if current == nil || !current.Groups[event.GroupID].AccountQuotaResetSyncEnabled || !event.SyncAllowed {
		return result, nil
	}
	result.Enabled = true
	if !event.UniquePeriod {
		result.Reason = "ambiguous_period_sources"
		return result, nil
	}
	var allowlist []uint
	var intent *resetCreditSyncIntent
	if event.Kind == "card" {
		ready, ids, reason, err := s.readyResetCreditEvent(ctx, event)
		if err != nil {
			return result, err
		}
		if reason != "" {
			result.Reason = reason
			return result, nil
		}
		event, allowlist = ready, ids
		_, operationResult, found, err := s.resetCreditOperationForEvent(ctx, event)
		if err != nil {
			return result, err
		}
		if !found {
			result.Reason = "proof_missing"
			return result, nil
		}
		intent = operationResult.SyncIntent
	} else if s.cardResetOwnsWindow(ctx, event) {
		result.Reason = "superseded_by_card"
		return result, nil
	}
	ids := s.quotaResetKeyIDs(event, allowlist)
	if len(allowlist) > 0 && !slices.Equal(ids, allowlist) {
		result.Reason = "target_changed"
		return result, nil
	}
	if len(ids) == 0 {
		result.Reason = "no_matching_dedicated_periodic_keys"
		return result, nil
	}
	release, ok := s.quotaResetFence(ctx, ids)
	if !ok {
		result.Reason = "keys_busy"
		return result, nil
	}
	defer release()
	settledNewWindow := false
	if intent == nil {
		if waiter, ok := s.requestLogs.(interface {
			AwaitSettled(context.Context, int64) error
		}); ok {
			settleCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			settledNewWindow = waiter.AwaitSettled(settleCtx, event.EndMS-event.Period*1000) == nil
			cancel()
		}
	}
	// Complete precedes Emit, and fence release precedes neither. Equality against
	// native usage below additionally fails closed while async logs are pending.
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.enforceOperationRecoveryBarrierLocked(ctx, 0); err != nil {
		return result, err
	}
	var input state.CompileInput
	var installers []func() error
	err = s.withControlTransaction(ctx, func(tx *gorm.DB) error {
		if previous, exists, err := s.quotaResetReceiptDB(tx, event); err != nil {
			return err
		} else if exists {
			result = previous
			return errQuotaResetSkip
		}
		skip := func(reason string) error { result.Reason = reason; return errQuotaResetSkip }
		current := s.manager.Current()
		var group models.Group
		if err := tx.First(&group, event.GroupID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return skip("target_changed")
			}
			return err
		}
		if current == nil || !current.Groups[event.GroupID].AccountQuotaResetSyncEnabled || !groupOverrideBool(group, state.SettingAccountQuotaResetSyncEnabled) {
			result.Enabled = false
			return skip("sync_off")
		}
		if !group.Enabled || quotaResetTarget(group) != event.Target {
			return skip("target_changed")
		}
		var accounts []models.Credential
		if err := tx.Where("group_id = ? AND status = ?", event.GroupID, models.CredentialStatusActive).Find(&accounts).Error; err != nil {
			return err
		}
		if len(accounts) != 1 {
			return skip("multiple_active_accounts")
		}
		if accounts[0].ID != event.CredentialID || quotaResetIdentity(accounts[0]) != event.Identity {
			return skip("identity_changed")
		}
		now := s.now()
		nowMS := now.UnixMilli()
		if event.EndMS <= nowMS || event.EndMS-event.Period*1000 > nowMS || event.CreatedMS > nowMS || nowMS-event.CreatedMS > int64(10*time.Minute/time.Millisecond) {
			return skip("outdated_event")
		}
		var observation models.CredentialObservation
		if err := tx.Where("credential_id = ?", event.CredentialID).Take(&observation).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return skip("pending_fresh")
			}
			return err
		}
		latest := mapCredentialObservation(observation)
		if observation.IdentityFingerprint != accounts[0].IdentityFingerprint || !quotaResetFresh(latest) || *observation.ObservedAtMS < event.CreatedMS || *observation.ObservedAtMS > nowMS || nowMS-*observation.ObservedAtMS > int64(10*time.Minute/time.Millisecond) {
			return skip("pending_fresh")
		}
		matched := false
		for _, window := range quotaResetWindows(latest) {
			if window.Key == event.WindowKey && quotaResetSameAnchor(window.EndMS, event.EndMS) && window.UniquePeriod {
				matched = true
			}
		}
		if !matched {
			return skip("outdated_event")
		}
		// Re-evaluate scope inside writeMu. Never mutate a key not fenced above.
		if !slices.Equal(ids, s.quotaResetKeyIDs(event, allowlist)) {
			return skip("keys_busy")
		}
		aligned := 0
		for _, id := range ids {
			if s.manager.Concurrency().AccessKeyCurrent(id) != 0 {
				return skip("keys_busy")
			}
			var key models.AccessKey
			if err := tx.First(&key, id).Error; err != nil {
				return err
			}
			filters, err := decodeStoredAccessKeyFilters(key.Filters)
			if err != nil {
				return err
			}
			view, exists := current.AccessKeysByID[id]
			if !exists || key.Status != string(state.AccessKeyStatusActive) || view.Status != state.AccessKeyStatusActive || !singletonConfiguredGroups(filters.Groups, event.GroupID) || !forkKeyQuotaFiltersMatch(filters, view.Filters) || key.ExpiresAtMS != nil && *key.ExpiresAtMS <= nowMS {
				return skip("target_changed")
			}
			var rows []models.AccessKeyCostLimitRule
			if err := tx.Where("access_key_id = ?", id).Order("CASE WHEN kind = 'total' THEN 0 ELSE 1 END ASC, period_seconds ASC, id ASC").Find(&rows).Error; err != nil {
				return err
			}
			definitions := make([]accessquota.Rule, 0, len(rows))
			for _, row := range rows {
				definitions = append(definitions, accessquota.Rule{ID: row.ID, Revision: row.RuleRevision, Kind: accessquota.Kind(row.Kind), LimitNanoUSD: row.LimitNanoUSD, PeriodSeconds: row.PeriodSeconds})
			}
			if !slices.Equal(definitions, view.CostLimitRules) {
				return errors.New("quota reset rule configuration mismatch")
			}
			live := s.accessQuota.Snapshot(id, now)
			if len(live.Rules) != len(definitions) {
				return errors.New("quota reset runtime rule mismatch")
			}
			var totalUsed int64
			totalCount := 0
			for _, rule := range live.Rules {
				if rule.Kind == accessquota.KindTotal {
					totalUsed = rule.UsedNanoUSD
					totalCount++
				}
			}
			for index, rule := range definitions {
				native := live.Rules[index]
				if native.Rule != rule {
					return errors.New("quota reset runtime definition mismatch")
				}
				if rule.Kind != accessquota.KindPeriodic || rule.PeriodSeconds != event.Period {
					continue
				}
				// Cross-source dedup is physical-window based, not 'usage == 0'. Never clear
				// fees accrued after an earlier card/detector event aligned the same window.
				if native.WindowEndsAtMS != nil && (quotaResetSameAnchor(*native.WindowEndsAtMS, event.EndMS) || *native.WindowEndsAtMS > event.EndMS && native.WindowStartedAtMS != nil && *native.WindowStartedAtMS > event.CreatedMS) {
					aligned++
					continue
				}
				if rule.Revision == ^uint64(0) {
					return errors.New("quota reset revision overflow")
				}
				start := event.EndMS - event.Period*1000
				var used int64
				var err error
				if settledNewWindow && start >= now.Add(-time.Duration(current.Settings.RequestLogRetentionDays)*24*time.Hour).UnixMilli() && current.Settings.RequestLogRetentionDays > 0 {
					used, err = quotaResetLedgerSum(tx, id, start, event.EndMS)
				} else {
					used, err = quotaResetLedgerSeed(tx, id, native, totalUsed, totalCount, intent, start, event.EndMS, now, current.Settings.RequestLogRetentionDays)
				}
				if err != nil {
					if errors.Is(err, errForkKeyCycleLedgerNotReady) {
						return skip("ledger_not_ready")
					}
					return err
				}
				rule.Revision++
				end := event.EndMS
				seed := accessquota.RestoredState{AccessKeyID: id, RuleID: rule.ID, RuleRevision: rule.Revision, UsedNanoUSD: used, WindowStartedAtMS: &start, WindowEndsAtMS: &end, WindowGeneration: 1, SnapshotVersion: 1}
				install, err := s.accessQuota.PreparePeriodicRebase(id, []accessquota.Rule{rule}, []accessquota.RestoredState{seed})
				if err != nil {
					return err
				}
				update := tx.Model(&models.AccessKeyCostLimitRule{}).Where("id = ? AND access_key_id = ? AND rule_revision = ?", rule.ID, id, rule.Revision-1).Update("rule_revision", rule.Revision)
				if update.Error != nil {
					return update.Error
				}
				if update.RowsAffected != 1 {
					return errors.New("quota reset rule revision changed")
				}
				checkpoint := models.AccessKeyCostLimitState{RuleID: rule.ID, RuleRevision: rule.Revision, UsedNanoUSD: used, WindowStartedAtMS: &start, WindowEndsAtMS: &end, WindowGeneration: 1, SnapshotVersion: 1}
				if err := tx.Save(&checkpoint).Error; err != nil {
					return err
				}
				installers = append(installers, install)
				result.Seeds = append(result.Seeds, quotaResetSeed{AccessKeyID: id, Rule: rule, State: seed})
				result.Keys++
			}
		}
		if intent != nil {
			result.ProofFromMS, result.TotalBaselines = intent.ProofFromMS, intent.TotalBaselines
		}
		result.Reason = "synced"
		if result.Keys == 0 {
			if aligned > 0 {
				result.Reason = "already_aligned"
			} else {
				result.Reason = "no_matching_dedicated_periodic_keys"
			}
		}
		canonical, _ := json.Marshal(result)
		if err := tx.Create(&models.ControlOperation{OperationID: event.ID, IdempotencyKey: event.ID, DigestVersion: 1, RequestDigest: digest, OperationKind: quotaResetSyncKind, ResourceIdentity: event.Digest, CanonicalResult: canonical, CompletedAtMS: &nowMS}).Error; err != nil {
			return err
		}
		input, err = stateloader.BuildCompileInputWithProxy(ctx, tx, s.encryption, s.environmentProxy, s.channelRegistry)
		if err != nil {
			return err
		}
		_, err = state.Compile(input)
		return err
	})
	if errors.Is(err, errQuotaResetSkip) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	for _, install := range installers {
		if err := install(); err != nil {
			// Already fenced and holding writeMu: restage the committed DB seed
			// without recursively acquiring the same admissions fence.
			if _, stageErr := s.stageQuotaResetSeedsLocked(context.WithoutCancel(ctx), result); stageErr != nil {
				return result, errors.Join(err, stageErr)
			}
			return result, joinCommittedRuntimeRecovery(err, s.recoverCommittedRuntime(context.WithoutCancel(ctx), false))
		}
	}
	if _, err := s.publishSnapshot(input); err != nil {
		// Seeds are staged. Publish the committed revision while the SAME fence
		// still excludes requests; a later receipt replay would be too late.
		return result, joinCommittedRuntimeRecovery(err, s.recoverCommittedRuntime(context.WithoutCancel(ctx), false))
	}
	return result, nil
}

// Retry after a committed-but-unpublished transaction stages only adjacent old
// revisions. Applied/newer revisions are untouched, including their new usage.
func (s *Service) recoverQuotaResetSeeds(ctx context.Context, result quotaResetSyncResult) error {
	if len(result.Seeds) == 0 {
		return nil
	}
	ids := []uint{}
	for _, seed := range result.Seeds {
		if !slices.Contains(ids, seed.AccessKeyID) {
			ids = append(ids, seed.AccessKeyID)
		}
	}
	release, ok := s.quotaResetFence(ctx, ids)
	if !ok {
		return errors.New("quota reset recovery keys busy")
	}
	defer release()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	pending, err := s.stageQuotaResetSeedsLocked(ctx, result)
	if err != nil {
		return err
	}
	if pending {
		return s.recoverCommittedRuntime(context.WithoutCancel(ctx), false)
	}
	return nil
}

// Caller already owns writeMu and the affected keys' admissions fence.
func (s *Service) stageQuotaResetSeedsLocked(ctx context.Context, result quotaResetSyncResult) (bool, error) {
	pending := false
	for _, seed := range result.Seeds {
		for _, live := range s.accessQuota.Snapshot(seed.AccessKeyID, s.now()).Rules {
			if live.ID != seed.Rule.ID || live.Revision >= seed.Rule.Revision {
				continue
			}
			var row models.AccessKeyCostLimitRule
			var checkpoint models.AccessKeyCostLimitState
			if err := s.db.WithContext(ctx).Where("id = ? AND access_key_id = ?", seed.Rule.ID, seed.AccessKeyID).Take(&row).Error; err != nil {
				return false, err
			}
			if row.RuleRevision != seed.Rule.Revision {
				return false, errors.New("quota reset recovery revision changed")
			}
			if err := s.db.WithContext(ctx).Where("rule_id = ?", seed.Rule.ID).Take(&checkpoint).Error; err != nil {
				return false, err
			}
			if checkpoint.RuleRevision != seed.Rule.Revision || checkpoint.UsedNanoUSD != seed.State.UsedNanoUSD || checkpoint.WindowStartedAtMS == nil || checkpoint.WindowEndsAtMS == nil || *checkpoint.WindowStartedAtMS != *seed.State.WindowStartedAtMS || *checkpoint.WindowEndsAtMS != *seed.State.WindowEndsAtMS {
				return false, errors.New("quota reset recovery checkpoint changed")
			}
			install, err := s.accessQuota.PreparePeriodicRebase(seed.AccessKeyID, []accessquota.Rule{seed.Rule}, []accessquota.RestoredState{seed.State})
			if err != nil {
				return false, err
			}
			if err := install(); err != nil {
				return false, err
			}
			pending = true
		}
	}
	return pending, nil
}

// Exact integer reconciliation includes answer, decision and audit cost and uses
// completion time (the same event time as native completion accounting). Missing
// logs, imported unexplained periodic usage, retention gaps and cross-window
// tickets fail closed. A total baseline may be explicitly supplied only by the
// bounded legacy repair or captured before a new dispatch under a drained fence.
func quotaResetLedgerSeed(tx *gorm.DB, id uint, old accessquota.RuleView, totalUsed int64, totalCount int, intent *resetCreditSyncIntent, start, end int64, now time.Time, retentionDays int) (int64, error) {
	retention := now.Add(-time.Duration(retentionDays) * 24 * time.Hour).UnixMilli()
	if retentionDays <= 0 || start < retention || start > now.UnixMilli() || end <= now.UnixMilli() {
		return 0, errForkKeyCycleLedgerNotReady
	}
	from, through, expected := int64(0), now.UnixMilli()+1, old.UsedNanoUSD
	if intent != nil && intent.ProofFromMS != 0 {
		baseline, exists := intent.TotalBaselines[id]
		if !exists || totalCount != 1 || baseline < 0 || baseline > totalUsed || intent.ProofFromMS < retention || intent.ProofFromMS > start {
			return 0, errForkKeyCycleLedgerNotReady
		}
		from, expected = intent.ProofFromMS, totalUsed-baseline
	} else {
		if intent != nil {
			return 0, errForkKeyCycleLedgerNotReady
		}
		if old.WindowStartedAtMS == nil || old.WindowEndsAtMS == nil || *old.WindowStartedAtMS < retention || *old.WindowEndsAtMS <= now.UnixMilli() || *old.WindowStartedAtMS > start {
			return 0, errForkKeyCycleLedgerNotReady
		}
		from, through = *old.WindowStartedAtMS, *old.WindowEndsAtMS
	}
	var logs []struct {
		CompletedAtMS        int64
		EstimatedCostNanoUSD int64
		DecisionCostNanoUSD  int64
		AuditCostNanoUSD     int64
	}
	if err := tx.Model(&models.RequestLog{}).Select("completed_at_ms", "estimated_cost_nano_usd", "decision_cost_nano_usd", "audit_cost_nano_usd").Where("access_key_id = ? AND completed_at_ms >= ? AND completed_at_ms < ?", id, min(from, start), max(through, end)).Find(&logs).Error; err != nil {
		return 0, err
	}
	var proof, seed pricing.NanoUSD
	for _, log := range logs {
		cost, ok := pricing.CheckedAddNanoUSD(pricing.NanoUSD(log.EstimatedCostNanoUSD), pricing.NanoUSD(log.DecisionCostNanoUSD))
		if !ok {
			return 0, errForkKeyCycleLedgerNotReady
		}
		cost, ok = pricing.CheckedAddNanoUSD(cost, pricing.NanoUSD(log.AuditCostNanoUSD))
		if !ok {
			return 0, errForkKeyCycleLedgerNotReady
		}
		if log.CompletedAtMS > now.UnixMilli() {
			return 0, errForkKeyCycleLedgerNotReady
		}
		if log.CompletedAtMS >= from && log.CompletedAtMS < through {
			proof, ok = pricing.CheckedAddNanoUSD(proof, cost)
			if !ok {
				return 0, errForkKeyCycleLedgerNotReady
			}
		}
		if log.CompletedAtMS >= start && log.CompletedAtMS < end {
			seed, ok = pricing.CheckedAddNanoUSD(seed, cost)
			if !ok {
				return 0, errForkKeyCycleLedgerNotReady
			}
		}
	}
	if int64(proof) != expected {
		return 0, errForkKeyCycleLedgerNotReady
	}
	return int64(seed), nil
}
