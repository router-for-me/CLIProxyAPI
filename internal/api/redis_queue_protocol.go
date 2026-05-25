package api

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	log "github.com/sirupsen/logrus"
)

const redisUsageChannel = "usage"

type redisSubscriptionCommand struct {
	args []string
	err  error
}

func isRedisRESPPrefix(prefix byte) bool {
	switch prefix {
	case '*', '$', '+', '-', ':':
		return true
	default:
		return false
	}
}

func (s *Server) handleRedisConnection(conn net.Conn, reader *bufio.Reader) {
	if s == nil || conn == nil {
		return
	}
	if reader == nil {
		reader = bufio.NewReader(conn)
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

	if s.cfg != nil && s.cfg.Home.Enabled {
		_ = writeRedisError(writer, "ERR redis usage output disabled in home mode")
		_ = writer.Flush()
		return
	}

	for {
		if !s.managementRoutesEnabled.Load() {
			return
		}

		args, errRead := readRESPArray(reader)
		if errRead != nil {
			if !errors.Is(errRead, io.EOF) {
				_ = writeRedisError(writer, "ERR "+errRead.Error())
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

		if cmd != "AUTH" && !authed {
			if s.mgmt != nil {
				_, statusCode, errMsg := s.mgmt.AuthenticateManagementKey(clientIP, localClient, "")
				if statusCode == http.StatusForbidden && strings.HasPrefix(errMsg, "IP banned due to too many failed attempts") {
					_ = writeRedisError(writer, "ERR "+errMsg)
				} else {
					_ = writeRedisError(writer, "NOAUTH Authentication required.")
				}
			} else {
				_ = writeRedisError(writer, "NOAUTH Authentication required.")
			}
			if !flush() {
				return
			}
			continue
		}

		switch cmd {
		case "AUTH":
			password, ok := parseAuthPassword(args)
			if !ok {
				if s.mgmt != nil {
					_, statusCode, errMsg := s.mgmt.AuthenticateManagementKey(clientIP, localClient, "")
					if statusCode == http.StatusForbidden && strings.HasPrefix(errMsg, "IP banned due to too many failed attempts") {
						_ = writeRedisError(writer, "ERR "+errMsg)
						if !flush() {
							return
						}
						continue
					}
				}
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
		case "SUBSCRIBE":
			channel, ok := parseSubscribeChannel(args)
			if !ok {
				_ = writeRedisError(writer, "ERR wrong number of arguments for 'subscribe' command")
				if !flush() {
					return
				}
				continue
			}
			if !strings.EqualFold(channel, redisUsageChannel) {
				_ = writeRedisError(writer, fmt.Sprintf("ERR unsupported channel '%s'", channel))
				if !flush() {
					return
				}
				continue
			}
			messages, unsubscribe := redisqueue.SubscribeUsage()
			if errWrite := writeRedisPubSubSubscribe(writer, redisUsageChannel, 1); errWrite != nil {
				unsubscribe()
				log.Errorf("redis protocol subscribe response error: %v", errWrite)
				return
			}
			if !flush() {
				unsubscribe()
				return
			}
			s.streamRedisUsageSubscription(reader, writer, messages, unsubscribe)
			return
		case "LPOP", "RPOP":
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

func (s *Server) streamRedisUsageSubscription(reader *bufio.Reader, writer *bufio.Writer, messages <-chan []byte, unsubscribe func()) {
	if unsubscribe == nil {
		return
	}
	defer unsubscribe()

	done := make(chan struct{})
	defer close(done)

	commands := make(chan redisSubscriptionCommand, 1)
	go readRedisSubscriptionCommands(reader, commands, done)

	for {
		select {
		case msg, ok := <-messages:
			if !ok {
				return
			}
			if errWrite := writeRedisPubSubMessage(writer, redisUsageChannel, msg); errWrite != nil {
				log.Errorf("redis protocol publish message error: %v", errWrite)
				return
			}
			if errFlush := writer.Flush(); errFlush != nil {
				log.Errorf("redis protocol flush error: %v", errFlush)
				return
			}
		case command, ok := <-commands:
			if !ok {
				return
			}
			keepOpen := handleRedisSubscriptionCommand(writer, command)
			if errFlush := writer.Flush(); errFlush != nil {
				log.Errorf("redis protocol flush error: %v", errFlush)
				return
			}
			if !keepOpen {
				return
			}
		}
	}
}

func readRedisSubscriptionCommands(reader *bufio.Reader, commands chan<- redisSubscriptionCommand, done <-chan struct{}) {
	defer close(commands)

	for {
		args, errRead := readRESPArray(reader)
		if errRead != nil {
			if !errors.Is(errRead, io.EOF) {
				select {
				case commands <- redisSubscriptionCommand{err: errRead}:
				case <-done:
				}
			}
			return
		}
		select {
		case commands <- redisSubscriptionCommand{args: args}:
		case <-done:
			return
		}
	}
}

func handleRedisSubscriptionCommand(writer *bufio.Writer, command redisSubscriptionCommand) bool {
	if command.err != nil {
		_ = writeRedisError(writer, "ERR "+command.err.Error())
		return false
	}
	if len(command.args) == 0 {
		_ = writeRedisError(writer, "ERR empty command")
		return true
	}

	cmd := strings.ToUpper(strings.TrimSpace(command.args[0]))
	switch cmd {
	case "PING":
		payload := []byte(nil)
		if len(command.args) > 1 {
			payload = []byte(command.args[1])
		}
		_ = writeRedisPubSubPong(writer, payload)
		return true
	case "UNSUBSCRIBE":
		_ = writeRedisPubSubUnsubscribe(writer, redisUsageChannel, 0)
		return false
	case "QUIT":
		_ = writeRedisSimpleString(writer, "OK")
		return false
	default:
		_ = writeRedisError(writer, fmt.Sprintf("ERR unknown command '%s'", strings.ToLower(cmd)))
		return true
	}
}

func resolveRemoteIP(addr net.Addr) (ip string, localClient bool) {
	if addr == nil {
		return "", false
	}

	var host string
	switch a := addr.(type) {
	case *net.TCPAddr:
		if a != nil && a.IP != nil {
			if ip4 := a.IP.To4(); ip4 != nil {
				host = ip4.String()
			} else {
				host = a.IP.String()
			}
		}
	default:
		host = addr.String()
		if h, _, errSplit := net.SplitHostPort(host); errSplit == nil {
			host = h
		}
		host = strings.TrimSpace(host)
		if raw, _, ok := strings.Cut(host, "%"); ok {
			host = raw
		}
		if parsed := net.ParseIP(host); parsed != nil {
			if ip4 := parsed.To4(); ip4 != nil {
				host = ip4.String()
			} else {
				host = parsed.String()
			}
		}
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
		return args[2], true
	default:
		return "", false
	}
}

func parseSubscribeChannel(args []string) (string, bool) {
	if len(args) != 2 {
		return "", false
	}
	return strings.TrimSpace(args[1]), true
}

func parsePopCount(args []string) (count int, hasCount bool, ok bool) {
	if len(args) != 2 && len(args) != 3 {
		return 0, false, false
	}
	if len(args) == 2 {
		return 1, false, true
	}
	parsed, errParse := strconv.Atoi(strings.TrimSpace(args[2]))
	if errParse != nil {
		return 0, true, true
	}
	return parsed, true, true
}

func readRESPArray(reader *bufio.Reader) ([]string, error) {
	prefix, errRead := reader.ReadByte()
	if errRead != nil {
		return nil, errRead
	}
	if prefix != '*' {
		return nil, fmt.Errorf("protocol error")
	}
	line, errLine := readRESPLine(reader)
	if errLine != nil {
		return nil, errLine
	}
	count, errParse := strconv.Atoi(line)
	if errParse != nil || count < 0 {
		return nil, fmt.Errorf("protocol error")
	}
	args := make([]string, 0, count)
	for i := 0; i < count; i++ {
		value, errString := readRESPString(reader)
		if errString != nil {
			return nil, errString
		}
		args = append(args, value)
	}
	return args, nil
}

func readRESPString(reader *bufio.Reader) (string, error) {
	prefix, errRead := reader.ReadByte()
	if errRead != nil {
		return "", errRead
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
	line, errLine := readRESPLine(reader)
	if errLine != nil {
		return "", errLine
	}
	length, errParse := strconv.Atoi(line)
	if errParse != nil {
		return "", fmt.Errorf("protocol error")
	}
	if length < 0 {
		return "", nil
	}
	buf := make([]byte, length+2)
	if _, errRead := io.ReadFull(reader, buf); errRead != nil {
		return "", errRead
	}
	if length+2 < 2 || buf[length] != '\r' || buf[length+1] != '\n' {
		return "", fmt.Errorf("protocol error")
	}
	return string(buf[:length]), nil
}

func readRESPLine(reader *bufio.Reader) (string, error) {
	line, errRead := reader.ReadString('\n')
	if errRead != nil {
		return "", errRead
	}
	line = strings.TrimSuffix(line, "\n")
	line = strings.TrimSuffix(line, "\r")
	return line, nil
}

func writeRedisSimpleString(writer *bufio.Writer, value string) error {
	if writer == nil {
		return net.ErrClosed
	}
	_, errWrite := writer.WriteString("+" + value + "\r\n")
	return errWrite
}

func writeRedisError(writer *bufio.Writer, message string) error {
	if writer == nil {
		return net.ErrClosed
	}
	_, errWrite := writer.WriteString("-" + message + "\r\n")
	return errWrite
}

func writeRedisNilBulkString(writer *bufio.Writer) error {
	if writer == nil {
		return net.ErrClosed
	}
	_, errWrite := writer.WriteString("$-1\r\n")
	return errWrite
}

func writeRedisBulkString(writer *bufio.Writer, payload []byte) error {
	if writer == nil {
		return net.ErrClosed
	}
	if payload == nil {
		return writeRedisNilBulkString(writer)
	}
	if _, errWrite := writer.WriteString("$" + strconv.Itoa(len(payload)) + "\r\n"); errWrite != nil {
		return errWrite
	}
	if _, errWrite := writer.Write(payload); errWrite != nil {
		return errWrite
	}
	_, errWrite := writer.WriteString("\r\n")
	return errWrite
}

func writeRedisArrayOfBulkStrings(writer *bufio.Writer, items [][]byte) error {
	if writer == nil {
		return net.ErrClosed
	}
	if _, errWrite := writer.WriteString("*" + strconv.Itoa(len(items)) + "\r\n"); errWrite != nil {
		return errWrite
	}
	for i := range items {
		if errWrite := writeRedisBulkString(writer, items[i]); errWrite != nil {
			return errWrite
		}
	}
	return nil
}

func writeRedisInteger(writer *bufio.Writer, value int) error {
	if writer == nil {
		return net.ErrClosed
	}
	_, errWrite := writer.WriteString(":" + strconv.Itoa(value) + "\r\n")
	return errWrite
}

func writeRedisArrayHeader(writer *bufio.Writer, count int) error {
	if writer == nil {
		return net.ErrClosed
	}
	_, errWrite := writer.WriteString("*" + strconv.Itoa(count) + "\r\n")
	return errWrite
}

func writeRedisPubSubSubscribe(writer *bufio.Writer, channel string, count int) error {
	if errWrite := writeRedisArrayHeader(writer, 3); errWrite != nil {
		return errWrite
	}
	if errWrite := writeRedisBulkString(writer, []byte("subscribe")); errWrite != nil {
		return errWrite
	}
	if errWrite := writeRedisBulkString(writer, []byte(channel)); errWrite != nil {
		return errWrite
	}
	return writeRedisInteger(writer, count)
}

func writeRedisPubSubUnsubscribe(writer *bufio.Writer, channel string, count int) error {
	if errWrite := writeRedisArrayHeader(writer, 3); errWrite != nil {
		return errWrite
	}
	if errWrite := writeRedisBulkString(writer, []byte("unsubscribe")); errWrite != nil {
		return errWrite
	}
	if errWrite := writeRedisBulkString(writer, []byte(channel)); errWrite != nil {
		return errWrite
	}
	return writeRedisInteger(writer, count)
}

func writeRedisPubSubMessage(writer *bufio.Writer, channel string, payload []byte) error {
	if errWrite := writeRedisArrayHeader(writer, 3); errWrite != nil {
		return errWrite
	}
	if errWrite := writeRedisBulkString(writer, []byte("message")); errWrite != nil {
		return errWrite
	}
	if errWrite := writeRedisBulkString(writer, []byte(channel)); errWrite != nil {
		return errWrite
	}
	return writeRedisBulkString(writer, payload)
}

func writeRedisPubSubPong(writer *bufio.Writer, payload []byte) error {
	if errWrite := writeRedisArrayHeader(writer, 2); errWrite != nil {
		return errWrite
	}
	if errWrite := writeRedisBulkString(writer, []byte("pong")); errWrite != nil {
		return errWrite
	}
	return writeRedisBulkString(writer, payload)
}
