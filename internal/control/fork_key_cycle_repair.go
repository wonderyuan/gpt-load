package control

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"gpt-load/internal/accessquota"
	app_errors "gpt-load/internal/platform/errors"
	"gpt-load/internal/platform/response"
	"gpt-load/internal/pricing"
	"gpt-load/internal/state"
	stateloader "gpt-load/internal/state/loader"
	"gpt-load/internal/storage/models"
)

const (
	forkKeyCycleRepairKind       = "fork_key_cycle_repair"
	forkKeyWeeklySeconds   int64 = 604800
)

var (
	errForkKeyCycleRepairConflict = &app_errors.APIError{HTTPStatus: http.StatusConflict, Code: "CYCLE_REPAIR_CONFLICT", Message: "Cycle repair requires an unchanged, disabled, drained dedicated key and a fresh unique official weekly window"}
	errForkKeyCycleLedgerNotReady = &app_errors.APIError{HTTPStatus: http.StatusConflict, Code: "LEDGER_NOT_READY", Message: "The complete retained key ledger must exactly reconcile with native total usage minus the pre-cycle imported baseline"}
)

type ForkKeyCycleRepairRequest struct {
	ExpectedName         string `json:"expected_name"`
	GroupID              uint   `json:"group_id"`
	CredentialID         uint   `json:"credential_id"`
	RuleID               uint   `json:"rule_id"`
	ExpectedRuleRevision uint64 `json:"expected_rule_revision"`
	WindowEndMS          int64  `json:"window_end_ms"`
	ImportedTotalUsedUSD string `json:"imported_total_used_usd"`
}

type ForkKeyCycleLedgerProof struct {
	NativeTotalUsedUSD    string `json:"native_total_used_usd"`
	ImportedTotalUsedUSD  string `json:"imported_total_used_usd"`
	LifetimeLogsUsedUSD   string `json:"lifetime_logs_used_usd"`
	CycleLogsUsedUSD      string `json:"cycle_logs_used_usd"`
	LogCount              int64  `json:"log_count"`
	EarliestCompletedAtMS int64  `json:"earliest_completed_at_ms"`
	RetentionFromMS       int64  `json:"retention_from_ms"`
	BaselineImportedAtMS  *int64 `json:"baseline_imported_at_ms,omitempty"`
}

type ForkKeyCycleRepairResult struct {
	AccessKeyID   uint                     `json:"access_key_id"`
	Status        state.AccessKeyStatus    `json:"status"`
	RuleID        uint                     `json:"rule_id"`
	RuleRevision  uint64                   `json:"rule_revision"`
	WindowStartMS int64                    `json:"window_start_ms"`
	WindowEndMS   int64                    `json:"window_end_ms"`
	BeforeUsedUSD string                   `json:"before_used_usd,omitempty"`
	AfterUsedUSD  string                   `json:"after_used_usd,omitempty"`
	Replayed      bool                     `json:"replayed"`
	LedgerProof   *ForkKeyCycleLedgerProof `json:"ledger_proof,omitempty"`
	Quota         AccessKeyCostLimitStatus `json:"quota"`
}

func (s *Server) handleForkKeyCycleRepair(c *gin.Context) {
	id, ok := accessKeyID(c)
	if !ok {
		return
	}
	var request ForkKeyCycleRepairRequest
	if err := bindStrictJSON(c, &request); err != nil {
		writeServiceError(c, forkKeyCycleRepairKind, mapControlJSONError(err))
		return
	}
	result, err := s.service.RepairForkKeyCycle(c.Request.Context(), id, request)
	if err != nil {
		writeServiceError(c, forkKeyCycleRepairKind, err)
		return
	}
	response.SuccessI18n(c, "common.success", result)
}

// RepairForkKeyCycle is deliberately NOT a quota setter or reset. It replays
// only this key's retained, fully settled ledger into one official 7d window.
// No provider calls, credential refreshes, other rules or other keys are changed.
func (s *Service) RepairForkKeyCycle(ctx context.Context, id uint, request ForkKeyCycleRepairRequest) (ForkKeyCycleRepairResult, error) {
	baseline, err := pricing.ParseUSD(request.ImportedTotalUsedUSD)
	if err != nil || id == 0 || request.ExpectedName == "" || request.GroupID == 0 || request.CredentialID == 0 || request.RuleID == 0 || request.ExpectedRuleRevision == 0 || request.ExpectedRuleRevision == math.MaxUint64 || request.WindowEndMS <= forkKeyWeeklySeconds*1000 || request.WindowEndMS > maxSafeInteger {
		return ForkKeyCycleRepairResult{}, app_errors.ErrValidation
	}
	request.ImportedTotalUsedUSD = pricing.FormatUSD(baseline)
	body, _ := json.Marshal(request)
	digest := sha256.Sum256(body)
	identity := fmt.Sprintf("key:%d/rule:%d/end:%d", id, request.RuleID, request.WindowEndMS)
	receiptKey := forkQuotaReceiptKey(forkKeyCycleRepairKind + "/" + identity)
	result := ForkKeyCycleRepairResult{AccessKeyID: id, RuleID: request.RuleID, RuleRevision: request.ExpectedRuleRevision + 1,
		WindowStartMS: request.WindowEndMS - forkKeyWeeklySeconds*1000, WindowEndMS: request.WindowEndMS}
	if s.manager == nil || s.accessQuota == nil {
		return result, app_errors.ErrInternalServer
	}

	// Unlike writeConfig, this narrow writer NEVER invokes generic committed
	// recovery before the durable seed has been staged. Such recovery publishes a
	// changed revision with zero usage if the seed has not yet been installed.
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.enforceOperationRecoveryBarrierLocked(ctx, 0); err != nil {
		return result, err
	}
	var input state.CompileInput
	var install func() error
	err = s.withControlTransaction(ctx, func(tx *gorm.DB) error {
		var receipt models.ControlOperation
		err := tx.Where("idempotency_key = ?", receiptKey).Take(&receipt).Error
		if err == nil {
			if receipt.OperationKind != forkKeyCycleRepairKind || receipt.ResourceIdentity != identity || receipt.CompletedAtMS == nil || !bytes.Equal(receipt.RequestDigest, digest[:]) {
				return errForkKeyCycleRepairConflict
			}
			if len(receipt.CanonicalResult) != 0 && json.Unmarshal(receipt.CanonicalResult, &result) != nil {
				return app_errors.ErrInternalServer
			}
			result.Replayed = true
			// Receipt lookup precedes mutable guards. A retry after enable/expiry
			// returns the receipt, NEVER recomputes or resets already applied usage.
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return app_errors.ParseDBError(err)
		}
		var key models.AccessKey
		if err := tx.First(&key, id).Error; err != nil {
			return app_errors.ParseDBError(err)
		}
		current := s.manager.Current()
		if current == nil || key.Status != string(state.AccessKeyStatusDisabled) || key.Name != request.ExpectedName || s.manager.Concurrency().AccessKeyCurrent(id) != 0 {
			return errForkKeyCycleRepairConflict
		}
		view, exists := current.AccessKeysByID[id]
		filters, decodeErr := decodeStoredAccessKeyFilters(key.Filters)
		if decodeErr != nil || !exists || view.Status != state.AccessKeyStatusDisabled || view.Name != key.Name || !singletonConfiguredGroups(filters.Groups, request.GroupID) || !forkKeyQuotaFiltersMatch(filters, view.Filters) {
			return errForkKeyCycleRepairConflict
		}
		var group models.Group
		if err := tx.First(&group, request.GroupID).Error; err != nil {
			return app_errors.ParseDBError(err)
		}
		liveGroup, exists := current.Groups[group.ID]
		if !exists || !group.Enabled || !s.supportsCredentialObservation(group) || quotaResetTarget(group) == "" || quotaResetTarget(group) != quotaResetTarget(models.Group{ID: liveGroup.ID, ChannelID: string(liveGroup.ChannelID), ConnectionType: models.ConnectionType(liveGroup.ConnectionType), Params: models.JSON(liveGroup.Params)}) {
			return errForkKeyCycleRepairConflict
		}
		var accounts []models.Credential
		if err := tx.Where("group_id = ? AND status = ?", group.ID, models.CredentialStatusActive).Find(&accounts).Error; err != nil {
			return app_errors.ParseDBError(err)
		}
		if len(accounts) != 1 || accounts[0].ID != request.CredentialID || accounts[0].IdentityFingerprint == "" {
			return errForkKeyCycleRepairConflict
		}
		now := s.now()
		if err := forkKeyCycleOfficialWindow(tx, accounts[0], request, now); err != nil {
			return err
		}
		var rows []models.AccessKeyCostLimitRule
		if err := tx.Where("access_key_id = ?", id).Order("CASE WHEN kind = 'total' THEN 0 ELSE 1 END ASC, period_seconds ASC, id ASC").Find(&rows).Error; err != nil {
			return app_errors.ParseDBError(err)
		}
		definitions := make([]accessquota.Rule, 0, len(rows))
		var totalID uint
		var weekly accessquota.Rule
		totals, weeks := 0, 0
		for _, row := range rows {
			rule := accessquota.Rule{ID: row.ID, Revision: row.RuleRevision, Kind: accessquota.Kind(row.Kind), PeriodSeconds: row.PeriodSeconds, LimitNanoUSD: row.LimitNanoUSD}
			definitions = append(definitions, rule)
			if rule.Kind == accessquota.KindTotal {
				totals++
				totalID = rule.ID
			}
			if rule.Kind == accessquota.KindPeriodic && rule.PeriodSeconds == forkKeyWeeklySeconds {
				weeks++
				weekly = rule
			}
		}
		if totals != 1 || weeks != 1 || weekly.ID != request.RuleID || weekly.Revision != request.ExpectedRuleRevision || !slices.Equal(definitions, view.CostLimitRules) {
			return errForkKeyCycleRepairConflict
		}
		live := s.accessQuota.Snapshot(id, now)
		if len(live.Rules) != len(definitions) {
			return errForkKeyCycleRepairConflict
		}
		var nativeTotal int64
		for index, rule := range live.Rules {
			if rule.Rule != definitions[index] {
				return errForkKeyCycleRepairConflict
			}
			if rule.ID == totalID {
				nativeTotal = rule.UsedNanoUSD
			}
			if rule.ID == weekly.ID {
				result.BeforeUsedUSD = pricing.FormatUSD(pricing.NanoUSD(rule.UsedNanoUSD))
			}
		}
		proof, used, err := forkKeyCycleLedger(tx, key, nativeTotal, int64(baseline), result.WindowStartMS, result.WindowEndMS, now, current.Settings.RequestLogRetentionDays)
		if err != nil {
			return err
		}
		result.LedgerProof = &proof
		result.AfterUsedUSD = pricing.FormatUSD(pricing.NanoUSD(used))
		weekly.Revision++
		seed := accessquota.RestoredState{AccessKeyID: id, RuleID: weekly.ID, RuleRevision: weekly.Revision, UsedNanoUSD: used,
			WindowStartedAtMS: &result.WindowStartMS, WindowEndsAtMS: &result.WindowEndMS, WindowGeneration: 1, SnapshotVersion: 1}
		install, err = s.accessQuota.PreparePeriodicRebase(id, []accessquota.Rule{weekly}, []accessquota.RestoredState{seed})
		if err != nil {
			return errForkKeyCycleRepairConflict
		}
		updated := tx.Model(&models.AccessKeyCostLimitRule{}).Where("id = ? AND access_key_id = ? AND rule_revision = ?", weekly.ID, id, request.ExpectedRuleRevision).Update("rule_revision", weekly.Revision)
		if updated.Error != nil {
			return app_errors.ParseDBError(updated.Error)
		}
		if updated.RowsAffected != 1 {
			return errForkKeyCycleRepairConflict
		}
		checkpoint := models.AccessKeyCostLimitState{RuleID: weekly.ID, RuleRevision: weekly.Revision, UsedNanoUSD: used,
			WindowStartedAtMS: seed.WindowStartedAtMS, WindowEndsAtMS: seed.WindowEndsAtMS, WindowGeneration: 1, SnapshotVersion: 1}
		if err := tx.Save(&checkpoint).Error; err != nil {
			return app_errors.ParseDBError(err)
		}
		result.Status = state.AccessKeyStatusDisabled
		canonical, _ := json.Marshal(result)
		nowMS := now.UnixMilli()
		if err := tx.Create(&models.ControlOperation{OperationID: receiptKey, IdempotencyKey: receiptKey, DigestVersion: 1, RequestDigest: digest[:], OperationKind: forkKeyCycleRepairKind, ResourceIdentity: identity, CanonicalResult: canonical, CompletedAtMS: &nowMS}).Error; err != nil {
			return app_errors.ParseDBError(err)
		}
		input, err = stateloader.BuildCompileInputWithProxy(ctx, tx, s.encryption, s.environmentProxy, s.channelRegistry)
		if err != nil {
			return err
		}
		_, err = state.Compile(input)
		return err
	})
	if err != nil {
		return result, err
	}
	if result.Replayed {
		// A process may have stopped between commit and publication. Only the
		// exact old revision can need staging; applied/newer revisions are no-ops.
		if err := s.stageForkKeyCycleCheckpoint(ctx, id, request); err != nil {
			return result, err
		}
	} else {
		// Preparation succeeded under writeMu with disabled/drained key and exact
		// immutable definitions. The native installer does no I/O and only stages
		// that validated seed. No live rule can change in between these calls.
		if err := install(); err != nil {
			if retryErr := s.stageForkKeyCycleCheckpoint(ctx, id, request); retryErr != nil {
				return result, errors.Join(err, retryErr)
			}
		}
		if _, err := s.publishSnapshot(input); err != nil {
			// Seed is already staged: ordinary committed recovery is now safe.
			return result, joinCommittedRuntimeRecovery(newControlOperationError(stagePublishCommittedSnapshot), s.recoverCommittedRuntime(context.WithoutCancel(ctx), false))
		}
	}
	if current := s.manager.Current(); current != nil {
		result.Status = current.AccessKeysByID[id].Status
	}
	result.Quota = mapAccessKeyCostLimitStatus(s.accessQuota.Snapshot(id, s.now()))
	return result, nil
}

func forkKeyCycleOfficialWindow(tx *gorm.DB, account models.Credential, request ForkKeyCycleRepairRequest, now time.Time) error {
	var observation models.CredentialObservation
	if err := tx.Where("credential_id = ?", account.ID).Take(&observation).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errForkKeyCycleRepairConflict
		}
		return app_errors.ParseDBError(err)
	}
	value := mapCredentialObservation(observation)
	if observation.IdentityFingerprint != account.IdentityFingerprint || !quotaResetFresh(value) || *value.ObservedAtMS > now.UnixMilli() || now.UnixMilli()-*value.ObservedAtMS > int64(10*time.Minute/time.Millisecond) || request.WindowEndMS <= now.UnixMilli() || request.WindowEndMS-forkKeyWeeklySeconds*1000 > now.UnixMilli() {
		return errForkKeyCycleRepairConflict
	}
	count := 0
	for _, window := range value.Snapshot.QuotaWindows {
		if window.WindowSeconds == nil || *window.WindowSeconds != forkKeyWeeklySeconds {
			continue
		}
		count++
		if window.Scope != "account" || len(window.ModelIDs) != 0 || window.ID == "" || (window.State != "available" && window.State != "exhausted") || window.ResetAtMS == nil || *window.ResetAtMS != request.WindowEndMS {
			return errForkKeyCycleRepairConflict
		}
	}
	if count != 1 {
		return errForkKeyCycleRepairConflict
	}
	return nil
}

// Read individual integer columns rather than SQL SUM(a+b+c): SQLite can promote
// overflowing expressions to REAL. Every component and accumulation is checked
// as nonnegative int64. Lifetime proof has NO status/time/credential filter.
func forkKeyCycleLedger(tx *gorm.DB, key models.AccessKey, nativeTotal, baseline, start, end int64, now time.Time, retentionDays int) (ForkKeyCycleLedgerProof, int64, error) {
	proof := ForkKeyCycleLedgerProof{NativeTotalUsedUSD: pricing.FormatUSD(pricing.NanoUSD(nativeTotal)), ImportedTotalUsedUSD: pricing.FormatUSD(pricing.NanoUSD(baseline)), RetentionFromMS: max(int64(0), now.Add(-time.Duration(retentionDays)*24*time.Hour).UnixMilli())}
	fail := func() (ForkKeyCycleLedgerProof, int64, error) { return proof, 0, errForkKeyCycleLedgerNotReady }
	if retentionDays <= 0 || baseline > nativeTotal || key.CreatedAtMS > start || start < proof.RetentionFromMS {
		return fail()
	}
	if baseline != 0 {
		// The imported stock is excluded from the new weekly seed ONLY when it
		// was installed before this official cycle. Otherwise its cycle share is
		// unknown, even if the lifetime arithmetic happens to balance perfectly.
		var imported models.ControlOperation
		err := tx.Where("idempotency_key = ?", forkQuotaReceiptKey(fmt.Sprintf("access-key:%d", key.ID))).Take(&imported).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fail()
		}
		if err != nil {
			return proof, 0, app_errors.ParseDBError(err)
		}
		if imported.OperationKind != forkQuotaImportKind || imported.ResourceIdentity != fmt.Sprintf("access-key:%d", key.ID) || imported.CompletedAtMS == nil || *imported.CompletedAtMS > start {
			return fail()
		}
		proof.BaselineImportedAtMS = imported.CompletedAtMS
	}
	rows, err := tx.Model(&models.RequestLog{}).Select("completed_at_ms", "estimated_cost_nano_usd", "decision_cost_nano_usd", "audit_cost_nano_usd").Where("access_key_id = ?", key.ID).Rows()
	if err != nil {
		return proof, 0, app_errors.ParseDBError(err)
	}
	defer rows.Close()
	var lifetime, cycle pricing.NanoUSD
	for rows.Next() {
		var at, estimated, decision, audit int64
		if err := rows.Scan(&at, &estimated, &decision, &audit); err != nil {
			return proof, 0, app_errors.ParseDBError(err)
		}
		cost, ok := pricing.CheckedAddNanoUSD(pricing.NanoUSD(estimated), pricing.NanoUSD(decision))
		if !ok {
			return fail()
		}
		cost, ok = pricing.CheckedAddNanoUSD(cost, pricing.NanoUSD(audit))
		if !ok {
			return fail()
		}
		lifetime, ok = pricing.CheckedAddNanoUSD(lifetime, cost)
		if !ok {
			return fail()
		}
		if at >= start && at < end {
			cycle, ok = pricing.CheckedAddNanoUSD(cycle, cost)
			if !ok {
				return fail()
			}
		}
		if proof.LogCount == 0 || at < proof.EarliestCompletedAtMS {
			proof.EarliestCompletedAtMS = at
		}
		proof.LogCount++
	}
	if err := rows.Err(); err != nil {
		return proof, 0, app_errors.ParseDBError(err)
	}
	if proof.LogCount == 0 || proof.EarliestCompletedAtMS > start || nativeTotal-baseline != int64(lifetime) {
		return fail()
	}
	proof.LifetimeLogsUsedUSD, proof.CycleLogsUsedUSD = pricing.FormatUSD(lifetime), pricing.FormatUSD(cycle)
	return proof, int64(cycle), nil
}

// Caller holds writeMu. Only recover an unapplied adjacent revision from its
// durable checkpoint. Never feed an old seed into an applied/newer live rule.
func (s *Service) stageForkKeyCycleCheckpoint(ctx context.Context, id uint, request ForkKeyCycleRepairRequest) error {
	for _, live := range s.accessQuota.Snapshot(id, s.now()).Rules {
		if live.ID != request.RuleID {
			continue
		}
		if live.Revision >= request.ExpectedRuleRevision+1 {
			return nil
		}
		if live.Revision != request.ExpectedRuleRevision {
			return errForkKeyCycleRepairConflict
		}
		var row models.AccessKeyCostLimitRule
		var checkpoint models.AccessKeyCostLimitState
		if err := s.db.WithContext(ctx).Where("id = ? AND access_key_id = ?", request.RuleID, id).Take(&row).Error; err != nil {
			return app_errors.ParseDBError(err)
		}
		if err := s.db.WithContext(ctx).Where("rule_id = ?", request.RuleID).Take(&checkpoint).Error; err != nil {
			return app_errors.ParseDBError(err)
		}
		if row.RuleRevision != request.ExpectedRuleRevision+1 || checkpoint.RuleRevision != row.RuleRevision || checkpoint.WindowStartedAtMS == nil || checkpoint.WindowEndsAtMS == nil || *checkpoint.WindowStartedAtMS != request.WindowEndMS-forkKeyWeeklySeconds*1000 || *checkpoint.WindowEndsAtMS != request.WindowEndMS || checkpoint.WindowGeneration != 1 || checkpoint.SnapshotVersion != 1 {
			return errForkKeyCycleRepairConflict
		}
		install, err := s.accessQuota.PreparePeriodicRebase(id, []accessquota.Rule{{ID: row.ID, Revision: row.RuleRevision, Kind: accessquota.Kind(row.Kind), LimitNanoUSD: row.LimitNanoUSD, PeriodSeconds: row.PeriodSeconds}}, []accessquota.RestoredState{{AccessKeyID: id, RuleID: row.ID, RuleRevision: row.RuleRevision, UsedNanoUSD: checkpoint.UsedNanoUSD, WindowStartedAtMS: checkpoint.WindowStartedAtMS, WindowEndsAtMS: checkpoint.WindowEndsAtMS, WindowGeneration: 1, SnapshotVersion: 1}})
		if err != nil {
			return err
		}
		if err := install(); err != nil {
			return err
		}
		return s.recoverCommittedRuntime(context.WithoutCancel(ctx), false)
	}
	// An already deleted rule/key is also a valid terminal replay; no resurrection.
	return nil
}
