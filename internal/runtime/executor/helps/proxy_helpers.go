package helps

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

var proxyTransportCache sync.Map
var proxyAwareClientCache sync.Map
var contextTransportCache sync.Map

const prewarmedConnTTL = 30 * time.Second

type prewarmedConn struct {
	conn      net.Conn
	expiresAt time.Time
}

type prewarmableTransport struct {
	base *http.Transport

	mu sync.Mutex

	origDialContext    func(context.Context, string, string) (net.Conn, error)
	origDialTLSContext func(context.Context, string, string) (net.Conn, error)
	prewarmed          map[string]prewarmedConn
}

func newPrewarmableTransport(base *http.Transport) *prewarmableTransport {
	if base == nil {
		return nil
	}
	clone := base.Clone()
	p := &prewarmableTransport{
		base:               clone,
		origDialContext:    clone.DialContext,
		origDialTLSContext: clone.DialTLSContext,
		prewarmed:          make(map[string]prewarmedConn),
	}
	clone.DialContext = p.dialContext
	if clone.DialTLSContext != nil {
		clone.DialTLSContext = p.dialTLSContext
	}
	return p
}

func (p *prewarmableTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return p.base.RoundTrip(req)
}

func (p *prewarmableTransport) CloseIdleConnections() {
	p.mu.Lock()
	for key, cached := range p.prewarmed {
		if cached.conn != nil {
			_ = cached.conn.Close()
		}
		delete(p.prewarmed, key)
	}
	p.mu.Unlock()
	p.base.CloseIdleConnections()
}

func (p *prewarmableTransport) dialContext(ctx context.Context, network string, addr string) (net.Conn, error) {
	if conn := p.takePrewarmedConn(network, addr, time.Now()); conn != nil {
		return conn, nil
	}
	return p.dialContextFallback(ctx, network, addr)
}

func (p *prewarmableTransport) dialTLSContext(ctx context.Context, network string, addr string) (net.Conn, error) {
	if conn := p.takePrewarmedTLSConn(network, addr, time.Now()); conn != nil {
		return conn, nil
	}
	if p.origDialTLSContext != nil {
		return p.origDialTLSContext(ctx, network, addr)
	}
	return p.dialContextFallback(ctx, network, addr)
}

func (p *prewarmableTransport) dialContextFallback(ctx context.Context, network string, addr string) (net.Conn, error) {
	if p.origDialContext != nil {
		return p.origDialContext(ctx, network, addr)
	}
	var dialer net.Dialer
	return dialer.DialContext(ctx, network, addr)
}

func (p *prewarmableTransport) prewarm(ctx context.Context, targetURL *url.URL) error {
	if p == nil || p.base == nil || targetURL == nil {
		return nil
	}

	req := &http.Request{
		Method: http.MethodGet,
		URL:    targetURL,
		Header: make(http.Header),
	}

	addr := canonicalAddr(targetURL.Hostname(), targetURL.Port(), targetURL.Scheme)
	if addr == "" {
		return fmt.Errorf("prewarm transport: target address is empty")
	}

	if p.base.Proxy != nil {
		proxyURL, errProxy := p.base.Proxy(req)
		if errProxy != nil {
			return errProxy
		}
		if proxyURL != nil {
			addr = canonicalAddr(proxyURL.Hostname(), proxyURL.Port(), proxyURL.Scheme)
			if addr == "" {
				return fmt.Errorf("prewarm transport: proxy address is empty")
			}
		}
	}

	var (
		conn    net.Conn
		errDial error
	)
	if proxyUsed(req, p.base) || !strings.EqualFold(targetURL.Scheme, "https") || p.origDialTLSContext == nil {
		conn, errDial = p.dialContextFallback(ctx, "tcp", addr)
		if errDial == nil {
			p.storePrewarmedConn("tcp", addr, conn, time.Now().Add(prewarmedConnTTL))
		}
	} else {
		conn, errDial = p.origDialTLSContext(ctx, "tcp", addr)
		if errDial == nil {
			p.storePrewarmedTLSConn("tcp", addr, conn, time.Now().Add(prewarmedConnTTL))
		}
	}
	if errDial != nil {
		return errDial
	}
	return nil
}

func (p *prewarmableTransport) takePrewarmedConn(network string, addr string, now time.Time) net.Conn {
	key := prewarmedConnKey(network, addr)
	p.mu.Lock()
	defer p.mu.Unlock()
	cached, ok := p.prewarmed[key]
	if !ok {
		return nil
	}
	delete(p.prewarmed, key)
	if cached.conn == nil {
		return nil
	}
	if !cached.expiresAt.IsZero() && now.After(cached.expiresAt) {
		_ = cached.conn.Close()
		return nil
	}
	return cached.conn
}

func (p *prewarmableTransport) storePrewarmedConn(network string, addr string, conn net.Conn, expiresAt time.Time) {
	if conn == nil {
		return
	}
	key := prewarmedConnKey(network, addr)
	p.mu.Lock()
	if cached, ok := p.prewarmed[key]; ok && cached.conn != nil {
		_ = cached.conn.Close()
	}
	p.prewarmed[key] = prewarmedConn{conn: conn, expiresAt: expiresAt}
	p.mu.Unlock()
}

func (p *prewarmableTransport) takePrewarmedTLSConn(network string, addr string, now time.Time) net.Conn {
	return p.takePrewarmedConn(network+"+tls", addr, now)
}

func (p *prewarmableTransport) storePrewarmedTLSConn(network string, addr string, conn net.Conn, expiresAt time.Time) {
	p.storePrewarmedConn(network+"+tls", addr, conn, expiresAt)
}

func prewarmedConnKey(network string, addr string) string {
	return strings.TrimSpace(network) + "\x00" + strings.TrimSpace(addr)
}

func canonicalAddr(host string, port string, scheme string) string {
	host = strings.TrimSpace(host)
	port = strings.TrimSpace(port)
	if host == "" {
		return ""
	}
	if port == "" {
		if strings.EqualFold(strings.TrimSpace(scheme), "http") {
			port = "80"
		} else {
			port = "443"
		}
	}
	return net.JoinHostPort(host, port)
}

func proxyUsed(req *http.Request, transport *http.Transport) bool {
	if req == nil || transport == nil || transport.Proxy == nil {
		return false
	}
	proxyURL, errProxy := transport.Proxy(req)
	return errProxy == nil && proxyURL != nil
}

func cloneDefaultTransport() *http.Transport {
	if transport, ok := http.DefaultTransport.(*http.Transport); ok && transport != nil {
		return transport.Clone()
	}
	return &http.Transport{}
}

func wrapPrewarmableRoundTripper(rt http.RoundTripper) http.RoundTripper {
	switch transport := rt.(type) {
	case nil:
		return cachedProxyTransport("")
	case *prewarmableTransport:
		return transport
	case *http.Transport:
		key := fmt.Sprintf("%p", transport)
		if cached, ok := contextTransportCache.Load(key); ok {
			if wrapped, okWrapped := cached.(http.RoundTripper); okWrapped {
				return wrapped
			}
		}
		wrapped := newPrewarmableTransport(transport)
		actual, _ := contextTransportCache.LoadOrStore(key, wrapped)
		if wrappedRT, okWrapped := actual.(http.RoundTripper); okWrapped {
			return wrappedRT
		}
		return wrapped
	default:
		return rt
	}
}

// NewProxyAwareHTTPClient creates an HTTP client with proper proxy configuration priority:
// 1. Use auth.ProxyURL if configured (highest priority)
// 2. Use cfg.ProxyURL if auth proxy is not configured
// 3. Use RoundTripper from context if neither are configured
func NewProxyAwareHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	if rt, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && rt != nil {
		if auth != nil && strings.TrimSpace(auth.ProxyURL) != "" {
			return &http.Client{
				Transport: wrapPrewarmableRoundTripper(rt),
				Timeout:   timeout,
			}
		}
	}

	var proxyURL string
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}
	if proxyURL == "" && cfg != nil {
		proxyURL = strings.TrimSpace(cfg.ProxyURL)
	}

	if proxyURL != "" {
		httpClient := cachedProxyAwareHTTPClient(proxyURL, timeout)
		if httpClient != nil {
			return httpClient
		}
		log.Debugf("failed to setup proxy from URL: %s, falling back to context transport", proxyURL)
	}

	if rt, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && rt != nil {
		return &http.Client{
			Transport: wrapPrewarmableRoundTripper(rt),
			Timeout:   timeout,
		}
	}

	return cachedProxyAwareHTTPClient("", timeout)
}

// PrewarmProxyAwareHTTPClient primes the effective transport used for later requests
// without issuing an upstream HTTP API request.
func PrewarmProxyAwareHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, rawTargetURL string) error {
	targetURL, errParse := url.Parse(strings.TrimSpace(rawTargetURL))
	if errParse != nil {
		return errParse
	}
	client := NewProxyAwareHTTPClient(ctx, cfg, auth, 0)
	if client == nil || client.Transport == nil {
		return nil
	}
	transport, ok := client.Transport.(*prewarmableTransport)
	if !ok || transport == nil {
		return nil
	}
	return transport.prewarm(ctx, targetURL)
}

func cachedProxyAwareHTTPClient(proxyURL string, timeout time.Duration) *http.Client {
	key := buildProxyAwareClientCacheKey(proxyURL, timeout)
	if cached, ok := proxyAwareClientCache.Load(key); ok {
		if client, okClient := cached.(*http.Client); okClient {
			return client
		}
	}

	client := buildProxyAwareHTTPClient(proxyURL, timeout)
	if client == nil {
		return nil
	}

	actual, _ := proxyAwareClientCache.LoadOrStore(key, client)
	if cached, ok := actual.(*http.Client); ok {
		return cached
	}
	return client
}

func buildProxyAwareClientCacheKey(proxyURL string, timeout time.Duration) string {
	return strings.TrimSpace(proxyURL) + "\x00" + timeout.String()
}

func buildProxyAwareHTTPClient(proxyURL string, timeout time.Duration) *http.Client {
	httpClient := &http.Client{Timeout: timeout}
	transport := cachedProxyTransport(proxyURL)
	if transport == nil {
		return nil
	}
	httpClient.Transport = transport
	return httpClient
}

func cachedProxyTransport(proxyURL string) http.RoundTripper {
	proxyURL = strings.TrimSpace(proxyURL)
	key := proxyURL
	if key == "" {
		key = "\x00default"
	}
	if cached, ok := proxyTransportCache.Load(key); ok {
		if transport, okTransport := cached.(http.RoundTripper); okTransport {
			return transport
		}
	}

	transport := buildProxyTransport(proxyURL)
	if transport == nil {
		return nil
	}

	actual, _ := proxyTransportCache.LoadOrStore(key, transport)
	if cached, ok := actual.(http.RoundTripper); ok {
		return cached
	}
	return transport
}

// buildProxyTransport creates an HTTP transport configured for the given proxy URL.
func buildProxyTransport(proxyURL string) http.RoundTripper {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return newPrewarmableTransport(cloneDefaultTransport())
	}

	transport, _, errBuild := proxyutil.BuildHTTPTransport(proxyURL)
	if errBuild != nil {
		log.Errorf("%v", errBuild)
		return nil
	}
	if transport == nil {
		return newPrewarmableTransport(cloneDefaultTransport())
	}
	return newPrewarmableTransport(transport)
}
