package pricing

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"gpt-load/internal/channel"
)

// GLMCodingNanoUSDPerCredit is an INTERNAL ledger conversion, not a USD price
// or an exchange rate: 1 credit = 0.01 internal USD. Thus 6000/30000 credits
// are represented by native key limits of 60/300, independently for each key.
const GLMCodingNanoUSDPerCredit int64 = 10_000_000

// CodingCreditReceipt identifies synthetic ledger units explicitly while keeping
// native cost/quota storage unchanged. Ordinary API receipts never carry it.
type CodingCreditReceipt struct {
	StartedAt        time.Time       `json:"started_at"`
	TimeMultiplier   PriceMultiplier `json:"time_multiplier"`
	NanoUSDPerCredit int64           `json:"internal_nano_usd_per_credit"`
}

// IsGLMCodingTarget accepts only the official personal CN Coding Plan endpoint,
// never PAYG, international services, arbitrary proxies or compatible channels.
func IsGLMCodingTarget(target channel.ResolvedTarget) bool {
	if target.ChannelID != channel.ZhipuAI {
		return false
	}
	var config struct {
		BaseURL string `json:"base_url"`
	}
	if json.Unmarshal(target.TargetConfig, &config) != nil {
		return false
	}
	u, err := url.Parse(config.BaseURL)
	return err == nil && u.Scheme == "https" && strings.EqualFold(u.Hostname(), "open.bigmodel.cn") &&
		(u.Port() == "" || u.Port() == "443") && u.User == nil && u.RawQuery == "" &&
		!u.ForceQuery && u.Fragment == "" && u.RawPath == "" &&
		strings.TrimRight(u.Path, "/") == "/api/coding/paas/v4"
}

var codingCreditZone = time.FixedZone("Asia/Shanghai", 8*60*60)

// GLMCodingTimeMultiplier uses the official weekday [14:00,18:00) UTC+8
// peak window. It must be selected BEFORE dispatch, not when a stream completes.
func GLMCodingTimeMultiplier(startedAt time.Time) PriceMultiplier {
	local := startedAt.In(codingCreditZone)
	if local.Weekday() >= time.Monday && local.Weekday() <= time.Friday && local.Hour() >= 14 && local.Hour() < 18 {
		return DefaultPriceMultiplier
	}
	return DefaultPriceMultiplier / 2
}

// Verified 2026-11-24: https://docs.bigmodel.cn/cn/coding-plan/overview
// credits = (uncached_input*input + cached_input*cache + output*output)/10000.
// At .01 internalUSD/credit, the coefficients equal internalUSD/M tokens.
// Only upstream IDs with verified rates are accepted.
var glmCodingRates = map[string]struct{ input, cache, output NanoUSD }{
	"glm-5.3":       {6_900_000_000, 1_700_000_000, 24_000_000_000},
	"glm-5.3-flash": {2_300_000_000, 560_000_000, 8_000_000_000},
	"glm-5.4":       {4_600_000_000, 1_100_000_000, 16_000_000_000},
}

// GLMCodingCreditPrices are independent of mutable PAYG/model-price tables.
func GLMCodingCreditPrices(model string) (Prices, bool) {
	rate, ok := glmCodingRates[model]
	if !ok {
		return Prices{}, false
	}
	return Prices{
		Input:     Price{NanoUSDPerMillion: rate.input, Set: true},
		CacheRead: Price{NanoUSDPerMillion: rate.cache, Set: true},
		Output:    Price{NanoUSDPerMillion: rate.output, Set: true},
	}, true
}

// FreezeGLMCodingCreditTable freezes exact time-adjusted rates in an isolated
// immutable table. No shared PAYG table, group multiplier or setting is mutated.
func FreezeGLMCodingCreditTable(startedAt time.Time) (*Table, *CodingCreditReceipt) {
	factor := GLMCodingTimeMultiplier(startedAt)
	table := &Table{rules: make(map[Identity]Rule, len(glmCodingRates))}
	for model := range glmCodingRates {
		prices, _ := GLMCodingCreditPrices(model)
		for _, price := range []*Price{&prices.Input, &prices.CacheRead, &prices.Output} {
			// All published rates divide exactly; no intermediate rounding.
			price.NanoUSDPerMillion = price.NanoUSDPerMillion * NanoUSD(factor) / NanoUSD(DefaultPriceMultiplier)
		}
		identity := Identity{ChannelID: string(channel.ZhipuAI), ModelID: model}
		table.rules[identity] = Rule{Identity: identity, Prices: prices}
	}
	return table, &CodingCreditReceipt{
		StartedAt: startedAt.UTC(), TimeMultiplier: factor, NanoUSDPerCredit: GLMCodingNanoUSDPerCredit,
	}
}

func validateCodingCreditReceipt(receipt Receipt) error {
	credit := receipt.CodingCredit
	if credit == nil || credit.StartedAt.IsZero() || credit.NanoUSDPerCredit != GLMCodingNanoUSDPerCredit ||
		credit.TimeMultiplier != GLMCodingTimeMultiplier(credit.StartedAt) || receipt.Rule.ChannelID != string(channel.ZhipuAI) ||
		receipt.PricingMode != ModeStandard || receipt.ContextThresholdTokens != nil ||
		receipt.PriceMultipliers == nil || receipt.PriceMultipliers.Group != DefaultPriceMultiplier ||
		receipt.PriceMultipliers.AccessKey != DefaultPriceMultiplier {
		return fmt.Errorf("invalid GLM Coding credit receipt metadata")
	}
	// Validate frozen inputs without consulting today's mutable PAYG prices.
	return nil
}
