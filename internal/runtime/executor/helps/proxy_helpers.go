package helps

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

// proxyHTTPClientPoolSize is the number of reusable *http.Client values kept per
// proxy URL + timeout pair. Keep this in the 5-10 range so concurrent upstream
// calls can reuse keep-alive connections without creating a client per request.
const proxyHTTPClientPoolSize = 8

type proxyHTTPClientPool struct {
	clients []*http.Client
	cursor  uint64
}

func (p *proxyHTTPClientPool) get() *http.Client {
	if p == nil || len(p.clients) == 0 {
		return &http.Client{}
	}
	idx := atomic.AddUint64(&p.cursor, 1) - 1
	return p.clients[int(idx%uint64(len(p.clients)))]
}

var proxyHTTPClientPools sync.Map // map[string]*proxyHTTPClientPool

func proxyHTTPClientPoolKey(proxyURL string, timeout time.Duration) string {
	return fmt.Sprintf("%s\x00%d", proxyURL, int64(timeout))
}

// getPooledProxyHTTPClient returns a reusable client for the proxy URL.
// Clients in the pool share one *http.Transport so idle connections are reused.
func getPooledProxyHTTPClient(proxyURL string, timeout time.Duration) *http.Client {
	key := proxyHTTPClientPoolKey(proxyURL, timeout)
	if existing, ok := proxyHTTPClientPools.Load(key); ok {
		return existing.(*proxyHTTPClientPool).get()
	}

	transport := buildProxyTransport(proxyURL)
	if transport == nil {
		return nil
	}

	// Tune keep-alive for long-lived proxy tunnels (WARP/HTTP CONNECT).
	if transport.IdleConnTimeout == 0 || transport.IdleConnTimeout > 30*time.Second {
		transport.IdleConnTimeout = 30 * time.Second
	}
	if transport.MaxIdleConns == 0 || transport.MaxIdleConns < 100 {
		transport.MaxIdleConns = 100
	}
	if transport.MaxIdleConnsPerHost == 0 || transport.MaxIdleConnsPerHost < 32 {
		transport.MaxIdleConnsPerHost = 32
	}

	clients := make([]*http.Client, 0, proxyHTTPClientPoolSize)
	for i := 0; i < proxyHTTPClientPoolSize; i++ {
		client := &http.Client{Transport: transport}
		if timeout > 0 {
			client.Timeout = timeout
		}
		clients = append(clients, client)
	}
	pool := &proxyHTTPClientPool{clients: clients}
	actual, _ := proxyHTTPClientPools.LoadOrStore(key, pool)
	return actual.(*proxyHTTPClientPool).get()
}

// NewProxyAwareHTTPClient creates an HTTP client with proper proxy configuration priority:
// 1. Use auth.ProxyURL if configured (highest priority)
// 2. Use cfg.ProxyURL if auth proxy is not configured
// 3. Use RoundTripper from context if neither are configured
//
// When a proxy URL is configured, clients are taken from a small process-wide pool
// (see proxyHTTPClientPoolSize) so keep-alive connections to the upstream proxy are reused.
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
	// Priority 1: Use auth.ProxyURL if configured
	var proxyURL string
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}

	// Priority 2: Use cfg.ProxyURL if auth proxy is not configured
	if proxyURL == "" && cfg != nil {
		proxyURL = strings.TrimSpace(cfg.ProxyURL)
	}

	// If we have a proxy URL configured, use a pooled client/transport.
	if proxyURL != "" {
		if client := getPooledProxyHTTPClient(proxyURL, timeout); client != nil {
			return client
		}
		// If proxy setup failed, log and fall through to context RoundTripper
		log.Debugf("failed to setup proxy from URL: %s, falling back to context transport", proxyutil.Redact(proxyURL))
	}

	httpClient := &http.Client{}
	if timeout > 0 {
		httpClient.Timeout = timeout
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
