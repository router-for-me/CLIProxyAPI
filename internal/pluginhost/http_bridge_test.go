package pluginhost

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestHostHTTPClientMarksUpstreamAttempt(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(server.Close)

	client := New().newHTTPClient(nil)
	for _, test := range []struct {
		name string
		do   func(context.Context) error
	}{
		{
			name: "buffered",
			do: func(ctx context.Context) error {
				_, errDo := client.Do(ctx, pluginapi.HTTPRequest{URL: server.URL})
				return errDo
			},
		},
		{
			name: "stream",
			do: func(ctx context.Context) error {
				response, errDo := client.DoStream(ctx, pluginapi.HTTPRequest{URL: server.URL})
				if errDo != nil {
					return errDo
				}
				for range response.Chunks {
				}
				return nil
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := cliproxyexecutor.WithUpstreamAttemptTracker(context.Background())
			if errDo := test.do(ctx); errDo != nil {
				t.Fatalf("host HTTP request error = %v", errDo)
			}
			if !cliproxyexecutor.UpstreamAttempted(ctx) {
				t.Fatal("host HTTP request did not mark an upstream attempt")
			}
		})
	}
}

func TestHostHTTPClientAppliesWireProfile(t *testing.T) {
	t.Parallel()

	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen tcp: %v", errListen)
	}
	defer func() {
		if errClose := listener.Close(); errClose != nil {
			t.Logf("close listener error: %v", errClose)
		}
	}()

	type capturedRequest struct {
		rawHeader string
	}
	captured := make(chan capturedRequest, 2)

	go func() {
		for {
			conn, errAccept := listener.Accept()
			if errAccept != nil {
				return
			}
			go func(c net.Conn) {
				defer func() {
					if errClose := c.Close(); errClose != nil {
						t.Logf("close connection error: %v", errClose)
					}
				}()
				buf := make([]byte, 4096)
				n, errRead := c.Read(buf)
				if errRead != nil && errRead != io.EOF {
					return
				}
				data := string(buf[:n])
				headerEnd := strings.Index(data, "\r\n\r\n")
				if headerEnd >= 0 {
					captured <- capturedRequest{rawHeader: data[:headerEnd]}
				}
				resp := "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok"
				_, _ = c.Write([]byte(resp))
			}(conn)
		}
	}()

	client := New().newHTTPClient(nil)
	profile := &pluginapi.HTTPWireProfile{
		HTTP1Only:              true,
		DisableAutoCompression: true,
		HeaderProfile:          []string{"x-custom-b", "X-Custom-A", "User-Agent", "Host"},
	}

	for _, mode := range []string{"buffered", "stream"} {
		req := pluginapi.HTTPRequest{
			Method: http.MethodGet,
			URL:    "http://" + listener.Addr().String() + "/test",
			Headers: http.Header{
				"X-Custom-A": []string{"value-a"},
				"x-custom-b": []string{"value-b"},
				"User-Agent": []string{"test-agent"},
			},
			WireProfile: profile,
		}

		if mode == "buffered" {
			resp, errDo := client.Do(context.Background(), req)
			if errDo != nil {
				t.Fatalf("Do error: %v", errDo)
			}
			if string(resp.Body) != "ok" {
				t.Fatalf("unexpected body: %q", string(resp.Body))
			}
		} else {
			resp, errDo := client.DoStream(context.Background(), req)
			if errDo != nil {
				t.Fatalf("DoStream error: %v", errDo)
			}
			var body []byte
			for chunk := range resp.Chunks {
				if chunk.Err != nil {
					t.Fatalf("stream chunk error: %v", chunk.Err)
				}
				body = append(body, chunk.Payload...)
			}
			if string(body) != "ok" {
				t.Fatalf("unexpected stream body: %q", string(body))
			}
		}

		select {
		case capReq := <-captured:
			lines := strings.Split(capReq.rawHeader, "\r\n")
			if len(lines) == 0 || !strings.HasPrefix(lines[0], "GET /test HTTP/1.1") {
				t.Fatalf("unexpected request line: %q", capReq.rawHeader)
			}
			expectedHeaders := []string{"x-custom-b:", "X-Custom-A:", "User-Agent:", "Host:"}
			headerIndex := 0
			for _, line := range lines[1:] {
				if headerIndex < len(expectedHeaders) && strings.HasPrefix(line, expectedHeaders[headerIndex]) {
					headerIndex++
				}
				if strings.HasPrefix(strings.ToLower(line), "accept-encoding:") {
					t.Fatalf("expected no automatic Accept-Encoding when DisableAutoCompression is set, got: %s", line)
				}
			}
			if headerIndex != len(expectedHeaders) {
				t.Fatalf("headers did not match expected order/casing: matched %d/%d; raw headers:\n%s",
					headerIndex, len(expectedHeaders), capReq.rawHeader)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("timeout waiting for %s request to reach server", mode)
		}
	}
}

func TestHostHTTPClientWireProfile_DisableAutoCompression_PreservesHTTP2(t *testing.T) {
	t.Parallel()

	client := New().newHTTPClient(nil).(*hostHTTPClient)
	profile := &pluginapi.HTTPWireProfile{
		DisableAutoCompression: true,
	}

	httpReq, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.com", nil)
	httpClient, cleanup, errClient := client.newHTTPClientForRequest(context.Background(), nil, pluginapi.HTTPRequest{
		URL:         "https://example.com",
		WireProfile: profile,
	}, httpReq)
	if errClient != nil {
		t.Fatalf("newHTTPClientForRequest error = %v", errClient)
	}
	defer cleanup()

	transport, ok := httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", httpClient.Transport)
	}
	if !transport.DisableCompression {
		t.Fatal("expected DisableCompression = true")
	}
	if !transport.ForceAttemptHTTP2 {
		t.Fatal("expected ForceAttemptHTTP2 = true when HTTP1Only is false")
	}
	if transport.TLSNextProto != nil {
		t.Fatal("expected TLSNextProto = nil when HTTP1Only is false")
	}
}

type dummyRoundTripper struct{}

func (d *dummyRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, nil
}

func TestHostHTTPClientWireProfile_CustomRoundTripperValidation(t *testing.T) {
	t.Parallel()

	client := New().newHTTPClient(nil)
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", &dummyRoundTripper{})

	_, errDo := client.Do(ctx, pluginapi.HTTPRequest{
		URL: "http://example.com",
		WireProfile: &pluginapi.HTTPWireProfile{
			HeaderProfile: []string{"Host"},
		},
	})
	if errDo == nil {
		t.Fatal("expected error with custom non-*http.Transport roundtripper, got nil")
	}
	if !strings.Contains(errDo.Error(), "wire profile is not supported with custom context RoundTripper") {
		t.Fatalf("unexpected error message: %v", errDo)
	}
}

func TestHostHTTPClientWireProfile_PlainHTTPProxyUsesStandardProxy(t *testing.T) {
	t.Parallel()

	receivedMethod := make(chan string, 1)
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod <- r.Method
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("proxied"))
	}))
	t.Cleanup(proxyServer.Close)

	client := New().newHTTPClient(nil).(*hostHTTPClient)
	profile := &pluginapi.HTTPWireProfile{
		HTTP1Only:     true,
		HeaderProfile: []string{"Host", "User-Agent"},
	}

	cfg := &config.Config{
		SDKConfig: config.SDKConfig{
			ProxyURL: proxyServer.URL,
		},
	}

	httpReq, errReq := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.org/resource", nil)
	if errReq != nil {
		t.Fatalf("new request error = %v", errReq)
	}

	httpClient, cleanup, errClient := client.newHTTPClientForRequest(context.Background(), cfg, pluginapi.HTTPRequest{
		URL:         "http://example.org/resource",
		WireProfile: profile,
	}, httpReq)
	if errClient != nil {
		t.Fatalf("newHTTPClientForRequest error = %v", errClient)
	}
	defer cleanup()

	resp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		t.Fatalf("execute request error = %v", errDo)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	select {
	case method := <-receivedMethod:
		if method != http.MethodGet {
			t.Fatalf("proxy received method %q, want %q (should not use CONNECT for plain http)", method, http.MethodGet)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for proxy request")
	}
}

func TestHostHTTPClientWireProfile_ClosesIdleConnectionsOnCompletion(t *testing.T) {
	t.Parallel()

	closedConns := make(chan struct{}, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	server.Config.ConnState = func(c net.Conn, state http.ConnState) {
		if state == http.StateClosed {
			select {
			case closedConns <- struct{}{}:
			default:
			}
		}
	}
	server.Start()
	t.Cleanup(server.Close)

	client := New().newHTTPClient(nil)
	profile := &pluginapi.HTTPWireProfile{
		HTTP1Only: true,
	}

	resp, errDo := client.Do(context.Background(), pluginapi.HTTPRequest{
		URL:         server.URL,
		WireProfile: profile,
	})
	if errDo != nil {
		t.Fatalf("Do error = %v", errDo)
	}
	if string(resp.Body) != "ok" {
		t.Fatalf("body = %q, want ok", string(resp.Body))
	}

	select {
	case <-closedConns:
		// Confirmed idle connection was actively closed by cleanup
	case <-time.After(3 * time.Second):
		t.Fatal("expected idle connection to be closed by transport cleanup, but timed out")
	}
}

func TestHostHTTPClientWireProfile_ProxyPriorityOverContextRoundTripper(t *testing.T) {
	t.Parallel()

	proxyReceived := make(chan struct{}, 1)
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case proxyReceived <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("proxied"))
	}))
	t.Cleanup(proxyServer.Close)

	// Auth has proxy configured
	auth := &coreauth.Auth{
		ProxyURL: proxyServer.URL,
	}
	client := New().newHTTPClient(auth)

	// Context contains a dummy roundtripper
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", &dummyRoundTripper{})

	resp, errDo := client.Do(ctx, pluginapi.HTTPRequest{
		URL: "http://example.org/priority-test",
		WireProfile: &pluginapi.HTTPWireProfile{
			HeaderProfile: []string{"Host"},
		},
	})
	if errDo != nil {
		t.Fatalf("Do error = %v", errDo)
	}
	if string(resp.Body) != "proxied" {
		t.Fatalf("body = %q, want proxied", string(resp.Body))
	}

	select {
	case <-proxyReceived:
		// Confirmed auth proxy was used instead of context roundtripper
	case <-time.After(3 * time.Second):
		t.Fatal("expected auth proxy to receive request, timed out")
	}
}

func TestHostHTTPClientWireProfile_HTTPToHTTPSRedirect(t *testing.T) {
	t.Parallel()

	var httpsServer *httptest.Server
	httpsServer = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("https-ok"))
	}))
	t.Cleanup(httpsServer.Close)

	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, httpsServer.URL+"/redirected", http.StatusFound)
	}))
	t.Cleanup(httpServer.Close)

	// Pass the TLS test server's client transport certificate to the host
	host := New()
	client := host.newHTTPClient(nil).(*hostHTTPClient)

	// Provide a base transport that trusts the test TLS certificate
	customTransport := httpsServer.Client().Transport.(*http.Transport).Clone()
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", customTransport)

	profile := &pluginapi.HTTPWireProfile{
		HTTP1Only:     true,
		HeaderProfile: []string{"Host", "User-Agent"},
	}

	resp, errDo := client.Do(ctx, pluginapi.HTTPRequest{
		URL:         httpServer.URL + "/start",
		WireProfile: profile,
	})
	if errDo != nil {
		t.Fatalf("Do error across HTTP->HTTPS redirect: %v", errDo)
	}
	if string(resp.Body) != "https-ok" {
		t.Fatalf("body = %q, want https-ok", string(resp.Body))
	}
}

func TestHostHTTPClientWireProfile_SOCKS5ProxyWithHeaderProfile(t *testing.T) {
	t.Parallel()

	backendReceived := make(chan string, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendReceived <- r.Header.Get("X-Custom-A")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("socks5-ok"))
	}))
	t.Cleanup(backend.Close)

	socksListener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen socks5 error: %v", errListen)
	}
	t.Cleanup(func() { _ = socksListener.Close() })

	go func() {
		conn, errAccept := socksListener.Accept()
		if errAccept != nil {
			return
		}
		defer conn.Close()

		buf := make([]byte, 256)
		n, errRead := conn.Read(buf)
		if errRead != nil || n < 3 || buf[0] != 0x05 {
			return
		}
		_, _ = conn.Write([]byte{0x05, 0x00})

		n, errRead = conn.Read(buf)
		if errRead != nil || n < 4 || buf[1] != 0x01 {
			return
		}

		backendConn, errDial := net.Dial("tcp", backend.Listener.Addr().String())
		if errDial != nil {
			_, _ = conn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
			return
		}
		defer backendConn.Close()

		_, _ = conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})

		done := make(chan struct{})
		go func() {
			_, _ = io.Copy(backendConn, conn)
			close(done)
		}()
		_, _ = io.Copy(conn, backendConn)
		<-done
	}()

	auth := &coreauth.Auth{
		ProxyURL: "socks5://" + socksListener.Addr().String(),
	}
	client := New().newHTTPClient(auth)
	profile := &pluginapi.HTTPWireProfile{
		HTTP1Only:     true,
		HeaderProfile: []string{"X-Custom-A", "Host"},
	}

	resp, errDo := client.Do(context.Background(), pluginapi.HTTPRequest{
		URL:         backend.URL + "/test",
		Headers:     http.Header{"X-Custom-A": []string{"custom-val"}},
		WireProfile: profile,
	})
	if errDo != nil {
		t.Fatalf("Do error through SOCKS5: %v", errDo)
	}
	if string(resp.Body) != "socks5-ok" {
		t.Fatalf("body = %q, want socks5-ok", string(resp.Body))
	}

	select {
	case customVal := <-backendReceived:
		if customVal != "custom-val" {
			t.Fatalf("backend received X-Custom-A = %q, want custom-val", customVal)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for backend to receive request via SOCKS5")
	}
}

func TestHostHTTPClientWireProfile_HTTPSTargetThroughSOCKS5Proxy(t *testing.T) {
	t.Parallel()

	backendReceived := make(chan string, 1)
	httpsBackend := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendReceived <- r.Header.Get("X-Custom-A")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("https-socks5-ok"))
	}))
	t.Cleanup(httpsBackend.Close)

	socksListener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen socks5 error: %v", errListen)
	}
	t.Cleanup(func() { _ = socksListener.Close() })

	go func() {
		conn, errAccept := socksListener.Accept()
		if errAccept != nil {
			return
		}
		defer conn.Close()

		buf := make([]byte, 256)
		n, errRead := conn.Read(buf)
		if errRead != nil || n < 3 || buf[0] != 0x05 {
			return
		}
		_, _ = conn.Write([]byte{0x05, 0x00})

		n, errRead = conn.Read(buf)
		if errRead != nil || n < 4 || buf[1] != 0x01 {
			return
		}

		backendConn, errDial := net.Dial("tcp", httpsBackend.Listener.Addr().String())
		if errDial != nil {
			_, _ = conn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
			return
		}
		defer backendConn.Close()

		_, _ = conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})

		done := make(chan struct{})
		go func() {
			_, _ = io.Copy(backendConn, conn)
			close(done)
		}()
		_, _ = io.Copy(conn, backendConn)
		<-done
	}()

	auth := &coreauth.Auth{
		ProxyURL: "socks5://" + socksListener.Addr().String(),
	}
	client := New().newHTTPClient(auth)

	// Trust the self-signed test TLS certificate
	tlsTransport := httpsBackend.Client().Transport.(*http.Transport).Clone()
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", tlsTransport)

	profile := &pluginapi.HTTPWireProfile{
		HTTP1Only:     true,
		HeaderProfile: []string{"X-Custom-A", "Host"},
	}

	resp, errDo := client.Do(ctx, pluginapi.HTTPRequest{
		URL:         httpsBackend.URL + "/test",
		Headers:     http.Header{"X-Custom-A": []string{"custom-val-https"}},
		WireProfile: profile,
	})
	if errDo != nil {
		t.Fatalf("Do error through SOCKS5 to HTTPS: %v", errDo)
	}
	if string(resp.Body) != "https-socks5-ok" {
		t.Fatalf("body = %q, want https-socks5-ok", string(resp.Body))
	}

	select {
	case customVal := <-backendReceived:
		if customVal != "custom-val-https" {
			t.Fatalf("backend received X-Custom-A = %q, want custom-val-https", customVal)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for HTTPS backend to receive request via SOCKS5")
	}
}

func TestHostHTTPClientWireProfile_DirectProxyMode(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("direct-ok"))
	}))
	t.Cleanup(server.Close)

	auth := &coreauth.Auth{
		ProxyURL: "direct",
	}
	client := New().newHTTPClient(auth)

	resp, errDo := client.Do(context.Background(), pluginapi.HTTPRequest{
		URL: server.URL + "/direct-test",
		WireProfile: &pluginapi.HTTPWireProfile{
			HTTP1Only:     true,
			HeaderProfile: []string{"Host", "User-Agent"},
		},
	})
	if errDo != nil {
		t.Fatalf("Do error with direct proxy setting: %v", errDo)
	}
	if string(resp.Body) != "direct-ok" {
		t.Fatalf("body = %q, want direct-ok", string(resp.Body))
	}
}

func TestHostHTTPClientWireProfile_CustomHTTPMethod(t *testing.T) {
	t.Parallel()

	capturedHeaders := make(chan []string, 1)
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen tcp: %v", errListen)
	}
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		conn, errAccept := listener.Accept()
		if errAccept != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 2048)
		n, _ := conn.Read(buf)
		lines := strings.Split(string(buf[:n]), "\r\n")
		var headers []string
		for _, line := range lines[1:] {
			if line == "" {
				break
			}
			headers = append(headers, line)
		}
		capturedHeaders <- headers
		_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok"))
	}()

	client := New().newHTTPClient(nil)
	profile := &pluginapi.HTTPWireProfile{
		HTTP1Only:     true,
		HeaderProfile: []string{"Man", "Host"},
	}

	_, errDo := client.Do(context.Background(), pluginapi.HTTPRequest{
		Method: "M-SEARCH",
		URL:    "http://" + listener.Addr().String() + "/ssdp",
		Headers: http.Header{
			"Host": []string{"target.local"},
			"Man":  []string{"\"ssdp:discover\""},
		},
		WireProfile: profile,
	})
	if errDo != nil {
		t.Fatalf("Do error: %v", errDo)
	}

	select {
	case headers := <-capturedHeaders:
		if len(headers) < 2 {
			t.Fatalf("not enough headers: %v", headers)
		}
		if !strings.HasPrefix(headers[0], "Man:") {
			t.Fatalf("expected first header to be Man, got: %s", headers[0])
		}
		if !strings.HasPrefix(headers[1], "Host:") {
			t.Fatalf("expected second header to be Host, got: %s", headers[1])
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for server to receive M-SEARCH request")
	}
}

func TestHostHTTPClientWireProfile_CustomTLSDialerRejectsHTTP2(t *testing.T) {
	t.Parallel()

	// An HTTPS server supporting h2
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	// Custom transport that negotiates h2 via custom DialTLSContext
	customTransport := server.Client().Transport.(*http.Transport).Clone()
	customTransport.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		rawConn, errDial := net.Dial("tcp", addr)
		if errDial != nil {
			return nil, errDial
		}
		tlsCfg := customTransport.TLSClientConfig.Clone()
		tlsCfg.NextProtos = []string{"h2"}
		tlsCfg.InsecureSkipVerify = true
		tlsConn := tls.Client(rawConn, tlsCfg)
		return tlsConn, nil
	}

	client := New().newHTTPClient(nil)
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", customTransport)

	_, errDo := client.Do(ctx, pluginapi.HTTPRequest{
		URL: server.URL + "/h2-test",
		WireProfile: &pluginapi.HTTPWireProfile{
			HeaderProfile: []string{"Host"},
		},
	})
	if errDo == nil {
		t.Fatal("expected error when custom TLS dialer negotiates h2, got nil")
	}
	if !strings.Contains(errDo.Error(), "custom TLS dialer negotiated unsupported protocol") {
		t.Fatalf("unexpected error: %v", errDo)
	}
}

func TestHostHTTPClientWireProfile_IPv6ProxyURL(t *testing.T) {
	t.Parallel()

	client := New().newHTTPClient(nil).(*hostHTTPClient)
	profile := &pluginapi.HTTPWireProfile{
		HTTP1Only:     true,
		HeaderProfile: []string{"Host"},
	}

	cfg := &config.Config{
		SDKConfig: config.SDKConfig{
			ProxyURL: "http://[::1]",
		},
	}

	httpReq, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com", nil)
	httpClient, cleanup, errClient := client.newHTTPClientForRequest(context.Background(), cfg, pluginapi.HTTPRequest{
		URL:         "http://example.com",
		WireProfile: profile,
	}, httpReq)
	if errClient != nil {
		t.Fatalf("newHTTPClientForRequest with IPv6 proxy failed: %v", errClient)
	}
	defer cleanup()

	if httpClient.Transport == nil {
		t.Fatal("expected transport, got nil")
	}
}

func TestHostHTTPClientWireProfile_LegacyDialHookSupported(t *testing.T) {
	t.Parallel()

	legacyDialed := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("legacy-ok"))
	}))
	t.Cleanup(server.Close)

	customTransport := &http.Transport{
		Dial: func(network, addr string) (net.Conn, error) {
			legacyDialed <- addr
			return net.Dial(network, addr)
		},
	}
	client := New().newHTTPClient(nil)
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", customTransport)

	resp, errDo := client.Do(ctx, pluginapi.HTTPRequest{
		URL: server.URL + "/legacy-test",
		WireProfile: &pluginapi.HTTPWireProfile{
			HeaderProfile: []string{"Host"},
		},
	})
	if errDo != nil {
		t.Fatalf("Do error with legacy dialer: %v", errDo)
	}
	if string(resp.Body) != "legacy-ok" {
		t.Fatalf("body = %q, want legacy-ok", string(resp.Body))
	}

	select {
	case addr := <-legacyDialed:
		if addr != server.Listener.Addr().String() {
			t.Fatalf("legacy dialer called with addr %q, want %q", addr, server.Listener.Addr().String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for legacy dialer to be called")
	}
}

func TestHostHTTPClientWireProfile_CONNECTProxyAuthPrecedence(t *testing.T) {
	t.Parallel()

	receivedAuthHeader := make(chan string, 1)
	proxyListener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen proxy error: %v", errListen)
	}
	t.Cleanup(func() { _ = proxyListener.Close() })

	go func() {
		conn, errAccept := proxyListener.Accept()
		if errAccept != nil {
			return
		}
		defer conn.Close()

		br := bufio.NewReader(conn)
		req, errRead := http.ReadRequest(br)
		if errRead != nil {
			return
		}
		receivedAuthHeader <- req.Header.Get("Proxy-Authorization")
		_, _ = conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))

		// Read TLS ClientHello and close
		buf := make([]byte, 1024)
		_, _ = conn.Read(buf)
	}()

	customTransport := &http.Transport{
		ProxyConnectHeader: http.Header{
			"Proxy-Authorization": []string{"HeaderToken"},
		},
	}

	proxyURL := &url.URL{
		Scheme: "http",
		Host:   proxyListener.Addr().String(),
		User:   url.UserPassword("user", "pass"),
	}

	// Dial through proxy tunnel
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	conn, errTunnel := dialProxyTunnel(ctx, (&net.Dialer{}).DialContext, nil, proxyURL, "target.local:443", customTransport)
	if errTunnel == nil && conn != nil {
		_ = conn.Close()
	}

	select {
	case authHeader := <-receivedAuthHeader:
		wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:pass"))
		if authHeader != wantAuth {
			t.Fatalf("Proxy-Authorization header = %q, want %q (URL credentials must override header)", authHeader, wantAuth)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for proxy CONNECT request")
	}
}

func TestHostHTTPClientWireProfile_HTTP1OnlyWithoutHeaderProfile_RejectsHTTP2(t *testing.T) {
	t.Parallel()

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	customTransport := server.Client().Transport.(*http.Transport).Clone()
	customTransport.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		rawConn, errDial := net.Dial("tcp", addr)
		if errDial != nil {
			return nil, errDial
		}
		tlsCfg := customTransport.TLSClientConfig.Clone()
		tlsCfg.NextProtos = []string{"h2"}
		tlsCfg.InsecureSkipVerify = true
		tlsConn := tls.Client(rawConn, tlsCfg)
		return tlsConn, nil
	}

	client := New().newHTTPClient(nil)
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", customTransport)

	_, errDo := client.Do(ctx, pluginapi.HTTPRequest{
		URL: server.URL + "/h2-test",
		WireProfile: &pluginapi.HTTPWireProfile{
			HTTP1Only: true, // Notice: NO HeaderProfile set!
		},
	})
	if errDo == nil {
		t.Fatal("expected error when custom TLS dialer negotiates h2 under HTTP1Only, got nil")
	}
	if !strings.Contains(errDo.Error(), "custom TLS dialer negotiated unsupported protocol") {
		t.Fatalf("unexpected error: %v", errDo)
	}
}

func TestHostHTTPClientWireProfile_DirectProxyModeInheritsDefaultTransport(t *testing.T) {
	t.Parallel()

	client := New().newHTTPClient(nil).(*hostHTTPClient)
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{
			ProxyURL: "direct",
		},
	}

	httpReq, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com", nil)
	httpClient, cleanup, errClient := client.newHTTPClientForRequest(context.Background(), cfg, pluginapi.HTTPRequest{
		URL: "http://example.com",
		WireProfile: &pluginapi.HTTPWireProfile{
			HTTP1Only: true,
		},
	}, httpReq)
	if errClient != nil {
		t.Fatalf("newHTTPClientForRequest failed: %v", errClient)
	}
	defer cleanup()

	transport, ok := httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", httpClient.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("expected nil proxy for direct mode")
	}
	// Verify it inherited default transport dial timeouts
	if transport.IdleConnTimeout == 0 {
		t.Fatal("expected inherited IdleConnTimeout from DefaultTransport")
	}
}

func TestHostHTTPClientWireProfile_MixedCaseSOCKS5Scheme(t *testing.T) {
	t.Parallel()

	socksListener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen socks5 error: %v", errListen)
	}
	t.Cleanup(func() { _ = socksListener.Close() })

	backendReceived := make(chan string, 1)
	httpsBackend := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendReceived <- r.Header.Get("X-Custom-A")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("mixed-socks5-ok"))
	}))
	t.Cleanup(httpsBackend.Close)

	go func() {
		conn, errAccept := socksListener.Accept()
		if errAccept != nil {
			return
		}
		defer conn.Close()

		buf := make([]byte, 256)
		n, errRead := conn.Read(buf)
		if errRead != nil || n < 3 || buf[0] != 0x05 {
			return
		}
		_, _ = conn.Write([]byte{0x05, 0x00})

		n, errRead = conn.Read(buf)
		if errRead != nil || n < 4 || buf[1] != 0x01 {
			return
		}

		backendConn, errDial := net.Dial("tcp", httpsBackend.Listener.Addr().String())
		if errDial != nil {
			_, _ = conn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
			return
		}
		defer backendConn.Close()

		_, _ = conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})

		done := make(chan struct{})
		go func() {
			_, _ = io.Copy(backendConn, conn)
			close(done)
		}()
		_, _ = io.Copy(conn, backendConn)
		<-done
	}()

	auth := &coreauth.Auth{
		ProxyURL: "SOCKS5://" + socksListener.Addr().String(),
	}
	client := New().newHTTPClient(auth)

	tlsTransport := httpsBackend.Client().Transport.(*http.Transport).Clone()
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", tlsTransport)

	profile := &pluginapi.HTTPWireProfile{
		HTTP1Only:     true,
		HeaderProfile: []string{"X-Custom-A", "Host"},
	}

	resp, errDo := client.Do(ctx, pluginapi.HTTPRequest{
		URL:         httpsBackend.URL + "/mixed-test",
		Headers:     http.Header{"X-Custom-A": []string{"val"}},
		WireProfile: profile,
	})
	if errDo != nil {
		t.Fatalf("Do error with mixed-case SOCKS5 scheme: %v", errDo)
	}
	if string(resp.Body) != "mixed-socks5-ok" {
		t.Fatalf("body = %q, want mixed-socks5-ok", string(resp.Body))
	}

	select {
	case customVal := <-backendReceived:
		if customVal != "val" {
			t.Fatalf("backend received X-Custom-A = %q, want val", customVal)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for backend to receive request via mixed-case SOCKS5")
	}
}

func TestHostHTTPClientWireProfile_HTTPSProxyForwardingPlainHTTP(t *testing.T) {
	t.Parallel()

	proxyReceived := make(chan string, 1)
	proxyServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyReceived <- r.Method + " " + r.URL.String()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("https-proxy-plain-ok"))
	}))
	t.Cleanup(proxyServer.Close)

	auth := &coreauth.Auth{
		ProxyURL: proxyServer.URL,
	}
	client := New().newHTTPClient(auth)

	proxyCertTransport := proxyServer.Client().Transport.(*http.Transport).Clone()
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", proxyCertTransport)

	profile := &pluginapi.HTTPWireProfile{
		HTTP1Only:     true,
		HeaderProfile: []string{"Host", "User-Agent"},
	}

	resp, errDo := client.Do(ctx, pluginapi.HTTPRequest{
		URL:         "http://example.com/plain-via-https-proxy",
		WireProfile: profile,
	})
	if errDo != nil {
		t.Fatalf("Do error: %v", errDo)
	}
	if string(resp.Body) != "https-proxy-plain-ok" {
		t.Fatalf("body = %q, want https-proxy-plain-ok", string(resp.Body))
	}

	select {
	case reqSummary := <-proxyReceived:
		if !strings.HasPrefix(reqSummary, "GET http://example.com/plain-via-https-proxy") {
			t.Fatalf("unexpected proxy request: %s (should not use CONNECT for plain http)", reqSummary)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for proxy request")
	}
}

func TestHostHTTPClientWireProfile_HTTPSProxyForwardingHTTPS(t *testing.T) {
	t.Parallel()

	backendReceived := make(chan string, 1)
	backendServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendReceived <- r.Header.Get("X-Custom")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("https-via-https-proxy-ok"))
	}))
	t.Cleanup(backendServer.Close)

	proxyServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				http.Error(w, "hijacking not supported", http.StatusInternalServerError)
				return
			}
			clientConn, _, errHijack := hijacker.Hijack()
			if errHijack != nil {
				return
			}
			defer clientConn.Close()

			targetConn, errDial := net.Dial("tcp", r.Host)
			if errDial != nil {
				_, _ = clientConn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
				return
			}
			defer targetConn.Close()

			_, _ = clientConn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))

			done := make(chan struct{})
			go func() {
				_, _ = io.Copy(targetConn, clientConn)
				close(done)
			}()
			_, _ = io.Copy(clientConn, targetConn)
			<-done
			return
		}
		http.Error(w, "expected CONNECT", http.StatusBadRequest)
	}))
	t.Cleanup(proxyServer.Close)

	auth := &coreauth.Auth{
		ProxyURL: proxyServer.URL,
	}
	client := New().newHTTPClient(auth)

	baseTransport := backendServer.Client().Transport.(*http.Transport).Clone()
	proxyCert := proxyServer.Certificate()
	baseTransport.TLSClientConfig.RootCAs.AddCert(proxyCert)
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", baseTransport)

	profile := &pluginapi.HTTPWireProfile{
		HTTP1Only:     true,
		HeaderProfile: []string{"X-Custom", "Host"},
	}

	resp, errDo := client.Do(ctx, pluginapi.HTTPRequest{
		URL:         backendServer.URL + "/secure",
		Headers:     http.Header{"X-Custom": []string{"custom-header-value"}},
		WireProfile: profile,
	})
	if errDo != nil {
		t.Fatalf("Do error: %v", errDo)
	}
	if string(resp.Body) != "https-via-https-proxy-ok" {
		t.Fatalf("body = %q, want https-via-https-proxy-ok", string(resp.Body))
	}

	select {
	case customVal := <-backendReceived:
		if customVal != "custom-header-value" {
			t.Fatalf("backend received X-Custom = %q, want custom-header-value", customVal)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for backend to receive request via HTTPS proxy")
	}
}

func TestHostHTTPClientWireProfile_CustomTLSDialerUsedForHTTPSProxy(t *testing.T) {
	t.Parallel()

	proxyTLSDialed := make(chan string, 1)
	proxyServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("custom-tls-proxy-ok"))
	}))
	t.Cleanup(proxyServer.Close)

	auth := &coreauth.Auth{
		ProxyURL: proxyServer.URL,
	}
	client := New().newHTTPClient(auth)

	customTransport := &http.Transport{
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			proxyTLSDialed <- addr
			rawConn, errDial := net.Dial("tcp", addr)
			if errDial != nil {
				return nil, errDial
			}
			tlsCfg := proxyServer.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
			tlsCfg.ServerName = "127.0.0.1"
			tlsConn := tls.Client(rawConn, tlsCfg)
			return tlsConn, nil
		},
	}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", customTransport)

	resp, errDo := client.Do(ctx, pluginapi.HTTPRequest{
		URL: "http://example.com/test",
		WireProfile: &pluginapi.HTTPWireProfile{
			HeaderProfile: []string{"Host"},
		},
	})
	if errDo != nil {
		t.Fatalf("Do error: %v", errDo)
	}
	if string(resp.Body) != "custom-tls-proxy-ok" {
		t.Fatalf("body = %q, want custom-tls-proxy-ok", string(resp.Body))
	}

	select {
	case addr := <-proxyTLSDialed:
		if addr != proxyServer.Listener.Addr().String() {
			t.Fatalf("custom TLS dialer dialed %q, want %q", addr, proxyServer.Listener.Addr().String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for custom TLS dialer to be called for HTTPS proxy")
	}
}

func TestHostHTTPClientWireProfile_HTTPSProxyForwardingHTTPS_WithCustomTLSDialer(t *testing.T) {
	t.Parallel()

	// HTTPS backend
	backendServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("backend-secure-ok"))
	}))
	t.Cleanup(backendServer.Close)

	var proxyConnCount int64
	proxyServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			atomic.AddInt64(&proxyConnCount, 1)
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				http.Error(w, "hijack failed", http.StatusInternalServerError)
				return
			}
			clientConn, _, errHijack := hijacker.Hijack()
			if errHijack != nil {
				return
			}
			defer clientConn.Close()

			targetConn, errDial := net.Dial("tcp", r.Host)
			if errDial != nil {
				_, _ = clientConn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
				return
			}
			defer targetConn.Close()

			_, _ = clientConn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
			done := make(chan struct{})
			go func() {
				_, _ = io.Copy(targetConn, clientConn)
				close(done)
			}()
			_, _ = io.Copy(clientConn, targetConn)
			<-done
			return
		}
		http.Error(w, "expected CONNECT", http.StatusBadRequest)
	}))
	t.Cleanup(proxyServer.Close)

	auth := &coreauth.Auth{
		ProxyURL: proxyServer.URL,
	}
	client := New().newHTTPClient(auth)

	var tlsDialCount int64
	customTransport := &http.Transport{
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			atomic.AddInt64(&tlsDialCount, 1)
			rawConn, errDial := net.Dial("tcp", addr)
			if errDial != nil {
				return nil, errDial
			}
			tlsCfg := proxyServer.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
			tlsCfg.ServerName = "127.0.0.1"
			tlsConn := tls.Client(rawConn, tlsCfg)
			return tlsConn, nil
		},
		TLSClientConfig: backendServer.Client().Transport.(*http.Transport).TLSClientConfig.Clone(),
	}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", customTransport)

	resp, errDo := client.Do(ctx, pluginapi.HTTPRequest{
		URL: backendServer.URL + "/test",
		WireProfile: &pluginapi.HTTPWireProfile{
			HTTP1Only:     true,
			HeaderProfile: []string{"Host", "User-Agent"},
		},
	})
	if errDo != nil {
		t.Fatalf("Do error: %v", errDo)
	}
	if string(resp.Body) != "backend-secure-ok" {
		t.Fatalf("body = %q, want backend-secure-ok", string(resp.Body))
	}

	if count := atomic.LoadInt64(&tlsDialCount); count != 1 {
		t.Fatalf("custom TLS dialer called %d times, want exactly 1", count)
	}
	if count := atomic.LoadInt64(&proxyConnCount); count != 1 {
		t.Fatalf("proxy handled %d CONNECT requests, want exactly 1", count)
	}
}

func TestHostHTTPClientWireProfile_CONNECTCancellationClosesConnection(t *testing.T) {
	t.Parallel()

	proxyReceived := make(chan struct{}, 1)
	connClosed := make(chan struct{}, 1)
	proxyListener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen proxy error: %v", errListen)
	}
	t.Cleanup(func() { _ = proxyListener.Close() })

	go func() {
		conn, errAccept := proxyListener.Accept()
		if errAccept != nil {
			return
		}
		defer conn.Close()

		proxyReceived <- struct{}{}
		// Do not reply to CONNECT, wait until client cancels and closes the conn
		buf := make([]byte, 1024)
		_, _ = conn.Read(buf)
		// Wait for EOF when client closes
		_, _ = conn.Read(buf)
		connClosed <- struct{}{}
	}()

	proxyURL := &url.URL{
		Scheme: "http",
		Host:   proxyListener.Addr().String(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-proxyReceived
		// Cancel while CONNECT response is pending
		cancel()
	}()

	_, errTunnel := dialProxyTunnel(ctx, (&net.Dialer{}).DialContext, nil, proxyURL, "target.local:443", &http.Transport{})
	if errTunnel == nil {
		t.Fatal("expected error on canceled context, got nil")
	}

	select {
	case <-connClosed:
		// Succeeded: canceled context closed the connection promptly
	case <-time.After(3 * time.Second):
		t.Fatal("connection was not closed promptly upon context cancellation")
	}
}

func TestHostHTTPClientWireProfile_CustomDefaultRoundTripperError(t *testing.T) {
	orig := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = orig })
	http.DefaultTransport = &dummyRoundTripper{}

	client := New().newHTTPClient(nil).(*hostHTTPClient)
	httpReq, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com", nil)
	_, _, err := client.newHTTPClientForRequest(context.Background(), nil, pluginapi.HTTPRequest{
		URL: "http://example.com",
		WireProfile: &pluginapi.HTTPWireProfile{
			HeaderProfile: []string{"Host"},
		},
	}, httpReq)
	if err == nil {
		t.Fatal("expected error with custom default roundtripper, got nil")
	}
	if !strings.Contains(err.Error(), "wire profile is not supported with custom default RoundTripper") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestHostHTTPClientWireProfile_DefaultTransportCustomTLSDialer(t *testing.T) {
	orig := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = orig })

	dialedAddr := make(chan string, 1)
	cloned := orig.(*http.Transport).Clone()
	cloned.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialedAddr <- addr
		rawConn, errDial := net.Dial("tcp", addr)
		if errDial != nil {
			return nil, errDial
		}
		tlsCfg := &tls.Config{InsecureSkipVerify: true}
		tlsConn := tls.Client(rawConn, tlsCfg)
		return tlsConn, nil
	}
	http.DefaultTransport = cloned

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("tls-ok"))
	}))
	t.Cleanup(server.Close)

	client := New().newHTTPClient(nil)
	resp, errDo := client.Do(context.Background(), pluginapi.HTTPRequest{
		URL: server.URL + "/default-tls",
		WireProfile: &pluginapi.HTTPWireProfile{
			HeaderProfile: []string{"Host"},
		},
	})
	if errDo != nil {
		t.Fatalf("Do error: %v", errDo)
	}
	if string(resp.Body) != "tls-ok" {
		t.Fatalf("body = %q, want tls-ok", string(resp.Body))
	}

	select {
	case addr := <-dialedAddr:
		if addr != server.Listener.Addr().String() {
			t.Fatalf("dialed %q, want %q", addr, server.Listener.Addr().String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for default transport custom TLS dialer")
	}
}
