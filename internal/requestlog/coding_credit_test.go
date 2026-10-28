package requestlog

import (
	"encoding/json"
	"testing"
	"time"

	"gpt-load/internal/pricing"
	"gpt-load/internal/telemetry"
	"gpt-load/internal/usage"
)

func TestCanonicalPricingReceiptPreservesGLMCodingCreditUnits(t *testing.T) {
	table, credit := pricing.FreezeGLMCodingCreditTable(time.Date(2026, 10, 26, 6, 0, 0, 0, time.UTC))
	quote, receipt := table.QuoteForModeWithMultipliers(
		pricing.Identity{ChannelID: "zhipuai", ModelID: "glm-5.3"},
		usage.Result{State: usage.StateComplete, Tokens: usage.Tokens{UncachedInput: 10_000}},
		pricing.ModeStandard,
		pricing.PriceMultipliers{Group: pricing.DefaultPriceMultiplier, AccessKey: pricing.DefaultPriceMultiplier},
	)
	receipt.SchemaVersion, receipt.CodingCredit = 8, credit
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := canonicalPricingReceipt(telemetry.PricingObservation{
		UpstreamModel: "glm-5.3", EstimatedCostNanoUSD: int64(quote.EstimatedCostNanoUSD), ReceiptJSON: string(encoded),
	}, "zhipuai")
	if err != nil {
		t.Fatal(err)
	}
	var persisted pricing.Receipt
	if err := json.Unmarshal(canonical, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.CodingCredit == nil || persisted.CodingCredit.NanoUSDPerCredit != 10_000_000 || persisted.TotalNanoUSD != 69_000_000 {
		t.Fatalf("canonical receipt = %#v", persisted)
	}
}
