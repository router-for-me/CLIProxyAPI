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

// utlsProtectedHosts are the upstream hosts served with a client-matched uTLS
// fingerprint instead of Go's default TLS stack, to bypass TLS fingerprinting
// and to keep the TLS fingerprint consistent with the client User-Agent.
var utlsProtectedHosts = []string{"api.anthropic.com", "chatgpt.com"}

// utlsSessionCache is a shared TLS session cache enabling realistic session
// resumption for built-in (Chrome) profiles. Custom HelloCustom specs (Node)
// intentionally skip it to keep their ClientHello byte-stable across handshakes.
var utlsSessionCache = tls.NewLRUClientSessionCache(256)

// utlsHandshake wraps an already-established TCP conn in a uTLS client using the
// given profile and completes the handshake. It returns the TLS conn and the
// negotiated ALPN protocol ("h2", "http/1.1", or "").
func utlsHandshake(conn net.Conn, serverName string, p tlsProfile) (net.Conn, string, error) {
	cfg := &tls.Config{ServerName: serverName}
	var uconn *tls.UConn
	if p.spec != nil {
		uconn = tls.UClient(conn, cfg, tls.HelloCustom)
		if err := uconn.ApplyPreset(p.spec()); err != nil {
			return nil, "", err
		}
	} else {
		cfg.ClientSessionCache = utlsSessionCache
		uconn = tls.UClient(conn, cfg, p.helloID)
	}
	if err := uconn.Handshake(); err != nil {
		return nil, "", err
	}
	return uconn, uconn.ConnectionState().NegotiatedProtocol, nil
}

// utlsRoundTripper implements http.RoundTripper for HTTP/2 uTLS profiles by
// pooling one multiplexed http2 client connection per host.
type utlsRoundTripper struct {
	mu          sync.Mutex
	connections map[string]*http2.ClientConn
	pending     map[string]*sync.Cond
	dialer      proxy.Dialer
	profile     tlsProfile
}

func newUtlsRoundTripper(dialer proxy.Dialer, p tlsProfile) *utlsRoundTripper {
	return &utlsRoundTripper{
		connections: make(map[string]*http2.ClientConn),
		pending:     make(map[string]*sync.Cond),
		dialer:      dialer,
		profile:     p,
	}
}

func (t *utlsRoundTripper) getOrCreateConnection(host, addr string) (*http2.ClientConn, error) {
	t.mu.Lock()

	if h2Conn, ok := t.connections[host]; ok && h2Conn.CanTakeNewRequest() {
		t.mu.Unlock()
		return h2Conn, nil
	}

	if cond, ok := t.pending[host]; ok {
		cond.Wait()
		if h2Conn, ok := t.connections[host]; ok && h2Conn.CanTakeNewRequest() {
			t.mu.Unlock()
			return h2Conn, nil
		}
	}

	cond := sync.NewCond(&t.mu)
	t.pending[host] = cond
	t.mu.Unlock()

	h2Conn, err := t.createConnection(host, addr)

	t.mu.Lock()
	defer t.mu.Unlock()

	delete(t.pending, host)
	cond.Broadcast()

	if err != nil {
		return nil, err
	}

	t.connections[host] = h2Conn
	return h2Conn, nil
}

func (t *utlsRoundTripper) createConnection(host, addr string) (*http2.ClientConn, error) {
	conn, err := t.dialer.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}

	tlsConn, _, err := utlsHandshake(conn, host, t.profile)
	if err != nil {
		conn.Close()
		return nil, err
	}

	tr := newUtlsHTTP2Transport()
	h2Conn, err := tr.NewClientConn(tlsConn)
	if err != nil {
		tlsConn.Close()
		return nil, err
	}

	return h2Conn, nil
}

func newUtlsHTTP2Transport() *http2.Transport {
	return &http2.Transport{
		// Codex/ChatGPT requests manage Accept-Encoding explicitly. The x/net
		// HTTP/2 transport otherwise injects "Accept-Encoding: gzip" when the
		// request leaves it empty, which diverges from captured codex_cli_rs.
		DisableCompression: true,
		// Actively health-check pooled HTTP/2 connections. A proxy/NAT can silently
		// sever an idle upstream connection while both ends still believe it is alive;
		// a new request assigned to such a dead connection then hangs until the OS TCP
		// retransmit timeout (minutes), which surfaces as intermittent multi-minute
		// first-token stalls (TTFT ~0, total ~300s until the downstream proxy gives
		// up). With ReadIdleTimeout the transport sends an HTTP/2 PING after the
		// connection is idle that long, and PingTimeout fails the connection when the
		// PING is not answered in time, so the dead connection is evicted from the
		// pool (CanTakeNewRequest → false) and the request is rebuilt on a fresh one.
		ReadIdleTimeout: 15 * time.Second,
		PingTimeout:     15 * time.Second,
	}
}

func (t *utlsRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	hostname := req.URL.Hostname()
	port := req.URL.Port()
	if port == "" {
		port = "443"
	}
	addr := net.JoinHostPort(hostname, port)

	h2Conn, err := t.getOrCreateConnection(hostname, addr)
	if err != nil {
		return nil, err
	}

	resp, err := h2Conn.RoundTrip(req)
	if err != nil {
		t.mu.Lock()
		if cached, ok := t.connections[hostname]; ok && cached == h2Conn {
			delete(t.connections, hostname)
		}
		t.mu.Unlock()
		return nil, err
	}

	return resp, nil
}

// utlsDispatchRoundTripper routes protected hosts to their per-host uTLS
// round-tripper (h1 transport or pooled h2), and everything else to a standard
// fallback transport.
type utlsDispatchRoundTripper struct {
	perHost  map[string]http.RoundTripper
	fallback http.RoundTripper
}

func (d *utlsDispatchRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme == "https" {
		if rt, ok := d.perHost[strings.ToLower(req.URL.Hostname())]; ok {
			return rt.RoundTrip(req)
		}
	}
	return d.fallback.RoundTrip(req)
}

// NewUtlsHTTPClient creates an HTTP client that presents a client-matched uTLS
// fingerprint per protected host (Anthropic → Node.js/HTTP-1.1, chatgpt → Chrome/
// HTTP-2) and falls back to the standard transport for all other requests.
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

	// Test/override hook: with no proxy and an injected round-tripper, route all
	// traffic (including protected hosts) through it.
	if proxyURL == "" && ctxRoundTripper != nil {
		client := &http.Client{Transport: ctxRoundTripper}
		if timeout > 0 {
			client.Timeout = timeout
		}
		return client
	}

	// Base dialer: direct, or through the configured proxy.
	var dialer proxy.Dialer = proxy.Direct
	if proxyURL != "" {
		proxyDialer, mode, errBuild := proxyutil.BuildDialer(proxyURL)
		if errBuild != nil {
			log.Errorf("utls: failed to configure proxy dialer for %q: %v", proxyutil.Redact(proxyURL), errBuild)
		} else if mode != proxyutil.ModeInherit && proxyDialer != nil {
			dialer = proxyDialer
		}
	}

	// Fallback transport for non-protected hosts.
	var standardTransport http.RoundTripper = http.DefaultTransport
	if proxyURL != "" {
		if transport := buildProxyTransport(proxyURL); transport != nil {
			standardTransport = transport
		}
	}

	// Per-host uTLS round-trippers.
	disableNode := cfg != nil && cfg.DisableNodeTLSFingerprint
	perHost := make(map[string]http.RoundTripper, len(utlsProtectedHosts))
	for _, host := range utlsProtectedHosts {
		p, ok := resolveTLSProfile(host, disableNode)
		if !ok {
			continue
		}
		if p.http2 {
			perHost[host] = newUtlsRoundTripper(dialer, p)
		} else {
			perHost[host] = newUtlsH1RoundTripper(dialer, p)
		}
	}

	client := &http.Client{
		Transport: &utlsDispatchRoundTripper{perHost: perHost, fallback: standardTransport},
		// Persistent per-account cookie jar so Cloudflare clearance cookies are
		// stored and replayed across requests, matching the real Codex CLI.
		Jar: cookieJarForAuth(cfg, auth),
	}
	if timeout > 0 {
		client.Timeout = timeout
	}
	return client
}
