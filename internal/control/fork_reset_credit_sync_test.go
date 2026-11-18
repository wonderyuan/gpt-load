package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"gpt-load/internal/pricing"
	stateloader "gpt-load/internal/state/loader"
	"gpt-load/internal/subscription"
	"reflect"
	"testing"
	"time"

	"gpt-load/internal/state"
	"gpt-load/internal/storage/models"
)

func resetCreditSyncFixture(t *testing.T) (serviceFixture, quotaResetEvent, models.CredentialResetOperation, uint) {
	t.Helper()
	f, groupID, credentialID := newSubscriptionCredentialFixture(t)
	now := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	oldNow := now.Add(-24 * time.Hour)
	f.service.now = func() time.Time { return oldNow }
	quotaResetTestEnable(t, f, groupID, true)
	id := quotaResetTestKey(t, f, []uint{groupID}, state.AccessKeyStatusActive)
	f.service.now = func() time.Time { return now }
	var group models.Group
	var credential models.Credential
	if err := f.db.First(&group, groupID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.First(&credential, credentialID).Error; err != nil {
		t.Fatal(err)
	}
	// The real returned weekly period starts an hour ago. The key's native old
	// weekly counter still includes yesterday's imported/mismatched usage.
	start, end, period := now.Add(-time.Hour).UnixMilli(), now.Add(7*24*time.Hour-time.Hour).UnixMilli(), int64(604800)
	at, percent := now.UnixMilli(), float64(1)
	payload, _ := json.Marshal(CredentialObservationSnapshot{QuotaWindows: []ObservationQuotaWindow{{ID: "weekly", SourceID: "official", Scope: "account", Unit: "percent", Used: &percent, ResetAtMS: &end, WindowSeconds: &period, State: "available"}}})
	observation := models.CredentialObservation{CredentialID: credentialID, IdentityFingerprint: credential.IdentityFingerprint, SchemaVersion: 1, ObservationVersion: 10, State: models.CredentialObservationFresh, ObservedAtMS: &at, LastAttemptAtMS: &at, SnapshotJSON: payload, UpdatedAtMS: at}
	if err := f.service.upsertCredentialObservation(t.Context(), observation); err != nil {
		t.Fatal(err)
	}
	next := quotaResetWindows(mapCredentialObservation(observation))[0]
	before := next
	before.EndMS = oldNow.Add(7 * 24 * time.Hour).UnixMilli()
	before.ObservedMS = now.Add(-2 * time.Hour).UnixMilli()
	before.UsedPercent = 100
	dispatch, completed := now.Add(-90*time.Minute).UnixMilli(), now.Add(-89*time.Minute).UnixMilli()
	intent := &resetCreditSyncIntent{Identity: quotaResetIdentity(credential), Target: quotaResetTarget(group), SyncAllowed: true, DispatchMS: dispatch, BeforeWindows: []quotaResetWindow{before}, ProofFromMS: oldNow.UnixMilli(), TotalBaselines: map[uint]int64{id: 1_000_000_000}}
	// Complete answer+decision+audit in the current returned window. Total native
	// usage is 1 USD baseline + these exact retained costs, while old weekly usage
	// remains inconsistent with retained window-only logs (the live legacy case).
	ticket, allowed := f.accessQuota.Admit(id, now)
	if !allowed.Allowed {
		t.Fatal(allowed)
	}
	f.accessQuota.Complete(ticket, 123)
	log := models.RequestLog{ID: fmt.Sprintf("reset-sync-%d", testIdempotencySequence.Add(1)), AccessKeyID: id, CompletedAtMS: start + 1, EstimatedCostNanoUSD: 100, DecisionCostNanoUSD: 20, AuditCostNanoUSD: 3, Status: "success", UsageState: "complete", CostState: "priced", PricingCompleteness: "complete"}
	if err := f.db.Create(&log).Error; err != nil {
		t.Fatal(err)
	}
	digest := resetCreditRequestDigest(groupID, credentialID, credential.IdentityFingerprint, "")
	result, _ := json.Marshal(storedResetCreditResult{Status: "succeeded", WindowsReset: 1, RedeemedAtMS: &completed, SyncIntent: intent})
	operation := models.CredentialResetOperation{IdempotencyKey: resetCreditTestKey, RedeemRequestID: resetCreditTestKey, RequestDigest: digest[:], GroupID: groupID, CredentialID: credentialID, State: models.CredentialResetOperationSucceeded, CreatedAtMS: dispatch, UpdatedAtMS: completed, CompletedAtMS: &completed, ResultJSON: result}
	if err := f.db.Create(&operation).Error; err != nil {
		t.Fatal(err)
	}
	event := resetCreditEvent(operation, intent, before)
	return f, event, operation, id
}

func TestResetCreditSyncPreservesNewFeesReceiptAndDetectorDedup(t *testing.T) {
	f, event, _, id := resetCreditSyncFixture(t)
	before := f.accessQuota.Snapshot(id, f.service.now())
	result, err := f.service.syncQuotaReset(t.Context(), event)
	if err != nil || result.Reason != "synced" || result.Keys != 1 {
		t.Fatalf("sync=%#v %v", result, err)
	}
	after := f.accessQuota.Snapshot(id, f.service.now())
	if !reflect.DeepEqual(before.Rules[0], after.Rules[0]) || !reflect.DeepEqual(before.Rules[1], after.Rules[1]) || after.Rules[2].UsedNanoUSD != 123 || after.Rules[2].Revision != before.Rules[2].Revision+1 {
		t.Fatalf("before=%#v after=%#v", before, after)
	}
	ticket, _ := f.accessQuota.Admit(id, f.service.now())
	f.accessQuota.Complete(ticket, 17)
	if _, err := f.service.syncQuotaReset(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	if got := f.accessQuota.Snapshot(id, f.service.now()).Rules[2].UsedNanoUSD; got != 140 {
		t.Fatalf("replay lost new fees %d", got)
	}
	ready, _, reason, err := f.service.readyResetCreditEvent(t.Context(), event)
	if err != nil || reason != "" {
		t.Fatalf("ready=%v %v", reason, err)
	}
	ready.Kind = "sudden"
	ready.Digest = quotaResetHash("other detector source")
	ready.ID = quotaResetUUID(ready.Digest)
	result, err = f.service.syncQuotaReset(t.Context(), ready)
	if err != nil || result.Reason != "superseded_by_card" {
		t.Fatalf("duplicate %#v %v", result, err)
	}
	if got := f.accessQuota.Snapshot(id, f.service.now()).Rules[2].UsedNanoUSD; got != 140 {
		t.Fatal("detector lost fees")
	}
}

func TestResetCreditSyncPendingFreshAndRestart(t *testing.T) {
	f, event, operation, _ := resetCreditSyncFixture(t)
	f.service.quotaResetDataDir = t.TempDir()
	var observation models.CredentialObservation
	if err := f.db.Where("credential_id = ?", event.CredentialID).Take(&observation).Error; err != nil {
		t.Fatal(err)
	}
	observation.ObservedAtMS, observation.LastAttemptAtMS = &operation.CreatedAtMS, &operation.CreatedAtMS
	if err := f.db.Save(&observation).Error; err != nil {
		t.Fatal(err)
	}
	monitor, err := newQuotaResetMonitor(f.service)
	if err != nil {
		t.Fatal(err)
	}
	if err := monitor.flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(monitor.state.Events) != 1 || monitor.state.Events[0].DecisionDone || monitor.state.Events[0].SkipReason != "pending_fresh" {
		t.Fatalf("events=%#v", monitor.state.Events)
	}
	restored, err := newQuotaResetMonitor(f.service)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.state.Events) != 1 || restored.state.Events[0].ID != event.ID || restored.state.Events[0].DecisionDone {
		t.Fatalf("restored=%#v", restored.state.Events)
	}
	nowMS := f.service.now().UnixMilli()
	observation.ObservedAtMS, observation.LastAttemptAtMS = &nowMS, &nowMS
	if err := f.db.Save(&observation).Error; err != nil {
		t.Fatal(err)
	}
	restored.state.Events[0].DecisionNextMS = 0
	if err := restored.flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !restored.state.Events[0].DecisionDone || restored.state.Events[0].SyncedKeys != 1 {
		t.Fatalf("resumed=%#v", restored.state.Events)
	}
}

func TestResetCreditSyncMissingLedgerNeverClears(t *testing.T) {
	f, event, _, id := resetCreditSyncFixture(t)
	if err := f.db.Where("access_key_id = ?", id).Delete(&models.RequestLog{}).Error; err != nil {
		t.Fatal(err)
	}
	before := f.accessQuota.Snapshot(id, f.service.now())
	result, err := f.service.syncQuotaReset(t.Context(), event)
	if err != nil || result.Reason != "ledger_not_ready" || !reflect.DeepEqual(before, f.accessQuota.Snapshot(id, f.service.now())) {
		t.Fatalf("missing proof %#v %v", result, err)
	}
	var count int64
	if err := f.db.Model(&models.ControlOperation{}).Where("idempotency_key = ?", event.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("receipt count=%d %v", count, err)
	}
}

func TestResetCreditStablePeriodIDAndNoPreDispatchFresh(t *testing.T) {
	f, event, operation, _ := resetCreditSyncFixture(t)
	result, err := decodeStoredResetCreditResult(operation.ResultJSON)
	if err != nil {
		t.Fatal(err)
	}
	jitter := result.SyncIntent.BeforeWindows[0]
	jitter.EndMS += 30000
	if got := resetCreditEvent(operation, result.SyncIntent, jitter).ID; got != event.ID {
		t.Fatal("jitter changed operation-period identity")
	}
	var observation models.CredentialObservation
	if err := f.db.Where("credential_id = ?", event.CredentialID).Take(&observation).Error; err != nil {
		t.Fatal(err)
	}
	observation.LastAttemptAtMS = &operation.CreatedAtMS
	if err := f.db.Save(&observation).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, reason, err := f.service.readyResetCreditEvent(t.Context(), event); err != nil || reason != "pending_fresh" {
		t.Fatalf("used pre-dispatch observation %s %v", reason, err)
	}
}

func TestResetCreditLedgerStrictOldWindowProofAndNonzeroSeed(t *testing.T) {
	f, event, _, id := resetCreditSyncFixture(t)
	live := f.accessQuota.Snapshot(id, f.service.now()).Rules[2]
	if _, err := quotaResetLedgerSeed(f.db, id, live, 0, 0, nil, event.OldEndMS-event.Period*1000, event.OldEndMS, f.service.now(), 30); err == nil {
		t.Fatal("accepted unmatched old ledger")
	}
	if !resetCreditPeriodAllowed("week", 18000) || !resetCreditPeriodAllowed("week", 604800) || resetCreditPeriodAllowed("five_hour", 604800) {
		t.Fatal("reset kind period scope")
	}
}

func TestResetCreditLegacyExplicitReconcileAndHistoryGuards(t *testing.T) {
	f, event, operation, id := resetCreditSyncFixture(t)
	parsed, _ := decodeStoredResetCreditResult(operation.ResultJSON)
	intent := parsed.SyncIntent
	parsed.SyncIntent = nil
	raw, _ := json.Marshal(parsed)
	if err := f.db.Model(&models.CredentialResetOperation{}).Where("idempotency_key = ?", operation.IdempotencyKey).Update("result_json", models.JSON(raw)).Error; err != nil {
		t.Fatal(err)
	}
	var group models.Group
	var credential models.Credential
	if err := f.db.First(&group, event.GroupID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.First(&credential, event.CredentialID).Error; err != nil {
		t.Fatal(err)
	}
	identity := subscription.QuotaHistoryTargetIdentity(stateloader.CredentialIdentityGeneration(credential.IdentityFingerprint, group.ChannelID, string(group.ConnectionType), json.RawMessage(group.Params)))
	old := intent.BeforeWindows[0]
	period := int64(604800)
	var observation models.CredentialObservation
	if err := f.db.Where("credential_id = ?", event.CredentialID).Take(&observation).Error; err != nil {
		t.Fatal(err)
	}
	next := quotaResetWindows(mapCredentialObservation(observation))[0]
	request := ResetCreditReconcileRequest{GroupID: event.GroupID, CredentialID: event.CredentialID, AccessKeyIDs: []uint{id}, ExpectedWindowEndMS: next.EndMS, ProofFromMS: intent.ProofFromMS, TotalBaselinesUSD: map[uint]string{id: pricing.FormatUSD(pricing.NanoUSD(intent.TotalBaselines[id]))}}
	if _, err := f.service.ReconcileResetCredit(t.Context(), operation.IdempotencyKey, request); err == nil {
		t.Fatal("legacy accepted missing history")
	}
	rows := []models.CredentialQuotaHistory{
		{GroupID: event.GroupID, CredentialID: event.CredentialID, TargetIdentity: identity, WindowKey: "weekly", WindowID: "weekly", SourceID: "official", Scope: "account", Label: "Weekly", ObservedAtMS: old.ObservedMS, UsedBasisPoints: 10000, ResetAtMS: &old.EndMS, WindowSeconds: &period},
		{GroupID: event.GroupID, CredentialID: event.CredentialID, TargetIdentity: identity, WindowKey: "weekly", WindowID: "weekly", SourceID: "official", Scope: "account", Label: "Weekly", ObservedAtMS: *operation.CompletedAtMS + 1, UsedBasisPoints: 0, ResetAtMS: &next.EndMS, WindowSeconds: &period},
	}
	if err := f.db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	result, err := f.service.ReconcileResetCredit(t.Context(), operation.IdempotencyKey, request)
	if err != nil || result.Sync.Reason != "synced" || result.Sync.Keys != 1 {
		t.Fatalf("legacy=%#v %v", result, err)
	}
	if got := f.accessQuota.Snapshot(id, f.service.now()).Rules[2].UsedNanoUSD; got != 123 {
		t.Fatalf("legacy seed=%d", got)
	}
	ticket, _ := f.accessQuota.Admit(id, f.service.now())
	f.accessQuota.Complete(ticket, 7)
	changed := request
	changed.ExpectedWindowEndMS += 30000
	if _, err := f.service.ReconcileResetCredit(t.Context(), operation.IdempotencyKey, changed); err == nil {
		t.Fatal("accepted changed explicit proof")
	}
	result, err = f.service.ReconcileResetCredit(t.Context(), operation.IdempotencyKey, request)
	if err != nil || result.EventID != event.ID || f.accessQuota.Snapshot(id, f.service.now()).Rules[2].UsedNanoUSD != 130 {
		t.Fatalf("legacy replay=%#v %v", result, err)
	}
}

func TestResetCreditCommittedPublicationFailureRecoversSeed(t *testing.T) {
	f, event, _, id := resetCreditSyncFixture(t)
	publish := f.service.publishSnapshot
	before := f.accessQuota.Snapshot(id, f.service.now()).Rules[2]
	f.service.publishSnapshot = func(state.CompileInput) (*state.ConfigSnapshot, error) {
		if release, allowed := f.manager.Concurrency().TryAcquireRequest(id, 0, 0); allowed {
			release()
			t.Fatal("publication attempted without the admissions fence")
		}
		return nil, errors.New("injected publication failure")
	}
	if _, err := f.service.syncQuotaReset(t.Context(), event); err == nil {
		t.Fatal("expected original publication failure after successful recovery")
	}
	// Recovery must publish the committed nonzero seed BEFORE releasing the fence,
	// not wait for the next monitor cycle or receipt replay to change revisions.
	after := f.accessQuota.Snapshot(id, f.service.now()).Rules[2]
	if after.Revision != before.Revision+1 || after.UsedNanoUSD != 123 {
		t.Fatalf("fence released with unrecovered live rule: %#v", after)
	}
	release, allowed := f.manager.Concurrency().TryAcquireRequest(id, 0, 0)
	if !allowed {
		t.Fatal("successful recovery did not release the fence")
	}
	ticket, decision := f.accessQuota.Admit(id, f.service.now())
	if !decision.Allowed {
		release()
		t.Fatal(decision)
	}
	f.accessQuota.Complete(ticket, 17)
	release()
	f.service.publishSnapshot = publish
	result, err := f.service.syncQuotaReset(t.Context(), event)
	if err != nil || result.Keys != 1 {
		t.Fatalf("replay=%#v %v", result, err)
	}
	if got := f.accessQuota.Snapshot(id, f.service.now()).Rules[2].UsedNanoUSD; got != 140 {
		t.Fatalf("receipt replay overwrote post-fence fees: %d", got)
	}
}

func TestResetCreditOldReceiptDoesNotShadowNextNaturalPeriod(t *testing.T) {
	f, event, _, _ := resetCreditSyncFixture(t)
	ready, _, reason, err := f.service.readyResetCreditEvent(t.Context(), event)
	if err != nil || reason != "" {
		t.Fatal(reason, err)
	}
	if _, err := f.service.syncQuotaReset(t.Context(), ready); err != nil {
		t.Fatal(err)
	}
	ready.Kind = "natural"
	ready.EndMS += ready.Period * 1000
	ready.CreatedMS += ready.Period * 1000
	if f.service.cardResetOwnsWindow(t.Context(), ready) {
		t.Fatal("old card shadows next natural window")
	}
}

func TestResetCreditPreparationKeepsIntentAndSelection(t *testing.T) {
	f, event, _, _ := resetCreditSyncFixture(t)
	intent := &resetCreditSyncIntent{Identity: event.Identity, Target: event.Target, DispatchMS: f.service.now().UnixMilli(), SyncAllowed: true}
	raw, _ := json.Marshal(storedResetCreditRequest{SyncIntent: intent})
	op := models.CredentialResetOperation{IdempotencyKey: "8f0f4c32-89d2-4bcb-9e19-052940dc2f16", RedeemRequestID: "8f0f4c32-89d2-4bcb-9e19-052940dc2f16", GroupID: event.GroupID, CredentialID: event.CredentialID, State: models.CredentialResetOperationPrepared, UpdatedAtMS: f.service.now().UnixMilli(), ResultJSON: raw}
	if err := f.db.Create(&op).Error; err != nil {
		t.Fatal(err)
	}
	calls := 0
	prepare := func(context.Context) (json.RawMessage, error) {
		calls++
		return json.RawMessage(`{"card":"selected"}`), nil
	}
	request, err := f.service.prepareResetCreditRequest(t.Context(), op, prepare)
	if err != nil || string(request.Data) != `{"card":"selected"}` {
		t.Fatal(request, err)
	}
	if err := f.db.Where("idempotency_key = ?", op.IdempotencyKey).Take(&op).Error; err != nil {
		t.Fatal(err)
	}
	var stored storedResetCreditRequest
	if err := json.Unmarshal(op.ResultJSON, &stored); err != nil || stored.SyncIntent == nil || stored.SyncIntent.Identity != intent.Identity {
		t.Fatal("prepare overwrote sync intent", err)
	}
	if _, err := f.service.prepareResetCreditRequest(t.Context(), op, prepare); err != nil || calls != 1 {
		t.Fatal("retry selected another card", err, calls)
	}
}
