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

	"gpt-load/internal/outboundproxy"
	providerobservation "gpt-load/internal/subscription/providers/observation"
	subscriptionruntime "gpt-load/internal/subscription/runtime"
)

const syntheticResetList = `{"code":200,"success":true,"data":{"targetType":"PERSONAL","fiveHourResets":[{"recordId":123,"grantType":"GLOBAL","expireTime":"2099-10-28 22:25:56","available":true},{"recordId":124,"grantType":"DIRECT","expireTime":"2099-10-28 20:25:56","available":true},{"recordId":125,"available":false}],"weekResets":[{"recordId":126,"grantType":"FUTURE_KIND","expireTime":"2099-10-28 21:25:56","available":true}]}}`

func TestZhipuResetListNormalizesOnlySafeAvailableCards(t *testing.T) {
	t.Parallel()
	cards, err := normalizeZhipuResetList([]byte(syntheticResetList), time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC))
	if err != nil || len(cards) != 3 || cards[0].selection.RecordID != 124 || cards[1].kind != "week" {
		t.Fatalf("cards/error = %#v/%v", cards, err)
	}
	expected := time.Date(2099, 10, 28, 12, 25, 56, 0, time.UTC).UnixMilli()
	if cards[0].expiresMS != expected {
		t.Fatalf("China-local expiry = %d, want %d", cards[0].expiresMS, expected)
	}
	count, credits := projectZhipuResetCredits(cards)
	if count != 3 || credits[1].Kind != "week" || credits[1].Available == nil || !*credits[1].Available {
		t.Fatal("typed card projection lost availability")
	}
	raw, _ := json.Marshal(credits)
	for _, forbidden := range []string{"recordId", "grantType", "FUTURE_KIND", "targetType", "api_key"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("browser card projection leaked %s", forbidden)
		}
	}
	for _, body := range []string{
		`{}`, `null`, `{`, `{"success":true,"code":200,"data":{"targetType":"TEAM","fiveHourResets":[],"weekResets":[]}}`,
		`{"success":true,"code":200,"data":{"targetType":"PERSONAL"}}`,
		strings.Replace(syntheticResetList, `"recordId":123`, `"recordId":0`, 1),
		strings.Replace(syntheticResetList, `"expireTime":"2099-10-28 22:25:56"`, `"expireTime":"invalid"`, 1),
		strings.Replace(syntheticResetList, `"available":true`, `"available":null`, 1),
	} {
		if _, err := normalizeZhipuResetList([]byte(body), time.Now()); err == nil {
			t.Fatal("invalid list accepted")
		}
	}
	expired := strings.ReplaceAll(syntheticResetList, "2099-", "2000-")
	if cards, err := normalizeZhipuResetList([]byte(expired), time.Now()); err != nil || len(cards) != 0 {
		t.Fatal("expired credits counted")
	}
}

func TestZhipuResetCardIdentityIncludesKindAndGrant(t *testing.T) {
	t.Parallel()
	body := strings.Replace(syntheticResetList, `"recordId":126`, `"recordId":123`, 1)
	body = strings.Replace(body, `"recordId":124`, `"recordId":123`, 1)
	if cards, err := normalizeZhipuResetList([]byte(body), time.Now()); err != nil || len(cards) != 3 {
		t.Fatal("same ID under distinct kind/grant must be allowed")
	}
	duplicate := strings.Replace(body, `"grantType":"DIRECT"`, `"grantType":"GLOBAL"`, 1)
	if _, err := normalizeZhipuResetList([]byte(duplicate), time.Now()); err == nil {
		t.Fatal("exact duplicate card accepted")
	}
}

func TestZhipuResetPrepareAndConsumeWireContract(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"five_hour", "week"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			observer := ZhipuObserver{client: func(ctx context.Context) (*http.Client, error) {
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > quotaTimeout {
					t.Fatal("unbounded reset request")
				}
				return &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					if req.Header.Get("Authorization") != "synthetic-quota-key" || req.Header.Get("Content-Type") != "application/json" {
						t.Fatal("incorrect reset authentication")
					}
					body := syntheticResetList
					if calls == 1 {
						if req.Method != "GET" || req.URL.String() != zhipuResetListURL {
							t.Fatal("incorrect official list endpoint")
						}
					} else {
						if req.Method != "POST" || req.URL.String() != zhipuResetUseURL {
							t.Fatal("consume re-listed or used incorrect endpoint")
						}
						var payload map[string]any
						if json.NewDecoder(req.Body).Decode(&payload) != nil || len(payload) != 5 || payload["requestId"] != "9f0f4c32-89d2-4bcb-9e19-052940dc2f16" || payload["targetType"] != "PERSONAL" {
							t.Fatal("incorrect reset use body")
						}
						id, resetType, grant := float64(124), "FIVE_HOUR", "DIRECT"
						if kind == "week" {
							id, resetType, grant = 126, "WEEK", "FUTURE_KIND"
						}
						if payload["recordId"] != id || payload["resetType"] != resetType || payload["grantType"] != grant {
							t.Fatal("incorrect selection; grant type must retain original value")
						}
						body = `{"code":200,"success":true,"data":true}`
					}
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
				})}, nil
			}}
			request := zhipuRequest(t)
			data, err := observer.PrepareResetCredit(t.Context(), request, kind)
			if err != nil {
				t.Fatal(err)
			}
			result, err := observer.ConsumeResetCredit(t.Context(), request, subscriptionruntime.ResetCreditRequest{ID: "9f0f4c32-89d2-4bcb-9e19-052940dc2f16", Data: data})
			wantWindows := 1
			if kind == "week" {
				wantWindows = 2
			}
			if err != nil || result.Status != "succeeded" || result.WindowsReset != wantWindows || calls != 2 {
				t.Fatalf("result/error/calls = %#v/%v/%d", result, err, calls)
			}
		})
	}
}

func TestZhipuResetFailuresAreSanitizedAndConservative(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, body         string
		status, classified int
	}{
		{"business unavailable", `{"code":400,"success":false,"msg":"指定的重置次数不可用，请刷新后重试"}`, 200, 409},
		{"business auth", `{"code":401,"success":false,"msg":"synthetic-quota-key"}`, 200, 401},
		{"business rejected", `{"code":400,"success":false,"msg":"synthetic-quota-key"}`, 200, 400},
		{"contradiction", `{"code":200,"success":false}`, 200, 0},
		{"business server", `{"code":500,"success":false}`, 200, 0},
		{"malformed", `{`, 200, 0}, {"oversize", strings.Repeat("x", maxQuotaBody+1), 200, 0},
		{"redirect", `synthetic-quota-key`, 302, 0}, {"unauthorized", `synthetic-quota-key`, 401, 401},
		{"server", `synthetic-quota-key`, 503, 503},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: test.status, Header: http.Header{"Location": []string{zhipuResetUseURL}}, Body: io.NopCloser(strings.NewReader(test.body)), Request: req}, nil
			})}
			observer := ZhipuObserver{client: func(context.Context) (*http.Client, error) { return client, nil }}
			_, err := observer.ConsumeResetCredit(t.Context(), zhipuRequest(t), subscriptionruntime.ResetCreditRequest{ID: "9f0f4c32-89d2-4bcb-9e19-052940dc2f16", Data: json.RawMessage(`{"resetType":"FIVE_HOUR","recordId":1,"grantType":"DIRECT","targetType":"PERSONAL"}`)})
			if err == nil || strings.Contains(err.Error(), "synthetic-quota-key") || calls != 1 || client.CheckRedirect != nil {
				t.Fatal("failure leaked data, followed redirect or changed shared client")
			}
			var upstream *subscriptionruntime.UpstreamHTTPError
			if test.classified > 0 && (!errors.As(err, &upstream) || upstream.StatusCode != test.classified) || test.classified == 0 && errors.As(err, &upstream) {
				t.Fatalf("classification = %v", err)
			}
		})
	}
}

func TestZhipuObserverAddsCardsAndKeepsQuotaOnListFailure(t *testing.T) {
	t.Parallel()
	for _, fail := range []bool{false, true} {
		observer := ZhipuObserver{client: func(context.Context) (*http.Client, error) {
			return &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				body, status := `{"data":{"limits":[{"type":"TOKENS_LIMIT","unit":3,"percentage":25}]}}`, 200
				if req.URL.String() == zhipuResetListURL {
					body = syntheticResetList
					if fail {
						status = 503
					}
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
			})}, nil
		}}
		result, err := observer.Observe(t.Context(), zhipuRequest(t))
		var snapshot providerobservation.Snapshot
		if err != nil || json.Unmarshal(result.Payload, &snapshot) != nil || len(snapshot.QuotaWindows) != 1 || result.Partial != fail {
			t.Fatalf("observation=%#v,%v", result, err)
		}
		if fail && snapshot.ResetCreditsAvailable != nil || !fail && (snapshot.ResetCreditsAvailable == nil || *snapshot.ResetCreditsAvailable != 3 || len(snapshot.ResetCredits) != 3) {
			t.Fatal("missing cards became zero, or safe cards missing")
		}
	}
}

func TestZhipuResetUsesFrozenNetworkAndCancellation(t *testing.T) {
	t.Parallel()
	calls := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "CONNECT" || r.Host != "www.bigmodel.cn:443" {
			t.Errorf("unexpected reset transport %s %s", r.Method, r.Host)
		}
		w.WriteHeader(502)
	}))
	defer proxy.Close()
	ctx := subscriptionruntime.WithNetworkContext(t.Context(), subscriptionruntime.NetworkContext{Proxy: outboundproxy.Effective{Config: outboundproxy.Config{Mode: outboundproxy.ModeCustom, URL: proxy.URL}}})
	if _, err := (ZhipuObserver{}).PrepareResetCredit(ctx, zhipuRequest(t), "week"); err == nil || calls != 1 {
		t.Fatal("reset ignored frozen network")
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := (ZhipuObserver{}).PrepareResetCredit(ctx, zhipuRequest(t), "week"); err == nil || calls != 1 {
		t.Fatal("reset ignored cancellation")
	}
}
