package helps

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	tls "github.com/refraction-networking/utls"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/http2"
	"golang.org/x/net/proxy"
)

// utlsRoundTripper implements http.RoundTripper using utls with Chrome fingerprint
// to bypass Cloudflare's TLS fingerprinting on Anthropic domains.
type utlsRoundTripper struct {
	mu          sync.Mutex
	connections map[string]*http2.ClientConn
	pending     map[string]*sync.Cond
	dialer      proxy.Dialer
}

type cachedUtlsRoundTripper struct {
	utls     http.RoundTripper
	fallback http.RoundTripper
}

const maxCachedUtlsRoundTrippers = 128

var utlsRoundTripperCache = struct {
	mu    sync.RWMutex
	items map[string]cachedUtlsRoundTripper
}{
	items: make(map[string]cachedUtlsRoundTripper),
}

func newUtlsRoundTripper(proxyURL string) *utlsRoundTripper {
	var dialer proxy.Dialer = proxy.Direct
	if proxyURL != "" {
		proxyDialer, mode, errBuild := proxyutil.BuildDialer(proxyURL)
		if errBuild != nil {
			log.Errorf("utls: failed to configure proxy dialer for %q: %v", proxyutil.Redact(proxyURL), errBuild)
		} else if mode != proxyutil.ModeInherit && proxyDialer != nil {
			dialer = proxyDialer
		}
	}
	return &utlsRoundTripper{
		connections: make(map[string]*http2.ClientConn),
		pending:     make(map[string]*sync.Cond),
		dialer:      dialer,
	}
}

func (t *utlsRoundTripper) getOrCreateConnection(hostname, addr string) (*http2.ClientConn, error) {
	t.mu.Lock()

	if h2Conn, ok := t.connections[addr]; ok && h2Conn.CanTakeNewRequest() {
		t.mu.Unlock()
		return h2Conn, nil
	}

	if cond, ok := t.pending[addr]; ok {
		cond.Wait()
		if h2Conn, ok := t.connections[addr]; ok && h2Conn.CanTakeNewRequest() {
			t.mu.Unlock()
			return h2Conn, nil
		}
	}

	cond := sync.NewCond(&t.mu)
	t.pending[addr] = cond
	t.mu.Unlock()

	h2Conn, err := t.createConnection(hostname, addr)

	t.mu.Lock()
	defer t.mu.Unlock()

	delete(t.pending, addr)
	cond.Broadcast()

	if err != nil {
		return nil, err
	}

	t.connections[addr] = h2Conn
	return h2Conn, nil
}

func (t *utlsRoundTripper) createConnection(host, addr string) (*http2.ClientConn, error) {
	conn, err := t.dialer.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}

	tlsConfig := &tls.Config{ServerName: host}
	tlsConn := tls.UClient(conn, tlsConfig, tls.HelloChrome_Auto)

	if err := tlsConn.Handshake(); err != nil {
		conn.Close()
		return nil, err
	}

	tr := &http2.Transport{}
	h2Conn, err := tr.NewClientConn(tlsConn)
	if err != nil {
		tlsConn.Close()
		return nil, err
	}

	return h2Conn, nil
}

func (t *utlsRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	hostname := req.URL.Hostname()
	addr := utlsConnectionKey(hostname, req.URL.Port())

	h2Conn, err := t.getOrCreateConnection(hostname, addr)
	if err != nil {
		return nil, err
	}

	resp, err := h2Conn.RoundTrip(req)
	if err != nil {
		t.mu.Lock()
		if cached, ok := t.connections[addr]; ok && cached == h2Conn {
			delete(t.connections, addr)
		}
		t.mu.Unlock()
		return nil, err
	}

	return resp, nil
}

func utlsConnectionKey(hostname, port string) string {
	if port == "" {
		port = "443"
	}
	return net.JoinHostPort(hostname, port)
}

// utlsProtectedHosts contains the hosts that should use utls Chrome TLS fingerprint
// to bypass Cloudflare's TLS fingerprinting.
var utlsProtectedHosts = map[string]struct{}{
	"api.anthropic.com": {},
	"chatgpt.com":       {},
}

// fallbackRoundTripper uses utls for protected HTTPS hosts and falls back to
// standard transport for all other requests.
type fallbackRoundTripper struct {
	utls     http.RoundTripper
	fallback http.RoundTripper
}

func (f *fallbackRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme == "https" {
		if _, ok := utlsProtectedHosts[strings.ToLower(req.URL.Hostname())]; ok {
			return f.utls.RoundTrip(req)
		}
	}
	return f.fallback.RoundTrip(req)
}

func cachedUtlsRoundTrippers(proxyURL string) cachedUtlsRoundTripper {
	utlsRoundTripperCache.mu.RLock()
	cached, ok := utlsRoundTripperCache.items[proxyURL]
	utlsRoundTripperCache.mu.RUnlock()
	if ok {
		return cached
	}

	cached = cachedUtlsRoundTripper{
		utls:     newUtlsRoundTripper(proxyURL),
		fallback: http.DefaultTransport,
	}
	if proxyURL != "" {
		if transport := buildProxyTransport(proxyURL); transport != nil {
			cached.fallback = transport
		}
	}

	utlsRoundTripperCache.mu.Lock()
	defer utlsRoundTripperCache.mu.Unlock()
	if existing, ok := utlsRoundTripperCache.items[proxyURL]; ok {
		return existing
	}
	if len(utlsRoundTripperCache.items) >= maxCachedUtlsRoundTrippers {
		return cached
	}
	utlsRoundTripperCache.items[proxyURL] = cached
	return cached
}

// NewUtlsHTTPClient creates an HTTP client using utls Chrome TLS fingerprint.
// Use this for provider requests that need a Chrome-like TLS fingerprint.
// Falls back to standard transport for non-HTTPS requests.
func NewUtlsHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	var proxyURL string
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}
	if proxyURL == "" && cfg != nil {
		proxyURL = strings.TrimSpace(cfg.ProxyURL)
	}

	var ctxRoundTripper http.RoundTripper
	if ctx != nil {
		ctxRoundTripper, _ = ctx.Value("cliproxy.roundtripper").(http.RoundTripper)
	}

	var roundTrippers cachedUtlsRoundTripper
	if proxyURL == "" && ctxRoundTripper != nil {
		roundTrippers = cachedUtlsRoundTripper{
			utls:     ctxRoundTripper,
			fallback: ctxRoundTripper,
		}
	} else {
		roundTrippers = cachedUtlsRoundTrippers(proxyURL)
	}

	client := &http.Client{
		Transport: &fallbackRoundTripper{
			utls:     roundTrippers.utls,
			fallback: roundTrippers.fallback,
		},
	}
	if timeout > 0 {
		client.Timeout = timeout
	}
	return client
}
