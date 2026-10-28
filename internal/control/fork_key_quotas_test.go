package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"gpt-load/internal/accessquota"
	"gpt-load/internal/channel"
	"gpt-load/internal/platform/config"
	app_errors "gpt-load/internal/platform/errors"
	"gpt-load/internal/protocol"
	"gpt-load/internal/state"
	"gpt-load/internal/storage/models"
)

func createForkQuotaGroup(t *testing.T, fixture serviceFixture, name string, credit, enabled bool, model, alias string) uint {
	t.Helper()
	group := validControlGroup(name)
	if credit {
		group.ChannelID = string(channel.ZhipuAI)
		group.Params = models.JSON(`{"base_url":"https://open.bigmodel.cn/api/coding/paas/v4"}`)
		group.Overrides = models.JSON(`{"glm_coding_credit_enabled":true}`)
	}
	body, err := json.Marshal([]state.ModelConfig{{ID: model, Alias: alias}})
	if err != nil {
		t.Fatal(err)
	}
	group.Models = body
	if err := fixture.db.Create(group).Error; err != nil {
		t.Fatal(err)
	}
	// GORM's true default requires an explicit update to persist false.
	if !enabled {
		if err := fixture.db.Model(group).Update("enabled", false).Error; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fixture.manager.Publish(mustBuildCompileInput(t, fixture.db)); err != nil {
		t.Fatal(err)
	}
	return group.ID
}

func TestForkKeyQuotasConfigurationScopeAccounting(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	usd := createForkQuotaGroup(t, fixture, "ordinary", false, true, "gpt-4o", "usd-alias")
	credit := createForkQuotaGroup(t, fixture, "credit", true, true, "glm-5.3", "credit-alias")
	disabledCredit := createForkQuotaGroup(t, fixture, "disabled-credit", true, false, "glm-5.3", "disabled-alias")
	disabledUSD := createForkQuotaGroup(t, fixture, "disabled-usd", false, false, "gpt-4o", "disabled-usd-alias")
	cases := []struct {
		name      string
		filters   *AccessKeyFilters
		unit      string
		potential []uint
		effective []uint
	}{
		{"unrestricted", nil, "mixed", []uint{usd, credit, disabledCredit, disabledUSD}, []uint{usd, credit}},
		{"usd", &AccessKeyFilters{Groups: []uint{usd}}, "usd", []uint{usd}, []uint{usd}},
		{"credit", &AccessKeyFilters{Groups: []uint{credit}}, "glm_coding_credits", []uint{credit}, []uint{credit}},
		{"disabled-credit", &AccessKeyFilters{Groups: []uint{disabledCredit}}, "glm_coding_credits", []uint{disabledCredit}, []uint{}},
		{"disabled-credit-still-mixed", &AccessKeyFilters{Groups: []uint{usd, disabledCredit}}, "mixed", []uint{usd, disabledCredit}, []uint{usd}},
		{"disabled-usd-still-mixed", &AccessKeyFilters{Groups: []uint{credit, disabledUSD}}, "mixed", []uint{credit, disabledUSD}, []uint{credit}},
		{"external-alias", &AccessKeyFilters{Models: []string{"credit-alias"}}, "glm_coding_credits", []uint{credit}, []uint{credit}},
		{"disabled-external-alias", &AccessKeyFilters{Models: []string{"disabled-alias"}}, "glm_coding_credits", []uint{disabledCredit}, []uint{}},
		{"upstream-id-not-external", &AccessKeyFilters{Models: []string{"glm-5.3"}}, "unknown", []uint{}, []uint{}},
		{"protocol-mismatch", &AccessKeyFilters{Groups: []uint{credit}, Protocols: []protocol.Protocol{protocol.Rerank}}, "unknown", []uint{}, []uint{}},
	}
	for _, tc := range cases {
		key, err := fixture.service.CreateAccessKey(t.Context(), AccessKeyCreateRequest{Name: tc.name, Filters: tc.filters})
		if err != nil {
			t.Fatal(err)
		}
		result, err := fixture.service.readForkKeyQuotas(t.Context(), forkKeyQuotaQuery{Page: 1, PageSize: 100})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(result.Groups) != 4 || result.Groups[2].Enabled || result.Groups[3].Enabled {
			t.Fatalf("%s: missing full configured groups: %#v", tc.name, result.Groups)
		}
		item := result.Items[len(result.Items)-1]
		var configured []uint
		if tc.filters != nil {
			configured = tc.filters.Groups
		}
		if item.GroupIDs == nil || !slices.Equal(item.GroupIDs, configured) {
			t.Fatalf("%s: group_ids must preserve configured scope (empty means all): %#v", tc.name, item.GroupIDs)
		}
		if item.ID != key.ID || item.Accounting.Unit != tc.unit || item.Accounting.Basis != "current_config" || !slices.Equal(item.PotentialGroupIDs, tc.potential) || !slices.Equal(item.EffectiveGroupIDs, tc.effective) {
			t.Fatalf("%s: %#v", tc.name, item)
		}
		if (item.Accounting.InternalUnitsPerCredit == "0.01") != (tc.unit == "glm_coding_credits") {
			t.Fatalf("%s: only pure credit may advertise a conversion: %#v", tc.name, item.Accounting)
		}
		wantActive := sortedForkQuotaGroupIDs(accessibleHomeGroups(fixture.manager.Current(), fixture.manager.Current().AccessKeysByID[key.ID]))
		if !slices.Equal(item.EffectiveGroupIDs, wantActive) {
			t.Fatalf("%s: diverged from home scope matcher", tc.name)
		}
	}
}

func TestForkKeyQuotasConfiguredAllScopeSurvivesProtocolFiltering(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	usd := createForkQuotaGroup(t, fixture, "all-scope-usd", false, true, "gpt-4o", "")
	createForkQuotaGroup(t, fixture, "all-scope-credit", true, true, "glm-5.3", "")
	key, err := fixture.service.CreateAccessKey(t.Context(), AccessKeyCreateRequest{
		Name: "all-groups-rerank-only", Filters: &AccessKeyFilters{Protocols: []protocol.Protocol{protocol.Rerank}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := fixture.service.readForkKeyQuotas(t.Context(), forkKeyQuotaQuery{Page: 1, PageSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	item := result.Items[0]
	if item.ID != key.ID || item.GroupIDs == nil || len(item.GroupIDs) != 0 || !slices.Equal(item.PotentialGroupIDs, []uint{usd}) || !slices.Equal(item.EffectiveGroupIDs, []uint{usd}) || item.Accounting.Unit != "usd" {
		t.Fatalf("computed protocol scope must not replace original configured all scope: %#v", item)
	}
	payload, err := json.Marshal(item)
	if err != nil || !strings.Contains(string(payload), `"group_ids":[]`) {
		t.Fatalf("configured all must be an empty JSON array: %s %v", payload, err)
	}
}

func TestForkKeyQuotasExplicitScopePreservesOrderAndMissingIDs(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	usd := createForkQuotaGroup(t, fixture, "explicit-scope-usd", false, true, "gpt-4o", "")
	missing := createForkQuotaGroup(t, fixture, "explicit-scope-deleted", true, false, "glm-5.3", "")
	configured := []uint{missing, usd}
	key, err := fixture.service.CreateAccessKey(t.Context(), AccessKeyCreateRequest{Name: "explicit-scope", Filters: &AccessKeyFilters{Groups: configured}})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Delete(&models.Group{}, missing).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.manager.Publish(mustBuildCompileInput(t, fixture.db)); err != nil {
		t.Fatal(err)
	}
	query := forkKeyQuotaQuery{Page: 1, PageSize: 50}
	result, err := fixture.service.readForkKeyQuotas(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}
	item := result.Items[0]
	if item.ID != key.ID || !slices.Equal(item.GroupIDs, configured) || !slices.Equal(item.PotentialGroupIDs, []uint{usd}) || !slices.Equal(item.EffectiveGroupIDs, []uint{usd}) || item.Accounting.Unit != "unknown" {
		t.Fatalf("original explicit scope must retain missing IDs and configured order: %#v", item)
	}
	// Returned raw scope must be detached from the runtime configuration.
	item.GroupIDs[0] = usd
	again, err := fixture.service.readForkKeyQuotas(t.Context(), query)
	if err != nil || !slices.Equal(again.Items[0].GroupIDs, configured) {
		t.Fatalf("caller mutation changed configured scope: %#v %v", again, err)
	}
}

func TestForkKeyQuotasUnknownForMissingOrUnreliableScope(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"no-groups", "deleted-group", "invalid-disabled-setting", "invalid-disabled-credit-target"} {
		t.Run(kind, func(t *testing.T) {
			fixture := newServiceFixture(t)
			var filters *AccessKeyFilters
			var gid uint
			if kind != "no-groups" {
				gid = createForkQuotaGroup(t, fixture, kind, true, false, "glm-5.3", "")
				filters = &AccessKeyFilters{Groups: []uint{gid}}
			}
			if _, err := fixture.service.CreateAccessKey(t.Context(), AccessKeyCreateRequest{Name: kind, Filters: filters}); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "deleted-group":
				if err := fixture.db.Delete(&models.Group{}, gid).Error; err != nil {
					t.Fatal(err)
				}
				if _, err := fixture.manager.Publish(mustBuildCompileInput(t, fixture.db)); err != nil {
					t.Fatal(err)
				}
			case "invalid-disabled-setting":
				if err := fixture.db.Model(&models.Group{}).Where("id = ?", gid).Update("overrides", models.JSON(`{"glm_coding_credit_enabled":"true"}`)).Error; err != nil {
					t.Fatal(err)
				}
			case "invalid-disabled-credit-target":
				if err := fixture.db.Model(&models.Group{}).Where("id = ?", gid).Update("params", models.JSON(`{"base_url":"https://open.bigmodel.cn/api/paas/v4"}`)).Error; err != nil {
					t.Fatal(err)
				}
			}
			result, err := fixture.service.readForkKeyQuotas(t.Context(), forkKeyQuotaQuery{Page: 1, PageSize: 50})
			if err != nil || len(result.Items) != 1 || result.Items[0].Accounting.Unit != "unknown" || result.Items[0].Accounting.InternalUnitsPerCredit != "" {
				t.Fatalf("%s: result=%#v err=%v", kind, result, err)
			}
		})
	}
}

type forkQuotaSQLRecorder struct {
	logger.Interface
	statements []string
}

func (r *forkQuotaSQLRecorder) Trace(_ context.Context, _ time.Time, sql func() (string, int64), _ error) {
	statement, _ := sql()
	r.statements = append(r.statements, statement)
}

func TestForkKeyQuotasSQLPaginationMetadataOnlyAndReadOnly(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	gid := createForkQuotaGroup(t, fixture, "metadata-only", false, true, "gpt-4o", "")
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	fixture.service.now = func() time.Time { return now }
	var ids []uint
	for i := range 5 {
		status := state.AccessKeyStatusActive
		if i%2 == 0 {
			status = state.AccessKeyStatusDisabled
		}
		expires := now.Add(time.Minute).UnixMilli()
		key, err := fixture.service.CreateAccessKey(t.Context(), AccessKeyCreateRequest{Name: fmt.Sprintf("metadata-%d", i), Status: &status, ExpiresAtMS: &expires})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, key.ID)
	}
	// A corrupt ciphertext and unavailable decryptor cannot affect metadata GET.
	if err := fixture.db.Model(&models.AccessKey{}).Where("id IN ?", ids).Update("key_value", "do-not-read-private-ciphertext").Error; err != nil {
		t.Fatal(err)
	}
	fixture.service.encryption = nil
	now = now.Add(2 * time.Minute)
	beforeSnapshot := fixture.manager.Current()
	beforeDirty := fixture.accessQuota.DirtySnapshots(-1)
	recorder := &forkQuotaSQLRecorder{Interface: logger.Discard}
	fixture.service.db = fixture.db.Session(&gorm.Session{Logger: recorder})
	result, err := fixture.service.readForkKeyQuotas(t.Context(), forkKeyQuotaQuery{Page: 2, PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 5 || result.Page != 2 || result.PageSize != 2 || len(result.Items) != 2 || result.Items[0].ID != ids[2] || result.Items[1].ID != ids[3] || len(result.Groups) != 1 || result.Groups[0].ID != gid {
		t.Fatalf("unexpected page: %#v", result)
	}
	if result.ObservedAtMS != now.UnixMilli() || result.Items[0].ExpiresAtMS == nil || *result.Items[0].ExpiresAtMS >= result.ObservedAtMS {
		t.Fatal("expired/disabled keys must remain in the administrator projection")
	}
	bounded := false
	for _, statement := range recorder.statements {
		upper := strings.ToUpper(statement)
		if strings.Contains(upper, "KEY_VALUE") || strings.Contains(upper, "REQUEST_LOG") || strings.Contains(upper, "CREDENTIAL") || strings.Contains(upper, "SELECT *") {
			t.Fatalf("projection queried forbidden data: %s", statement)
		}
		if !strings.HasPrefix(upper, "SELECT ") {
			t.Fatalf("GET issued a non-read statement: %s", statement)
		}
		if strings.Contains(upper, "FROM `ACCESS_KEYS`") && !strings.Contains(upper, "COUNT(") {
			if !strings.Contains(upper, "ORDER BY ID ASC") || !strings.Contains(upper, "LIMIT 2 OFFSET 2") {
				t.Fatalf("Key metadata was not paginated in SQL: %s", statement)
			}
			bounded = true
		}
	}
	if !bounded || fixture.manager.Current() != beforeSnapshot || !reflect.DeepEqual(beforeDirty, fixture.accessQuota.DirtySnapshots(-1)) {
		t.Fatal("missing bounded metadata query or GET changed runtime state")
	}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"do-not-read-private-ciphertext", "key_hash", "key_value", "internal_units_per_credit", "last_request_at_ms"} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatalf("metadata JSON contains forbidden field/material %q", forbidden)
		}
	}
	for _, page := range []int{3, 4, 1_000_000} {
		got, err := fixture.service.readForkKeyQuotas(t.Context(), forkKeyQuotaQuery{Page: page, PageSize: 2})
		want := 0
		if page == 3 {
			want = 1
		}
		if err != nil || got.Total != 5 || len(got.Items) != want || len(got.Groups) != 1 {
			t.Fatalf("page %d: %#v %v", page, got, err)
		}
	}
}

func TestForkKeyQuotasUsesNativeSnapshotWithoutResettingExpiredWindows(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	fixture.service.now = func() time.Time { return now }
	key := createForkQuotaTarget(t, fixture, state.AccessKeyStatusDisabled)
	request := forkQuotaImportRequest(now)
	if _, err := fixture.service.ImportForkKeyQuota(t.Context(), key.ID, request); err != nil {
		t.Fatal(err)
	}
	now = time.UnixMilli(*request.Rules[1].WindowEndsAtMS).Add(time.Minute)
	beforeDirty := fixture.accessQuota.DirtySnapshots(-1)
	var beforeRows []models.AccessKeyCostLimitState
	if err := fixture.db.Order("rule_id ASC").Find(&beforeRows).Error; err != nil {
		t.Fatal(err)
	}
	result, err := fixture.service.readForkKeyQuotas(t.Context(), forkKeyQuotaQuery{Page: 1, PageSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	status := result.Items[0].CostLimitStatus
	want := mapAccessKeyCostLimitStatus(fixture.accessQuota.Snapshot(key.ID, now))
	if !reflect.DeepEqual(status, want) || !reflect.DeepEqual(result.Items[0].CostLimitRules, costLimitDefinitionsFromStatus(want)) || status.Rules[1].Status != accessquota.RuleStatusInactive || status.Rules[0].UsedUSD != "70.123456789" {
		t.Fatalf("native snapshot mismatch: %#v want %#v", status, want)
	}
	var afterRows []models.AccessKeyCostLimitState
	if err := fixture.db.Order("rule_id ASC").Find(&afterRows).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforeRows, afterRows) || !reflect.DeepEqual(beforeDirty, fixture.accessQuota.DirtySnapshots(-1)) {
		t.Fatal("quota GET reset or checkpointed the expired native window")
	}
}

func TestForkKeyQuotasRejectsRuntimeMismatch(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"status", "key_hash", "filters", "rule_revision", "native_rules"} {
		t.Run(field, func(t *testing.T) {
			fixture := newServiceFixture(t)
			key, err := fixture.service.CreateAccessKey(t.Context(), AccessKeyCreateRequest{Name: "consistent", CostLimitRules: OptionalAccessKeyCostLimitRules{Set: true, Values: []AccessKeyCostLimitRuleRequest{{Kind: accessquota.KindTotal, LimitUSD: "10"}}}})
			if err != nil {
				t.Fatal(err)
			}
			switch field {
			case "rule_revision":
				err = fixture.db.Model(&models.AccessKeyCostLimitRule{}).Where("access_key_id = ?", key.ID).Update("rule_revision", 2).Error
			case "native_rules":
				err = fixture.accessQuota.Reconcile(map[uint][]accessquota.Rule{})
			default:
				value := any("disabled")
				if field == "key_hash" {
					value = "another-fingerprint"
				} else if field == "filters" {
					value = models.JSON(`{"groups":[],"protocols":[],"models":["changed"]}`)
				}
				err = fixture.db.Model(&models.AccessKey{}).Where("id = ?", key.ID).Update(field, value).Error
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.service.readForkKeyQuotas(t.Context(), forkKeyQuotaQuery{Page: 1, PageSize: 50}); !errors.Is(err, app_errors.ErrInternalServer) {
				t.Fatalf("%s mismatch must fail closed: %v", field, err)
			}
		})
	}
}

func TestForkKeyQuotaQueryAndEmptyResponse(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"page=0", "page=-1", "page=01", "page=1000001", "page_size=101", "page_size=0", "page=1&page=2", "page_size=1&page_size=1", "page=+1", "page=1.0", "group_id=1", "unknown=1", "page=%ZZ"} {
		if _, err := parseForkKeyQuotaQuery(raw); err == nil {
			t.Errorf("accepted invalid query %q", raw)
		}
	}
	for raw, want := range map[string]forkKeyQuotaQuery{"": {Page: 1, PageSize: 50}, "page=2&page_size=100": {Page: 2, PageSize: 100}} {
		if got, err := parseForkKeyQuotaQuery(raw); err != nil || got != want {
			t.Fatalf("%q: %#v %v", raw, got, err)
		}
	}
	fixture := newServiceFixture(t)
	result, err := fixture.service.readForkKeyQuotas(t.Context(), forkKeyQuotaQuery{Page: 1, PageSize: 100})
	if err != nil || result.Items == nil || result.Groups == nil || result.Total != 0 {
		t.Fatalf("empty response must use arrays: %#v %v", result, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := fixture.service.readForkKeyQuotas(ctx, forkKeyQuotaQuery{Page: 1, PageSize: 50}); !errors.Is(err, context.Canceled) {
		t.Fatalf("context error lost: %v", err)
	}
	for _, query := range []forkKeyQuotaQuery{{Page: 0, PageSize: 50}, {Page: 1, PageSize: 101}, {Page: 1_000_001, PageSize: 1}} {
		if _, err := fixture.service.readForkKeyQuotas(t.Context(), query); !errors.Is(err, app_errors.ErrBadRequest) {
			t.Fatalf("invalid service query accepted: %#v %v", query, err)
		}
	}
}

func TestForkKeyQuotasRouteSharedReadOnlyAndNoStore(t *testing.T) {
	t.Parallel()
	initControlI18n(t)
	fixture := newServiceFixture(t)
	key, err := fixture.service.CreateAccessKey(t.Context(), AccessKeyCreateRequest{Name: "viewer"})
	if err != nil {
		t.Fatal(err)
	}
	engine := gin.New()
	NewServer(&config.Config{AuthKey: authTestKey}, fixture.service).RegisterRoutes(engine)
	for token, want := range map[string]int{authTestKey: http.StatusOK, key.Key: http.StatusOK, "unknown": http.StatusUnauthorized, "": http.StatusUnauthorized} {
		result := performHomeRequest(engine, "/api/fork/key-quotas", token)
		if result.Code != want {
			t.Fatalf("expected %d got %d: %s", want, result.Code, result.Body.String())
		}
		if want == http.StatusOK && result.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("shared Key projection must not be cached")
		}
		if strings.Contains(result.Body.String(), key.Key) {
			t.Fatal("route leaked plaintext AccessKey")
		}
	}
	for _, path := range []string{fmt.Sprintf("/api/access-keys/%d/reveal", key.ID), fmt.Sprintf("/api/access-keys/%d/cost-limits/reset", key.ID), fmt.Sprintf("/api/fork/access-keys/%d/quota-import", key.ID)} {
		if result := performForkOverviewMutation(engine, http.MethodPost, path, key.Key, `{}`); result.Code != http.StatusForbidden {
			t.Fatalf("AccessKey mutation %s status=%d", path, result.Code)
		}
	}
	invalid := performHomeRequest(engine, "/api/fork/key-quotas?page_size=101", authTestKey)
	if invalid.Code != http.StatusBadRequest || invalid.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("invalid query must fail no-store: %d", invalid.Code)
	}
}
