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
	connectTO   time.Duration
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
	if ctx == nil {
		ctx = context.Background()
	}
	if t.connectTO > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, t.connectTO)
		defer cancel()
	}
	conn, err := dialProxyContext(ctx, t.dialer, "tcp", addr)
	if err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	tlsConfig := &tls.Config{ServerName: host}
	tlsConn := tls.UClient(conn, tlsConfig, tls.HelloChrome_Auto)

	if err := tlsConn.Handshake(); err != nil {
		conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})

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
	port := req.URL.Port()
	if port == "" {
		port = "443"
	}
	addr := net.JoinHostPort(hostname, port)

	h2Conn, err := t.getOrCreateConnection(req.Context(), hostname, addr)
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

type proxyContextDialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

func dialProxyContext(ctx context.Context, dialer proxy.Dialer, network, address string) (net.Conn, error) {
	if contextDialer, ok := dialer.(proxyContextDialer); ok {
		return contextDialer.DialContext(ctx, network, address)
	}
	type dialResult struct {
		conn net.Conn
		err  error
	}
	resultCh := make(chan dialResult)
	abandoned := make(chan struct{})
	go func() {
		conn, err := dialer.Dial(network, address)
		select {
		case resultCh <- dialResult{conn: conn, err: err}:
		case <-abandoned:
			if conn != nil {
				_ = conn.Close()
			}
		}
	}()
	select {
	case result := <-resultCh:
		return result.conn, result.err
	case <-ctx.Done():
		close(abandoned)
		return nil, ctx.Err()
	}
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

	var utlsRT http.RoundTripper = newUtlsRoundTripper(proxyURL)
	var standardTransport http.RoundTripper = http.DefaultTransport
	if proxyURL != "" {
		if transport := buildProxyTransport(proxyURL); transport != nil {
			standardTransport = transport
		}
	} else if ctxRoundTripper != nil {
		utlsRT = ctxRoundTripper
		standardTransport = ctxRoundTripper
	}

	client := &http.Client{
		Transport: &fallbackRoundTripper{
			utls:     utlsRT,
			fallback: standardTransport,
		},
	}
	if timeout > 0 {
		client.Timeout = timeout
	}
	return client
}

// ConfigureHTTPClientTransportTimeouts applies connection and response-header
// limits without setting http.Client.Timeout, which would also cap long streams.
func ConfigureHTTPClientTransportTimeouts(client *http.Client, connectTimeout, responseHeaderTimeout time.Duration) {
	if client == nil {
		return
	}
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	client.Transport = configureRoundTripperTimeouts(transport, connectTimeout, responseHeaderTimeout)
}

func configureRoundTripperTimeouts(transport http.RoundTripper, connectTimeout, responseHeaderTimeout time.Duration) http.RoundTripper {
	switch current := transport.(type) {
	case *fallbackRoundTripper:
		clone := *current
		clone.utls = configureRoundTripperTimeouts(clone.utls, connectTimeout, responseHeaderTimeout)
		clone.fallback = configureRoundTripperTimeouts(clone.fallback, connectTimeout, responseHeaderTimeout)
		return &clone
	case *utlsRoundTripper:
		current.connectTO = connectTimeout
		return current
	case *http.Transport:
		clone := current.Clone()
		clone.ResponseHeaderTimeout = responseHeaderTimeout
		baseDial := clone.DialContext
		if baseDial == nil {
			baseDial = (&net.Dialer{}).DialContext
		}
		clone.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			if connectTimeout <= 0 {
				return baseDial(ctx, network, address)
			}
			dialCtx, cancel := context.WithTimeout(ctx, connectTimeout)
			defer cancel()
			return baseDial(dialCtx, network, address)
		}
		return clone
	default:
		return transport
	}
}
