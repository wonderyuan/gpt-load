package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"gpt-load/internal/channel"
	"gpt-load/internal/outboundproxy"
	"gpt-load/internal/platform/config"
	"gpt-load/internal/requestlog"
	"gpt-load/internal/storage/models"
	subscriptionruntime "gpt-load/internal/subscription/runtime"
)

func TestForkHomeOverviewGlobalReadOnly(t *testing.T) {
	initControlI18n(t)
	fixture := newServiceFixture(t)
	now := time.Date(2026, 10, 28, 12, 30, 0, 0, time.UTC)
	fixture.service.now = func() time.Time { return now }
	apiSecret := "sk-overview-upstream-secret-material"
	apiGroupID := createGroupWithCredentials(t, fixture, apiSecret)
	var credentials []models.Credential
	var emails []string
	var disabledGroupID uint
	for i := range 6 {
		email := fmt.Sprintf("private-overview-%d@example.test", i)
		groupID, credential := createHomeSubscriptionCredential(t, fixture, fmt.Sprintf("shared-%d", i), fmt.Sprintf("account-%d", i), email)
		if i == 5 {
			disabledGroupID = groupID
			if _, err := fixture.service.UpdateGroupSettings(t.Context(), groupID, GroupSettingsUpdateRequest{
				Enabled: optionalField[bool]{Set: true, Value: false},
			}); err != nil {
				t.Fatal(err)
			}
		}
		createHomeCredentialObservation(t, fixture, credential, now.Add(-time.Minute), "Pro")
		if err := fixture.db.Create(&models.CredentialAttemptStat{
			CredentialID: credential.ID, BucketStartMS: now.Add(-time.Hour).Truncate(time.Hour).UnixMilli(), SuccessCount: int64(20 - i),
		}).Error; err != nil {
			t.Fatal(err)
		}
		credentials = append(credentials, credential)
		emails = append(emails, email)
	}
	// A stored provider account summary is admin-only even when it contains
	// display names or organization metadata rather than an email field.
	var observation models.CredentialObservation
	if err := fixture.db.Where("credential_id = ?", credentials[0].ID).Take(&observation).Error; err != nil {
		t.Fatal(err)
	}
	var snapshot CredentialObservationSnapshot
	if err := json.Unmarshal(observation.SnapshotJSON, &snapshot); err != nil {
		t.Fatal(err)
	}
	const nestedEmail = "nested-private-overview@example.test"
	snapshot.Account = &ObservationAccountSummary{Email: nestedEmail, DisplayName: nestedEmail, OrganizationName: "private-organization"}
	expiry := now.Add(time.Hour).UnixMilli()
	available := int64(1)
	snapshot.ResetCreditsAvailable = &available
	snapshot.ResetCredits = []ObservationResetCredit{{Kind: "five_hour", ExpiresAtMS: &expiry}}
	body, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&observation).Update("snapshot_json", models.JSON(body)).Error; err != nil {
		t.Fatal(err)
	}
	const proxyPassword = "overview-proxy-password-secret"
	proxy := outboundproxy.Config{Mode: outboundproxy.ModeCustom, URL: "http://overview-user:" + proxyPassword + "@proxy-private.example:8080"}
	if _, err := fixture.service.UpdateGroupCredential(t.Context(), credentials[0].GroupID, credentials[0].ID, CredentialUpdateRequest{
		Name:  optionalField[string]{Set: true, Value: emails[0]},
		Proxy: optionalField[outboundproxy.Config]{Set: true, Value: proxy},
	}); err != nil {
		t.Fatal(err)
	}
	viewer, err := fixture.service.CreateAccessKey(t.Context(), AccessKeyCreateRequest{
		Name: "scoped viewer", Filters: &AccessKeyFilters{Groups: []uint{apiGroupID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	other, err := fixture.service.CreateAccessKey(t.Context(), AccessKeyCreateRequest{Name: "other active key"})
	if err != nil {
		t.Fatal(err)
	}
	if !fixture.registry.SetCooldown(credentials[0].ID, now.Add(time.Minute)) || !fixture.registry.SetBlacklisted(credentials[1].ID) {
		t.Fatal("failed to create runtime health issues")
	}
	remaining := 0.1
	if !fixture.registry.SetCredentialQuotaObservation(credentials[0].ID, &remaining, now.Add(time.Hour)) {
		t.Fatal("failed to create low-quota health issue")
	}
	reader := &recordingHomeStatisticsReader{fn: func(query requestlog.HomeStatisticsQuery) requestlog.HomeStatisticsReport {
		report := homeStatisticsReportForQuery(t, query)
		report.Summary = requestlog.UsageAggregate{RequestCount: 11, SuccessCount: 8, FailureCount: 3}
		if query.AccessKeyID != nil {
			report.Summary = requestlog.UsageAggregate{RequestCount: 2, SuccessCount: 1, FailureCount: 1}
		}
		report.Series[0].UsageAggregate = report.Summary
		return report
	}}
	fixture.service.homeStatistics = reader
	fixture.service.observeSubscriptionAccount = func(context.Context, channel.ID, subscriptionruntime.Credential, subscriptionruntime.Target) (subscriptionruntime.Observation, error) {
		t.Fatal("overview must not contact providers")
		return subscriptionruntime.Observation{}, nil
	}
	writes := 0
	if err := fixture.db.Callback().Create().Before("gorm:create").Register("fork_home_overview_no_create", func(*gorm.DB) { writes++ }); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Callback().Update().Before("gorm:update").Register("fork_home_overview_no_update", func(*gorm.DB) { writes++ }); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Callback().Delete().Before("gorm:delete").Register("fork_home_overview_no_delete", func(*gorm.DB) { writes++ }); err != nil {
		t.Fatal(err)
	}
	server := NewServer(&config.Config{AuthKey: authTestKey}, fixture.service)
	server.now = func() time.Time { return now }
	server.startedAt = now.Add(-time.Hour)
	engine := gin.New()
	server.RegisterRoutes(engine)
	var admin forkHomeOverviewResponse
	for _, token := range []string{authTestKey, viewer.Key} {
		result := performHomeRequest(engine, "/api/fork/home-overview", token)
		if result.Code != http.StatusOK {
			t.Fatalf("overview status=%d body=%s", result.Code, result.Body.String())
		}
		if result.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("overview must be no-store for both principals")
		}
		var envelope struct {
			Data forkHomeOverviewResponse `json:"data"`
		}
		if err := json.Unmarshal(result.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		data := envelope.Data
		if token == authTestKey {
			admin = data
		} else if !reflect.DeepEqual(admin, data) {
			t.Fatal("AccessKey must see exactly the same global overview as admin")
		}
		if data.ObservedAtMS != now.UnixMilli() || data.Base.CurrentAccessKey != nil || data.Base.Concurrency == nil ||
			data.Base.Inventory.GroupCount != 7 || data.Base.Inventory.CredentialCount != 7 || data.Base.Inventory.AvailableCredentialCount != 4 ||
			len(data.Base.AccessKeys) != 2 || len(data.Groups) != 7 || len(data.Accounts.Items) != 4 ||
			data.Statistics.Summary.RequestCount != 11 || data.Statistics.Summary.FailureCount != 3 || len(data.Statistics.Series) != 24 {
			t.Fatalf("incomplete shared overview: %#v", data)
		}
		if data.Statistics.ToMS-data.Statistics.FromMS != int64(24*time.Hour/time.Millisecond) || data.Statistics.ObservedAtMS != now.UnixMilli() {
			t.Fatal("statistics must retain the fixed global 24h window")
		}
		foundDisabled := false
		for _, group := range data.Groups {
			if group.ID == disabledGroupID {
				foundDisabled = !group.Enabled && group.Availability == "paused"
			}
			if group.ChannelID == "" || group.ChannelName == "" || group.CredentialCount != 1 {
				t.Fatal("missing minimal group/channel display metadata")
			}
		}
		if !foundDisabled || len(data.Health.CooldownCredentials) != 1 || len(data.Health.BlacklistedCredentials) != 1 || len(data.Health.LowQuotaCredentials) != 1 || len(data.Health.ExpiringResetCredits) != 1 {
			t.Fatal("global group or health information is missing")
		}
		for _, account := range data.Accounts.Items {
			if account.Credential.Account.EmailMask == "" || account.Credential.Observation == nil || account.Credential.Observation.Snapshot == nil {
				t.Fatal("safe account display/quota metadata missing")
			}
		}
		secrets := append([]string{apiSecret, viewer.Key, other.Key, credentials[0].Data, nestedEmail, "private-organization", proxyPassword, "proxy-private.example", `"account_summary"`, `"secret_version"`, `"proxy"`, `"access_token"`, `"refresh_token"`, `"key_value"`, `"KeyValue"`, `"email"`, `"last_failure_summary"`, `"rankings"`}, emails...)
		for _, secret := range secrets {
			if strings.Contains(result.Body.String(), secret) {
				t.Fatalf("overview leaked private data %q", secret)
			}
		}
	}
	for _, query := range reader.queries {
		if query.AccessKeyID != nil || query.Range != requestlog.HomeStatistics24H {
			t.Fatal("shared overview statistics must use nil AccessKey scope and fixed 24h")
		}
	}
	// Existing APIs retain their principal scope; the exception is confined to
	// the dedicated fork overview rather than any general admin GET surface.
	scoped := performHomeRequest(engine, "/api/home", viewer.Key)
	var scopedEnvelope struct {
		Data homeResponse `json:"data"`
	}
	if err := json.Unmarshal(scoped.Body.Bytes(), &scopedEnvelope); err != nil || scoped.Code != http.StatusOK || scopedEnvelope.Data.Inventory.GroupCount != 1 || len(scopedEnvelope.Data.AccessKeys) != 1 || scopedEnvelope.Data.CurrentAccessKey == nil {
		t.Fatalf("existing home scope changed: %d %v", scoped.Code, err)
	}
	if result := performHomeRequest(engine, "/api/home/statistics", viewer.Key); result.Code != http.StatusOK {
		t.Fatalf("existing statistics request failed: %d %s", result.Code, result.Body.String())
	}
	lastQuery := reader.queries[len(reader.queries)-1]
	if lastQuery.AccessKeyID == nil || *lastQuery.AccessKeyID != viewer.ID {
		t.Fatal("existing home/statistics scope changed")
	}
	for _, path := range []string{"/api/health", "/api/home/subscription-accounts", "/api/modern/groups", "/api/modern/access-keys", "/api/groups", "/api/channels", "/api/settings", "/api/access-keys"} {
		if result := performHomeRequest(engine, path, viewer.Key); result.Code != http.StatusForbidden {
			t.Fatalf("general admin GET %s became accessible: %d", path, result.Code)
		}
	}
	for _, path := range []string{"/api/fork/account-quotas", "/api/fork/key-quotas"} {
		for _, token := range []string{authTestKey, viewer.Key} {
			result := performHomeRequest(engine, path, token)
			if result.Code != http.StatusOK || result.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("explicitly shared quota GET %s failed: %d %s", path, result.Code, result.Body.String())
			}
			secrets := []string{apiSecret, viewer.Key, other.Key, proxyPassword}
			if token == viewer.Key {
				secrets = append(secrets, nestedEmail, `"account_summary"`)
				secrets = append(secrets, emails...)
			}
			for _, secret := range secrets {
				if strings.Contains(result.Body.String(), secret) {
					t.Fatalf("quota projection leaked %q", secret)
				}
			}
		}
	}
	for _, path := range []string{"/api/fork/home-overview", "/api/fork/account-quotas", "/api/fork/key-quotas"} {
		if result := performHomeRequest(engine, path, ""); result.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous request %s status=%d", path, result.Code)
		}
	}
	for _, suffix := range []string{"?", "?range=30d", "?access_key_id=1", "?group_id=1", "?unknown=1", "?range=24h&range=24h", "?%ZZ"} {
		for _, token := range []string{authTestKey, viewer.Key} {
			if result := performHomeRequest(engine, "/api/fork/home-overview"+suffix, token); result.Code != http.StatusBadRequest {
				t.Fatalf("overview accepted arbitrary query %q: %d", suffix, result.Code)
			}
		}
	}
	for _, mutation := range []struct{ method, path, body string }{
		{http.MethodPost, fmt.Sprintf("/api/groups/%d/credentials/%d/reset-credits/consume", credentials[0].GroupID, credentials[0].ID), `{"reset_type":"five_hour"}`},
		{http.MethodPost, fmt.Sprintf("/api/groups/%d/credentials/%d/reveal", credentials[0].GroupID, credentials[0].ID), `{}`},
		{http.MethodPost, fmt.Sprintf("/api/access-keys/%d/reveal", other.ID), `{}`},
		{http.MethodPost, fmt.Sprintf("/api/access-keys/%d/cost-limits/reset", other.ID), `{"rule_ids":[]}`},
		{http.MethodPost, fmt.Sprintf("/api/fork/access-keys/%d/quota-import", other.ID), `{}`},
		{http.MethodPut, fmt.Sprintf("/api/groups/%d/settings", apiGroupID), `{"enabled":false}`},
		{http.MethodDelete, fmt.Sprintf("/api/groups/%d", apiGroupID), ``},
	} {
		result := performForkOverviewMutation(engine, mutation.method, mutation.path, viewer.Key, mutation.body)
		if result.Code != http.StatusForbidden {
			t.Fatalf("mutation %s %s status=%d body=%s", mutation.method, mutation.path, result.Code, result.Body.String())
		}
	}
	if writes != 0 {
		t.Fatalf("read-only requests or forbidden mutations attempted %d database writes", writes)
	}
}

func performForkOverviewMutation(engine *gin.Engine, method, path, token, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	result := httptest.NewRecorder()
	engine.ServeHTTP(result, request)
	return result
}

func TestForkHomeOverviewProjectionDoesNotMutateAdminDTOs(t *testing.T) {
	email := "private@example.test"
	source := HomeSubscriptionAccountsResponse{Items: []HomeSubscriptionAccountResponse{{Credential: CredentialItemResponse{
		CredentialID: 1, Name: email, Mask: "p***e@example.test", Account: CredentialAccountResponse{Email: email, EmailMask: "p***e@example.test"},
		Observation: &CredentialObservationResponse{State: "fresh", Snapshot: &CredentialObservationSnapshot{Account: &ObservationAccountSummary{Email: email}, QuotaWindows: []ObservationQuotaWindow{}}},
	}}}}
	masked := maskForkHomeOverviewAccounts(source)
	payload, err := json.Marshal(masked)
	if err != nil {
		t.Fatal(err)
	}
	if source.Items[0].Credential.Account.Email != email || source.Items[0].Credential.Observation.Snapshot.Account.Email != email || source.Items[0].Credential.Name != email {
		t.Fatal("shared account projection mutated the admin DTO")
	}
	if strings.Contains(string(payload), email) || strings.Contains(string(payload), "account_summary") || masked.Items[0].Credential.Name != maskEmail(email) {
		t.Fatal("whitelist projection leaked account identity")
	}
	display := credentialDisplayResponse{CredentialAlias: email, CredentialConnectionType: "subscription"}
	health := runtimeHealthResponse{
		CooldownCredentials:    []healthProblemCredentialResponse{{credentialDisplayResponse: display, CredentialID: 1, Identity: email}},
		BlacklistedCredentials: []healthProblemCredentialResponse{{credentialDisplayResponse: display, CredentialID: 1, Identity: email}},
		LowQuotaCredentials:    []healthQuotaCredentialResponse{{credentialDisplayResponse: display, CredentialID: 1, Identity: email}},
		ExpiringResetCredits:   []healthExpiringResetCreditResponse{{credentialDisplayResponse: display, CredentialID: 1, Identity: email}},
	}
	safeHealth := maskForkHomeOverviewHealth(health)
	payload, err = json.Marshal(safeHealth)
	if err != nil || strings.Contains(string(payload), email) {
		t.Fatalf("health identity redaction failed: %v", err)
	}
	if health.CooldownCredentials[0].Identity != email || health.BlacklistedCredentials[0].Identity != email || health.LowQuotaCredentials[0].Identity != email || health.ExpiringResetCredits[0].Identity != email || health.CooldownCredentials[0].CredentialAlias != email {
		t.Fatal("health projection mutated the existing admin report")
	}
	if forkHomeOverviewHealthIdentity("", "subscription", 7) != "Subscription #7" || forkHomeOverviewHealthIdentity("****abcd", "api_key", 7) != "****abcd" {
		t.Fatal("safe identity fallback or API-key mask changed")
	}
}
