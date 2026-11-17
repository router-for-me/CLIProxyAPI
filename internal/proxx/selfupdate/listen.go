package selfupdate

import (
	"errors"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// listenFDEnv carries the inherited listening socket across the in-place exec.
const listenFDEnv = "PROXX_LISTEN_FD"

// gateListener wraps the API server's base listener. While paused, it accepts no new connections, so
// they wait in the kernel backlog (they are not refused) until the restarted process accepts them on the
// same socket. Pausing also wakes an Accept that is already blocked (by setting a past deadline on the
// socket), so not even one more connection is taken; the timeout never reaches the caller.
//
// It also remembers connections that were accepted but have not delivered a request to a handler yet
// ("fresh"). Before the restart the updater waits until none are left, so a connection accepted just
// before the pause is served instead of dropped.
type gateListener struct {
	net.Listener
	mu     sync.Mutex
	paused bool
	closed bool
	resume chan struct{}
	fresh  map[string]freshConn
	now    func() time.Time
}

type freshConn struct {
	conn       *gateConn
	acceptedAt time.Time
}

type deadliner interface{ SetDeadline(time.Time) error }

func newGateListener(inner net.Listener) *gateListener {
	return &gateListener{Listener: inner, resume: make(chan struct{}), fresh: map[string]freshConn{}, now: time.Now}
}

func (l *gateListener) setDeadline(t time.Time) {
	if d, ok := l.Listener.(deadliner); ok {
		_ = d.SetDeadline(t)
	}
}

func (l *gateListener) Accept() (net.Conn, error) {
	for {
		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			return nil, net.ErrClosed
		}
		if l.paused {
			ch := l.resume
			l.mu.Unlock()
			<-ch
			continue
		}
		l.mu.Unlock()
		conn, err := l.Listener.Accept()
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				// Woken by setPaused(true); never surface it. If the pause already ended, make sure the
				// deadline is cleared before accepting again.
				l.mu.Lock()
				paused, closed := l.paused, l.closed
				l.mu.Unlock()
				if !paused && !closed {
					l.setDeadline(time.Time{})
				}
				continue
			}
			return conn, err
		}
		return l.track(conn), nil
	}
}

func (l *gateListener) track(conn net.Conn) net.Conn {
	gc := &gateConn{Conn: conn, l: l, key: conn.RemoteAddr().String()}
	l.mu.Lock()
	l.fresh[gc.key] = freshConn{conn: gc, acceptedAt: l.now()}
	l.mu.Unlock()
	return gc
}

// served marks the connection a request arrived on as no longer fresh.
func (l *gateListener) served(remoteAddr string) {
	l.mu.Lock()
	delete(l.fresh, remoteAddr)
	l.mu.Unlock()
}

func (l *gateListener) forget(gc *gateConn) {
	l.mu.Lock()
	if f, ok := l.fresh[gc.key]; ok && f.conn == gc {
		delete(l.fresh, gc.key)
	}
	l.mu.Unlock()
}

// freshCount counts accepted connections without a request yet, ignoring ones silent for longer than
// maxAge (a client that connected and never sent anything; the shutdown closes those like idle ones).
func (l *gateListener) freshCount(maxAge time.Duration) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := l.now().Add(-maxAge)
	n := 0
	for _, f := range l.fresh {
		if f.acceptedAt.After(cutoff) {
			n++
		}
	}
	return n
}

func (l *gateListener) setPaused(p bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.paused == p {
		return
	}
	l.paused = p
	if p {
		l.setDeadline(time.Unix(1, 0)) // wake a blocked Accept
		return
	}
	l.setDeadline(time.Time{})
	close(l.resume)
	l.resume = make(chan struct{})
}

func (l *gateListener) Close() error {
	l.mu.Lock()
	if !l.closed {
		l.closed = true
		close(l.resume)
		l.resume = make(chan struct{})
	}
	l.mu.Unlock()
	return l.Listener.Close()
}

// gateConn removes itself from the fresh set when closed.
type gateConn struct {
	net.Conn
	l    *gateListener
	key  string
	once sync.Once
}

func (c *gateConn) Close() error {
	c.once.Do(func() { c.l.forget(c) })
	return c.Conn.Close()
}

// NetConn exposes the transport (wstunnel reclaims raw sockets through it).
func (c *gateConn) NetConn() net.Conn { return c.Conn }

// noteRequest is called by the in-flight tracker for every request.
func noteRequest(remoteAddr string) {
	if l := currentListener(); l != nil {
		l.served(remoteAddr)
	}
}

var current struct {
	sync.Mutex
	ln *gateListener
}

func currentListener() *gateListener {
	current.Lock()
	defer current.Unlock()
	return current.ln
}

// Listen replaces net.Listen("tcp", addr) for the API server (hook in internal/api/server.go). After an
// in-place update it adopts the socket inherited through PROXX_LISTEN_FD instead of binding again.
func Listen(addr string) (net.Listener, error) {
	var inner net.Listener
	if fdStr, ok := os.LookupEnv(listenFDEnv); ok {
		_ = os.Unsetenv(listenFDEnv)
		inner = inheritListener(fdStr, addr)
	}
	if inner == nil {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return nil, err
		}
		inner = ln
	}
	g := newGateListener(inner)
	current.Lock()
	current.ln = g
	current.Unlock()
	startHealthProbe(inner.Addr())
	return g, nil
}

func inheritListener(fdStr, addr string) net.Listener {
	fd, err := strconv.Atoi(fdStr)
	if err != nil || fd < 3 {
		log.Warnf("proxx update: ignoring invalid %s", listenFDEnv)
		return nil
	}
	f := os.NewFile(uintptr(fd), "proxx-listener")
	ln, err := net.FileListener(f)
	_ = f.Close()
	if err != nil {
		log.WithError(err).Warn("proxx update: cannot adopt the inherited listener; binding again")
		return nil
	}
	if !samePort(ln.Addr(), addr) {
		log.Warnf("proxx update: inherited listener %s does not match %s; binding again", ln.Addr(), addr)
		_ = ln.Close()
		return nil
	}
	log.Infof("proxx update: adopted inherited listener %s", ln.Addr())
	return ln
}

func samePort(a net.Addr, addr string) bool {
	tcp, ok := a.(*net.TCPAddr)
	if !ok {
		return false
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	return port == strconv.Itoa(tcp.Port)
}
