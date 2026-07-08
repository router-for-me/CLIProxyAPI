package proxyutil

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func mustDefaultTransport(t *testing.T) *http.Transport {
	t.Helper()

	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok || transport == nil {
		t.Fatal("http.DefaultTransport is not an *http.Transport")
	}
	return transport
}

func TestParse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    Mode
		wantErr bool
	}{
		{name: "inherit", input: "", want: ModeInherit},
		{name: "direct", input: "direct", want: ModeDirect},
		{name: "none", input: "none", want: ModeDirect},
		{name: "http", input: "http://proxy.example.com:8080", want: ModeProxy},
		{name: "https", input: "https://proxy.example.com:8443", want: ModeProxy},
		{name: "socks5", input: "socks5://proxy.example.com:1080", want: ModeProxy},
		{name: "socks5h", input: "socks5h://proxy.example.com:1080", want: ModeProxy},
		{name: "invalid", input: "bad-value", want: ModeInvalid, wantErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			setting, errParse := Parse(tt.input)
			if tt.wantErr && errParse == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && errParse != nil {
				t.Fatalf("unexpected error: %v", errParse)
			}
			if setting.Mode != tt.want {
				t.Fatalf("mode = %d, want %d", setting.Mode, tt.want)
			}
		})
	}
}

func TestBuildHTTPTransportDirectBypassesProxy(t *testing.T) {
	t.Parallel()

	transport, mode, errBuild := BuildHTTPTransport("direct")
	if errBuild != nil {
		t.Fatalf("BuildHTTPTransport returned error: %v", errBuild)
	}
	if mode != ModeDirect {
		t.Fatalf("mode = %d, want %d", mode, ModeDirect)
	}
	if transport == nil {
		t.Fatal("expected transport, got nil")
	}
	if transport.Proxy != nil {
		t.Fatal("expected direct transport to disable proxy function")
	}
}

func TestBuildHTTPTransportHTTPProxy(t *testing.T) {
	t.Parallel()

	transport, mode, errBuild := BuildHTTPTransport("http://proxy.example.com:8080")
	if errBuild != nil {
		t.Fatalf("BuildHTTPTransport returned error: %v", errBuild)
	}
	if mode != ModeProxy {
		t.Fatalf("mode = %d, want %d", mode, ModeProxy)
	}
	if transport == nil {
		t.Fatal("expected transport, got nil")
	}

	req, errRequest := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if errRequest != nil {
		t.Fatalf("http.NewRequest returned error: %v", errRequest)
	}

	proxyURL, errProxy := transport.Proxy(req)
	if errProxy != nil {
		t.Fatalf("transport.Proxy returned error: %v", errProxy)
	}
	if proxyURL == nil || proxyURL.String() != "http://proxy.example.com:8080" {
		t.Fatalf("proxy URL = %v, want http://proxy.example.com:8080", proxyURL)
	}

	defaultTransport := mustDefaultTransport(t)
	if transport.ForceAttemptHTTP2 != defaultTransport.ForceAttemptHTTP2 {
		t.Fatalf("ForceAttemptHTTP2 = %v, want %v", transport.ForceAttemptHTTP2, defaultTransport.ForceAttemptHTTP2)
	}
	if transport.IdleConnTimeout != defaultTransport.IdleConnTimeout {
		t.Fatalf("IdleConnTimeout = %v, want %v", transport.IdleConnTimeout, defaultTransport.IdleConnTimeout)
	}
	if transport.TLSHandshakeTimeout != defaultTransport.TLSHandshakeTimeout {
		t.Fatalf("TLSHandshakeTimeout = %v, want %v", transport.TLSHandshakeTimeout, defaultTransport.TLSHandshakeTimeout)
	}
}

func TestBuildHTTPTransportSOCKS5ProxyInheritsDefaultTransportSettings(t *testing.T) {
	t.Parallel()

	transport, mode, errBuild := BuildHTTPTransport("socks5://proxy.example.com:1080")
	if errBuild != nil {
		t.Fatalf("BuildHTTPTransport returned error: %v", errBuild)
	}
	if mode != ModeProxy {
		t.Fatalf("mode = %d, want %d", mode, ModeProxy)
	}
	if transport == nil {
		t.Fatal("expected transport, got nil")
	}
	if transport.Proxy != nil {
		t.Fatal("expected SOCKS5 transport to bypass http proxy function")
	}

	defaultTransport := mustDefaultTransport(t)
	if transport.ForceAttemptHTTP2 != defaultTransport.ForceAttemptHTTP2 {
		t.Fatalf("ForceAttemptHTTP2 = %v, want %v", transport.ForceAttemptHTTP2, defaultTransport.ForceAttemptHTTP2)
	}
	if transport.IdleConnTimeout != defaultTransport.IdleConnTimeout {
		t.Fatalf("IdleConnTimeout = %v, want %v", transport.IdleConnTimeout, defaultTransport.IdleConnTimeout)
	}
	if transport.TLSHandshakeTimeout != defaultTransport.TLSHandshakeTimeout {
		t.Fatalf("TLSHandshakeTimeout = %v, want %v", transport.TLSHandshakeTimeout, defaultTransport.TLSHandshakeTimeout)
	}
}

func TestBuildHTTPTransportSOCKS5HProxy(t *testing.T) {
	t.Parallel()

	transport, mode, errBuild := BuildHTTPTransport("socks5h://proxy.example.com:1080")
	if errBuild != nil {
		t.Fatalf("BuildHTTPTransport returned error: %v", errBuild)
	}
	if mode != ModeProxy {
		t.Fatalf("mode = %d, want %d", mode, ModeProxy)
	}
	if transport == nil {
		t.Fatal("expected transport, got nil")
	}
	if transport.Proxy != nil {
		t.Fatal("expected SOCKS5H transport to bypass http proxy function")
	}
	if transport.DialContext == nil {
		t.Fatal("expected SOCKS5H transport to have custom DialContext")
	}
}

func TestBuildDialerHTTPProxyCONNECT(t *testing.T) {
	t.Parallel()

	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("net.Listen returned error: %v", errListen)
	}
	defer func() {
		if errClose := listener.Close(); errClose != nil {
			t.Errorf("listener.Close returned error: %v", errClose)
		}
	}()

	done := make(chan error, 1)
	go func() {
		conn, errAccept := listener.Accept()
		if errAccept != nil {
			done <- errAccept
			return
		}
		defer func() { _ = conn.Close() }()
		if errDeadline := conn.SetDeadline(time.Now().Add(5 * time.Second)); errDeadline != nil {
			done <- errDeadline
			return
		}

		req, errRead := http.ReadRequest(bufio.NewReader(conn))
		if errRead != nil {
			done <- fmt.Errorf("read CONNECT request failed: %w", errRead)
			return
		}
		if req.Method != http.MethodConnect {
			done <- fmt.Errorf("method = %s, want CONNECT", req.Method)
			return
		}
		if req.Host != "target.example.com:443" {
			done <- fmt.Errorf("host = %s, want target.example.com:443", req.Host)
			return
		}
		wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:pass"))
		if gotAuth := req.Header.Get("Proxy-Authorization"); gotAuth != wantAuth {
			done <- fmt.Errorf("Proxy-Authorization = %q, want %q", gotAuth, wantAuth)
			return
		}

		if _, errWrite := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\nok"); errWrite != nil {
			done <- fmt.Errorf("write CONNECT response failed: %w", errWrite)
			return
		}

		buf := make([]byte, 4)
		n, errReadTunnel := io.ReadFull(conn, buf)
		if errReadTunnel != nil {
			done <- fmt.Errorf("read tunneled payload failed after %d bytes: %w", n, errReadTunnel)
			return
		}
		if string(buf) != "ping" {
			done <- fmt.Errorf("tunneled payload = %q, want ping", string(buf))
			return
		}
		done <- nil
	}()

	dialer, mode, errBuild := BuildDialer("http://user:pass@" + listener.Addr().String())
	if errBuild != nil {
		t.Fatalf("BuildDialer returned error: %v", errBuild)
	}
	if mode != ModeProxy {
		t.Fatalf("mode = %d, want %d", mode, ModeProxy)
	}
	if dialer == nil {
		t.Fatal("expected dialer, got nil")
	}

	conn, errDial := dialer.Dial("tcp", "target.example.com:443")
	if errDial != nil {
		t.Fatalf("dialer.Dial returned error: %v", errDial)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			t.Errorf("conn.Close returned error: %v", errClose)
		}
	}()

	buf := make([]byte, 2)
	n, errRead := io.ReadFull(conn, buf)
	if errRead != nil {
		t.Fatalf("conn.Read returned error after %d bytes: %v", n, errRead)
	}
	if string(buf) != "ok" {
		t.Fatalf("buffered tunnel payload = %q, want ok", string(buf))
	}

	if _, errWrite := conn.Write([]byte("ping")); errWrite != nil {
		t.Fatalf("conn.Write returned error: %v", errWrite)
	}

	if errServer := <-done; errServer != nil {
		t.Fatalf("proxy server returned error: %v", errServer)
	}
}

func TestRedactProxyURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "with credentials",
			input: "http://user:pass@proxy.example.com:8080/path?token=secret",
			want:  "http://redacted@proxy.example.com:8080",
		},
		{
			name:  "without credentials",
			input: "socks5://proxy.example.com:1080",
			want:  "socks5://proxy.example.com:1080",
		},
		{
			name:  "invalid",
			input: "bad-value",
			want:  "<invalid proxy URL>",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := Redact(tt.input); got != tt.want {
				t.Fatalf("Redact() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseErrorDoesNotExposeProxyCredentials(t *testing.T) {
	t.Parallel()

	input := "http://user:secret%@proxy.example.com:8080"
	_, errParse := Parse(input)
	if errParse == nil {
		t.Fatal("expected Parse to return an error")
	}
	if strings.Contains(errParse.Error(), input) ||
		strings.Contains(errParse.Error(), "user") ||
		strings.Contains(errParse.Error(), "secret") {
		t.Fatalf("parse error exposes proxy credentials: %q", errParse.Error())
	}
}
