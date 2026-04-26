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

	"github.com/router-for-me/CLIProxyAPI/v6/internal/redisqueue"
)

type remoteAddrConn struct {
	net.Conn
	remoteAddr net.Addr
}

func (c *remoteAddrConn) RemoteAddr() net.Addr {
	if c == nil {
		return nil
	}
	return c.remoteAddr
}

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

func readTestRESPSimpleString(r *bufio.Reader) (string, error) {
	prefix, err := r.ReadByte()
	if err != nil {
		return "", err
	}
	if prefix != '+' {
		return "", fmt.Errorf("expected simple string prefix '+', got %q", prefix)
	}
	return readTestRESPLine(r)
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

func readTestRESPBulkString(r *bufio.Reader) ([]byte, error) {
	prefix, err := r.ReadByte()
	if err != nil {
		return nil, err
	}
	if prefix != '$' {
		return nil, fmt.Errorf("expected bulk string prefix '$', got %q", prefix)
	}

	line, err := readTestRESPLine(r)
	if err != nil {
		return nil, err
	}
	length, err := strconv.Atoi(line)
	if err != nil {
		return nil, fmt.Errorf("invalid bulk string length %q: %v", line, err)
	}
	if length == -1 {
		return nil, nil
	}
	if length < -1 {
		return nil, fmt.Errorf("invalid bulk string length %d", length)
	}

	payload := make([]byte, length+2)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	if payload[length] != '\r' || payload[length+1] != '\n' {
		return nil, fmt.Errorf("invalid bulk string terminator")
	}
	return payload[:length], nil
}

func readRESPArrayOfBulkStrings(r *bufio.Reader) ([][]byte, error) {
	prefix, err := r.ReadByte()
	if err != nil {
		return nil, err
	}
	if prefix != '*' {
		return nil, fmt.Errorf("expected array prefix '*', got %q", prefix)
	}

	line, err := readTestRESPLine(r)
	if err != nil {
		return nil, err
	}
	count, err := strconv.Atoi(line)
	if err != nil {
		return nil, fmt.Errorf("invalid array length %q: %v", line, err)
	}
	if count < 0 {
		return nil, fmt.Errorf("invalid array length %d", count)
	}

	out := make([][]byte, 0, count)
	for i := 0; i < count; i++ {
		item, err := readTestRESPBulkString(r)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
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

	if errWrite := writeTestRESPCommand(conn, "AUTH", "test-key"); errWrite != nil {
		t.Fatalf("failed to write AUTH command: %v", errWrite)
	}
	if msg, err := readTestRESPError(reader); err != nil {
		t.Fatalf("failed to read AUTH error: %v", err)
	} else if msg != "ERR invalid management key" {
		t.Fatalf("unexpected AUTH error: %q", msg)
	}

	if errWrite := writeTestRESPCommand(conn, "LPOP", "queue"); errWrite != nil {
		t.Fatalf("failed to write LPOP command: %v", errWrite)
	}
	if msg, err := readTestRESPError(reader); err != nil {
		t.Fatalf("failed to read LPOP NOAUTH error: %v", err)
	} else if msg != "NOAUTH Authentication required." {
		t.Fatalf("unexpected LPOP NOAUTH error: %q", msg)
	}

	if errWrite := writeTestRESPCommand(conn, "AUTH", managementPassword); errWrite != nil {
		t.Fatalf("failed to write AUTH command: %v", errWrite)
	}
	if msg, err := readTestRESPSimpleString(reader); err != nil {
		t.Fatalf("failed to read AUTH response: %v", err)
	} else if msg != "OK" {
		t.Fatalf("unexpected AUTH response: %q", msg)
	}

	if !redisqueue.Enabled() {
		t.Fatalf("expected redisqueue to be enabled")
	}
	redisqueue.Enqueue([]byte("a"))
	redisqueue.Enqueue([]byte("b"))
	redisqueue.Enqueue([]byte("c"))

	if errWrite := writeTestRESPCommand(conn, "RPOP", "queue"); errWrite != nil {
		t.Fatalf("failed to write RPOP command: %v", errWrite)
	}
	if item, err := readTestRESPBulkString(reader); err != nil {
		t.Fatalf("failed to read RPOP response: %v", err)
	} else if string(item) != "a" {
		t.Fatalf("unexpected RPOP item: %q", string(item))
	}

	if errWrite := writeTestRESPCommand(conn, "LPOP", "queue"); errWrite != nil {
		t.Fatalf("failed to write LPOP command: %v", errWrite)
	}
	if item, err := readTestRESPBulkString(reader); err != nil {
		t.Fatalf("failed to read LPOP response: %v", err)
	} else if string(item) != "b" {
		t.Fatalf("unexpected LPOP item: %q", string(item))
	}

	if errWrite := writeTestRESPCommand(conn, "RPOP", "queue", "10"); errWrite != nil {
		t.Fatalf("failed to write RPOP count command: %v", errWrite)
	}
	items, errItems := readRESPArrayOfBulkStrings(reader)
	if errItems != nil {
		t.Fatalf("failed to read RPOP count response: %v", errItems)
	}
	if len(items) != 1 || string(items[0]) != "c" {
		t.Fatalf("unexpected RPOP count items: %#v", items)
	}

	if errWrite := writeTestRESPCommand(conn, "LPOP", "queue"); errWrite != nil {
		t.Fatalf("failed to write LPOP empty command: %v", errWrite)
	}
	item, errItem := readTestRESPBulkString(reader)
	if errItem != nil {
		t.Fatalf("failed to read LPOP empty response: %v", errItem)
	}
	if item != nil {
		t.Fatalf("expected nil bulk string for empty queue, got %q", string(item))
	}

	if errWrite := writeTestRESPCommand(conn, "RPOP", "queue", "2"); errWrite != nil {
		t.Fatalf("failed to write RPOP empty count command: %v", errWrite)
	}
	emptyItems, errEmpty := readRESPArrayOfBulkStrings(reader)
	if errEmpty != nil {
		t.Fatalf("failed to read RPOP empty count response: %v", errEmpty)
	}
	if len(emptyItems) != 0 {
		t.Fatalf("expected empty array for empty queue with count, got %#v", emptyItems)
	}
}

func TestRedisProtocol_IPBan_MirrorsManagementPolicy(t *testing.T) {
	const managementPassword = "test-management-password"

	t.Setenv("MANAGEMENT_PASSWORD", managementPassword)
	redisqueue.SetEnabled(false)
	t.Cleanup(func() { redisqueue.SetEnabled(false) })

	server := newTestServer(t)
	if !server.managementRoutesEnabled.Load() {
		t.Fatalf("expected managementRoutesEnabled to be true")
	}

	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() { _ = clientConn.Close() })
	t.Cleanup(func() { _ = serverConn.Close() })

	fakeRemote := &net.TCPAddr{
		IP:   net.ParseIP("1.2.3.4"),
		Port: 1234,
	}
	wrappedConn := &remoteAddrConn{Conn: serverConn, remoteAddr: fakeRemote}

	go server.handleRedisConnection(wrappedConn, bufio.NewReader(wrappedConn))

	reader := bufio.NewReader(clientConn)
	_ = clientConn.SetDeadline(time.Now().Add(5 * time.Second))

	for i := 0; i < 5; i++ {
		if errWrite := writeTestRESPCommand(clientConn, "LPOP", "queue"); errWrite != nil {
			t.Fatalf("failed to write LPOP command: %v", errWrite)
		}
		if msg, err := readTestRESPError(reader); err != nil {
			t.Fatalf("failed to read LPOP NOAUTH error: %v", err)
		} else if msg != "NOAUTH Authentication required." {
			t.Fatalf("unexpected LPOP NOAUTH error at attempt %d: %q", i+1, msg)
		}
	}

	if errWrite := writeTestRESPCommand(clientConn, "LPOP", "queue"); errWrite != nil {
		t.Fatalf("failed to write LPOP command after failures: %v", errWrite)
	}
	msg, err := readTestRESPError(reader)
	if err != nil {
		t.Fatalf("failed to read LPOP banned error: %v", err)
	}
	if !strings.HasPrefix(msg, "ERR IP banned due to too many failed attempts. Try again in") {
		t.Fatalf("unexpected LPOP banned error: %q", msg)
	}
}

func TestRedisProtocol_AUTH_IPBan_BlocksCorrectPasswordDuringBan(t *testing.T) {
	const managementPassword = "test-management-password"

	t.Setenv("MANAGEMENT_PASSWORD", managementPassword)
	redisqueue.SetEnabled(false)
	t.Cleanup(func() { redisqueue.SetEnabled(false) })

	server := newTestServer(t)
	if !server.managementRoutesEnabled.Load() {
		t.Fatalf("expected managementRoutesEnabled to be true")
	}

	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() { _ = clientConn.Close() })
	t.Cleanup(func() { _ = serverConn.Close() })

	fakeRemote := &net.TCPAddr{
		IP:   net.ParseIP("1.2.3.4"),
		Port: 1234,
	}
	wrappedConn := &remoteAddrConn{Conn: serverConn, remoteAddr: fakeRemote}

	go server.handleRedisConnection(wrappedConn, bufio.NewReader(wrappedConn))

	reader := bufio.NewReader(clientConn)
	_ = clientConn.SetDeadline(time.Now().Add(5 * time.Second))

	for i := 0; i < 5; i++ {
		if errWrite := writeTestRESPCommand(clientConn, "AUTH", "wrong-password"); errWrite != nil {
			t.Fatalf("failed to write AUTH command: %v", errWrite)
		}
		if msg, err := readTestRESPError(reader); err != nil {
			t.Fatalf("failed to read AUTH error: %v", err)
		} else if msg != "ERR invalid management key" {
			t.Fatalf("unexpected AUTH error at attempt %d: %q", i+1, msg)
		}
	}

	for i := 0; i < 2; i++ {
		if errWrite := writeTestRESPCommand(clientConn, "AUTH", "wrong-password"); errWrite != nil {
			t.Fatalf("failed to write AUTH command after failures: %v", errWrite)
		}
		msg, err := readTestRESPError(reader)
		if err != nil {
			t.Fatalf("failed to read AUTH banned error: %v", err)
		}
		if !strings.HasPrefix(msg, "ERR IP banned due to too many failed attempts. Try again in") {
			t.Fatalf("unexpected AUTH banned error at attempt %d: %q", i+6, msg)
		}
	}

	if errWrite := writeTestRESPCommand(clientConn, "AUTH", managementPassword); errWrite != nil {
		t.Fatalf("failed to write AUTH command with correct password: %v", errWrite)
	}
	msg, err := readTestRESPError(reader)
	if err != nil {
		t.Fatalf("failed to read AUTH banned error for correct password: %v", err)
	}
	if !strings.HasPrefix(msg, "ERR IP banned due to too many failed attempts. Try again in") {
		t.Fatalf("unexpected AUTH banned error for correct password: %q", msg)
	}
}

func TestRedisProtocol_LOCALHOST_AUTH_IPBan_BlocksCorrectPasswordDuringBan(t *testing.T) {
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

	for i := 0; i < 5; i++ {
		if errWrite := writeTestRESPCommand(conn, "AUTH", "wrong-password"); errWrite != nil {
			t.Fatalf("failed to write AUTH command: %v", errWrite)
		}
		if msg, err := readTestRESPError(reader); err != nil {
			t.Fatalf("failed to read AUTH error: %v", err)
		} else if msg != "ERR invalid management key" {
			t.Fatalf("unexpected AUTH error at attempt %d: %q", i+1, msg)
		}
	}

	if errWrite := writeTestRESPCommand(conn, "AUTH", managementPassword); errWrite != nil {
		t.Fatalf("failed to write AUTH command with correct password: %v", errWrite)
	}
	msg, err := readTestRESPError(reader)
	if err != nil {
		t.Fatalf("failed to read AUTH banned error for correct password: %v", err)
	}
	if !strings.HasPrefix(msg, "ERR IP banned due to too many failed attempts. Try again in") {
		t.Fatalf("unexpected AUTH banned error for correct password: %q", msg)
	}
}
