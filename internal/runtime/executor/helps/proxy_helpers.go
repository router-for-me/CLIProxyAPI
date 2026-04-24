package helps

import (
	"context"
	"crypto/x509"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

const (
	pooledTransportMaxIdleConns        = 512
	pooledTransportMaxIdleConnsPerHost = 64
	pooledTransportIdleConnTimeout     = 90 * time.Second
	defaultClientCacheKey              = "default"
)

type clientCacheKey struct {
	timeout      time.Duration
	transportKey string
}

type customCATransportCacheKey struct {
	transportKey string
	pool         string
}

var (
	defaultTransport       http.RoundTripper
	defaultTransportOnce   sync.Once
	httpClientCache        sync.Map
	proxyTransportCache    sync.Map
	customCATransportCache sync.Map
)

// NewProxyAwareHTTPClient creates an HTTP client with pooled transport reuse.
// Priority:
// 1. Use the context RoundTripper when auth.ProxyURL is managed upstream.
// 2. Use auth.ProxyURL if configured.
// 3. Use cfg.ProxyURL if auth proxy is not configured.
// 4. Use the context RoundTripper when no explicit proxy is configured.
// 5. Fall back to the shared default transport.
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
	pool, errCA := misc.CustomRootCAsFromEnv()
	if errCA != nil {
		log.Warnf("custom CA disabled: %v", errCA)
		pool = nil
	}

	if rt := contextRoundTripper(ctx); rt != nil && authProxyURL(auth) != "" {
		if pool != nil {
			rt = misc.RoundTripperWithCustomRootCAs(rt, pool)
		}
		return newHTTPClient(rt, timeout)
	}

	proxyURL := resolvedProxyURL(cfg, auth)
	if proxyURL != "" {
		transport := cachedProxyTransport(proxyURL)
		if transport != nil {
			if pool != nil {
				transport = cachedCustomCATransport("proxy:"+proxyURL, transport, pool)
				return cachedHTTPClient("proxy-ca:"+proxyURL+":"+customRootCAPoolKey(pool), transport, timeout)
			}
			return cachedHTTPClient("proxy:"+proxyURL, transport, timeout)
		}
		log.Debugf("failed to setup proxy from URL: %s, falling back to context transport", proxyURL)
	}

	if rt := contextRoundTripper(ctx); rt != nil {
		if pool != nil {
			rt = misc.RoundTripperWithCustomRootCAs(rt, pool)
		}
		return newHTTPClient(rt, timeout)
	}

	if pool != nil {
		transport := cachedCustomCATransport(defaultClientCacheKey, cachedDefaultTransport(), pool)
		return cachedHTTPClient("default-ca:"+customRootCAPoolKey(pool), transport, timeout)
	}
	return cachedHTTPClient(defaultClientCacheKey, cachedDefaultTransport(), timeout)
}

func cachedCustomCATransport(transportKey string, transport http.RoundTripper, pool *x509.CertPool) http.RoundTripper {
	if transport == nil || pool == nil {
		return transport
	}
	key := customCATransportCacheKey{transportKey: transportKey, pool: customRootCAPoolKey(pool)}
	if cached, ok := customCATransportCache.Load(key); ok {
		if cachedTransport, okTransport := cached.(http.RoundTripper); okTransport {
			return cachedTransport
		}
	}

	wrapped := misc.RoundTripperWithCustomRootCAs(transport, pool)
	actual, _ := customCATransportCache.LoadOrStore(key, wrapped)
	if cached, ok := actual.(http.RoundTripper); ok {
		return cached
	}
	return wrapped
}

func customRootCAPoolKey(pool *x509.CertPool) string {
	return strings.TrimPrefix(strings.TrimSuffix(strings.TrimSpace(fmt.Sprintf("%p", pool)), ">"), "&")
}

func cachedProxyTransport(proxyURL string) http.RoundTripper {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return nil
	}
	if cached, ok := proxyTransportCache.Load(proxyURL); ok {
		if transport, okTransport := cached.(http.RoundTripper); okTransport {
			return transport
		}
	}

	transport := buildProxyTransport(proxyURL)
	if transport == nil {
		return nil
	}

	actual, _ := proxyTransportCache.LoadOrStore(proxyURL, transport)
	if cached, ok := actual.(http.RoundTripper); ok {
		return cached
	}
	return transport
}

func cachedDefaultTransport() http.RoundTripper {
	defaultTransportOnce.Do(func() {
		defaultTransport = buildPooledTransport(cloneDefaultTransport())
	})
	return defaultTransport
}

func cachedHTTPClient(transportKey string, transport http.RoundTripper, timeout time.Duration) *http.Client {
	key := clientCacheKey{timeout: timeout, transportKey: transportKey}
	if cached, ok := httpClientCache.Load(key); ok {
		if client, okClient := cached.(*http.Client); okClient {
			return client
		}
	}

	client := newHTTPClient(transport, timeout)
	actual, _ := httpClientCache.LoadOrStore(key, client)
	if cached, ok := actual.(*http.Client); ok {
		return cached
	}
	return client
}

func newHTTPClient(transport http.RoundTripper, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}
}

func contextRoundTripper(ctx context.Context) http.RoundTripper {
	if ctx == nil {
		return nil
	}
	rt, _ := ctx.Value("cliproxy.roundtripper").(http.RoundTripper)
	return rt
}

func resolvedProxyURL(cfg *config.Config, auth *cliproxyauth.Auth) string {
	if proxyURL := authProxyURL(auth); proxyURL != "" {
		return proxyURL
	}
	if cfg == nil {
		return ""
	}
	return strings.TrimSpace(cfg.ProxyURL)
}

func authProxyURL(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	return strings.TrimSpace(auth.ProxyURL)
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
	return buildPooledTransport(transport)
}

func cloneDefaultTransport() *http.Transport {
	if transport, ok := http.DefaultTransport.(*http.Transport); ok && transport != nil {
		return transport.Clone()
	}
	return &http.Transport{}
}

func buildPooledTransport(base *http.Transport) *http.Transport {
	if base == nil {
		base = &http.Transport{}
	}

	transport := base.Clone()
	transport.MaxIdleConns = pooledTransportMaxIdleConns
	transport.MaxIdleConnsPerHost = pooledTransportMaxIdleConnsPerHost
	transport.MaxConnsPerHost = 0
	transport.IdleConnTimeout = pooledTransportIdleConnTimeout
	transport.ForceAttemptHTTP2 = true
	return transport
}
