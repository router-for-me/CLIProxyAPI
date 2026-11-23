package api

import (
	"net"
	"sync"
	"time"
)

type muxListener struct {
	addr     net.Addr
	connCh   chan net.Conn
	closeCh  chan struct{}
	closed   bool
	mu       sync.Mutex
	inFlight sync.WaitGroup
	once     sync.Once
}

func newMuxListener(addr net.Addr, buffer int) *muxListener {
	if buffer <= 0 {
		buffer = 1
	}
	return &muxListener{
		addr:    addr,
		connCh:  make(chan net.Conn, buffer),
		closeCh: make(chan struct{}),
	}
}

func (l *muxListener) Put(conn net.Conn) error {
	if conn == nil {
		return nil
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return net.ErrClosed
	}
	l.inFlight.Add(1)
	l.mu.Unlock()
	defer l.inFlight.Done()

	select {
	case <-l.closeCh:
		return net.ErrClosed
	case l.connCh <- conn:
		return nil
	}
}

func (l *muxListener) putWithTimeout(conn net.Conn, timeout time.Duration) error {
	if conn == nil {
		return nil
	}
	if timeout <= 0 {
		return l.Put(conn)
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return net.ErrClosed
	}
	l.inFlight.Add(1)
	l.mu.Unlock()
	defer l.inFlight.Done()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-l.closeCh:
		return net.ErrClosed
	case l.connCh <- conn:
		return nil
	case <-timer.C:
		return net.ErrClosed
	}
}

func (l *muxListener) isClosed() bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.closed
}

func (l *muxListener) Accept() (net.Conn, error) {
	select {
	case <-l.closeCh:
		return nil, net.ErrClosed
	case conn, ok := <-l.connCh:
		if !ok || conn == nil {
			return nil, net.ErrClosed
		}
		select {
		case <-l.closeCh:
			_ = conn.Close()
			return nil, net.ErrClosed
		default:
			return conn, nil
		}
	}
}

func (l *muxListener) Close() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		l.mu.Lock()
		l.closed = true
		close(l.closeCh)
		l.mu.Unlock()

		// Wait for all in-flight Put calls to complete their channel send or observe closeCh
		l.inFlight.Wait()

		// Drain and close all connections currently queued in connCh
		for {
			select {
			case conn := <-l.connCh:
				if conn != nil {
					_ = conn.Close()
				}
			default:
				return
			}
		}
	})
	return nil
}

func (l *muxListener) Addr() net.Addr {
	if l == nil {
		return &net.TCPAddr{}
	}
	if l.addr == nil {
		return &net.TCPAddr{}
	}
	return l.addr
}
