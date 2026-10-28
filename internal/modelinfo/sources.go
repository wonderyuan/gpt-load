package modelinfo

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/net/html"
)

const (
	maxSourceBytes         = 4 << 20
	maxSourceModels        = 128
	maxSourceEfforts       = 32
	maxScoreRows           = 4096
	maxSpeedRows           = 256
	maxHistoryPoints       = 512
	maxSourceNumber        = 1e12
	maxSafeCount     int64 = 9007199254740991
)

var (
	errSourceContract   = errors.New("public model source contract mismatch")
	modelIDPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9._:/+\-]{0,63}$`)
	simpleIDPattern     = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)
	effortPattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9._+\-]{0,63}$`)
	datePattern         = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}\b`)
	testIntervalPattern = regexp.MustCompile(`(\d{4}-\d{2}-\d{2})\s+(\d{2}:\d{2})[–—-](\d{2}:\d{2})`)
	secondsPattern      = regexp.MustCompile(`·\s*([0-9]+(?:\.[0-9]+)?)\s*(?:秒|s)\s*$`)
)

func parseSourceHTML(raw []byte) (*html.Node, error) {
	if len(raw) == 0 || len(raw) > maxSourceBytes {
		return nil, errSourceContract
	}
	root, err := html.Parse(bytes.NewReader(raw))
	if err != nil {
		return nil, errSourceContract
	}
	return root, nil
}

func attr(n *html.Node, key string) string {
	if n != nil {
		for _, a := range n.Attr {
			if a.Key == key {
				return a.Val
			}
		}
	}
	return ""
}
func hasAttr(n *html.Node, key string) bool {
	if n != nil {
		for _, a := range n.Attr {
			if a.Key == key {
				return true
			}
		}
	}
	return false
}
func class(n *html.Node, name string) bool {
	for _, c := range strings.Fields(attr(n, "class")) {
		if c == name {
			return true
		}
	}
	return false
}
func elements(root *html.Node, match func(*html.Node) bool) []*html.Node {
	var found []*html.Node
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if n == nil {
			return
		}
		if n.Type == html.ElementNode && match(n) {
			found = append(found, n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(root)
	return found
}
func unique(root *html.Node, match func(*html.Node) bool) *html.Node {
	nodes := elements(root, match)
	if len(nodes) != 1 {
		return nil
	}
	return nodes[0]
}
func text(n *html.Node) string {
	var b strings.Builder
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if n == nil || n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style") {
			return
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(n)
	return strings.Join(strings.Fields(b.String()), " ")
}
func safeText(s string, limit int) bool {
	if s == "" || len(s) > limit || strings.TrimSpace(s) != s || strings.ContainsAny(s, "<>") {
		return false
	}
	lower := strings.ToLower(s)
	if strings.Contains(lower, "://") || strings.Contains(lower, "javascript:") || strings.Contains(lower, "data:") {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func modelID(s string) bool { return modelIDPattern.MatchString(s) && safeText(s, 64) }
func effort(s string) bool  { return effortPattern.MatchString(s) && safeText(s, 64) }
func number(n float64, max float64) bool {
	return !math.IsNaN(n) && !math.IsInf(n, 0) && n >= 0 && n <= max
}
func floatPtr(v float64) *float64 { return &v }
func missing(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "—", "–", "-", "unknown", "未知", "未测", "待测试", "n/a", "null", "无可比样本", "暂无样本", "no sample":
		return true
	}
	return false
}
func decimal(s string) (*float64, error) {
	s = strings.TrimSpace(s)
	if missing(s) {
		return nil, nil
	}
	s = strings.TrimSpace(strings.TrimPrefix(s, "≈"))
	s = strings.TrimSpace(strings.TrimPrefix(s, "$"))
	s = strings.ReplaceAll(s, ",", "")
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || !number(v, maxSourceNumber) {
		return nil, errSourceContract
	}
	return &v, nil
}
func moment(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || t.Year() < 2000 || t.Year() > 2200 {
		return time.Time{}, errSourceContract
	}
	return t, nil
}

// Decode measurements independently: a new optional field or one mistyped number
// must not make json.Unmarshal discard every other model in the response.
type sourceObject map[string]json.RawMessage

func object(raw json.RawMessage) sourceObject {
	var out sourceObject
	_ = json.Unmarshal(raw, &out)
	return out
}
func sourceString(raw json.RawMessage) string {
	var out string
	_ = json.Unmarshal(raw, &out)
	return out
}
func sourceNumber(raw json.RawMessage, max float64) *float64 {
	var out *float64
	if json.Unmarshal(raw, &out) != nil || out == nil || !number(*out, max) {
		return nil
	}
	return out
}
func sourceCount(raw json.RawMessage) *int64 {
	var out *int64
	if json.Unmarshal(raw, &out) != nil || out == nil || *out < 0 || *out > maxSafeCount {
		return nil
	}
	return out
}
func sourceBool(raw json.RawMessage) *bool {
	var out *bool
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}
func sourceRows(raw json.RawMessage) []json.RawMessage {
	var out []json.RawMessage
	_ = json.Unmarshal(raw, &out)
	return out
}
func schemaFamily(schema, family string) bool {
	return schema == "" || strings.HasPrefix(schema, family+"/") && modelID(strings.TrimPrefix(schema, family+"/"))
}
func labelOrID(label, id string) string {
	if safeText(label, 128) {
		return label
	}
	return id
}
func setting(s string) (string, string) {
	i := strings.LastIndexByte(s, ':')
	if i < 0 {
		return "", ""
	}
	id, e := s[:i], strings.ToLower(s[i+1:])
	if !modelID(id) || !effort(e) {
		return "", ""
	}
	return id, e
}
func renderedNumber(s string, max float64) *float64 {
	v, err := decimal(s)
	if err != nil || v != nil && !number(*v, max) {
		return nil
	}
	return v
}

func parseIntelligence(raw []byte) (Intelligence, *int64, error) {
	fail := func() (Intelligence, *int64, error) { return Intelligence{}, nil, errSourceContract }
	doc, err := parseSourceHTML(raw)
	if err != nil {
		return fail()
	}
	root := unique(doc, func(n *html.Node) bool { return hasAttr(n, "data-intelligence-ui") })
	if root == nil {
		root = doc
	}
	panel := unique(root, func(n *html.Node) bool {
		return attr(n, "data-cir-panel") == "total" || attr(n, "id") == "cir-panel-total"
	})
	payload := unique(doc, func(n *html.Node) bool {
		return n.Data == "script" && hasAttr(n, "data-mtr-payload")
	})
	var data sourceObject
	if payload != nil && payload.FirstChild != nil {
		data = object([]byte(payload.FirstChild.Data))
		if !schemaFamily(sourceString(data["schema"]), "codex-model-tradeoffs") {
			data = nil // A foreign chart schema cannot invalidate a recognizable total matrix.
		}
	}
	rows := sourceRows(data["rows"])
	if len(rows) > maxScoreRows {
		return fail()
	}
	out := Intelligence{MaxScore: 150, Efforts: []string{}, Models: []ScoreModel{}, Pareto: []ParetoPoint{}}
	if max := sourceNumber(data["max_score"], 1e6); max != nil && *max > 0 {
		out.MaxScore = *max
	}
	if batch := sourceString(data["round_id"]); safeText(batch, 128) {
		out.Batch = batch
	}
	modelNames := map[string]string{}
	for _, selectNode := range elements(root, func(n *html.Node) bool { return n.Data == "select" && hasAttr(n, "data-cir-model") }) {
		for _, option := range elements(selectNode, func(n *html.Node) bool { return n.Data == "option" }) {
			id := attr(option, "value")
			if modelID(id) && modelNames[id] == "" {
				modelNames[id] = labelOrID(text(option), id)
			}
		}
	}
	seenEfforts := map[string]bool{}
	addEffort := func(e string) bool {
		if !effort(e) {
			return false
		}
		if !seenEfforts[e] {
			seenEfforts[e] = true
			out.Efforts = append(out.Efforts, e)
		}
		return true
	}
	head := unique(panel, func(n *html.Node) bool { return n.Data == "thead" })
	for i, th := range elements(head, func(n *html.Node) bool { return n.Data == "th" }) {
		if i > 0 {
			addEffort(strings.ToLower(text(th)))
		}
	}
	models := map[string]int{}
	results := map[string]map[string]ScoreResult{}
	matrixScores := map[string]*float64{}
	trs := elements(panel, func(n *html.Node) bool { return n.Data == "tr" })
	if len(trs) > maxSourceModels+1 {
		return fail()
	}
	for _, tr := range trs {
		cells := elements(tr, func(n *html.Node) bool { return n.Data == "td" && hasAttr(n, "data-cir-column") })
		if len(cells) == 0 {
			continue
		}
		if len(cells) > maxSourceEfforts {
			return fail()
		}
		label := unique(tr, func(n *html.Node) bool { return n.Data == "th" && attr(n, "scope") == "row" })
		if label == nil {
			label = unique(tr, func(n *html.Node) bool { return n.Data == "th" })
		}
		name, id := text(label), ""
		for _, key := range []string{"data-cir-model", "data-model", "data-model-id"} {
			if candidate := attr(tr, key); modelID(candidate) {
				id = candidate
				break
			}
		}
		if id == "" && safeText(name, 128) {
			for candidate, modelName := range modelNames {
				if modelName == name {
					if id != "" {
						id = "" // Ambiguous labels are not an identifier.
						break
					}
					id = candidate
				}
			}
		}
		if id == "" {
			candidates := map[string]bool{}
			for _, button := range elements(tr, func(n *html.Node) bool { return hasAttr(n, "data-cir-setting") }) {
				candidate, _ := setting(attr(button, "data-cir-setting"))
				if candidate != "" {
					candidates[candidate] = true
				}
			}
			if len(candidates) == 1 {
				for candidate := range candidates {
					id = candidate
				}
			}
		}
		if id == "" {
			continue
		}
		if _, duplicate := models[id]; duplicate {
			continue
		}
		modelNames[id] = labelOrID(name, id)
		models[id] = len(out.Models)
		out.Models = append(out.Models, ScoreModel{ID: id, Name: modelNames[id], Results: []ScoreResult{}})
		results[id] = map[string]ScoreResult{}
		for _, cell := range cells {
			e := strings.ToLower(attr(cell, "data-cir-column"))
			if !addEffort(e) {
				continue
			}
			if _, duplicate := results[id][e]; duplicate {
				continue
			}
			result := ScoreResult{Effort: e}
			buttons := elements(cell, func(n *html.Node) bool { return hasAttr(n, "data-cir-setting") })
			if len(buttons) == 1 {
				button := buttons[0]
				buttonModel, buttonEffort := setting(attr(button, "data-cir-setting"))
				if buttonModel == id && buttonEffort == e {
					strong := unique(button, func(n *html.Node) bool { return n.Data == "strong" })
					result.Score = renderedNumber(text(strong), out.MaxScore)
					cost := unique(button, func(n *html.Node) bool { return hasAttr(n, "data-cir-cost") })
					costText := strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(text(cost), "刀"), " USD"))
					result.CostUSD = renderedNumber(costText, maxSourceNumber)
					duration := unique(button, func(n *html.Node) bool { return hasAttr(n, "data-cir-duration") })
					result.DurationSeconds, _ = durationSeconds(text(duration))
					// The accessible line retains tenths of seconds; its CSS class is irrelevant.
					if m := secondsPattern.FindStringSubmatch(text(button)); m != nil {
						result.DurationSeconds = renderedNumber(m[1], maxSourceNumber)
					}
				}
			}
			results[id][e] = result
			matrixScores[id+"--"+e] = result.Score
		}
	}
	// A recognizable embedded row can introduce a model even before the optional
	// model picker is updated. Chart per-task cost/time are NEVER used as totals.
	renderedModels := map[string]bool{}
	for id := range models {
		renderedModels[id] = true
	}
	for _, rawRow := range rows {
		row := object(rawRow)
		id, e := sourceString(row["model"]), strings.ToLower(sourceString(row["effort"]))
		if !modelID(id) || !effort(e) {
			continue
		}
		metrics := object(row["chart_metrics"])
		score := sourceNumber(row["score150"], out.MaxScore)
		if score == nil {
			score = sourceNumber(metrics["score150"], out.MaxScore)
		}
		if score == nil || renderedModels[id] {
			continue
		}
		if _, exists := models[id]; !exists {
			models[id] = len(out.Models)
			out.Models = append(out.Models, ScoreModel{ID: id, Name: labelOrID(modelNames[id], id), Results: []ScoreResult{}})
			results[id] = map[string]ScoreResult{}
		}
		if _, duplicate := results[id][e]; !duplicate {
			addEffort(e)
			results[id][e] = ScoreResult{Effort: e, Score: score}
			matrixScores[id+"--"+e] = score
		}
	}
	if len(out.Models) > maxSourceModels || len(out.Efforts) > maxSourceEfforts {
		return fail()
	}
	usable := false
	for i := range out.Models {
		for _, e := range out.Efforts {
			result, exists := results[out.Models[i].ID][e]
			if !exists {
				result = ScoreResult{Effort: e}
			}
			usable = usable || result.Score != nil || result.CostUSD != nil || result.DurationSeconds != nil
			out.Models[i].Results = append(out.Models[i].Results, result)
		}
	}
	if !usable {
		return fail()
	}
	seenPoints := map[string]bool{}
	for _, rawRow := range rows {
		row := object(rawRow)
		id, e := sourceString(row["model"]), strings.ToLower(sourceString(row["effort"]))
		key := id + "--" + e
		matrix := matrixScores[key]
		metrics := object(row["chart_metrics"])
		score := sourceNumber(metrics["score150"], out.MaxScore)
		rowScore := sourceNumber(row["score150"], out.MaxScore)
		if score == nil && len(metrics["score150"]) == 0 {
			score = rowScore
		}
		cost := sourceNumber(metrics["api_equivalent_usd_per_task"], maxSourceNumber)
		minutes := sourceNumber(metrics["end_to_end_minutes_per_task"], maxSourceNumber)
		composite := sourceNumber(metrics["composite_cost_index"], maxSourceNumber)
		if matrix == nil || score == nil || cost == nil || minutes == nil || composite == nil || seenPoints[key] || math.Abs(*matrix-*score) > .011 || rowScore != nil && math.Abs(*rowScore-*score) > 1e-9 {
			continue
		}
		if suppliedID := sourceString(row["id"]); suppliedID != "" && suppliedID != key {
			continue
		}
		available := true
		flags := object(row["availability"])
		for _, axis := range []string{"cost", "time", "composite"} {
			if rawFlag := flags[axis]; len(rawFlag) > 0 && string(rawFlag) != "null" {
				flag := sourceBool(rawFlag)
				available = available && flag != nil && *flag
			}
		}
		if available {
			seenPoints[key] = true
			out.Pareto = append(out.Pareto, ParetoPoint{ID: key, ModelID: id, Model: out.Models[models[id]].Name, Effort: e, Score: *score, CostUSD: *cost, DurationMinutes: *minutes, CompositeCost: *composite})
		}
	}
	label := unique(root, func(n *html.Node) bool { return class(n, "cir-test-time") })
	if label == nil {
		label = unique(root, func(n *html.Node) bool {
			return (n.Data == "p" || n.Data == "time") && testIntervalPattern.MatchString(text(n))
		})
	}
	if tested := text(label); safeText(tested, 256) {
		out.TestedAt = tested
	}
	interval := testIntervalPattern.FindStringSubmatch(out.TestedAt)
	if interval != nil {
		updated, endErr := moment(interval[1] + "T" + interval[3] + ":00+08:00")
		started, startErr := moment(interval[1] + "T" + interval[2] + ":00+08:00")
		if startErr == nil && endErr == nil && !updated.Before(started) {
			ms := updated.UnixMilli()
			return out, &ms, nil
		}
	}
	return out, nil, nil
}

func durationSeconds(s string) (*float64, error) {
	if missing(s) {
		return nil, nil
	}
	for _, unit := range []struct {
		suffix string
		factor float64
	}{{"分钟", 60}, {" min", 60}, {"秒", 1}, {"s", 1}} {
		if strings.HasSuffix(s, unit.suffix) {
			v, err := decimal(strings.TrimSuffix(s, unit.suffix))
			if err != nil || v == nil || !number(*v*unit.factor, maxSourceNumber) {
				return nil, errSourceContract
			}
			return floatPtr(*v * unit.factor), nil
		}
	}
	return nil, errSourceContract
}

func parseQuota(raw []byte) (Quota, *int64, error) {
	fail := func() (Quota, *int64, error) { return Quota{}, nil, errSourceContract }
	doc, err := parseSourceHTML(raw)
	if err != nil {
		return fail()
	}
	root := unique(doc, func(n *html.Node) bool { return attr(n, "id") == "quota-radar" })
	if root == nil {
		return fail()
	}
	out := Quota{Plans: []QuotaPlan{}}
	header := unique(root, func(n *html.Node) bool { return class(n, "quota-radar-head") })
	if header == nil {
		header = unique(root, func(n *html.Node) bool { return n.Data == "h2" })
	}
	date := datePattern.FindString(text(header))
	var updated *int64
	if at, err := moment(date + "T00:00:00+08:00"); err == nil {
		out.RecordedAt = date
		ms := at.UnixMilli()
		updated = &ms
	}
	plans := elements(root, func(n *html.Node) bool { return hasAttr(n, "data-quota-plan") })
	if len(plans) > 32 {
		return fail()
	}
	seenPlans, known := map[string]bool{}, false
	for _, node := range plans {
		id := attr(node, "data-quota-plan")
		name := text(unique(node, func(n *html.Node) bool { return n.Data == "h3" }))
		price := unique(node, func(n *html.Node) bool { return class(n, "quota-plan-price") })
		if price == nil {
			price = unique(node, func(n *html.Node) bool { return n.Data == "summary" })
		}
		monthly := renderedNumber(text(unique(price, func(n *html.Node) bool { return n.Data == "strong" })), maxSourceNumber)
		if !simpleIDPattern.MatchString(id) || seenPlans[id] || monthly == nil {
			continue
		}
		seenPlans[id] = true
		plan := QuotaPlan{ID: id, Name: labelOrID(name, id), MonthlyUSD: *monthly, Models: []QuotaModel{}}
		rows := elements(node, func(n *html.Node) bool { return n.Data == "tr" && hasAttr(n, "data-quota-model") })
		if len(rows) > maxSourceModels {
			return fail()
		}
		seenModels := map[string]bool{}
		for _, row := range rows {
			id := attr(row, "data-quota-model")
			if !modelID(id) || seenModels[id] {
				continue
			}
			name := text(unique(row, func(n *html.Node) bool { return n.Data == "th" }))
			model := QuotaModel{Name: labelOrID(name, id)}
			cells := elements(row, func(n *html.Node) bool { return n.Data == "td" })
			switch {
			case len(cells) == 1 && attr(cells[0], "colspan") == "2":
				// Any merged no-sample variant is null, never an estimate or zero.
			case len(cells) == 2:
				model.WeeklyTokens = renderedNumber(text(cells[0]), maxSourceNumber)
				model.APIValueUSD = renderedNumber(text(cells[1]), maxSourceNumber)
				if model.WeeklyTokens != nil {
					converted := *model.WeeklyTokens * 1e8
					model.WeeklyTokens = nil
					if number(converted, 1e18) {
						model.WeeklyTokens = floatPtr(converted)
					}
				}
			default:
				continue // A missing column cannot safely be assigned a unit.
			}
			known = known || model.WeeklyTokens != nil || model.APIValueUSD != nil
			seenModels[id] = true
			plan.Models = append(plan.Models, model)
		}
		if len(plan.Models) > 0 {
			out.Plans = append(out.Plans, plan)
		}
	}
	if !known {
		return fail()
	}
	return out, updated, nil
}

var speedNames = map[string]string{
	"gpt-6.1-sol": "GPT-6.1 Sol", "gpt-6-astra": "GPT-6 Astra", "gpt-6-luna": "GPT-6 Luna", "gpt-6-sol": "GPT-6 Sol", "gpt-5.6-sol": "GPT-5.6 Sol", "gpt-5.6-terra": "GPT-5.6 Terra", "gpt-5.6-luna": "GPT-5.6 Luna", "deepseek-flash": "DeepSeek Flash", "claude-opus-5-5": "Claude Opus 5.5", "claude-opus-5": "Claude Opus 5", "glm-5.3": "GLM-5.3", "glm-5.3-flash": "GLM-5.3 Flash", "minimax-m3.1-flash": "MiniMax M3.1 Flash",
}
var harnessNames = map[string]string{"codex": "Codex", "claudeCode": "Claude CLI", "claudeDesktop": "Claude Desktop", "deepseek": "DSH", "opencode": "OpenCode", "gemini": "Gemini CLI", "omp": "oh-my-pi", "antigravity": "Antigravity", "workbuddy": "WorkBuddy", "workbuddyAi": "WorkBuddy AI", "zcode": "ZCode", "kimi": "Kimi Code CLI", "grokBuild": "Grok Build"}

// Optional harness and history metadata are validated separately from speeds.
func speedHarnesses(row sourceObject) []Harness {
	counts := map[string]int64{}
	if rows := sourceRows(row["harnessRequests"]); len(rows) <= 128 {
		for _, raw := range rows {
			h := object(raw)
			source, requests := sourceString(h["source"]), sourceCount(h["requests"])
			if simpleIDPattern.MatchString(source) && requests != nil && *requests > 0 {
				if _, duplicate := counts[source]; !duplicate {
					counts[source] = *requests
				}
			}
		}
	}
	if len(counts) == 0 {
		if rows := sourceRows(row["sourceUsage"]); len(rows) <= 128 {
			for _, raw := range rows {
				u := object(raw)
				sources, requests := sourceRows(u["sources"]), sourceCount(u["requests"])
				if len(sources) != 1 || requests == nil || *requests == 0 {
					continue
				}
				source := sourceString(sources[0])
				if simpleIDPattern.MatchString(source) && counts[source] <= maxSafeCount-*requests {
					counts[source] += *requests
				}
			}
		}
	}
	keys := make([]string, 0, len(counts))
	for source := range counts {
		keys = append(keys, source)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})
	out := []Harness{}
	var total int64
	for _, source := range keys {
		if total > maxSafeCount-counts[source] {
			return []Harness{} // Keep the measured speed and raw request count instead.
		}
		total += counts[source]
		out = append(out, Harness{Name: labelOrID(harnessNames[source], source), Requests: counts[source]})
	}
	return out
}

func speedHistory(meta sourceObject, raw json.RawMessage) []SpeedPoint {
	out := []SpeedPoint{}
	from, err := moment(sourceString(meta["from"]))
	count, bucket := sourceCount(meta["count"]), sourceCount(meta["bucketSeconds"])
	values := sourceRows(raw)
	if err != nil || count == nil || bucket == nil || *count < 1 || *count > maxHistoryPoints || *bucket < 1 || *bucket > 300 || len(values) != int(*count) {
		return out
	}
	for i, value := range values {
		out = append(out, SpeedPoint{AtMS: from.Add(time.Duration(int64(i)**bucket) * time.Second).UnixMilli(), Value: sourceNumber(value, 100000)})
	}
	return out
}

func parseSpeed(raw []byte) (Speed, *int64, error) {
	fail := func() (Speed, *int64, error) { return Speed{}, nil, errSourceContract }
	if len(raw) == 0 || len(raw) > maxSourceBytes || !json.Valid(raw) {
		return fail()
	}
	p := object(raw)
	if p == nil || !schemaFamily(sourceString(p["contract"]), "deng-speed-public") || sourceString(p["window"]) != "5m" || sourceString(p["unit"]) != "tok/s" {
		return fail()
	}
	for _, key := range []string{"ok", "demo"} {
		if value := p[key]; len(value) > 0 {
			flag := sourceBool(value)
			if flag == nil || key == "ok" && !*flag || key == "demo" && *flag {
				return fail()
			}
		}
	}
	if kind := sourceString(p["kind"]); kind != "" && kind != "models" {
		return fail()
	}
	filter := object(p["filter"])
	if source := sourceString(filter["source"]); source != "" && source != "all" {
		return fail()
	}
	for _, key := range []string{"model", "effort"} {
		if value := bytes.TrimSpace(filter[key]); len(value) > 0 && string(value) != "null" {
			return fail()
		}
	}
	items := sourceRows(p["items"])
	if items == nil || len(items) > maxSpeedRows {
		return fail()
	}
	from, fromErr := moment(sourceString(p["from"]))
	to, toErr := moment(sourceString(p["to"]))
	asOf, asOfErr := moment(sourceString(p["asOf"]))
	if fromErr == nil && toErr == nil && to.Sub(from) != 5*time.Minute {
		return fail()
	}
	var updated *int64
	if asOfErr == nil && (toErr != nil || !asOf.Before(to) && asOf.Sub(to) < 10*time.Second) {
		ms := asOf.UnixMilli()
		updated = &ms
	}
	// Do not invent a publication time. The service deliberately hides untimed
	// speed data and still applies its existing stale/expiry policy.
	out := Speed{WindowMinutes: 5, Models: []SpeedModel{}}
	seen := map[string]bool{}
	sourceRequests := map[string]int64{}
	legitimateEmpty := len(items) == 0 && sourceString(p["status"]) == "empty"
	for _, rawRow := range items {
		r := object(rawRow)
		id, status := sourceString(r["model"]), sourceString(r["status"])
		requests, devices := sourceCount(r["requests"]), sourceCount(r["devices"])
		output := sourceNumber(r["outputTokS"], 100000)
		if !modelID(id) || seen[id] || requests == nil || devices == nil {
			continue
		}
		if demo := sourceBool(r["demo"]); demo != nil && *demo {
			continue
		}
		if output == nil {
			if string(bytes.TrimSpace(r["outputTokS"])) == "null" && (status == "insufficient" || status == "empty") {
				legitimateEmpty = true
			}
			continue
		}
		if grouped := sourceBool(r["grouped"]); grouped != nil && *grouped {
			continue
		}
		if id == "unknown" || id == "local" || id == "other" || *requests == 0 || *output <= 0 || status != "" && status != "live" && status != "warming" {
			continue
		}
		measuredOutput, measuredMS := sourceCount(r["measuredOutput"]), sourceCount(r["measuredMs"])
		if len(r["measuredOutput"]) > 0 && measuredOutput == nil || len(r["measuredMs"]) > 0 && measuredMS == nil {
			continue
		}
		if measuredOutput != nil && measuredMS != nil && (*measuredMS < 500 || math.Abs(*output-float64(*measuredOutput)*1000/float64(*measuredMS)) > 1e-9*math.Max(1, *output)) {
			continue
		}
		seen[id] = true
		model := SpeedModel{ID: id, Name: labelOrID(speedNames[id], id), TokensPerSecond: output, Requests: *requests, Devices: *devices, Harnesses: speedHarnesses(r), History: speedHistory(object(p["spark"]), r["spark"])}
		if len(model.Harnesses) > 0 {
			model.Requests = 0
			for _, h := range model.Harnesses {
				model.Requests += h.Requests
			}
		}
		out.Models = append(out.Models, model)
		sourceRequests[id] = *requests
	}
	if len(out.Models) == 0 && !legitimateEmpty {
		return fail()
	}
	// Match the source table: sort raw request counts, display harness totals.
	sort.Slice(out.Models, func(i, j int) bool {
		left, right := sourceRequests[out.Models[i].ID], sourceRequests[out.Models[j].ID]
		if left != right {
			return left > right
		}
		return out.Models[i].ID < out.Models[j].ID
	})
	return out, updated, nil
}
