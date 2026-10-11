package claudemaster

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type proxyTestTransport func(*http.Request) (*http.Response, error)

func (fn proxyTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func proxyTestCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: masterAPIHost}, DNSNames: []string{masterAPIHost},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: cert}, pool
}

func proxyTestStart(t *testing.T, inference http.Handler, control http.RoundTripper) (*Proxy, *http.Client, *x509.CertPool) {
	t.Helper()
	cert, pool := proxyTestCertificate(t)
	return proxyTestStartWithCertificate(t, inference, control, cert, pool)
}

func proxyTestStartWithCertificate(t *testing.T, inference http.Handler, control http.RoundTripper, cert tls.Certificate, pool *x509.CertPool) (*Proxy, *http.Client, *x509.CertPool) {
	t.Helper()
	if control == nil {
		control = proxyTestTransport(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("unexpected control request")
		})
	}
	auth := newProxyTestAuth(t)
	p, err := StartProxy(auth.apply(ProxyOptions{Certificate: cert, Inference: inference, ControlTransport: control}))
	if err != nil {
		t.Fatal(err)
	}
	proxyTestAuths.Store(p, auth)
	proxyURL, err := url.Parse(p.URL())
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: auth.clientTLS(pool)}
	client := &http.Client{Transport: transport}
	t.Cleanup(func() { transport.CloseIdleConnections(); _ = p.Close() })
	return p, client, pool
}

func proxyTestResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}

func proxyTestRequest(t *testing.T, client *http.Client, method, path, body string, headers http.Header) (*http.Response, string) {
	t.Helper()
	r, err := http.NewRequest(method, "https://"+masterAPIHost+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	r.Header = headers
	resp, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	bytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(bytes)
}

func TestProxyInferenceCredentialBoundary(t *testing.T) {
	var calls atomic.Int32
	inference := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Anthropic-Version") != "2023-06-01" || r.Header.Get("Anthropic-Beta") != "test-beta" {
			t.Error("required inference protocol headers were not retained")
		}
		if len(r.Header) != 7 || r.Header.Get("X-Custom-Identity") != "future-end-to-end" || strings.Contains(fmt.Sprint(r.Header), "MASTER-CANARY") || r.URL.RawQuery != "" || r.URL.Host != "" || r.Host != "" || r.TLS != nil || r.RequestURI != "" {
			t.Error("master account request identity leaked into inference")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != `{"model":"master-model","messages":[]}` {
			t.Error("inference body was corrupted")
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"type":"message"}`)
	})
	_, client, _ := proxyTestStart(t, inference, nil)
	headers := http.Header{
		"Authorization": {"Bearer MASTER-CANARY"}, "Cookie": {"session=MASTER-CANARY"}, "X-Api-Key": {"MASTER-CANARY"},
		"X-Account-Id": {"MASTER-CANARY"}, "X-Custom-Identity": {"future-end-to-end"}, "Accept": {"application/json"},
		"Anthropic-Version": {"2023-06-01"}, "Anthropic-Beta": {"test-beta"}, "Content-Type": {"application/json"},
		"User-Agent": {"claude-cli/2.1.285 (external, cli)"}, "Accept-Encoding": {"gzip, deflate, br, zstd"},
	}
	for _, path := range []string{"/v1/messages?beta=true&identity=MASTER-CANARY", "/v1/messages/count_tokens"} {
		resp, body := proxyTestRequest(t, client, http.MethodPost, path, `{"model":"master-model","messages":[]}`, headers.Clone())
		if resp.StatusCode != http.StatusCreated || body != `{"type":"message"}` {
			t.Fatalf("inference response changed: %d %s", resp.StatusCode, body)
		}
	}
	if calls.Load() != 2 {
		t.Fatal("inference handler was not used exactly twice")
	}
}

func TestProxyControlPreservesIdentityQueryAndBody(t *testing.T) {
	const query = "entrypoint=cli&model=claude%2Fopus&limit=20&x=a+b&x=a%20b"
	const content = `{"master":"CONTROL-CANARY","session":"native"}`
	control := proxyTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.Host != masterAPIHost || r.Host != masterAPIHost || r.URL.RawQuery != query {
			t.Error("control target or raw query changed")
		}
		if r.Header.Get("Authorization") != "Bearer CONTROL-CANARY" || r.Header.Get("Cookie") != "session=CONTROL-CANARY" || r.Header.Get("X-Account-Id") != "CONTROL-CANARY" {
			t.Error("control identity changed")
		}
		if r.Header.Get("X-Forwarded-Host") != "" || r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("X-Hop-Secret") != "" {
			t.Error("untrusted forwarding or hop-by-hop headers were retained")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != content {
			t.Error("control body changed")
		}
		resp := proxyTestResponse(201, `{"worker_jwt":"CONTROL-CANARY"}`)
		resp.Header.Set("Set-Cookie", "native=CONTROL-CANARY")
		return resp, nil
	})
	_, client, _ := proxyTestStart(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("control hit inference") }), control)
	for _, path := range []string{"/api/claude_cli/bootstrap", "/v1/code/sessions", "/v1/code/sessions/cse_123/bridge", "/api/oauth/profile"} {
		resp, body := proxyTestRequest(t, client, http.MethodPost, path+"?"+query, content, http.Header{
			"Authorization": {"Bearer CONTROL-CANARY"}, "Cookie": {"session=CONTROL-CANARY"}, "X-Account-Id": {"CONTROL-CANARY"},
			"X-Forwarded-Host": {"attacker.invalid"}, "X-Forwarded-For": {"192.0.2.1"}, "Connection": {"X-Hop-Secret"}, "X-Hop-Secret": {"CONTROL-CANARY"},
		})
		if resp.StatusCode != 201 || body != `{"worker_jwt":"CONTROL-CANARY"}` || resp.Header.Get("Set-Cookie") != "native=CONTROL-CANARY" {
			t.Error("control response changed")
		}
	}
}

func TestProxyInferenceFailureNeverFallsBack(t *testing.T) {
	var controlCalls atomic.Int32
	control := proxyTestTransport(func(*http.Request) (*http.Response, error) {
		controlCalls.Add(1)
		return proxyTestResponse(200, "unexpected"), nil
	})
	_, client, _ := proxyTestStart(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "selected account unavailable", http.StatusServiceUnavailable)
	}), control)
	resp, body := proxyTestRequest(t, client, http.MethodPost, "/v1/messages", `{}`, nil)
	if resp.StatusCode != 503 || !strings.Contains(body, "selected account unavailable") || controlCalls.Load() != 0 {
		t.Fatal("inference failure was not isolated from master control")
	}
}

func TestProxyRejectsUnknownOrAmbiguousPaths(t *testing.T) {
	var upstream, pool atomic.Int32
	var seen []string
	var mu sync.Mutex
	p, client, certPool := proxyTestStart(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { pool.Add(1) }), proxyTestTransport(func(r *http.Request) (*http.Response, error) {
		upstream.Add(1)
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		mu.Unlock()
		return proxyTestResponse(200, "anthropic"), nil
	}))
	// Compatibility first: a route the pool does not serve goes to Anthropic unchanged, on the session's own
	// login, as Claude Code would send it without claude-master. None of them reaches the pool.
	relayed := []struct{ method, path string }{
		{"POST", "/v1/messages/batches"}, {"POST", "/v1/messages/"}, {"POST", "/v1/complete"}, {"POST", "/v1/responses"},
		{"POST", "/v1/unknown"}, {"POST", "/api/claude_cli/new_inference"}, {"POST", "/v1/code/agent-proxy"},
		{"GET", "/v2/messages"}, {"POST", "/v1/files"}, {"GET", "/v1/environments/bridge"},
		{"POST", "/v1/environments/env_1/work/work_2/messages"},
	}
	for _, test := range relayed {
		t.Run("relayed"+test.path, func(t *testing.T) {
			resp, body := proxyTestRequest(t, client, test.method, test.path, `{}`, nil)
			if resp.StatusCode != 200 || body != "anthropic" {
				t.Fatalf("got %d %q, want Anthropic's answer", resp.StatusCode, body)
			}
		})
	}
	if int(upstream.Load()) != len(relayed) || pool.Load() != 0 {
		t.Fatalf("upstream %d pool %d, want %d and 0", upstream.Load(), pool.Load(), len(relayed))
	}
	// The pool's own routes with the wrong method, and anything ambiguous or encoded, never leave the proxy.
	for _, test := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/v1/messages", 405}, {"GET", "/v1/messages/count_tokens", 405},
		{"POST", "/v1/%6dessages", 400}, {"POST", "/v1/messages%2Fcount_tokens", 400},
		{"POST", "/v1/code/sessions/../../messages", 400}, {"POST", "//v1/messages", 400},
		{"POST", "/v1/code/sessions\\messages", 400},
	} {
		t.Run(test.path, func(t *testing.T) {
			resp, _ := proxyTestRequest(t, client, test.method, test.path, `{}`, nil)
			if resp.StatusCode != test.status {
				t.Fatalf("got %d, expected %d", resp.StatusCode, test.status)
			}
		})
	}
	for _, raw := range []string{
		"POST https://api.anthropic.com/v1/messages HTTP/1.1\r\nHost: api.anthropic.com\r\nContent-Length: 2\r\n\r\n{}",
		"POST /v1/messages HTTP/1.1\r\nHost: other.invalid\r\nContent-Length: 2\r\n\r\n{}",
		"POST /v1/messages HTTP/1.1\r\nHost: api.anthropic.com\r\nProxy-Authorization: SECRET\r\nContent-Length: 2\r\n\r\n{}",
	} {
		conn := proxyTestTLSConnection(t, p, certPool, "api.anthropic.com:443")
		_, _ = io.WriteString(conn, raw)
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		_ = conn.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("ambiguous HTTP target returned %d", resp.StatusCode)
		}
	}
	if int(upstream.Load()) != len(relayed) || pool.Load() != 0 {
		t.Fatalf("a refused request reached an upstream: upstream %d pool %d (%v)", upstream.Load(), pool.Load(), seen)
	}
}

func proxyTestTLSConnection(t *testing.T, p *Proxy, pool *x509.CertPool, authority string) *tls.Conn {
	t.Helper()
	conn, err := proxyTestDialRaw(p)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", authority, authority)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil || resp.StatusCode != 200 {
		_ = conn.Close()
		t.Fatalf("CONNECT failed: %v", err)
	}
	tlsConn := tls.Client(&proxyBufferedConn{Conn: conn, reader: reader}, &tls.Config{ServerName: masterAPIHost, RootCAs: pool, MinVersion: tls.VersionTLS12})
	if err := tlsConn.Handshake(); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tlsConn.Close() })
	return tlsConn
}

func TestProxyConnectNormalizesMasterHost(t *testing.T) {
	p, _, pool := proxyTestStart(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) }), nil)
	conn := proxyTestTLSConnection(t, p, pool, "API.Anthropic.Com..:443")
	_, _ = io.WriteString(conn, "POST /v1/messages HTTP/1.1\r\nHost: api.anthropic.com\r\nContent-Length: 2\r\n\r\n{}")
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatal("normalized master host bypassed inference")
	}
}

// Send CONNECT and the first TLS ClientHello in one write, as clients may do.
type proxyPipelinedClientConn struct {
	net.Conn
	header       string
	reader       *bufio.Reader
	wroteHeader  bool
	readResponse bool
}

func (conn *proxyPipelinedClientConn) Write(b []byte) (int, error) {
	if conn.wroteHeader {
		return conn.Conn.Write(b)
	}
	conn.wroteHeader = true
	message := append([]byte(conn.header), b...)
	n, err := conn.Conn.Write(message)
	n -= len(conn.header)
	if n < 0 {
		n = 0
	}
	return n, err
}

func (conn *proxyPipelinedClientConn) Read(b []byte) (int, error) {
	if !conn.readResponse {
		conn.readResponse = true
		resp, err := http.ReadResponse(conn.reader, nil)
		if err != nil {
			return 0, err
		}
		if resp.StatusCode != 200 {
			return 0, errors.New("CONNECT rejected")
		}
	}
	return conn.reader.Read(b)
}

func TestProxyPipelinedConnectClientHello(t *testing.T) {
	p, _, pool := proxyTestStart(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) }), nil)
	raw, err := proxyTestDialRaw(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	pipelined := &proxyPipelinedClientConn{Conn: raw, reader: bufio.NewReader(raw), header: fmt.Sprintf("CONNECT api.anthropic.com:443 HTTP/1.1\r\nHost: api.anthropic.com:443\r\n\r\n")}
	conn := tls.Client(pipelined, &tls.Config{ServerName: masterAPIHost, RootCAs: pool, MinVersion: tls.VersionTLS12})
	defer func() { _ = conn.Close() }()
	if err := conn.Handshake(); err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(conn, "POST /v1/messages HTTP/1.1\r\nHost: api.anthropic.com\r\nContent-Length: 2\r\n\r\n{}")
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatal("pipelined ClientHello was corrupted")
	}
}

func TestProxyRejectsConnectAuthoritiesAndNonConnectRequests(t *testing.T) {
	p, _, _ := proxyTestStart(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("unexpected inference") }), nil)
	for _, authority := range []string{"api.anthropic.com:80", "api.anthropic.com:444", "api.anthropic.com", "other.invalid:80", "user@api.anthropic.com:443", "api.anthropic.com:443/path", "api.anthropic.com%2e:443", "api..anthropic.com:443", "-invalid.example:443"} {
		conn, err := proxyTestDialRaw(p)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", authority, authority)
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode != 400 {
				t.Errorf("invalid CONNECT returned %d", resp.StatusCode)
			}
		}
		_ = conn.Close()
	}
	for _, test := range []struct {
		raw    string
		status int
	}{
		{"GET http://api.anthropic.com/v1/messages HTTP/1.1\r\nHost: api.anthropic.com\r\n\r\n", 400},
	} {
		conn, err := proxyTestDialRaw(p)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(conn, test.raw)
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		_ = conn.Close()
		if resp.StatusCode != test.status {
			t.Fatal("proxy accepted authentication or non-CONNECT request")
		}
	}
}

func TestProxyControlStreamingCancellationAndShutdown(t *testing.T) {
	for _, action := range []string{"client-disconnect", "proxy-close"} {
		t.Run(action, func(t *testing.T) {
			canceled := make(chan struct{})
			control := proxyTestTransport(func(r *http.Request) (*http.Response, error) {
				reader, writer := io.Pipe()
				go func() {
					_, _ = io.WriteString(writer, "event: message\ndata: first\n\n")
					<-r.Context().Done()
					_ = writer.CloseWithError(r.Context().Err())
					close(canceled)
				}()
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader, ContentLength: -1}, nil
			})
			p, client, _ := proxyTestStart(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("control hit inference") }), control)
			resp, err := client.Get("https://api.anthropic.com/v1/code/sessions/cse_123/worker/events?cursor=1")
			if err != nil {
				t.Fatal(err)
			}
			frame := make([]byte, len("event: message\ndata: first\n\n"))
			if _, err := io.ReadFull(resp.Body, frame); err != nil || string(frame) != "event: message\ndata: first\n\n" {
				t.Fatalf("SSE was buffered or changed: %v", err)
			}
			if action == "client-disconnect" {
				_ = resp.Body.Close()
			} else {
				if err := p.Close(); err != nil {
					t.Fatal(err)
				}
				_ = resp.Body.Close()
			}
			select {
			case <-canceled:
			case <-time.After(5 * time.Second):
				t.Fatal("upstream stream outlived its owner")
			}
		})
	}
}

func TestProxyInferenceStreamingAndCancellation(t *testing.T) {
	canceled := make(chan struct{})
	_, client, _ := proxyTestStart(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(canceled)
	}), nil)
	r, _ := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", strings.NewReader(`{}`))
	resp, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	frame := make([]byte, len("data: first\n\n"))
	if _, err := io.ReadFull(resp.Body, frame); err != nil || string(frame) != "data: first\n\n" {
		t.Fatalf("inference SSE changed: %v", err)
	}
	_ = resp.Body.Close()
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("inference was not canceled on disconnect")
	}
}

func TestProxyClosesIncompleteConnectAndActiveHandlers(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	p, client, _ := proxyTestStart(t, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(finished)
	}), nil)
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		r, _ := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", strings.NewReader(`{}`))
		resp, err := client.Do(r)
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	<-started
	conn, err := proxyTestDialRaw(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_, _ = fmt.Fprintf(conn, "CONNECT api.anthropic.com:443 HTTP/1.1\r\nHost: api.anthropic.com:443\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("partial TLS setup failed: %v", err)
	}
	var closers sync.WaitGroup
	closers.Add(2)
	for range 2 {
		go func() { defer closers.Done(); _ = p.Close() }()
	}
	closers.Wait()
	<-finished
	<-requestDone
	byteBuffer := make([]byte, 1)
	if _, err := conn.Read(byteBuffer); err == nil {
		t.Fatal("CONNECT socket survived close")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.conns) != 0 {
		t.Fatal("owned sockets remain after close")
	}
}

func TestProxyControlErrorsAreSanitized(t *testing.T) {
	_, client, _ := proxyTestStart(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), proxyTestTransport(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("failed https://secret:MASTER-CANARY@api.anthropic.com/path")
	}))
	resp, body := proxyTestRequest(t, client, http.MethodGet, "/api/claude_cli/bootstrap", "", nil)
	if resp.StatusCode != 502 || strings.Contains(body, "MASTER-CANARY") || body != "control upstream unavailable\n" {
		t.Fatal("control transport error was not sanitized")
	}
}

func TestProxyRejectsIncompleteOptions(t *testing.T) {
	cert, _ := proxyTestCertificate(t)
	auth := newProxyTestAuth(t)
	complete := auth.apply(ProxyOptions{Certificate: cert, Inference: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})})
	noProxyCert, noClientCAs := complete, complete
	noProxyCert.ProxyCertificate, noClientCAs.ClientCAs = nil, nil
	for _, opts := range []ProxyOptions{{}, {Certificate: cert}, {Inference: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})}, noProxyCert, noClientCAs} {
		if p, err := StartProxy(opts); err == nil {
			_ = p.Close()
			t.Fatal("incomplete options accepted")
		}
	}
}

func TestProxyAuthorityValidation(t *testing.T) {
	for _, authority := range []string{"example.com:443", "127.0.0.1:443", "[::1]:443", "API.ANTHROPIC.COM..:443"} {
		if _, err := proxyAuthority(authority); err != nil {
			t.Errorf("valid HTTPS authority rejected: %s", authority)
		}
	}
	for _, authority := range []string{"example.com:0443", "example.com:22", ":443", "[fe80::1%eth0]:443", "user:password@example.com:443", "example.com:443?token=secret"} {
		if _, err := proxyAuthority(authority); err == nil {
			t.Errorf("invalid HTTPS authority accepted: %s", authority)
		}
	}
}

func TestProxyControlRouteInventory(t *testing.T) {
	for _, test := range []struct {
		method, path string
		allowed      bool
	}{
		{"POST", "/v1/environments/bridge", true},
		{"DELETE", "/v1/environments/bridge/env_123-abc", true},
		{"GET", "/v1/environments/bridge", false},
		{"DELETE", "/v1/environments/bridge/", false},
		{"DELETE", "/v1/environments/bridge/abc/nested", false},
		{"DELETE", "/v1/environments/bridge/abc%2Fdef", false},
		{"POST", "/v1/environments/bridge/abc", false},
		{"GET", "/v1/environments/env_123/work/poll", true},
		{"POST", "/v1/environments/env_123/work/work_456/ack", true},
		{"POST", "/v1/environments/env_123/work/work_456/stop", true},
		{"POST", "/v1/environments/env_123/work/work_456/heartbeat", true},
		{"POST", "/v1/environments/env_123/bridge/reconnect", true},
		{"POST", "/v1/environments/env_123/work/poll", false},
		{"GET", "/v1/environments/env_123/work/work_456/ack", false},
		{"PUT", "/v1/environments/env_123/work/work_456/heartbeat", false},
		{"GET", "/v1/environments/env_123/bridge/reconnect", false},
		{"POST", "/v1/environments/env_123/work/work_456/result", false},
		{"POST", "/v1/environments/env_123/work/work_456/messages", false},
		{"GET", "/v1/environments/env_123/work/poll/extra", false},
		{"POST", "/v1/environments/env_123/work/work_456/ack/extra", false},
		{"POST", "/v1/environments/env_123/bridge/reconnect/extra", false},
		{"GET", "/v1/environments//work/poll", false},
		{"GET", "/v1/environments/../work/poll", false},
		{"GET", "/v1/environments/env%2Fother/work/poll", false},
		{"POST", "/v1/environments/env_123/work/../ack", false},
		{"POST", "/v1/environments/env_123/work/work%2Fother/ack", false},
		{"POST", "/v1/code/auth/refresh", true},
		{"GET", "/v1/code/auth/refresh", false},
		{"POST", "/v1/oauth/token", true},
		{"POST", "/v1/code/runners/self-hosted/runners/register", true},
		{"PUT", "/v1/code/runners/self-hosted/runners/register", false},
		{"POST", "/v1/code/agent-proxy", false},
		{"POST", "/v1/code/agent-proxy/messages", false},
		{"GET", "/api/claude_code/settings", true},
		{"GET", "/api/claude_code/policy_limits", true},
		{"GET", "/api/claude_cli_profile", true},
		{"POST", "/api/claude_cli_feedback", true},
		{"POST", "/api/claude_code/unknown", false},
		{"GET", "/api/web/domain_info", true},
		{"GET", "/api/hello", true},
		{"POST", "/api/artifacts/mcp", true},
		{"GET", "/v1/code/mcp/ccr-artifacts", true},
		{"POST", "/api/event_logging/v2/batch", true},
		{"POST", "/v1/mcp/srv_123", true},
		{"POST", "/v1/toolbox/shttp/mcp/srv_123", true},
		{"GET", "/v1/me", true},
		{"GET", "/v1/ultrareview/quota", true},
		{"GET", "/v1/filestore/fs/readFile", true},
		{"POST", "/v1/complete", false},
		{"POST", "/v1/files", false},
		{"POST", "/v1/messages/batches", false},
		{"POST", "/v1/messages/cache_touch", false},
		{"HEAD", "/api/hello", true},
		{"POST", "/api/hello", false},
		{"GET", "/api/hello/extra", false},
		{"POST", "/api/web/domain_info", false},
		{"GET", "/api/web/domain_info/extra", false},
		{"GET", "/api/web/fetch", false},
		{"POST", "/api/eval/sdk-exampleClientKey", true},
		{"GET", "/api/eval/sdk-exampleClientKey", false},
		{"POST", "/api/eval", false},
		{"POST", "/api/eval/", false},
		{"POST", "/api/eval/sdk-exampleClientKey/extra", false},
		{"POST", "/api/eval/..", false},
	} {
		if got := proxyControlPath(test.method, test.path); got != test.allowed {
			t.Errorf("%s %s allowed=%v, expected %v", test.method, test.path, got, test.allowed)
		}
	}
}

func TestProxyEnvironmentControlsUseMasterControlOnly(t *testing.T) {
	var calls atomic.Int32
	var verifying atomic.Bool
	verifying.Store(true)
	_, client, _ := proxyTestStart(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("environment control reached inference")
	}), proxyTestTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.URL.Host != masterAPIHost || (verifying.Load() && (r.URL.RawQuery != "cursor=synthetic%2Bvalue" || r.Header.Get("Authorization") != "Bearer SYNTHETIC-CONTROL")) {
			t.Error("environment control lost fixed target, raw query, or native auth")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != `{}` {
			t.Error("environment control body changed")
		}
		return proxyTestResponse(http.StatusOK, `{"accepted":true}`), nil
	}))
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/v1/environments/env_test/work/poll"},
		{http.MethodPost, "/v1/environments/env_test/work/work_test/ack"},
		{http.MethodPost, "/v1/environments/env_test/work/work_test/stop"},
		{http.MethodPost, "/v1/environments/env_test/work/work_test/heartbeat"},
		{http.MethodPost, "/v1/environments/env_test/bridge/reconnect"},
	} {
		response, _ := proxyTestRequest(t, client, route.method, route.path+"?cursor=synthetic%2Bvalue", `{}`, http.Header{"Authorization": {"Bearer SYNTHETIC-CONTROL"}})
		if response.StatusCode != http.StatusOK {
			t.Fatalf("verified environment control rejected: %d", response.StatusCode)
		}
	}
	if calls.Load() != 5 {
		t.Fatal("not all five environment controls reached fixed control transport")
	}
	// Environment routes this release does not know are relayed as well (compatibility first); encoded or
	// ambiguous ones never leave the proxy.
	verifying.Store(false)
	for _, route := range []struct {
		method, path string
		status       int
	}{
		{http.MethodPost, "/v1/environments/env_test/work/poll", http.StatusOK},
		{http.MethodGet, "/v1/environments/env_test/work/work_test/ack", http.StatusOK},
		{http.MethodPut, "/v1/environments/env_test/work/work_test/heartbeat", http.StatusOK},
		{http.MethodPost, "/v1/environments/env_test/work/work_test/result", http.StatusOK},
		{http.MethodPost, "/v1/environments/env_test/work/work_test/ack/extra", http.StatusOK},
		{http.MethodGet, "/v1/environments/../work/poll", http.StatusBadRequest},
		{http.MethodGet, "/v1/environments/env%2Fother/work/poll", http.StatusBadRequest},
		{http.MethodPost, "/v1/environments/env_test/work/../ack", http.StatusBadRequest},
		{http.MethodPost, "/v1/environments/env_test/work//ack", http.StatusBadRequest},
	} {
		response, _ := proxyTestRequest(t, client, route.method, route.path, `{}`, nil)
		if response.StatusCode != route.status {
			t.Fatalf("invalid environment request returned %d, expected %d", response.StatusCode, route.status)
		}
	}
	if calls.Load() != 10 {
		t.Fatalf("want the five unknown routes relayed and the four encoded ones refused; upstream saw %d", calls.Load())
	}
}

func TestProxyRejectsAnotherProxysClientCertificate(t *testing.T) {
	first, _, _ := proxyTestStart(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), nil)
	second, _, _ := proxyTestStart(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), nil)
	if proxyTestAuthFor(first).certs.ca.SerialNumber.Cmp(proxyTestAuthFor(second).certs.ca.SerialNumber) == 0 {
		t.Fatal("two proxies shared a CA")
	}
	config := proxyTestAuthFor(second).clientTLS(nil)
	config.Certificates = []tls.Certificate{proxyTestAuthFor(first).client}
	conn, err := tls.Dial("tcp", second.Addr(), config)
	if err == nil {
		defer func() { _ = conn.Close() }()
		_, _ = conn.Write([]byte("CONNECT api.anthropic.com:443 HTTP/1.1\r\nHost: api.anthropic.com:443\r\n\r\n"))
		if _, err = http.ReadResponse(bufio.NewReader(conn), nil); err == nil {
			t.Fatal("another proxy's client certificate was accepted")
		}
	}
}

// A canceled control request must not be retried against an alternate transport.
func TestProxyControlCancellationBeforeResponse(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	_, client, _ := proxyTestStart(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), proxyTestTransport(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-r.Context().Done()
		close(finished)
		return nil, r.Context().Err()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.anthropic.com/v1/models", nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		resp, err := client.Do(r)
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	<-started
	cancel()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("control request was not canceled")
	}
	<-done
}

func TestProxySnapshotCountsOnlyRoutingEvents(t *testing.T) {
	p, client, _ := proxyTestStart(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, `{}`)
	}), proxyTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.Body != nil {
			_, _ = io.Copy(io.Discard, r.Body)
		}
		return proxyTestResponse(200, `{}`), nil
	}))
	if initial := p.Snapshot(); initial != (ProxyStats{}) {
		t.Fatalf("new proxy counters are not empty: %+v", initial)
	}
	for _, path := range []string{"/v1/messages", "/v1/code/sessions", "/v1/unknown"} {
		proxyTestRequest(t, client, http.MethodPost, path, `{}`, http.Header{"Authorization": {"Bearer SNAPSHOT-CANARY"}})
	}
	conn, err := proxyTestDialRaw(p)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(conn, "CONNECT api.anthropic.com:80 HTTP/1.1\r\nHost: api.anthropic.com:80\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	_ = conn.Close()
	snapshot := p.Snapshot()
	if snapshot.ConnectAccepted == 0 || snapshot.ConnectRejected != 1 || snapshot.APIRequests != 3 || snapshot.InferenceRequests != 1 || snapshot.ControlRequests != 2 || snapshot.BlockedRequests != 0 || snapshot.ActiveConnections == 0 {
		t.Fatalf("unexpected routing counters: %+v", snapshot)
	}
	// Concurrent reads must never race request counters or the owned socket map.
	var readers sync.WaitGroup
	readers.Add(4)
	for range 4 {
		go func() {
			defer readers.Done()
			for range 100 {
				_ = p.Snapshot()
			}
		}()
	}
	_ = p.Close()
	readers.Wait()
	after := p.Snapshot()
	if after.ActiveConnections != 0 || after.APIRequests != snapshot.APIRequests || after.ConnectRejected != snapshot.ConnectRejected {
		t.Fatalf("shutdown did not retain safe counters and clear sockets: %+v", after)
	}
}

func TestProxySnapshotDistinguishesConnectFromTLSRequests(t *testing.T) {
	p, _, _ := proxyTestStart(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("TLS never completed") }), nil)
	conn, err := proxyTestDialRaw(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_, _ = fmt.Fprintf(conn, "CONNECT api.anthropic.com:443 HTTP/1.1\r\nHost: api.anthropic.com:443\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("CONNECT failed: %v", err)
	}
	snapshot := p.Snapshot()
	if snapshot.ConnectAccepted != 1 || snapshot.APIRequests != 0 || snapshot.InferenceRequests != 0 || snapshot.ControlRequests != 0 || snapshot.ActiveConnections != 1 {
		t.Fatalf("incomplete TLS handshake was counted as an API request: %+v", snapshot)
	}
}

// WebFetch asks api.anthropic.com whether a domain may be fetched before fetching it. Through
// claude-master that question goes to Anthropic as a control request, never to inference.
func TestProxyRelaysTheWebFetchDomainCheck(t *testing.T) {
	var seen string
	control := proxyTestTransport(func(r *http.Request) (*http.Response, error) {
		seen = r.Method + " " + r.URL.Host + r.URL.Path + "?" + r.URL.RawQuery
		return proxyTestResponse(http.StatusOK, `{"domain":"example.com","can_fetch":true}`), nil
	})
	p, client, _ := proxyTestStart(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("the domain check hit inference") }), control)
	resp, body := proxyTestRequest(t, client, http.MethodGet, "/api/web/domain_info?domain=example.com", "", http.Header{"User-Agent": {"claude-cli/test"}})
	if resp.StatusCode != http.StatusOK || body != `{"domain":"example.com","can_fetch":true}` {
		t.Fatalf("domain check = %d %q", resp.StatusCode, body)
	}
	if seen != "GET api.anthropic.com/api/web/domain_info?domain=example.com" {
		t.Fatalf("Anthropic saw %q", seen)
	}
	if snapshot := p.Snapshot(); snapshot.ControlRequests != 1 || snapshot.BlockedRequests != 0 || snapshot.InferenceRequests != 0 {
		t.Fatalf("snapshot = %+v, want one control request", snapshot)
	}
}

// An unlisted route outside the token-spending surface goes through on the session's own login (a new Claude
// Code release keeps working); each shape is noted once. Inference-like ones are refused and never reach
// Anthropic or the pool.
func TestProxyRelaysUnlistedControlRoutesAndNotesEachShapeOnce(t *testing.T) {
	var seen []string
	control := proxyTestTransport(func(r *http.Request) (*http.Response, error) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		return proxyTestResponse(http.StatusOK, `{}`), nil
	})
	p, client, _ := proxyTestStart(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("an unlisted route reached the pool") }), control)
	for _, path := range []string{"/api/claude_code/new_feature", "/v1/code/mcp/ccr-artifacts", "/api/artifacts/mcp", "/mcp-registry/v0/servers"} {
		resp, _ := proxyTestRequest(t, client, http.MethodGet, path, "", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s = %d, want relayed", path, resp.StatusCode)
		}
	}
	if len(seen) != 4 || seen[1] != "GET /v1/code/mcp/ccr-artifacts" {
		t.Fatalf("Anthropic saw %v", seen)
	}
	if snapshot := p.Snapshot(); snapshot.ControlRequests != 4 || snapshot.BlockedRequests != 0 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	// once per shape: two ids of the same route are one shape
	p.noteRoute("GET", "/v1/code/sessions/abc123/extra")
	p.noteRoute("GET", "/v1/code/sessions/def456/extra")
	count := 0
	p.routes.Range(func(k, _ any) bool {
		if strings.Contains(k.(string), "/v1/code/sessions/:id/extra") {
			count++
		}
		return true
	})
	if count != 1 {
		t.Fatalf("route shapes noted %d times, want 1", count)
	}
	if got := proxyRouteShape("/v1/code/sessions/session_01ABCdefGHIjklMNOpqrSTUvw/events"); got != "/v1/code/sessions/:id/events" {
		t.Fatalf("shape %q", got)
	}
}
