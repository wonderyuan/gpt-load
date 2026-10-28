package modelinfo

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"strings"
	"time"

	"gpt-load/internal/outboundproxy"
	"gpt-load/internal/platform/httpclient"
)

const (
	homeEndpoint  = "https://codexradar.com/"
	speedEndpoint = "https://speed-api.codexradar.com/api/agent-speed/public/models?window=5m&source=all"
	maxHomeBytes  = int64(4 << 20)
	maxSpeedBytes = int64(512 << 10)
)

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type Client struct {
	manager       *httpclient.HTTPClientManager
	proxyProvider httpclient.OutboundProxyProvider
	httpClient    httpDoer
}

func NewClient(manager *httpclient.HTTPClientManager, proxyProvider httpclient.OutboundProxyProvider) *Client {
	if manager == nil {
		manager = httpclient.NewHTTPClientManager()
	}
	return &Client{manager: manager, proxyProvider: modelInfoProxyProvider(proxyProvider, os.Getenv("MODEL_INFO_PROXY_URL"))}
}

func modelInfoProxyProvider(fallback httpclient.OutboundProxyProvider, override string) httpclient.OutboundProxyProvider {
	if strings.TrimSpace(override) == "" {
		return fallback
	}
	return func() outboundproxy.Effective {
		config, err := outboundproxy.Normalize(outboundproxy.Config{Mode: outboundproxy.ModeCustom, URL: override})
		if err != nil {
			// Keep an invalid override fail-closed rather than falling back to direct traffic.
			config = outboundproxy.Config{Mode: outboundproxy.ModeCustom}
		}
		return outboundproxy.Effective{Config: config, Source: outboundproxy.SourceEnvironment}
	}
}

func (client *Client) fetch(ctx context.Context, endpoint, contentType string, limit int64) ([]byte, error) {
	cfg := &httpclient.Config{
		ConnectTimeout:        4 * time.Second,
		RequestTimeout:        10 * time.Second,
		IdleConnTimeout:       30 * time.Second,
		MaxIdleConns:          2,
		MaxIdleConnsPerHost:   2,
		ResponseHeaderTimeout: 6 * time.Second,
		DisableCompression:    true,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   4 * time.Second,
		ExpectContinueTimeout: time.Second,
		DisableRedirects:      true,
	}
	transport := client.httpClient
	if transport == nil {
		if client.proxyProvider != nil {
			var err error
			transport, err = client.manager.NewClientForOutboundProxy(cfg, client.proxyProvider())
			if err != nil {
				return nil, fmt.Errorf("model information transport unavailable")
			}
		} else {
			transport = client.manager.GetClient(cfg)
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", contentType)
	request.Header.Set("User-Agent", "GPT-Load/ModelInfo")
	result, err := transport.Do(request)
	if err != nil {
		return nil, fmt.Errorf("model information fetch failed")
	}
	defer result.Body.Close()
	if result.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("model information status %d", result.StatusCode)
	}
	mediaType, _, err := mime.ParseMediaType(result.Header.Get("Content-Type"))
	if err != nil || mediaType != contentType {
		return nil, fmt.Errorf("model information content type changed")
	}
	payload, err := io.ReadAll(io.LimitReader(result.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("model information body unavailable")
	}
	if int64(len(payload)) > limit {
		return nil, fmt.Errorf("model information response too large")
	}
	return payload, nil
}

func (client *Client) fetchHome(ctx context.Context) ([]byte, error) {
	return client.fetch(ctx, homeEndpoint, "text/html", maxHomeBytes)
}

func (client *Client) fetchSpeed(ctx context.Context) ([]byte, error) {
	return client.fetch(ctx, speedEndpoint, "application/json", maxSpeedBytes)
}
