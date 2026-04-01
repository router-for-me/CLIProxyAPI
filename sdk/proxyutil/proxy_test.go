package proxyutil

import (
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
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

func TestBuildDialerHTTPProxySupportsConnect(t *testing.T) {
	targetListener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("net.Listen returned error: %v", errListen)
	}
	defer targetListener.Close()

	targetDone := make(chan error, 1)
	go func() {
		conn, errAccept := targetListener.Accept()
		if errAccept != nil {
			targetDone <- errAccept
			return
		}
		defer conn.Close()

		buf := make([]byte, 4)
		if _, errRead := io.ReadFull(conn, buf); errRead != nil {
			targetDone <- errRead
			return
		}
		if string(buf) != "ping" {
			targetDone <- io.ErrUnexpectedEOF
			return
		}
		_, errWrite := conn.Write([]byte("pong"))
		targetDone <- errWrite
	}()

	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			t.Errorf("method = %s, want CONNECT", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:pass"))
		if got := r.Header.Get("Proxy-Authorization"); got != wantAuth {
			t.Errorf("Proxy-Authorization = %q, want %q", got, wantAuth)
			w.WriteHeader(http.StatusProxyAuthRequired)
			return
		}

		targetConn, errDial := net.Dial("tcp", targetListener.Addr().String())
		if errDial != nil {
			t.Errorf("net.Dial returned error: %v", errDial)
			w.WriteHeader(http.StatusBadGateway)
			return
		}

		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("response writer does not implement http.Hijacker")
			w.WriteHeader(http.StatusInternalServerError)
			targetConn.Close()
			return
		}

		clientConn, _, errHijack := hijacker.Hijack()
		if errHijack != nil {
			t.Errorf("Hijack returned error: %v", errHijack)
			targetConn.Close()
			return
		}

		if _, errWrite := clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); errWrite != nil {
			t.Errorf("write CONNECT response returned error: %v", errWrite)
			clientConn.Close()
			targetConn.Close()
			return
		}

		go func() {
			defer clientConn.Close()
			defer targetConn.Close()
			_, _ = io.Copy(targetConn, clientConn)
		}()
		go func() {
			defer clientConn.Close()
			defer targetConn.Close()
			_, _ = io.Copy(clientConn, targetConn)
		}()
	}))
	defer proxyServer.Close()

	dialer, mode, errBuild := BuildDialer("http://user:pass@" + proxyServer.Listener.Addr().String())
	if errBuild != nil {
		t.Fatalf("BuildDialer returned error: %v", errBuild)
	}
	if mode != ModeProxy {
		t.Fatalf("mode = %d, want %d", mode, ModeProxy)
	}
	if dialer == nil {
		t.Fatal("expected dialer, got nil")
	}

	conn, errDial := dialer.Dial("tcp", targetListener.Addr().String())
	if errDial != nil {
		t.Fatalf("dialer.Dial returned error: %v", errDial)
	}
	defer conn.Close()

	if _, errWrite := conn.Write([]byte("ping")); errWrite != nil {
		t.Fatalf("conn.Write returned error: %v", errWrite)
	}

	reply := make([]byte, 4)
	if _, errRead := io.ReadFull(conn, reply); errRead != nil {
		t.Fatalf("io.ReadFull returned error: %v", errRead)
	}
	if string(reply) != "pong" {
		t.Fatalf("reply = %q, want %q", reply, "pong")
	}

	if errTarget := <-targetDone; errTarget != nil {
		t.Fatalf("target server returned error: %v", errTarget)
	}
}
