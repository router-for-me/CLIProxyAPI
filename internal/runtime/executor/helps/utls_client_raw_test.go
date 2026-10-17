package helps

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	gotls "crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"
)

// TestRawClaudeCodeTransportKeepsTheClientsEncoding sends a request with no Accept-Encoding
// through the raw Claude Code transport to a TLS server that answers with gzip anyway. The
// transport must not add an Accept-Encoding of its own, and must hand back the gzip bytes and
// Content-Encoding exactly as sent.
func TestRawClaudeCodeTransportKeepsTheClientsEncoding(t *testing.T) {
	certificate := newResumptionTestCertificateAt(t, time.Now())
	roots := x509.NewCertPool()
	roots.AddCert(certificate.Leaf)

	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, errWrite := writer.Write([]byte(`{"type":"message"}`)); errWrite != nil {
		t.Fatal(errWrite)
	}
	if errClose := writer.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	body := compressed.Bytes()

	listener, errListen := gotls.Listen("tcp", "127.0.0.1:0", &gotls.Config{
		Certificates: []gotls.Certificate{certificate},
		MinVersion:   gotls.VersionTLS12,
		NextProtos:   []string{"http/1.1"},
	})
	if errListen != nil {
		t.Fatal(errListen)
	}
	t.Cleanup(func() { _ = listener.Close() })
	sentEncoding := make(chan []string, 1)
	serverDone := make(chan error, 1)
	go func() {
		conn, errAccept := listener.Accept()
		if errAccept != nil {
			serverDone <- errAccept
			return
		}
		defer func() { _ = conn.Close() }()
		request, errRead := http.ReadRequest(bufio.NewReader(conn))
		if errRead != nil {
			serverDone <- fmt.Errorf("read request: %w", errRead)
			return
		}
		sentEncoding <- request.Header.Values("Accept-Encoding")
		head := "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Encoding: gzip\r\nContent-Length: " +
			strconv.Itoa(len(body)) + "\r\nConnection: close\r\n\r\n"
		if _, errWrite := conn.Write(append([]byte(head), body...)); errWrite != nil {
			serverDone <- fmt.Errorf("write response: %w", errWrite)
			return
		}
		serverDone <- nil
	}()

	address := listener.Addr().String()
	dialer := contextDialerFunc(func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, address)
	})
	transport := rawClaudeCodeTransport(newClaudeCodeRoundTripperForDialerAndRoots(dialer, roots))
	t.Cleanup(transport.CloseIdleConnections)

	request, errRequest := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", bytes.NewReader([]byte(`{}`)))
	if errRequest != nil {
		t.Fatal(errRequest)
	}
	response, errRoundTrip := transport.RoundTrip(request)
	if errRoundTrip != nil {
		t.Fatalf("RoundTrip() error = %v", errRoundTrip)
	}
	got, errRead := io.ReadAll(response.Body)
	if errClose := response.Body.Close(); errClose != nil {
		t.Errorf("close response body: %v", errClose)
	}
	if errRead != nil {
		t.Fatal(errRead)
	}
	if errServer := <-serverDone; errServer != nil {
		t.Fatal(errServer)
	}

	if encoding := <-sentEncoding; len(encoding) != 0 {
		t.Errorf("upstream saw Accept-Encoding %q, want none (the client sent none)", encoding)
	}
	if encoding := response.Header.Get("Content-Encoding"); encoding != "gzip" {
		t.Errorf("Content-Encoding = %q, want gzip as sent", encoding)
	}
	if response.Uncompressed {
		t.Error("response was decoded by the transport")
	}
	if !bytes.Equal(got, body) {
		t.Errorf("body = %x, want the gzip bytes as sent %x", got, body)
	}
}
