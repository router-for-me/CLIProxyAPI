package transport

import (
	"bufio"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	fhttp2 "github.com/bogdanfinn/fhttp/http2"
	utls "github.com/refraction-networking/utls"
)

// Doer is the minimal interface the wire decorators and the client rely on.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// DialContextFunc dials the upstream TCP connection, optionally through a
// proxy. It mirrors net.Dialer.DialContext so callers can plug in SOCKS/HTTP
// proxy dialers.
type DialContextFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// Client dials upstream over uTLS so the TLS ClientHello (JA3/JA4) impersonates
// Chrome, then speaks HTTP/2 or HTTP/1.1 according to the negotiated ALPN.
//
// The HTTP/2 layer is fhttp rather than golang.org/x/net/http2 so the SETTINGS
// frame, connection WINDOW_UPDATE, pseudo-header order and header order match
// Chrome as well. See chrome.go — a Chrome TLS handshake followed by Go's
// HTTP/2 preface is a stronger bot signal than not impersonating at all.
type Client struct {
	timeout     time.Duration
	dialContext DialContextFunc
	h2          *fhttp2.Transport

	mu      sync.Mutex
	conns   map[string]*fhttp2.ClientConn // authority (host:port) -> pooled h2 conn
	dialing map[string]*sync.Mutex        // per-authority dial serialization
}

// New creates a Chrome-impersonating transport with the default net dialer.
func New(timeout time.Duration) *Client {
	return NewWithDialContext(timeout, nil)
}

// NewWithDialContext creates a Chrome-impersonating transport that uses the
// supplied dialer (e.g. a SOCKS5 proxy dialer) for the underlying TCP connection.
func NewWithDialContext(timeout time.Duration, dialContext DialContextFunc) *Client {
	if dialContext == nil {
		dialContext = (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	}
	h2 := newChromeH2Transport()
	// ReadIdleTimeout keeps pooled conns healthy via PING; the conn is dialed
	// and TLS-handshaked externally, so the h2 transport never dials itself.
	h2.ReadIdleTimeout = 30 * time.Second
	h2.PingTimeout = 15 * time.Second
	return &Client{
		timeout:     timeout,
		dialContext: dialContext,
		h2:          h2,
		conns:       make(map[string]*fhttp2.ClientConn),
		dialing:     make(map[string]*sync.Mutex),
	}
}

// Do executes the request, applying the per-request timeout when configured.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	// Ensure req.Host is set and remove any explicit Host header. HTTP/2
	// transports use :authority and may reject or mishandle a Host header.
	if h := req.Header.Get("Host"); h != "" {
		if req.Host == "" {
			req.Host = h
		}
		req.Header.Del("Host")
	}
	if req.Host == "" {
		req.Host = req.URL.Host
	}

	if c.timeout > 0 {
		ctx, cancel := context.WithTimeout(req.Context(), c.timeout)
		req = req.WithContext(ctx)
		resp, err := c.roundTrip(req)
		if err != nil {
			cancel()
			return nil, err
		}
		resp.Body = &cancelBody{ReadCloser: resp.Body, cancel: cancel}
		return resp, nil
	}
	return c.roundTrip(req)
}

func (c *Client) roundTrip(req *http.Request) (*http.Response, error) {
	authority := authorityAddr(req.URL)

	// Fast path: reuse a healthy pooled HTTP/2 connection.
	if cc := c.takePooledConn(authority); cc != nil {
		resp, err := cc.RoundTrip(toFHTTPRequest(req))
		if err == nil {
			return fromFHTTPResponse(resp, req), nil
		}
		// The pooled conn was stale/broken; drop it and dial fresh below.
		c.dropConn(authority, cc)
	}

	cc, conn, alpn, err := c.getConn(req.Context(), authority, req.URL.Hostname())
	if err != nil {
		return nil, err
	}
	if alpn == "h2" {
		resp, err := cc.RoundTrip(toFHTTPRequest(req))
		if err != nil {
			return nil, err
		}
		return fromFHTTPResponse(resp, req), nil
	}

	// HTTP/1.1: drive the request manually over the uTLS conn. The body owns
	// the conn and closing it releases the connection (single-use, no h1 pool).
	return http1RoundTrip(conn, req)
}

func (c *Client) getConn(ctx context.Context, authority, serverName string) (*fhttp2.ClientConn, net.Conn, string, error) {
	// Fast path outside the dial lock so multiple requests can multiplex on an
	// existing HTTP/2 connection concurrently.
	if cc := c.takePooledConn(authority); cc != nil {
		return cc, nil, "h2", nil
	}

	// Serialize dials to the same authority so concurrent requests share one
	// freshly dialed connection instead of creating redundant TLS handshakes.
	dialMu := c.dialMutex(authority)
	dialMu.Lock()
	defer dialMu.Unlock()

	// Double-check in case another goroutine established the conn while we
	// waited for the dial lock.
	if cc := c.takePooledConn(authority); cc != nil {
		return cc, nil, "h2", nil
	}

	conn, alpn, err := c.dialTLS(ctx, authority, serverName)
	if err != nil {
		return nil, nil, "", err
	}

	if alpn == "h2" {
		cc, err := c.h2.NewClientConn(conn)
		if err != nil {
			_ = conn.Close()
			return nil, nil, "", err
		}
		c.storeConn(authority, cc)
		return cc, nil, "h2", nil
	}

	return nil, conn, alpn, nil
}

func (c *Client) dialTLS(ctx context.Context, authority, serverName string) (*utls.UConn, string, error) {
	rawConn, err := c.dialContext(ctx, "tcp", authority)
	if err != nil {
		return nil, "", err
	}
	uconn := utls.UClient(rawConn, &utls.Config{ServerName: serverName, MinVersion: tls.VersionTLS12}, clientHelloID)
	if err := uconn.HandshakeContext(ctx); err != nil {
		_ = rawConn.Close()
		return nil, "", err
	}
	return uconn, uconn.ConnectionState().NegotiatedProtocol, nil
}

func (c *Client) takePooledConn(authority string) *fhttp2.ClientConn {
	c.mu.Lock()
	defer c.mu.Unlock()
	cc, ok := c.conns[authority]
	if !ok {
		return nil
	}
	if !cc.CanTakeNewRequest() {
		delete(c.conns, authority)
		return nil
	}
	return cc
}

func (c *Client) storeConn(authority string, cc *fhttp2.ClientConn) {
	c.mu.Lock()
	c.conns[authority] = cc
	c.mu.Unlock()
}

func (c *Client) dropConn(authority string, cc *fhttp2.ClientConn) {
	c.mu.Lock()
	if c.conns[authority] == cc {
		delete(c.conns, authority)
	}
	c.mu.Unlock()
	_ = cc.Close()
}

func (c *Client) dialMutex(authority string) *sync.Mutex {
	c.mu.Lock()
	mu, ok := c.dialing[authority]
	if !ok {
		mu = &sync.Mutex{}
		c.dialing[authority] = mu
	}
	c.mu.Unlock()
	return mu
}

// CloseIdleConnections closes all pooled HTTP/2 connections.
func (c *Client) CloseIdleConnections() {
	c.mu.Lock()
	conns := make([]*fhttp2.ClientConn, 0, len(c.conns))
	for _, cc := range c.conns {
		conns = append(conns, cc)
	}
	c.conns = make(map[string]*fhttp2.ClientConn)
	c.mu.Unlock()
	for _, cc := range conns {
		_ = cc.Close()
	}
}

func http1RoundTrip(conn net.Conn, req *http.Request) (*http.Response, error) {
	if err := req.Write(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	resp.Body = &connBody{ReadCloser: resp.Body, conn: conn}
	return resp, nil
}

func authorityAddr(u *url.URL) string {
	host := u.Host
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(host, "443")
	}
	return host
}

// connBody closes the underlying HTTP/1.1 connection when the response body is
// closed, since these connections are single-use.
type connBody struct {
	io.ReadCloser
	conn net.Conn
}

func (b *connBody) Close() error {
	err := b.ReadCloser.Close()
	if cerr := b.conn.Close(); cerr != nil && err == nil {
		err = cerr
	}
	return err
}

// cancelBody cancels the per-request timeout context once the body is fully
// consumed and closed.
type cancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

// NewFallbackClient returns a standard-library HTTP client used when the
// Chrome-impersonating transport cannot be used (e.g. connectivity probes).
// It does not impersonate Chrome and should only be a last resort.
func NewFallbackClient(timeout time.Duration, dialContext DialContextFunc) *http.Client {
	useEnvProxy := dialContext == nil
	if dialContext == nil {
		dialContext = (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	}
	base := &http.Transport{
		ForceAttemptHTTP2:   false,
		MaxIdleConns:        200,
		MaxIdleConnsPerHost: 100,
		IdleConnTimeout:     90 * time.Second,
		DialContext:         dialContext,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
	}
	if useEnvProxy {
		base.Proxy = http.ProxyFromEnvironment
	}
	return &http.Client{Timeout: timeout, Transport: base}
}
