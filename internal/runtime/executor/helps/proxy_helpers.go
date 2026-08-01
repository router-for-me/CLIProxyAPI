package helps

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

// ResolveEffectiveProxy returns the proxy URL that should be used for the given auth,
// following the resolution priority:
//  1. per-credential proxy-url (auth.ProxyURL)
//  2. per-provider proxy (cfg.ProxyByProvider[auth.Provider])
//  3. global proxy-url (cfg.ProxyURL)
//  4. empty string (let the caller fall back to context transport / direct)
//
// Values may be a proxy URL or the literals "direct"/"none"; they are returned verbatim
// so downstream transport construction can honor them.
func ResolveEffectiveProxy(cfg *config.Config, auth *cliproxyauth.Auth) string {
	// Priority 1: per-credential proxy override.
	if auth != nil {
		if proxyURL := strings.TrimSpace(auth.ProxyURL); proxyURL != "" {
			return proxyURL
		}
	}

	provider := ""
	if auth != nil {
		provider = auth.Provider
	}
	if cfg == nil {
		return ""
	}
	// Priority 2-3: proxy-by-provider then global proxy-url.
	return util.ResolveProxyURL(&cfg.SDKConfig, provider)
}

// NewProxyAwareHTTPClient creates an HTTP client with proper proxy configuration priority:
// 1. Use auth.ProxyURL if configured (highest priority)
// 2. Use cfg.ProxyByProvider[auth.Provider] if the credential has no own proxy
// 3. Use cfg.ProxyURL if neither above is configured
// 4. Use RoundTripper from context if none are configured
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

	// Resolve the effective proxy following credential > provider > global priority.
	proxyURL := ResolveEffectiveProxy(cfg, auth)

	// If we have a proxy URL configured, set up the transport
	if proxyURL != "" {
		transport := buildProxyTransport(proxyURL)
		if transport != nil {
			httpClient.Transport = transport
			return httpClient
		}
		// If proxy setup failed, log and fall through to context RoundTripper
		log.Debugf("failed to setup proxy from URL: %s, falling back to context transport", proxyutil.Redact(proxyURL))
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
