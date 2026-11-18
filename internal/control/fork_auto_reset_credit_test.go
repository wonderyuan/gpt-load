package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gpt-load/internal/channel"
	"gpt-load/internal/platform/config"
	app_errors "gpt-load/internal/platform/errors"
	"gpt-load/internal/storage/models"
	subscriptionruntime "gpt-load/internal/subscription/runtime"
)

func autoResetTestObservation(now time.Time, kind string, expires int64) CredentialObservationResponse {
	stamp, count, available := now.UnixMilli(), int64(1), true
	return CredentialObservationResponse{State: "fresh", ObservedAtMS: &stamp, LastAttemptAtMS: &stamp,
		Snapshot: &CredentialObservationSnapshot{QuotaWindows: []ObservationQuotaWindow{}, ResetCreditsAvailable: &count,
			ResetCredits: []ObservationResetCredit{{Kind: kind, Available: &available, ExpiresAtMS: &expires}}}}
}

func TestAutoResetCreditCandidateExpiryBoundaries(t *testing.T) {
	now := time.Date(2026, 10, 30, 2, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name   string
		offset time.Duration
		want   bool
	}{
		{"expired", -time.Second, false}, {"at_expiry", 0, false}, {"near", time.Minute, true},
		{"threshold", 30 * time.Minute, true}, {"outside", 30*time.Minute + time.Millisecond, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, due := autoResetCreditCandidate(autoResetTestObservation(now, "week", now.Add(test.offset).UnixMilli()), now)
			if due != test.want {
				t.Fatalf("due=%v want %v", due, test.want)
			}
		})
	}
	for _, change := range []func(*CredentialObservationResponse){
		func(r *CredentialObservationResponse) { r.LastErrorCode = "observation_partial" },
		func(r *CredentialObservationResponse) { r.Snapshot.ResetCredits = nil },
		func(r *CredentialObservationResponse) { r.Snapshot.ResetCredits[0].ExpiresAtMS = nil },
		func(r *CredentialObservationResponse) { r.Snapshot.ResetCreditsAvailable = nil },
		func(r *CredentialObservationResponse) { r.State = "stale" },
	} {
		response := autoResetTestObservation(now, "week", now.Add(time.Minute).UnixMilli())
		change(&response)
		if _, _, due := autoResetCreditCandidate(response, now); due {
			t.Fatal("incomplete inventory authorized spend")
		}
	}
}

func autoResetTestRows(t *testing.T, fixture serviceFixture, groupID, credentialID uint) (models.Group, models.Credential) {
	t.Helper()
	var group models.Group
	var credential models.Credential
	if err := fixture.db.First(&group, groupID).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.First(&credential, credentialID).Error; err != nil {
		t.Fatal(err)
	}
	credential.Group = &group
	return group, credential
}

func TestAutoResetCreditAccountToggleIsolationAndRoute(t *testing.T) {
	fixture, groupID, id := newSubscriptionCredentialFixture(t)
	group, credential := autoResetTestRows(t, fixture, groupID, id)
	policy, err := fixture.service.loadAutoResetCreditPolicy(t.Context(), credential, group)
	if err != nil || policy.Enabled {
		t.Fatalf("default=%#v %v", policy, err)
	}
	status, err := fixture.service.SetAutoResetCredit(t.Context(), groupID, id, true)
	if err != nil || !status.Enabled || status.State != "waiting" {
		t.Fatalf("enable=%#v %v", status, err)
	}
	var row models.Credential
	fixture.db.First(&row, id)
	if row.Status != credential.Status || row.SecretVersion != credential.SecretVersion || row.Data != credential.Data {
		t.Fatal("toggle changed credential")
	}
	status, err = fixture.service.SetAutoResetCredit(t.Context(), groupID, id, false)
	if err != nil || status.Enabled {
		t.Fatalf("disable=%#v %v", status, err)
	}
	initControlI18n(t)
	engine := gin.New()
	NewServer(&config.Config{AuthKey: authTestKey}, fixture.service).RegisterRoutes(engine)
	for _, body := range []string{`{}`, `{"enabled":null}`, `{"enabled":"true"}`, `{"enabled":true,"extra":1}`} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/groups/%d/credentials/%d/auto-reset-credit", groupID, id), strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+authTestKey)
		request.Header.Set("Content-Type", "application/json")
		engine.ServeHTTP(recorder, request)
		if recorder.Code < 400 {
			t.Fatalf("invalid body accepted %s", body)
		}
	}
}

func TestAutoResetCreditDurableSuccessAndSingleExpiryBatch(t *testing.T) {
	fixture, groupID, id := newSubscriptionCredentialFixture(t)
	now := time.Date(2026, 10, 30, 2, 0, 0, 0, time.UTC)
	fixture.service.now = func() time.Time { return now }
	group, credential := autoResetTestRows(t, fixture, groupID, id)
	if _, err := fixture.service.SetAutoResetCredit(t.Context(), groupID, id, true); err != nil {
		t.Fatal(err)
	}
	fixture.service.prepareSubscriptionResetCredit = func(ctx context.Context, _ channel.ID, _ subscriptionruntime.Credential, _ subscriptionruntime.Target) (json.RawMessage, error) {
		condition, ok := subscriptionruntime.ResetCreditExpiryFromContext(ctx)
		if !ok || condition.ExpiresAtMS != now.Add(10*time.Minute).UnixMilli() {
			t.Fatal("missing expiry guard")
		}
		return json.RawMessage(`{"expires_at_ms":1}`), nil
	}
	calls := 0
	fixture.service.consumeSubscriptionResetCredit = func(ctx context.Context, _ channel.ID, _ subscriptionruntime.Credential, _ subscriptionruntime.Target, request subscriptionruntime.ResetCreditRequest) (subscriptionruntime.ResetCreditResult, error) {
		calls++
		var row models.SystemSetting
		if err := fixture.db.Where("key = ?", autoResetCreditKey(id)).Take(&row).Error; err != nil || !strings.Contains(row.Value, request.ID) {
			t.Fatal("intent not durable before dispatch")
		}
		_, manualErr := fixture.service.ConsumeCredentialResetCredit(ctx, groupID, id, "9f0f4c32-89d2-4bcb-9e19-052940dc2f16")
		if !errors.Is(manualErr, app_errors.ErrResetCreditOutcomeUnknown) {
			t.Fatal("overlapping manual action not blocked", manualErr)
		}
		return subscriptionruntime.ResetCreditResult{Status: "succeeded", WindowsReset: 1}, nil
	}
	fixture.service.observeSubscriptionAccount = func(context.Context, channel.ID, subscriptionruntime.Credential, subscriptionruntime.Target) (subscriptionruntime.Observation, error) {
		return subscriptionruntime.Observation{Payload: []byte(`{"quota_windows":[],"reset_credits_available":0}`), QuotaObserved: true}, nil
	}
	monitor := &quotaResetMonitor{service: fixture.service}
	observation := autoResetTestObservation(now, "", now.Add(10*time.Minute).UnixMilli())
	for range 2 {
		if err := monitor.autoResetCredit(t.Context(), credential, group, observation); err != nil {
			t.Fatal(err)
		}
		monitor = &quotaResetMonitor{service: fixture.service}
	}
	if calls != 1 {
		t.Fatal("repeat spend", calls)
	}
	policy, err := fixture.service.loadAutoResetCreditPolicy(t.Context(), credential, group)
	if err != nil || policy.State != "succeeded" {
		t.Fatalf("state=%#v %v", policy, err)
	}
}

func TestAutoResetCreditUnknownRetryKeepsUUIDAndSelection(t *testing.T) {
	fixture, groupID, id := newSubscriptionCredentialFixture(t)
	now := time.Date(2026, 10, 30, 2, 0, 0, 0, time.UTC)
	fixture.service.now = func() time.Time { return now }
	group, credential := autoResetTestRows(t, fixture, groupID, id)
	fixture.service.SetAutoResetCredit(t.Context(), groupID, id, true)
	prepared, calls := 0, 0
	fixture.service.prepareSubscriptionResetCredit = func(context.Context, channel.ID, subscriptionruntime.Credential, subscriptionruntime.Target) (json.RawMessage, error) {
		prepared++
		return json.RawMessage(`{"selected":"same"}`), nil
	}
	var requestID string
	fixture.service.consumeSubscriptionResetCredit = func(_ context.Context, _ channel.ID, _ subscriptionruntime.Credential, _ subscriptionruntime.Target, request subscriptionruntime.ResetCreditRequest) (subscriptionruntime.ResetCreditResult, error) {
		calls++
		if requestID == "" {
			requestID = request.ID
		}
		if request.ID != requestID || string(request.Data) != `{"selected":"same"}` {
			t.Fatal("changed retry identity or card")
		}
		return subscriptionruntime.ResetCreditResult{}, errors.New("connection lost")
	}
	observation := autoResetTestObservation(now, "", now.Add(20*time.Minute).UnixMilli())
	for range 2 {
		monitor := &quotaResetMonitor{service: fixture.service}
		if err := monitor.autoResetCredit(t.Context(), credential, group, observation); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Minute)
	}
	if calls != 2 || prepared != 1 {
		t.Fatalf("calls=%d prepared=%d", calls, prepared)
	}
	if _, err := fixture.service.SetAutoResetCredit(t.Context(), groupID, id, false); err != nil {
		t.Fatal(err)
	}
	if err := (&quotaResetMonitor{service: fixture.service}).autoResetCredit(t.Context(), credential, group, observation); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("disabled action retried")
	}
}
