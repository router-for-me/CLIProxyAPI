package api

import (
	"bufio"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	muxSniffDeadline        = 10 * time.Second
	initialAcceptRetryDelay = 5 * time.Millisecond
	maxAcceptRetryDelay     = 1 * time.Second
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

func isTransientAcceptError(err error) bool {
	if err == nil || errors.Is(err, net.ErrClosed) {
		return false
	}
	if errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Temporary() {
		return true
	}
	return false
}

func waitAcceptRetry(httpListener *muxListener, delay time.Duration) bool {
	if httpListener != nil && httpListener.isClosed() {
		return false
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	if httpListener == nil {
		<-timer.C
		return true
	}
	select {
	case <-httpListener.closeCh:
		return false
	case <-timer.C:
		return !httpListener.isClosed()
	}
}

func (s *Server) acceptMuxConnections(listener net.Listener, httpListener *muxListener) error {
	if s == nil || listener == nil {
		return net.ErrClosed
	}

	var retryDelay time.Duration
	for {
		conn, errAccept := listener.Accept()
		if errAccept != nil {
			if httpListener != nil && httpListener.isClosed() {
				return net.ErrClosed
			}
			if isTransientAcceptError(errAccept) {
				if retryDelay == 0 {
					retryDelay = initialAcceptRetryDelay
				} else {
					retryDelay *= 2
					if retryDelay > maxAcceptRetryDelay {
						retryDelay = maxAcceptRetryDelay
					}
				}
				log.Warnf("transient accept error on shared listener: %v; retrying in %v", errAccept, retryDelay)
				if !waitAcceptRetry(httpListener, retryDelay) {
					return net.ErrClosed
				}
				continue
			}
			return errAccept
		}
		retryDelay = 0
		if conn == nil {
			continue
		}
		if httpListener != nil && httpListener.isClosed() {
			_ = conn.Close()
			return net.ErrClosed
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
			if errPut := httpListener.putWithTimeout(tlsConn, muxSniffDeadline); errPut != nil {
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
	if errPut := httpListener.putWithTimeout(&bufferedConn{Conn: conn, reader: reader}, muxSniffDeadline); errPut != nil {
		if errClose := conn.Close(); errClose != nil {
			log.Errorf("failed to close connection after HTTP routing failure: %v", errClose)
		}
	}
}
