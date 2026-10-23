package api

import (
	"bufio"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

func normalizeHTTPServeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func normalizeListenerError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func (s *Server) acceptMuxConnections(listener net.Listener, httpListener *muxListener) error {
	if s == nil || listener == nil {
		return net.ErrClosed
	}

	for {
		conn, errAccept := listener.Accept()
		if errAccept != nil {
			return errAccept
		}
		if conn == nil {
			continue
		}

		// Dispatch each connection to a goroutine so that slow/idle clients
		// cannot block the accept loop. Previously, TLS handshake and
		// reader.Peek(1) were performed inline; an idle TCP connection that
		// never sent bytes would block Peek indefinitely, preventing all
		// subsequent connections from being accepted (issue #3267).
		go s.routeMuxConnection(conn, httpListener)
	}
}

// routeMuxConnection performs per-connection protocol detection and routing.
func (s *Server) routeMuxConnection(conn net.Conn, httpListener *muxListener) {
	defer func() {
		if recovered := recover(); recovered != nil {
			var remoteAddr net.Addr
			if conn != nil {
				remoteAddr = conn.RemoteAddr()
			}
			log.Errorf("panic while routing connection from %v: %v", remoteAddr, recovered)
			if conn != nil {
				if errClose := conn.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
					log.Errorf("failed to close connection after panic: %v", errClose)
				}
			}
		}
	}()

	// Set a read deadline so that idle connections that never send bytes do not
	// leak goroutines and file descriptors. The deadline is cleared once the
	// connection is successfully routed to its handler.
	const muxSniffDeadline = 10 * time.Second
	_ = conn.SetReadDeadline(time.Now().Add(muxSniffDeadline))

	tlsConn, ok := conn.(*tls.Conn)
	if ok {
		if errHandshake := tlsConn.Handshake(); errHandshake != nil {
			if errClose := conn.Close(); errClose != nil {
				log.Errorf("failed to close connection after TLS handshake error: %v", errClose)
			}
			return
		}
		proto := strings.TrimSpace(tlsConn.ConnectionState().NegotiatedProtocol)
		if proto == "h2" || proto == "http/1.1" {
			if httpListener == nil {
				if errClose := conn.Close(); errClose != nil {
					log.Errorf("failed to close connection: %v", errClose)
				}
				return
			}
			_ = conn.SetReadDeadline(time.Time{})
			if errPut := httpListener.Put(tlsConn); errPut != nil {
				if errClose := conn.Close(); errClose != nil {
					log.Errorf("failed to close connection after HTTP routing failure: %v", errClose)
				}
			}
			return
		}
	}

	reader := bufio.NewReader(conn)
	prefix, errPeek := reader.Peek(1)
	if errPeek != nil {
		if errClose := conn.Close(); errClose != nil {
			log.Errorf("failed to close connection after protocol peek failure: %v", errClose)
		}
		return
	}

	if isRedisRESPPrefix(prefix[0]) {
		_ = conn.SetReadDeadline(time.Time{})
		s.handleRedisConnection(conn, reader)
		return
	}

	if httpListener == nil {
		if errClose := conn.Close(); errClose != nil {
			log.Errorf("failed to close connection without HTTP listener: %v", errClose)
		}
		return
	}

	_ = conn.SetReadDeadline(time.Time{})
	if errPut := httpListener.Put(&bufferedConn{Conn: conn, reader: reader}); errPut != nil {
		if errClose := conn.Close(); errClose != nil {
			log.Errorf("failed to close connection after HTTP routing failure: %v", errClose)
		}
	}
}
