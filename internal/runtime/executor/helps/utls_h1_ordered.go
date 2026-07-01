package helps

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"sort"
	"sync"
	"sync/atomic"
)

// maxIdleUtlsH1ConnsPerHost caps the pooled keep-alive connections per host so a
// concurrency burst cannot leave connections resident for the process lifetime.
const maxIdleUtlsH1ConnsPerHost = 8

// errUnframableBody is returned when a request body has no determinable length
// (no Content-Length and no chunked framing is emitted), which would corrupt a
// keep-alive connection. The executors always use in-memory bodies, so this is a
// defensive guard rather than an expected path.
var errUnframableBody = errors.New("utls h1: request body has unknown length")

// writeError marks a failure that occurred while writing the request, before the
// server could have processed it. Only such failures are safe to retry on a
// fresh connection; a read-phase failure may follow a request the server already
// acted on (non-idempotent POST) and must not be replayed.
type writeError struct{ err error }

func (e *writeError) Error() string { return e.err.Error() }
func (e *writeError) Unwrap() error { return e.err }

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
	// Reject a body whose length cannot be framed: without Content-Length (and we
	// never emit chunked) the server cannot find the message boundary and the
	// bytes would bleed into the next pooled request.
	hasBody := req.Body != nil && req.Body != http.NoBody
	if hasBody && req.ContentLength < 0 {
		return errUnframableBody
	}

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
	if hasBody {
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
	if len(t.idle[host]) >= maxIdleUtlsH1ConnsPerHost {
		t.mu.Unlock()
		_ = pc.conn.Close()
		return
	}
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

	// Try a pooled keep-alive connection first. roundTripOn owns the connection's
	// lifecycle and closes it on every error path, so no leak here.
	if pc := t.getIdle(host); pc != nil {
		resp, err := t.roundTripOn(pc, host, req)
		if err == nil {
			return resp, nil
		}
		// Retry once on a fresh connection ONLY if the failure happened while
		// writing the request (server could not have processed it) and the body
		// can be rewound. A read-phase failure is never replayed — the server may
		// already have acted on a non-idempotent request.
		var we *writeError
		if !errors.As(err, &we) || !rewindBody(req) {
			return nil, err
		}
	}

	pc, err := t.dial(host, addr)
	if err != nil {
		return nil, err
	}
	return t.roundTripOn(pc, host, req)
}

// rewindBody restores req.Body so the request can be re-sent on a fresh
// connection. It returns true when a retry is safe: no body, or a rewindable
// body exposed via req.GetBody (set automatically by net/http for in-memory
// bodies such as bytes.Reader/strings.Reader, which the executors use).
func rewindBody(req *http.Request) bool {
	if req.Body == nil || req.Body == http.NoBody {
		return true
	}
	if req.GetBody == nil {
		return false
	}
	body, err := req.GetBody()
	if err != nil {
		return false
	}
	req.Body = body
	return true
}

// roundTripOn writes req over pc and reads the response. It owns pc: on any
// error it closes pc.conn (so callers never leak it); on success it hands pc to
// a pooledBody that returns it to the idle pool (or closes it) when the response
// body is closed. A watcher tied to req.Context() closes the connection if the
// request is cancelled, unblocking a stuck read/write.
func (t *utlsH1RoundTripper) roundTripOn(pc *utlsH1Conn, host string, req *http.Request) (*http.Response, error) {
	var finishOnce sync.Once
	finished := make(chan struct{})
	finish := func() { finishOnce.Do(func() { close(finished) }) }

	if ctx := req.Context(); ctx != nil && ctx.Done() != nil {
		go func() {
			select {
			case <-ctx.Done():
				_ = pc.conn.Close()
			case <-finished:
			}
		}()
	}

	if err := writeOrderedRequest(pc.bw, req, t.order); err != nil {
		finish()
		_ = pc.conn.Close()
		if errors.Is(err, errUnframableBody) {
			return nil, err // permanent; retrying would fail identically
		}
		return nil, &writeError{err: err} // I/O write failure → safe to retry
	}
	resp, err := http.ReadResponse(pc.br, req)
	if err != nil {
		finish()
		_ = pc.conn.Close()
		return nil, err // read-phase failure → NOT retried (see RoundTrip)
	}
	resp.Body = &pooledBody{
		ReadCloser: resp.Body,
		rt:         t,
		pc:         pc,
		host:       host,
		keepAlive:  !resp.Close && resp.ProtoAtLeast(1, 1),
		finish:     finish,
	}
	return resp, nil
}

// pooledBody returns the connection to the idle pool once the response body is
// fully read and closed (keep-alive), or closes it otherwise. Its flags are
// atomic because io.ReadCloser permits Close to be called from a different
// goroutine than Read (to unblock it).
type pooledBody struct {
	io.ReadCloser
	rt        *utlsH1RoundTripper
	pc        *utlsH1Conn
	host      string
	keepAlive bool
	finish    func()
	fullyRead atomic.Bool
	done      atomic.Bool
}

func (b *pooledBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err == io.EOF {
		b.fullyRead.Store(true)
	}
	return n, err
}

func (b *pooledBody) Close() error {
	err := b.ReadCloser.Close()
	if b.done.Swap(true) {
		return err
	}
	b.finish() // tear down the context watcher
	// Only reuse a connection whose response was fully consumed; a half-read or
	// cancelled stream leaves undefined bytes on the wire, so discard it.
	if b.keepAlive && b.fullyRead.Load() {
		b.rt.putIdle(b.host, b.pc)
	} else {
		_ = b.pc.conn.Close()
	}
	return err
}
