package codex

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	subscriptionruntime "gpt-load/internal/subscription/runtime"
)

func TestCodexResetCreditExpiryGuard(t *testing.T) {
	now := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	near, later := now.Add(10*time.Minute).UnixMilli(), now.Add(20*time.Minute).UnixMilli()
	card := func(expiry int64) string {
		return fmt.Sprintf(`{"status":"available","reset_type":"codex_rate_limits","expires_at":%q}`, time.UnixMilli(expiry).UTC().Format(time.RFC3339Nano))
	}
	conflict, invalid := "conflict", "invalid"
	for _, test := range []struct {
		name, body, wantError string
		expected              int64
		status                int
	}{
		{name: "unsorted full inventory", body: `{"available_count":2,"credits":[` + card(later) + `,` + card(near) + `]}`},
		{name: "bare list", body: `[` + card(near) + `]`},
		{name: "camel aliases", body: fmt.Sprintf(`{"availableCount":"1","items":[{"resetType":"codex_rate_limits","expiresAt":%q}]}`, time.UnixMilli(near).UTC().Format(time.RFC3339Nano))},
		{name: "data list", body: `{"data":[` + card(near) + `]}`},
		{name: "rate limit list", body: `{"rate_limit_reset_credits":[` + card(near) + `]}`},
		{name: "past available card ignored", body: `{"available_count":2,"items":[` + card(now.Add(-time.Minute).UnixMilli()) + `,` + card(near) + `]}`},
		{name: "unavailable and other type ignored", body: `{"available_count":1,"items":[{"status":"consumed"},{"status":"available","reset_type":"other"},` + card(near) + `]}`},
		{name: "one millisecond future", body: `[` + card(now.UnixMilli()+1) + `]`, expected: now.UnixMilli() + 1},
		{name: "thirty minute boundary", body: `[` + card(now.Add(30*time.Minute).UnixMilli()) + `]`, expected: now.Add(30 * time.Minute).UnixMilli()},
		{name: "beyond boundary", body: `[` + card(now.Add(30*time.Minute).UnixMilli()+1) + `]`, expected: now.Add(30*time.Minute).UnixMilli() + 1, wantError: conflict},
		{name: "expiry at now", body: `[` + card(now.UnixMilli()) + `]`, expected: now.UnixMilli(), wantError: conflict},
		{name: "all expired", body: `[` + card(now.UnixMilli()-1) + `]`, expected: now.UnixMilli() - 1, wantError: conflict},
		{name: "expected later card never selected", body: `[` + card(later) + `,` + card(near) + `]`, expected: later, wantError: conflict},
		{name: "expected disappeared", body: `[` + card(later) + `]`, wantError: conflict},
		{name: "empty inventory", body: `{"available_count":0,"credits":[]}`, wantError: conflict},
		{name: "count only", body: `{"available_count":1}`, wantError: invalid},
		{name: "null list", body: `{"available_count":1,"credits":null}`, wantError: invalid},
		{name: "null entry", body: `[` + card(near) + `,null]`, wantError: invalid},
		{name: "unknown expiry", body: `[` + card(near) + `,{}]`, wantError: invalid},
		{name: "invalid expiry", body: `[` + card(near) + `,{"expires_at":"not-an-expiry"}]`, wantError: invalid},
		{name: "nonpositive expiry", body: `[` + card(near) + `,` + card(0) + `]`, wantError: invalid},
		{name: "unknown availability", body: `[` + card(near) + `,{"status":"unknown"}]`, wantError: invalid},
		{name: "truncated count", body: `{"available_count":2,"credits":[` + card(near) + `]}`, wantError: invalid},
		{name: "smaller count", body: `{"available_count":0,"credits":[` + card(near) + `]}`, wantError: invalid},
		{name: "invalid count", body: `{"available_count":"invalid","credits":[` + card(near) + `]}`, wantError: invalid},
		{name: "negative count", body: `{"available_count":-1,"credits":[` + card(near) + `]}`, wantError: invalid},
		{name: "null count", body: `{"available_count":null,"credits":[` + card(near) + `]}`, wantError: invalid},
		{name: "contradictory count aliases", body: `{"available_count":1,"availableCount":2,"credits":[` + card(near) + `]}`, wantError: invalid},
		{name: "ambiguous lists", body: `{"credits":[` + card(near) + `],"items":[]}`, wantError: invalid},
		{name: "paginated list", body: `{"has_more":true,"credits":[` + card(near) + `]}`, wantError: invalid},
		{name: "camel paginated list", body: `{"hasMore":true,"items":[` + card(near) + `]}`, wantError: invalid},
		{name: "remaining cursor", body: `{"next_cursor":"more","credits":[` + card(near) + `]}`, wantError: invalid},
		{name: "remaining page token", body: `{"nextPageToken":"more","credits":[` + card(near) + `]}`, wantError: invalid},
		{name: "contradictory type aliases", body: `[{"reset_type":"codex_rate_limits","resetType":"other","expires_at":"2001-01-01T00:10:00Z"}]`, wantError: invalid},
		{name: "contradictory expiry aliases", body: `[{"expires_at":"2001-01-01T00:10:00Z","expiresAt":"2001-01-01T00:20:00Z"}]`, wantError: invalid},
		{name: "matching expiry aliases", body: `[{"expires_at":"2001-01-01T00:10:00Z","expiresAt":"2001-01-01T01:10:00+01:00"}]`},
		{name: "missing details", body: `{}`, wantError: invalid},
		{name: "null payload", body: `null`, wantError: invalid},
		{name: "upstream unauthorized", body: `{"secret":"synthetic-access"}`, status: 401, wantError: "upstream"},
		{name: "upstream unavailable", body: `{}`, status: 503, wantError: "upstream"},
	} {
		t.Run(test.name, func(t *testing.T) {
			driver := newCodexDriver()
			credential, err := driver.Parse([]byte(`{"type":"codex","access_token":"synthetic-access","refresh_token":"synthetic-refresh","account_id":"account-one"}`))
			if err != nil {
				t.Fatal(err)
			}
			action := driver.resetCreditAction()
			preparer, ok := action.(subscriptionruntime.ResetCreditPreparer)
			if !ok {
				t.Fatal("reset wrapper did not promote preparation")
			}
			calls := 0
			previous := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = previous })
			http.DefaultTransport = targetRoundTripper(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != http.MethodGet || req.URL.String() != "https://relay.example/team/backend-api/wham/rate-limit-reset-credits" || req.Header.Get("Authorization") != "Bearer synthetic-access" {
					t.Fatalf("unexpected preparation request: %s %s", req.Method, req.URL)
				}
				status := test.status
				if status == 0 {
					status = http.StatusOK
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(test.body)), Request: req}, nil
			})
			expected := test.expected
			if expected == 0 {
				expected = near
			}
			ctx := subscriptionruntime.WithResetCreditExpiryCondition(t.Context(), subscriptionruntime.ResetCreditExpiryCondition{ExpiresAtMS: expected, NowMS: now.UnixMilli()})
			data, err := preparer.PrepareResetCredit(ctx, credential, subscriptionruntime.NewTarget([]byte(`{"base_url":"https://relay.example/team"}`)))
			if calls != 1 {
				t.Fatalf("preparation made %d requests, want one inventory request", calls)
			}
			if test.wantError == "" {
				if err != nil || string(data) != fmt.Sprintf(`{"expires_at_ms":%d}`, expected) {
					t.Fatalf("marker/error = %s / %v", data, err)
				}
				return
			}
			if err == nil || data != nil || strings.Contains(err.Error(), "synthetic-access") {
				t.Fatalf("unsafe preparation = %s / %v", data, err)
			}
			var upstream *subscriptionruntime.UpstreamHTTPError
			switch test.wantError {
			case "invalid":
				if !errors.Is(err, subscriptionruntime.ErrObservationPayloadInvalid) {
					t.Fatalf("invalid inventory classification = %v", err)
				}
			case "conflict":
				if !errors.As(err, &upstream) || upstream.StatusCode != http.StatusConflict {
					t.Fatalf("stale condition classification = %v", err)
				}
			case "upstream":
				if !errors.As(err, &upstream) || upstream.StatusCode != test.status {
					t.Fatalf("upstream classification = %v", err)
				}
			}
		})
	}
}

func TestCodexResetCreditExpiryGuardManualPreparationIsNoop(t *testing.T) {
	preparer := newCodexDriver().resetCreditAction().(subscriptionruntime.ResetCreditPreparer)
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	http.DefaultTransport = targetRoundTripper(func(*http.Request) (*http.Response, error) {
		t.Fatal("manual preparation fetched inventory")
		return nil, errors.New("unexpected request")
	})
	data, err := preparer.PrepareResetCredit(t.Context(), subscriptionruntime.Credential{}, subscriptionruntime.NewTarget([]byte(`invalid`)))
	if err != nil || data != nil {
		t.Fatalf("manual preparation = %s / %v", data, err)
	}
}

func TestCodexResetCreditExpiryGuardStoredConsumeRetriesUseOnlyID(t *testing.T) {
	driver := newCodexDriver()
	credential, err := driver.Parse([]byte(`{"type":"codex","access_token":"synthetic-access","refresh_token":"synthetic-refresh","account_id":"account-one"}`))
	if err != nil {
		t.Fatal(err)
	}
	const id = "9f0f4c32-89d2-4bcb-9e19-052940dc2f16"
	calls := 0
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	http.DefaultTransport = targetRoundTripper(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != http.MethodPost || req.URL.Path != "/backend-api/wham/rate-limit-reset-credits/consume" {
			t.Fatalf("stored retry re-listed: %s %s", req.Method, req.URL)
		}
		var body map[string]any
		if json.NewDecoder(req.Body).Decode(&body) != nil || len(body) != 1 || body["redeem_request_id"] != id {
			t.Fatalf("consume body must remain ID only: %#v", body)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":"reset","windows_reset":2}`)), Request: req}, nil
	})
	ctx := subscriptionruntime.WithResetCreditExpiryCondition(t.Context(), subscriptionruntime.ResetCreditExpiryCondition{ExpiresAtMS: 1, NowMS: 100})
	stored := subscriptionruntime.ResetCreditRequest{ID: id, Data: json.RawMessage(`{"expires_at_ms":1}`)}
	for range 2 {
		result, err := driver.resetCreditAction().Consume(ctx, credential, subscriptionruntime.Target{}, stored)
		if err != nil || result.Status != "succeeded" || result.WindowsReset != 2 {
			t.Fatalf("stored consume = %#v / %v", result, err)
		}
	}
	if calls != 2 {
		t.Fatalf("consume made %d requests", calls)
	}
}

func TestCodexResetCreditExpiryGuardDefaultsToWallClock(t *testing.T) {
	expiry := time.Now().Add(5 * time.Minute).UTC().Truncate(time.Millisecond)
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	http.DefaultTransport = targetRoundTripper(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`[{"expires_at":%q}]`, expiry.Format(time.RFC3339Nano)))), Request: req}, nil
	})
	driver := newCodexDriver()
	credential, err := driver.Parse([]byte(`{"type":"codex","access_token":"synthetic-access","refresh_token":"synthetic-refresh","account_id":"account-one"}`))
	if err != nil {
		t.Fatal(err)
	}
	ctx := subscriptionruntime.WithResetCreditExpiryCondition(t.Context(), subscriptionruntime.ResetCreditExpiryCondition{ExpiresAtMS: expiry.UnixMilli()})
	if _, err := driver.PrepareResetCredit(ctx, credential, subscriptionruntime.Target{}); err != nil {
		t.Fatal(err)
	}
}
