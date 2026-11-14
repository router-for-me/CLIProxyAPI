package wstunnel

import (
	"bufio"
	"errors"
	"net"
	"net/http"
	"reflect"
	"sync/atomic"
	"time"
)

// deadlineGuardResponseWriter protects even Gorilla's internal protocol-error writes.
type deadlineGuardResponseWriter struct {
	http.ResponseWriter
	conn *deadlineGuardConn
}

func (w *deadlineGuardResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, errHijack := http.NewResponseController(w.ResponseWriter).Hijack()
	if errHijack != nil {
		return nil, nil, errHijack
	}
	// Gorilla v1.5.3 rejects buffered input before sending HTTP 101. The Hub
	// sets ReadBufferSize, so its accepted reader always reads through this guard.
	w.conn = &deadlineGuardConn{Conn: conn}
	return w.conn, rw, nil
}

// deadlineGuardConn permits handshake deadlines, but never finite upgraded deadlines.
type deadlineGuardConn struct {
	net.Conn
	postUpgrade     atomic.Bool
	frames          frameInspector
	onProtocolError func()
}

var errInvalidInboundFrame = errors.New("wstunnel: invalid inbound WebSocket frame")

func (c *deadlineGuardConn) Read(raw []byte) (int, error) {
	if c.frames.failed {
		return 0, errInvalidInboundFrame
	}
	n, errRead := c.Conn.Read(raw)
	if n > 0 && c.postUpgrade.Load() && !c.frames.inspect(raw[:n]) {
		// Do not expose fatal bytes to Gorilla: its reader could otherwise wait
		// a second for the write token before attempting a protocol-error close.
		if c.onProtocolError != nil {
			c.onProtocolError()
		}
		return 0, errInvalidInboundFrame
	}
	return n, errRead
}

// rawTransportConn follows transport wrappers without invoking their Close methods.
func rawTransportConn(conn net.Conn) net.Conn {
	seen := make(map[net.Conn]struct{})
	for conn != nil {
		value := reflect.ValueOf(conn)
		// Reject malformed wrappers before calling methods or using them as map keys.
		if !value.Comparable() {
			return nil
		}
		switch value.Kind() {
		case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
			if value.IsNil() {
				return nil
			}
		}
		if _, okSeen := seen[conn]; okSeen {
			return nil
		}
		seen[conn] = struct{}{}
		wrapper, okWrapper := conn.(interface{ NetConn() net.Conn })
		if !okWrapper {
			return conn
		}
		conn = wrapper.NetConn()
	}
	return nil
}

func (c *deadlineGuardConn) NetConn() net.Conn {
	return c.Conn
}

func (c *deadlineGuardConn) SetDeadline(deadline time.Time) error {
	if c.postUpgrade.Load() && !deadline.IsZero() {
		return nil
	}
	return c.Conn.SetDeadline(deadline)
}

func (c *deadlineGuardConn) SetReadDeadline(deadline time.Time) error {
	if c.postUpgrade.Load() && !deadline.IsZero() {
		return nil
	}
	return c.Conn.SetReadDeadline(deadline)
}

func (c *deadlineGuardConn) SetWriteDeadline(deadline time.Time) error {
	if c.postUpgrade.Load() && !deadline.IsZero() {
		return nil
	}
	return c.Conn.SetWriteDeadline(deadline)
}
