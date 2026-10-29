package modelnotify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const modelNotifyEventUUID = "6b1c0d3e-9f21-4c67-a610-58cc3a1284da"

func modelNotifyMessage() Message {
	return Message{
		Topic:     "model.subscription.reset",
		Title:     "Subscription quota reset",
		Level:     "info",
		Body:      "The model subscription quota has reset.",
		ActionURL: "https://gpt-load.example/models",
		DedupeKey: "model.subscription.reset:account-42:window-7",
		DedupeTTL: 86400,
		Metadata:  map[string]any{"event_uuid": modelNotifyEventUUID, "account_id": "42"},
	}
}

func modelNotifyTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := newClient(server.URL, "test-api-key")
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	t.Cleanup(client.httpClient.CloseIdleConnections)
	return client
}

func modelNotifyAssertError(t *testing.T, err error, status int, retryable bool, code string) {
	t.Helper()
	var gatewayError *Error
	if !errors.As(err, &gatewayError) {
		t.Fatalf("error = %v, want *Error", err)
	}
	if gatewayError.Status != status || gatewayError.Retryable != retryable || gatewayError.Code != code {
		t.Fatalf("error = %#v, want status=%d retryable=%v code=%q", gatewayError, status, retryable, code)
	}
}

func modelNotifyWriteResult(writer http.ResponseWriter, status int, body string) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_, _ = io.WriteString(writer, body)
}

func TestModelNotifyRequestAuthDedupeAndStableRetry(t *testing.T) {
	var calls atomic.Int32
	var bodies []string
	client := modelNotifyTestClient(t, func(writer http.ResponseWriter, request *http.Request) {
		attempt := calls.Add(1)
		if request.Method != http.MethodPost || request.URL.RequestURI() != "/api/v1/notifications" {
			t.Errorf("unexpected request method or path: %s %s", request.Method, request.URL.RequestURI())
		}
		if request.Header.Get("X-API-Key") != "test-api-key" || request.Header.Get("Idempotency-Key") != modelNotifyEventUUID {
			t.Error("request authentication or event UUID header mismatch")
		}
		if request.Header.Get("Content-Type") != "application/json" || request.Header.Get("Accept") != "application/json" {
			t.Error("request must use JSON")
		}
		if request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" {
			t.Error("unrelated credentials must not be sent")
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		bodies = append(bodies, string(body))
		var got map[string]any
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		want := map[string]any{
			"topic": "model.subscription.reset", "title": "Subscription quota reset", "level": "info",
			"body": "The model subscription quota has reset.", "action_url": "https://gpt-load.example/models",
			"dedupe_key": "model.subscription.reset:account-42:window-7", "dedupe_ttl": float64(86400),
			"metadata": map[string]any{"event_uuid": modelNotifyEventUUID, "account_id": "42"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("payload = %#v, want %#v", got, want)
		}
		if attempt == 1 {
			modelNotifyWriteResult(writer, http.StatusServiceUnavailable, `{"detail":"temporary outage"}`)
			return
		}
		modelNotifyWriteResult(writer, http.StatusAccepted, `{"id":"ntf_accepted42","status":"queued"}`)
	})
	message := modelNotifyMessage()
	_, err := client.Send(t.Context(), modelNotifyEventUUID, message)
	modelNotifyAssertError(t, err, http.StatusServiceUnavailable, true, "http_error")
	if calls.Load() != 1 {
		t.Fatal("Send retried internally")
	}
	result, err := client.Send(t.Context(), modelNotifyEventUUID, message)
	if err != nil || result != (Result{ID: "ntf_accepted42", Status: "queued"}) {
		t.Fatalf("caller retry = %#v, %v", result, err)
	}
	if calls.Load() != 2 || len(bodies) != 2 || bodies[0] != bodies[1] {
		t.Fatal("caller retry did not preserve the exact body")
	}
}

func TestModelNotifyIntegrationTopicAndOptionalJSON(t *testing.T) {
	client := modelNotifyTestClient(t, func(writer http.ResponseWriter, request *http.Request) {
		var got map[string]any
		if err := json.NewDecoder(request.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		want := map[string]any{
			"topic": "model.integration.test", "title": "Explicit integration test", "level": "info",
			"body": "", "dedupe_key": modelNotifyEventUUID, "dedupe_ttl": float64(60),
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("payload = %#v, want %#v", got, want)
		}
		modelNotifyWriteResult(writer, http.StatusAccepted, `{"id":"ntf_test42","status":"queued"}`)
	})
	_, err := client.Send(t.Context(), modelNotifyEventUUID, Message{
		Topic: "model.integration.test", Title: "Explicit integration test", Level: "info",
		DedupeKey: modelNotifyEventUUID, DedupeTTL: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestModelNotifyDisabledWithoutCredentials(t *testing.T) {
	for _, key := range []string{"", " \t\n "} {
		t.Run(fmt.Sprintf("key-length-%d", len(key)), func(t *testing.T) {
			t.Setenv("NOTIFICATION_GATEWAY_API_KEY", key)
			t.Setenv("NOTIFICATION_GATEWAY_URL", "http://user:password@unsafe.example/invalid")
			client, err := NewFromEnvironment()
			if err != nil || client == nil || client.Enabled() {
				t.Fatalf("missing key should disable: enabled=%v err=%v", client.Enabled(), err)
			}
			_, err = client.Send(t.Context(), modelNotifyEventUUID, modelNotifyMessage())
			if !errors.Is(err, ErrDisabled) {
				t.Fatalf("Send = %v, want ErrDisabled", err)
			}
		})
	}
	for _, client := range []*Client{nil, {}} {
		if client.Enabled() {
			t.Fatal("nil or zero client must be disabled")
		}
		_, err := client.Send(nil, "", Message{})
		if !errors.Is(err, ErrDisabled) {
			t.Fatalf("zero client Send = %v", err)
		}
	}
}

func TestModelNotifyEnvironmentAndDirectBoundedTransport(t *testing.T) {
	t.Setenv("NOTIFICATION_GATEWAY_URL", "")
	t.Setenv("NOTIFICATION_GATEWAY_API_KEY", "environment-key")
	t.Setenv("HTTP_PROXY", "http://proxy-user:proxy-password@127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://proxy-user:proxy-password@127.0.0.1:1")
	client, err := NewFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.httpClient.CloseIdleConnections)
	if !client.Enabled() || client.endpoint != "https://notice.yuanundo.top/api/v1/notifications" || client.apiKey != "environment-key" {
		t.Fatal("environment constructor did not configure the default endpoint and key")
	}
	if client.httpClient.Timeout != 8*time.Second || client.httpClient.CheckRedirect == nil {
		t.Fatal("client must have an eight-second timeout and disable redirects")
	}
	transport, ok := client.httpClient.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil || !transport.DisableCompression {
		t.Fatal("client must use its own direct transport with bounded uncompressed responses")
	}
	if transport.TLSClientConfig != nil && transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("public gateway TLS must be verified")
	}
	t.Setenv("NOTIFICATION_GATEWAY_URL", "https://gateway.example:8443/")
	custom, err := NewFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(custom.httpClient.CloseIdleConnections)
	if custom.endpoint != "https://gateway.example:8443/api/v1/notifications" {
		t.Fatalf("custom endpoint = %q", custom.endpoint)
	}
}

func TestModelNotifyURLValidationAndSanitizedConfigurationErrors(t *testing.T) {
	for _, baseURL := range []string{
		"https://gateway.example", "https://gateway.example/", "https://gateway.example:8443",
		"http://localhost:1234", "http://127.0.0.1:1234", "http://127.0.0.2:1234", "http://[::1]:1234",
	} {
		t.Run(baseURL, func(t *testing.T) {
			client, err := newClient(baseURL, "key")
			if err != nil || !client.Enabled() {
				t.Fatalf("valid URL rejected: %v", err)
			}
			t.Cleanup(client.httpClient.CloseIdleConnections)
		})
	}
	for _, baseURL := range []string{
		"http://gateway.example", "http://localhost.evil.example", "http://192.168.1.1", "http://[::2]",
		"ftp://gateway.example", "//gateway.example", "/relative", "https:///missing-host",
		"https://gateway.example/api/v1/notifications", "https://gateway.example/extra",
		"https://gateway.example/?secret=payload", "https://gateway.example/?", "https://gateway.example/#secret",
		"https://user:url-password@gateway.example", "https://gateway.example:0", "https://gateway.example:65536",
		"https://gateway.example:invalid", "https://gateway.example/%zz",
	} {
		t.Run(baseURL, func(t *testing.T) {
			client, err := newClient(baseURL, "api-key-secret")
			if client != nil {
				t.Fatal("invalid URL produced a client")
			}
			modelNotifyAssertError(t, err, 0, false, "invalid_configuration")
			for _, secret := range []string{baseURL, "url-password", "api-key-secret", "payload"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatal("configuration error leaked input")
				}
			}
		})
	}
	for _, key := range []string{"key\r\nX-Injected: yes", "key\x00suffix", "key\x7f", "key\tvalue"} {
		_, err := newClient("https://gateway.example", key)
		modelNotifyAssertError(t, err, 0, false, "invalid_configuration")
	}
}

func TestModelNotifyAcceptedAndUnroutedStatuses(t *testing.T) {
	for _, status := range []string{"queued", "sent", "partial", "deduplicated"} {
		t.Run(status, func(t *testing.T) {
			client := modelNotifyTestClient(t, func(writer http.ResponseWriter, _ *http.Request) {
				modelNotifyWriteResult(writer, http.StatusAccepted, `{"id":"ntf_status42","status":"`+status+`","deliveries":[]}`)
			})
			result, err := client.Send(t.Context(), modelNotifyEventUUID, modelNotifyMessage())
			if err != nil || result != (Result{ID: "ntf_status42", Status: status}) {
				t.Fatalf("Send = %#v, %v", result, err)
			}
		})
	}
	for _, body := range []string{
		`{"id":"ntf_unrouted42","status":"skipped"}`,
		`{"status":"skipped","reason":"No matching route"}`,
	} {
		t.Run(body, func(t *testing.T) {
			client := modelNotifyTestClient(t, func(writer http.ResponseWriter, _ *http.Request) {
				modelNotifyWriteResult(writer, http.StatusAccepted, body)
			})
			result, err := client.Send(t.Context(), modelNotifyEventUUID, modelNotifyMessage())
			modelNotifyAssertError(t, err, http.StatusAccepted, false, "unrouted")
			if result.Status != "skipped" {
				t.Fatalf("unrouted result = %#v", result)
			}
		})
	}
}

func TestModelNotifyHTTPErrorClassificationAndNoRetry(t *testing.T) {
	for _, test := range []struct {
		status    int
		retryable bool
	}{
		{400, false}, {401, false}, {403, false}, {404, false}, {409, false}, {413, false}, {422, false},
		{408, true}, {425, true}, {429, true}, {500, true}, {502, true}, {503, true}, {504, true},
	} {
		t.Run(fmt.Sprint(test.status), func(t *testing.T) {
			var calls atomic.Int32
			client := modelNotifyTestClient(t, func(writer http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				writer.WriteHeader(test.status)
				_, _ = io.WriteString(writer, `{"detail":"api-key-secret body-secret url-password"}`)
			})
			result, err := client.Send(t.Context(), modelNotifyEventUUID, modelNotifyMessage())
			modelNotifyAssertError(t, err, test.status, test.retryable, "http_error")
			if result != (Result{}) || calls.Load() != 1 || errors.Unwrap(err) != nil {
				t.Fatal("HTTP failure returned a result, leaked a cause, or retried")
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "password") {
				t.Fatal("HTTP error leaked the response body")
			}
		})
	}
}

func TestModelNotifyRedirectsDoNotForwardCredentials(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var targetCalls, originCalls atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				targetCalls.Add(1)
				modelNotifyWriteResult(writer, http.StatusAccepted, `{"id":"ntf_leaked42","status":"queued"}`)
			}))
			t.Cleanup(target.Close)
			client := modelNotifyTestClient(t, func(writer http.ResponseWriter, _ *http.Request) {
				originCalls.Add(1)
				writer.Header().Set("Location", target.URL+"/secret-target")
				writer.WriteHeader(status)
			})
			_, err := client.Send(t.Context(), modelNotifyEventUUID, modelNotifyMessage())
			modelNotifyAssertError(t, err, status, false, "redirect")
			if targetCalls.Load() != 0 || originCalls.Load() != 1 || strings.Contains(err.Error(), "secret-target") {
				t.Fatal("redirect followed, retried, or leaked its target")
			}
		})
	}
}

func TestModelNotifyRejectsMalformedAndOversizedResponses(t *testing.T) {
	for _, test := range []struct {
		name, body, contentType string
		status                  int
		code                    string
	}{
		{"empty", "", "application/json", 202, "invalid_response"},
		{"null", "null", "application/json", 202, "invalid_response"},
		{"array", "[]", "application/json", 202, "invalid_response"},
		{"malformed", `{"id":"body-secret"`, "application/json", 202, "invalid_response"},
		{"trailing JSON", `{"id":"ntf_ok42","status":"queued"}{}`, "application/json", 202, "invalid_response"},
		{"missing ID", `{"status":"queued"}`, "application/json", 202, "invalid_response"},
		{"wrong ID", `{"id":"other_42","status":"queued"}`, "application/json", 202, "invalid_response"},
		{"empty ID suffix", `{"id":"ntf_","status":"queued"}`, "application/json", 202, "invalid_response"},
		{"unsafe ID", `{"id":"ntf_42\nbody-secret","status":"queued"}`, "application/json", 202, "invalid_response"},
		{"missing status", `{"id":"ntf_ok42"}`, "application/json", 202, "invalid_response"},
		{"unknown status", `{"id":"ntf_ok42","status":"accepted"}`, "application/json", 202, "invalid_response"},
		{"failed status", `{"id":"ntf_ok42","status":"failed"}`, "application/json", 202, "invalid_response"},
		{"pending status", `{"id":"ntf_ok42","status":"pending"}`, "application/json", 202, "invalid_response"},
		{"wrong ID type", `{"id":42,"status":"queued"}`, "application/json", 202, "invalid_response"},
		{"html", `<html>body-secret</html>`, "text/html", 202, "invalid_response"},
		{"missing content type", `{"id":"ntf_ok42","status":"queued"}`, "", 202, "invalid_response"},
		{"no content", "", "application/json", 204, "invalid_response"},
		{"oversized", strings.Repeat("x", maxResponseBytes+1), "application/json", 202, "response_too_large"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := modelNotifyTestClient(t, func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header()["Content-Type"] = []string{test.contentType}
				writer.WriteHeader(test.status)
				_, _ = io.WriteString(writer, test.body)
			})
			result, err := client.Send(t.Context(), modelNotifyEventUUID, modelNotifyMessage())
			modelNotifyAssertError(t, err, test.status, true, test.code)
			if result != (Result{}) || strings.Contains(err.Error(), "body-secret") {
				t.Fatal("invalid response returned a success or leaked upstream data")
			}
		})
	}
}

func TestModelNotifyResponseAtExactLimit(t *testing.T) {
	body := `{"id":"ntf_ok42","status":"queued"}`
	body += strings.Repeat(" ", maxResponseBytes-len(body))
	client := modelNotifyTestClient(t, func(writer http.ResponseWriter, _ *http.Request) {
		modelNotifyWriteResult(writer, http.StatusAccepted, body)
	})
	if _, err := client.Send(t.Context(), modelNotifyEventUUID, modelNotifyMessage()); err != nil {
		t.Fatalf("response at limit rejected: %v", err)
	}
}

type modelNotifyRoundTripper func(*http.Request) (*http.Response, error)

func (call modelNotifyRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return call(request)
}

type modelNotifyFailingReader struct{ bytesRead int }

func (reader *modelNotifyFailingReader) Read(buffer []byte) (int, error) {
	reader.bytesRead += len(buffer)
	return 0, errors.New("response body-secret")
}

func (*modelNotifyFailingReader) Close() error { return nil }

func TestModelNotifyNetworkAndReadErrorsAreSanitized(t *testing.T) {
	client, err := newClient("https://gateway.example", "api-key-secret")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	client.httpClient.Transport = modelNotifyRoundTripper(func(request *http.Request) (*http.Response, error) {
		calls++
		if request.GetBody != nil {
			t.Error("request must not opt into transparent transport replay")
		}
		return nil, &url.Error{
			Op: "Post", URL: "https://user:url-password@gateway.example/private-body-secret",
			Err: errors.New("api-key-secret"),
		}
	})
	_, err = client.Send(t.Context(), modelNotifyEventUUID, modelNotifyMessage())
	modelNotifyAssertError(t, err, 0, true, "network_error")
	if calls != 1 || errors.Unwrap(err) != nil {
		t.Fatal("network error retried or exposed its original cause")
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "password") || strings.Contains(err.Error(), "gateway.example") {
		t.Fatal("network error leaked request details")
	}
	reader := &modelNotifyFailingReader{}
	client.httpClient.Transport = modelNotifyRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusAccepted,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       reader,
		}, nil
	})
	_, err = client.Send(t.Context(), modelNotifyEventUUID, modelNotifyMessage())
	modelNotifyAssertError(t, err, http.StatusAccepted, true, "response_read_failed")
	if strings.Contains(err.Error(), "body-secret") || errors.Unwrap(err) != nil || reader.bytesRead > maxResponseBytes+1 {
		t.Fatal("response read was unbounded or exposed its original cause")
	}
}

func TestModelNotifyContextCancellationAndDeadline(t *testing.T) {
	var calls atomic.Int32
	client := modelNotifyTestClient(t, func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		modelNotifyWriteResult(writer, http.StatusAccepted, `{"id":"ntf_ok42","status":"queued"}`)
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := client.Send(ctx, modelNotifyEventUUID, modelNotifyMessage())
	modelNotifyAssertError(t, err, 0, true, "network_error")
	if !errors.Is(err, context.Canceled) || calls.Load() != 0 {
		t.Fatal("canceled context was not preserved or still sent a request")
	}
	deadlineCtx, cancelDeadline := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancelDeadline()
	_, err = client.Send(deadlineCtx, modelNotifyEventUUID, modelNotifyMessage())
	modelNotifyAssertError(t, err, 0, true, "network_error")
	if !errors.Is(err, context.DeadlineExceeded) || calls.Load() != 0 {
		t.Fatal("expired deadline was not preserved or still sent a request")
	}
	_, err = client.Send(nil, modelNotifyEventUUID, modelNotifyMessage())
	modelNotifyAssertError(t, err, 0, false, "invalid_context")
}

func TestModelNotifyValidationBeforeNetwork(t *testing.T) {
	var calls atomic.Int32
	client := modelNotifyTestClient(t, func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		modelNotifyWriteResult(writer, http.StatusAccepted, `{"id":"ntf_ok42","status":"queued"}`)
	})
	for _, test := range []struct {
		name   string
		mutate func(*Message)
	}{
		{"empty topic", func(m *Message) { m.Topic = "" }},
		{"long topic", func(m *Message) { m.Topic = "model." + strings.Repeat("a", 195) }},
		{"single segment topic", func(m *Message) { m.Topic = "model" }},
		{"wildcard topic", func(m *Message) { m.Topic = "model.*" }},
		{"empty topic segment", func(m *Message) { m.Topic = "model..reset" }},
		{"leading dot", func(m *Message) { m.Topic = ".model.reset" }},
		{"trailing dot", func(m *Message) { m.Topic = "model.reset." }},
		{"topic path", func(m *Message) { m.Topic = "model/reset" }},
		{"topic space", func(m *Message) { m.Topic = "model reset" }},
		{"invalid level", func(m *Message) { m.Level = "debug" }},
		{"empty title", func(m *Message) { m.Title = "" }},
		{"long title", func(m *Message) { m.Title = strings.Repeat("界", 501) }},
		{"long body", func(m *Message) { m.Body = strings.Repeat("界", 10001) }},
		{"invalid UTF8", func(m *Message) { m.Body = string([]byte{0xff}) }},
		{"long action URL", func(m *Message) { m.ActionURL = strings.Repeat("a", 2049) }},
		{"long dedupe key", func(m *Message) { m.DedupeKey = strings.Repeat("k", 201) }},
		{"negative TTL", func(m *Message) { m.DedupeTTL = -1 }},
		{"excessive TTL", func(m *Message) { m.DedupeTTL = 604801 }},
		{"unserializable metadata", func(m *Message) { m.Metadata = map[string]any{"body-secret": make(chan int)} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			message := modelNotifyMessage()
			test.mutate(&message)
			_, err := client.Send(t.Context(), modelNotifyEventUUID, message)
			modelNotifyAssertError(t, err, 0, false, "invalid_message")
			if strings.Contains(err.Error(), "body-secret") {
				t.Fatal("validation error leaked metadata")
			}
		})
	}
	for _, key := range []string{"", " ", " event-uuid", "event-uuid ", "uuid\r\nX-Injected: yes", "uuid\x00", "uuid\tvalue"} {
		_, err := client.Send(t.Context(), key, modelNotifyMessage())
		modelNotifyAssertError(t, err, 0, false, "invalid_idempotency_key")
	}
	if calls.Load() != 0 {
		t.Fatal("invalid input reached the network")
	}
}

func TestModelNotifyValidationBoundaries(t *testing.T) {
	client := modelNotifyTestClient(t, func(writer http.ResponseWriter, _ *http.Request) {
		modelNotifyWriteResult(writer, http.StatusAccepted, `{"id":"ntf_ok42","status":"queued"}`)
	})
	message := Message{
		Topic: "model." + strings.Repeat("a", 194), Title: strings.Repeat("界", 500), Body: strings.Repeat("界", 10000),
		DedupeKey: strings.Repeat("k", 200), DedupeTTL: 604800, ActionURL: strings.Repeat("a", 2048),
	}
	if _, err := client.Send(t.Context(), modelNotifyEventUUID, message); err != nil {
		t.Fatalf("maximum valid message rejected: %v", err)
	}
	message = Message{Topic: "model.integration.test", Title: "Test", DedupeTTL: 1}
	if _, err := client.Send(t.Context(), modelNotifyEventUUID, message); err != nil {
		t.Fatalf("minimum valid message rejected: %v", err)
	}
	message.DedupeTTL = 0
	if _, err := client.Send(t.Context(), modelNotifyEventUUID, message); err != nil {
		t.Fatalf("optional TTL rejected: %v", err)
	}
}
