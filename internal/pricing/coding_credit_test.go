package pricing

import (
	"encoding/json"
	"testing"
	"time"

	"gpt-load/internal/channel"
	"gpt-load/internal/usage"
)

func TestGLMCodingTimeMultiplier(t *testing.T) {
	for _, test := range []struct {
		at   string
		want PriceMultiplier
	}{
		{"2026-10-26T13:59:59.999999999+08:00", 500_000},
		{"2026-10-26T14:00:00+08:00", 1_000_000},
		{"2026-10-26T17:59:59.999999999+08:00", 1_000_000},
		{"2026-10-26T18:00:00+08:00", 500_000},
		{"2026-10-30T14:00:00+08:00", 1_000_000},
		{"2026-10-31T14:00:00+08:00", 500_000},
		{"2026-11-01T14:00:00+08:00", 500_000},
		{"2026-10-26T06:00:00Z", 1_000_000},
		{"2026-10-25T22:30:00-07:00", 500_000},
	} {
		at, err := time.Parse(time.RFC3339Nano, test.at)
		if err != nil {
			t.Fatal(err)
		}
		if got := GLMCodingTimeMultiplier(at); got != test.want {
			t.Errorf("%s: got %d, want %d", test.at, got, test.want)
		}
	}
}

func TestIsGLMCodingTargetIsStrict(t *testing.T) {
	for _, test := range []struct {
		channel channel.ID
		url     string
		want    bool
	}{
		{channel.ZhipuAI, "https://open.bigmodel.cn/api/coding/paas/v4", true},
		{channel.ZhipuAI, "https://OPEN.BIGMODEL.CN:443/api/coding/paas/v4/", true},
		{channel.OpenAICompatible, "https://open.bigmodel.cn/api/coding/paas/v4", false},
		{channel.ZhipuAI, "https://open.bigmodel.cn/api/paas/v4", false},
		{channel.ZhipuAI, "https://api.z.ai/api/coding/paas/v4", false},
		{channel.ZhipuAI, "https://open.bigmodel.cn.evil.example/api/coding/paas/v4", false},
		{channel.ZhipuAI, "http://open.bigmodel.cn/api/coding/paas/v4", false},
		{channel.ZhipuAI, "https://open.bigmodel.cn:8443/api/coding/paas/v4", false},
		{channel.ZhipuAI, "https://user@open.bigmodel.cn/api/coding/paas/v4", false},
		{channel.ZhipuAI, "https://open.bigmodel.cn/api/coding/paas/v4?", false},
		{channel.ZhipuAI, "https://open.bigmodel.cn/api/coding/paas/v4?x=1", false},
		{channel.ZhipuAI, "https://open.bigmodel.cn/api/coding/paas/v4#x", false},
		{channel.ZhipuAI, "https://open.bigmodel.cn/api/%63oding/paas/v4", false},
	} {
		config, _ := json.Marshal(map[string]string{"base_url": test.url})
		if got := IsGLMCodingTarget(channel.ResolvedTarget{ChannelID: test.channel, TargetConfig: config}); got != test.want {
			t.Errorf("%s %s: got %v", test.channel, test.url, got)
		}
	}
}

func TestGLMCodingOfficialCreditFormulaAndReceipt(t *testing.T) {
	// 10K tokens of each type: 32.6/10.86 peak credits. Cached input is
	// exclusive of uncached input, not charged twice.
	result := usage.Result{State: usage.StateComplete, Tokens: usage.Tokens{UncachedInput: 10_000, CacheRead: 10_000, Output: 10_000}}
	for _, model := range []string{"glm-5.3", "glm-5.3-flash"} {
		for _, hour := range []int{14, 18} {
			at := time.Date(2026, 10, 26, hour, 0, 0, 0, codingCreditZone)
			table, credit := FreezeGLMCodingCreditTable(at)
			quote, receipt := table.QuoteForModeWithMultipliers(Identity{ChannelID: "zhipuai", ModelID: model}, result, ModeStandard, PriceMultipliers{Group: DefaultPriceMultiplier, AccessKey: DefaultPriceMultiplier})
			want := int64(326_000_000)
			if model == "glm-5.3-flash" {
				want = 108_600_000
			}
			if hour == 18 {
				want /= 2
			}
			if quote.State != CostStatePriced || quote.Completeness != CompletenessComplete || int64(quote.EstimatedCostNanoUSD) != want {
				t.Fatalf("%s hour %d: %#v, want %d", model, hour, quote, want)
			}
			receipt.SchemaVersion, receipt.CodingCredit = 8, credit
			encoded, err := json.Marshal(receipt)
			if err != nil {
				t.Fatal(err)
			}
			var decoded Receipt
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatal(err)
			}
			if err := ValidateReceipt(decoded); err != nil {
				t.Fatal(err)
			}
			decoded.CodingCredit.NanoUSDPerCredit = 1
			if ValidateReceipt(decoded) == nil {
				t.Fatal("accepted incorrect unit conversion")
			}
			receipt.SchemaVersion = 7
			if ValidateReceipt(*receipt) == nil {
				t.Fatal("accepted credit metadata on historical receipt")
			}
		}
	}
	for _, model := range []string{"glm-5.3-flashx", "glm-5.1", "glm-4.7", "GLM-5.3", "unknown"} {
		if _, ok := GLMCodingCreditPrices(model); ok {
			t.Fatalf("invented rate for %s", model)
		}
	}
	if 6000*GLMCodingNanoUSDPerCredit != 60_000_000_000 || 30000*GLMCodingNanoUSDPerCredit != 300_000_000_000 {
		t.Fatal("credit budget conversion changed")
	}
}
