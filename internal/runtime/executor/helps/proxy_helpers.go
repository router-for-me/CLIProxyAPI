package helps

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

// NewProxyAwareHTTPClient creates an HTTP client with proper proxy configuration priority:
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
func NewProxyAwareHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	httpClient := &http.Client{}
	// Resolve a request timeout. We intentionally do NOT set http.Client.Timeout:
	// that covers the entire request-response lifecycle including reading the
	// response body, which would abort healthy streaming (SSE) responses mid-stream.
	// Instead we apply it as the transport ResponseHeaderTimeout, which bounds the
	// connect + TLS handshake + first-response-byte phase without cutting off an
	// active stream body.
	var headerTimeout time.Duration
	if timeout > 0 {
		headerTimeout = timeout
	} else if cfg != nil && cfg.RequestTimeoutSeconds > 0 {
		headerTimeout = time.Duration(cfg.RequestTimeoutSeconds) * time.Second
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
			applyHeaderTimeout(transport, headerTimeout)
			httpClient.Transport = transport
			return httpClient
		}
		// If proxy setup failed, log and fall through to context RoundTripper
		log.Debugf("failed to setup proxy from URL: %s, falling back to context transport", proxyutil.Redact(proxyURL))
	}

	// Priority 3: Use RoundTripper from context (typically from RoundTripperFor).
	// Context round trippers are custom (e.g. utls) and may be shared, so we do
	// not attempt to mutate them; the header timeout only applies to transports
	// we construct ourselves below.
	if rt, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && rt != nil {
		httpClient.Transport = rt
		return httpClient
	}

	// No proxy and no context round tripper: use a default transport clone so we
	// can apply the header timeout without mutating the shared default.
	if headerTimeout > 0 {
		httpClient.Transport = cloneDefaultTransportWithHeaderTimeout(headerTimeout)
	}
	return httpClient
}

// buildProxyTransport creates an HTTP transport configured for the given proxy URL.
// It supports SOCKS5, HTTP, and HTTPS proxy protocols.
//
// Parameters:
//   - proxyURL: The proxy URL string (e.g., "socks5://user:pass@host:port", "http://host:port")
//
// Returns:
//   - *http.Transport: A configured transport, or nil if the proxy URL is invalid
func buildProxyTransport(proxyURL string) *http.Transport {
	transport, _, errBuild := proxyutil.BuildHTTPTransport(proxyURL)
	if errBuild != nil {
		log.Errorf("%v", errBuild)
		return nil
	}
	return transport
}

// applyHeaderTimeout sets ResponseHeaderTimeout on a transport we own. It is a
// no-op when the timeout is zero (legacy behavior).
func applyHeaderTimeout(transport *http.Transport, headerTimeout time.Duration) {
	if headerTimeout > 0 {
		transport.ResponseHeaderTimeout = headerTimeout
	}
}

// cloneDefaultTransportWithHeaderTimeout returns a clone of http.DefaultTransport
// configured with the given ResponseHeaderTimeout, bounding the connect + TLS +
// first-response-byte phase without limiting how long a streaming body can run.
func cloneDefaultTransportWithHeaderTimeout(headerTimeout time.Duration) *http.Transport {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok || base == nil {
		return &http.Transport{ResponseHeaderTimeout: headerTimeout}
	}
	clone := base.Clone()
	clone.ResponseHeaderTimeout = headerTimeout
	return clone
}
