package api

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/redisqueue"
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

func (s *Server) handleRedisConnection(conn net.Conn, reader *bufio.Reader) {
	if s == nil || conn == nil || reader == nil {
		return
	}

	clientIP, localClient := resolveRemoteIP(conn.RemoteAddr())
	authed := false
	writer := bufio.NewWriter(conn)
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			log.Errorf("redis connection close error: %v", errClose)
		}
	}()

	flush := func() bool {
		if errFlush := writer.Flush(); errFlush != nil {
			log.Errorf("redis protocol flush error: %v", errFlush)
			return false
		}
		return true
	}

	for {
		if !s.managementRoutesEnabled.Load() {
			return
		}

		args, err := readRESPArray(reader)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				_ = writeRedisError(writer, "ERR "+err.Error())
				_ = writer.Flush()
			}
			return
		}
		if len(args) == 0 {
			_ = writeRedisError(writer, "ERR empty command")
			if !flush() {
				return
			}
			continue
		}

		cmd := strings.ToUpper(strings.TrimSpace(args[0]))
		switch cmd {
		case "AUTH":
			password, ok := parseAuthPassword(args)
			if !ok {
				_ = writeRedisError(writer, "ERR wrong number of arguments for 'auth' command")
				if !flush() {
					return
				}
				continue
			}
			if s.mgmt == nil {
				_ = writeRedisError(writer, "ERR remote management disabled")
				if !flush() {
					return
				}
				continue
			}
			allowed, _, errMsg := s.mgmt.AuthenticateManagementKey(clientIP, localClient, password)
			if !allowed {
				_ = writeRedisError(writer, "ERR "+errMsg)
				if !flush() {
					return
				}
				continue
			}
			authed = true
			_ = writeRedisSimpleString(writer, "OK")
			if !flush() {
				return
			}
		case "LPOP", "RPOP":
			if !authed {
				_ = writeRedisError(writer, "NOAUTH Authentication required.")
				if !flush() {
					return
				}
				continue
			}
			count, hasCount, ok := parsePopCount(args)
			if !ok {
				_ = writeRedisError(writer, "ERR wrong number of arguments for '"+strings.ToLower(cmd)+"' command")
				if !flush() {
					return
				}
				continue
			}
			if count <= 0 {
				_ = writeRedisError(writer, "ERR value is not an integer or out of range")
				if !flush() {
					return
				}
				continue
			}
			items := redisqueue.PopOldest(count)
			if hasCount {
				_ = writeRedisArrayOfBulkStrings(writer, items)
				if !flush() {
					return
				}
				continue
			}
			if len(items) == 0 {
				_ = writeRedisNilBulkString(writer)
				if !flush() {
					return
				}
				continue
			}
			_ = writeRedisBulkString(writer, items[0])
			if !flush() {
				return
			}
		default:
			_ = writeRedisError(writer, fmt.Sprintf("ERR unknown command '%s'", strings.ToLower(cmd)))
			if !flush() {
				return
			}
		}
	}
}

func resolveRemoteIP(addr net.Addr) (ip string, localClient bool) {
	if addr == nil {
		return "", false
	}
	host := addr.String()
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSpace(host)
	localClient = host == "127.0.0.1" || host == "::1"
	return host, localClient
}

func parseAuthPassword(args []string) (string, bool) {
	switch len(args) {
	case 2:
		return args[1], true
	case 3:
		// Support AUTH <username> <password> by ignoring username for compatibility.
		return args[2], true
	default:
		return "", false
	}
}

func parsePopCount(args []string) (count int, hasCount bool, ok bool) {
	if len(args) != 2 && len(args) != 3 {
		return 0, false, false
	}
	if len(args) == 2 {
		return 1, false, true
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(args[2]))
	if err != nil {
		return 0, true, true
	}
	return parsed, true, true
}

func readRESPArray(reader *bufio.Reader) ([]string, error) {
	prefix, err := reader.ReadByte()
	if err != nil {
		return nil, err
	}
	if prefix != '*' {
		return nil, fmt.Errorf("protocol error")
	}
	line, err := readRESPLine(reader)
	if err != nil {
		return nil, err
	}
	count, err := strconv.Atoi(line)
	if err != nil || count < 0 {
		return nil, fmt.Errorf("protocol error")
	}
	args := make([]string, 0, count)
	for i := 0; i < count; i++ {
		value, err := readRESPString(reader)
		if err != nil {
			return nil, err
		}
		args = append(args, value)
	}
	return args, nil
}

func readRESPString(reader *bufio.Reader) (string, error) {
	prefix, err := reader.ReadByte()
	if err != nil {
		return "", err
	}
	switch prefix {
	case '$':
		return readRESPBulkString(reader)
	case '+', ':':
		return readRESPLine(reader)
	default:
		return "", fmt.Errorf("protocol error")
	}
}

func readRESPBulkString(reader *bufio.Reader) (string, error) {
	line, err := readRESPLine(reader)
	if err != nil {
		return "", err
	}
	length, err := strconv.Atoi(line)
	if err != nil {
		return "", fmt.Errorf("protocol error")
	}
	if length < 0 {
		return "", nil
	}
	buf := make([]byte, length+2)
	if _, err := io.ReadFull(reader, buf); err != nil {
		return "", err
	}
	if length+2 < 2 || buf[length] != '\r' || buf[length+1] != '\n' {
		return "", fmt.Errorf("protocol error")
	}
	return string(buf[:length]), nil
}

func readRESPLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	line = strings.TrimSuffix(line, "\n")
	line = strings.TrimSuffix(line, "\r")
	return line, nil
}

func writeRedisSimpleString(writer *bufio.Writer, value string) error {
	if writer == nil {
		return net.ErrClosed
	}
	_, err := writer.WriteString("+" + value + "\r\n")
	return err
}

func writeRedisError(writer *bufio.Writer, message string) error {
	if writer == nil {
		return net.ErrClosed
	}
	_, err := writer.WriteString("-" + message + "\r\n")
	return err
}

func writeRedisNilBulkString(writer *bufio.Writer) error {
	if writer == nil {
		return net.ErrClosed
	}
	_, err := writer.WriteString("$-1\r\n")
	return err
}

func writeRedisBulkString(writer *bufio.Writer, payload []byte) error {
	if writer == nil {
		return net.ErrClosed
	}
	if payload == nil {
		return writeRedisNilBulkString(writer)
	}
	if _, err := writer.WriteString("$" + strconv.Itoa(len(payload)) + "\r\n"); err != nil {
		return err
	}
	if _, err := writer.Write(payload); err != nil {
		return err
	}
	_, err := writer.WriteString("\r\n")
	return err
}

func writeRedisArrayOfBulkStrings(writer *bufio.Writer, items [][]byte) error {
	if writer == nil {
		return net.ErrClosed
	}
	if _, err := writer.WriteString("*" + strconv.Itoa(len(items)) + "\r\n"); err != nil {
		return err
	}
	for i := range items {
		if err := writeRedisBulkString(writer, items[i]); err != nil {
			return err
		}
	}
	return nil
}
