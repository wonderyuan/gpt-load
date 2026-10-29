// Package modelnotify submits notifications without owning event persistence or retries.
package modelnotify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	defaultBaseURL   = "https://notice.yuanundo.top"
	requestTimeout   = 8 * time.Second
	maxResponseBytes = 64 << 10
)

var (
	// ErrDisabled means credentials are absent and no request was attempted.
	ErrDisabled = errors.New("notification gateway is disabled")

	topicPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+(\.[A-Za-z0-9_-]+)+$`)
	idPattern    = regexp.MustCompile(`^ntf[_-]?[A-Za-z0-9][A-Za-z0-9_-]*$`)
)

// Message is the gateway payload. Keep it unchanged across retries of an event.
// A zero DedupeTTL omits the optional TTL; otherwise it is seconds, at most 7 days.
type Message struct {
	Topic     string         `json:"topic"`
	Title     string         `json:"title"`
	Level     string         `json:"level,omitempty"`
	Body      string         `json:"body"`
	ActionURL string         `json:"action_url,omitempty"`
	DedupeKey string         `json:"dedupe_key,omitempty"`
	DedupeTTL int            `json:"dedupe_ttl,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// Result describes gateway acceptance, not confirmed delivery to a recipient.
type Result struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// Error contains only local, sanitized classifications, never upstream text.
// Status is zero when no HTTP response was received. The caller owns retry policy.
type Error struct {
	Status    int
	Retryable bool
	Code      string
	cause     error
}

func (err *Error) Error() string {
	if err.Status != 0 {
		return fmt.Sprintf("notification gateway: %s (HTTP %d)", err.Code, err.Status)
	}
	return "notification gateway: " + err.Code
}

// Unwrap exposes only context cancellation/deadlines, never transport details.
func (err *Error) Unwrap() error { return err.cause }

// Client is a direct, bounded HTTP client independent of model proxy settings.
// Its zero value (and a nil *Client) is disabled.
type Client struct {
	endpoint   string
	apiKey     string
	httpClient *http.Client
}

// NewFromEnvironment reads NOTIFICATION_GATEWAY_URL (a base URL, defaulting to
// https://notice.yuanundo.top) and NOTIFICATION_GATEWAY_API_KEY. Missing credentials
// disable notifications without an error. HTTP is allowed only on loopback hosts.
func NewFromEnvironment() (*Client, error) {
	return newClient(os.Getenv("NOTIFICATION_GATEWAY_URL"), os.Getenv("NOTIFICATION_GATEWAY_API_KEY"))
}

func newClient(baseURL, apiKey string) (*Client, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return &Client{}, nil
	}
	if !validHeaderValue(apiKey) {
		return nil, &Error{Code: "invalid_configuration"}
	}
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.Opaque != "" ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawPath != "" {
		return nil, &Error{Code: "invalid_configuration"}
	}
	loopback := strings.EqualFold(parsed.Hostname(), "localhost")
	if ip := net.ParseIP(parsed.Hostname()); ip != nil {
		loopback = ip.IsLoopback()
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && loopback) {
		return nil, &Error{Code: "invalid_configuration"}
	}
	if port := parsed.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return nil, &Error{Code: "invalid_configuration"}
		}
	}
	parsed.Path = "/api/v1/notifications"
	return &Client{
		endpoint: parsed.String(),
		apiKey:   apiKey,
		httpClient: &http.Client{
			Timeout: requestTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
			Transport: &http.Transport{
				// Do not inherit HTTP(S)_PROXY or the application's model proxy.
				Proxy:                 nil,
				DialContext:           (&net.Dialer{Timeout: 4 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
				ForceAttemptHTTP2:     true,
				MaxIdleConns:          2,
				MaxIdleConnsPerHost:   2,
				IdleConnTimeout:       30 * time.Second,
				TLSHandshakeTimeout:   4 * time.Second,
				ResponseHeaderTimeout: 6 * time.Second,
				ExpectContinueTimeout: time.Second,
				DisableCompression:    true,
			},
		},
	}, nil
}

// Enabled reports whether the client has credentials and a configured transport.
func (client *Client) Enabled() bool {
	return client != nil && client.apiKey != "" && client.endpoint != "" && client.httpClient != nil
}

// Send makes one submission attempt. The caller must reuse the persisted event
// UUID as idempotencyKey, with the same Message, for every retry. The gateway does
// not check that the body matches an earlier use of that key.
// A skipped notification returns Code "unrouted" with Retryable false.
func (client *Client) Send(ctx context.Context, idempotencyKey string, message Message) (Result, error) {
	if !client.Enabled() {
		return Result{}, ErrDisabled
	}
	if ctx == nil {
		return Result{}, &Error{Code: "invalid_context"}
	}
	if idempotencyKey == "" || strings.TrimSpace(idempotencyKey) != idempotencyKey || !validHeaderValue(idempotencyKey) {
		return Result{}, &Error{Code: "invalid_idempotency_key"}
	}
	if err := validateMessage(message); err != nil {
		return Result{}, err
	}
	payload, err := json.Marshal(message)
	if err != nil {
		return Result{}, &Error{Code: "invalid_message"}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.endpoint, bytes.NewReader(payload))
	if err != nil {
		return Result{}, &Error{Code: "invalid_configuration"}
	}
	// net/http otherwise treats Idempotency-Key + GetBody as replayable on a
	// reused connection. Retries belong to the worker, not to this client.
	request.GetBody = nil
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-API-Key", client.apiKey)
	request.Header.Set("Idempotency-Key", idempotencyKey)
	response, err := client.httpClient.Do(request)
	if err != nil {
		return Result{}, &Error{Code: "network_error", Retryable: true, cause: ctx.Err()}
	}
	defer response.Body.Close()
	status := response.StatusCode
	if status < 200 || status >= 300 {
		code := "http_error"
		if status >= 300 && status < 400 {
			code = "redirect"
		}
		return Result{}, &Error{
			Status:    status,
			Retryable: status >= 500 || status == http.StatusRequestTimeout || status == http.StatusTooEarly || status == http.StatusTooManyRequests,
			Code:      code,
		}
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return Result{}, &Error{Status: status, Retryable: true, Code: "invalid_response"}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return Result{}, &Error{Status: status, Retryable: true, Code: "response_read_failed", cause: ctx.Err()}
	}
	if len(body) > maxResponseBytes {
		return Result{}, &Error{Status: status, Retryable: true, Code: "response_too_large"}
	}
	var result Result
	if err := json.Unmarshal(body, &result); err != nil {
		return Result{}, &Error{Status: status, Retryable: true, Code: "invalid_response"}
	}
	if result.Status == "skipped" {
		// A no-route response need not have created a notification ID.
		return Result{Status: "skipped"}, &Error{Status: status, Code: "unrouted"}
	}
	if len(result.ID) > 200 || !idPattern.MatchString(result.ID) {
		return Result{}, &Error{Status: status, Retryable: true, Code: "invalid_response"}
	}
	switch result.Status {
	case "queued", "sent", "partial", "deduplicated":
		return result, nil
	default:
		return Result{}, &Error{Status: status, Retryable: true, Code: "invalid_response"}
	}
}

func validHeaderValue(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] < ' ' || value[i] > '~' {
			return false
		}
	}
	return true
}

func validateMessage(message Message) error {
	switch message.Level {
	case "", "info", "success", "warning", "error", "critical":
	default:
		return &Error{Code: "invalid_message"}
	}
	if len(message.Topic) < 1 || len(message.Topic) > 200 || !topicPattern.MatchString(message.Topic) ||
		!utf8.ValidString(message.Title) || utf8.RuneCountInString(message.Title) < 1 || utf8.RuneCountInString(message.Title) > 500 ||
		!utf8.ValidString(message.Body) || utf8.RuneCountInString(message.Body) > 10000 ||
		!utf8.ValidString(message.Level) ||
		!utf8.ValidString(message.ActionURL) || utf8.RuneCountInString(message.ActionURL) > 2048 ||
		!utf8.ValidString(message.DedupeKey) || utf8.RuneCountInString(message.DedupeKey) > 200 ||
		message.DedupeTTL < 0 || message.DedupeTTL > 604800 {
		return &Error{Code: "invalid_message"}
	}
	return nil
}
