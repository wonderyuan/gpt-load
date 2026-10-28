package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"gpt-load/internal/channel"
	"gpt-load/internal/platform/config"
	"gpt-load/internal/state"
	"gpt-load/internal/storage/models"
	subscriptionruntime "gpt-load/internal/subscription/runtime"
)

func TestForkAccountQuotasIncludesInactiveMembershipsAndMasksIdentity(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	now := time.Date(2026, time.October, 27, 10, 0, 0, 0, time.UTC)
	fixture.service.now = func() time.Time { return now }
	for i := range 6 {
		gid, credential := createHomeSubscriptionCredential(t, fixture, fmt.Sprintf("overview-%d", i), "shared-account", "private@example.com")
		if i == 0 {
			createHomeCredentialObservation(t, fixture, credential, now.Add(-time.Minute), "Pro plan")
			if _, err := fixture.service.UpdateGroupCredential(t.Context(), gid, credential.ID, CredentialUpdateRequest{Status: optionalField[state.CredentialStatus]{Set: true, Value: state.CredentialStatusDisabled}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	createGroupWithCredentials(t, fixture, "sk-private-api-key-material")
	result, err := fixture.service.readForkAccountQuotas(t.Context(), forkAccountQuotaQuery{Page: 1, PageSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 7 || len(result.Items) != 7 || result.ObservedAtMS != now.UnixMilli() {
		t.Fatalf("overview must include idle and API-key credentials, got total=%d items=%d", result.Total, len(result.Items))
	}
	if result.Items[0].Credential.ConfiguredStatus != string(state.CredentialStatusDisabled) || result.Items[0].Credential.Observation == nil {
		t.Fatalf("disabled credential or stored observation missing: %#v", result.Items[0])
	}
	if result.Items[1].Credential.Observation == nil || result.Items[1].Credential.Observation.State != "unavailable" {
		t.Fatalf("missing quota must remain unavailable, got %#v", result.Items[1].Credential.Observation)
	}
	if result.Items[6].QuotaSupported || result.Items[6].Credential.Observation != nil {
		t.Fatal("unsupported API-key channel must not advertise quota support")
	}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private@example.com", "sk-private-api-key-material", `"access_token"`, `"refresh_token"`} {
		if strings.Contains(string(payload), secret) {
			t.Fatalf("overview leaked private credential data: %s", secret)
		}
	}
	if result.Items[0].Credential.Account.EmailMask == "" {
		t.Fatal("account label must retain the safe mask")
	}
}

func TestForkAccountQuotaObservationMaskDetachesSharedPointers(t *testing.T) {
	t.Parallel()
	original := &CredentialObservationResponse{
		State: "fresh", ObservationVersion: 4,
		Snapshot: &CredentialObservationSnapshot{
			Plan:         ObservationPlanSummary{Name: "Pro"},
			Account:      &ObservationAccountSummary{Email: "nested-private@example.com", DisplayName: "Safe display", OrganizationName: "Workspace"},
			QuotaWindows: []ObservationQuotaWindow{{ID: "five-hour", State: "available"}},
		},
	}
	masked := maskForkAccountQuotaObservation(original)
	if masked == original || masked.Snapshot == original.Snapshot || masked.Snapshot.Account == original.Snapshot.Account {
		t.Fatal("mask must detach all pointers on the redaction path")
	}
	if original.Snapshot.Account.Email != "nested-private@example.com" || masked.Snapshot.Account.Email != "" || masked.Snapshot.Account.OrganizationName != "Workspace" || masked.ObservationVersion != 4 || len(masked.Snapshot.QuotaWindows) != 1 {
		t.Fatal("mask mutated the source or removed non-email observation fields")
	}
	for _, input := range []*CredentialObservationResponse{nil, {}, {Snapshot: &CredentialObservationSnapshot{}}} {
		_ = maskForkAccountQuotaObservation(input)
	}
}

func TestForkAccountQuotaRemovesNestedEmailWithoutChangingCredentialDetail(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	gid, credential := createHomeSubscriptionCredential(t, fixture, "nested-email", "nested-account", "credential-private@example.com")
	observedAt := fixture.service.now().Add(-time.Minute)
	createHomeCredentialObservation(t, fixture, credential, observedAt, "Pro")
	var stored models.CredentialObservation
	if err := fixture.db.Where("credential_id = ?", credential.ID).Take(&stored).Error; err != nil {
		t.Fatal(err)
	}
	var snapshot CredentialObservationSnapshot
	if err := json.Unmarshal(stored.SnapshotJSON, &snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.Account = &ObservationAccountSummary{Email: "observation-private@example.com", DisplayName: "Safe account", OrganizationName: "Workspace"}
	body, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&stored).Update("snapshot_json", models.JSON(body)).Error; err != nil {
		t.Fatal(err)
	}
	fixture.service.observeSubscriptionAccount = func(context.Context, channel.ID, subscriptionruntime.Credential, subscriptionruntime.Target) (subscriptionruntime.Observation, error) {
		t.Fatal("read-only quota projection contacted upstream")
		return subscriptionruntime.Observation{}, errors.New("unexpected upstream")
	}
	result, err := fixture.service.readForkAccountQuotas(t.Context(), forkAccountQuotaQuery{Page: 1, PageSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "credential-private@example.com") || strings.Contains(string(payload), "observation-private@example.com") {
		t.Fatal("account quota projection leaked nested email")
	}
	account := result.Items[0].Credential.Observation.Snapshot.Account
	if account == nil || account.Email != "" || account.DisplayName != "Safe account" || account.OrganizationName != "Workspace" {
		t.Fatal("nested email mask lost safe account metadata")
	}
	detail, err := fixture.service.GetCredentialDetail(t.Context(), gid, credential.ID)
	if err != nil || detail.Observation.Snapshot == nil || detail.Observation.Snapshot.Account.Email != "observation-private@example.com" {
		t.Fatalf("original credential detail contract changed: %v", err)
	}
	var after models.CredentialObservation
	if err := fixture.db.Where("credential_id = ?", credential.ID).Take(&after).Error; err != nil || string(after.SnapshotJSON) != string(body) {
		t.Fatal("read projection modified persisted observation")
	}
}

func TestForkAccountQuotaPaginationAndGroupFilter(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	gid := createGroupWithCredentials(t, fixture, "sk-page-one\nsk-page-two\nsk-page-three")
	for page, size := range map[int]int{1: 2, 2: 1, 3: 0} {
		result, err := fixture.service.readForkAccountQuotas(t.Context(), forkAccountQuotaQuery{Page: page, PageSize: 2, GroupID: gid})
		if err != nil {
			t.Fatal(err)
		}
		if result.Total != 3 || len(result.Items) != size || result.Page != page || result.PageSize != 2 {
			t.Fatalf("page %d: %#v", page, result)
		}
		for _, item := range result.Items {
			if item.GroupID != gid {
				t.Fatal("group filter leaked another group")
			}
		}
	}
	result, err := fixture.service.readForkAccountQuotas(t.Context(), forkAccountQuotaQuery{Page: 1_000_000, PageSize: 100})
	if err != nil || len(result.Items) != 0 {
		t.Fatalf("large valid page must safely be empty: %#v %v", result, err)
	}
}

func TestForkAccountQuotaQueryValidation(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"page=0", "page=-1", "page=01", "page=1000001", "page_size=101", "page_size=0", "page=1&page=2", "group_id=0", "group_id=-1", "group_id=1&group_id=2", "unknown=1", "page=%ZZ"} {
		if _, err := parseForkAccountQuotaQuery(raw); err == nil {
			t.Errorf("accepted invalid query %q", raw)
		}
	}
	query, err := parseForkAccountQuotaQuery("page=2&page_size=100&group_id=3")
	if err != nil || query != (forkAccountQuotaQuery{Page: 2, PageSize: 100, GroupID: 3}) {
		t.Fatalf("valid query: %#v %v", query, err)
	}
	fixture := newServiceFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := fixture.service.readForkAccountQuotas(ctx, forkAccountQuotaQuery{Page: 1, PageSize: 50}); !errors.Is(err, context.Canceled) {
		t.Fatalf("context error not preserved: %v", err)
	}
}

func TestForkAccountQuotaRouteSharedReadOnlyAndDoesNotChangeObservations(t *testing.T) {
	t.Parallel()
	initControlI18n(t)
	fixture := newServiceFixture(t)
	groupID, credential := createHomeSubscriptionCredential(t, fixture, "readonly", "readonly-account", "readonly@example.com")
	key, err := fixture.service.CreateAccessKey(t.Context(), AccessKeyCreateRequest{Name: "viewer"})
	if err != nil {
		t.Fatal(err)
	}
	engine := gin.New()
	NewServer(&config.Config{AuthKey: authTestKey}, fixture.service).RegisterRoutes(engine)
	for token, want := range map[string]int{authTestKey: http.StatusOK, key.Key: http.StatusOK, "unknown": http.StatusUnauthorized, "": http.StatusUnauthorized} {
		result := performHomeRequest(engine, "/api/fork/account-quotas", token)
		if result.Code != want {
			t.Fatalf("expected %d got %d: %s", want, result.Code, result.Body.String())
		}
		if want == http.StatusOK && result.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("private overview must not be cached by a shared proxy")
		}
		for _, secret := range []string{key.Key, "readonly@example.com", `"access_token"`, `"refresh_token"`} {
			if strings.Contains(result.Body.String(), secret) {
				t.Fatalf("shared quota GET leaked %q", secret)
			}
		}
	}
	for _, suffix := range []string{"reset-credits/consume", "reveal"} {
		if result := performForkOverviewMutation(engine, http.MethodPost, fmt.Sprintf("/api/groups/%d/credentials/%d/%s", groupID, credential.ID, suffix), key.Key, `{}`); result.Code != http.StatusForbidden {
			t.Fatalf("AccessKey mutation %s status=%d", suffix, result.Code)
		}
	}
	var count int64
	if err := fixture.db.Model(&models.CredentialObservation{}).Where("credential_id = ?", credential.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("GET unexpectedly created an upstream observation: %d %v", count, err)
	}
}
