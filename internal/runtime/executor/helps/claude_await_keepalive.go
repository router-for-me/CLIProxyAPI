package helps

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// claudeAwaitKeepAliveLine is what the keepalive writes. HTTP/1.1 servers
// ignore empty lines received before a request line (RFC 9112 section 2.2),
// so an upstream treats it as leading whitespace of a future request while
// it is still producing the current response.
const claudeAwaitKeepAliveLine = "\r\n"

// errClaudeAwaitKeepAliveConnReused rejects a new request on a connection
// that already carried keepalive lines. Nothing is written, so net/http
// reports it as a nothing-written error and replays the request on a fresh
// connection; the stray lines never precede another request line.
var errClaudeAwaitKeepAliveConnReused = errors.New("claude await keepalive: connection carried keepalive lines and is not reused")

// claudeAwaitKeepAliveContextKey carries the opt-in keepalive interval
// through request contexts. The Claude Code round tripper is cached per
// proxy URL, so the interval is read per dial instead of living on the
// transport, which also lets config hot reloads apply to new connections.
type claudeAwaitKeepAliveContextKey struct{}

func withClaudeAwaitKeepAlive(ctx context.Context, interval time.Duration) context.Context {
	if ctx == nil || interval <= 0 {
		return ctx
	}
	return context.WithValue(ctx, claudeAwaitKeepAliveContextKey{}, interval)
}

func claudeAwaitKeepAliveFromContext(ctx context.Context) time.Duration {
	if ctx == nil {
		return 0
	}
	interval, _ := ctx.Value(claudeAwaitKeepAliveContextKey{}).(time.Duration)
	if interval <= 0 {
		return 0
	}
	return interval
}

// claudeAwaitKeepAliveRoundTripper stamps the configured keepalive interval
// onto each outgoing request context. It only annotates the context.
type claudeAwaitKeepAliveRoundTripper struct {
	base     http.RoundTripper
	interval time.Duration
}

func (rt *claudeAwaitKeepAliveRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if rt.interval > 0 {
		req = req.WithContext(withClaudeAwaitKeepAlive(req.Context(), rt.interval))
	}
	return rt.base.RoundTrip(req)
}

// awaitKeepAliveConn sits between the TLS connection and the HTTP/1.1
// writer. After request bytes are written and until the first response byte
// is read, it writes a blank line every interval without other writes, so
// proxies that drop tunnels without payload traffic keep the connection
// open while the upstream prepares a slow first byte. It never closes the
// connection and never sets deadlines.
type awaitKeepAliveConn struct {
	net.Conn
	interval time.Duration

	mu        sync.Mutex
	timer     *time.Timer
	lastWrite time.Time
	awaiting  bool // request bytes written, no response byte read since
	sent      bool // keepalive lines were written on this connection
	stopped   bool // closed, or a keepalive write failed
}

// newAwaitKeepAliveWire wraps conn for an HTTP/1.1 writer whose request
// framing layer is built by wrapRequest. The keepalive sits below that layer,
// because a header reorderer would otherwise buffer blank lines as the start
// of a new request. The reuse guard sits above it, so a rejected request
// consumes no bytes even when the framing layer buffers header writes.
func newAwaitKeepAliveWire(conn net.Conn, interval time.Duration, wrapRequest func(net.Conn) net.Conn) net.Conn {
	if conn == nil || interval <= 0 {
		return wrapRequest(conn)
	}
	keepAlive := &awaitKeepAliveConn{Conn: conn, interval: interval}
	return &awaitKeepAliveReuseGuard{Conn: wrapRequest(keepAlive), keepAlive: keepAlive}
}

// awaitKeepAliveReuseGuard rejects the first write of a new request on a
// connection that already carried keepalive lines.
type awaitKeepAliveReuseGuard struct {
	net.Conn
	keepAlive *awaitKeepAliveConn
}

func (g *awaitKeepAliveReuseGuard) Write(p []byte) (int, error) {
	if g.keepAlive.carriedKeepAlive() {
		return 0, errClaudeAwaitKeepAliveConnReused
	}
	return g.Conn.Write(p)
}

// carriedKeepAlive reports whether keepalive lines were written and the
// response has since started, so any further write begins a new request.
func (c *awaitKeepAliveConn) carriedKeepAlive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sent && !c.awaiting
}

func (c *awaitKeepAliveConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n, errWrite := c.Conn.Write(p)
	c.lastWrite = time.Now()
	c.awaiting = true
	c.scheduleLocked(c.interval)
	return n, errWrite
}

func (c *awaitKeepAliveConn) Read(p []byte) (int, error) {
	n, errRead := c.Conn.Read(p)
	if n > 0 {
		c.mu.Lock()
		if c.awaiting {
			c.awaiting = false
			if c.timer != nil {
				c.timer.Stop()
			}
		}
		c.mu.Unlock()
	}
	return n, errRead
}

func (c *awaitKeepAliveConn) Close() error {
	c.mu.Lock()
	c.stopped = true
	if c.timer != nil {
		c.timer.Stop()
	}
	c.mu.Unlock()
	return c.Conn.Close()
}

func (c *awaitKeepAliveConn) scheduleLocked(delay time.Duration) {
	if c.stopped {
		return
	}
	if c.timer == nil {
		c.timer = time.AfterFunc(delay, c.fire)
		return
	}
	c.timer.Reset(delay)
}

func (c *awaitKeepAliveConn) fire() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped || !c.awaiting {
		return
	}
	// The timer may fire while a slow request write held the lock; only an
	// interval without writes means the request is fully sent.
	if idle := time.Since(c.lastWrite); idle < c.interval {
		c.scheduleLocked(c.interval - idle)
		return
	}
	if _, errWrite := c.Conn.Write([]byte(claudeAwaitKeepAliveLine)); errWrite != nil {
		// The read side surfaces the broken connection to net/http.
		log.Debugf("claude await keepalive: write failed: %v", errWrite)
		c.stopped = true
		return
	}
	c.sent = true
	c.lastWrite = time.Now()
	log.Debug("claude await keepalive: sent blank line while awaiting response")
	c.scheduleLocked(c.interval)
}
