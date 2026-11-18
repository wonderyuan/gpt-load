package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
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
	"gpt-load/internal/subscription"
)

// Stored in the existing operation JSON BEFORE dispatch. This is evidence, not
// an instruction to spend another card. Retries must keep the original baseline.
type resetCreditSyncIntent struct {
	Identity        string             `json:"identity"`
	Target          string             `json:"target"`
	SyncAllowed     bool               `json:"sync_allowed"`
	ResetKind       string             `json:"reset_kind,omitempty"`
	DispatchMS      int64              `json:"dispatch_ms"`
	BeforeWindows   []quotaResetWindow `json:"before_windows"`
	AccessKeyIDs    []uint             `json:"access_key_ids,omitempty"`
	Legacy          bool               `json:"legacy,omitempty"`
	ProofFromMS     int64              `json:"proof_from_ms,omitempty"`
	TotalBaselines  map[uint]int64     `json:"total_baselines_nano_usd,omitempty"`
	ExpectedEndMS   int64              `json:"expected_end_ms,omitempty"`
	ReconcileDigest string             `json:"reconcile_digest,omitempty"`
}

func (s *Service) persistResetCreditSyncIntent(ctx context.Context, operation *models.CredentialResetOperation, group models.Group, credential models.Credential, kind string) error {
	var stored storedResetCreditRequest
	if len(operation.ResultJSON) != 0 {
		if err := json.Unmarshal(operation.ResultJSON, &stored); err != nil {
			return err
		}
		if stored.SyncIntent != nil {
			return nil
		}
		// An older ambiguous operation may already have dispatched. Do not invent a
		// pre-dispatch baseline from the current (possibly post-redemption) window.
		return nil
	}
	enabled := false
	if current := s.manager.Current(); current != nil {
		enabled = current.Groups[group.ID].AccountQuotaResetSyncEnabled
	}
	intent := &resetCreditSyncIntent{Identity: quotaResetIdentity(credential), Target: quotaResetTarget(group), SyncAllowed: enabled && groupOverrideBool(group, state.SettingAccountQuotaResetSyncEnabled), ResetKind: kind, DispatchMS: s.now().UnixMilli()}
	var observation models.CredentialObservation
	err := s.db.WithContext(ctx).Where("credential_id = ?", credential.ID).Take(&observation).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	value := mapCredentialObservation(observation)
	if observation.IdentityFingerprint == credential.IdentityFingerprint && quotaResetFresh(value) && *value.ObservedAtMS <= intent.DispatchMS && intent.DispatchMS-*value.ObservedAtMS <= int64(10*time.Minute/time.Millisecond) {
		intent.BeforeWindows = quotaResetWindows(value)
	}
	if intent.SyncAllowed && len(intent.BeforeWindows) > 0 {
		_ = s.captureResetCreditLedgerBaseline(ctx, group.ID, intent) // Redemption remains available; linkage without proof fails closed.
	}
	stored.SyncIntent = intent
	raw, err := json.Marshal(stored)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	update := s.db.WithContext(ctx).Model(&models.CredentialResetOperation{}).Where("idempotency_key = ? AND state = ? AND updated_at_ms = ?", operation.IdempotencyKey, models.CredentialResetOperationPrepared, operation.UpdatedAtMS).Update("result_json", models.JSON(raw))
	if update.Error != nil {
		return update.Error
	}
	if update.RowsAffected != 1 {
		return errors.New("reset credit operation changed before intent persistence")
	}
	operation.ResultJSON = models.JSON(raw)
	return nil
}

func (s *Service) captureResetCreditLedgerBaseline(ctx context.Context, groupID uint, intent *resetCreditSyncIntent) error {
	var ids []uint
	current := s.manager.Current()
	if current == nil {
		return errors.New("quota reset configuration unavailable")
	}
	for id, key := range current.AccessKeysByID {
		if key.Status != state.AccessKeyStatusActive || !quotaResetSingletonGroupMap(key.Filters.Groups, groupID) || key.ExpiresAtMS != nil && *key.ExpiresAtMS <= s.now().UnixMilli() {
			continue
		}
		for _, rule := range key.CostLimitRules {
			if rule.Kind == accessquota.KindPeriodic && slices.ContainsFunc(intent.BeforeWindows, func(window quotaResetWindow) bool {
				return window.UniquePeriod && window.Period == rule.PeriodSeconds && resetCreditPeriodAllowed(intent.ResetKind, window.Period)
			}) {
				ids = append(ids, id)
				break
			}
		}
	}
	slices.Sort(ids)
	if len(ids) == 0 {
		return nil
	}
	release, ok := s.quotaResetFence(ctx, ids)
	if !ok {
		return errors.New("quota reset baseline keys busy")
	}
	defer release()
	waiter, ok := s.requestLogs.(interface {
		AwaitSettled(context.Context, int64) error
	})
	if !ok {
		return errors.New("quota reset request log persistence unavailable")
	}
	settleCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := waiter.AwaitSettled(settleCtx, s.now().UnixMilli()); err != nil {
		return err
	}
	s.writeMu.RLock()
	defer s.writeMu.RUnlock()
	now := s.now()
	retention := now.Add(-time.Duration(current.Settings.RequestLogRetentionDays) * 24 * time.Hour).UnixMilli()
	from := intent.DispatchMS
	for _, window := range intent.BeforeWindows {
		if window.UniquePeriod && resetCreditPeriodAllowed(intent.ResetKind, window.Period) {
			from = min(from, window.EndMS-window.Period*1000)
		}
	}
	from = max(from, retention+1000)
	if current.Settings.RequestLogRetentionDays <= 0 || from > intent.DispatchMS {
		return errors.New("quota reset baseline outside retention")
	}
	baselines := make(map[uint]int64, len(ids))
	err := s.withReadSnapshot(ctx, func(tx *gorm.DB) error {
		for _, id := range ids {
			live := s.accessQuota.Snapshot(id, now)
			count, total := 0, int64(0)
			for _, rule := range live.Rules {
				if rule.Kind == accessquota.KindTotal {
					count++
					total = rule.UsedNanoUSD
				}
			}
			if count != 1 {
				return errors.New("quota reset baseline requires unique total rule")
			}
			sum, err := quotaResetLedgerSum(tx, id, from, now.UnixMilli()+1)
			if err != nil {
				return err
			}
			if sum > total {
				return errors.New("quota reset baseline total/log mismatch")
			}
			baselines[id] = total - sum
		}
		return nil
	})
	if err != nil {
		return err
	}
	intent.ProofFromMS, intent.TotalBaselines = from, baselines
	return nil
}

func resetCreditPeriodAllowed(kind string, period int64) bool {
	return kind == "" || kind == "week" && (period == 604800 || period == 18000) || kind == "five_hour" && period == 18000
}

// Event identity binds the non-refundable operation and real period, NEVER the
// provider's jittering reset timestamp. A pending event has no new end yet.
func resetCreditEvent(operation models.CredentialResetOperation, intent *resetCreditSyncIntent, old quotaResetWindow) quotaResetEvent {
	digest := quotaResetHash("reset-credit/v1/" + operation.IdempotencyKey + "/" + strconv.FormatInt(old.Period, 10))
	return quotaResetEvent{ID: quotaResetUUID(digest), Digest: digest, GroupID: operation.GroupID, CredentialID: operation.CredentialID, Identity: intent.Identity, Target: intent.Target, WindowKey: old.Key, Source: "reset_credit", Period: old.Period, OldEndMS: old.EndMS, Kind: "card", CreatedMS: intent.DispatchMS, UniquePeriod: old.UniquePeriod, SyncAllowed: intent.SyncAllowed}
}

func (s *Service) resetCreditOperationForEvent(ctx context.Context, event quotaResetEvent) (models.CredentialResetOperation, storedResetCreditResult, bool, error) {
	var operations []models.CredentialResetOperation
	if err := s.db.WithContext(ctx).Where("group_id = ? AND credential_id = ? AND state = ?", event.GroupID, event.CredentialID, models.CredentialResetOperationSucceeded).Find(&operations).Error; err != nil {
		return models.CredentialResetOperation{}, storedResetCreditResult{}, false, err
	}
	for _, operation := range operations {
		result, err := decodeStoredResetCreditResult(operation.ResultJSON)
		if err != nil || result.SyncIntent == nil {
			continue
		}
		for _, old := range result.SyncIntent.BeforeWindows {
			if resetCreditEvent(operation, result.SyncIntent, old).ID == event.ID {
				return operation, result, true, nil
			}
		}
	}
	return models.CredentialResetOperation{}, storedResetCreditResult{}, false, nil
}

func (s *Service) readyResetCreditEvent(ctx context.Context, event quotaResetEvent) (quotaResetEvent, []uint, string, error) {
	operation, result, found, err := s.resetCreditOperationForEvent(ctx, event)
	if err != nil {
		return event, nil, "", err
	}
	if !found || operation.CompletedAtMS == nil || result.WindowsReset <= 0 {
		return event, nil, "proof_missing", nil
	}
	intent := result.SyncIntent
	var credential models.Credential
	var group models.Group
	if err := s.db.WithContext(ctx).First(&credential, event.CredentialID).Error; err != nil {
		return event, nil, "identity_changed", nil
	}
	if err := s.db.WithContext(ctx).First(&group, event.GroupID).Error; err != nil {
		return event, nil, "target_changed", nil
	}
	if credential.GroupID != group.ID || quotaResetIdentity(credential) != intent.Identity || quotaResetTarget(group) != intent.Target {
		return event, nil, "identity_changed", nil
	}
	_, baseURL, err := s.resolveResetCreditTarget(group)
	if err != nil {
		return event, nil, "target_changed", nil
	}
	digest := resetCreditRequestDigest(group.ID, credential.ID, credential.IdentityFingerprint, baseURL, intent.ResetKind)
	if !bytes.Equal(digest[:], operation.RequestDigest) {
		return event, nil, "identity_changed", nil
	}
	var observation models.CredentialObservation
	if err := s.db.WithContext(ctx).Where("credential_id = ?", event.CredentialID).Take(&observation).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return event, nil, "pending_fresh", nil
		}
		return event, nil, "", err
	}
	fresh := mapCredentialObservation(observation)
	after := max(intent.DispatchMS, *operation.CompletedAtMS)
	if result.RedeemedAtMS != nil {
		after = max(after, *result.RedeemedAtMS)
	}
	nowMS := s.now().UnixMilli()
	if observation.IdentityFingerprint != credential.IdentityFingerprint || !quotaResetFresh(fresh) || *fresh.LastAttemptAtMS <= after || *fresh.ObservedAtMS > nowMS || nowMS-*fresh.ObservedAtMS > int64(10*time.Minute/time.Millisecond) {
		return event, nil, "pending_fresh", nil
	}
	var changed []quotaResetWindow
	for _, next := range quotaResetWindows(fresh) {
		if intent.ExpectedEndMS != 0 && !quotaResetSameAnchor(intent.ExpectedEndMS, next.EndMS) {
			continue
		}
		if !next.UniquePeriod || !resetCreditPeriodAllowed(intent.ResetKind, next.Period) || next.EndMS <= nowMS || next.EndMS-next.Period*1000 > nowMS || next.EndMS-next.Period*1000 > after+int64(24*time.Hour/time.Millisecond) {
			continue
		}
		for _, old := range intent.BeforeWindows {
			if old.Key == next.Key && old.UniquePeriod && old.Period == next.Period && old.ObservedMS <= intent.DispatchMS && (next.EndMS > old.EndMS+quotaResetToleranceMS || old.UsedPercent-next.UsedPercent >= 5 && next.UsedPercent <= 5) {
				changed = append(changed, next)
			}
		}
	}
	if len(changed) > result.WindowsReset {
		return event, nil, "ambiguous_period_sources", nil
	}
	for _, next := range changed {
		if next.Period == event.Period && next.Key == event.WindowKey {
			// Once ready, never retarget this operation to a later provider cycle.
			if event.EndMS != 0 && !quotaResetSameAnchor(event.EndMS, next.EndMS) {
				return event, nil, "outdated_event", nil
			}
			if event.EndMS == 0 {
				event.EndMS = next.EndMS
			}
			event.CreatedMS = *fresh.ObservedAtMS
			return event, intent.AccessKeyIDs, "", nil
		}
	}
	return event, nil, "pending_fresh", nil
}

// Only new operations with durable pre-dispatch intent participate automatically.
// Legacy rows are never guessed from a scan; the explicit admin route below is
// the sole opt-in for a bounded, explicitly scoped historical operation.
func (monitor *quotaResetMonitor) scanResetCreditOperations(ctx context.Context) error {
	var operations []models.CredentialResetOperation
	if err := monitor.service.db.WithContext(ctx).Where("state = ? AND result_json IS NOT NULL", models.CredentialResetOperationSucceeded).Find(&operations).Error; err != nil {
		return err
	}
	for _, operation := range operations {
		result, err := decodeStoredResetCreditResult(operation.ResultJSON)
		if err != nil || result.SyncIntent == nil || operation.CompletedAtMS == nil || result.WindowsReset <= 0 {
			continue
		}
		intent := result.SyncIntent
		if intent.Legacy {
			continue
		} // Explicit legacy proof is never a background instruction.
		for _, old := range intent.BeforeWindows {
			if !old.UniquePeriod || !resetCreditPeriodAllowed(intent.ResetKind, old.Period) {
				continue
			}
			event := resetCreditEvent(operation, intent, old)
			if _, exists, err := monitor.service.quotaResetReceipt(ctx, event); err != nil {
				return err
			} else if exists {
				continue
			}
			if slices.ContainsFunc(monitor.state.Events, func(previous quotaResetEvent) bool { return previous.ID == event.ID }) {
				continue
			}
			ready, _, reason, err := monitor.service.readyResetCreditEvent(ctx, event)
			if err != nil {
				return err
			}
			if reason == "" {
				event = ready
			}
			monitor.state.Events = append(monitor.state.Events, event)
		}
	}
	return nil
}

func (s *Service) cardResetOwnsWindow(ctx context.Context, event quotaResetEvent) bool {
	var operations []models.CredentialResetOperation
	if s.db.WithContext(ctx).Where("group_id = ? AND credential_id = ? AND state = ?", event.GroupID, event.CredentialID, models.CredentialResetOperationSucceeded).Find(&operations).Error != nil {
		return true
	}
	for _, operation := range operations {
		result, err := decodeStoredResetCreditResult(operation.ResultJSON)
		if err != nil || result.SyncIntent == nil {
			continue
		}
		for _, old := range result.SyncIntent.BeforeWindows {
			if old.Period != event.Period || old.Key != event.WindowKey || result.SyncIntent.Identity != event.Identity || result.SyncIntent.Target != event.Target {
				continue
			}
			card := resetCreditEvent(operation, result.SyncIntent, old)
			if receipt, exists, err := s.quotaResetReceipt(ctx, card); err != nil {
				return true
			} else if exists {
				for _, seed := range receipt.Seeds {
					if seed.Rule.PeriodSeconds == event.Period && seed.State.WindowEndsAtMS != nil && quotaResetSameAnchor(*seed.State.WindowEndsAtMS, event.EndMS) {
						return true
					}
				}
				continue
			}
			ready, _, reason, err := s.readyResetCreditEvent(ctx, card)
			if err != nil {
				return true
			}
			if reason == "pending_fresh" && operation.CompletedAtMS != nil && event.EndMS-event.Period*1000 <= *operation.CompletedAtMS+int64(24*time.Hour/time.Millisecond) && event.CreatedMS >= result.SyncIntent.DispatchMS {
				return true
			}
			if reason == "" && quotaResetSameAnchor(ready.EndMS, event.EndMS) {
				return true
			}
		}
	}
	return false
}

type ResetCreditReconcileRequest struct {
	GroupID             uint            `json:"group_id"`
	CredentialID        uint            `json:"credential_id"`
	AccessKeyIDs        []uint          `json:"access_key_ids"`
	ExpectedWindowEndMS int64           `json:"expected_window_end_ms"`
	ProofFromMS         int64           `json:"proof_from_ms"`
	TotalBaselinesUSD   map[uint]string `json:"total_baselines_usd"`
}

type ResetCreditReconcileResult struct {
	EventID     string               `json:"event_id"`
	WindowEndMS int64                `json:"window_end_ms"`
	Sync        quotaResetSyncResult `json:"sync"`
}

var errResetCreditReconcile = &app_errors.APIError{HTTPStatus: http.StatusConflict, Code: "RESET_CREDIT_RECONCILE_CONFLICT", Message: "Reconciliation requires a successful reset, unchanged identity/target, explicit dedicated keys and retained old-to-new window evidence"}

func resetCreditReconcileMutationLocator(c *gin.Context) string {
	id := c.Param("operationID")
	if validateIdempotencyKey(id) != nil {
		return "reset-credit:unknown"
	}
	return "reset-credit:" + id
}

func (server *Server) handleResetCreditReconcile(c *gin.Context) {
	var request ResetCreditReconcileRequest
	if err := bindStrictJSON(c, &request); err != nil {
		writeServiceError(c, "reset_credit_reconcile", app_errors.ErrValidation)
		return
	}
	result, err := server.service.ReconcileResetCredit(c.Request.Context(), c.Param("operationID"), request)
	if err != nil {
		writeServiceError(c, "reset_credit_reconcile", err)
		return
	}
	response.SuccessI18n(c, "common.success", result)
}

func (s *Service) ReconcileResetCredit(ctx context.Context, operationID string, request ResetCreditReconcileRequest) (ResetCreditReconcileResult, error) {
	if validateIdempotencyKey(operationID) != nil || request.GroupID == 0 || request.CredentialID == 0 || request.ExpectedWindowEndMS <= 0 || len(request.AccessKeyIDs) == 0 {
		return ResetCreditReconcileResult{}, app_errors.ErrValidation
	}
	ids := append([]uint(nil), request.AccessKeyIDs...)
	slices.Sort(ids)
	for index, id := range ids {
		if id == 0 || index > 0 && ids[index-1] == id {
			return ResetCreditReconcileResult{}, app_errors.ErrValidation
		}
	}
	request.AccessKeyIDs = ids
	requestRaw, _ := json.Marshal(request)
	reconcileDigest := quotaResetHash(string(requestRaw))
	s.resetCreditActionMu.Lock()
	defer s.resetCreditActionMu.Unlock()
	var operation models.CredentialResetOperation
	if err := s.db.WithContext(ctx).Where("idempotency_key = ?", operationID).Take(&operation).Error; err != nil {
		return ResetCreditReconcileResult{}, errResetCreditReconcile
	}
	result, err := decodeStoredResetCreditResult(operation.ResultJSON)
	if err != nil || operation.State != models.CredentialResetOperationSucceeded || operation.CompletedAtMS == nil || operation.GroupID != request.GroupID || operation.CredentialID != request.CredentialID || result.WindowsReset != 1 {
		return ResetCreditReconcileResult{}, errResetCreditReconcile
	}
	group, credential, _, err := s.loadObservationTarget(ctx, request.GroupID, request.CredentialID)
	if err != nil {
		return ResetCreditReconcileResult{}, errResetCreditReconcile
	}
	_, baseURL, err := s.resolveResetCreditTarget(group)
	if err != nil {
		return ResetCreditReconcileResult{}, errResetCreditReconcile
	}
	if result.SyncIntent == nil {
		// Explicit bounded compatibility only: no historical records are swept automatically.
		if operation.CreatedAtMS > s.now().UnixMilli() || s.now().UnixMilli()-operation.CreatedAtMS > int64(7*24*time.Hour/time.Millisecond) {
			return ResetCreditReconcileResult{}, errResetCreditReconcile
		}
		digest := resetCreditRequestDigest(group.ID, credential.ID, credential.IdentityFingerprint, baseURL)
		if !bytes.Equal(digest[:], operation.RequestDigest) || !groupOverrideBool(group, state.SettingAccountQuotaResetSyncEnabled) {
			return ResetCreditReconcileResult{}, errResetCreditReconcile
		}
		old, err := s.legacyResetCreditBeforeWindow(ctx, operation, credential, group, request.ExpectedWindowEndMS)
		if err != nil {
			return ResetCreditReconcileResult{}, err
		}
		if request.ProofFromMS <= 0 || request.ProofFromMS > operation.CreatedAtMS || len(request.TotalBaselinesUSD) != len(ids) {
			return ResetCreditReconcileResult{}, errResetCreditReconcile
		}
		baselines := make(map[uint]int64, len(ids))
		for _, id := range ids {
			value, ok := request.TotalBaselinesUSD[id]
			parsed, err := pricing.ParseUSD(value)
			if !ok || err != nil || parsed < 0 {
				return ResetCreditReconcileResult{}, errResetCreditReconcile
			}
			baselines[id] = int64(parsed)
		}
		result.SyncIntent = &resetCreditSyncIntent{Identity: quotaResetIdentity(credential), Target: quotaResetTarget(group), SyncAllowed: true, DispatchMS: operation.CreatedAtMS, BeforeWindows: []quotaResetWindow{old}, AccessKeyIDs: ids, Legacy: true, ProofFromMS: request.ProofFromMS, TotalBaselines: baselines, ExpectedEndMS: request.ExpectedWindowEndMS, ReconcileDigest: reconcileDigest}
		// Persist the explicit scope before making any native change. A restart can
		// recover it without inferring another history baseline or consuming a card.
		raw, _ := json.Marshal(result)
		s.writeMu.Lock()
		update := s.db.WithContext(ctx).Model(&models.CredentialResetOperation{}).Where("idempotency_key = ? AND state = ? AND result_json = ?", operationID, models.CredentialResetOperationSucceeded, operation.ResultJSON).Update("result_json", models.JSON(raw))
		s.writeMu.Unlock()
		if update.Error != nil || update.RowsAffected != 1 {
			return ResetCreditReconcileResult{}, errResetCreditReconcile
		}
		operation.ResultJSON = models.JSON(raw)
	}
	intent := result.SyncIntent
	if !slices.Equal(intent.AccessKeyIDs, ids) || !intent.SyncAllowed || intent.Legacy && intent.ReconcileDigest != reconcileDigest {
		return ResetCreditReconcileResult{}, errResetCreditReconcile
	}
	for _, old := range intent.BeforeWindows {
		event := resetCreditEvent(operation, intent, old)
		if previous, exists, err := s.quotaResetReceipt(ctx, event); err != nil {
			return ResetCreditReconcileResult{}, err
		} else if exists {
			recovered, recoverErr := s.syncQuotaReset(ctx, event)
			_ = previous
			return ResetCreditReconcileResult{EventID: event.ID, WindowEndMS: request.ExpectedWindowEndMS, Sync: recovered}, recoverErr
		}
		ready, _, reason, err := s.readyResetCreditEvent(ctx, event)
		if err != nil {
			return ResetCreditReconcileResult{}, err
		}
		if reason != "" || !quotaResetSameAnchor(ready.EndMS, request.ExpectedWindowEndMS) {
			continue
		}
		sync, err := s.syncQuotaReset(ctx, ready)
		if err != nil {
			return ResetCreditReconcileResult{}, err
		}
		if sync.Reason != "synced" && sync.Reason != "already_aligned" && sync.Reason != "already_processed" {
			return ResetCreditReconcileResult{EventID: event.ID, WindowEndMS: ready.EndMS, Sync: sync}, errResetCreditReconcile
		}
		return ResetCreditReconcileResult{EventID: event.ID, WindowEndMS: ready.EndMS, Sync: sync}, nil
	}
	return ResetCreditReconcileResult{}, errResetCreditReconcile
}

func (s *Service) legacyResetCreditBeforeWindow(ctx context.Context, operation models.CredentialResetOperation, credential models.Credential, group models.Group, expectedEnd int64) (quotaResetWindow, error) {
	identity := subscription.QuotaHistoryTargetIdentity(stateloader.CredentialIdentityGeneration(credential.IdentityFingerprint, group.ChannelID, string(group.ConnectionType), json.RawMessage(group.Params)))
	var oldRows []models.CredentialQuotaHistory
	if err := s.db.WithContext(ctx).Where("group_id = ? AND credential_id = ? AND target_identity = ? AND scope = ? AND window_seconds = ? AND used_basis_points = ? AND observed_at_ms <= ? AND observed_at_ms >= ?", group.ID, credential.ID, identity, "account", int64(604800), 10000, operation.CreatedAtMS, operation.CreatedAtMS-int64(24*time.Hour/time.Millisecond)).Order("observed_at_ms DESC").Find(&oldRows).Error; err != nil {
		return quotaResetWindow{}, err
	}
	for _, old := range oldRows {
		if old.ResetAtMS == nil || *old.ResetAtMS <= operation.CreatedAtMS || expectedEnd <= *old.ResetAtMS+quotaResetToleranceMS {
			continue
		}
		var after []models.CredentialQuotaHistory
		if err := s.db.WithContext(ctx).Where("group_id = ? AND credential_id = ? AND target_identity = ? AND window_key = ? AND observed_at_ms > ? AND reset_at_ms >= ? AND reset_at_ms <= ? AND used_basis_points = 0", group.ID, credential.ID, identity, old.WindowKey, *operation.CompletedAtMS, expectedEnd-quotaResetToleranceMS, expectedEnd+quotaResetToleranceMS).Find(&after).Error; err != nil {
			return quotaResetWindow{}, err
		}
		if len(after) == 0 {
			continue
		}
		var observation models.CredentialObservation
		if err := s.db.WithContext(ctx).Where("credential_id = ?", credential.ID).Take(&observation).Error; err != nil {
			return quotaResetWindow{}, errResetCreditReconcile
		}
		for _, current := range quotaResetWindows(mapCredentialObservation(observation)) {
			if current.Period == 604800 && current.Source == safeInternalErrorCode(old.SourceID) && current.UniquePeriod && quotaResetSameAnchor(current.EndMS, expectedEnd) {
				return quotaResetWindow{Key: current.Key, Source: current.Source, Period: current.Period, EndMS: *old.ResetAtMS, UsedPercent: 100, ObservedMS: old.ObservedAtMS, UniquePeriod: true}, nil
			}
		}
	}
	return quotaResetWindow{}, fmt.Errorf("%w", errResetCreditReconcile)
}
