package apikeyobservation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"gpt-load/internal/channel"
	"gpt-load/internal/channel/modules"
	"gpt-load/internal/channel/spec"
	providerobservation "gpt-load/internal/subscription/providers/observation"
	subscriptionruntime "gpt-load/internal/subscription/runtime"
)

const (
	zhipuQuotaURL = "https://open.bigmodel.cn/api/monitor/usage/quota/limit"
	quotaTimeout  = 10 * time.Second
	maxQuotaBody  = 1 << 20
)

// ZhipuObserver observes personal CN GLM Coding Plans. The ordinary PAYG preset,
// international service, team plans and arbitrary inference proxies are not opted in.
// client is a test seam; production always uses the frozen effective proxy policy.
type ZhipuObserver struct {
	client func(context.Context) (*http.Client, error)
}

func (ZhipuObserver) ID() spec.UtilityID { return modules.ZhipuAIQuotaObservation }

func (ZhipuObserver) SupportsTarget(target channel.ResolvedTarget) bool {
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

func (observer ZhipuObserver) Observe(ctx context.Context, request Request) (subscriptionruntime.Observation, error) {
	if !observer.SupportsTarget(request.Target) {
		return subscriptionruntime.Observation{}, errors.New("unsupported GLM quota target")
	}
	key, _ := request.Credential.Value("api_key")
	if strings.TrimSpace(key) == "" {
		return subscriptionruntime.Observation{}, errors.New("missing GLM quota credential")
	}
	ctx, cancel := context.WithTimeout(ctx, quotaTimeout)
	defer cancel()
	clientFactory := observer.client
	if clientFactory == nil {
		clientFactory = subscriptionruntime.HTTPClient
	}
	client, err := clientFactory(ctx)
	if err != nil {
		return subscriptionruntime.Observation{}, errors.New("initialize quota transport")
	}
	if client == nil {
		client = http.DefaultClient
	}
	// Never mutate a shared client or follow even same-origin redirects with the key.
	boundedClient := *client
	boundedClient.Timeout = quotaTimeout
	boundedClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, zhipuQuotaURL, nil)
	if err != nil {
		return subscriptionruntime.Observation{}, errors.New("build quota request")
	}
	req.Header.Set("Authorization", strings.TrimSpace(key))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Language", "en-US,en")
	resp, err := boundedClient.Do(req)
	if err != nil {
		return subscriptionruntime.Observation{}, errors.New("quota request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return subscriptionruntime.Observation{}, &subscriptionruntime.UpstreamHTTPError{StatusCode: resp.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxQuotaBody+1))
	if err != nil {
		return subscriptionruntime.Observation{}, errors.New("read quota response")
	}
	if len(body) > maxQuotaBody {
		return subscriptionruntime.Observation{}, subscriptionruntime.ErrObservationPayloadInvalid
	}
	payload, err := normalizeZhipuQuota(body)
	if err != nil {
		return subscriptionruntime.Observation{}, err
	}
	observation := subscriptionruntime.Observation{
		Payload: payload, AccountObserved: true, QuotaObserved: true,
		ObservedQuotaScopes: []string{"account"},
	}
	cards, err := observer.listResetCredits(ctx, request)
	if err != nil {
		// Quota remains usable, but absent cards are unknown, never a zero count.
		observation.Partial = true
		return observation, nil
	}
	var snapshot providerobservation.Snapshot
	if json.Unmarshal(payload, &snapshot) != nil {
		return subscriptionruntime.Observation{}, subscriptionruntime.ErrObservationPayloadInvalid
	}
	count, credits := projectZhipuResetCredits(cards)
	snapshot.ResetCreditsAvailable, snapshot.ResetCredits = &count, credits
	observation.Payload, err = json.Marshal(snapshot)
	return observation, err
}

type zhipuLimit struct {
	Type          string          `json:"type"`
	Unit          json.RawMessage `json:"unit"`
	Percentage    json.RawMessage `json:"percentage"`
	NextResetTime json.RawMessage `json:"nextResetTime"`
}

type zhipuWindow struct {
	percent float64
	reset   *int64
}

func normalizeZhipuQuota(body []byte) ([]byte, error) {
	invalid := subscriptionruntime.ErrObservationPayloadInvalid
	var envelope struct {
		Success *bool `json:"success"`
		Data    struct {
			Level  string       `json:"level"`
			Limits []zhipuLimit `json:"limits"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &envelope) != nil || (envelope.Success != nil && !*envelope.Success) {
		return nil, invalid
	}
	family := "CREDIT_LIMIT"
	for _, limit := range envelope.Data.Limits {
		if strings.EqualFold(strings.TrimSpace(limit.Type), "TOKENS_LIMIT") {
			family = "TOKENS_LIMIT"
			break
		}
	}
	var slots [2]*zhipuWindow
	var unclassified []zhipuWindow
	for _, limit := range envelope.Data.Limits {
		if !strings.EqualFold(strings.TrimSpace(limit.Type), family) {
			continue
		}
		percentage, ok := quotaNumber(limit.Percentage)
		if !ok || percentage < 0 || percentage > 100 {
			// A missing/invalid percentage is not evidence of a healthy zero-usage plan.
			return nil, invalid
		}
		unit, _ := quotaNumber(limit.Unit)
		window := zhipuWindow{percent: percentage, reset: quotaReset(limit.NextResetTime)}
		if unit == 3 || unit == 6 {
			index := 0
			if unit == 6 {
				index = 1
			}
			if slots[index] != nil {
				// Do not turn a duplicate explicit 5h bucket into an invented weekly bucket.
				return nil, invalid
			}
			slots[index] = &window
		} else {
			unclassified = append(unclassified, window)
		}
	}
	// Legacy plans omit unit. Normalize timestamps before sorting, including strings.
	sort.SliceStable(unclassified, func(i, j int) bool {
		if (unclassified[i].reset == nil) != (unclassified[j].reset == nil) {
			return unclassified[i].reset == nil
		}
		return unclassified[i].reset != nil && *unclassified[i].reset < *unclassified[j].reset
	})
	for _, window := range unclassified {
		for index := range slots {
			if slots[index] == nil {
				copy := window
				slots[index] = &copy
				break
			}
		}
	}
	snapshot := providerobservation.Snapshot{
		Plan:         providerobservation.PlanSummary{Name: strings.TrimSpace(envelope.Data.Level)},
		QuotaWindows: []providerobservation.QuotaWindow{},
	}
	for index, window := range slots {
		if window == nil {
			continue
		}
		id, label, labelKey, seconds := "5h", "5h", providerobservation.QuotaLabelSession, int64(18000)
		if index == 1 {
			id, label, labelKey, seconds = "weekly", "7d", providerobservation.QuotaLabelWeekly, 604800
		}
		used, total, remaining, utilization := window.percent, 100.0, 100-window.percent, window.percent/100
		state := "available"
		if window.percent == 100 {
			state = "exhausted"
		}
		snapshot.QuotaWindows = append(snapshot.QuotaWindows, providerobservation.QuotaWindow{
			ID: id, Label: label, LabelKey: labelKey, SourceID: "zhipuai_coding", Scope: "account", Unit: "percent",
			Used: &used, Limit: &total, Remaining: &remaining, Utilization: &utilization,
			ResetAtMS: window.reset, WindowSeconds: &seconds, State: state,
		})
	}
	if len(snapshot.QuotaWindows) == 0 {
		return nil, invalid
	}
	primary := 0
	for index := range snapshot.QuotaWindows {
		if *snapshot.QuotaWindows[index].Utilization > *snapshot.QuotaWindows[primary].Utilization {
			primary = index
		}
	}
	snapshot.QuotaWindows[primary].IsPrimary = true
	return json.Marshal(snapshot)
}

func quotaNumber(raw json.RawMessage) (float64, bool) {
	value := strings.TrimSpace(string(raw))
	if len(value) > 0 && value[0] == '"' {
		if json.Unmarshal(raw, &value) != nil {
			return 0, false
		}
	}
	number, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	return number, err == nil && !math.IsNaN(number) && !math.IsInf(number, 0)
}

func quotaReset(raw json.RawMessage) *int64 {
	if number, ok := quotaNumber(raw); ok {
		if number <= 0 || number > float64(math.MaxInt64)/1000 {
			return nil
		}
		if number < 1e12 {
			number *= 1000
		}
		ms := int64(number)
		return &ms
	}
	var value string
	if json.Unmarshal(bytes.TrimSpace(raw), &value) != nil {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil || parsed.UnixMilli() <= 0 {
		return nil
	}
	ms := parsed.UnixMilli()
	return &ms
}
