package httpwire

import (
	"bytes"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestOrderedRequestConnReordersKeepAliveRequestsWithoutChangingBodies(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	t.Cleanup(func() {
		if errClose := client.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
			t.Errorf("close client connection: %v", errClose)
		}
		if errClose := server.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
			t.Errorf("close server connection: %v", errClose)
		}
	})

	conn := NewOrderedRequestConn(client, func(method, target string) []string {
		if method == "POST" && target == "/v1/messages?beta=true" {
			return []string{"Accept", "Authorization", "Content-Type", "User-Agent", "Connection", "Host", "Accept-Encoding", "Content-Length"}
		}
		return []string{"Accept", "Host", "Connection"}
	})

	firstInput := "POST /v1/messages?beta=true HTTP/1.1\r\nHost: api.anthropic.com\r\nUser-Agent: claude-cli/2.1.220 (external, cli)\r\nContent-Length: 7\r\nAccept: application/json\r\nX-Unknown: keep\r\nAuthorization: Bearer placeholder\r\nContent-Type: application/json\r\nConnection: keep-alive\r\nAccept-Encoding: gzip, deflate, br, zstd\r\n\r\n{\"a\":1}"
	secondInput := "GET /api/oauth/profile HTTP/1.1\r\nConnection: close\r\nHost: api.anthropic.com\r\nAccept: application/json\r\n\r\n"
	want := "POST /v1/messages?beta=true HTTP/1.1\r\nAccept: application/json\r\nAuthorization: Bearer placeholder\r\nContent-Type: application/json\r\nUser-Agent: claude-cli/2.1.220 (external, cli)\r\nConnection: keep-alive\r\nHost: api.anthropic.com\r\nAccept-Encoding: gzip, deflate, br, zstd\r\nContent-Length: 7\r\nX-Unknown: keep\r\n\r\n{\"a\":1}GET /api/oauth/profile HTTP/1.1\r\nAccept: application/json\r\nHost: api.anthropic.com\r\nConnection: close\r\n\r\n"

	readDone := make(chan []byte, 1)
	go func() {
		if errDeadline := server.SetReadDeadline(time.Now().Add(5 * time.Second)); errDeadline != nil {
			readDone <- nil
			return
		}
		got := make([]byte, len(want))
		if _, errRead := io.ReadFull(server, got); errRead != nil {
			readDone <- nil
			return
		}
		readDone <- got
	}()

	parts := [][]byte{
		[]byte(firstInput[:29]),
		[]byte(firstInput[29 : len(firstInput)-3]),
		[]byte(firstInput[len(firstInput)-3:] + secondInput[:17]),
		[]byte(secondInput[17:]),
	}
	for _, part := range parts {
		written, errWrite := conn.Write(part)
		if errWrite != nil {
			t.Fatalf("write request bytes: %v", errWrite)
		}
		if written != len(part) {
			t.Fatalf("write length = %d, want %d", written, len(part))
		}
	}

	select {
	case got := <-readDone:
		if !bytes.Equal(got, []byte(want)) {
			t.Fatalf("wire bytes differ\n got: %q\nwant: %q", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out reading ordered request bytes")
	}
}

func TestOrderedRequestConnPreservesChunkedBodyAndReordersNextRequest(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	conn := NewOrderedRequestConn(client, func(_, _ string) []string { return []string{"Host", "Transfer-Encoding"} })
	first := "POST /upload HTTP/1.1\r\nTransfer-Encoding: chunked\r\nHost: example.com\r\n\r\n4\r\ntest\r\n0\r\nX-Trailer: done\r\n\r\n"
	second := "GET /next HTTP/1.1\r\nTransfer-Encoding: identity\r\nHost: example.com\r\n\r\n"
	input := []byte(first + second)
	want := []byte("POST /upload HTTP/1.1\r\nHost: example.com\r\nTransfer-Encoding: chunked\r\n\r\n4\r\ntest\r\n0\r\nX-Trailer: done\r\n\r\nGET /next HTTP/1.1\r\nHost: example.com\r\nTransfer-Encoding: identity\r\n\r\n")

	readDone := make(chan []byte, 1)
	go func() {
		got := make([]byte, len(want))
		_, _ = io.ReadFull(server, got)
		readDone <- got
	}()
	for index := range input {
		part := input[index : index+1]
		written, errWrite := conn.Write(part)
		if errWrite != nil {
			t.Fatal(errWrite)
		}
		if written != len(part) {
			t.Fatalf("write length = %d, want %d", written, len(part))
		}
	}
	if got := <-readDone; !bytes.Equal(got, want) {
		t.Fatalf("chunked wire bytes differ\n got: %q\nwant: %q", got, want)
	}
}

type partialErrorConn struct {
	bytes.Buffer
	failLimit int
	failErr   error
}

func (conn *partialErrorConn) Write(data []byte) (int, error) {
	if conn.failErr == nil {
		return conn.Buffer.Write(data)
	}
	written := min(conn.failLimit, len(data))
	_, _ = conn.Buffer.Write(data[:written])
	return written, conn.failErr
}

func (*partialErrorConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (*partialErrorConn) Close() error                     { return nil }
func (*partialErrorConn) LocalAddr() net.Addr              { return nil }
func (*partialErrorConn) RemoteAddr() net.Addr             { return nil }
func (*partialErrorConn) SetDeadline(time.Time) error      { return nil }
func (*partialErrorConn) SetReadDeadline(time.Time) error  { return nil }
func (*partialErrorConn) SetWriteDeadline(time.Time) error { return nil }

func TestOrderedRequestConnReportsPartialBodyWrite(t *testing.T) {
	underlying := &partialErrorConn{}
	conn := NewOrderedRequestConn(underlying, func(_, _ string) []string { return []string{"Host", "Content-Length"} })
	header := []byte("POST /upload HTTP/1.1\r\nContent-Length: 5\r\nHost: example.com\r\n\r\n")
	if written, errWrite := conn.Write(header); errWrite != nil || written != len(header) {
		t.Fatalf("header write = %d, %v", written, errWrite)
	}

	underlying.failLimit = 2
	injectedErr := errors.New("injected partial write")
	underlying.failErr = injectedErr
	written, errWrite := conn.Write([]byte("hello"))
	if !errors.Is(errWrite, injectedErr) {
		t.Fatalf("body write error = %v, want injected error", errWrite)
	}
	if written != 2 {
		t.Fatalf("body write length = %d, want underlying partial count 2", written)
	}
	if remaining := conn.(*orderedRequestConn).bodyRemaining; remaining != 3 {
		t.Fatalf("bodyRemaining = %d, want 3 after confirmed partial write", remaining)
	}

	underlying.failErr = nil
	if written, errWrite = conn.Write([]byte("llo")); errWrite != nil || written != 3 {
		t.Fatalf("retried body write = %d, %v", written, errWrite)
	}
	second := []byte("GET /next HTTP/1.1\r\nContent-Length: 0\r\nHost: example.com\r\n\r\n")
	if written, errWrite = conn.Write(second); errWrite != nil || written != len(second) {
		t.Fatalf("next request write = %d, %v", written, errWrite)
	}
	want := "POST /upload HTTP/1.1\r\nHost: example.com\r\nContent-Length: 5\r\n\r\nhelloGET /next HTTP/1.1\r\nHost: example.com\r\nContent-Length: 0\r\n\r\n"
	if got := underlying.String(); got != want {
		t.Fatalf("wire bytes differ after retry\n got: %q\nwant: %q", got, want)
	}
}

func TestOrderedRequestConnTracksOnlyWrittenChunkBytesAfterPartialError(t *testing.T) {
	underlying := &partialErrorConn{}
	conn := NewOrderedRequestConn(underlying, func(_, _ string) []string { return []string{"Host", "Transfer-Encoding"} })
	header := []byte("POST /upload HTTP/1.1\r\nTransfer-Encoding: chunked\r\nHost: example.com\r\n\r\n")
	if written, errWrite := conn.Write(header); errWrite != nil || written != len(header) {
		t.Fatalf("header write = %d, %v", written, errWrite)
	}

	chunkedBody := []byte("4\r\ntest\r\n0\r\nX-Trailer: done\r\n\r\n")
	underlying.failLimit = 6
	injectedErr := errors.New("injected chunk partial write")
	underlying.failErr = injectedErr
	written, errWrite := conn.Write(chunkedBody)
	if !errors.Is(errWrite, injectedErr) || written != 6 {
		t.Fatalf("chunk write = %d, %v; want 6 and injected error", written, errWrite)
	}

	underlying.failErr = nil
	if retried, errRetry := conn.Write(chunkedBody[written:]); errRetry != nil || retried != len(chunkedBody)-written {
		t.Fatalf("retried chunk write = %d, %v", retried, errRetry)
	}
	second := []byte("GET /next HTTP/1.1\r\nTransfer-Encoding: identity\r\nHost: example.com\r\n\r\n")
	if written, errWrite = conn.Write(second); errWrite != nil || written != len(second) {
		t.Fatalf("next request write = %d, %v", written, errWrite)
	}
	want := "POST /upload HTTP/1.1\r\nHost: example.com\r\nTransfer-Encoding: chunked\r\n\r\n" + string(chunkedBody) +
		"GET /next HTTP/1.1\r\nHost: example.com\r\nTransfer-Encoding: identity\r\n\r\n"
	if got := underlying.String(); got != want {
		t.Fatalf("wire bytes differ after chunk retry\n got: %q\nwant: %q", got, want)
	}
}
