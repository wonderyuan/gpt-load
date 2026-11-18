package control

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"gpt-load/internal/channel"
	"gpt-load/internal/storage/models"
	subscriptionruntime "gpt-load/internal/subscription/runtime"
)

func TestAutoResetCreditMonitorPersistsBaselineAndConsumesGLM(t *testing.T) {
	fixture, groupID, id, observer := newAPIKeyResetFixture(t)
	now := time.Date(2026, 10, 30, 2, 0, 0, 0, time.UTC)
	fixture.service.now = func() time.Time { return now }
	fixture.service.quotaResetDataDir = t.TempDir()
	group, credential := autoResetTestRows(t, fixture, groupID, id)
	if _, err := fixture.service.SetAutoResetCredit(t.Context(), groupID, id, true); err != nil {
		t.Fatal(err)
	}
	monitor, err := newQuotaResetMonitor(fixture.service)
	if err != nil {
		t.Fatal(err)
	}
	// Inventory from the regular observer is long-dated and never consumed.
	if err := monitor.cycle(t.Context()); err != nil {
		t.Fatal(err)
	}
	if observer.consumeCalls != 0 {
		t.Fatal("long dated card consumed")
	}
	monitor.state.Cursors[id].NextAttemptMS = 0
	// Native consume uses a typed selection and the pre-action cursor survives
	// the after-reset observation invalidation.
	observer.consume = func(ctx context.Context, request subscriptionruntime.ResetCreditRequest) (subscriptionruntime.ResetCreditResult, error) {
		if _, ok := subscriptionruntime.ResetCreditExpiryFromContext(ctx); !ok {
			t.Fatal("GLM lost expiry condition")
		}
		return subscriptionruntime.ResetCreditResult{Status: "succeeded", WindowsReset: 2}, nil
	}
	observation := autoResetTestObservation(now, "week", now.Add(time.Minute).UnixMilli())
	if err := monitor.autoResetCredit(t.Context(), credential, group, observation); err != nil {
		t.Fatal(err)
	}
	if observer.consumeCalls != 1 || len(observer.kinds) != 1 || observer.kinds[0] != "week" {
		t.Fatalf("consume=%d kinds=%v", observer.consumeCalls, observer.kinds)
	}
	policy, err := fixture.service.loadAutoResetCreditPolicy(t.Context(), credential, group)
	if err != nil || policy.State != "succeeded" {
		t.Fatalf("state=%#v %v", policy, err)
	}
}

func TestAutoResetCreditKnownRetryRejectionRetainsSelection(t *testing.T) {
	fixture, groupID, id := newSubscriptionCredentialFixture(t)
	now := time.Date(2026, 10, 30, 2, 0, 0, 0, time.UTC)
	fixture.service.now = func() time.Time { return now }
	group, credential := autoResetTestRows(t, fixture, groupID, id)
	fixture.service.SetAutoResetCredit(t.Context(), groupID, id, true)
	fixture.service.prepareSubscriptionResetCredit = func(context.Context, channel.ID, subscriptionruntime.Credential, subscriptionruntime.Target) (json.RawMessage, error) {
		return json.RawMessage(`{"expires_at_ms":1}`), nil
	}
	calls := 0
	fixture.service.consumeSubscriptionResetCredit = func(context.Context, channel.ID, subscriptionruntime.Credential, subscriptionruntime.Target, subscriptionruntime.ResetCreditRequest) (subscriptionruntime.ResetCreditResult, error) {
		calls++
		if calls == 1 {
			return subscriptionruntime.ResetCreditResult{}, errors.New("response lost")
		}
		return subscriptionruntime.ResetCreditResult{}, &subscriptionruntime.UpstreamHTTPError{StatusCode: 409}
	}
	observation := autoResetTestObservation(now, "", now.Add(10*time.Minute).UnixMilli())
	for range 2 {
		if err := (&quotaResetMonitor{service: fixture.service}).autoResetCredit(t.Context(), credential, group, observation); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Minute)
	}
	policy, _ := fixture.service.loadAutoResetCreditPolicy(t.Context(), credential, group)
	var operation models.CredentialResetOperation
	fixture.db.Where("idempotency_key = ?", policy.RequestID).Take(&operation)
	if operation.State != models.CredentialResetOperationOutcomeUnknown || len(operation.ResultJSON) == 0 || policy.State != "unknown" {
		t.Fatalf("selection unlocked %#v %#v", policy, operation)
	}
}
