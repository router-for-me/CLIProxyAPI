package executor

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

var (
	proxyHTTPTransportCache sync.Map // map[string]*cachedProxyTransport
)

type cachedProxyTransport struct {
	once      sync.Once
	transport *http.Transport
}

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
	var contextTransport http.RoundTripper
	if ctx != nil {
		if rt, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && rt != nil {
			contextTransport = rt
		}
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

	// Priority 3: Use RoundTripper from context (typically from RoundTripperFor)
	if contextTransport != nil && proxyURL == "" {
		return newProxyHTTPClient(contextTransport, timeout)
	}

	// If we have a proxy URL configured, set up the transport
	if proxyURL != "" {
		if transport := cachedTransportForProxyURL(proxyURL); transport != nil {
			return newProxyHTTPClient(transport, timeout)
		}
		// If proxy setup failed, fall through to context RoundTripper.
		log.Debugf("failed to setup proxy from URL: %s, falling back to context transport", proxyURL)
	}

	if contextTransport != nil {
		return newProxyHTTPClient(contextTransport, timeout)
	}

	return newProxyHTTPClient(nil, timeout)
}

func cachedTransportForProxyURL(proxyURL string) *http.Transport {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return nil
	}
	entryAny, _ := proxyHTTPTransportCache.LoadOrStore(proxyURL, &cachedProxyTransport{})
	entry := entryAny.(*cachedProxyTransport)
	entry.once.Do(func() {
		entry.transport = buildProxyTransport(proxyURL)
	})
	return entry.transport
}

func newProxyHTTPClient(transport http.RoundTripper, timeout time.Duration) *http.Client {
	client := &http.Client{Transport: transport}
	if timeout > 0 {
		client.Timeout = timeout
	}
	return client
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
