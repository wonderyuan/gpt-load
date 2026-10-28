package modelinfo

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"gpt-load/internal/outboundproxy"
	"gpt-load/internal/platform/httpclient"
)

type doerFunc func(*http.Request) (*http.Response, error)

func (call doerFunc) Do(request *http.Request) (*http.Response, error) { return call(request) }

func TestModelInfoProxyOverrideIsScopedAndFailClosed(t *testing.T) {
	fallbackCalls := 0
	fallback := func() outboundproxy.Effective {
		fallbackCalls++
		return outboundproxy.Effective{Config: outboundproxy.Config{Mode: outboundproxy.ModeDirect}, Source: outboundproxy.SourceGlobal}
	}
	for _, override := range []string{"", "   "} {
		provider := modelInfoProxyProvider(fallback, override)
		if provider().Config.Mode != outboundproxy.ModeDirect {
			t.Fatal("empty override must retain system policy")
		}
	}
	if fallbackCalls != 2 {
		t.Fatal("system proxy provider not reused")
	}
	valid := modelInfoProxyProvider(fallback, "http://preview-user:preview-password@127.0.0.1:7892")()
	if valid.Config.Mode != outboundproxy.ModeCustom || valid.Source != outboundproxy.SourceEnvironment || !strings.Contains(valid.Config.URL, "127.0.0.1:7892") {
		t.Fatal("dedicated public-data proxy not used")
	}
	invalid := modelInfoProxyProvider(fallback, "https://invalid-proxy.example/private")()
	if invalid.Config.Mode != outboundproxy.ModeCustom || invalid.Config.URL != "" || fallbackCalls != 2 {
		t.Fatal("invalid override fell back to direct")
	}
	manager := httpclient.NewHTTPClientManager()
	if _, err := manager.NewClientForOutboundProxy(&httpclient.Config{}, invalid); err == nil {
		t.Fatal("invalid proxy unexpectedly opened a transport")
	}
}

func TestClientFixedPublicRequests(t *testing.T) {
	for _, test := range []struct {
		name, endpoint, contentType string
		fetch                       func(*Client, context.Context) ([]byte, error)
	}{
		{"home", homeEndpoint, "text/html", (*Client).fetchHome},
		{"speed", speedEndpoint, "application/json", (*Client).fetchSpeed},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &Client{httpClient: doerFunc(func(request *http.Request) (*http.Response, error) {
				if request.URL.String() != test.endpoint || request.Method != http.MethodGet {
					t.Fatalf("unexpected request %s %s", request.Method, request.URL)
				}
				if request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" || request.Header.Get("Accept") != test.contentType {
					t.Fatalf("unexpected public request headers: %v", request.Header)
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {test.contentType + "; charset=utf-8"}}, Body: io.NopCloser(strings.NewReader("public"))}, nil
			})}
			payload, err := test.fetch(client, t.Context())
			if err != nil || string(payload) != "public" {
				t.Fatalf("fetch = %q, %v", payload, err)
			}
		})
	}
}

func TestClientRejectsUnsafeResponses(t *testing.T) {
	for _, test := range []struct {
		name, body, contentType string
		status                  int
		err                     error
	}{
		{name: "redirect", status: 302, contentType: "application/json"},
		{name: "failure", status: 502, contentType: "application/json"},
		{name: "html", status: 200, contentType: "text/html"},
		{name: "missing content type", status: 200},
		{name: "oversized", status: 200, contentType: "application/json", body: "123456789"},
		{name: "transport", err: errors.New("secret proxy credentials must not leak")},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &Client{httpClient: doerFunc(func(*http.Request) (*http.Response, error) {
				if test.err != nil {
					return nil, test.err
				}
				return &http.Response{StatusCode: test.status, Header: http.Header{"Content-Type": {test.contentType}}, Body: io.NopCloser(strings.NewReader(test.body))}, nil
			})}
			_, err := client.fetch(t.Context(), speedEndpoint, "application/json", 8)
			if err == nil || strings.Contains(err.Error(), "credentials") {
				t.Fatalf("fetch error = %v", err)
			}
		})
	}
}
