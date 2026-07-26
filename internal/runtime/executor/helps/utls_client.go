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

// utlsReadIdleTimeout is a liveness health check, not a request timeout: when a
// pooled connection has been silent for this long, an HTTP/2 PING is sent and
// the connection is closed only if the peer does not answer. This detects
// connections silently dropped by NATs/firewalls, which per-request dialing
// never had to worry about.
const utlsReadIdleTimeout = 60 * time.Second

// utlsIdleConnTimeout closes a pooled connection with no active streams after
// this long, matching http.DefaultTransport's idle-connection hygiene that the
// rest of the codebase already relies on.
const utlsIdleConnTimeout = 90 * time.Second

// utlsRoundTripper implements http.RoundTripper using utls with Chrome fingerprint
// to bypass Cloudflare's TLS fingerprinting on Anthropic domains.
// Connections are pooled per host: when every pooled connection is at its
// concurrent-stream limit, an additional connection is dialed and kept
// alongside the others (never replacing them).
type utlsRoundTripper struct {
	mu     sync.Mutex
	conns  map[string][]*http2.ClientConn
	dials  map[string]chan struct{}
	dialer proxy.Dialer
}

// utlsRoundTrippers caches one round tripper per proxy URL so HTTP/2
// connections are reused across requests instead of being re-established
// (TCP + uTLS handshake) on every call.
var (
	utlsRoundTrippersMu sync.Mutex
	utlsRoundTrippers   = make(map[string]*utlsRoundTripper)
)

func sharedUtlsRoundTripper(proxyURL string) *utlsRoundTripper {
	utlsRoundTrippersMu.Lock()
	defer utlsRoundTrippersMu.Unlock()
	if rt, ok := utlsRoundTrippers[proxyURL]; ok {
		return rt
	}
	rt := newUtlsRoundTripper(proxyURL)
	utlsRoundTrippers[proxyURL] = rt
	return rt
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
		conns:  make(map[string][]*http2.ClientConn),
		dials:  make(map[string]chan struct{}),
		dialer: dialer,
	}
}

// reserveConnLocked prunes dead connections for host and reserves a stream on
// the first pooled connection that can take one. Callers must hold t.mu.
// ReserveNewRequest (rather than CanTakeNewRequest) is used so the slot cannot
// be stolen between selection and RoundTrip; the reservation is consumed by
// the next RoundTrip on that connection.
func (t *utlsRoundTripper) reserveConnLocked(host string) *http2.ClientConn {
	kept := t.conns[host][:0]
	var reserved *http2.ClientConn
	for _, conn := range t.conns[host] {
		state := conn.State()
		if state.Closed || state.Closing {
			// Closed/closing connections terminate on their own once their
			// remaining streams finish; dropping the reference is enough.
			continue
		}
		kept = append(kept, conn)
		if reserved == nil && conn.ReserveNewRequest() {
			reserved = conn
		}
	}
	t.conns[host] = kept
	return reserved
}

func (t *utlsRoundTripper) getOrCreateConnection(ctx context.Context, host, addr string) (*http2.ClientConn, error) {
	for {
		t.mu.Lock()
		if conn := t.reserveConnLocked(host); conn != nil {
			t.mu.Unlock()
			return conn, nil
		}
		if inflight, ok := t.dials[host]; ok {
			t.mu.Unlock()
			select {
			case <-inflight:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			continue
		}
		done := make(chan struct{})
		t.dials[host] = done
		t.mu.Unlock()

		conn, err := t.createConnection(ctx, host, addr)

		t.mu.Lock()
		delete(t.dials, host)
		close(done)
		if err != nil {
			t.mu.Unlock()
			return nil, err
		}
		t.conns[host] = append(t.conns[host], conn)
		if !conn.ReserveNewRequest() {
			// Freshly dialed connections always have free slots; defensive.
			t.mu.Unlock()
			continue
		}
		t.mu.Unlock()
		return conn, nil
	}
}

// dialContext propagates request cancellation into the dial when the
// underlying dialer supports it. No fixed timeout is applied.
func (t *utlsRoundTripper) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if contextDialer, ok := t.dialer.(proxy.ContextDialer); ok {
		return contextDialer.DialContext(ctx, network, addr)
	}
	return t.dialer.Dial(network, addr)
}

func (t *utlsRoundTripper) createConnection(ctx context.Context, host, addr string) (*http2.ClientConn, error) {
	conn, err := t.dialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}

	tlsConfig := &tls.Config{ServerName: host}
	tlsConn := tls.UClient(conn, tlsConfig, tls.HelloChrome_Auto)

	if err := tlsConn.HandshakeContext(ctx); err != nil {
		conn.Close()
		return nil, err
	}

	tr := &http2.Transport{
		ReadIdleTimeout: utlsReadIdleTimeout,
		IdleConnTimeout: utlsIdleConnTimeout,
	}
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
		// Stream-level errors (e.g. caller cancellation) must not evict a
		// connection other requests are using; only drop connections that
		// are actually closed or draining (GOAWAY).
		if state := h2Conn.State(); state.Closed || state.Closing {
			t.mu.Lock()
			conns := t.conns[hostname]
			for i, cached := range conns {
				if cached == h2Conn {
					t.conns[hostname] = append(conns[:i], conns[i+1:]...)
					break
				}
			}
			t.mu.Unlock()
		}
		return nil, err
	}

	return resp, nil
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

	var utlsRT http.RoundTripper
	var standardTransport http.RoundTripper = http.DefaultTransport
	if proxyURL != "" {
		utlsRT = sharedUtlsRoundTripper(proxyURL)
		if transport := sharedProxyTransport(proxyURL); transport != nil {
			standardTransport = transport
		}
	} else if ctxRoundTripper != nil {
		utlsRT = ctxRoundTripper
		standardTransport = ctxRoundTripper
	} else {
		utlsRT = sharedUtlsRoundTripper("")
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
