package helps

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"sort"
	"strings"
	"sync"
)

// claudeHeaderOrder is the HTTP/1.1 request header order emitted by Claude Code
// (the Anthropic TypeScript SDK on Node.js/undici). undici writes h1 headers in
// insertion order, and the SDK's buildHeaders composes them in exactly this
// sequence: the fixed block (Accept, User-Agent, X-Stainless-Retry-Count,
// X-Stainless-Timeout, platform headers, anthropic-version), then auth headers,
// then default headers (anthropic-beta, x-app, …), then body headers
// (content-type/length), then per-request headers.
//
// Go's net/http instead sorts request headers alphabetically, which is itself a
// recognizable "Go net/http" fingerprint. Emitting them in this order removes
// that tell on the Anthropic (Node/HTTP-1.1) path. Header names not listed here
// are appended afterwards in a stable order. Values are taken from whatever the
// executor actually set; only ordering is imposed here.
var claudeHeaderOrder = []string{
	"Accept",
	"User-Agent",
	"X-Stainless-Retry-Count",
	"X-Stainless-Timeout",
	"X-Stainless-Lang",
	"X-Stainless-Package-Version",
	"X-Stainless-Os",
	"X-Stainless-Arch",
	"X-Stainless-Runtime",
	"X-Stainless-Runtime-Version",
	"X-Stainless-Helper-Method",
	"Anthropic-Dangerous-Direct-Browser-Access",
	"Anthropic-Version",
	"Authorization",
	"X-Api-Key",
	"Anthropic-Beta",
	"X-App",
	"X-Claude-Code-Session-Id",
	"Content-Type",
	"Content-Length",
	"X-Client-Request-Id",
	"Accept-Encoding",
	"Connection",
}

// writeOrderedRequest serializes an HTTP/1.1 request to w with header names
// emitted in the given priority order first (only those actually present),
// followed by any remaining headers in a stable alphabetical order. Host is
// always written first and Content-Length is synthesized from req.ContentLength.
// This is a pure function (no network) so the ordering is unit-testable.
func writeOrderedRequest(w *bufio.Writer, req *http.Request, order []string) error {
	if _, err := fmt.Fprintf(w, "%s %s HTTP/1.1\r\n", req.Method, req.URL.RequestURI()); err != nil {
		return err
	}
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	if _, err := fmt.Fprintf(w, "Host: %s\r\n", host); err != nil {
		return err
	}

	written := make(map[string]bool, len(req.Header)+2)
	writeHeader := func(key string, values []string) error {
		for _, v := range values {
			if _, err := fmt.Fprintf(w, "%s: %s\r\n", key, v); err != nil {
				return err
			}
		}
		return nil
	}

	for _, name := range order {
		canonical := textproto.CanonicalMIMEHeaderKey(name)
		if written[canonical] {
			continue
		}
		if canonical == "Content-Length" {
			if req.ContentLength > 0 {
				if err := writeHeader("Content-Length", []string{fmt.Sprintf("%d", req.ContentLength)}); err != nil {
					return err
				}
				written[canonical] = true
			}
			continue
		}
		if values, ok := req.Header[canonical]; ok && len(values) > 0 {
			if err := writeHeader(canonical, values); err != nil {
				return err
			}
			written[canonical] = true
		}
	}

	// Remaining headers not covered by the priority list, stable-sorted so the
	// output is deterministic.
	remaining := make([]string, 0, len(req.Header))
	for name := range req.Header {
		if !written[name] {
			remaining = append(remaining, name)
		}
	}
	sort.Strings(remaining)
	for _, name := range remaining {
		if err := writeHeader(name, req.Header[name]); err != nil {
			return err
		}
	}

	if _, err := w.WriteString("\r\n"); err != nil {
		return err
	}
	if req.Body != nil {
		if _, err := io.Copy(w, req.Body); err != nil {
			return err
		}
		_ = req.Body.Close()
	}
	return w.Flush()
}

// utlsH1Conn is a pooled keep-alive HTTP/1.1 connection over uTLS.
type utlsH1Conn struct {
	conn net.Conn
	br   *bufio.Reader
	bw   *bufio.Writer
}

// utlsH1RoundTripper is an http.RoundTripper for HTTP/1.1 uTLS profiles that
// writes request headers in a fixed (undici-matching) order instead of Go's
// alphabetical sort, while keeping connections alive per host.
type utlsH1RoundTripper struct {
	dialer  proxyDialer
	profile tlsProfile
	order   []string

	mu   sync.Mutex
	idle map[string][]*utlsH1Conn
}

// proxyDialer is the minimal dial surface used here (satisfied by proxy.Dialer).
type proxyDialer interface {
	Dial(network, addr string) (net.Conn, error)
}

func newUtlsH1RoundTripper(dialer proxyDialer, p tlsProfile, order []string) *utlsH1RoundTripper {
	return &utlsH1RoundTripper{
		dialer:  dialer,
		profile: p,
		order:   order,
		idle:    make(map[string][]*utlsH1Conn),
	}
}

func (t *utlsH1RoundTripper) getIdle(host string) *utlsH1Conn {
	t.mu.Lock()
	defer t.mu.Unlock()
	conns := t.idle[host]
	if len(conns) == 0 {
		return nil
	}
	pc := conns[len(conns)-1]
	t.idle[host] = conns[:len(conns)-1]
	return pc
}

func (t *utlsH1RoundTripper) putIdle(host string, pc *utlsH1Conn) {
	t.mu.Lock()
	t.idle[host] = append(t.idle[host], pc)
	t.mu.Unlock()
}

func (t *utlsH1RoundTripper) dial(host, addr string) (*utlsH1Conn, error) {
	raw, err := t.dialer.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	tlsConn, _, err := utlsHandshake(raw, host, t.profile)
	if err != nil {
		_ = raw.Close()
		return nil, err
	}
	return &utlsH1Conn{
		conn: tlsConn,
		br:   bufio.NewReader(tlsConn),
		bw:   bufio.NewWriter(tlsConn),
	}, nil
}

func (t *utlsH1RoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Hostname()
	port := req.URL.Port()
	if port == "" {
		port = "443"
	}
	addr := net.JoinHostPort(host, port)

	// Try a pooled connection first; a stale keep-alive conn (server-closed) is
	// transparently retried once on a fresh connection.
	if pc := t.getIdle(host); pc != nil {
		if resp, err := t.roundTripOn(pc, host, req); err == nil {
			return resp, nil
		}
		_ = pc.conn.Close()
	}

	pc, err := t.dial(host, addr)
	if err != nil {
		return nil, err
	}
	return t.roundTripOn(pc, host, req)
}

func (t *utlsH1RoundTripper) roundTripOn(pc *utlsH1Conn, host string, req *http.Request) (*http.Response, error) {
	if err := writeOrderedRequest(pc.bw, req, t.order); err != nil {
		return nil, err
	}
	resp, err := http.ReadResponse(pc.br, req)
	if err != nil {
		return nil, err
	}
	keepAlive := !resp.Close && resp.ProtoAtLeast(1, 1)
	resp.Body = &pooledBody{
		ReadCloser: resp.Body,
		rt:         t,
		pc:         pc,
		host:       host,
		keepAlive:  keepAlive,
	}
	return resp, nil
}

// pooledBody returns the connection to the idle pool once the response body is
// fully read and closed (keep-alive), or closes it otherwise.
type pooledBody struct {
	io.ReadCloser
	rt        *utlsH1RoundTripper
	pc        *utlsH1Conn
	host      string
	keepAlive bool
	fullyRead bool
	done      bool
}

func (b *pooledBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err == io.EOF {
		b.fullyRead = true
	}
	return n, err
}

func (b *pooledBody) Close() error {
	err := b.ReadCloser.Close()
	if b.done {
		return err
	}
	b.done = true
	if b.keepAlive && b.fullyRead {
		b.rt.putIdle(b.host, b.pc)
	} else {
		_ = b.pc.conn.Close()
	}
	return err
}

// canonicalHeaderOrder returns order with each name canonicalized, for callers
// that want to validate/compose lists.
func canonicalHeaderOrder(order []string) []string {
	out := make([]string, len(order))
	for i, name := range order {
		out[i] = textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(name))
	}
	return out
}
