package control

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"gpt-load/internal/accessquota"
	"gpt-load/internal/pricing"
	"gpt-load/internal/state"
	stateloader "gpt-load/internal/state/loader"
	"gpt-load/internal/storage/models"
)

type cycleRepairFixture struct {
	serviceFixture
	request ForkKeyCycleRepairRequest
	id      uint
	now     time.Time
	start   int64
}

func newCycleRepairFixture(t *testing.T) cycleRepairFixture {
	t.Helper()
	fixture, groupID, credentialID := newSubscriptionCredentialFixture(t)
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	start := time.Date(2026, 10, 28, 14, 35, 55, 0, time.UTC).UnixMilli()
	importedAt := time.UnixMilli(start).Add(-2 * time.Hour)
	fixture.service.now = func() time.Time { return importedAt }
	disabled := state.AccessKeyStatusDisabled
	created, err := fixture.service.CreateAccessKey(t.Context(), AccessKeyCreateRequest{Name: "cycle-repair-target", Key: fmt.Sprintf("sk-cycle-test-%d", testIdempotencySequence.Add(1)), Status: &disabled, Filters: &AccessKeyFilters{Groups: []uint{groupID}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&models.AccessKey{}).Where("id = ?", created.ID).Update("created_at_ms", importedAt.Add(-2*time.Hour).UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}
	request := forkQuotaImportRequest(importedAt)
	request.Rules[0].LimitUSD, request.Rules[0].UsedUSD = "9000", "8131.1893746"
	request.Rules[1].LimitUSD, request.Rules[1].UsedUSD = "150", "2"
	request.Rules[2].LimitUSD, request.Rules[2].UsedUSD = "350", "10"
	if _, err := fixture.service.ImportForkKeyQuota(t.Context(), created.ID, request); err != nil {
		t.Fatal(err)
	}
	fixture.service.now = func() time.Time { return now }
	// Populate a current authoritative observation without calling a provider.
	end, period, used, at := start+forkKeyWeeklySeconds*1000, forkKeyWeeklySeconds, float64(30), now.UnixMilli()
	var credential models.Credential
	if err := fixture.db.First(&credential, credentialID).Error; err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(CredentialObservationSnapshot{QuotaWindows: []ObservationQuotaWindow{{ID: "weekly", SourceID: "official", Scope: "account", Unit: "percent", Used: &used, ResetAtMS: &end, WindowSeconds: &period, State: "available"}}})
	if err := fixture.service.upsertCredentialObservation(t.Context(), models.CredentialObservation{CredentialID: credentialID, IdentityFingerprint: credential.IdentityFingerprint, SchemaVersion: 1, ObservationVersion: 1, State: models.CredentialObservationFresh, ObservedAtMS: &at, LastAttemptAtMS: &at, SnapshotJSON: models.JSON(payload), UpdatedAtMS: at}); err != nil {
		t.Fatal(err)
	}
	weekly := loadAccessKeyCostLimitRules(t, fixture, created.ID)[2]
	result := cycleRepairFixture{serviceFixture: fixture, id: created.ID, now: now, start: start, request: ForkKeyCycleRepairRequest{ExpectedName: "cycle-repair-target", GroupID: groupID, CredentialID: credentialID, RuleID: weekly.ID, ExpectedRuleRevision: weekly.RuleRevision, WindowEndMS: end, ImportedTotalUsedUSD: "8131.1893746"}}
	// All statuses/credentials count. Only completed_at determines cycle inclusion.
	result.addLog(t, "pre", start-1, "success", 100, 20, 3)
	result.addLog(t, "start", start, "error", 200, 30, 4)
	result.addLog(t, "middle", now.UnixMilli()-1, "incomplete", 300, 40, 5)
	result.addLog(t, "end-before", end-1, "canceled", 400, 50, 6)
	result.addLog(t, "end", end, "success", 500, 60, 7)
	return result
}

func (f cycleRepairFixture) addLog(t *testing.T, label string, at int64, status string, estimated, decision, audit int64) {
	t.Helper()
	row := models.RequestLog{ID: fmt.Sprintf("%s-%d", label, testIdempotencySequence.Add(1)), AccessKeyID: f.id, CompletedAtMS: at, CredentialID: 999,
		Status: status, EstimatedCostNanoUSD: estimated, DecisionCostNanoUSD: decision, AuditCostNanoUSD: audit, UsageState: "complete", CostState: "priced", PricingCompleteness: "complete"}
	if err := f.db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	// Direct runtime admissions are used solely to model settled native pricing;
	// manager admissions would (correctly) reject this disabled production key.
	ticket, allowed := f.accessQuota.Admit(f.id, f.now)
	if !allowed.Allowed {
		t.Fatal(allowed)
	}
	f.accessQuota.Complete(ticket, estimated+decision+audit)
}

func (f cycleRepairFixture) updateObservation(t *testing.T, mutate func(*models.CredentialObservation, *CredentialObservationSnapshot)) {
	t.Helper()
	var row models.CredentialObservation
	if err := f.db.Take(&row, "credential_id = ?", f.request.CredentialID).Error; err != nil {
		t.Fatal(err)
	}
	var snapshot CredentialObservationSnapshot
	if err := json.Unmarshal(row.SnapshotJSON, &snapshot); err != nil {
		t.Fatal(err)
	}
	mutate(&row, &snapshot)
	payload, _ := json.Marshal(snapshot)
	row.SnapshotJSON = models.JSON(payload)
	if err := f.db.Save(&row).Error; err != nil {
		t.Fatal(err)
	}
}

func TestForkKeyCycleRepairExactLedgerPreservesOtherRulesAndKeys(t *testing.T) {
	f := newCycleRepairFixture(t)
	// Allocate protected ID 10 in the isolated fixture and prove it is untouched.
	var key10 uint
	for i := 2; i <= 10; i++ {
		key10 = quotaResetTestKey(t, f.serviceFixture, []uint{f.request.GroupID}, state.AccessKeyStatusActive)
	}
	if key10 != 10 {
		t.Fatalf("key10=%d", key10)
	}
	before10 := f.accessQuota.Snapshot(key10, f.now)
	before := f.accessQuota.Snapshot(f.id, f.now)
	rules := loadAccessKeyCostLimitRules(t, f.serviceFixture, f.id)
	// Newly completed requests before invocation must also participate in proof.
	f.addLog(t, "new", f.now.UnixMilli(), "success", 600, 70, 8)
	before = f.accessQuota.Snapshot(f.id, f.now)
	result, err := f.service.RepairForkKeyCycle(t.Context(), f.id, f.request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Replayed || result.Status != state.AccessKeyStatusDisabled || result.AfterUsedUSD != "0.000001713" || result.RuleRevision != f.request.ExpectedRuleRevision+1 {
		t.Fatalf("result=%#v", result)
	}
	if result.LedgerProof.LogCount != 6 || result.LedgerProof.LifetimeLogsUsedUSD != "0.000002403" || result.LedgerProof.ImportedTotalUsedUSD != "8131.1893746" || result.WindowStartMS != f.start || result.BeforeUsedUSD == result.AfterUsedUSD {
		t.Fatalf("proof=%#v", result)
	}
	after := f.accessQuota.Snapshot(f.id, f.now)
	if !reflect.DeepEqual(before.Rules[0], after.Rules[0]) || !reflect.DeepEqual(before.Rules[1], after.Rules[1]) || after.Rules[2].UsedNanoUSD != 1713 || after.Rules[2].WindowGeneration != 1 || *after.Rules[2].WindowStartedAtMS != f.start {
		t.Fatalf("before=%#v after=%#v", before, after)
	}
	if !reflect.DeepEqual(before10, f.accessQuota.Snapshot(key10, f.now)) {
		t.Fatal("key10 changed")
	}
	afterRules := loadAccessKeyCostLimitRules(t, f.serviceFixture, f.id)
	for index := range rules {
		if index == 2 {
			rules[index].RuleRevision++
			rules[index].UpdatedAtMS = afterRules[index].UpdatedAtMS
		}
		if !reflect.DeepEqual(rules[index], afterRules[index]) {
			t.Fatalf("definition changed %d", index)
		}
	}
	var checkpoint models.AccessKeyCostLimitState
	if err := f.db.Take(&checkpoint, "rule_id = ?", f.request.RuleID).Error; err != nil {
		t.Fatal(err)
	}
	if checkpoint.UsedNanoUSD != 1713 || checkpoint.SnapshotVersion != 1 || checkpoint.RuleRevision != result.RuleRevision {
		t.Fatalf("checkpoint=%#v", checkpoint)
	}
	var receipt models.ControlOperation
	if err := f.db.Where("operation_kind = ?", forkKeyCycleRepairKind).Take(&receipt).Error; err != nil {
		t.Fatal(err)
	}
	if receipt.CompletedAtMS == nil || !bytes.Contains(receipt.CanonicalResult, []byte(`"ledger_proof"`)) || !bytes.Contains(receipt.CanonicalResult, []byte(`"before_used_usd"`)) {
		t.Fatalf("receipt=%#v", receipt)
	}
	var count int64
	f.db.Model(&models.RequestLog{}).Where("access_key_id = ?", f.id).Count(&count)
	if count != 6 {
		t.Fatal("repair changed request logs")
	}
}

func TestForkKeyCycleRepairGuardsHaveNoWrites(t *testing.T) {
	for _, name := range []string{"active", "snapshot-active", "inflight", "name", "all-groups", "multi-groups", "group", "credential", "identity", "target", "scope", "duplicate-window", "anchor", "expired", "stale", "partial", "revision", "db-definition", "runtime-definition", "missing-total", "wrong-baseline", "unflushed", "retention", "created-after", "earliest-after", "baseline-during-cycle", "missing-import-receipt"} {
		t.Run(name, func(t *testing.T) {
			f := newCycleRepairFixture(t)
			setDB := func(model any, where string, arg any, column string, value any) {
				t.Helper()
				if err := f.db.Model(model).Where(where, arg).Update(column, value).Error; err != nil {
					t.Fatal(err)
				}
			}
			switch name {
			case "active":
				active := state.AccessKeyStatusActive
				if _, err := f.service.UpdateAccessKey(t.Context(), f.id, AccessKeyUpdateRequest{Status: &active}); err != nil {
					t.Fatal(err)
				}
			case "snapshot-active":
				setDB(&models.AccessKey{}, "id = ?", f.id, "status", "active")
				if err := f.service.recoverCommittedRuntime(t.Context(), false); err != nil {
					t.Fatal(err)
				}
				setDB(&models.AccessKey{}, "id = ?", f.id, "status", "disabled")
			case "inflight":
				release, ok := f.manager.Concurrency().TryAcquireRequest(f.id, 0, 0)
				if !ok {
					t.Fatal("acquire")
				}
				defer release()
			case "name":
				f.request.ExpectedName = "another"
			case "all-groups":
				setDB(&models.AccessKey{}, "id = ?", f.id, "filters", models.JSON(`{"groups":[]}`))
			case "multi-groups":
				setDB(&models.AccessKey{}, "id = ?", f.id, "filters", models.JSON(fmt.Sprintf(`{"groups":[%d,999]}`, f.request.GroupID)))
			case "group":
				f.request.GroupID++
			case "credential":
				f.request.CredentialID++
			case "identity":
				f.updateObservation(t, func(row *models.CredentialObservation, _ *CredentialObservationSnapshot) {
					row.IdentityFingerprint = "wrong"
				})
			case "target":
				setDB(&models.Group{}, "id = ?", f.request.GroupID, "params", models.JSON(`{"base_url":"https://changed.invalid"}`))
			case "scope":
				f.updateObservation(t, func(_ *models.CredentialObservation, s *CredentialObservationSnapshot) {
					s.QuotaWindows[0].Scope = "model"
				})
			case "duplicate-window":
				f.updateObservation(t, func(_ *models.CredentialObservation, s *CredentialObservationSnapshot) {
					s.QuotaWindows = append(s.QuotaWindows, s.QuotaWindows[0])
				})
			case "anchor":
				f.request.WindowEndMS++
			case "expired":
				f.request.WindowEndMS = f.now.UnixMilli()
			case "stale":
				f.updateObservation(t, func(row *models.CredentialObservation, _ *CredentialObservationSnapshot) {
					at := f.now.Add(-11 * time.Minute).UnixMilli()
					row.ObservedAtMS = &at
					row.LastAttemptAtMS = &at
				})
			case "partial":
				f.updateObservation(t, func(row *models.CredentialObservation, _ *CredentialObservationSnapshot) {
					row.LastErrorCode = "observation_partial"
				})
			case "revision":
				f.request.ExpectedRuleRevision++
			case "db-definition":
				setDB(&models.AccessKeyCostLimitRule{}, "id = ?", f.request.RuleID, "limit_nano_usd", 123)
			case "runtime-definition":
				defs := f.manager.Current().AccessQuotaDefinitions()
				defs[f.id][2].LimitNanoUSD++
				if err := f.accessQuota.Reconcile(defs); err != nil {
					t.Fatal(err)
				}
			case "missing-total":
				rows := loadAccessKeyCostLimitRules(t, f.serviceFixture, f.id)
				if err := f.db.Delete(&models.AccessKeyCostLimitRule{}, rows[0].ID).Error; err != nil {
					t.Fatal(err)
				}
			case "wrong-baseline":
				f.request.ImportedTotalUsedUSD = "8131.189374601"
			case "unflushed":
				ticket, _ := f.accessQuota.Admit(f.id, f.now)
				f.accessQuota.Complete(ticket, 1)
			case "retention":
				input, err := stateloader.BuildCompileInputWithProxy(t.Context(), f.db, f.encryption, f.service.environmentProxy, f.channelRegistry)
				if err != nil {
					t.Fatal(err)
				}
				input.SystemSettings = map[string]any{state.SettingRequestLogRetentionDays: 1}
				if _, err := f.manager.Publish(input); err != nil {
					t.Fatal(err)
				}
			case "created-after":
				setDB(&models.AccessKey{}, "id = ?", f.id, "created_at_ms", f.start+1)
			case "earliest-after":
				if err := f.db.Where("access_key_id = ? AND completed_at_ms <= ?", f.id, f.start).Delete(&models.RequestLog{}).Error; err != nil {
					t.Fatal(err)
				}
			case "baseline-during-cycle":
				setDB(&models.ControlOperation{}, "operation_kind = ?", forkQuotaImportKind, "completed_at_ms", f.start+1)
			case "missing-import-receipt":
				if err := f.db.Where("operation_kind = ?", forkQuotaImportKind).Delete(&models.ControlOperation{}).Error; err != nil {
					t.Fatal(err)
				}
			}
			before := f.accessQuota.Snapshot(f.id, f.now)
			writes := 0
			if err := f.db.Callback().Create().Before("gorm:create").Register("repair:no_create", func(*gorm.DB) { writes++ }); err != nil {
				t.Fatal(err)
			}
			if err := f.db.Callback().Update().Before("gorm:update").Register("repair:no_update", func(*gorm.DB) { writes++ }); err != nil {
				t.Fatal(err)
			}
			_, err := f.service.RepairForkKeyCycle(t.Context(), f.id, f.request)
			if err == nil || writes != 0 || !reflect.DeepEqual(before, f.accessQuota.Snapshot(f.id, f.now)) {
				t.Fatalf("guard err=%v writes=%d", err, writes)
			}
			if slicesContains([]string{"wrong-baseline", "unflushed", "retention", "created-after", "earliest-after", "baseline-during-cycle", "missing-import-receipt"}, name) && !errors.Is(err, errForkKeyCycleLedgerNotReady) {
				t.Fatalf("ledger guard=%v", err)
			}
		})
	}
}

func slicesContains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func TestForkKeyCycleRepairReplayAfterEnableDoesNotReseed(t *testing.T) {
	f := newCycleRepairFixture(t)
	first, err := f.service.RepairForkKeyCycle(t.Context(), f.id, f.request)
	if err != nil {
		t.Fatal(err)
	}
	active := state.AccessKeyStatusActive
	if _, err := f.service.UpdateAccessKey(t.Context(), f.id, AccessKeyUpdateRequest{Status: &active}); err != nil {
		t.Fatal(err)
	}
	f.addLog(t, "after-repair", f.now.UnixMilli(), "success", 100, 200, 300)
	before := f.accessQuota.Snapshot(f.id, f.now)
	for range 2 {
		result, err := f.service.RepairForkKeyCycle(t.Context(), f.id, f.request)
		if err != nil || !result.Replayed || result.Status != active || result.AfterUsedUSD != first.AfterUsedUSD || !reflect.DeepEqual(before, f.accessQuota.Snapshot(f.id, f.now)) {
			t.Fatalf("replay=%#v %v", result, err)
		}
	}
	f.request.ExpectedName = "changed"
	if _, err := f.service.RepairForkKeyCycle(t.Context(), f.id, f.request); !errors.Is(err, errForkKeyCycleRepairConflict) {
		t.Fatalf("conflict=%v", err)
	}
}

func TestForkKeyCycleRepairPostCommitFailureAndRestart(t *testing.T) {
	for _, failedPublish := range []bool{false, true} {
		t.Run(fmt.Sprint(failedPublish), func(t *testing.T) {
			f := newCycleRepairFixture(t)
			if failedPublish {
				f.service.publishSnapshot = func(state.CompileInput) (*state.ConfigSnapshot, error) {
					return nil, errors.New("injected postcommit publish failure")
				}
			}
			result, err := f.service.RepairForkKeyCycle(t.Context(), f.id, f.request)
			if (err != nil) != failedPublish {
				t.Fatalf("result=%#v err=%v", result, err)
			}
			after := f.accessQuota.Snapshot(f.id, f.now)
			if after.Rules[2].UsedNanoUSD != 1035 || *after.Rules[2].WindowStartedAtMS != f.start || f.manager.Current().AccessKeysByID[f.id].Status != state.AccessKeyStatusDisabled {
				t.Fatalf("lost seed=%#v", after)
			}
			// In production the existing persister saves unaffected dirty rules.
			// Flush them here so restart also proves total/5h remain unchanged.
			for _, dirty := range f.accessQuota.DirtySnapshots(100) {
				checkpoint := models.AccessKeyCostLimitState{RuleID: dirty.RuleID, RuleRevision: dirty.RuleRevision, UsedNanoUSD: dirty.UsedNanoUSD, WindowStartedAtMS: dirty.WindowStartedAtMS, WindowEndsAtMS: dirty.WindowEndsAtMS, WindowGeneration: dirty.WindowGeneration, SnapshotVersion: dirty.SnapshotVersion}
				if err := f.db.Save(&checkpoint).Error; err != nil {
					t.Fatal(err)
				}
			}
			restarted := accessquota.NewRuntime()
			manager := state.NewManager()
			manager.SetSnapshotReconciler(controlAccessQuotaReconciler{runtime: restarted})
			if err := stateloader.NewWithAccessQuota(f.db, manager, state.NewCredentialRegistry(), restarted).Load(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got := restarted.Snapshot(f.id, f.now); !reflect.DeepEqual(got, after) {
				t.Fatalf("restart=%#v want=%#v", got, after)
			}
			f.service.publishSnapshot = f.manager.Publish
			if replay, err := f.service.RepairForkKeyCycle(t.Context(), f.id, f.request); err != nil || !replay.Replayed {
				t.Fatalf("replay=%#v err=%v", replay, err)
			}
		})
	}
}

func TestForkKeyCycleRepairRollbackNeverStagesSeed(t *testing.T) {
	f := newCycleRepairFixture(t)
	before := f.accessQuota.Snapshot(f.id, f.now)
	rules := loadAccessKeyCostLimitRules(t, f.serviceFixture, f.id)
	if err := f.db.Callback().Create().Before("gorm:create").Register("repair:fail_receipt", func(tx *gorm.DB) {
		if tx.Statement.Table == "control_operations" {
			tx.AddError(errors.New("injected receipt failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.RepairForkKeyCycle(t.Context(), f.id, f.request); err == nil {
		t.Fatal("rollback succeeded")
	}
	if !reflect.DeepEqual(before, f.accessQuota.Snapshot(f.id, f.now)) || !reflect.DeepEqual(rules, loadAccessKeyCostLimitRules(t, f.serviceFixture, f.id)) {
		t.Fatal("rollback changed quota")
	}
	if err := f.service.recoverCommittedRuntime(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, f.accessQuota.Snapshot(f.id, f.now)) {
		t.Fatal("uncommitted seed published")
	}
}

func TestForkKeyCycleRepairLedgerIntegerOverflow(t *testing.T) {
	for _, costs := range [][3]int64{{math.MaxInt64, 1, 0}, {1, -1, 0}, {1, 0, -1}} {
		f := newCycleRepairFixture(t)
		row := models.RequestLog{ID: fmt.Sprint(testIdempotencySequence.Add(1)), AccessKeyID: f.id, CompletedAtMS: f.now.UnixMilli(), Status: "error", EstimatedCostNanoUSD: costs[0], DecisionCostNanoUSD: costs[1], AuditCostNanoUSD: costs[2], UsageState: "complete", CostState: "priced", PricingCompleteness: "complete"}
		if err := f.db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := f.service.RepairForkKeyCycle(t.Context(), f.id, f.request); !errors.Is(err, errForkKeyCycleLedgerNotReady) {
			t.Fatalf("cost %v => %v", costs, err)
		}
	}
	if got, err := pricing.ParseUSD("8131.1893746"); err != nil || int64(got) != 8131189374600 {
		t.Fatal("baseline not exact")
	}
}

func TestForkKeyCycleRepairHTTPContract(t *testing.T) {
	fixture, engine := newAccessKeyCostLimitHTTPFixture(t)
	path := "/api/fork/access-keys/99999/cycle-repair"
	for _, body := range []string{`{}`, `{"expected_name":"x","extra":true}`, `{"expected_name":"x","expected_name":"y"}`, `{"expected_name":"x","imported_total_used_usd":1}`, `{"expected_name":"x","rule_id":1.5}`} {
		got := serveAccessKeyCostLimitRequest(t, engine, http.MethodPost, path, body, "")
		if got.Code != http.StatusBadRequest {
			t.Fatalf("body=%s got=%d %s", body, got.Code, got.Body.String())
		}
	}
	payload := `{"expected_name":"test","group_id":1,"credential_id":1,"rule_id":1,"expected_rule_revision":1,"window_end_ms":1793774155000,"imported_total_used_usd":"0"}`
	unauthorized := httptest.NewRecorder()
	engine.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, path, strings.NewReader(payload)))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("auth=%d", unauthorized.Code)
	}
	active := createForkQuotaTarget(t, fixture, state.AccessKeyStatusActive)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+active.Key)
	forbidden := httptest.NewRecorder()
	engine.ServeHTTP(forbidden, req)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("key auth=%d %s", forbidden.Code, forbidden.Body.String())
	}
	missing := serveAccessKeyCostLimitRequest(t, engine, http.MethodPost, path, payload, "")
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing=%d %s", missing.Code, missing.Body.String())
	}
}
