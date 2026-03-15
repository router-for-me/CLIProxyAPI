package executor

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

// Connection-setup timeouts. Safe for streaming — only affect dial/TLS/headers, not body reads.
const (
	defaultDialTimeout           = 10 * time.Second
	defaultDialKeepAlive         = 30 * time.Second
	defaultTLSHandshakeTimeout   = 10 * time.Second
	defaultResponseHeaderTimeout = 30 * time.Second
	defaultIdleConnTimeout       = 90 * time.Second
)

// newProxyAwareHTTPClient creates an HTTP client with proper proxy configuration priority:
// 1. Use auth.ProxyURL if configured (highest priority)
// 2. Use cfg.ProxyURL if auth proxy is not configured
// 3. Use RoundTripper from context if neither are configured
//
// Parameters:
//   - ctx: The context containing optional RoundTripper
//   - cfg: The application configuration
//   - auth: The authentication information
//   - timeout: The client timeout (0 means no timeout)
//
// Returns:
//   - *http.Client: An HTTP client with configured proxy or transport
func newProxyAwareHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	httpClient := &http.Client{}
	if timeout > 0 {
		httpClient.Timeout = timeout
	}

	// Priority 1: Use auth.ProxyURL if configured
	var proxyURL string
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}

	// Priority 2: Use cfg.ProxyURL if auth proxy is not configured
	if proxyURL == "" && cfg != nil {
		proxyURL = strings.TrimSpace(cfg.ProxyURL)
	}

	// If we have a proxy URL configured, set up the transport
	if proxyURL != "" {
		transport := buildProxyTransport(proxyURL)
		if transport != nil {
			httpClient.Transport = transport
			return httpClient
		}
		// If proxy setup failed, log and fall through to context RoundTripper
		log.Debugf("failed to setup proxy from URL: %s, falling back to context transport", proxyURL)
	}

	// Priority 3: Use RoundTripper from context (typically from RoundTripperFor)
	if rt, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && rt != nil {
		httpClient.Transport = rt
		return httpClient
	}

	// No proxy, no context RT — use default transport with timeouts.
	httpClient.Transport = newDefaultTransport()
	return httpClient
}

// buildProxyTransport creates a proxy-configured transport with timeouts.
// Supports SOCKS5, HTTP, HTTPS proxies. Returns nil on invalid URL.
func buildProxyTransport(proxyURL string) *http.Transport {
	transport, _, errBuild := proxyutil.BuildHTTPTransport(proxyURL)
	if errBuild != nil {
		log.Errorf("%v", errBuild)
		return nil
	}
	if transport != nil {
		applyTransportTimeouts(transport)
	}
	return transport
}

// newDefaultTransport returns a transport with default timeouts.
func newDefaultTransport() *http.Transport {
	t := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		ForceAttemptHTTP2:   true,
	}
	applyTransportTimeouts(t)
	return t
}

// applyTransportTimeouts sets dial/TLS/header timeouts. Skips already-set fields.
func applyTransportTimeouts(t *http.Transport) {
	if t.DialContext == nil {
		t.DialContext = (&net.Dialer{
			Timeout:   defaultDialTimeout,
			KeepAlive: defaultDialKeepAlive,
		}).DialContext
	}
	if t.TLSHandshakeTimeout == 0 {
		t.TLSHandshakeTimeout = defaultTLSHandshakeTimeout
	}
	if t.ResponseHeaderTimeout == 0 {
		t.ResponseHeaderTimeout = defaultResponseHeaderTimeout
	}
	if t.IdleConnTimeout == 0 {
		t.IdleConnTimeout = defaultIdleConnTimeout
	}
}
