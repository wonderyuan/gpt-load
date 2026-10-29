package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"gpt-load/internal/accessquota"
	"gpt-load/internal/channel"
	"gpt-load/internal/execution"
	"gpt-load/internal/modelnotify"
	"gpt-load/internal/state"
	"gpt-load/internal/storage/models"
	subscriptionruntime "gpt-load/internal/subscription/runtime"
)

func quotaResetTestEvent(t *testing.T, fixture serviceFixture, groupID, credentialID uint, now time.Time) quotaResetEvent {
	t.Helper()
	var group models.Group
	var credential models.Credential
	if err := fixture.db.First(&group, groupID).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.First(&credential, credentialID).Error; err != nil {
		t.Fatal(err)
	}
	end := now.Add(time.Hour).UnixMilli()
	cursor := &quotaResetCursor{}
	cursor.observe(credential, group, quotaResetTestObservation(now.Add(-2*time.Minute), 1, 80, end), true)
	cursor.observe(credential, group, quotaResetTestObservation(now.Add(-time.Minute), 2, 0, end), true)
	response := quotaResetTestObservation(now, 3, 0, end)
	events := cursor.observe(credential, group, response, true)
	if len(events) != 1 {
		t.Fatalf("test event = %#v", events)
	}
	payload, _ := json.Marshal(response.Snapshot)
	if err := fixture.service.upsertCredentialObservation(t.Context(), models.CredentialObservation{CredentialID: credential.ID, IdentityFingerprint: credential.IdentityFingerprint,
		SchemaVersion: 1, ObservationVersion: 3, State: models.CredentialObservationFresh, ObservedAtMS: response.ObservedAtMS,
		LastAttemptAtMS: response.LastAttemptAtMS, SnapshotJSON: models.JSON(payload), UpdatedAtMS: now.UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	return events[0]
}

func quotaResetTestEnable(t *testing.T, fixture serviceFixture, groupID uint, enabled bool) {
	t.Helper()
	_, err := fixture.service.UpdateGroupSettings(t.Context(), groupID, GroupSettingsUpdateRequest{Overrides: optionalField[map[string]any]{Set: true, Value: map[string]any{state.SettingAccountQuotaResetSyncEnabled: enabled}}})
	if err != nil {
		t.Fatal(err)
	}
}

func quotaResetTestKey(t *testing.T, fixture serviceFixture, groups []uint, status state.AccessKeyStatus) uint {
	t.Helper()
	created, err := fixture.service.CreateAccessKey(t.Context(), AccessKeyCreateRequest{Name: "quota-reset", Key: fmt.Sprintf("sk-reset-test-%d", testIdempotencySequence.Add(1)), Status: &status,
		Filters: &AccessKeyFilters{Groups: groups}, CostLimitRules: OptionalAccessKeyCostLimitRules{Set: true, Values: []AccessKeyCostLimitRuleRequest{
			{Kind: accessquota.KindTotal, LimitUSD: "100"}, {Kind: accessquota.KindPeriodic, LimitUSD: "10", PeriodSeconds: 18000}, {Kind: accessquota.KindPeriodic, LimitUSD: "20", PeriodSeconds: 604800},
		}}})
	if err != nil {
		t.Fatal(err)
	}
	ticket, decision := fixture.accessQuota.Admit(created.ID, fixture.service.now())
	if !decision.Allowed {
		t.Fatal(decision)
	}
	fixture.accessQuota.Complete(ticket, 1_000_000_000)
	return created.ID
}

func TestQuotaResetSyncDedicatedKeysTotalPreservedIncludingKey10(t *testing.T) {
	fixture, groupID, credentialID := newSubscriptionCredentialFixture(t)
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	fixture.service.now = func() time.Time { return now }
	quotaResetTestEnable(t, fixture, groupID, true)
	otherGroup := createGroupWithCredentials(t, fixture, "sk-other")
	// Allocate 1..10 to exercise the formerly protected ID as a normal key.
	var selected uint
	for i := 1; i <= 10; i++ {
		selected = quotaResetTestKey(t, fixture, []uint{groupID}, state.AccessKeyStatusActive)
	}
	if selected != 10 {
		t.Fatalf("key ID = %d, want 10", selected)
	}
	all := quotaResetTestKey(t, fixture, nil, state.AccessKeyStatusActive)
	multi := quotaResetTestKey(t, fixture, []uint{groupID, otherGroup}, state.AccessKeyStatusActive)
	cross := quotaResetTestKey(t, fixture, []uint{otherGroup}, state.AccessKeyStatusActive)
	inactive := quotaResetTestKey(t, fixture, []uint{groupID}, state.AccessKeyStatusDisabled)
	before := fixture.accessQuota.Snapshot(selected, now)
	untouched := map[uint]accessquota.View{}
	for _, id := range []uint{all, multi, cross, inactive} {
		untouched[id] = fixture.accessQuota.Snapshot(id, now)
	}
	// Old requests must retain total/other-period accounting, but not charge the
	// replaced period's revised generation after publication.
	oldTicket, _ := fixture.accessQuota.Admit(selected, now)
	event := quotaResetTestEvent(t, fixture, groupID, credentialID, now)
	result, err := fixture.service.syncQuotaReset(t.Context(), event)
	if err != nil || result.Keys != 10 || !result.Enabled {
		t.Fatalf("sync = %#v, %v", result, err)
	}
	after := fixture.accessQuota.Snapshot(selected, now)
	if after.Rules[0] != before.Rules[0] || !reflect.DeepEqual(after.Rules[2], before.Rules[2]) || after.Rules[1].UsedNanoUSD != 0 || after.Rules[1].Revision != before.Rules[1].Revision+1 || *after.Rules[1].WindowEndsAtMS != event.EndMS {
		t.Fatalf("unsafe rebase before=%#v after=%#v", before, after)
	}
	fixture.accessQuota.Complete(oldTicket, 1_000_000_000)
	afterOld := fixture.accessQuota.Snapshot(selected, now)
	if afterOld.Rules[1].UsedNanoUSD != 0 || afterOld.Rules[0].UsedNanoUSD != 2_000_000_000 || afterOld.Rules[2].UsedNanoUSD != 2_000_000_000 {
		t.Fatalf("old ticket = %#v", afterOld)
	}
	for id, before := range untouched {
		if after := fixture.accessQuota.Snapshot(id, now); !reflect.DeepEqual(after, before) {
			t.Fatalf("out-of-scope key %d changed", id)
		}
	}
	// Permanent receipt survives canonical result compaction and never clears new
	// usage, even if a process crashes after SQL commit but before JSON progress.
	ticket, _ := fixture.accessQuota.Admit(selected, now)
	fixture.accessQuota.Complete(ticket, 100_000_000)
	if err := fixture.db.Model(&models.ControlOperation{}).Where("idempotency_key = ?", event.ID).Update("canonical_result", nil).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.syncQuotaReset(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	if got := fixture.accessQuota.Snapshot(selected, now).Rules[1].UsedNanoUSD; got != 100_000_000 {
		t.Fatalf("receipt replay cleared new usage: %d", got)
	}
	var receipt models.ControlOperation
	if err := fixture.db.Where("idempotency_key = ?", event.ID).Take(&receipt).Error; err != nil || receipt.CompletedAtMS == nil || receipt.OperationKind != quotaResetSyncKind {
		t.Fatalf("terminal receipt %#v %v", receipt, err)
	}
}

func TestQuotaResetSyncOffAndAmbiguousTargetsDoNotWrite(t *testing.T) {
	for _, reason := range []string{"sync_off", "multiple_active_accounts", "identity_changed", "target_changed", "ambiguous_period_sources", "outdated_event"} {
		t.Run(reason, func(t *testing.T) {
			fixture, groupID, credentialID := newSubscriptionCredentialFixture(t)
			now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
			fixture.service.now = func() time.Time { return now }
			if reason != "sync_off" {
				quotaResetTestEnable(t, fixture, groupID, true)
			}
			id := quotaResetTestKey(t, fixture, []uint{groupID}, state.AccessKeyStatusActive)
			event := quotaResetTestEvent(t, fixture, groupID, credentialID, now)
			switch reason {
			case "multiple_active_accounts":
				stage := mustImportSubscriptionStage(t, fixture, "second-account", "second@example.com")
				if _, err := fixture.service.ConnectGroupCredentials(t.Context(), groupID, []string{stage.StageID}); err != nil {
					t.Fatal(err)
				}
			case "identity_changed":
				event.Identity = quotaResetHash("different")
			case "target_changed":
				event.Target = quotaResetHash("different")
			case "ambiguous_period_sources":
				event.UniquePeriod = false
			case "outdated_event":
				event.EndMS = now.Add(-time.Minute).UnixMilli()
			}
			before := fixture.accessQuota.Snapshot(id, now)
			writes := 0
			if err := fixture.db.Callback().Create().Before("gorm:create").Register("quota_reset_no_create", func(tx *gorm.DB) { writes++ }); err != nil {
				t.Fatal(err)
			}
			if err := fixture.db.Callback().Update().Before("gorm:update").Register("quota_reset_no_update", func(tx *gorm.DB) { writes++ }); err != nil {
				t.Fatal(err)
			}
			result, err := fixture.service.syncQuotaReset(t.Context(), event)
			if err != nil || result.Reason != reason || writes != 0 || !reflect.DeepEqual(before, fixture.accessQuota.Snapshot(id, now)) {
				t.Fatalf("guard = %#v %v writes=%d", result, err, writes)
			}
		})
	}
}

type quotaResetFakeNotifier struct {
	messages []modelnotify.Message
	ids      []string
	err      error
}

func (fake *quotaResetFakeNotifier) Enabled() bool { return true }
func (fake *quotaResetFakeNotifier) Send(_ context.Context, id string, message modelnotify.Message) (modelnotify.Result, error) {
	fake.ids = append(fake.ids, id)
	fake.messages = append(fake.messages, message)
	return modelnotify.Result{Status: "queued"}, fake.err
}

func TestQuotaResetMonitorDurableOutboxRetryOffNoReplayAndRedaction(t *testing.T) {
	fixture, groupID, credentialID := newSubscriptionCredentialFixture(t)
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	fixture.service.now = func() time.Time { return now }
	fixture.service.quotaResetDataDir = t.TempDir()
	id := quotaResetTestKey(t, fixture, []uint{groupID}, state.AccessKeyStatusActive)
	event := quotaResetTestEvent(t, fixture, groupID, credentialID, now)
	monitor, err := newQuotaResetMonitor(fixture.service)
	if err != nil {
		t.Fatal(err)
	}
	monitor.state.Events = append(monitor.state.Events, event)
	if err := monitor.save(); err != nil {
		t.Fatal(err)
	}
	fake := &quotaResetFakeNotifier{err: &modelnotify.Error{Retryable: true, Code: "network_error"}}
	monitor.notifier = fake
	if err := monitor.flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(fake.ids) != 1 || monitor.state.Events[0].NotifyState != "" || !monitor.state.Events[0].DecisionDone || monitor.state.Events[0].SkipReason != "sync_off" {
		t.Fatalf("first flush %#v", monitor.state.Events)
	}
	body, err := os.ReadFile(monitor.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "observation@example.com") || strings.Contains(string(body), "account-observation") {
		t.Fatal("state leaked account identity")
	}
	if info, err := os.Stat(monitor.path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("state mode %v %v", info, err)
	}
	before := fixture.accessQuota.Snapshot(id, now)
	quotaResetTestEnable(t, fixture, groupID, true)
	now = now.Add(5 * time.Minute)
	reloaded, err := newQuotaResetMonitor(fixture.service)
	if err != nil {
		t.Fatal(err)
	}
	reloaded.notifier = fake
	fake.err = nil
	if err := reloaded.flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	firstPayload, _ := json.Marshal(fake.messages[0])
	secondPayload, _ := json.Marshal(fake.messages[len(fake.messages)-1])
	if len(fake.ids) != 2 || fake.ids[0] != fake.ids[1] || string(firstPayload) != string(secondPayload) || reloaded.state.Events[0].NotifyState != "accepted" {
		t.Fatal("notification retry changed idempotency or payload")
	}
	if got := fixture.accessQuota.Snapshot(id, time.UnixMilli(before.ObservedAtMS)); !reflect.DeepEqual(got, before) {
		t.Fatal("enabling replayed OFF event")
	}
	if err := reloaded.flush(t.Context()); err != nil || len(fake.ids) != 2 {
		t.Fatal("accepted notification resubmitted")
	}
	if strings.Contains(fake.messages[0].Body, "@") || !strings.Contains(fake.messages[0].Body, "联动：关闭") || fake.messages[0].Topic != "model.subscription.reset" {
		t.Fatalf("notification = %#v", fake.messages[0])
	}
}

func TestQuotaResetMonitorNotificationPolicy(t *testing.T) {
	for _, test := range []struct {
		name              string
		kind              string
		period            int64
		wantNotifications int
	}{
		{"five_hour_natural", "natural", 18000, 0},
		{"weekly_natural", "natural", 604800, 1},
		{"five_hour_sudden", "sudden", 18000, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, groupID, credentialID := newSubscriptionCredentialFixture(t)
			now := time.Now().UTC()
			fixture.service.now = func() time.Time { return now }
			fixture.service.quotaResetDataDir = t.TempDir()
			monitor, err := newQuotaResetMonitor(fixture.service)
			if err != nil {
				t.Fatal(err)
			}
			event := quotaResetTestEvent(t, fixture, groupID, credentialID, now)
			event.Kind, event.Period = test.kind, test.period
			event.DecisionDone, event.SyncEnabled, event.SyncedKeys, event.SkipReason = true, true, 2, "synced"
			fake := &quotaResetFakeNotifier{}
			monitor.notifier = fake
			monitor.state.Events = []quotaResetEvent{event}
			if err := monitor.flush(t.Context()); err != nil {
				t.Fatal(err)
			}
			if len(fake.messages) != test.wantNotifications {
				t.Fatalf("notifications = %d, want %d", len(fake.messages), test.wantNotifications)
			}
			stored := monitor.state.Events[0]
			if !stored.DecisionDone || stored.SyncedKeys != 2 {
				t.Fatal("notification policy changed reset decision")
			}
			if test.wantNotifications == 0 {
				if stored.NotifyState != "suppressed" {
					t.Fatalf("notification state = %s", stored.NotifyState)
				}
				reloaded, err := newQuotaResetMonitor(fixture.service)
				if err != nil {
					t.Fatal(err)
				}
				reloaded.notifier = fake
				if err := reloaded.flush(t.Context()); err != nil || len(fake.messages) != 0 {
					t.Fatal("suppressed notification replayed")
				}
			} else {
				message := fake.messages[0]
				if strings.Contains(message.Title+message.Body, "脱敏") || strings.Contains(message.Body, "推断") || strings.Contains(message.Body, "两次") || strings.Contains(message.Body, "保持不变") {
					t.Fatal("notification contains defensive copy")
				}
				if !strings.Contains(message.Body, "同步 Key：2 个") {
					t.Fatalf("message = %s", message.Body)
				}
			}
		})
	}
}

func TestQuotaResetMonitorUnroutedTerminalAndDisabledBounded(t *testing.T) {
	fixture, groupID, credentialID := newSubscriptionCredentialFixture(t)
	now := time.Now().UTC()
	fixture.service.now = func() time.Time { return now }
	fixture.service.quotaResetDataDir = t.TempDir()
	monitor, err := newQuotaResetMonitor(fixture.service)
	if err != nil {
		t.Fatal(err)
	}
	event := quotaResetTestEvent(t, fixture, groupID, credentialID, now)
	monitor.state.Events = []quotaResetEvent{event}
	monitor.notifier = nil
	if err := monitor.flush(t.Context()); err != nil || monitor.state.Events[0].NotifyState != "" || monitor.state.Events[0].NotifyNextMS <= now.UnixMilli() {
		t.Fatal("disabled notifier did not preserve pending event")
	}
	now = now.Add(30 * time.Minute)
	fake := &quotaResetFakeNotifier{err: &modelnotify.Error{Code: "unrouted", Retryable: false}}
	monitor.notifier = fake
	if err := monitor.flush(t.Context()); err != nil || monitor.state.Events[0].NotifyState != "unrouted" {
		t.Fatal("unrouted notification claimed accepted")
	}
	now = now.Add(time.Hour)
	if err := monitor.flush(t.Context()); err != nil || len(fake.ids) != 1 {
		t.Fatal("terminal notification retried")
	}
	for i := 0; i < 1100; i++ {
		monitor.state.Events = append(monitor.state.Events, event)
	}
	monitor.prune(now)
	if len(monitor.state.Events) != 1000 {
		t.Fatal("unbounded history")
	}
	now = now.Add(31 * 24 * time.Hour)
	monitor.prune(now)
	if len(monitor.state.Events) != 0 {
		t.Fatal("old history retained")
	}
}

func TestQuotaResetMonitorAutomaticObservationNoForced401Refresh(t *testing.T) {
	fixture, groupID, credentialID := newSubscriptionCredentialFixture(t)
	prepareCalls, observeCalls := 0, 0
	fixture.service.prepareSubscriptionCredential = func(_ context.Context, _ channel.ID, _ execution.CredentialSnapshot, force bool) (subscriptionruntime.Credential, *execution.ErrorEvidence) {
		prepareCalls++
		if force {
			t.Fatal("automatic observation forced OAuth refresh")
		}
		return subscriptionruntime.Credential{}, nil
	}
	fixture.service.observeSubscriptionAccount = func(context.Context, channel.ID, subscriptionruntime.Credential, subscriptionruntime.Target) (subscriptionruntime.Observation, error) {
		observeCalls++
		return subscriptionruntime.Observation{}, &subscriptionruntime.UpstreamHTTPError{StatusCode: 401}
	}
	_, err := fixture.service.refreshCredentialObservation(t.Context(), groupID, credentialID, observationRefreshAutomatic)
	if err == nil || prepareCalls != 1 || observeCalls != 1 {
		t.Fatalf("automatic 401 = %v prepares=%d observes=%d", err, prepareCalls, observeCalls)
	}
}

func TestQuotaResetMonitorPartialQuotaUsesOnlyFreshCoverage(t *testing.T) {
	fixture, groupID, credentialID := newSubscriptionCredentialFixture(t)
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	fixture.service.now = func() time.Time { return now }
	fixture.service.prepareSubscriptionCredential = func(context.Context, channel.ID, execution.CredentialSnapshot, bool) (subscriptionruntime.Credential, *execution.ErrorEvidence) {
		return subscriptionruntime.Credential{}, nil
	}
	for _, quotaObserved := range []bool{true, false} {
		fixture.service.observeSubscriptionAccount = func(context.Context, channel.ID, subscriptionruntime.Credential, subscriptionruntime.Target) (subscriptionruntime.Observation, error) {
			response := quotaResetTestObservation(now, 1, 1, now.Add(time.Hour).UnixMilli())
			if !quotaObserved {
				response.Snapshot.QuotaWindows = []ObservationQuotaWindow{}
			}
			body, _ := json.Marshal(response.Snapshot)
			return subscriptionruntime.Observation{Payload: body, Partial: true, QuotaObserved: quotaObserved}, nil
		}
		response, err := fixture.service.refreshCredentialObservation(t.Context(), groupID, credentialID, observationRefreshAutomatic)
		if err != nil {
			t.Fatal(err)
		}
		if got := len(quotaResetWindows(response)); quotaObserved && got != 1 || !quotaObserved && got != 0 {
			t.Fatalf("partial quotaObserved=%v fresh monitor windows=%d", quotaObserved, got)
		}
		var persisted models.CredentialObservation
		if err := fixture.db.Where("credential_id = ?", credentialID).Take(&persisted).Error; err != nil || persisted.LastErrorCode != "observation_partial" {
			t.Fatal("automatic private evidence changed persisted partial metadata")
		}
		now = now.Add(time.Minute)
	}
}

func TestQuotaResetMonitorSerialFreshCycleAndCandidateReload(t *testing.T) {
	fixture, groupID, credentialID := newSubscriptionCredentialFixture(t)
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	fixture.service.now = func() time.Time { return now }
	fixture.service.quotaResetDataDir = t.TempDir()
	used, end := float64(80), now.Add(time.Hour).UnixMilli()
	fixture.service.prepareSubscriptionCredential = func(context.Context, channel.ID, execution.CredentialSnapshot, bool) (subscriptionruntime.Credential, *execution.ErrorEvidence) {
		return subscriptionruntime.Credential{}, nil
	}
	calls := 0
	fixture.service.observeSubscriptionAccount = func(context.Context, channel.ID, subscriptionruntime.Credential, subscriptionruntime.Target) (subscriptionruntime.Observation, error) {
		calls++
		response := quotaResetTestObservation(now, uint64(calls), used, end)
		body, _ := json.Marshal(response.Snapshot)
		return subscriptionruntime.Observation{Payload: body}, nil
	}
	monitor, err := newQuotaResetMonitor(fixture.service)
	if err != nil {
		t.Fatal(err)
	}
	fake := &quotaResetFakeNotifier{}
	monitor.notifier = fake
	if err := monitor.cycle(t.Context()); err != nil || len(monitor.state.Events) != 0 || calls != 1 {
		t.Fatalf("baseline cycle %v calls=%d", err, calls)
	}
	if err := monitor.cycle(t.Context()); err != nil || calls != 1 {
		t.Fatal("polled before five minutes")
	}
	now = now.Add(5 * time.Minute)
	used = 0
	if err := monitor.cycle(t.Context()); err != nil || len(monitor.state.Cursors[credentialID].Candidates) != 1 || len(monitor.state.Events) != 0 {
		t.Fatalf("candidate %v %#v", err, monitor.state)
	}
	reloaded, err := newQuotaResetMonitor(fixture.service)
	if err != nil {
		t.Fatal(err)
	}
	reloaded.notifier = fake
	now = now.Add(time.Minute)
	if err := reloaded.cycle(t.Context()); err != nil || len(reloaded.state.Events) != 1 || len(fake.ids) != 1 || reloaded.state.Events[0].GroupID != groupID {
		t.Fatalf("confirmed cycle %v %#v calls=%d", err, reloaded.state, calls)
	}
	if cursor := reloaded.state.Cursors[credentialID]; cursor.Failures != 0 || cursor.NextAttemptMS != now.Add(5*time.Minute).UnixMilli() {
		t.Fatalf("schedule %#v", cursor)
	}
	// Failures back off without creating events from preserved fresh state.
	fixture.service.observeSubscriptionAccount = func(context.Context, channel.ID, subscriptionruntime.Credential, subscriptionruntime.Target) (subscriptionruntime.Observation, error) {
		return subscriptionruntime.Observation{}, errors.New("offline")
	}
	for _, interval := range []time.Duration{5 * time.Minute, 10 * time.Minute, 20 * time.Minute, 30 * time.Minute} {
		now = time.UnixMilli(reloaded.state.Cursors[credentialID].NextAttemptMS)
		if err := reloaded.cycle(t.Context()); err != nil {
			t.Fatal(err)
		}
		if got := reloaded.state.Cursors[credentialID].NextAttemptMS; got != now.Add(interval).UnixMilli() || len(reloaded.state.Events) != 1 {
			t.Fatalf("failure next=%d want %d", got, now.Add(interval).UnixMilli())
		}
	}
}
