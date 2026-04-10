package executor

import (
	"bufio"
	"context"
	stdtls "crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	tls "github.com/refraction-networking/utls"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/http2"
	"golang.org/x/net/proxy"
)

// utlsHosts lists hosts that should use Chrome TLS fingerprinting.
var utlsHosts = map[string]bool{
	"api.anthropic.com":                         true,
	"cloudcode-pa.googleapis.com":               true,
	"daily-cloudcode-pa.googleapis.com":         true,
	"daily-cloudcode-pa.sandbox.googleapis.com": true,
	"oauth2.googleapis.com":                     true,
	"www.googleapis.com":                        true,
	"cloudresourcemanager.googleapis.com":        true,
	"serviceusage.googleapis.com":               true,
}

const (
	utlsDialTimeout      = 10 * time.Second
	utlsHandshakeTimeout = 10 * time.Second
)

// ---------- Context-aware dialer ----------

// contextDialer wraps a proxy.Dialer to support context-based cancellation and timeouts.
type contextDialer struct {
	inner proxy.Dialer
}

func (d *contextDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	type connResult struct {
		conn net.Conn
		err  error
	}
	ch := make(chan connResult, 1)
	go func() {
		c, err := d.inner.Dial(network, addr)
		ch <- connResult{c, err}
	}()
	select {
	case <-ctx.Done():
		// Attempt to clean up the background dial; if it returns later the conn will be closed.
		go func() {
			if r := <-ch; r.conn != nil {
				r.conn.Close()
			}
		}()
		return nil, ctx.Err()
	case r := <-ch:
		return r.conn, r.err
	}
}

// ---------- HTTP CONNECT proxy dialer ----------

// httpConnectDialer establishes a TCP connection through an HTTP or HTTPS proxy using CONNECT.
type httpConnectDialer struct {
	proxyAddr string // host:port
	proxyAuth string // base64-encoded "user:pass", or empty
	tlsProxy  bool   // true if the proxy itself uses TLS (https:// scheme)
}

func newHTTPConnectDialer(proxyURL *url.URL) *httpConnectDialer {
	host := proxyURL.Host
	if !strings.Contains(host, ":") {
		if proxyURL.Scheme == "https" {
			host += ":443"
		} else {
			host += ":80"
		}
	}
	var auth string
	if proxyURL.User != nil {
		username := proxyURL.User.Username()
		password, _ := proxyURL.User.Password()
		auth = base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
	}
	return &httpConnectDialer{
		proxyAddr: host,
		proxyAuth: auth,
		tlsProxy:  proxyURL.Scheme == "https",
	}
}

func (d *httpConnectDialer) DialContext(ctx context.Context, _, targetAddr string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: utlsDialTimeout}
	rawConn, err := dialer.DialContext(ctx, "tcp", d.proxyAddr)
	if err != nil {
		return nil, fmt.Errorf("connect to proxy %s: %w", d.proxyAddr, err)
	}

	// For HTTPS proxies, TLS-wrap the connection to the proxy first.
	var conn net.Conn = rawConn
	if d.tlsProxy {
		proxyHost, _, _ := net.SplitHostPort(d.proxyAddr)
		tlsConn := stdtls.Client(rawConn, &stdtls.Config{ServerName: proxyHost})
		if deadline, ok := ctx.Deadline(); ok {
			tlsConn.SetDeadline(deadline)
		} else {
			tlsConn.SetDeadline(time.Now().Add(utlsHandshakeTimeout))
		}
		if err := tlsConn.Handshake(); err != nil {
			rawConn.Close()
			return nil, fmt.Errorf("TLS to proxy %s: %w", d.proxyAddr, err)
		}
		tlsConn.SetDeadline(time.Time{})
		conn = tlsConn
	}

	connectReq := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n", targetAddr, targetAddr)
	if d.proxyAuth != "" {
		connectReq += "Proxy-Authorization: Basic " + d.proxyAuth + "\r\n"
	}
	connectReq += "\r\n"

	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	} else {
		conn.SetDeadline(time.Now().Add(utlsDialTimeout))
	}

	if _, err := conn.Write([]byte(connectReq)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("write CONNECT: %w", err)
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("read CONNECT response: %w", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		conn.Close()
		return nil, fmt.Errorf("CONNECT to %s via %s: %s", targetAddr, d.proxyAddr, resp.Status)
	}

	// Clear deadline after CONNECT succeeds.
	conn.SetDeadline(time.Time{})
	return conn, nil
}

// ---------- uTLS Round Tripper ----------

// utlsRoundTripper implements http.RoundTripper using utls with Chrome fingerprint.
type utlsRoundTripper struct {
	mu          sync.Mutex
	connections map[string]*http2.ClientConn
	pending     map[string]*sync.Cond
	dialFunc    func(ctx context.Context, network, addr string) (net.Conn, error)
}

// newUtlsRoundTripper creates a uTLS round tripper that correctly handles:
// - No proxy: direct dial with context timeout
// - SOCKS5 proxy: via proxy.Dialer wrapped with context
// - HTTP/HTTPS proxy: via HTTP CONNECT tunnel
func newUtlsRoundTripper(proxyURL string) *utlsRoundTripper {
	rt := &utlsRoundTripper{
		connections: make(map[string]*http2.ClientConn),
		pending:     make(map[string]*sync.Cond),
	}

	if proxyURL == "" {
		// Direct connection with timeout.
		rt.dialFunc = (&net.Dialer{Timeout: utlsDialTimeout}).DialContext
		return rt
	}

	parsed, err := url.Parse(proxyURL)
	if err != nil {
		log.Errorf("utls: invalid proxy URL: %v", err)
		rt.dialFunc = (&net.Dialer{Timeout: utlsDialTimeout}).DialContext
		return rt
	}

	switch parsed.Scheme {
	case "socks5", "socks5h":
		proxyDialer, mode, errBuild := proxyutil.BuildDialer(proxyURL)
		if errBuild != nil || mode == proxyutil.ModeInherit || proxyDialer == nil {
			log.Errorf("utls: failed to build SOCKS5 dialer: %v", errBuild)
			rt.dialFunc = (&net.Dialer{Timeout: utlsDialTimeout}).DialContext
		} else {
			rt.dialFunc = (&contextDialer{inner: proxyDialer}).DialContext
		}
	case "http", "https":
		rt.dialFunc = newHTTPConnectDialer(parsed).DialContext
	default:
		log.Errorf("utls: unsupported proxy scheme %q, falling back to direct", parsed.Scheme)
		rt.dialFunc = (&net.Dialer{Timeout: utlsDialTimeout}).DialContext
	}

	return rt
}

func (t *utlsRoundTripper) getOrCreateConnection(ctx context.Context, host, addr string) (*http2.ClientConn, error) {
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

	h2Conn, err := t.createConnection(ctx, host, addr)

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

func (t *utlsRoundTripper) createConnection(ctx context.Context, host, addr string) (*http2.ClientConn, error) {
	conn, err := t.dialFunc(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}

	// Set a deadline for the TLS handshake.
	if err := conn.SetDeadline(time.Now().Add(utlsHandshakeTimeout)); err != nil {
		conn.Close()
		return nil, err
	}

	tlsConfig := &tls.Config{ServerName: host}
	tlsConn := tls.UClient(conn, tlsConfig, tls.HelloChrome_Auto)
	if err := tlsConn.Handshake(); err != nil {
		conn.Close()
		return nil, err
	}

	// Clear the deadline after handshake succeeds.
	if err := conn.SetDeadline(time.Time{}); err != nil {
		tlsConn.Close()
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
	host := req.URL.Host
	addr := host
	if !strings.Contains(addr, ":") {
		addr += ":443"
	}
	hostname := req.URL.Hostname()

	h2Conn, err := t.getOrCreateConnection(req.Context(), hostname, addr)
	if err != nil {
		return nil, err
	}

	resp, err := h2Conn.RoundTrip(req)
	if err != nil {
		t.mu.Lock()
		if cached, ok := t.connections[hostname]; ok && cached == h2Conn {
			delete(t.connections, hostname)
			go cached.Close() // close broken connections to avoid leaking
		}
		t.mu.Unlock()
		return nil, err
	}
	return resp, nil
}

// ---------- Fallback Round Tripper ----------

// utlsFallbackRoundTripper uses uTLS for known hosts and falls back to a standard
// transport for everything else.
type utlsFallbackRoundTripper struct {
	utls     *utlsRoundTripper
	fallback http.RoundTripper
}

func (f *utlsFallbackRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme == "https" && utlsHosts[req.URL.Hostname()] {
		return f.utls.RoundTrip(req)
	}
	return f.fallback.RoundTrip(req)
}

// ---------- Singleton shared transport ----------

var (
	sharedAntigravityTransport   = make(map[string]*utlsFallbackRoundTripper)
	sharedAntigravityTransportMu sync.Mutex
)

func getSharedAntigravityTransport(proxyURL string) *utlsFallbackRoundTripper {
	sharedAntigravityTransportMu.Lock()
	defer sharedAntigravityTransportMu.Unlock()

	if rt, ok := sharedAntigravityTransport[proxyURL]; ok {
		return rt
	}

	var fallback http.RoundTripper
	if proxyURL != "" {
		if t := buildProxyTransport(proxyURL); t != nil {
			fallback = t
		}
	}
	if fallback == nil {
		if base, ok := http.DefaultTransport.(*http.Transport); ok {
			fallback = base.Clone()
		} else {
			fallback = &http.Transport{
				DialContext: (&net.Dialer{Timeout: utlsDialTimeout}).DialContext,
			}
		}
	}

	rt := &utlsFallbackRoundTripper{
		utls:     newUtlsRoundTripper(proxyURL),
		fallback: fallback,
	}
	sharedAntigravityTransport[proxyURL] = rt
	return rt
}

// newAntigravityUtlsHTTPClient creates an HTTP client that uses Chrome TLS fingerprint
// for domains in utlsHosts and standard Go HTTP for everything else.
// The transport is shared/singleton per proxy URL so connection pooling persists.
// ctx is checked for a conductor-injected RoundTripper (cliproxy.roundtripper);
// if present, it wraps that transport for non-uTLS hosts.
func newAntigravityUtlsHTTPClient(ctx context.Context, proxyURL string, timeout time.Duration) *http.Client {
	transport := getSharedAntigravityTransport(proxyURL)

	var rt http.RoundTripper = transport
	if ctxRT, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && ctxRT != nil {
		rt = &utlsFallbackRoundTripper{
			utls:     transport.utls,
			fallback: ctxRT,
		}
	}

	client := &http.Client{Transport: rt}
	if timeout > 0 {
		client.Timeout = timeout
	}
	return client
}
