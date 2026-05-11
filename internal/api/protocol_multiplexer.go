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

		// Guard TLS handshake and protocol peek with a deadline so a client
		// that connects but never sends data cannot block the accept loop.
		_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

		tlsConn, ok := conn.(*tls.Conn)
		if ok {
			if errHandshake := tlsConn.Handshake(); errHandshake != nil {
				if errClose := conn.Close(); errClose != nil {
					log.Errorf("failed to close connection after TLS handshake error: %v", errClose)
				}
				continue
			}
			proto := strings.TrimSpace(tlsConn.ConnectionState().NegotiatedProtocol)
			if proto == "h2" || proto == "http/1.1" {
				_ = conn.SetDeadline(time.Time{})
				if httpListener == nil {
					if errClose := conn.Close(); errClose != nil {
						log.Errorf("failed to close connection: %v", errClose)
					}
					continue
				}
				if errPut := httpListener.Put(tlsConn); errPut != nil {
					if errClose := conn.Close(); errClose != nil {
						log.Errorf("failed to close connection after HTTP routing failure: %v", errClose)
					}
				}
				continue
			}
		}

		reader := bufio.NewReader(conn)
		prefix, errPeek := reader.Peek(1)
		_ = conn.SetDeadline(time.Time{})
		if errPeek != nil {
			if errClose := conn.Close(); errClose != nil {
				log.Errorf("failed to close connection after protocol peek failure: %v", errClose)
			}
			continue
		}

		if isRedisRESPPrefix(prefix[0]) {
			if s.cfg != nil && s.cfg.Home.Enabled {
				if errClose := conn.Close(); errClose != nil {
					log.Errorf("failed to close redis connection while home mode is enabled: %v", errClose)
				}
				continue
			}
			if !s.managementRoutesEnabled.Load() {
				if errClose := conn.Close(); errClose != nil {
					log.Errorf("failed to close redis connection while management is disabled: %v", errClose)
				}
				continue
			}
			go s.handleRedisConnection(conn, reader)
			continue
		}

		if httpListener == nil {
			if errClose := conn.Close(); errClose != nil {
				log.Errorf("failed to close connection without HTTP listener: %v", errClose)
			}
			continue
		}

		if errPut := httpListener.Put(&bufferedConn{Conn: conn, reader: reader}); errPut != nil {
			if errClose := conn.Close(); errClose != nil {
				log.Errorf("failed to close connection after HTTP routing failure: %v", errClose)
			}
		}
	}
}
