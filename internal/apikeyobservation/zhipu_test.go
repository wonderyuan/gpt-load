package apikeyobservation

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gpt-load/internal/channel"
	"gpt-load/internal/outboundproxy"
	providerobservation "gpt-load/internal/subscription/providers/observation"
	subscriptionruntime "gpt-load/internal/subscription/runtime"
)

func TestNormalizeZhipuQuota(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, limits string
		want         []float64
	}{
		{"explicit units beat reset order", `[{"type":"TOKENS_LIMIT","unit":6,"percentage":70,"nextResetTime":1700000000000},{"type":"TOKENS_LIMIT","unit":3,"percentage":"20","nextResetTime":1700000099999}]`, []float64{.2, .7}},
		{"credit-only explicit units", `[{"type":"CREDIT_LIMIT","unit":6,"percentage":90,"nextResetTime":1700000000000},{"type":"CREDIT_LIMIT","unit":3,"percentage":14,"nextResetTime":1700000099999}]`, []float64{.14, .9}},
		{"tokens exclude credits", `[{"type":"CREDIT_LIMIT","unit":6,"percentage":99},{"type":"TOKENS_LIMIT","unit":3,"percentage":15}]`, []float64{.15}},
		{"unitless missing reset first", `[{"type":"TOKENS_LIMIT","percentage":50,"nextResetTime":1700000000},{"type":"TOKENS_LIMIT","percentage":0}]`, []float64{0, .5}},
		{"unitless ISO sorted", `[{"type":"CREDIT_LIMIT","percentage":80,"nextResetTime":"2026-10-31T12:00:00Z"},{"type":"CREDIT_LIMIT","percentage":10,"nextResetTime":"2026-10-27T12:00:00Z"}]`, []float64{.1, .8}},
		{"zero and exhausted", `[{"type":"TOKENS_LIMIT","unit":3,"percentage":0},{"type":"TOKENS_LIMIT","unit":6,"percentage":100}]`, []float64{0, 1}},
		{"unknown type ignored", `[{"type":"TIME_LIMIT","percentage":99},{"type":"TOKENS_LIMIT","unit":3,"percentage":25}]`, []float64{.25}},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload, err := normalizeZhipuQuota([]byte(`{"success":true,"data":{"level":"lite","limits":` + test.limits + `}}`))
			if err != nil {
				t.Fatal(err)
			}
			var snapshot providerobservation.Snapshot
			if err := json.Unmarshal(payload, &snapshot); err != nil {
				t.Fatal(err)
			}
			if snapshot.Plan.Name != "lite" || len(snapshot.QuotaWindows) != len(test.want) {
				t.Fatalf("snapshot = %s", payload)
			}
			primary := 0
			for index, window := range snapshot.QuotaWindows {
				if window.Utilization == nil || *window.Utilization != test.want[index] || window.Unit != "percent" || window.Scope != "account" || window.WindowSeconds == nil {
					t.Fatalf("window %d = %#v", index, window)
				}
				seconds := int64(18000)
				if window.ID == "weekly" {
					seconds = 604800
				}
				if *window.WindowSeconds != seconds {
					t.Fatalf("duration = %d", *window.WindowSeconds)
				}
				if window.IsPrimary {
					primary++
				}
				if (*window.Utilization == 1) != (window.State == "exhausted") {
					t.Fatalf("state = %s", window.State)
				}
			}
			if primary != 1 {
				t.Fatalf("primary count = %d", primary)
			}
		})
	}
}

func TestNormalizeZhipuQuotaRejectsUnknownUsage(t *testing.T) {
	t.Parallel()
	for _, body := range []string{
		`{`, `null`, `{}`, `{"success":false,"msg":"not a coding plan","data":{"limits":[]}}`,
		`{"data":{"limits":[{"type":"TIME_LIMIT","percentage":50}]}}`,
		`{"data":{"limits":[{"type":"TOKENS_LIMIT","unit":3}]}}`,
		`{"data":{"limits":[{"type":"TOKENS_LIMIT","percentage":null}]}}`,
		`{"data":{"limits":[{"type":"TOKENS_LIMIT","percentage":"NaN"}]}}`,
		`{"data":{"limits":[{"type":"TOKENS_LIMIT","percentage":101}]}}`,
		`{"data":{"limits":[{"type":"TOKENS_LIMIT","percentage":-1}]}}`,
		`{"data":{"limits":[{"type":"TOKENS_LIMIT","unit":3,"percentage":10},{"type":"TOKENS_LIMIT","unit":3,"percentage":20}]}}`,
	} {
		if _, err := normalizeZhipuQuota([]byte(body)); !errors.Is(err, subscriptionruntime.ErrObservationPayloadInvalid) {
			t.Fatalf("body %s: error = %v", body, err)
		}
	}
}

func TestQuotaReset(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		raw string
		ms  int64
	}{
		{`1700000000`, 1700000000000}, {`1700000000000`, 1700000000000}, {`"1700000000"`, 1700000000000},
		{`"2023-11-14T22:13:20Z"`, 1700000000000}, {`null`, 0}, {`0`, 0}, {`-1`, 0}, {`"bad"`, 0},
	} {
		got := quotaReset(json.RawMessage(test.raw))
		if test.ms == 0 && got != nil || test.ms != 0 && (got == nil || *got != test.ms) {
			t.Fatalf("reset(%s) = %v", test.raw, got)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func zhipuRequest(t *testing.T) Request {
	t.Helper()
	registry := channel.NewRegistry()
	target, err := registry.Resolve(channel.ZhipuAI, json.RawMessage(`{"base_url":"https://open.bigmodel.cn/api/coding/paas/v4"}`))
	if err != nil {
		t.Fatal(err)
	}
	credential, err := registry.ValidateCredential(channel.ZhipuAI, json.RawMessage(`{"api_key":"synthetic-quota-key"}`))
	if err != nil {
		t.Fatal(err)
	}
	return Request{Credential: credential, Target: target}
}

func TestZhipuObserverRequestAndBoundaries(t *testing.T) {
	t.Parallel()
	request := zhipuRequest(t)
	for _, test := range []struct {
		name      string
		status    int
		body      string
		wantError bool
	}{
		{"success", 200, `{"data":{"limits":[{"type":"TOKENS_LIMIT","unit":3,"percentage":25}]}}`, false},
		{"business error", 200, `{"success":false,"msg":"synthetic-quota-key"}`, true},
		{"malformed", 200, `{`, true}, {"oversize", 200, strings.Repeat("x", maxQuotaBody+1), true},
		{"unauthorized", 401, "synthetic-quota-key", true}, {"forbidden", 403, "", true}, {"limited", 429, "", true}, {"server", 503, "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			observer := ZhipuObserver{client: func(ctx context.Context) (*http.Client, error) {
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > quotaTimeout {
					t.Fatal("unbounded context")
				}
				return &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					if req.Method != "GET" || req.Header.Get("Authorization") != "synthetic-quota-key" || req.Header.Get("Accept") != "application/json" {
						t.Fatalf("unexpected quota request method/headers")
					}
					body := test.body
					if calls == 2 && req.URL.String() == zhipuResetListURL {
						body = `{"code":200,"success":true,"data":{"targetType":"PERSONAL","fiveHourResets":[],"weekResets":[]}}`
					} else if req.URL.String() != zhipuQuotaURL {
						t.Fatal("unexpected quota request URL")
					}
					return &http.Response{StatusCode: test.status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: req}, nil
				})}, nil
			}}
			result, err := observer.Observe(t.Context(), request)
			wantCalls := 1
			if !test.wantError {
				wantCalls = 2
			}
			if (err != nil) != test.wantError || calls != wantCalls {
				t.Fatalf("calls/error = %d/%v", calls, err)
			}
			if err != nil && strings.Contains(err.Error(), "synthetic-quota-key") {
				t.Fatal("error leaked upstream response")
			}
			if test.status >= 300 {
				var upstream *subscriptionruntime.UpstreamHTTPError
				if !errors.As(err, &upstream) || upstream.StatusCode != test.status {
					t.Fatalf("classification = %v", err)
				}
			}
			if err == nil && (!result.QuotaObserved || len(result.Payload) == 0 || len(result.ObservedQuotaScopes) != 1) {
				t.Fatalf("result = %#v", result)
			}
		})
	}
}

func TestZhipuObserverBlocksRedirect(t *testing.T) {
	t.Parallel()
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://open.bigmodel.cn/elsewhere"}}, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
	})}
	observer := ZhipuObserver{client: func(context.Context) (*http.Client, error) { return client, nil }}
	_, err := observer.Observe(t.Context(), zhipuRequest(t))
	if calls != 1 || err == nil || client.CheckRedirect != nil {
		t.Fatalf("calls/error/shared client = %d/%v/%v", calls, err, client.CheckRedirect != nil)
	}
}

func TestZhipuObserverUsesNetworkProxyAndCancellation(t *testing.T) {
	t.Parallel()
	calls := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "CONNECT" || r.Host != "open.bigmodel.cn:443" {
			t.Errorf("proxy method/host = %s/%s", r.Method, r.Host)
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()
	ctx := subscriptionruntime.WithNetworkContext(t.Context(), subscriptionruntime.NetworkContext{Proxy: outboundproxy.Effective{Config: outboundproxy.Config{Mode: outboundproxy.ModeCustom, URL: proxy.URL}}})
	if _, err := (ZhipuObserver{}).Observe(ctx, zhipuRequest(t)); err == nil || calls != 1 {
		t.Fatalf("proxy calls/error = %d/%v", calls, err)
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := (ZhipuObserver{}).Observe(ctx, zhipuRequest(t)); err == nil || calls != 1 {
		t.Fatalf("cancel calls/error = %d/%v", calls, err)
	}
}
