package control

import (
	"bytes"
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

	"gpt-load/internal/apikeyobservation"
	"gpt-load/internal/channel"
	"gpt-load/internal/platform/config"
	app_errors "gpt-load/internal/platform/errors"
	"gpt-load/internal/storage/models"
	subscriptionruntime "gpt-load/internal/subscription/runtime"
)

type testAPIKeyResetObserver struct {
	*testAPIKeyObserver
	prepareCalls, consumeCalls int
	kinds                      []string
	prepareErr                 error
	consume                    func(context.Context, subscriptionruntime.ResetCreditRequest) (subscriptionruntime.ResetCreditResult, error)
	observeErr                 error
	partial                    bool
}

func (*testAPIKeyResetObserver) ResetTypes() []string { return []string{"five_hour", "week"} }
func (observer *testAPIKeyResetObserver) PrepareResetCredit(ctx context.Context, request apikeyobservation.Request, kind string) (json.RawMessage, error) {
	observer.prepareCalls++
	observer.kinds = append(observer.kinds, kind)
	key, _ := request.Credential.Value("api_key")
	if key != "synthetic-coding-key" {
		return nil, errors.New("incorrect API-key preparation")
	}
	if _, ok := subscriptionruntime.NetworkFromContext(ctx); !ok {
		return nil, errors.New("missing frozen API-key network")
	}
	return json.RawMessage(fmt.Sprintf(`{"targetType":"PERSONAL","resetType":%q,"recordId":123,"grantType":"DIRECT"}`, kind)), observer.prepareErr
}
func (observer *testAPIKeyResetObserver) ConsumeResetCredit(ctx context.Context, request apikeyobservation.Request, reset subscriptionruntime.ResetCreditRequest) (subscriptionruntime.ResetCreditResult, error) {
	observer.consumeCalls++
	return observer.consume(ctx, reset)
}
func (observer *testAPIKeyResetObserver) Observe(ctx context.Context, request apikeyobservation.Request) (subscriptionruntime.Observation, error) {
	result, err := observer.testAPIKeyObserver.Observe(ctx, request)
	if err != nil {
		return result, err
	}
	result.Payload = []byte(`{"plan_summary":{"name":"pro"},"quota_windows":[],"reset_credits_available":1,"reset_credits":[{"kind":"week","available":true,"expires_at_ms":4097000000000}]}`)
	result.Partial = observer.partial
	return result, observer.observeErr
}

func newAPIKeyResetFixture(t *testing.T) (serviceFixture, uint, uint, *testAPIKeyResetObserver) {
	t.Helper()
	fixture, groupID, credentialID, base := newAPIKeyObservationFixture(t)
	observer := &testAPIKeyResetObserver{testAPIKeyObserver: base}
	observer.consume = func(context.Context, subscriptionruntime.ResetCreditRequest) (subscriptionruntime.ResetCreditResult, error) {
		return subscriptionruntime.ResetCreditResult{Status: "succeeded", WindowsReset: 2}, nil
	}
	var err error
	fixture.service.apiKeyObservations, err = apikeyobservation.NewRegistry(fixture.channelRegistry, observer)
	if err != nil {
		t.Fatal(err)
	}
	fixture.service.prepareSubscriptionResetCredit = func(context.Context, channel.ID, subscriptionruntime.Credential, subscriptionruntime.Target) (json.RawMessage, error) {
		panic("API-key reset entered subscription preparation")
	}
	fixture.service.consumeSubscriptionResetCredit = nil
	return fixture, groupID, credentialID, observer
}

func TestAPIKeyResetUsesDurableOperationAndNativeObservation(t *testing.T) {
	t.Parallel()
	fixture, groupID, credentialID, observer := newAPIKeyResetFixture(t)
	observer.consume = func(_ context.Context, request subscriptionruntime.ResetCreditRequest) (subscriptionruntime.ResetCreditResult, error) {
		var stored models.CredentialResetOperation
		if err := fixture.db.Take(&stored, "idempotency_key = ?", resetCreditTestKey).Error; err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(stored.ResultJSON), `"recordId":123`) || request.ID != resetCreditTestKey || !strings.Contains(string(request.Data), `"resetType":"week"`) {
			t.Fatal("consume dispatched before persisting selection and UUID")
		}
		return subscriptionruntime.ResetCreditResult{Status: "succeeded", WindowsReset: 2}, nil
	}
	for attempt := range 2 {
		response, err := fixture.service.ConsumeCredentialResetCredit(t.Context(), groupID, credentialID, resetCreditTestKey, "week")
		if err != nil || response.Status != "succeeded" || response.WindowsReset != 2 || response.Replayed != (attempt == 1) || response.ObservationPending || response.Observation == nil || response.Observation.Snapshot == nil {
			t.Fatalf("response/error = %#v/%v", response, err)
		}
		cards := response.Observation.Snapshot.ResetCredits
		if len(cards) != 1 || cards[0].Kind != "week" || cards[0].Available == nil || !*cards[0].Available {
			t.Fatal("native observation dropped typed cards")
		}
		raw, _ := json.Marshal(response)
		if strings.Contains(string(raw), "recordId") || strings.Contains(string(raw), "synthetic-coding-key") || strings.Contains(string(raw), "DIRECT") {
			t.Fatal("reset response leaked selection/credential")
		}
	}
	if observer.prepareCalls != 1 || observer.consumeCalls != 1 || observer.calls != 1 || observer.kinds[0] != "week" {
		t.Fatal("reset replay selected/redeemed/refreshed again")
	}
	_, err := fixture.service.ConsumeCredentialResetCredit(t.Context(), groupID, credentialID, resetCreditTestKey, "five_hour")
	assertAPIErrorCode(t, err, app_errors.ErrIdempotencyKeyReused.Code)
}

func TestAPIKeyResetUnknownRetryNeverChangesCard(t *testing.T) {
	t.Parallel()
	for _, prepared := range []bool{false, true} {
		t.Run(fmt.Sprint(prepared), func(t *testing.T) {
			fixture, groupID, credentialID, observer := newAPIKeyResetFixture(t)
			var original []byte
			observer.consume = func(_ context.Context, request subscriptionruntime.ResetCreditRequest) (subscriptionruntime.ResetCreditResult, error) {
				if original == nil {
					original = bytes.Clone(request.Data)
					return subscriptionruntime.ResetCreditResult{}, context.DeadlineExceeded
				}
				if request.ID != resetCreditTestKey || !bytes.Equal(original, request.Data) || observer.prepareCalls != 1 {
					t.Fatal("uncertain retry changed card or request ID")
				}
				if observer.consumeCalls == 2 {
					return subscriptionruntime.ResetCreditResult{}, &subscriptionruntime.UpstreamHTTPError{StatusCode: 409}
				}
				return subscriptionruntime.ResetCreditResult{Status: "succeeded", WindowsReset: 2}, nil
			}
			_, err := fixture.service.ConsumeCredentialResetCredit(t.Context(), groupID, credentialID, resetCreditTestKey, "week")
			assertAPIErrorCode(t, err, app_errors.ErrResetCreditOutcomeUnknown.Code)
			if prepared {
				if err := fixture.db.Model(&models.CredentialResetOperation{}).Where("idempotency_key = ?", resetCreditTestKey).Updates(map[string]any{"state": models.CredentialResetOperationPrepared, "updated_at_ms": fixture.service.now().Add(-defaultSubscriptionControlTimeout - time.Second).UnixMilli()}).Error; err != nil {
					t.Fatal(err)
				}
			}
			_, err = fixture.service.ConsumeCredentialResetCredit(t.Context(), groupID, credentialID, resetCreditTestKey, "week")
			assertAPIErrorCode(t, err, app_errors.ErrResetCreditOutcomeUnknown.Code)
			_, err = fixture.service.ConsumeCredentialResetCredit(t.Context(), groupID, credentialID, "9f0f4c32-89d2-4bcb-9e19-052940dc2f17", "week")
			assertAPIErrorCode(t, err, app_errors.ErrResetCreditOutcomeUnknown.Code)
			if observer.consumeCalls != 2 || observer.prepareCalls != 1 {
				t.Fatal("uncertain unavailable card unlocked a new operation")
			}
			response, err := fixture.service.ConsumeCredentialResetCredit(t.Context(), groupID, credentialID, resetCreditTestKey, "week")
			if err != nil || response.Status != "succeeded" || observer.prepareCalls != 1 || observer.consumeCalls != 3 {
				t.Fatalf("final retry=%#v,%v", response, err)
			}
		})
	}
}

func TestAPIKeyResetFreshRejectionAllowsNewOperation(t *testing.T) {
	t.Parallel()
	fixture, groupID, credentialID, observer := newAPIKeyResetFixture(t)
	observer.consume = func(context.Context, subscriptionruntime.ResetCreditRequest) (subscriptionruntime.ResetCreditResult, error) {
		if observer.consumeCalls == 1 {
			return subscriptionruntime.ResetCreditResult{}, &subscriptionruntime.UpstreamHTTPError{StatusCode: 409}
		}
		return subscriptionruntime.ResetCreditResult{Status: "succeeded", WindowsReset: 1}, nil
	}
	_, err := fixture.service.ConsumeCredentialResetCredit(t.Context(), groupID, credentialID, resetCreditTestKey, "five_hour")
	assertAPIErrorCode(t, err, app_errors.ErrResetCreditUnavailable.Code)
	response, err := fixture.service.ConsumeCredentialResetCredit(t.Context(), groupID, credentialID, "9f0f4c32-89d2-4bcb-9e19-052940dc2f17", "five_hour")
	if err != nil || response.Status != "succeeded" || observer.prepareCalls != 2 || observer.consumeCalls != 2 {
		t.Fatal("known fresh rejection blocked a new card operation")
	}
}

func TestAPIKeyResetEligibilityAndCapabilities(t *testing.T) {
	t.Parallel()
	for _, target := range []struct {
		base      string
		supported bool
	}{
		{"https://open.bigmodel.cn/api/coding/paas/v4", true},
		{"https://open.bigmodel.cn/api/paas/v4", false},
		{"https://open.bigmodel.cn/api/coding/paas/v4/team", false},
		{"https://proxy.example/api/coding/paas/v4", false},
		{"https://api.z.ai/api/coding/paas/v4", false},
	} {
		fixture, groupID, credentialID, observer := newAPIKeyResetFixture(t)
		params, _ := json.Marshal(map[string]string{"base_url": target.base})
		if _, err := fixture.service.UpdateGroupSettings(t.Context(), groupID, GroupSettingsUpdateRequest{Params: optionalField[json.RawMessage]{Set: true, Value: params}}); err != nil {
			t.Fatal(err)
		}
		result, err := fixture.service.readForkAccountQuotas(t.Context(), forkAccountQuotaQuery{Page: 1, PageSize: 10})
		if err != nil || len(result.Items) != 1 || result.Items[0].ResetSupported != target.supported || (len(result.Items[0].ResetTypes) == 2) != target.supported {
			t.Fatalf("capabilities=%#v,%v", result, err)
		}
		if !target.supported {
			_, err := fixture.service.ConsumeCredentialResetCredit(t.Context(), groupID, credentialID, resetCreditTestKey, "week")
			assertAPIErrorCode(t, err, app_errors.ErrValidation.Code)
			if observer.prepareCalls != 0 || observer.consumeCalls != 0 {
				t.Fatal("unsupported target dispatched reset")
			}
		}
	}
	fixture, groupID, _ := newSubscriptionCredentialFixture(t)
	var group models.Group
	if fixture.db.Take(&group, groupID).Error != nil {
		t.Fatal("load subscription group")
	}
	if supported, kinds := fixture.service.credentialResetTypes(group); !supported || len(kinds) != 0 || kinds == nil {
		t.Fatal("Codex channel fallback capability lost")
	}
}

func TestAPIKeyResetNativeRouteBodyAndAuthorization(t *testing.T) {
	t.Parallel()
	initControlI18n(t)
	fixture, groupID, credentialID, observer := newAPIKeyResetFixture(t)
	engine := gin.New()
	NewServer(&config.Config{AuthKey: authTestKey}, fixture.service).RegisterRoutes(engine)
	path := fmt.Sprintf("/api/groups/%d/credentials/%d/reset-credits/consume", groupID, credentialID)
	for _, body := range []string{`null`, `[]`, `{"unknown":true}`, `{"reset_type":"month"}`, `{"reset_type":123}`, `{"reset_type":"week","reset_type":"five_hour"}`} {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+authTestKey)
		request.Header.Set("Idempotency-Key", resetCreditTestKey)
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, request)
		if response.Code < 400 || observer.consumeCalls != 0 || observer.prepareCalls != 0 {
			t.Fatalf("invalid body accepted: %s / %d", body, response.Code)
		}
	}
	for _, token := range []string{"wrong-key", authTestKey} {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"reset_type":"week"}`))
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Idempotency-Key", resetCreditTestKey)
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, request)
		if token == authTestKey {
			if response.Code != 200 || observer.consumeCalls != 1 || observer.kinds[0] != "week" {
				t.Fatalf("native typed reset failed %d %s", response.Code, response.Body.String())
			}
		} else if response.Code != 401 || observer.consumeCalls != 0 {
			t.Fatal("reset route lost admin authentication")
		}
	}
}

func TestAPIKeyResetPartialObservationReportsPendingIncludingReplay(t *testing.T) {
	t.Parallel()
	fixture, groupID, credentialID, observer := newAPIKeyResetFixture(t)
	observer.partial = true
	for attempt := range 2 {
		response, err := fixture.service.ConsumeCredentialResetCredit(t.Context(), groupID, credentialID, resetCreditTestKey, "week")
		if err != nil || response.Status != "succeeded" || !response.ObservationPending || response.Replayed != (attempt == 1) ||
			response.Observation == nil || response.Observation.State != "fresh" || response.Observation.LastErrorCode != "observation_partial" {
			t.Fatalf("partial reset/replay = %#v, %v", response, err)
		}
	}
	if observer.consumeCalls != 1 || observer.calls != 1 {
		t.Fatal("partial observation replay dispatched another consume or refresh")
	}
}

func TestAPIKeyResetObservationFailureReportsPending(t *testing.T) {
	t.Parallel()
	fixture, groupID, credentialID, observer := newAPIKeyResetFixture(t)
	observer.observeErr = errors.New("synthetic observation failure")
	response, err := fixture.service.ConsumeCredentialResetCredit(t.Context(), groupID, credentialID, resetCreditTestKey, "week")
	if err != nil || response.Status != "succeeded" || !response.ObservationPending {
		t.Fatalf("reset with observation failure=%#v,%v", response, err)
	}
}
