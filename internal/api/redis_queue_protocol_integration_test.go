package api

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/redisqueue"
)

func startRedisMuxListener(t *testing.T, server *Server) (addr string, stop func()) {
	t.Helper()

	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("failed to listen: %v", errListen)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.acceptMuxConnections(listener, nil)
	}()

	stop = func() {
		_ = listener.Close()
		select {
		case err := <-errCh:
			if err != nil && !errors.Is(err, net.ErrClosed) {
				t.Errorf("accept loop returned unexpected error: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Errorf("timeout waiting for accept loop to exit")
		}
	}

	return listener.Addr().String(), stop
}

func writeTestRESPCommand(conn net.Conn, args ...string) error {
	if conn == nil {
		return net.ErrClosed
	}
	if len(args) == 0 {
		return nil
	}

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "*%d\r\n", len(args))
	for _, arg := range args {
		fmt.Fprintf(&buf, "$%d\r\n%s\r\n", len(arg), arg)
	}
	_, err := conn.Write(buf.Bytes())
	return err
}

func readTestRESPLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(line, "\r\n") {
		return "", fmt.Errorf("invalid RESP line terminator: %q", line)
	}
	return strings.TrimSuffix(line, "\r\n"), nil
}

func readTestRESPError(r *bufio.Reader) (string, error) {
	prefix, err := r.ReadByte()
	if err != nil {
		return "", err
	}
	if prefix != '-' {
		return "", fmt.Errorf("expected error prefix '-', got %q", prefix)
	}
	return readTestRESPLine(r)
}

func readTestRESPSimpleString(r *bufio.Reader) (string, error) {
	prefix, errRead := r.ReadByte()
	if errRead != nil {
		return "", errRead
	}
	if prefix != '+' {
		return "", fmt.Errorf("expected simple string prefix '+', got %q", prefix)
	}
	return readTestRESPLine(r)
}

func readTestRESPBulkString(r *bufio.Reader) ([]byte, error) {
	prefix, errRead := r.ReadByte()
	if errRead != nil {
		return nil, errRead
	}
	if prefix != '$' {
		return nil, fmt.Errorf("expected bulk string prefix '$', got %q", prefix)
	}

	line, errLine := readTestRESPLine(r)
	if errLine != nil {
		return nil, errLine
	}
	length, errParse := strconv.Atoi(line)
	if errParse != nil {
		return nil, fmt.Errorf("invalid bulk string length %q: %v", line, errParse)
	}
	if length == -1 {
		return nil, nil
	}
	if length < -1 {
		return nil, fmt.Errorf("invalid bulk string length %d", length)
	}

	payload := make([]byte, length+2)
	if _, errRead := io.ReadFull(r, payload); errRead != nil {
		return nil, errRead
	}
	if payload[length] != '\r' || payload[length+1] != '\n' {
		return nil, fmt.Errorf("invalid bulk string terminator")
	}
	return payload[:length], nil
}

func readRESPArrayOfBulkStrings(r *bufio.Reader) ([][]byte, error) {
	prefix, errRead := r.ReadByte()
	if errRead != nil {
		return nil, errRead
	}
	if prefix != '*' {
		return nil, fmt.Errorf("expected array prefix '*', got %q", prefix)
	}

	line, errLine := readTestRESPLine(r)
	if errLine != nil {
		return nil, errLine
	}
	count, errParse := strconv.Atoi(line)
	if errParse != nil {
		return nil, fmt.Errorf("invalid array length %q: %v", line, errParse)
	}
	if count < 0 {
		return nil, fmt.Errorf("invalid array length %d", count)
	}

	out := make([][]byte, 0, count)
	for i := 0; i < count; i++ {
		item, errItem := readTestRESPBulkString(r)
		if errItem != nil {
			return nil, errItem
		}
		out = append(out, item)
	}
	return out, nil
}

func readTestRESPPubSubSubscribe(r *bufio.Reader) (string, int, error) {
	prefix, errRead := r.ReadByte()
	if errRead != nil {
		return "", 0, errRead
	}
	if prefix != '*' {
		return "", 0, fmt.Errorf("expected array prefix '*', got %q", prefix)
	}
	line, errLine := readTestRESPLine(r)
	if errLine != nil {
		return "", 0, errLine
	}
	count, errParse := strconv.Atoi(line)
	if errParse != nil {
		return "", 0, fmt.Errorf("invalid array length %q: %v", line, errParse)
	}
	if count != 3 {
		return "", 0, fmt.Errorf("subscribe ack length = %d, want 3", count)
	}
	kind, errKind := readTestRESPBulkString(r)
	if errKind != nil {
		return "", 0, errKind
	}
	if string(kind) != "subscribe" {
		return "", 0, fmt.Errorf("subscribe ack kind = %q", string(kind))
	}
	channel, errChannel := readTestRESPBulkString(r)
	if errChannel != nil {
		return "", 0, errChannel
	}
	prefix, errRead = r.ReadByte()
	if errRead != nil {
		return "", 0, errRead
	}
	if prefix != ':' {
		return "", 0, fmt.Errorf("expected integer prefix ':', got %q", prefix)
	}
	line, errLine = readTestRESPLine(r)
	if errLine != nil {
		return "", 0, errLine
	}
	subscriptions, errParse := strconv.Atoi(line)
	if errParse != nil {
		return "", 0, fmt.Errorf("invalid subscription count %q: %v", line, errParse)
	}
	return string(channel), subscriptions, nil
}

func readTestRESPPubSubMessage(r *bufio.Reader) (string, []byte, error) {
	items, errItems := readRESPArrayOfBulkStrings(r)
	if errItems != nil {
		return "", nil, errItems
	}
	if len(items) != 3 {
		return "", nil, fmt.Errorf("pubsub message length = %d, want 3", len(items))
	}
	if string(items[0]) != "message" {
		return "", nil, fmt.Errorf("pubsub message kind = %q", string(items[0]))
	}
	return string(items[1]), items[2], nil
}

func TestRedisProtocol_ManagementDisabled_RejectsConnection(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	redisqueue.SetEnabled(false)

	server := newTestServer(t)
	if server.managementRoutesEnabled.Load() {
		t.Fatalf("expected managementRoutesEnabled to be false")
	}

	addr, stop := startRedisMuxListener(t, server)
	t.Cleanup(stop)

	conn, errDial := net.DialTimeout("tcp", addr, time.Second)
	if errDial != nil {
		t.Fatalf("failed to dial redis listener: %v", errDial)
	}
	t.Cleanup(func() { _ = conn.Close() })

	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if errWrite := writeTestRESPCommand(conn, "PING"); errWrite != nil {
		t.Fatalf("failed to write RESP command: %v", errWrite)
	}

	buf := make([]byte, 1)
	_, errRead := conn.Read(buf)
	if errRead == nil {
		t.Fatalf("expected connection to be closed when management is disabled")
	}
	if ne, ok := errRead.(net.Error); ok && ne.Timeout() {
		t.Fatalf("expected connection to be closed when management is disabled, got timeout: %v", errRead)
	}
}

func TestRedisProtocol_HomeEnabled_DisablesConnection(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-password")
	redisqueue.SetEnabled(false)
	t.Cleanup(func() { redisqueue.SetEnabled(false) })

	server := newTestServer(t)
	if !server.managementRoutesEnabled.Load() {
		t.Fatalf("expected managementRoutesEnabled to be true")
	}
	if server.cfg == nil {
		t.Fatalf("expected server cfg to be non-nil")
	}
	server.cfg.Home.Enabled = true
	redisqueue.SetEnabled(true)

	addr, stop := startRedisMuxListener(t, server)
	t.Cleanup(stop)

	conn, errDial := net.DialTimeout("tcp", addr, time.Second)
	if errDial != nil {
		t.Fatalf("failed to dial redis listener: %v", errDial)
	}
	t.Cleanup(func() { _ = conn.Close() })

	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	_ = writeTestRESPCommand(conn, "PING")

	if msg, err := readTestRESPError(bufio.NewReader(conn)); err != nil {
		t.Fatalf("failed to read home-mode RESP error: %v", err)
	} else if msg != "ERR redis usage output disabled in home mode" {
		t.Fatalf("unexpected disabled RESP error: %q", msg)
	}

	buf := make([]byte, 1)
	_, errRead := conn.Read(buf)
	if errRead == nil {
		t.Fatalf("expected connection to be closed after home-mode RESP error")
	}
	if ne, ok := errRead.(net.Error); ok && ne.Timeout() {
		t.Fatalf("expected connection to be closed after home-mode RESP error, got timeout: %v", errRead)
	}
}

func TestRedisProtocol_SUBSCRIBE_UsageSendsSupportRefresh(t *testing.T) {
	const managementPassword = "test-management-password"

	t.Setenv("MANAGEMENT_PASSWORD", managementPassword)
	redisqueue.SetEnabled(false)
	t.Cleanup(func() { redisqueue.SetEnabled(false) })

	server := newTestServer(t)
	if !server.managementRoutesEnabled.Load() {
		t.Fatalf("expected managementRoutesEnabled to be true")
	}

	addr, stop := startRedisMuxListener(t, server)
	t.Cleanup(stop)

	conn, errDial := net.DialTimeout("tcp", addr, time.Second)
	if errDial != nil {
		t.Fatalf("failed to dial redis listener: %v", errDial)
	}
	t.Cleanup(func() { _ = conn.Close() })

	reader := bufio.NewReader(conn)
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	if errWrite := writeTestRESPCommand(conn, "AUTH", managementPassword); errWrite != nil {
		t.Fatalf("failed to write AUTH command: %v", errWrite)
	}
	if msg, errRead := readTestRESPSimpleString(reader); errRead != nil {
		t.Fatalf("failed to read AUTH response: %v", errRead)
	} else if msg != "OK" {
		t.Fatalf("unexpected AUTH response: %q", msg)
	}

	if errWrite := writeTestRESPCommand(conn, "SUBSCRIBE", "usage"); errWrite != nil {
		t.Fatalf("failed to write SUBSCRIBE command: %v", errWrite)
	}
	channel, subscriptions, errSubscribe := readTestRESPPubSubSubscribe(reader)
	if errSubscribe != nil {
		t.Fatalf("failed to read subscribe response: %v", errSubscribe)
	}
	if channel != "usage" || subscriptions != 1 {
		t.Fatalf("unexpected subscribe response channel=%q subscriptions=%d", channel, subscriptions)
	}

	channel, payload, errMessage := readTestRESPPubSubMessage(reader)
	if errMessage != nil {
		t.Fatalf("failed to read support refresh message: %v", errMessage)
	}
	if channel != "usage" || string(payload) != `{"support_refresh":true}` {
		t.Fatalf("unexpected support refresh message channel=%q payload=%q", channel, string(payload))
	}

	redisqueue.Enqueue([]byte(`{"id":1}`))
	channel, payload, errMessage = readTestRESPPubSubMessage(reader)
	if errMessage != nil {
		t.Fatalf("failed to read usage message: %v", errMessage)
	}
	if channel != "usage" || string(payload) != `{"id":1}` {
		t.Fatalf("unexpected usage message channel=%q payload=%q", channel, string(payload))
	}
}

func TestRedisProtocol_SUBSCRIBE_ErrorsReceivesErrorEvents(t *testing.T) {
	const managementPassword = "test-management-password"

	t.Setenv("MANAGEMENT_PASSWORD", managementPassword)
	redisqueue.SetEnabled(false)
	t.Cleanup(func() { redisqueue.SetEnabled(false) })

	server := newTestServer(t)
	if !server.managementRoutesEnabled.Load() {
		t.Fatalf("expected managementRoutesEnabled to be true")
	}

	addr, stop := startRedisMuxListener(t, server)
	t.Cleanup(stop)

	conn, errDial := net.DialTimeout("tcp", addr, time.Second)
	if errDial != nil {
		t.Fatalf("failed to dial redis listener: %v", errDial)
	}
	t.Cleanup(func() { _ = conn.Close() })

	reader := bufio.NewReader(conn)
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	if errWrite := writeTestRESPCommand(conn, "AUTH", managementPassword); errWrite != nil {
		t.Fatalf("failed to write AUTH command: %v", errWrite)
	}
	if msg, errRead := readTestRESPSimpleString(reader); errRead != nil {
		t.Fatalf("failed to read AUTH response: %v", errRead)
	} else if msg != "OK" {
		t.Fatalf("unexpected AUTH response: %q", msg)
	}

	if errWrite := writeTestRESPCommand(conn, "SUBSCRIBE", "errors"); errWrite != nil {
		t.Fatalf("failed to write SUBSCRIBE command: %v", errWrite)
	}
	channel, subscriptions, errSubscribe := readTestRESPPubSubSubscribe(reader)
	if errSubscribe != nil {
		t.Fatalf("failed to read subscribe response: %v", errSubscribe)
	}
	if channel != "errors" || subscriptions != 1 {
		t.Fatalf("unexpected subscribe response channel=%q subscriptions=%d", channel, subscriptions)
	}

	redisqueue.EnqueueError([]byte(`{"auth_index":"auth-1","status_code":401}`))
	channel, payload, errMessage := readTestRESPPubSubMessage(reader)
	if errMessage != nil {
		t.Fatalf("failed to read error message: %v", errMessage)
	}
	if channel != "errors" || string(payload) != `{"auth_index":"auth-1","status_code":401}` {
		t.Fatalf("unexpected error message channel=%q payload=%q", channel, string(payload))
	}
}

func TestRedisProtocol_AUTH_And_PopContracts(t *testing.T) {
	const managementPassword = "test-management-password"

	t.Setenv("MANAGEMENT_PASSWORD", managementPassword)
	redisqueue.SetEnabled(false)
	t.Cleanup(func() { redisqueue.SetEnabled(false) })

	server := newTestServer(t)
	if !server.managementRoutesEnabled.Load() {
		t.Fatalf("expected managementRoutesEnabled to be true")
	}

	addr, stop := startRedisMuxListener(t, server)
	t.Cleanup(stop)

	conn, errDial := net.DialTimeout("tcp", addr, time.Second)
	if errDial != nil {
		t.Fatalf("failed to dial redis listener: %v", errDial)
	}
	t.Cleanup(func() { _ = conn.Close() })

	reader := bufio.NewReader(conn)

	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	if errWrite := writeTestRESPCommand(conn, "AUTH", managementPassword); errWrite != nil {
		t.Fatalf("failed to write AUTH command: %v", errWrite)
	}
	if msg, errRead := readTestRESPSimpleString(reader); errRead != nil {
		t.Fatalf("failed to read AUTH response: %v", errRead)
	} else if msg != "OK" {
		t.Fatalf("unexpected AUTH response: %q", msg)
	}

	if !redisqueue.Enabled() {
		t.Fatalf("expected redisqueue to be enabled")
	}
	redisqueue.Enqueue([]byte("a"))
	redisqueue.Enqueue([]byte("b"))
	redisqueue.Enqueue([]byte("c"))

	if errWrite := writeTestRESPCommand(conn, "RPOP", "usage"); errWrite != nil {
		t.Fatalf("failed to write RPOP command: %v", errWrite)
	}
	if item, errRead := readTestRESPBulkString(reader); errRead != nil {
		t.Fatalf("failed to read RPOP response: %v", errRead)
	} else if string(item) != "a" {
		t.Fatalf("unexpected RPOP item: %q", string(item))
	}

	if errWrite := writeTestRESPCommand(conn, "LPOP", "usage"); errWrite != nil {
		t.Fatalf("failed to write LPOP command: %v", errWrite)
	}
	if item, errRead := readTestRESPBulkString(reader); errRead != nil {
		t.Fatalf("failed to read LPOP response: %v", errRead)
	} else if string(item) != "b" {
		t.Fatalf("unexpected LPOP item: %q", string(item))
	}

	if errWrite := writeTestRESPCommand(conn, "RPOP", "usage", "10"); errWrite != nil {
		t.Fatalf("failed to write RPOP count command: %v", errWrite)
	}
	items, errItems := readRESPArrayOfBulkStrings(reader)
	if errItems != nil {
		t.Fatalf("failed to read RPOP count response: %v", errItems)
	}
	if len(items) != 1 || string(items[0]) != "c" {
		t.Fatalf("unexpected RPOP count items: %#v", items)
	}

	if errWrite := writeTestRESPCommand(conn, "LPOP", "usage"); errWrite != nil {
		t.Fatalf("failed to write LPOP empty command: %v", errWrite)
	}
	item, errItem := readTestRESPBulkString(reader)
	if errItem != nil {
		t.Fatalf("failed to read LPOP empty response: %v", errItem)
	}
	if item != nil {
		t.Fatalf("expected nil bulk string for empty queue, got %q", string(item))
	}

	if errWrite := writeTestRESPCommand(conn, "RPOP", "usage", "2"); errWrite != nil {
		t.Fatalf("failed to write RPOP empty count command: %v", errWrite)
	}
	emptyItems, errEmpty := readRESPArrayOfBulkStrings(reader)
	if errEmpty != nil {
		t.Fatalf("failed to read RPOP empty count response: %v", errEmpty)
	}
	if len(emptyItems) != 0 {
		t.Fatalf("expected empty array for empty queue with count, got %#v", emptyItems)
	}

	if errWrite := writeTestRESPCommand(conn, "RPOP", "errors", "2"); errWrite != nil {
		t.Fatalf("failed to write RPOP errors count command: %v", errWrite)
	}
	if msg, errRead := readTestRESPError(reader); errRead != nil {
		t.Fatalf("failed to read RPOP errors response: %v", errRead)
	} else if msg != "ERR unsupported channel 'errors'" {
		t.Fatalf("unexpected RPOP errors response: %q", msg)
	}
}

func TestReadRESPBoundsAndOverflowProtection(t *testing.T) {
	t.Run("bulk string integer overflow and cap exceeded do not panic", func(t *testing.T) {
		for _, tc := range []struct {
			raw         string
			wantErr     string
			overflows32 bool
		}{
			{raw: "9223372036854775806\r\n", wantErr: "bulk string too large", overflows32: true}, // MaxInt64 - 1 (length+2 wraps negative)
			{raw: "9223372036854775807\r\n", wantErr: "bulk string too large", overflows32: true}, // MaxInt64
			{raw: "2147483646\r\n", wantErr: "bulk string too large"},                             // MaxInt32 - 1
			{raw: "2147483647\r\n", wantErr: "bulk string too large"},                             // MaxInt32
			{raw: "2147483648\r\n", wantErr: "bulk string too large", overflows32: true},          // MaxInt32 + 1
			{raw: "-2\r\n", wantErr: "protocol error"},
			{raw: "-9223372036854775808\r\n", wantErr: "protocol error", overflows32: true},
			{raw: strconv.Itoa(maxRESPBulkLen+1) + "\r\n" + strings.Repeat("x", maxRESPBulkLen+4), wantErr: "bulk string too large"},
			{raw: "1073741824\r\n", wantErr: "bulk string too large"}, // 1 GiB
		} {
			r := bufio.NewReader(strings.NewReader(tc.raw))
			_, errRead := readRESPBulkString(r)
			if errRead == nil {
				t.Fatalf("readRESPBulkString(%q) succeeded, want error %q", tc.raw[:min(len(tc.raw), 32)], tc.wantErr)
			}
			if errors.Is(errRead, io.EOF) || errors.Is(errRead, io.ErrUnexpectedEOF) {
				t.Fatalf("readRESPBulkString(%q) returned EOF %v, want explicit bounds error %q", tc.raw[:min(len(tc.raw), 32)], errRead, tc.wantErr)
			}
			wantErr := tc.wantErr
			if strconv.IntSize == 32 && tc.overflows32 {
				wantErr = "protocol error"
			}
			if !strings.Contains(errRead.Error(), wantErr) {
				t.Fatalf("readRESPBulkString(%q) error = %v, want %q", tc.raw[:min(len(tc.raw), 32)], errRead, wantErr)
			}
		}
	})

	t.Run("bulk string at exact maxRESPBulkLen succeeds", func(t *testing.T) {
		payload := strings.Repeat("Z", maxRESPBulkLen)
		raw := strconv.Itoa(maxRESPBulkLen) + "\r\n" + payload + "\r\n"
		r := bufio.NewReader(strings.NewReader(raw))
		got, errRead := readRESPBulkString(r)
		if errRead != nil {
			t.Fatalf("readRESPBulkString(maxRESPBulkLen) error = %v, want nil", errRead)
		}
		if len(got) != maxRESPBulkLen {
			t.Fatalf("len(readRESPBulkString(maxRESPBulkLen)) = %d, want %d", len(got), maxRESPBulkLen)
		}
	})

	t.Run("nil bulk string returns empty without error", func(t *testing.T) {
		r := bufio.NewReader(strings.NewReader("-1\r\n"))
		got, errRead := readRESPBulkString(r)
		if errRead != nil {
			t.Fatalf("readRESPBulkString(-1) error = %v, want nil", errRead)
		}
		if got != "" {
			t.Fatalf("readRESPBulkString(-1) = %q, want empty", got)
		}
	})

	t.Run("array count overflow and cap exceeded do not panic", func(t *testing.T) {
		for _, raw := range []string{
			"*9223372036854775807\r\n$1\r\na\r\n",
			"*9223372036854775806\r\n$1\r\na\r\n",
			"*2147483647\r\n$1\r\na\r\n",
			"*" + strconv.Itoa(maxRESPArrayLen+1) + "\r\n$1\r\na\r\n",
			"*-1\r\n$1\r\na\r\n",
		} {
			r := bufio.NewReader(strings.NewReader(raw))
			_, errRead := readRESPArray(r)
			if errRead == nil {
				t.Fatalf("readRESPArray(%q) succeeded, want error", raw)
			}
			if errors.Is(errRead, io.EOF) || errors.Is(errRead, io.ErrUnexpectedEOF) {
				t.Fatalf("readRESPArray(%q) returned EOF %v, want protocol error", raw, errRead)
			}
			if !strings.Contains(errRead.Error(), "protocol error") {
				t.Fatalf("readRESPArray(%q) error = %v, want protocol error", raw, errRead)
			}
		}
	})

	t.Run("array at exact maxRESPArrayLen succeeds", func(t *testing.T) {
		var buf bytes.Buffer
		fmt.Fprintf(&buf, "*%d\r\n", maxRESPArrayLen)
		for i := 0; i < maxRESPArrayLen; i++ {
			buf.WriteString("$1\r\nx\r\n")
		}
		args, errRead := readRESPArray(bufio.NewReader(&buf))
		if errRead != nil {
			t.Fatalf("readRESPArray(maxRESPArrayLen) error = %v, want nil", errRead)
		}
		if len(args) != maxRESPArrayLen {
			t.Fatalf("len(args) = %d, want %d", len(args), maxRESPArrayLen)
		}
	})

	t.Run("whole frame cumulative byte budget rejects multi-element frame exceeding maxRESPFrameBytes", func(t *testing.T) {
		chunkLen := (maxRESPFrameBytes / 2) + 1
		chunk := strings.Repeat("M", chunkLen)
		raw := fmt.Sprintf("*2\r\n$%d\r\n%s\r\n$%d\r\n%s\r\n", chunkLen, chunk, chunkLen, chunk)
		_, errRead := readRESPArray(bufio.NewReader(strings.NewReader(raw)))
		if errRead == nil {
			t.Fatal("readRESPArray() succeeded when cumulative bytes exceeded maxRESPFrameBytes, want error")
		}
		if errors.Is(errRead, io.EOF) || errors.Is(errRead, io.ErrUnexpectedEOF) {
			t.Fatalf("readRESPArray() returned EOF %v, want bulk string too large", errRead)
		}
		if !strings.Contains(errRead.Error(), "bulk string too large") {
			t.Fatalf("readRESPArray() error = %v, want bulk string too large", errRead)
		}
	})

	t.Run("unbounded line without newline is rejected with protocol error before EOF", func(t *testing.T) {
		oversized := strings.Repeat("A", maxRESPLineLen+4096)
		r := bufio.NewReader(strings.NewReader(oversized))
		_, errRead := readRESPLine(r)
		if errRead == nil {
			t.Fatal("readRESPLine() succeeded on oversized line without newline, want error")
		}
		if errors.Is(errRead, io.EOF) || errors.Is(errRead, io.ErrUnexpectedEOF) {
			t.Fatalf("readRESPLine() returned EOF %v, want protocol error", errRead)
		}
		if !strings.Contains(errRead.Error(), "protocol error") {
			t.Fatalf("readRESPLine() error = %v, want protocol error", errRead)
		}
	})

	t.Run("line at maxRESPLineLen succeeds and maxRESPLineLen+1 fails", func(t *testing.T) {
		exact := strings.Repeat("B", maxRESPLineLen) + "\r\n"
		rExact := bufio.NewReader(strings.NewReader(exact))
		got, errExact := readRESPLine(rExact)
		if errExact != nil {
			t.Fatalf("readRESPLine(exact) error = %v, want nil", errExact)
		}
		if len(got) != maxRESPLineLen {
			t.Fatalf("len(readRESPLine(exact)) = %d, want %d", len(got), maxRESPLineLen)
		}

		for _, terminator := range []string{"\r\n", "\n"} {
			tooLong := strings.Repeat("B", maxRESPLineLen+1) + terminator
			rTooLong := bufio.NewReader(strings.NewReader(tooLong))
			_, errTooLong := readRESPLine(rTooLong)
			if errTooLong == nil {
				t.Fatalf("readRESPLine(maxRESPLineLen+1, %q) succeeded, want error", terminator)
			}
			if errors.Is(errTooLong, io.EOF) || errors.Is(errTooLong, io.ErrUnexpectedEOF) {
				t.Fatalf("readRESPLine(maxRESPLineLen+1, %q) returned EOF %v, want protocol error", terminator, errTooLong)
			}
		}
	})

	t.Run("small reader buffer handles CRLF across ErrBufferFull fragments", func(t *testing.T) {
		want := strings.Repeat("C", 15) // 15 bytes + '\r' fills 16-byte buffer, '\n' is in next fragment
		r := bufio.NewReaderSize(strings.NewReader(want+"\r\n"), 16)
		got, errRead := readRESPLine(r)
		if errRead != nil {
			t.Fatalf("readRESPLine(split CRLF) error = %v, want nil", errRead)
		}
		if got != want {
			t.Fatalf("readRESPLine(split CRLF) = %q, want %q", got, want)
		}
	})
}

func TestRedisProtocol_MalformedAndOverflowFramesDoNotCrashServer(t *testing.T) {
	const managementPassword = "test-management-password"

	t.Setenv("MANAGEMENT_PASSWORD", managementPassword)
	redisqueue.SetEnabled(false)
	t.Cleanup(func() { redisqueue.SetEnabled(false) })

	server := newTestServer(t)
	addr, stop := startRedisMuxListener(t, server)
	t.Cleanup(stop)

	frames := []struct {
		name  string
		bytes []byte
	}{
		{
			name:  "bulk length int64 overflow",
			bytes: []byte("*1\r\n$9223372036854775806\r\n"),
		},
		{
			name:  "array count int64 overflow",
			bytes: []byte("*9223372036854775807\r\n"),
		},
		{
			name:  "huge bulk length exceeding cap",
			bytes: []byte("*1\r\n$1073741824\r\n"),
		},
		{
			name:  "unbounded line without CRLF",
			bytes: append([]byte("*1\r\n+"), bytes.Repeat([]byte("A"), maxRESPLineLen+8192)...),
		},
	}

	for _, tc := range frames {
		t.Run(tc.name, func(t *testing.T) {
			conn, errDial := net.DialTimeout("tcp", addr, time.Second)
			if errDial != nil {
				t.Fatalf("failed to dial redis listener: %v", errDial)
			}
			defer func() {
				if errClose := conn.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
					t.Errorf("close redis test connection: %v", errClose)
				}
			}()

			if errDeadline := conn.SetDeadline(time.Now().Add(2 * time.Second)); errDeadline != nil {
				t.Fatalf("failed to set connection deadline: %v", errDeadline)
			}
			if _, errWrite := conn.Write(tc.bytes); errWrite != nil && !errors.Is(errWrite, net.ErrClosed) {
				// For oversized frames the server may reject and close before all bytes are written.
				t.Logf("write returned early after server rejection: %v", errWrite)
			}

			msg, errRead := readTestRESPError(bufio.NewReader(conn))
			if errRead != nil {
				t.Fatalf("expected protocol error reply, got read error: %v", errRead)
			}
			if !strings.HasPrefix(msg, "ERR ") {
				t.Fatalf("unexpected error reply: %q", msg)
			}
		})
	}

	// Verify the server is still alive and serves normal RESP commands afterwards.
	conn, errDial := net.DialTimeout("tcp", addr, time.Second)
	if errDial != nil {
		t.Fatalf("failed to dial redis listener after malformed frames: %v", errDial)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
			t.Errorf("close redis verification connection: %v", errClose)
		}
	}()

	reader := bufio.NewReader(conn)
	if errDeadline := conn.SetDeadline(time.Now().Add(2 * time.Second)); errDeadline != nil {
		t.Fatalf("failed to set verification connection deadline: %v", errDeadline)
	}
	if errWrite := writeTestRESPCommand(conn, "AUTH", managementPassword); errWrite != nil {
		t.Fatalf("failed to write AUTH command: %v", errWrite)
	}
	if msg, errRead := readTestRESPSimpleString(reader); errRead != nil {
		t.Fatalf("failed to read AUTH response: %v", errRead)
	} else if msg != "OK" {
		t.Fatalf("unexpected AUTH response: %q", msg)
	}
}
