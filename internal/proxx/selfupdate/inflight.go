package selfupdate

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
)

// tracker counts in-flight work: every proxied HTTP request (including SSE streams) from handler entry
// to return, plus every hijacked connection (WebSockets) until it is closed. http.Server does not track
// hijacked connections, so they are counted through the ResponseWriter's Hijack.
//
// Management API requests (/v8/management, /v0/management, the panel) are not counted: the panel polls
// and keeps live-tail streams open, which would otherwise block an update forever. Their contexts are
// cancelled right before the restart instead (cancelManagement), which ends those streams cleanly.
type tracker struct {
	active   atomic.Int64
	draining atomic.Bool

	mu      sync.Mutex
	nextID  uint64
	cancels map[uint64]context.CancelFunc
	// changed is closed and replaced whenever active drops, so waiters can block without polling.
	changed chan struct{}
}

func newTracker() *tracker {
	return &tracker{cancels: map[uint64]context.CancelFunc{}, changed: make(chan struct{})}
}

var inflight = newTracker()

// Track wraps the API server's root handler (hook in internal/api/server.go).
func Track(next http.Handler) http.Handler { return inflight.wrap(next) }

// InFlight returns the number of in-flight requests and hijacked connections.
func InFlight() int64 { return inflight.active.Load() }

func isManagementPath(p string) bool {
	return strings.HasPrefix(p, "/v8/management") || strings.HasPrefix(p, "/v0/management") ||
		strings.HasPrefix(p, "/management.html")
}

func (t *tracker) wrap(next http.Handler) http.Handler {
	if next == nil {
		return nil
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if t.draining.Load() {
			// Last moments before a restart: do not keep this connection alive.
			w.Header().Set("Connection", "close")
		}
		tw := &trackedWriter{ResponseWriter: w, t: t}
		if r != nil && r.URL != nil && isManagementPath(r.URL.Path) {
			ctx, cancel := context.WithCancel(r.Context())
			id := t.register(cancel)
			defer t.unregister(id)
			next.ServeHTTP(tw, r.WithContext(ctx))
			return
		}
		t.inc()
		defer t.dec()
		next.ServeHTTP(tw, r)
	})
}

func (t *tracker) inc() { t.active.Add(1) }

func (t *tracker) dec() {
	if t.active.Add(-1) <= 0 {
		t.mu.Lock()
		close(t.changed)
		t.changed = make(chan struct{})
		t.mu.Unlock()
	}
}

// idleSignal returns a channel that is closed the next time the counter drops to zero.
func (t *tracker) idleSignal() <-chan struct{} {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.changed
}

func (t *tracker) register(cancel context.CancelFunc) uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.nextID++
	t.cancels[t.nextID] = cancel
	return t.nextID
}

func (t *tracker) unregister(id uint64) {
	t.mu.Lock()
	cancel := t.cancels[id]
	delete(t.cancels, id)
	t.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// cancelManagement cancels the contexts of all running management requests (live-tail streams).
func (t *tracker) cancelManagement() {
	t.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(t.cancels))
	for _, c := range t.cancels {
		cancels = append(cancels, c)
	}
	t.mu.Unlock()
	for _, c := range cancels {
		c()
	}
}

// trackedWriter forwards the optional ResponseWriter interfaces and counts hijacked connections.
type trackedWriter struct {
	http.ResponseWriter
	t *tracker
}

func (w *trackedWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *trackedWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *trackedWriter) CloseNotify() <-chan bool {
	if n, ok := w.ResponseWriter.(http.CloseNotifier); ok { //nolint:staticcheck // gin still calls it
		return n.CloseNotify()
	}
	return make(chan bool)
}

func (w *trackedWriter) Push(target string, opts *http.PushOptions) error {
	if p, ok := w.ResponseWriter.(http.Pusher); ok {
		return p.Push(target, opts)
	}
	return http.ErrNotSupported
}

func (w *trackedWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err != nil {
		return conn, rw, err
	}
	w.t.inc()
	return &trackedConn{Conn: conn, t: w.t}, rw, nil
}

// trackedConn decrements the counter once when the hijacked connection is closed.
type trackedConn struct {
	net.Conn
	t    *tracker
	once sync.Once
}

func (c *trackedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.t.dec)
	return err
}

// NetConn exposes the raw transport for code that reclaims a socket without calling the wrappers'
// Close (wstunnel does this when a TLS close_notify stalls). The returned conn closes the raw transport
// and still decrements the counter.
func (c *trackedConn) NetConn() net.Conn {
	return &trackedRawConn{Conn: rawConn(c.Conn), owner: c}
}

type trackedRawConn struct {
	net.Conn
	owner *trackedConn
}

func (r *trackedRawConn) Close() error {
	err := r.Conn.Close()
	r.owner.once.Do(r.owner.t.dec)
	return err
}

// rawConn follows NetConn() wrappers (tls.Conn, buffered conns) down to the transport.
func rawConn(conn net.Conn) net.Conn {
	for i := 0; i < 16; i++ {
		w, ok := conn.(interface{ NetConn() net.Conn })
		if !ok {
			return conn
		}
		next := w.NetConn()
		if next == nil || next == conn {
			return conn
		}
		conn = next
	}
	return conn
}
