package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gpt-load/internal/accessquota"
	"gpt-load/internal/channel"
	"gpt-load/internal/dialect"
	"gpt-load/internal/execution"
	"gpt-load/internal/platform/config"
	"gpt-load/internal/pricing"
	"gpt-load/internal/scheduler"
	"gpt-load/internal/state"
	"gpt-load/internal/usage"
)

func TestHandlerGLMCodingCreditFreezesRatesAndChargesSameLedger(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, peak := range []bool{false, true} {
			name := "offpeak"
			if peak {
				name = "peak"
			}
			if stream {
				name += "/stream"
			}
			t.Run(name, func(t *testing.T) {
				forwarder := &scriptedForwarder{results: []UpstreamResult{{StatusCode: 200, Header: make(http.Header), RequestWritten: true, Body: []byte(`{"ok":true}`), Usage: usage.Result{State: usage.StateComplete, Tokens: usage.Tokens{UncachedInput: 10_000, CacheRead: 10_000, Output: 10_000}}}}}
				if stream {
					forwarder.results[0].Committed = true
					forwarder.results[0].Stream = StreamObservation{EndReason: StreamEndCleanEOF}
					forwarder.streamResults = forwarder.results
				}
				sink := &recordingRequestLogSink{}
				engine, handler, manager, _ := newRequestLogHandlerTestRuntime(t, forwarder, &recordingAccessKeyRPMLimiter{}, sink, "sk-first")
				rules := []accessquota.Rule{{ID: 901, Revision: 1, Kind: accessquota.KindTotal, LimitNanoUSD: 60_000_000_000}}
				runtime := accessquota.NewRuntime()
				if err := runtime.Reconcile(map[uint][]accessquota.Rule{1: rules}); err != nil {
					t.Fatal(err)
				}
				handler.accessQuota = runtime
				input := gatewayAccessQuotaCompileInput(handler, rules)
				input.Groups[0].ChannelID = channel.ZhipuAI
				input.Groups[0].Params = json.RawMessage(`{"base_url":"https://open.bigmodel.cn/api/coding/paas/v4"}`)
				input.Groups[0].Models = []state.ModelConfig{{ID: "glm-5.3"}}
				input.Groups[0].Settings = config.Settings{state.SettingGLMCodingCreditEnabled: true}
				if _, err := manager.Publish(input); err != nil {
					t.Fatal(err)
				}
				// Frozen official prices must not depend on (or mutate) PAYG table.
				table, err := pricing.NewTable([]pricing.Rule{{Identity: pricing.Identity{ChannelID: "zhipuai", ModelID: "glm-5.3"}, Prices: pricing.Prices{Input: pricing.Price{Set: true, NanoUSDPerMillion: 99_000_000_000}}}})
				if err != nil {
					t.Fatal(err)
				}
				handler.priceTables = &mutableGatewayPriceTableProvider{table: table}
				hour := 13
				if peak {
					hour = 17
				}
				start := time.Date(2026, 10, 26, hour, 59, 59, 0, time.FixedZone("UTC+8", 8*60*60))
				now := start
				handler.now = func() time.Time { return now }
				forwarder.onCall = func(int) {
					now = start.Add(2 * time.Second)
					input.Groups[0].Settings = nil
					if _, err := manager.Publish(input); err != nil {
						t.Fatal(err)
					}
				}
				forwarder.onStreamCall = func(index int, _ http.ResponseWriter) { forwarder.onCall(index) }
				body := `{"model":"glm-5.3"}`
				if stream {
					body = `{"model":"glm-5.3","stream":true}`
				}
				request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
				request.Header.Set("Authorization", "Bearer gl-client")
				response := httptest.NewRecorder()
				engine.ServeHTTP(response, request)
				events := sink.snapshot()
				want := int64(163_000_000)
				if peak {
					want *= 2
				}
				if response.Code != 200 || len(events) != 1 || events[0].Usage.Pricing.EstimatedCostNanoUSD != want {
					t.Fatalf("response=%d; events=%#v; want=%d", response.Code, events, want)
				}
				var receipt pricing.Receipt
				if err := json.Unmarshal([]byte(events[0].Usage.Pricing.ReceiptJSON), &receipt); err != nil {
					t.Fatal(err)
				}
				if err := pricing.ValidateReceipt(receipt); err != nil {
					t.Fatal(err)
				}
				if receipt.SchemaVersion != 8 || receipt.CodingCredit == nil || !receipt.CodingCredit.StartedAt.Equal(start) {
					t.Fatalf("receipt=%#v", receipt)
				}
				view := runtime.Snapshot(1, now)
				if len(view.Rules) != 1 || view.Rules[0].UsedNanoUSD != want {
					t.Fatalf("quota=%#v", view)
				}
				unchanged, _ := table.Lookup(pricing.Identity{ChannelID: "zhipuai", ModelID: "glm-5.3"})
				if unchanged.Prices.Input.NanoUSDPerMillion != 99_000_000_000 {
					t.Fatal("mutated PAYG table")
				}
			})
		}
	}
}

func TestHandlerGLMCodingCreditDoesNotAffectOrdinaryTargets(t *testing.T) {
	table, err := pricing.NewTable(nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := &Handler{priceTables: &mutableGatewayPriceTableProvider{table: table}, now: func() time.Time { return time.Date(2026, 10, 26, 5, 0, 0, 0, time.UTC) }}
	for _, test := range []struct {
		enabled bool
		id      channel.ID
		url     string
	}{
		{false, channel.ZhipuAI, "https://open.bigmodel.cn/api/coding/paas/v4"},
		{true, channel.ZhipuAI, "https://open.bigmodel.cn/api/paas/v4"},
		{true, channel.OpenAICompatible, "https://open.bigmodel.cn/api/coding/paas/v4"},
		{true, channel.ZhipuAI, "https://proxy.example/api/coding/paas/v4"},
	} {
		targetConfig, _ := json.Marshal(map[string]string{"base_url": test.url})
		frozen := handler.freezeAttemptPricing(scheduler.Selection{ChannelID: test.id, Group: state.GroupView{GLMCodingCreditEnabled: test.enabled, PriceMultiplier: pricing.DefaultPriceMultiplier}, ResolvedTarget: channel.ResolvedTarget{ChannelID: test.id, TargetConfig: targetConfig}}, dialect.RequestMetadata{ObserveUsage: true, Operation: execution.OperationChatCompletion}, true, pricing.DefaultPriceMultiplier)
		if frozen.table != table || frozen.codingCredit != nil {
			t.Fatalf("changed ordinary target %#v", test)
		}
	}
}
