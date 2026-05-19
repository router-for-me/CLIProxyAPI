package api

import (
	"bufio"
	"net"

	log "github.com/sirupsen/logrus"
)

func isRedisRESPPrefix(prefix byte) bool {
	switch prefix {
	case '*', '$', '+', '-', ':':
		return true
	default:
		return false
	}
}

func (s *Server) handleRedisConnection(conn net.Conn) {
	if s == nil || conn == nil {
		return
	}

	writer := bufio.NewWriter(conn)
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			log.Errorf("redis connection close error: %v", errClose)
		}
	}()

	_ = writeRedisError(writer, "ERR RESP AUTH disabled; use mTLS")
	if errFlush := writer.Flush(); errFlush != nil {
		log.Errorf("redis protocol flush error: %v", errFlush)
	}
}

func writeRedisError(writer *bufio.Writer, message string) error {
	if writer == nil {
		return net.ErrClosed
	}
	_, err := writer.WriteString("-" + message + "\r\n")
	return err
}
