package control

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"gpt-load/internal/apikeyobservation"
	"gpt-load/internal/channel"
	"gpt-load/internal/channel/modules"
	"gpt-load/internal/channel/spec"
	"gpt-load/internal/execution"
	app_errors "gpt-load/internal/platform/errors"
	"gpt-load/internal/storage/models"
	subscriptionruntime "gpt-load/internal/subscription/runtime"
)

type testAPIKeyObserver struct {
	calls int
	err   error
}

func (*testAPIKeyObserver) ID() spec.UtilityID { return modules.ZhipuAIQuotaObservation }
func (*testAPIKeyObserver) SupportsTarget(target channel.ResolvedTarget) bool {
	return (apikeyobservation.ZhipuObserver{}).SupportsTarget(target)
}
func (observer *testAPIKeyObserver) Observe(ctx context.Context, request apikeyobservation.Request) (subscriptionruntime.Observation, error) {
	observer.calls++
	key, _ := request.Credential.Value("api_key")
	if key != "synthetic-coding-key" {
		return subscriptionruntime.Observation{}, errors.New("unexpected synthetic credential")
	}
	if _, ok := subscriptionruntime.NetworkFromContext(ctx); !ok {
		return subscriptionruntime.Observation{}, errors.New("missing frozen network")
	}
	return subscriptionruntime.Observation{Payload: []byte(`{"plan_summary":{"name":"pro"},"quota_windows":[{"id":"5h","label":"5h","scope":"account","unit":"percent","utilization":0.16,"window_seconds":18000,"state":"available","is_primary":true},{"id":"weekly","label":"7d","scope":"account","unit":"percent","utilization":0.64,"window_seconds":604800,"state":"available"}]}`), QuotaObserved: true, ObservedQuotaScopes: []string{"account"}}, observer.err
}

func newAPIKeyObservationFixture(t *testing.T) (serviceFixture, uint, uint, *testAPIKeyObserver) {
	t.Helper()
	fixture := newServiceFixture(t)
	created, err := fixture.service.CreateGroup(t.Context(), GroupCreateRequest{
		Name: stringPointer("GLM coding observation"), ChannelID: channel.ZhipuAI, ConnectionType: models.ConnectionTypeAPIKey,
		Params: json.RawMessage(`{"base_url":"https://open.bigmodel.cn/api/coding/paas/v4"}`),
		Models: optionalGroupModels{Set: true}, Credentials: "synthetic-coding-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	var credential models.Credential
	if err := fixture.db.Take(&credential, "group_id = ?", created.GroupID).Error; err != nil {
		t.Fatal(err)
	}
	observer := &testAPIKeyObserver{}
	fixture.service.apiKeyObservations, err = apikeyobservation.NewRegistry(fixture.channelRegistry, observer)
	if err != nil {
		t.Fatal(err)
	}
	fixture.service.prepareSubscriptionCredential = func(context.Context, channel.ID, execution.CredentialSnapshot, bool) (subscriptionruntime.Credential, *execution.ErrorEvidence) {
		panic("API key entered OAuth preparation")
	}
	return fixture, created.GroupID, credential.ID, observer
}

func TestAPIKeyQuotaRefreshPersistsAndPresentsWithoutOAuth(t *testing.T) {
	t.Parallel()
	fixture, groupID, credentialID, observer := newAPIKeyObservationFixture(t)
	result, err := fixture.service.RefreshCredentialObservation(t.Context(), groupID, credentialID)
	if err != nil || result.State != "fresh" || result.Snapshot == nil || len(result.Snapshot.QuotaWindows) != 2 || result.Snapshot.Plan.Name != "pro" || observer.calls != 1 {
		t.Fatalf("result/calls = %#v/%d: %v", result, observer.calls, err)
	}
	got, err := fixture.service.GetCredentialObservation(t.Context(), groupID, credentialID)
	if err != nil || got.Snapshot == nil || got.Snapshot.Plan.Name != "pro" {
		t.Fatalf("get=%#v,%v", got, err)
	}
	list, err := fixture.service.ListGroupCredentials(t.Context(), groupID, CredentialCollectionQuery{Page: 1, PageSize: 20})
	if err != nil || len(list.Items) != 1 || list.Items[0].Observation == nil || list.Items[0].Observation.Snapshot == nil {
		t.Fatalf("list=%#v,%v", list, err)
	}
	detail, err := fixture.service.GetCredentialDetail(t.Context(), groupID, credentialID)
	if err != nil || detail.Observation.Snapshot == nil || detail.Credential.Observation == nil {
		t.Fatalf("detail=%#v,%v", detail, err)
	}
	if detail.Credential.ConnectionType != "api_key" || detail.Credential.Account.Email != "" {
		t.Fatalf("API-key lifecycle changed: %#v", detail.Credential)
	}

	// A 401 must preserve successful data, record failure, and never retry via OAuth.
	observer.err = &subscriptionruntime.UpstreamHTTPError{StatusCode: 401}
	failed, err := fixture.service.RefreshCredentialObservation(t.Context(), groupID, credentialID)
	if !errors.Is(err, app_errors.ErrBadGateway) || observer.calls != 2 || failed.State != "fresh" || failed.Snapshot == nil || failed.Snapshot.Plan.Name != "pro" || failed.LastErrorCode != "observation_authorization_failed" {
		t.Fatalf("failure=%#v calls=%d err=%v", failed, observer.calls, err)
	}
	if failed.ObservationVersion != result.ObservationVersion {
		t.Fatal("failed observation replaced successful version")
	}
}

func TestAPIKeyQuotaEligibilityAndTargetIsolation(t *testing.T) {
	t.Parallel()
	fixture, groupID, credentialID, observer := newAPIKeyObservationFixture(t)
	if _, err := fixture.service.RefreshCredentialObservation(t.Context(), groupID, credentialID); err != nil {
		t.Fatal(err)
	}
	for _, base := range []string{"https://open.bigmodel.cn/api/paas/v4", "https://open.bigmodel.cn/api/coding/paas/v4"} {
		params, _ := json.Marshal(map[string]string{"base_url": base})
		if _, err := fixture.service.UpdateGroupSettings(t.Context(), groupID, GroupSettingsUpdateRequest{Params: optionalField[json.RawMessage]{Set: true, Value: params}}); err != nil {
			t.Fatal(err)
		}
		list, err := fixture.service.ListGroupCredentials(t.Context(), groupID, CredentialCollectionQuery{Page: 1, PageSize: 20})
		if err != nil || len(list.Items) != 1 {
			t.Fatalf("list=%#v,%v", list, err)
		}
		if base == "https://open.bigmodel.cn/api/paas/v4" {
			if _, err := fixture.service.RefreshCredentialObservation(t.Context(), groupID, credentialID); !errors.Is(err, app_errors.ErrValidation) {
				t.Fatalf("PAYG refresh error=%v", err)
			}
			if list.Items[0].Observation != nil {
				t.Fatal("PAYG displays old coding observation")
			}
		} else {
			got, err := fixture.service.GetCredentialObservation(t.Context(), groupID, credentialID)
			if err != nil || got.Snapshot != nil || got.State != "unavailable" {
				t.Fatalf("returned target revived old snapshot: %#v,%v", got, err)
			}
		}
	}
	if observer.calls != 1 {
		t.Fatalf("unexpected upstream calls=%d", observer.calls)
	}
}

func TestAPIKeyQuotaInitialFailureDoesNotInventZero(t *testing.T) {
	t.Parallel()
	fixture, groupID, credentialID, observer := newAPIKeyObservationFixture(t)
	observer.err = subscriptionruntime.ErrObservationPayloadInvalid
	result, err := fixture.service.RefreshCredentialObservation(t.Context(), groupID, credentialID)
	if !errors.Is(err, app_errors.ErrBadGateway) || result.State != "error" || result.Snapshot != nil || result.LastErrorCode != "observation_payload_invalid" {
		t.Fatalf("result=%#v,%v", result, err)
	}
}
