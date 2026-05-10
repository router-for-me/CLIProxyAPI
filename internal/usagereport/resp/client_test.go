package resp

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func TestPopAuthenticatesAndReadsArray(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = listener.Close() }()

	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer func() { _ = conn.Close() }()
		reader := bufio.NewReader(conn)
		first, _ := reader.ReadString('\n')
		if !strings.HasPrefix(first, "*2") {
			done <- errUnexpected(first)
			return
		}
		for i := 0; i < 4; i++ {
			_, _ = reader.ReadString('\n')
		}
		_, _ = conn.Write([]byte("+OK\r\n"))
		second, _ := reader.ReadString('\n')
		if !strings.HasPrefix(second, "*3") {
			done <- errUnexpected(second)
			return
		}
		_, _ = conn.Write([]byte("*2\r\n$7\r\n{\"a\":1}\r\n$7\r\n{\"b\":2}\r\n"))
		done <- nil
	}()

	client := New(Options{Address: listener.Addr().String(), Password: "secret", QueueKey: "queue"})
	items, err := client.Pop(context.Background(), 2)
	if err != nil {
		t.Fatalf("Pop: %v", err)
	}
	if len(items) != 2 || string(items[0]) != `{"a":1}` || string(items[1]) != `{"b":2}` {
		t.Fatalf("bad items: %q", items)
	}
	if err := <-done; err != nil {
		t.Fatalf("server: %v", err)
	}
}

func TestPopTimesOutWhenServerDoesNotRespond(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = listener.Close() }()

	// Accept the connection and hold it open for 10s without sending any response.
	// This simulates a server that accepts but never answers AUTH/RPOP.
	go func() {
		conn, _ := listener.Accept()
		if conn != nil {
			defer func() { _ = conn.Close() }()
			time.Sleep(10 * time.Second)
		}
	}()

	client := New(Options{Address: listener.Addr().String(), Password: "secret", QueueKey: "queue"})
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err = client.Pop(ctx, 1)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error when server does not respond, got nil")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("Pop blocked for %v, expected to return within 2s when context times out", elapsed)
	}
}

type errUnexpected string

func (e errUnexpected) Error() string { return "unexpected line: " + string(e) }
