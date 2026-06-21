package helps

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
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
// When a request timeout is resolved (explicit caller timeout or
// cfg.RequestTimeoutSeconds), it is applied as a dial + TLS handshake timeout
// on transports constructed by this helper. It bounds the connect phase where
// the process wedge in issue #3944 actually occurs, without limiting how long
// an active streaming (SSE) response body can run.
//
// Parameters:
//   - ctx: The context containing optional RoundTripper
//   - cfg: The application configuration
//   - auth: The authentication information
//   - timeout: The connect + TLS timeout (0 means no timeout, unless cfg sets one)
//
// Returns:
//   - *http.Client: An HTTP client with configured proxy or transport
func NewProxyAwareHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	httpClient := &http.Client{}
	// Resolve a connect/TLS timeout. We intentionally do NOT set
	// http.Client.Timeout: that covers the entire request-response lifecycle
	// including reading the response body, which would abort healthy streaming
	// (SSE) responses mid-stream. ResponseHeaderTimeout is also avoided: it fires
	// after the request is fully written and can cut off upstreams that compute
	// before sending headers, and it does not bound DNS/TCP/TLS setup. Instead we
	// bound DialContext + TLSHandshakeTimeout, which is where a half-broken
	// connection actually hangs.
	var dialTimeout time.Duration
	if timeout > 0 {
		dialTimeout = timeout
	} else if cfg != nil && cfg.RequestTimeoutSeconds > 0 {
		dialTimeout = time.Duration(cfg.RequestTimeoutSeconds) * time.Second
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
			applyConnectTimeout(transport, dialTimeout)
			httpClient.Transport = transport
			return httpClient
		}
		// If proxy setup failed, log and fall through to context RoundTripper
		log.Debugf("failed to setup proxy from URL: %s, falling back to context transport", proxyutil.Redact(proxyURL))
	}

	// Priority 3: Use RoundTripper from context (typically from RoundTripperFor).
	// Context round trippers are custom (e.g. utls) and may be shared, so we do
	// not attempt to mutate them; the connect timeout only applies to transports
	// we construct ourselves below.
	if rt, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && rt != nil {
		httpClient.Transport = rt
		return httpClient
	}

	// No proxy and no context round tripper. When no timeout is configured, leave
	// Transport nil so callers that treat a nil transport as a signal to reuse a
	// shared singleton (e.g. antigravity_executor) keep their behavior — this
	// preserves the legacy code path exactly. Only install a cached, timeout-
	// bounded transport when a connect timeout is actually configured.
	if dialTimeout > 0 {
		httpClient.Transport = sharedDefaultTransportWithConnectTimeout(dialTimeout)
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

// applyConnectTimeout bounds the DNS/TCP dial and TLS handshake phases on a
// transport we own. It is a no-op when the timeout is zero (legacy behavior).
func applyConnectTimeout(transport *http.Transport, dialTimeout time.Duration) {
	if dialTimeout <= 0 {
		return
	}
	// Preserve any existing DialContext (e.g. SOCKS5 dialer) and wrap it with a
	// deadline. Some dialers (notably the SOCKS5 dialer installed by
	// proxyutil.BuildHTTPTransport) ignore the passed-in context, so we cannot
	// rely on context.WithTimeout alone: run the dial on a goroutine and race it
	// against the deadline, closing a late-arriving connection so the caller is
	// unblocked even when the underlying dialer is not context-aware. The goroutine
	// may leak until the OS-level dial times out, but that is strictly better than
	// hanging the request (issue #3944). A fully context-aware SOCKS5 dialer would
	// remove this workaround and is tracked as a follow-up.
	baseDial := transport.DialContext
	if baseDial == nil {
		baseDial = (&net.Dialer{}).DialContext
	}
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
		defer cancel()

		type dialResult struct {
			conn net.Conn
			err  error
		}
		ch := make(chan dialResult, 1)
		go func() {
			conn, err := baseDial(dialCtx, network, addr)
			ch <- dialResult{conn: conn, err: err}
		}()
		select {
		case <-dialCtx.Done():
			go func() {
				if r := <-ch; r.conn != nil {
					_ = r.conn.Close()
				}
			}()
			return nil, dialCtx.Err()
		case r := <-ch:
			return r.conn, r.err
		}
	}
	// Always override with the configured timeout so an explicit setting wins
	// over the http.DefaultTransport default (10s).
	transport.TLSHandshakeTimeout = dialTimeout
}

var defaultTransportCache sync.Map // map[int64]*http.Transport keyed by dialTimeout seconds

// sharedDefaultTransportWithConnectTimeout returns a cached clone of
// http.DefaultTransport with a connect + TLS handshake timeout applied. Caching
// per timeout value preserves connection reuse across requests instead of
// allocating (and leaking idle sockets for) a new transport on every call.
func sharedDefaultTransportWithConnectTimeout(dialTimeout time.Duration) *http.Transport {
	key := int64(dialTimeout)
	if cached, ok := defaultTransportCache.Load(key); ok {
		return cached.(*http.Transport)
	}
	base, ok := http.DefaultTransport.(*http.Transport)
	var transport *http.Transport
	if ok && base != nil {
		transport = base.Clone()
	} else {
		transport = &http.Transport{}
	}
	applyConnectTimeout(transport, dialTimeout)
	actual, _ := defaultTransportCache.LoadOrStore(key, transport)
	return actual.(*http.Transport)
}
