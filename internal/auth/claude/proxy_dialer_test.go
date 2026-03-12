package claude

import (
	"bufio"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/proxy"
)

func TestBuildProxyDialerEmptyUsesDirect(t *testing.T) {
	t.Parallel()

	dialer, err := buildProxyDialer("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dialer != proxy.Direct {
		t.Fatalf("expected proxy.Direct, got %T", dialer)
	}
}

func TestBuildProxyDialerWhitespaceUsesDirect(t *testing.T) {
	t.Parallel()

	dialer, err := buildProxyDialer("   ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dialer != proxy.Direct {
		t.Fatalf("expected proxy.Direct, got %T", dialer)
	}
}

func TestBuildProxyDialerRejectsUnsupportedScheme(t *testing.T) {
	t.Parallel()

	_, err := buildProxyDialer("ftp://proxy.example.com:21")
	if err == nil {
		t.Fatal("expected error for unsupported scheme, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported scheme") {
		t.Fatalf("error = %q, want substring 'unsupported scheme'", err)
	}
}

func TestBuildProxyDialerRejectsMissingHost(t *testing.T) {
	t.Parallel()

	_, err := buildProxyDialer("http://")
	if err == nil {
		t.Fatal("expected error for missing host, got nil")
	}
}

func TestBuildProxyDialerAcceptsSocks5(t *testing.T) {
	t.Parallel()

	dialer, err := buildProxyDialer("socks5://proxy.example.com:1080")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dialer == nil {
		t.Fatal("expected dialer, got nil")
	}
	if dialer == proxy.Direct {
		t.Fatal("expected proxy dialer, got proxy.Direct")
	}
}

func TestBuildProxyDialerAcceptsHTTPSProxy(t *testing.T) {
	t.Parallel()

	dialer, err := buildProxyDialer("https://proxy.example.com:8443")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dialer == nil {
		t.Fatal("expected dialer, got nil")
	}
}

func TestBuildProxyDialerDefaultPort(t *testing.T) {
	t.Parallel()

	dialer, err := buildProxyDialer("http://proxy.example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dialer == nil {
		t.Fatal("expected dialer, got nil")
	}
}

func TestHTTPProxyConnectTunnel(t *testing.T) {
	t.Parallel()

	targetListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen target: %v", err)
	}
	defer func() { _ = targetListener.Close() }()

	targetPayload := make(chan string, 1)
	targetErr := make(chan error, 1)
	go func() {
		conn, errAccept := targetListener.Accept()
		if errAccept != nil {
			targetErr <- errAccept
			return
		}
		defer func() { _ = conn.Close() }()
		buf := make([]byte, 4)
		if _, errRead := io.ReadFull(conn, buf); errRead != nil {
			targetErr <- errRead
			return
		}
		targetPayload <- string(buf)
		_, _ = conn.Write([]byte("pong"))
		targetErr <- nil
	}()

	proxyListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen proxy: %v", err)
	}
	defer func() { _ = proxyListener.Close() }()

	connectLine := make(chan string, 1)
	proxyErr := make(chan error, 1)
	go serveConnectProxy(t, proxyListener, targetListener.Addr().String(), connectLine, proxyErr)

	dialer, err := buildProxyDialer("http://" + proxyListener.Addr().String())
	if err != nil {
		t.Fatalf("buildProxyDialer: %v", err)
	}

	conn, err := dialer.Dial("tcp", targetListener.Addr().String())
	if err != nil {
		t.Fatalf("dial through proxy: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("write through tunnel: %v", err)
	}

	resp := make([]byte, 4)
	if _, err := io.ReadFull(conn, resp); err != nil {
		t.Fatalf("read through tunnel: %v", err)
	}
	if string(resp) != "pong" {
		t.Fatalf("response = %q, want %q", string(resp), "pong")
	}

	wantLine := "CONNECT " + targetListener.Addr().String() + " HTTP/1.1"
	if got := <-connectLine; got != wantLine {
		t.Fatalf("CONNECT request = %q, want %q", got, wantLine)
	}
	if got := <-targetPayload; got != "ping" {
		t.Fatalf("target payload = %q, want %q", got, "ping")
	}
	if err := <-proxyErr; err != nil {
		t.Fatalf("proxy server: %v", err)
	}
	if err := <-targetErr; err != nil {
		t.Fatalf("target server: %v", err)
	}
}

func TestHTTPProxyConnectTunnelWithAuth(t *testing.T) {
	t.Parallel()

	proxyListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = proxyListener.Close() }()

	authHeader := make(chan string, 1)
	proxyErr := make(chan error, 1)
	go func() {
		conn, errAccept := proxyListener.Accept()
		if errAccept != nil {
			proxyErr <- errAccept
			return
		}
		defer func() { _ = conn.Close() }()
		reader := bufio.NewReader(conn)
		_, _ = reader.ReadString('\n')
		var foundAuth string
		for {
			line, errLine := reader.ReadString('\n')
			if errLine != nil {
				proxyErr <- errLine
				return
			}
			if strings.HasPrefix(line, "Proxy-Authorization:") {
				foundAuth = strings.TrimSpace(strings.TrimPrefix(line, "Proxy-Authorization:"))
			}
			if line == "\r\n" {
				break
			}
		}
		authHeader <- foundAuth
		_, _ = io.WriteString(conn, "HTTP/1.1 200 OK\r\n\r\n")
		proxyErr <- nil
	}()

	dialer, err := buildProxyDialer("http://user:pass@" + proxyListener.Addr().String())
	if err != nil {
		t.Fatalf("buildProxyDialer: %v", err)
	}

	conn, err := dialer.Dial("tcp", "example.com:443")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = conn.Close()

	got := <-authHeader
	if got == "" {
		t.Fatal("expected Proxy-Authorization header, got empty")
	}
	if !strings.HasPrefix(got, "Basic ") {
		t.Fatalf("auth = %q, want Basic prefix", got)
	}
	if err := <-proxyErr; err != nil {
		t.Fatalf("proxy: %v", err)
	}
}

func TestHTTPProxyConnectRejectsNon200(t *testing.T) {
	t.Parallel()

	proxyListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = proxyListener.Close() }()

	go func() {
		conn, errAccept := proxyListener.Accept()
		if errAccept != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		reader := bufio.NewReader(conn)
		for {
			line, errLine := reader.ReadString('\n')
			if errLine != nil {
				return
			}
			if line == "\r\n" {
				break
			}
		}
		_, _ = io.WriteString(conn, "HTTP/1.1 407 Proxy Authentication Required\r\nContent-Length: 12\r\n\r\nUnauthorized")
	}()

	dialer, err := buildProxyDialer("http://" + proxyListener.Addr().String())
	if err != nil {
		t.Fatalf("buildProxyDialer: %v", err)
	}

	_, err = dialer.Dial("tcp", "example.com:443")
	if err == nil {
		t.Fatal("expected error for 407, got nil")
	}
	if !strings.Contains(err.Error(), "407") {
		t.Fatalf("error = %q, want substring '407'", err)
	}
}

func serveConnectProxy(t *testing.T, proxyListener net.Listener, targetAddr string, connectLine chan<- string, errCh chan<- error) {
	t.Helper()
	conn, err := proxyListener.Accept()
	if err != nil {
		errCh <- err
		return
	}
	defer func() { _ = conn.Close() }()

	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil {
		errCh <- err
		return
	}
	connectLine <- strings.TrimSpace(line)

	for {
		hdr, errHdr := reader.ReadString('\n')
		if errHdr != nil {
			errCh <- errHdr
			return
		}
		if hdr == "\r\n" {
			break
		}
	}

	targetConn, err := net.Dial("tcp", targetAddr)
	if err != nil {
		errCh <- err
		return
	}
	defer func() { _ = targetConn.Close() }()

	if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		errCh <- err
		return
	}

	buf := make([]byte, 4)
	if _, err := io.ReadFull(reader, buf); err != nil {
		errCh <- err
		return
	}
	if _, err := targetConn.Write(buf); err != nil {
		errCh <- err
		return
	}

	resp := make([]byte, 4)
	if _, err := io.ReadFull(targetConn, resp); err != nil {
		errCh <- err
		return
	}
	if _, err := conn.Write(resp); err != nil {
		errCh <- err
		return
	}

	errCh <- nil
}
