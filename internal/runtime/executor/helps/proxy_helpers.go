package helps

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

// errTorTransportUnavailable is the error returned when Tor mode is enabled
// but the Tor SOCKS5 transport cannot be created.
var errTorTransportUnavailable = errors.New("Tor proxy configured but unavailable")

// torFailTransport is an http.RoundTripper that always returns errTorTransportUnavailable.
// Used when Tor mode is enabled but the Tor SOCKS5 transport cannot be created,
// guaranteeing that no traffic leaks outside Tor.
type torFailTransport struct{}

func (t *torFailTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("%w: check Tor daemon is running at %s", errTorTransportUnavailable, torAddrFromContext(req.Context()))
}

// torAddrFromContext extracts the Tor proxy address from the request context
// if it was set by NewProxyAwareHTTPClient.
type torAddrKey struct{}

func torAddrFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(torAddrKey{}).(string); ok {
		return v
	}
	return DefaultTorProxyAddr
}

// NewProxyAwareHTTPClient creates an HTTP client with proper proxy configuration priority:
// 1. Use auth.ProxyURL if configured (highest priority)
// 2. Use cfg.ProxyURL if auth proxy is not configured (unless cfg.ProxyMode is "tor")
// 3. If cfg.ProxyMode is "tor", use the Tor SOCKS5 proxy via cfg.TorProxyAddr
// 4. Otherwise, use RoundTripper from context if neither are configured
//
// CRITICAL: When Tor mode is enabled, this function guarantees no traffic escapes
// outside Tor. If the Tor transport cannot be created, the returned client's
// RoundTrip will always fail with errTorTransportUnavailable.
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
		log.Debugf("failed to setup proxy from URL: %s, falling back to context transport", proxyutil.Redact(proxyURL))
	}

	// Priority 3: If Tor mode is enabled, use Tor SOCKS5 proxy.
	// CRITICAL: When Tor is enabled, we MUST NOT fall through to a non-Tor transport.
	// If the Tor transport cannot be created, we use torFailTransport which will
	// make every request fail with a clear error, guaranteeing no traffic leak.
	if cfg != nil && IsTorMode(cfg) {
		torProxyAddr := strings.TrimSpace(cfg.TorProxyAddr)
		if torProxyAddr == "" {
			torProxyAddr = DefaultTorProxyAddr
		}
		transport := NewTorHTTPTransport(torProxyAddr)
		if transport != nil {
			httpClient.Transport = transport
			// Tag the context with the proxy address for error messages.
			ctx = context.WithValue(ctx, torAddrKey{}, torProxyAddr)
			return httpClient
		}
		// Tor transport creation failed — do NOT fall through to non-Tor transport.
		log.Error("Tor mode enabled but Tor transport creation failed — all requests will fail")
		httpClient.Transport = &torFailTransport{}
		return httpClient
	}

	// Priority 4: Use RoundTripper from context (typically from RoundTripperFor)
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
