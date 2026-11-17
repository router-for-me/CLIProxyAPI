package selfupdate

import (
	"net"
	"os"
	"strconv"
	"sync"

	log "github.com/sirupsen/logrus"
)

// listenFDEnv carries the inherited listening socket across the in-place exec.
const listenFDEnv = "PROXX_LISTEN_FD"

// gateListener wraps the API server's base listener. While paused, Accept does not take new
// connections, so they wait in the kernel backlog (they are not refused) until the restarted
// process accepts them on the same socket.
type gateListener struct {
	net.Listener
	mu     sync.Mutex
	paused bool
	closed bool
	resume chan struct{}
}

func newGateListener(inner net.Listener) *gateListener {
	return &gateListener{Listener: inner, resume: make(chan struct{})}
}

func (l *gateListener) Accept() (net.Conn, error) {
	for {
		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			return nil, net.ErrClosed
		}
		if !l.paused {
			l.mu.Unlock()
			return l.Listener.Accept()
		}
		ch := l.resume
		l.mu.Unlock()
		<-ch
	}
}

func (l *gateListener) setPaused(p bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.paused == p {
		return
	}
	l.paused = p
	if !p {
		close(l.resume)
		l.resume = make(chan struct{})
	}
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
