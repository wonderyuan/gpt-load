package control

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"gpt-load/internal/accessquota"
	"gpt-load/internal/state"
	stateloader "gpt-load/internal/state/loader"
	"gpt-load/internal/storage/models"
)

func forkQuotaImportRequest(now time.Time) ForkKeyQuotaImportRequest {
	start := now.Add(-time.Hour).UnixMilli()
	end5h := start + 18000*1000
	end7d := start + 604800*1000
	return ForkKeyQuotaImportRequest{Rules: []ForkKeyQuotaImportRule{
		{Kind: accessquota.KindTotal, LimitUSD: "100", UsedUSD: "70.123456789"},
		{Kind: accessquota.KindPeriodic, LimitUSD: "10", UsedUSD: "12", PeriodSeconds: 18000, WindowStartedAtMS: &start, WindowEndsAtMS: &end5h},
		{Kind: accessquota.KindPeriodic, LimitUSD: "30", UsedUSD: "20", PeriodSeconds: 604800, WindowStartedAtMS: &start, WindowEndsAtMS: &end7d},
	}}
}

func createForkQuotaTarget(t *testing.T, fixture serviceFixture, status state.AccessKeyStatus) AccessKeyCreateResult {
	t.Helper()
	created, err := fixture.service.CreateAccessKey(t.Context(), AccessKeyCreateRequest{Name: "migration", Status: &status, Key: fmt.Sprintf("sk-migration-test-%d", testIdempotencySequence.Add(1))})
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func TestForkKeyQuotaImportPersistsPublishesRestoresAndReplays(t *testing.T) {
	fixture := newServiceFixture(t)
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	fixture.service.now = func() time.Time { return now }
	target := createForkQuotaTarget(t, fixture, state.AccessKeyStatusDisabled)
	request := forkQuotaImportRequest(now)
	result, err := fixture.service.ImportForkKeyQuota(t.Context(), target.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Replayed || result.Quota.Allowed || len(result.Quota.Rules) != 3 || result.Quota.Rules[1].UsedUSD != "12" || result.Quota.Rules[0].UsedUSD != "70.123456789" {
		t.Fatalf("result = %#v", result)
	}
	if key := fixture.manager.Current().AccessKeysByID[target.ID]; key.Status != state.AccessKeyStatusDisabled || len(key.CostLimitRules) != 3 {
		t.Fatalf("published target = %#v", key)
	}
	if len(fixture.manager.Current().AccessKeysByHash) != 0 {
		t.Fatal("import enabled the target")
	}
	if decision := fixture.accessQuota.Check(target.ID, now); decision.Allowed || !decision.Recoverable || *decision.NextAvailableAtMS != *request.Rules[1].WindowEndsAtMS {
		t.Fatalf("native check = %#v", decision)
	}
	rules := loadAccessKeyCostLimitRules(t, fixture, target.ID)
	var persisted []models.AccessKeyCostLimitState
	if err := fixture.db.Order("rule_id ASC").Find(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if len(persisted) != 3 || persisted[1].UsedNanoUSD != 12_000_000_000 || *persisted[1].WindowStartedAtMS != *request.Rules[1].WindowStartedAtMS || persisted[1].SnapshotVersion != 1 {
		t.Fatalf("persisted = %#v", persisted)
	}
	// Exercise the real startup loader, rather than reconstructing counters from
	// the request. Imported checkpoints must survive a process restart exactly.
	restarted := accessquota.NewRuntime()
	manager := state.NewManager()
	manager.SetSnapshotReconciler(controlAccessQuotaReconciler{runtime: restarted})
	loader := stateloader.NewWithAccessQuota(fixture.db, manager, state.NewCredentialRegistry(), restarted)
	if err := loader.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, want := restarted.Snapshot(target.ID, now), fixture.accessQuota.Snapshot(target.ID, now); !reflect.DeepEqual(got, want) {
		t.Fatalf("restored = %#v, want %#v", got, want)
	}
	// Canonical order/decimal spelling are natural-idempotent.
	request.Rules[0].LimitUSD = "100.000"
	request.Rules[0], request.Rules[2] = request.Rules[2], request.Rules[0]
	replay, err := fixture.service.ImportForkKeyQuota(t.Context(), target.ID, request)
	if err != nil || !replay.Replayed || replay.Fingerprint != result.Fingerprint {
		t.Fatalf("replay = %#v, %v", replay, err)
	}
	// Renewal and billing are not undone by an old replay, even after receipt
	// compaction. No external reset or alignment occurs at import time.
	now = time.UnixMilli(*request.Rules[1].WindowEndsAtMS).Add(time.Minute)
	ticket, decision := fixture.accessQuota.Admit(target.ID, now)
	if !decision.Allowed {
		t.Fatal(decision)
	}
	fixture.accessQuota.Complete(ticket, 1_000_000_000)
	beforeReplay := fixture.accessQuota.Snapshot(target.ID, now)
	now = now.Add(operationResultRetention + time.Hour)
	if _, err := fixture.service.compactCompletedOperationsLocked(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	replay, err = fixture.service.ImportForkKeyQuota(t.Context(), target.ID, request)
	if err != nil || !replay.Replayed {
		t.Fatalf("compacted replay = %#v, %v", replay, err)
	}
	if got := fixture.accessQuota.Snapshot(target.ID, time.UnixMilli(beforeReplay.ObservedAtMS)); !reflect.DeepEqual(got, beforeReplay) {
		t.Fatal("replay reset native counters or periods")
	}
	if got := loadAccessKeyCostLimitRules(t, fixture, target.ID); !reflect.DeepEqual(got, rules) {
		t.Fatal("replay changed rule IDs or revisions")
	}
	request.Rules[0].UsedUSD = "21"
	if _, err := fixture.service.ImportForkKeyQuota(t.Context(), target.ID, request); !errors.Is(err, errForkQuotaImportConflict) {
		t.Fatalf("conflicting import = %v", err)
	}
}

func TestForkKeyQuotaImportGuardsAndRollback(t *testing.T) {
	for _, name := range []string{"active", "inflight", "existing rules", "transaction rollback", "runtime already occupied"} {
		t.Run(name, func(t *testing.T) {
			fixture := newServiceFixture(t)
			now := time.Now().UTC()
			fixture.service.now = func() time.Time { return now }
			status := state.AccessKeyStatusDisabled
			if name == "active" {
				status = state.AccessKeyStatusActive
			}
			target := createForkQuotaTarget(t, fixture, status)
			switch name {
			case "inflight":
				release, ok := fixture.manager.Concurrency().TryAcquireRequest(target.ID, 0, 0)
				if !ok {
					t.Fatal("failed to acquire test request")
				}
				defer release()
			case "existing rules":
				_, err := fixture.service.UpdateAccessKey(t.Context(), target.ID, AccessKeyUpdateRequest{CostLimitRules: OptionalAccessKeyCostLimitRules{Set: true, Values: []AccessKeyCostLimitRuleRequest{{Kind: accessquota.KindTotal, LimitUSD: "5"}}}})
				if err != nil {
					t.Fatal(err)
				}
			case "transaction rollback":
				if err := fixture.db.Callback().Create().Before("gorm:create").Register("test:reject_quota_receipt", func(tx *gorm.DB) {
					if tx.Statement.Table == "control_operations" {
						tx.AddError(errors.New("injected receipt insert failure"))
					}
				}); err != nil {
					t.Fatal(err)
				}
			case "runtime already occupied":
				if err := fixture.accessQuota.Reconcile(map[uint][]accessquota.Rule{target.ID: {{ID: 9999, Revision: 1, Kind: accessquota.KindTotal, LimitNanoUSD: 1}}}); err != nil {
					t.Fatal(err)
				}
			}
			before := fixture.accessQuota.Snapshot(target.ID, now)
			if _, err := fixture.service.ImportForkKeyQuota(t.Context(), target.ID, forkQuotaImportRequest(now)); err == nil {
				t.Fatal("unsafe import succeeded")
			}
			if got := fixture.accessQuota.Snapshot(target.ID, now); !reflect.DeepEqual(got, before) {
				t.Fatal("failed import mutated runtime")
			}
			var count int64
			fixture.db.Model(&models.AccessKeyCostLimitRule{}).Where("access_key_id = ?", target.ID).Count(&count)
			want := int64(0)
			if name == "existing rules" {
				want = 1
			}
			if count != want {
				t.Fatalf("rules after rollback = %d, want %d", count, want)
			}
			fixture.db.Model(&models.AccessKeyCostLimitState{}).Count(&count)
			if count != want {
				t.Fatalf("states after rollback = %d, want %d", count, want)
			}
			fixture.db.Model(&models.ControlOperation{}).Where("operation_kind = ?", forkQuotaImportKind).Count(&count)
			if count != 0 {
				t.Fatal("failed import persisted receipt")
			}
		})
	}
}

func TestForkKeyQuotaImportConcurrentReplayAndEnable(t *testing.T) {
	fixture := newServiceFixture(t)
	now := time.Now().UTC()
	fixture.service.now = func() time.Time { return now }
	target := createForkQuotaTarget(t, fixture, state.AccessKeyStatusDisabled)
	request := forkQuotaImportRequest(now)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := fixture.service.ImportForkKeyQuota(t.Context(), target.ID, request); err != nil {
				t.Errorf("concurrent replay: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := loadAccessKeyCostLimitRules(t, fixture, target.ID); len(got) != 3 {
		t.Fatalf("concurrent rules = %#v", got)
	}
	before := fixture.accessQuota.Snapshot(target.ID, now)
	active := state.AccessKeyStatusActive
	if _, err := fixture.service.UpdateAccessKey(t.Context(), target.ID, AccessKeyUpdateRequest{Status: &active}); err != nil {
		t.Fatal(err)
	}
	if got := fixture.accessQuota.Snapshot(target.ID, now); !reflect.DeepEqual(got, before) {
		t.Fatal("enable reset imported quotas")
	}
	if _, err := fixture.service.ImportForkKeyQuota(t.Context(), target.ID, request); !errors.Is(err, errForkQuotaImportConflict) {
		t.Fatalf("active target replay = %v", err)
	}
}

func TestForkKeyQuotaImportCommittedPublicationFailureDoesNotLoseState(t *testing.T) {
	fixture := newServiceFixture(t)
	now := time.Now().UTC()
	fixture.service.now = func() time.Time { return now }
	target := createForkQuotaTarget(t, fixture, state.AccessKeyStatusDisabled)
	fixture.service.publishSnapshot = func(state.CompileInput) (*state.ConfigSnapshot, error) {
		return nil, errors.New("injected post-commit publication failure")
	}
	request := forkQuotaImportRequest(now)
	if _, err := fixture.service.ImportForkKeyQuota(t.Context(), target.ID, request); err == nil {
		t.Fatal("publication error not reported")
	}
	if len(fixture.manager.Current().AccessKeysByID[target.ID].CostLimitRules) != 3 || fixture.accessQuota.Check(target.ID, now).Allowed {
		t.Fatal("committed runtime recovery lost imported limits")
	}
	fixture.service.publishSnapshot = fixture.manager.Publish
	if result, err := fixture.service.ImportForkKeyQuota(t.Context(), target.ID, request); err != nil || !result.Replayed {
		t.Fatalf("retry after committed publication failure = %#v, %v", result, err)
	}
}

func TestForkKeyQuotaImportHTTPContract(t *testing.T) {
	fixture, engine := newAccessKeyCostLimitHTTPFixture(t)
	now := time.Now().UTC()
	fixture.service.now = func() time.Time { return now }
	target := createForkQuotaTarget(t, fixture, state.AccessKeyStatusDisabled)
	path := fmt.Sprintf("/api/fork/access-keys/%d/quota-import", target.ID)
	for _, body := range []string{
		`{}`, `{"rules":null}`, `{"rules":[]}`, `{"rules":[{"kind":"unknown","limit_usd":"1","used_usd":"0"}]}`,
		`{"rules":[{"kind":"total","limit_usd":"1","used_usd":"0","reset":true}]}`,
		`{"rules":[{"kind":"total","limit_usd":"1","used_usd":"0","used_usd":"1"}]}`,
		`{"rules":[{"kind":"total","limit_usd":1,"used_usd":"0"}]}`,
		`{"rules":[{"kind":"total","limit_usd":"1","used_usd":1.5}]}`,
		`{"rules":[{"kind":"total","limit_usd":"0","used_usd":"0"}]}`,
		`{"rules":[{"kind":"total","limit_usd":"1","used_usd":"-1"}]}`,
		`{"rules":[{"kind":"total","limit_usd":"1","used_usd":"0","window_started_at_ms":1,"window_ends_at_ms":2}]}`,
		`{"rules":[{"kind":"total","limit_usd":"1","used_usd":"0"},{"kind":"total","limit_usd":"2","used_usd":"0"}]}`,
		`{"rules":[{"kind":"periodic","limit_usd":"1","used_usd":"1","period_seconds":18000}]}`,
		`{"rules":[{"kind":"periodic","limit_usd":"1","used_usd":"0","period_seconds":59}]}`,
		`{"rules":[{"kind":"periodic","limit_usd":"1","used_usd":"0","period_seconds":18000.5}]}`,
		`{"rules":[{"kind":"periodic","limit_usd":"1","used_usd":"0","period_seconds":18000,"window_started_at_ms":-1,"window_ends_at_ms":17999999}]}`,
		`{"rules":[{"kind":"periodic","limit_usd":"1","used_usd":"0","period_seconds":18000,"window_started_at_ms":1}]}`,
		`{"rules":[{"kind":"periodic","limit_usd":"1","used_usd":"0","period_seconds":18000,"window_started_at_ms":1000,"window_ends_at_ms":18001001}]}`,
		`{"rules":[{"kind":"periodic","limit_usd":"1","used_usd":"0","period_seconds":18000,"window_started_at_ms":1.5,"window_ends_at_ms":18000001.5}]}`,
		`{"rules":[{"kind":"periodic","limit_usd":"1","used_usd":"0","period_seconds":18000,"window_started_at_ms":9007199236740992,"window_ends_at_ms":9007199254740992}]}`,
		`{"rules":[{"kind":"periodic","limit_usd":"1","used_usd":"0","period_seconds":18000},{"kind":"periodic","limit_usd":"2","used_usd":"0","period_seconds":18000}]}`,
	} {
		got := serveAccessKeyCostLimitRequest(t, engine, http.MethodPost, path, body, "")
		if got.Code != http.StatusBadRequest {
			t.Fatalf("invalid %s = %d %s", body, got.Code, got.Body.String())
		}
	}
	payload := `{"rules":[{"kind":"total","limit_usd":"1","used_usd":"2","window_started_at_ms":null,"window_ends_at_ms":null}]}`
	unauthorized := httptest.NewRecorder()
	engine.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(payload)))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized = %d", unauthorized.Code)
	}
	// An ordinary active access key must not gain admin import permission.
	active := createForkQuotaTarget(t, fixture, state.AccessKeyStatusActive)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(payload))
	req.Header.Set("Authorization", "Bearer "+active.Key)
	forbidden := httptest.NewRecorder()
	engine.ServeHTTP(forbidden, req)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("access key auth = %d %s", forbidden.Code, forbidden.Body.String())
	}
	missing := serveAccessKeyCostLimitRequest(t, engine, http.MethodPost, "/api/fork/access-keys/99999/quota-import", payload, "")
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing target = %d %s", missing.Code, missing.Body.String())
	}
	for index := range 2 {
		got := serveAccessKeyCostLimitRequest(t, engine, http.MethodPost, path, payload, "")
		if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), fmt.Sprintf(`"replayed":%t`, index == 1)) {
			t.Fatalf("import/replay = %d %s", got.Code, got.Body.String())
		}
		if strings.Contains(got.Body.String(), `"key"`) || strings.Contains(got.Body.String(), target.Key) || !strings.Contains(got.Body.String(), `"used_usd":"2"`) || !strings.Contains(got.Body.String(), `"allowed":false`) {
			t.Fatalf("unsafe or incorrect response = %s", got.Body.String())
		}
	}
}
