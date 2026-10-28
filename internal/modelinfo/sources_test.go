package modelinfo

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

func sourceFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseIntelligenceUsesRenderedTotalsAndDeliveredChartMetrics(t *testing.T) {
	got, updated, err := parseIntelligence(sourceFixture(t, "home.html"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Batch != "codex-four-261-20261028" || got.MaxScore != 150 || len(got.Models) != 2 || len(got.Efforts) != 2 || got.Efforts[0] != "ultra" || len(got.Pareto) != 2 {
		t.Fatalf("unexpected intelligence: %#v", got)
	}
	if updated == nil || *updated != time.Date(2026, 10, 28, 5, 38, 0, 0, time.UTC).UnixMilli() {
		t.Fatalf("test-end timestamp = %v", updated)
	}
	low := got.Models[0].Results[1]
	if low.Score == nil || *low.Score != 110.47 || low.CostUSD == nil || *low.CostUSD != .59 || low.DurationSeconds == nil || *low.DurationSeconds != 1214.3 {
		t.Fatalf("matrix total lost precision/units: %#v", low)
	}
	unsupported := got.Models[1].Results[0]
	if unsupported.Score != nil || unsupported.CostUSD != nil || unsupported.DurationSeconds != nil {
		t.Fatalf("unsupported was zero-filled: %#v", unsupported)
	}
	if got.Models[1].Results[1].CostUSD != nil || *got.Models[1].Results[1].DurationSeconds != 40 {
		t.Fatal("unknown cost or seconds were not preserved")
	}
	point := got.Pareto[1]
	if point.CostUSD != .1463149 || point.DurationMinutes != 5.059418471599929 || point.CompositeCost != .037453271500706826 || point.Score != 110.46964285714284 {
		t.Fatalf("not source chart coordinates: %#v", point)
	}
	// Synthetic ultra composite is deliberately not equal to a recomputed expression.
	if got.Pareto[0].CompositeCost != 1.6554321 {
		t.Fatal("source composite was recomputed")
	}
}

func TestParseIntelligenceRejectsOnlyUnusableOrOversizedSources(t *testing.T) {
	for name, raw := range map[string]string{
		"empty": "", "unrelated": "<html>unrelated</html>",
		"no measurements":      `<section data-intelligence-ui><div data-cir-panel="total"><table><tr data-model="safe-model"><td data-cir-column="new-effort">unknown</td></tr></table></div></section>`,
		"foreign payload only": `<script data-mtr-payload>{"schema":"other/1","rows":[{"model":"safe-model","effort":"new","score150":100}]}</script>`,
		"oversized":            strings.Repeat("x", maxSourceBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := parseIntelligence([]byte(raw)); err == nil {
				t.Fatal("unusable intelligence accepted")
			}
		})
	}
}

func TestParseIntelligenceIsolatesMalformedMeasurements(t *testing.T) {
	home := string(sourceFixture(t, "home.html"))
	for _, test := range []struct {
		name, old, replacement string
		points                 int
		check                  func(*testing.T, ScoreResult)
	}{
		{"score range", "<strong>125.73</strong>", "<strong>151</strong>", 1, func(t *testing.T, r ScoreResult) {
			if r.Score != nil || r.CostUSD == nil {
				t.Fatal("bad score not isolated")
			}
		}},
		{"negative cost", "3.57刀", "-3.57刀", 2, func(t *testing.T, r ScoreResult) {
			if r.CostUSD != nil || r.Score == nil {
				t.Fatal("bad cost not isolated")
			}
		}},
		{"nan", "3.57刀", "NaN刀", 2, nil},
		{"bad duration", "54.49分钟", "NaN分钟", 2, nil},
		{"infinite metric", "0.892", "1e999", 1, nil},
		{"wrong metric type", `"api_equivalent_usd_per_task":0.892`, `"api_equivalent_usd_per_task":"bad"`, 1, nil},
		{"mismatched score", `"score150":125.734`, `"score150":125`, 1, nil},
		{"wrong setting", `data-cir-setting="gpt-6.1-sol:ultra"`, `data-cir-setting="gpt-6-luna:ultra"`, 1, func(t *testing.T, r ScoreResult) {
			if r.Score != nil || r.CostUSD != nil || r.DurationSeconds != nil {
				t.Fatal("wrong-model cell attached")
			}
		}},
		{"malformed row", `"rows":[`, `"rows":[null,{"model":{}},`, 2, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, _, err := parseIntelligence([]byte(strings.Replace(home, test.old, test.replacement, 1)))
			if err != nil || len(got.Models) != 2 || len(got.Pareto) != test.points || *got.Models[0].Results[1].Score != 110.47 {
				t.Fatalf("measurement isolation: %#v err=%v", got, err)
			}
			if test.check != nil {
				test.check(t, got.Models[0].Results[0])
			}
		})
	}
}

func TestParseIntelligenceOptionalMetadataAndSafeFallbacks(t *testing.T) {
	home := string(sourceFixture(t, "home.html"))
	for name, raw := range map[string]string{
		"new schema":                     strings.Replace(home, "codex-model-tradeoffs/2", "codex-model-tradeoffs/99", 1),
		"missing schema":                 strings.Replace(home, `"schema":"codex-model-tradeoffs/2",`, "", 1),
		"foreign chart preserves matrix": strings.Replace(home, "codex-model-tradeoffs/2", "other/1", 1),
		"wrong axis metadata":            strings.Replace(home, `"x":"api_equivalent_usd_per_task"`, `"x":"other"`, 1),
		"new exponent":                   strings.Replace(home, `"time_exponent":2`, `"time_exponent":3`, 1),
		"missing round":                  strings.Replace(home, `"round_id":"codex-four-261-20261028",`, "", 1),
		"missing timestamp":              strings.Replace(home, "本轮测试：2026-10-28 11:50–13:38（北京时间）", "", 1),
		"bad date":                       strings.Replace(home, "2026-10-28 11:50", "2026-99-28 11:50", 1),
		"classes removed":                strings.ReplaceAll(strings.ReplaceAll(home, ` class="cir-test-time"`, ""), ` class="cir-sr"`, ""),
		"missing picker":                 strings.Replace(home, `<select data-cir-model>`, `<select>`, 1),
		"stale picker":                   strings.Replace(home, `<option value="gpt-6-luna">GPT-6 Luna</option>`, "", 1),
		"unsafe label":                   strings.ReplaceAll(home, "GPT-6.1 Sol", "https://evil.invalid/name"),
		"markup label":                   strings.ReplaceAll(home, "GPT-6.1 Sol", "&lt;img onerror=evil&gt;"),
		"payload missing":                strings.Replace(home, "data-mtr-payload", "data-unrelated", 1),
	} {
		t.Run(name, func(t *testing.T) {
			got, _, err := parseIntelligence([]byte(raw))
			if err != nil || len(got.Models) != 2 || *got.Models[0].Results[1].Score != 110.47 || *got.Models[0].Results[1].DurationSeconds != 1214.3 {
				t.Fatalf("optional metadata changed matrix: %#v err=%v", got, err)
			}
			for _, model := range got.Models {
				if !modelID(model.ID) || !safeText(model.Name, 128) {
					t.Fatal("unsafe model escaped")
				}
			}
		})
	}
}

func TestParseIntelligenceNewModelsEffortsAndEmbeddedRows(t *testing.T) {
	home := string(sourceFixture(t, "home.html"))
	newRow := `<tr data-model="future/model:v2"><th>Future Model</th><td data-cir-column="ADAPTIVE"><button data-cir-setting="future/model:v2:adaptive"><strong>130.12</strong><span data-cir-cost>8.25刀</span><span data-cir-duration>3分钟</span></button></td></tr>`
	home = strings.Replace(home, "</tbody></table></div>", newRow+"</tbody></table></div>", 1)
	home = strings.Replace(home, `"rows":[`, `"rows":[{"model":"future/model:v2","effort":"adaptive","score150":130.123,"chart_metrics":{"score150":130.123,"api_equivalent_usd_per_task":2.0625,"end_to_end_minutes_per_task":0.75,"composite_cost_index":7.123456789}},`, 1)
	got, _, err := parseIntelligence([]byte(home))
	if err != nil || len(got.Models) != 3 || len(got.Efforts) != 3 || len(got.Pareto) != 3 {
		t.Fatalf("expanded source: %#v err=%v", got, err)
	}
	r := got.Models[2].Results[2]
	if got.Models[2].ID != "future/model:v2" || r.Effort != "adaptive" || *r.Score != 130.12 || *r.CostUSD != 8.25 || *r.DurationSeconds != 180 || got.Pareto[0].CompositeCost != 7.123456789 {
		t.Fatal("new IDs or delivered units changed")
	}
	payloadOnly := `<script data-mtr-payload>{"max_score":200,"rows":[null,{"model":"next-model","effort":"adaptive","score150":180.125,"chart_metrics":{"api_equivalent_usd_per_task":1.25,"end_to_end_minutes_per_task":2.5,"composite_cost_index":4.125},"extra":{"future":true}},{"model":"next-model","effort":"turbo","score150":175}]}</script>`
	got, updated, err := parseIntelligence([]byte(payloadOnly))
	if err != nil || updated != nil || got.MaxScore != 200 || len(got.Models) != 1 || got.Models[0].Name != "next-model" || len(got.Efforts) != 2 || len(got.Pareto) != 1 || got.Models[0].Results[0].CostUSD != nil || got.Models[0].Results[0].DurationSeconds != nil {
		t.Fatalf("embedded measurements without invented totals: %#v err=%v", got, err)
	}
}

func TestParseIntelligenceGrowthAndBounds(t *testing.T) {
	rows := []map[string]any{}
	for i := 0; i < 40; i++ {
		for e := 0; e < 8; e++ {
			rows = append(rows, map[string]any{"model": fmt.Sprintf("model-%d", i), "effort": fmt.Sprintf("effort-%d", e), "score150": 100})
		}
	}
	payload := map[string]any{"rows": rows}
	raw, _ := json.Marshal(payload)
	got, _, err := parseIntelligence([]byte(`<script data-mtr-payload>` + string(raw) + `</script>`))
	if err != nil || len(got.Models) != 40 || len(got.Efforts) != 8 {
		t.Fatalf("reasonable growth failed: models=%d efforts=%d err=%v", len(got.Models), len(got.Efforts), err)
	}
	for name, rows := range map[string][]any{"rows": make([]any, maxScoreRows+1), "models": {}, "efforts": {}} {
		if name == "models" {
			for i := 0; i <= maxSourceModels; i++ {
				rows = append(rows, map[string]any{"model": fmt.Sprintf("model-%d", i), "effort": "low", "score150": 100})
			}
		}
		if name == "efforts" {
			for i := 0; i <= maxSourceEfforts; i++ {
				rows = append(rows, map[string]any{"model": "model", "effort": fmt.Sprintf("effort-%d", i), "score150": 100})
			}
		}
		raw, _ := json.Marshal(map[string]any{"rows": rows})
		if _, _, err := parseIntelligence([]byte(`<script data-mtr-payload>` + string(raw) + `</script>`)); err == nil {
			t.Fatalf("%s bound ignored", name)
		}
	}
}

func TestParseIntelligenceMatchesUpstreamAvailabilitySemantics(t *testing.T) {
	home := string(sourceFixture(t, "home.html"))
	for _, test := range []struct {
		name   string
		raw    string
		points int
	}{
		{"protocol metadata", strings.Replace(home, "codex-four-weighted-150-v2", "new-protocol-label", 1), 2},
		{"absent flags", strings.Replace(home, `,"availability":{"cost":true,"time":true,"composite":true}`, "", 1), 2},
		{"null flags", strings.Replace(home, `"availability":{"cost":true,"time":true,"composite":true}`, `"availability":null`, 1), 2},
		{"omitted flags", strings.Replace(home, `"availability":{"cost":true,"time":true,"composite":true}`, `"availability":{}`, 1), 2},
		{"missing coordinate", strings.Replace(home, `"composite_cost_index":1.6554321`, `"composite_cost_index":null`, 1), 1},
		{"explicitly unavailable", strings.Replace(home, `"availability":{"cost":true,"time":true,"composite":true}`, `"availability":{"cost":false,"time":true,"composite":true}`, 1), 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, _, err := parseIntelligence([]byte(test.raw))
			if err != nil || len(got.Pareto) != test.points || len(got.Models) != 2 {
				t.Fatalf("valid upstream projection: points=%d err=%v", len(got.Pareto), err)
			}
		})
	}
}

func TestParseQuotaUnitsAndNullReferenceSamples(t *testing.T) {
	got, updated, err := parseQuota(sourceFixture(t, "home.html"))
	if err != nil {
		t.Fatal(err)
	}
	if got.RecordedAt != "2026-10-17" || len(got.Plans) != 2 || got.Plans[0].MonthlyUSD != 20 || got.Plans[1].MonthlyUSD != 200 {
		t.Fatalf("quota = %#v", got)
	}
	model := got.Plans[0].Models[0]
	if model.Name != "GPT-6 Astra" || model.WeeklyTokens == nil || math.Abs(*model.WeeklyTokens-43000000) > 1e-6 || model.APIValueUSD == nil || *model.APIValueUSD != 66 {
		t.Fatalf("quota unit conversion = %#v", model)
	}
	if *got.Plans[1].Models[0].APIValueUSD != 1243 {
		t.Fatal("thousands separator not parsed")
	}
	unknown := got.Plans[1].Models[1]
	if unknown.WeeklyTokens != nil || unknown.APIValueUSD != nil {
		t.Fatal("no comparable sample was estimated")
	}
	if updated == nil || *updated != time.Date(2026, 10, 16, 16, 0, 0, 0, time.UTC).UnixMilli() {
		t.Fatalf("recorded date timezone = %v", updated)
	}
}

func TestParseQuotaToleratesOptionalMetadataAndIsolatesBadData(t *testing.T) {
	home := string(sourceFixture(t, "home.html"))
	for _, test := range []struct {
		name, raw string
		plans     int
	}{
		{"missing date", strings.Replace(home, "资料收录于 2026-10-17", "资料待确认", 1), 2},
		{"invalid date", strings.Replace(home, "2026-10-17", "2026-99-17", 1), 2},
		{"bad fee", strings.Replace(home, "<strong>$20</strong>", "<strong>$-20</strong>", 1), 1},
		{"bad tokens", strings.Replace(home, "≈ 0.43", "≈ -0.43", 1), 2},
		{"missing cell", strings.Replace(home, "<td>≈ 0.43</td>", "", 1), 2},
		{"unsafe label", strings.ReplaceAll(home, "GPT-6 Astra", "javascript:evil"), 2},
		{"bad model", strings.Replace(home, `data-quota-model="gpt-6-astra"`, `data-quota-model="https://evil.invalid"`, 1), 2},
		{"no-sample variant", strings.Replace(home, `colspan="2" class="quota-no-sample">无可比样本`, `colspan="2" data-future="yes">暂无数据`, 1), 2},
		{"missing classes", strings.ReplaceAll(strings.ReplaceAll(home, `class="quota-radar-head"`, ""), `class="quota-plan-price"`, ""), 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, _, err := parseQuota([]byte(test.raw))
			if err != nil || len(got.Plans) != test.plans {
				t.Fatalf("isolated quota: %#v err=%v", got, err)
			}
			for _, p := range got.Plans {
				for _, m := range p.Models {
					if !safeText(m.Name, 128) {
						t.Fatal("unsafe label escaped")
					}
				}
			}
			if test.name == "bad tokens" && (got.Plans[0].Models[0].WeeklyTokens != nil || got.Plans[0].Models[0].APIValueUSD == nil) {
				t.Fatal("malformed metric erased valid sibling")
			}
		})
	}
}

func TestParseQuotaRejectsUnusableReferences(t *testing.T) {
	for _, raw := range []string{"<html></html>", `<section id="quota-radar"><details data-quota-plan="plus"><summary><h3>Plus</h3><strong>$20</strong></summary><table><tr data-quota-model="model"><th>Model</th><td colspan="2">无样本</td></tr></table></details></section>`, strings.Repeat("x", maxSourceBytes+1)} {
		if _, _, err := parseQuota([]byte(raw)); err == nil {
			t.Fatal("no usable reference accepted")
		}
	}
}

func TestParseSpeedMatchesRenderedTableAndHistory(t *testing.T) {
	got, updated, err := parseSpeed(sourceFixture(t, "speed.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got.WindowMinutes != 5 || len(got.Models) != 2 || got.Models[0].ID != "gpt-6.1-sol" || *got.Models[0].TokensPerSecond != 25 || got.Models[0].Requests != 3 || got.Models[0].Devices != 1 {
		t.Fatalf("speed = %#v", got)
	}
	if got.Models[0].Harnesses[0].Name != "Codex" || got.Models[0].Harnesses[1].Name != "Claude CLI" || got.Models[1].Harnesses[0].Name != "ZCode" {
		t.Fatal("harness summary mapping differs from source")
	}
	history := got.Models[0].History
	if len(history) != 3 || history[1].Value != nil || *history[2].Value != 30 || history[2].AtMS-history[0].AtMS != 30000 {
		t.Fatalf("sparse spark history = %#v", history)
	}
	if updated == nil || *updated != time.Date(2026, 10, 28, 8, 48, 6, 121000000, time.UTC).UnixMilli() {
		t.Fatalf("asOf = %v", updated)
	}
}

func TestParseSpeedValidInsufficientRowsAreAcceptedButNotDisplayed(t *testing.T) {
	var p map[string]any
	if err := json.Unmarshal(sourceFixture(t, "speed.json"), &p); err != nil {
		t.Fatal(err)
	}
	items := p["items"].([]any)
	// The source's current table excludes this valid, positive-request null-speed row.
	p["items"] = []any{items[3]}
	raw, _ := json.Marshal(p)
	got, _, err := parseSpeed(raw)
	if err != nil || len(got.Models) != 0 {
		t.Fatalf("insufficient row: models=%d err=%v", len(got.Models), err)
	}
	// Displayed request totals need not be the same as the raw counts used to sort.
	first, second := items[0].(map[string]any), items[1].(map[string]any)
	first["requests"], second["requests"] = 1, 2
	p["items"] = []any{first, second}
	raw, _ = json.Marshal(p)
	got, _, err = parseSpeed(raw)
	if err != nil || len(got.Models) != 2 || got.Models[0].ID != "glm-5.3" || got.Models[1].Requests != 3 {
		t.Fatalf("raw-count ordering: %#v err=%v", got, err)
	}
}

func TestParseSpeedRejectsMalformedOrNonPublicData(t *testing.T) {
	raw := string(sourceFixture(t, "speed.json"))
	for name, body := range map[string]string{
		"empty": "", "not json": "<html>error</html>",
		"foreign contract": strings.Replace(raw, "deng-speed-public/1", "other/2", 1),
		"demo":             strings.Replace(raw, `"demo":false`, `"demo":true`, 1),
		"invalid demo":     strings.Replace(raw, `"demo":false`, `"demo":"true"`, 1),
		"not ok":           strings.Replace(raw, `"ok":true`, `"ok":false`, 1),
		"filter":           strings.Replace(raw, `"source":"all"`, `"source":"private"`, 1),
		"unit":             strings.Replace(raw, `"unit":"tok/s"`, `"unit":"tokens/min"`, 1),
		"window":           strings.Replace(raw, `"window":"5m"`, `"window":"1h"`, 1),
		"bad interval":     strings.Replace(raw, "08:43:05.000Z", "08:42:05.000Z", 1),
		"no measurements":  `{"unit":"tok/s","window":"5m","items":[{"model":"bad","requests":1,"devices":1,"outputTokS":"bad"}]}`,
		"oversized":        strings.Repeat("x", maxSourceBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := parseSpeed([]byte(body)); err == nil {
				t.Fatal("unsafe or unusable speed accepted")
			}
		})
	}
}

func TestParseSpeedIsolatesBadRowsAndNonessentialFields(t *testing.T) {
	raw := string(sourceFixture(t, "speed.json"))
	for _, test := range []struct {
		name, old, replacement string
		models                 int
	}{
		{"bad speed", `"outputTokS":25`, `"outputTokS":26`, 1},
		{"negative device", `"devices":1`, `"devices":-1`, 1},
		{"missing count", `"devices":1,`, "", 1},
		{"unsafe model", `"model":"gpt-6.1-sol"`, `"model":"<img>"`, 1},
		{"bad exponent", `"outputTokS":25`, `"outputTokS":1e999`, 1},
		{"bad speed type", `"outputTokS":25`, `"outputTokS":{"future":1}`, 1},
		{"bad harness", `"harnessRequests":[{"source":"codex","requests":2},{"source":"claudeCode","requests":1}]`, `"harnessRequests":[{"source":"https://evil.invalid","requests":2},{"source":"claudeCode","requests":1}]`, 2},
		{"bad usage", `"sourceUsage":[{"sources":["zcode"],"requests":1}]`, `"sourceUsage":{"unexpected":true}`, 2},
		{"bad spark length", `"spark":[20,null,30]`, `"spark":[20,null]`, 2},
		{"bad spark value", `"spark":[20,null,30]`, `"spark":[20,1e999,30]`, 2},
		{"bad spark metadata", `"bucketSeconds":15`, `"bucketSeconds":"new"`, 2},
		{"new contract", `deng-speed-public/1`, `deng-speed-public/42`, 2},
		{"warming", `"status":"live"`, `"status":"warming"`, 2},
		{"incomplete", `"complete":true`, `"complete":false`, 2},
		{"bad optional flag", `"lowSample":true`, `"lowSample":{}`, 2},
		{"bad row shape", `"items":[`, `"items":[null,{"model":{}},`, 2},
		{"unknown aggregation", `recent_received_timing_deltas`, `future_aggregation`, 2},
		{"missing counters", `"measuredOutput":100,"measuredMs":4000,`, "", 2},
		{"per-row demo", `"model":"gpt-6.1-sol"`, `"demo":true,"model":"gpt-6.1-sol"`, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, _, err := parseSpeed([]byte(strings.Replace(raw, test.old, test.replacement, 1)))
			if err != nil || len(got.Models) != test.models {
				t.Fatalf("row/optional isolation: %#v err=%v", got, err)
			}
			for _, m := range got.Models {
				if !modelID(m.ID) || !safeText(m.Name, 128) {
					t.Fatal("unsafe ID escaped")
				}
			}
			if test.name == "bad spark value" && got.Models[0].History[1].Value != nil {
				t.Fatal("invalid history value not null")
			}
		})
	}
}

func TestParseSpeedOptionalMetadataAndSourceTimestamps(t *testing.T) {
	var p map[string]any
	_ = json.Unmarshal(sourceFixture(t, "speed.json"), &p)
	for _, key := range []string{"contract", "kind", "complete", "aggregation", "filter", "spark", "demo", "ok"} {
		delete(p, key)
	}
	p["status"] = "warming"
	items := p["items"].([]any)
	first := items[0].(map[string]any)
	first["model"] = "brand-new-model"
	delete(first, "status")
	delete(first, "sourceUsage")
	delete(first, "harnessRequests")
	delete(first, "spark")
	raw, _ := json.Marshal(p)
	got, updated, err := parseSpeed(raw)
	if err != nil || updated == nil || len(got.Models) != 2 || got.Models[0].Name != "brand-new-model" || got.Models[0].Requests != 2 {
		t.Fatalf("minimal actual snapshot: %#v err=%v", got, err)
	}
	for _, stamp := range []any{nil, "not-a-date", "2026-10-28T08:47:06.121Z"} {
		p["asOf"] = stamp
		raw, _ = json.Marshal(p)
		got, updated, err = parseSpeed(raw)
		if err != nil || updated != nil || len(got.Models) != 2 {
			t.Fatalf("untimed measurements lost: %#v err=%v", got, err)
		}
		section := ageSpeed(Section[Speed]{Status: StatusReady, Data: &got, UpdatedAtMS: updated}, time.Date(2026, 10, 28, 8, 48, 7, 0, time.UTC))
		if section.Status != StatusUnavailable || section.Data != nil {
			t.Fatal("untimed source bypassed service stale policy")
		}
	}
	p["items"] = []any{}
	p["status"] = "warming"
	raw, _ = json.Marshal(p)
	if _, _, err = parseSpeed(raw); err == nil {
		t.Fatal("warming without measurements replaced last good snapshot")
	}
}

func TestParseSpeedExplicitEmptyAndRowCaps(t *testing.T) {
	var p map[string]any
	if err := json.Unmarshal(sourceFixture(t, "speed.json"), &p); err != nil {
		t.Fatal(err)
	}
	p["status"] = "empty"
	p["items"] = []any{}
	raw, _ := json.Marshal(p)
	got, _, err := parseSpeed(raw)
	if err != nil || got.Models == nil || len(got.Models) != 0 {
		t.Fatalf("explicit empty snapshot: %#v %v", got, err)
	}
	p["status"] = "live"
	p["items"] = make([]any, maxSpeedRows+1)
	raw, _ = json.Marshal(p)
	if _, _, err := parseSpeed(raw); err == nil {
		t.Fatal("over-limit rows accepted")
	}
}

func TestParsersSavedPublicResearchSamples(t *testing.T) {
	// Opt-in verification uses already downloaded public bytes, never the network.
	if os.Getenv("MODELINFO_RESEARCH_SAMPLES") != "1" {
		t.Skip("set MODELINFO_RESEARCH_SAMPLES=1 to check saved public research fixtures")
	}
	home, err := os.ReadFile("/tmp/gpt-load-codexradar.html")
	if err != nil {
		t.Fatal(err)
	}
	intel, _, err := parseIntelligence(home)
	if err != nil || len(intel.Models) != 8 || len(intel.Pareto) != 46 {
		t.Fatalf("actual intelligence: models=%d points=%d err=%v", len(intel.Models), len(intel.Pareto), err)
	}
	quota, _, err := parseQuota(home)
	if err != nil || len(quota.Plans) != 4 {
		t.Fatalf("actual quota: plans=%d err=%v", len(quota.Plans), err)
	}
	speed, err := os.ReadFile("/tmp/gpt-load-radar-research/speed-live.json")
	if err != nil {
		t.Fatal(err)
	}
	live, _, err := parseSpeed(speed)
	if err != nil || len(live.Models) != 16 {
		t.Fatalf("actual speed: models=%d err=%v", len(live.Models), err)
	}
	var scored, unsupported, quotaKnown, quotaMissing, historyPoints int
	for _, model := range intel.Models {
		for _, result := range model.Results {
			if result.Score == nil {
				unsupported++
			} else {
				scored++
			}
		}
	}
	for _, plan := range quota.Plans {
		for _, model := range plan.Models {
			if model.WeeklyTokens == nil && model.APIValueUSD == nil {
				quotaMissing++
			} else {
				quotaKnown++
			}
		}
	}
	for _, model := range live.Models {
		historyPoints += len(model.History)
	}
	if scored != 46 || unsupported != 2 || quotaKnown != 12 || quotaMissing != 4 || historyPoints != 320 {
		t.Fatalf("actual fixture coverage: scored=%d unsupported=%d quota known/missing=%d/%d history=%d", scored, unsupported, quotaKnown, quotaMissing, historyPoints)
	}
	t.Logf("Actual saved public fixture proof: intelligence models=%d scored=%d unsupported=%d Pareto=%d; quota plans=%d known=%d missing=%d; speed models=%d history=%d", len(intel.Models), scored, unsupported, len(intel.Pareto), len(quota.Plans), quotaKnown, quotaMissing, len(live.Models), historyPoints)
}
