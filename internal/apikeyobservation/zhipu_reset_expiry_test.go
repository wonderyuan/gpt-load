package apikeyobservation

import (
	"context"
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

func zhipuExpiryTestList(fiveHour, week string) string {
	return `{"code":200,"success":true,"data":{"targetType":"PERSONAL","fiveHourResets":[` + fiveHour + `],"weekResets":[` + week + `]}}`
}

func zhipuExpiryTestCard(id int, expiry int64) string {
	return fmt.Sprintf(`{"recordId":%d,"grantType":"ORIGINAL_GRANT","expireTime":%q,"available":true}`, id, time.UnixMilli(expiry).UTC().Format(time.RFC3339Nano))
}

func TestZhipuResetCreditExpiryGuard(t *testing.T) {
	t.Parallel()
	// A historical clock catches accidental use of time.Now during normalization.
	now := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	near, later := now+(10*time.Minute).Milliseconds(), now+(20*time.Minute).Milliseconds()
	card := zhipuExpiryTestCard
	for _, test := range []struct {
		name, kind, fiveHour, week string
		expected                   int64
		wantID                     int64
		invalid                    bool
	}{
		{name: "globally earliest five hour", kind: "five_hour", fiveHour: card(3, later) + `,` + card(2, near), week: card(1, later), wantID: 2},
		{name: "globally earliest week", kind: "week", fiveHour: card(3, later), week: card(2, near), wantID: 2},
		{name: "wrong kind never falls through", kind: "five_hour", fiveHour: card(3, later), week: card(2, near)},
		{name: "wrong week kind never falls through", kind: "week", fiveHour: card(2, near), week: card(3, later)},
		{name: "later same type never selected", kind: "five_hour", fiveHour: card(3, later) + `,` + card(2, near), expected: later},
		{name: "earliest disappeared", kind: "five_hour", fiveHour: card(3, later)},
		{name: "expired skipped using injected clock", kind: "week", fiveHour: card(1, now-1), week: card(2, near), wantID: 2},
		{name: "one millisecond future", kind: "five_hour", fiveHour: card(2, now+1), expected: now + 1, wantID: 2},
		{name: "thirty minute boundary", kind: "week", week: card(2, now+(30*time.Minute).Milliseconds()), expected: now + (30 * time.Minute).Milliseconds(), wantID: 2},
		{name: "beyond boundary", kind: "week", week: card(2, now+(30*time.Minute).Milliseconds()+1), expected: now + (30 * time.Minute).Milliseconds() + 1},
		{name: "expiry at now", kind: "week", week: card(2, now), expected: now},
		{name: "expired expected never falls through", kind: "week", week: card(1, now-1) + `,` + card(2, near), expected: now - 1},
		{name: "empty inventory", kind: "week"},
		{name: "stable global tie selection", kind: "week", fiveHour: card(3, near), week: card(2, near), wantID: 2},
		{name: "wrong kind at tie", kind: "five_hour", fiveHour: card(3, near), week: card(2, near)},
		{name: "unknown expiry blocks all types", kind: "week", fiveHour: `{"recordId":1,"grantType":"DIRECT","available":true}`, week: card(2, near), invalid: true},
		{name: "unknown availability", kind: "week", week: `{"recordId":1}`, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			observer := ZhipuObserver{client: func(context.Context) (*http.Client, error) {
				return &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					if req.Method != http.MethodGet || req.URL.String() != zhipuResetListURL {
						t.Fatalf("unexpected preparation request: %s %s", req.Method, req.URL)
					}
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(zhipuExpiryTestList(test.fiveHour, test.week))), Request: req}, nil
				})}, nil
			}}
			expected := test.expected
			if expected == 0 {
				expected = near
			}
			ctx := subscriptionruntime.WithResetCreditExpiryCondition(t.Context(), subscriptionruntime.ResetCreditExpiryCondition{ExpiresAtMS: expected, NowMS: now})
			data, err := observer.PrepareResetCredit(ctx, zhipuRequest(t), test.kind)
			if calls != 1 {
				t.Fatalf("preparation made %d requests", calls)
			}
			if test.wantID > 0 {
				var selected zhipuResetSelection
				if err != nil || json.Unmarshal(data, &selected) != nil || selected.RecordID != test.wantID || selected.GrantType != "ORIGINAL_GRANT" || selected.TargetType != "PERSONAL" {
					t.Fatalf("selection/error = %s / %v", data, err)
				}
				wantType := "FIVE_HOUR"
				if test.kind == "week" {
					wantType = "WEEK"
				}
				if selected.ResetType != wantType {
					t.Fatalf("reset kind = %s, want %s", selected.ResetType, wantType)
				}
				return
			}
			if err == nil || data != nil {
				t.Fatalf("unsafe preparation = %s / %v", data, err)
			}
			var upstream *subscriptionruntime.UpstreamHTTPError
			if test.invalid {
				if !errors.Is(err, subscriptionruntime.ErrObservationPayloadInvalid) {
					t.Fatalf("invalid inventory classification = %v", err)
				}
			} else if !errors.As(err, &upstream) || upstream.StatusCode != http.StatusConflict {
				t.Fatalf("stale condition classification = %v", err)
			}
		})
	}
}

func TestZhipuResetCreditExpiryGuardManualKindSelectionUnchanged(t *testing.T) {
	t.Parallel()
	now := time.Now().UnixMilli()
	list := zhipuExpiryTestList(zhipuExpiryTestCard(2, now+(2*time.Hour).Milliseconds()), zhipuExpiryTestCard(1, now+(time.Hour).Milliseconds()))
	observer := ZhipuObserver{client: func(context.Context) (*http.Client, error) {
		return &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(list)), Request: req}, nil
		})}, nil
	}}
	data, err := observer.PrepareResetCredit(t.Context(), zhipuRequest(t), "five_hour")
	var selected zhipuResetSelection
	if err != nil || json.Unmarshal(data, &selected) != nil || selected.RecordID != 2 {
		t.Fatalf("manual kind selection changed: %s / %v", data, err)
	}
}

func TestZhipuResetCreditExpiryGuardStoredConsumeRetriesRetainSelection(t *testing.T) {
	t.Parallel()
	now := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	expiry := now + (10 * time.Minute).Milliseconds()
	calls := 0
	observer := ZhipuObserver{client: func(context.Context) (*http.Client, error) {
		return &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			body := zhipuExpiryTestList(zhipuExpiryTestCard(2, expiry), zhipuExpiryTestCard(3, expiry+1000))
			if calls == 1 {
				if req.Method != http.MethodGet || req.URL.String() != zhipuResetListURL {
					t.Fatal("prepare did not list")
				}
			} else {
				if req.Method != http.MethodPost || req.URL.String() != zhipuResetUseURL {
					t.Fatal("stored consume retry re-listed")
				}
				var payload map[string]any
				if json.NewDecoder(req.Body).Decode(&payload) != nil || len(payload) != 5 || payload["recordId"] != float64(2) || payload["resetType"] != "FIVE_HOUR" || payload["grantType"] != "ORIGINAL_GRANT" || payload["targetType"] != "PERSONAL" || payload["requestId"] != "9f0f4c32-89d2-4bcb-9e19-052940dc2f16" {
					t.Fatalf("stored selection changed: %#v", payload)
				}
				body = `{"code":200,"success":true,"data":true}`
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
		})}, nil
	}}
	request := zhipuRequest(t)
	ctx := subscriptionruntime.WithResetCreditExpiryCondition(t.Context(), subscriptionruntime.ResetCreditExpiryCondition{ExpiresAtMS: expiry, NowMS: now})
	data, err := observer.PrepareResetCredit(ctx, request, "five_hour")
	if err != nil {
		t.Fatal(err)
	}
	// Conditions can no longer match on a retry; the persisted card remains fixed.
	ctx = subscriptionruntime.WithResetCreditExpiryCondition(ctx, subscriptionruntime.ResetCreditExpiryCondition{ExpiresAtMS: 1, NowMS: expiry + 1})
	stored := subscriptionruntime.ResetCreditRequest{ID: "9f0f4c32-89d2-4bcb-9e19-052940dc2f16", Data: data}
	for range 2 {
		result, err := observer.ConsumeResetCredit(ctx, request, stored)
		if err != nil || result.Status != "succeeded" || result.WindowsReset != 1 {
			t.Fatalf("stored consume = %#v / %v", result, err)
		}
	}
	if calls != 3 {
		t.Fatalf("prepare and retries made %d requests, want 3", calls)
	}
}

func TestZhipuResetCreditExpiryGuardDefaultsToWallClock(t *testing.T) {
	t.Parallel()
	expiry := time.Now().Add(5 * time.Minute).UnixMilli()
	observer := ZhipuObserver{client: func(context.Context) (*http.Client, error) {
		return &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(zhipuExpiryTestList("", zhipuExpiryTestCard(1, expiry)))), Request: req}, nil
		})}, nil
	}}
	ctx := subscriptionruntime.WithResetCreditExpiryCondition(t.Context(), subscriptionruntime.ResetCreditExpiryCondition{ExpiresAtMS: expiry})
	if _, err := observer.PrepareResetCredit(ctx, zhipuRequest(t), "week"); err != nil {
		t.Fatal(err)
	}
}
