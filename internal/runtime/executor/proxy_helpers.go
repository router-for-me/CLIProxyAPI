package executor

import (
	"context"
	"net/http"
	"strings"
	"time"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

// ResolveProxyURL returns the effective proxy URL following the standard priority:
//  1. auth.ProxyURL (per-account override)
//  2. cfg.ProxyURL  (global config)
//  3. "" (empty — caller decides fallback behavior)
func ResolveProxyURL(cfg *config.Config, auth *cliproxyauth.Auth) string {
	if auth != nil {
		if u := strings.TrimSpace(auth.ProxyURL); u != "" {
			return u
		}
	}
	if cfg != nil {
		if u := strings.TrimSpace(cfg.ProxyURL); u != "" {
			return u
		}
	}
	return ""
}

// newClaudeHTTPClient creates an HTTP client for Anthropic API requests using
// utls with Bun BoringSSL TLS fingerprint. This ensures API requests and OAuth
// token refresh share the same TLS characteristics, matching real Claude Code CLI.
//
// Proxy priority: auth.ProxyURL > cfg.ProxyURL > env (HTTPS_PROXY etc.) > direct.
func newClaudeHTTPClient(cfg *config.Config, auth *cliproxyauth.Auth) *http.Client {
	proxyURL := ResolveProxyURL(cfg, auth)
	return claudeauth.NewAnthropicHttpClient(proxyURL)
}

// newProxyAwareHTTPClient creates an HTTP client with proper proxy configuration priority:
// 1. Use auth.ProxyURL if configured (highest priority)
// 2. Use cfg.ProxyURL if auth proxy is not configured
// 3. Use RoundTripper from context if neither are configured
//
// NOTE: This function uses standard Go TLS. For Anthropic/Claude API requests,
// use newClaudeHTTPClient() instead, which uses Bun BoringSSL TLS fingerprint.
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

	proxyURL := ResolveProxyURL(cfg, auth)

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
