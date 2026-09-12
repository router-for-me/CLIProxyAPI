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
	if control == nil {
		control = proxyTestTransport(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("unexpected control request")
		})
	}
	p, err := StartProxy(ProxyOptions{Certificate: cert, Inference: inference, ControlTransport: control})
	if err != nil {
		t.Fatal(err)
	}
	proxyURL, err := url.Parse(p.URL())
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
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
		if len(r.Header) != 4 || strings.Contains(fmt.Sprint(r.Header), "MASTER-CANARY") || r.URL.RawQuery != "" || r.URL.Host != "" || r.Host != "" || r.TLS != nil || r.RequestURI != "" {
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
		"X-Account-Id": {"MASTER-CANARY"}, "X-Custom-Identity": {"MASTER-CANARY"}, "Accept": {"MASTER-CANARY"},
		"Anthropic-Version": {"2023-06-01"}, "Anthropic-Beta": {"test-beta"}, "Content-Type": {"application/json"},
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
	var calls atomic.Int32
	p, client, pool := proxyTestStart(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }), proxyTestTransport(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return proxyTestResponse(200, "unexpected"), nil
	}))
	for _, test := range []struct {
		method, path string
		status       int
	}{
		{"POST", "/v1/messages/batches", 403}, {"POST", "/v1/messages/", 403}, {"POST", "/v1/complete", 403},
		{"POST", "/v1/responses", 403}, {"POST", "/v1/unknown", 403}, {"POST", "/api/claude_cli/new_inference", 403},
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
		conn := proxyTestTLSConnection(t, p, pool, "api.anthropic.com:443")
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
	if calls.Load() != 0 {
		t.Fatal("rejected request reached an upstream")
	}
}

func proxyTestTLSConnection(t *testing.T, p *Proxy, pool *x509.CertPool, authority string) *tls.Conn {
	t.Helper()
	proxyURL, _ := url.Parse(p.URL())
	conn, err := net.Dial("tcp", proxyURL.Host)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: %s\r\n\r\n", authority, authority, p.proxyAuth)
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
	proxyURL, _ := url.Parse(p.URL())
	raw, err := net.Dial("tcp", proxyURL.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	pipelined := &proxyPipelinedClientConn{Conn: raw, reader: bufio.NewReader(raw), header: fmt.Sprintf("CONNECT api.anthropic.com:443 HTTP/1.1\r\nHost: api.anthropic.com:443\r\nProxy-Authorization: %s\r\n\r\n", p.proxyAuth)}
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

func TestProxyRejectsConnectAuthoritiesAndProxyAuthentication(t *testing.T) {
	p, _, _ := proxyTestStart(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("unexpected inference") }), nil)
	proxyURL, _ := url.Parse(p.URL())
	for _, authority := range []string{"api.anthropic.com:80", "api.anthropic.com:444", "api.anthropic.com", "other.invalid:80", "user@api.anthropic.com:443", "api.anthropic.com:443/path", "api.anthropic.com%2e:443", "api..anthropic.com:443", "-invalid.example:443"} {
		conn, err := net.Dial("tcp", proxyURL.Host)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: %s\r\n\r\n", authority, authority, p.proxyAuth)
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
		{"CONNECT api.anthropic.com:443 HTTP/1.1\r\nHost: api.anthropic.com:443\r\nProxy-Authorization: Basic SECRET\r\n\r\n", 407},
		{"CONNECT api.anthropic.com:443 HTTP/1.1\r\nHost: api.anthropic.com:443\r\n\r\n", 407},
		{"GET http://api.anthropic.com/v1/messages HTTP/1.1\r\nHost: api.anthropic.com\r\n\r\n", 400},
	} {
		conn, err := net.Dial("tcp", proxyURL.Host)
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
	proxyURL, _ := url.Parse(p.URL())
	conn, err := net.Dial("tcp", proxyURL.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_, _ = fmt.Fprintf(conn, "CONNECT api.anthropic.com:443 HTTP/1.1\r\nHost: api.anthropic.com:443\r\nProxy-Authorization: %s\r\n\r\n", p.proxyAuth)
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
	for _, opts := range []ProxyOptions{{}, {Certificate: cert}, {Inference: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})}} {
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
	} {
		if got := proxyControlPath(test.method, test.path); got != test.allowed {
			t.Errorf("%s %s allowed=%v, expected %v", test.method, test.path, got, test.allowed)
		}
	}
}

func TestProxyPerProcessAuthentication(t *testing.T) {
	first, _, _ := proxyTestStart(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), nil)
	second, _, _ := proxyTestStart(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), nil)
	if first.proxyAuth == second.proxyAuth {
		t.Fatal("proxy authentication was reused between processes")
	}
	proxyURL, _ := url.Parse(second.URL())
	conn, err := net.Dial("tcp", proxyURL.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_, _ = fmt.Fprintf(conn, "CONNECT api.anthropic.com:443 HTTP/1.1\r\nHost: api.anthropic.com:443\r\nProxy-Authorization: %s\r\n\r\n", first.proxyAuth)
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusProxyAuthRequired {
		t.Fatal("another proxy's credential was accepted")
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
