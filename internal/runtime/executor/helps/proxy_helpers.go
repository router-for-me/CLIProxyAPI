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

// relayTransportFor returns a relay RoundTripper when the auth is bound to a
// relay pool. Relay semantics replace proxy semantics entirely (the renderer
// guarantees mutual exclusion; this is the enforcement point).
func relayTransportFor(auth *cliproxyauth.Auth) http.RoundTripper {
	return relayTransportForWithInner(auth, nil)
}

// relayTransportForWithInner allows tests to run against local http relay
// stubs; production callers pass nil (strict https base + default transport).
func relayTransportForWithInner(auth *cliproxyauth.Auth, inner http.RoundTripper) http.RoundTripper {
	if auth == nil {
		return nil
	}
	base := strings.TrimSpace(auth.RelayBaseURL)
	if base == "" {
		return nil
	}
	rt, errRelay := proxyutil.NewRelayTransportRelaxed(base, inner)
	if errRelay != nil {
		log.Errorf("invalid relay base URL for auth %s: %v", auth.ID, errRelay)
		return nil
	}
	return rt
}

// NewProxyAwareHTTPClient creates an HTTP client with proper proxy configuration priority:
// 0. Use a relay transport when auth.RelayBaseURL is set (replaces proxy semantics)
// 1. Use auth.ProxyURL if configured (highest proxy priority)
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
	if timeout > 0 {
		httpClient.Timeout = timeout
	}

	// Priority 0: relay-bound auth (replaces proxy semantics entirely).
	if rt := relayTransportFor(auth); rt != nil {
		httpClient.Transport = rt
		return httpClient
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
		rt, _, errBuild := proxyutil.BuildProxiedTransport(proxyURL)
		if errBuild == nil && rt != nil {
			httpClient.Transport = rt
			return httpClient
		}
		// If proxy setup failed, log and fall through to context RoundTripper
		if errBuild != nil {
			log.Debugf("failed to setup proxy from URL: %s, falling back to context transport", proxyutil.Redact(proxyURL))
		}
	}

	// Priority 3: Use RoundTripper from context (typically from RoundTripperFor)
	if rt, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && rt != nil {
		httpClient.Transport = rt
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
