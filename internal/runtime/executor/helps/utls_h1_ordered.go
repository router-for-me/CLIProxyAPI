package helps

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
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

// Header emission model, verified by LIVE-CAPTURING the real claude-cli 2.1.153
// on this machine (raw HTTP/1.1 to a local server, byte-exact):
//
//   1. Application headers are emitted in a CASE-SENSITIVE (raw ASCII) sort of
//      their wire name. Because ASCII uppercase (A–Z) sorts before lowercase
//      (a–z), the observed order is: Accept, Authorization, Content-Type,
//      User-Agent, X-Claude-Code-Session-Id, X-Stainless-* (sorted), then the
//      lowercase custom headers anthropic-beta, anthropic-dangerous-…,
//      anthropic-version, x-app. This is NOT the SDK buildHeaders insertion order
//      and NOT Go's canonical-key alphabetical order.
//   2. Then undici appends the transport headers in a fixed trailer:
//      Connection, Host, Accept-Encoding, Content-Length.
//   3. Casing on the wire is Go-canonical for everything EXCEPT anthropic-*,
//      x-app and x-api-key (lowercase), and X-Stainless-OS (OS uppercased).
//
// Sorting (rather than a fixed list) is robust to whichever headers are present
// and reproduces the client exactly.

// headerWireCasing maps a Go-canonical header key to the exact casing the real
// client puts on the wire. Only deviations from canonical are listed.
var headerWireCasing = map[string]string{
	"Anthropic-Beta":                            "anthropic-beta",
	"Anthropic-Version":                         "anthropic-version",
	"Anthropic-Dangerous-Direct-Browser-Access": "anthropic-dangerous-direct-browser-access",
	"X-App":          "x-app",
	"X-Api-Key":      "x-api-key",
	"X-Stainless-Os": "X-Stainless-OS",
}

// transportHeaderTrailer is undici's fixed trailing header order, appended after
// the sorted application headers.
var transportHeaderTrailer = []string{"Connection", "Host", "Accept-Encoding", "Content-Length"}

// transportHeaderSet is the trailer names (Go-canonical) excluded from the sorted
// application-header block.
var transportHeaderSet = map[string]bool{"Connection": true, "Host": true, "Accept-Encoding": true, "Content-Length": true}

// wireHeaderName returns the on-the-wire casing for a Go-canonical header key.
func wireHeaderName(canonical string) string {
	if w, ok := headerWireCasing[canonical]; ok {
		return w
	}
	return canonical
}

// writeOrderedRequest serializes an HTTP/1.1 request to w reproducing the real
// Claude Code (undici) wire order: application headers case-sensitively sorted by
// their wire-casing name, then the fixed transport trailer (Connection, Host,
// Accept-Encoding, Content-Length). Host and Content-Length are synthesized. This
// is a pure function (no network) so the ordering is unit-testable.
func writeOrderedRequest(w *bufio.Writer, req *http.Request) error {
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
	writeHeader := func(name string, values []string) error {
		for _, v := range values {
			if _, err := fmt.Fprintf(w, "%s: %s\r\n", name, v); err != nil {
				return err
			}
		}
		return nil
	}

	// Application headers (everything except the transport trailer), emitted in a
	// case-sensitive raw-ASCII sort of their wire casing — matching the real client.
	type appHeader struct{ wire, canonical string }
	apps := make([]appHeader, 0, len(req.Header))
	for canonical := range req.Header {
		if transportHeaderSet[canonical] {
			continue
		}
		apps = append(apps, appHeader{wire: wireHeaderName(canonical), canonical: canonical})
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].wire < apps[j].wire })
	for _, h := range apps {
		if err := writeHeader(h.wire, req.Header[h.canonical]); err != nil {
			return err
		}
	}

	// Transport trailer in undici's fixed order.
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	for _, name := range transportHeaderTrailer {
		switch name {
		case "Host":
			if err := writeHeader("Host", []string{host}); err != nil {
				return err
			}
		case "Content-Length":
			if req.ContentLength > 0 {
				if err := writeHeader("Content-Length", []string{fmt.Sprintf("%d", req.ContentLength)}); err != nil {
					return err
				}
			}
		default:
			if values, ok := req.Header[name]; ok && len(values) > 0 {
				if err := writeHeader(name, values); err != nil {
					return err
				}
			}
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
// writes request headers in the real Claude Code (undici) wire order instead of
// Go's alphabetical sort, while keeping connections alive per host.
type utlsH1RoundTripper struct {
	dialer  proxyDialer
	profile tlsProfile

	mu   sync.Mutex
	idle map[string][]*utlsH1Conn
}

// proxyDialer is the minimal dial surface used here (satisfied by proxy.Dialer).
type proxyDialer interface {
	Dial(network, addr string) (net.Conn, error)
}

func newUtlsH1RoundTripper(dialer proxyDialer, p tlsProfile) *utlsH1RoundTripper {
	return &utlsH1RoundTripper{
		dialer:  dialer,
		profile: p,
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

	if err := writeOrderedRequest(pc.bw, req); err != nil {
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
