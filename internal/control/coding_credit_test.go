package control

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"gpt-load/internal/channel"
	"gpt-load/internal/platform/config"
	app_errors "gpt-load/internal/platform/errors"
	"gpt-load/internal/pricing"
	"gpt-load/internal/state"
	"gpt-load/internal/storage/models"
	"gpt-load/internal/usage"
)

func TestMapGLMCodingCreditReceiptKeepsInternalUnitMetadata(t *testing.T) {
	table, credit := pricing.FreezeGLMCodingCreditTable(time.Date(2026, 10, 26, 6, 0, 0, 0, time.UTC))
	_, receipt := table.QuoteForModeWithMultipliers(
		pricing.Identity{ChannelID: "zhipuai", ModelID: "glm-5.3"},
		usage.Result{State: usage.StateComplete, Tokens: usage.Tokens{UncachedInput: 10_000}},
		pricing.ModeStandard,
		pricing.PriceMultipliers{Group: pricing.DefaultPriceMultiplier, AccessKey: pricing.DefaultPriceMultiplier},
	)
	receipt.SchemaVersion, receipt.CodingCredit = 8, credit
	response, err := mapRequestLogPricingReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if response.SchemaVersion != 8 || response.CodingCredit == nil || response.CodingCredit.NanoUSDPerCredit != 10_000_000 || response.TotalNanoUSD != "69000000" {
		t.Fatalf("credit metadata lost in request logs API: %#v", response)
	}
}

func TestCreditOnlyKeyHomeIdentifiesSyntheticLedger(t *testing.T) {
	fixture := newServiceFixture(t)
	group := validControlGroup("credit-only-home")
	group.ChannelID = string(channel.ZhipuAI)
	group.Params = models.JSON(`{"base_url":"https://open.bigmodel.cn/api/coding/paas/v4"}`)
	group.Models = models.JSON(`[{"id":"glm-5.3"}]`)
	group.Overrides = models.JSON(`{"glm_coding_credit_enabled":true}`)
	if err := fixture.db.Create(group).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.manager.Publish(mustBuildCompileInput(t, fixture.db)); err != nil {
		t.Fatal(err)
	}
	key, err := fixture.service.CreateAccessKey(t.Context(), AccessKeyCreateRequest{Name: "credit share", Filters: &AccessKeyFilters{Groups: []uint{group.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := fixture.service.ReadAccessKeyHomeBase(t.Context(), fixture.service.now().UnixMilli(), key.ID)
	if err != nil || result.KeyAccounting == nil || result.KeyAccounting.Unit != "glm_coding_credits" || result.KeyAccounting.InternalUnitsPerCredit != "0.01" {
		t.Fatalf("credit accounting metadata missing: %#v %v", result.KeyAccounting, err)
	}
	admin, err := fixture.service.ReadHomeBase(t.Context(), fixture.service.now().UnixMilli())
	if err != nil || admin.KeyAccounting != nil {
		t.Fatalf("administrator overview must not claim all costs are credits: %#v %v", admin.KeyAccounting, err)
	}
}

func TestGroupSettingsPersistGLMCodingCreditOptIn(t *testing.T) {
	fixture := newServiceFixture(t)
	group := validControlGroup("coding-credit")
	group.ChannelID = string(channel.ZhipuAI)
	group.Params = models.JSON(`{"base_url":"https://open.bigmodel.cn/api/coding/paas/v4"}`)
	group.Models = models.JSON(`[{"id":"glm-5.3"},{"id":"glm-5.3-flash"}]`)
	if err := fixture.db.Create(group).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.manager.Publish(mustBuildCompileInput(t, fixture.db)); err != nil {
		t.Fatal(err)
	}
	result, err := fixture.service.UpdateGroupSettings(t.Context(), group.ID, GroupSettingsUpdateRequest{Overrides: optionalField[config.Settings]{Set: true, Value: config.Settings{state.SettingGLMCodingCreditEnabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Effective.GLMCodingCreditEnabled || result.Overrides[state.SettingGLMCodingCreditEnabled] != true || !fixture.manager.Current().Groups[group.ID].GLMCodingCreditEnabled {
		t.Fatalf("not enabled: %#v", result)
	}
	var row models.Group
	if err := fixture.db.First(&row, group.ID).Error; err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	if err := json.Unmarshal(row.Overrides, &stored); err != nil || stored[state.SettingGLMCodingCreditEnabled] != true {
		t.Fatalf("stored=%#v, err=%v", stored, err)
	}
	// Reads and reloads keep the persisted mode; a target change to PAYG fails
	// transactionally rather than silently switching to ordinary USD pricing.
	if _, err := fixture.manager.Publish(mustBuildCompileInput(t, fixture.db)); err != nil {
		t.Fatal(err)
	}
	read, err := fixture.service.GetGroupSettings(t.Context(), group.ID)
	if err != nil || !read.Effective.GLMCodingCreditEnabled {
		t.Fatalf("read=%#v, err=%v", read, err)
	}
	_, err = fixture.service.UpdateGroupSettings(t.Context(), group.ID, GroupSettingsUpdateRequest{Params: optionalField[json.RawMessage]{Set: true, Value: json.RawMessage(`{}`)}})
	if !errors.Is(err, app_errors.ErrValidation) {
		t.Fatalf("PAYG update=%v", err)
	}
}
