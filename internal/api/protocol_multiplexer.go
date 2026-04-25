package api

import (
	"bufio"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"strings"

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
		if errPeek != nil {
			if errClose := conn.Close(); errClose != nil {
				log.Errorf("failed to close connection after protocol peek failure: %v", errClose)
			}
			continue
		}

		if isRedisRESPPrefix(prefix[0]) {
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
