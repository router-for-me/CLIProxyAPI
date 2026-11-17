// Package maintenance coordinates an authenticated update without interrupting work.
package maintenance

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
)

const LeaseDuration = 2 * time.Minute

type contextKey uint8

const (
	connectionKey contextKey = iota
	requestKey
)

// Controller fences admission and tracks requests through net/http's final flush.
// It deliberately does not change the server's ordinary immediate Stop behavior.
type Controller struct {
	mu                  sync.Mutex
	now                 func() time.Time
	instance            string
	owner               string
	expires             time.Time
	stopping            bool
	signal              chan struct{}
	requests            map[*request]struct{}
	sessions            map[*Session]struct{}
	connections         map[net.Conn]*connection
	untrackedCompletion bool
}

// A connection keeps at most one completed HTTP/2 barrier, plus live handlers.
// net/http may report HTTP/2 Idle before the final frame has been flushed.
type connection struct {
	requests map[*request]struct{}
	http2    bool
}

type request struct {
	controller   *Controller
	conn         net.Conn
	returned     bool
	flushed      bool
	hijacked     bool
	socketClosed bool
	http2        bool
}

// Session remains registered until the WebSocket handler and upstream cleanup finish.
type Session struct {
	controller *Controller
	busy       bool
}

// Status contains counts only; lease tokens and request/session contents are never exposed.
type Status struct {
	SchemaVersion        int    `json:"schema_version"`
	InstanceID           string `json:"instance_id"`
	Draining             bool   `json:"draining"`
	Stopping             bool   `json:"stopping"`
	LeaseRemainingMS     int64  `json:"lease_remaining_ms"`
	ActiveRequests       int    `json:"active_requests"`
	WebsocketSessions    int    `json:"websocket_sessions"`
	ActiveWebsocketTurns int    `json:"active_websocket_turns"`
	Ready                bool   `json:"ready"`
}

func New() *Controller {
	return &Controller{now: time.Now, instance: uuid.NewString(), signal: make(chan struct{}),
		requests: make(map[*request]struct{}), sessions: make(map[*Session]struct{}),
		connections: make(map[net.Conn]*connection)}
}

func (d *Controller) expireLocked() {
	if d.owner != "" && !d.stopping && !d.now().Before(d.expires) {
		d.owner = ""
		d.signal = make(chan struct{})
	}
}

func (d *Controller) statusLocked() Status {
	d.expireLocked()
	s := Status{SchemaVersion: 1, InstanceID: d.instance, Draining: d.owner != "", Stopping: d.stopping,
		ActiveRequests: len(d.requests), WebsocketSessions: len(d.sessions)}
	// Completed H2 requests coalesce into a single conservative barrier per
	// connection. Missing connection metadata also needs only one sticky barrier.
	for _, conn := range d.connections {
		if conn.http2 && len(conn.requests) == 0 {
			s.ActiveRequests++
		}
	}
	if d.untrackedCompletion {
		s.ActiveRequests++
	}
	if s.Draining && !s.Stopping {
		s.LeaseRemainingMS = max(0, d.expires.Sub(d.now()).Milliseconds())
	}
	for session := range d.sessions {
		if session.busy {
			s.ActiveWebsocketTurns++
		}
	}
	s.Ready = s.Draining && !s.Stopping && s.ActiveRequests == 0 && s.WebsocketSessions == 0
	return s
}

func (d *Controller) Status() Status {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.statusLocked()
}

// Begin is idempotent for its owner; retries never extend a lease.
func (d *Controller) Begin(instance, owner string) (Status, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.expireLocked()
	if instance != d.instance || len(owner) < 32 || len(owner) > 128 || d.stopping || (d.owner != "" && d.owner != owner) {
		return d.statusLocked(), false
	}
	if d.owner == "" {
		d.owner = owner
		d.expires = d.now().Add(LeaseDuration)
		close(d.signal)
	}
	return d.statusLocked(), true
}

func (d *Controller) Cancel(instance, owner string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.expireLocked()
	if instance != d.instance || owner == "" || owner != d.owner || d.stopping {
		return false
	}
	d.owner = ""
	d.signal = make(chan struct{})
	return true
}

// CommitStop checks readiness and permanently fences admission in the same lock.
// The caller must stop THIS server after success. An external readiness check
// followed by killing a PID is unsafe: its lease could expire in between.
func (d *Controller) CommitStop(instance, owner string, canStop func() bool) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.statusLocked()
	if instance != d.instance || owner == "" || owner != d.owner || !s.Ready {
		return false
	}
	// Read detached-work blockers only AFTER admitted handlers are known to
	// have finished, while the admission lock prevents a new handoff or turn.
	if canStop != nil && !canStop() {
		return false
	}
	d.stopping = true
	return true
}

func (d *Controller) ConnContext(ctx context.Context, conn net.Conn) context.Context {
	return context.WithValue(ctx, connectionKey, conn)
}

// Idle follows net/http's response flush. Closed also proves no bytes can remain
// in the server's user-space buffer. HTTP/2 can report Idle before its final
// frame is flushed, so it must close naturally. Hijacked sockets wait for Close.
func (d *Controller) ConnState(conn net.Conn, state http.ConnState) {
	if state != http.StateIdle && state != http.StateClosed && state != http.StateHijacked {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	tracked := d.connections[conn]
	if tracked == nil {
		return
	}
	for ticket := range tracked.requests {
		if ticket.http2 && state == http.StateIdle {
			continue
		}
		ticket.flushed = true
		if state == http.StateClosed {
			ticket.socketClosed = true
		}
		if ticket.returned && (!ticket.hijacked || ticket.socketClosed) {
			d.dropLocked(ticket)
		}
	}
	if state == http.StateClosed {
		delete(d.connections, conn)
	}
}

func (d *Controller) dropLocked(ticket *request) {
	delete(d.requests, ticket)
	if conn := d.connections[ticket.conn]; conn != nil {
		delete(conn.requests, ticket)
	}
}

func (d *Controller) admit(r *http.Request) (*request, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.expireLocked()
	if d.owner != "" {
		return nil, false
	}
	conn, _ := r.Context().Value(connectionKey).(net.Conn)
	ticket := &request{controller: d, conn: conn, http2: r.ProtoMajor >= 2}
	d.requests[ticket] = struct{}{}
	if conn != nil {
		tracked := d.connections[conn]
		if tracked == nil {
			tracked = &connection{requests: make(map[*request]struct{})}
			d.connections[conn] = tracked
		}
		tracked.http2 = tracked.http2 || ticket.http2
		tracked.requests[ticket] = struct{}{}
	}
	return ticket, true
}

func (t *request) finish() {
	d := t.controller
	d.mu.Lock()
	defer d.mu.Unlock()
	t.returned = true
	if _, counted := d.requests[t]; !counted {
		return
	}
	if t.hijacked {
		if t.socketClosed {
			d.dropLocked(t)
		}
		return
	}
	if t.http2 && t.conn != nil {
		// The connection, not every historical request, owns the completion barrier.
		d.dropLocked(t)
	} else if t.flushed {
		d.dropLocked(t)
	} else if t.conn == nil {
		d.untrackedCompletion = true
		d.dropLocked(t)
	}
}

// Wrap covers custom routes as well as inference aliases. Only the caller's
// explicit health/management exceptions bypass the admission fence.
func (d *Controller) Wrap(next http.Handler, exempt func(*http.Request) bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if exempt != nil && exempt(r) {
			next.ServeHTTP(w, r)
			return
		}
		ticket, ok := d.admit(r)
		if !ok {
			w.Header().Set("Retry-After", "2")
			w.Header().Set("Connection", "close")
			http.Error(w, "proxy updating; retry shortly", http.StatusServiceUnavailable)
			return
		}
		defer ticket.finish()
		next.ServeHTTP(&requestWriter{ResponseWriter: w, ticket: ticket}, r.WithContext(context.WithValue(r.Context(), requestKey, ticket)))
	})
}

// ManageWebsocket atomically converts its upgrade request to a managed session.
// Unknown WebSockets stay counted until both handler exit and socket Close.
func ManageWebsocket(ctx context.Context) *Session {
	ticket, _ := ctx.Value(requestKey).(*request)
	if ticket == nil {
		return nil
	}
	d := ticket.controller
	d.mu.Lock()
	defer d.mu.Unlock()
	session := &Session{controller: d, busy: true}
	d.sessions[session] = struct{}{}
	d.dropLocked(ticket)
	return session
}

// Waiting marks a safe turn boundary. Pending tool calls must pass true.
// The channel closes when maintenance begins. Pending tool work has no idle
// close signal, so its already admitted continuation can finish first.
func (s *Session) Waiting(pendingTools bool) <-chan struct{} {
	if s == nil {
		return nil
	}
	d := s.controller
	d.mu.Lock()
	defer d.mu.Unlock()
	d.expireLocked()
	s.busy = pendingTools
	if pendingTools {
		return nil
	}
	return d.signal
}

// BeginTurn races atomically with Begin. A pending tool continuation is already
// part of admitted work and is allowed to finish even during a drain.
func (s *Session) BeginTurn() bool {
	if s == nil {
		return true
	}
	d := s.controller
	d.mu.Lock()
	defer d.mu.Unlock()
	d.expireLocked()
	if d.owner != "" && !s.busy {
		return false
	}
	s.busy = true
	return true
}

func (s *Session) Close() {
	if s == nil {
		return
	}
	d := s.controller
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.sessions, s)
}

// Hijacked handlers may return while a background goroutine owns the socket,
// as the AI Studio relay does. Track its actual Close rather than handler return.
type requestWriter struct {
	http.ResponseWriter
	ticket *request
}

func (w *requestWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *requestWriter) Flush()                      { _ = http.NewResponseController(w.ResponseWriter).Flush() }
func (w *requestWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err != nil {
		return nil, nil, err
	}
	d := w.ticket.controller
	d.mu.Lock()
	w.ticket.hijacked = true
	d.mu.Unlock()
	return &hijackedConn{Conn: conn, ticket: w.ticket}, rw, nil
}
func (w *requestWriter) CloseNotify() <-chan bool {
	if notifier, ok := w.ResponseWriter.(http.CloseNotifier); ok {
		return notifier.CloseNotify()
	}
	return nil
}
func (w *requestWriter) Push(target string, options *http.PushOptions) error {
	if pusher, ok := w.ResponseWriter.(http.Pusher); ok {
		return pusher.Push(target, options)
	}
	return http.ErrNotSupported
}

type hijackedConn struct {
	net.Conn
	ticket *request
	closed sync.Once
}

func (c *hijackedConn) Close() error {
	err := c.Conn.Close()
	c.markClosed()
	return err
}

// NetConn lets callers that deliberately skip TLS close_notify, such as the
// WebSocket tunnel, close the raw transport. The returned conn has no NetConn
// of its own, so closing it still records the socket's close for the drain.
func (c *hijackedConn) NetConn() net.Conn {
	return &transportConn{Conn: rawConn(c.Conn), owner: c}
}

func (c *hijackedConn) markClosed() {
	c.closed.Do(func() {
		d := c.ticket.controller
		// Hijacked sockets do not get a later net/http Closed callback.
		d.ConnState(c.ticket.conn, http.StateClosed)
		d.mu.Lock()
		c.ticket.socketClosed = true
		c.ticket.flushed = true
		if c.ticket.returned {
			d.dropLocked(c.ticket)
		}
		d.mu.Unlock()
	})
}

type transportConn struct {
	net.Conn
	owner *hijackedConn
}

func (c *transportConn) Close() error {
	err := c.Conn.Close()
	c.owner.markClosed()
	return err
}

// rawConn follows NetConn wrappers, such as TLS, down to the transport.
func rawConn(conn net.Conn) net.Conn {
	for range 16 {
		wrapper, ok := conn.(interface{ NetConn() net.Conn })
		if !ok {
			return conn
		}
		inner := wrapper.NetConn()
		if inner == nil || inner == conn {
			return conn
		}
		conn = inner
	}
	return conn
}
