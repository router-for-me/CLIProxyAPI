package helps

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// All peers in this file are loopback fixtures. Certificate trust is added only
// to each test transport; production certificate verification remains enabled.
func codexHTTP1TestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.EnableHTTP2 = true
	server.TLS = &tls.Config{NextProtos: []string{"h2", "http/1.1"}}
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	t.Cleanup(func() {
		server.CloseClientConnections()
		server.Close()
	})
	return server
}

func codexHTTP1TestTransport(t *testing.T, server *httptest.Server) *http.Transport {
	t.Helper()
	transport, ok := newCodexHTTP1RoundTripper("").(*http.Transport)
	if !ok {
		t.Fatal("valid direct configuration must construct an HTTP transport")
	}
	if transport.TLSClientConfig == nil || transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("transport must retain explicit, verified TLS configuration")
	}
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	if server != nil {
		roots := x509.NewCertPool()
		roots.AddCert(server.Certificate())
		transport.TLSClientConfig.RootCAs = roots
	}
	t.Cleanup(transport.CloseIdleConnections)
	return transport
}

func codexHTTP1WaitClosed(t *testing.T, closed <-chan struct{}) {
	t.Helper()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream connection remained open after body close or cancellation")
	}
}

func TestCodexHTTP1StreamsSSEAndClosesBodyWithoutNegotiatingHTTP2(t *testing.T) {
	type protocol struct {
		major int
		alpn  string
	}
	observed := make(chan protocol, 1)
	closed := make(chan struct{})
	server := codexHTTP1TestServer(t, func(w http.ResponseWriter, r *http.Request) {
		observed <- protocol{r.ProtoMajor, r.TLS.NegotiatedProtocol}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: fixture\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(closed)
	})
	transport := codexHTTP1TestTransport(t, server)
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	line, err := bufio.NewReader(response.Body).ReadString('\n')
	if err != nil || line != "data: fixture\n" {
		t.Fatalf("first flushed SSE line = %q, error = %v", line, err)
	}
	got := <-observed
	if got.major != 1 || got.alpn != "http/1.1" {
		t.Fatalf("negotiated protocol = HTTP/%d, ALPN %q; want HTTP/1.1 despite H2 support", got.major, got.alpn)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	codexHTTP1WaitClosed(t, closed)
}

func TestCodexHTTP1CancellationStopsStalledSSEBody(t *testing.T) {
	closed := make(chan struct{})
	server := codexHTTP1TestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(closed)
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: codexHTTP1TestTransport(t, server), Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	for _, expected := range []string{"data: first\n", "\n"} {
		line, err := reader.ReadString('\n')
		if err != nil || line != expected {
			t.Fatalf("SSE prefix = %q, error = %v; want %q", line, err, expected)
		}
	}
	finished := make(chan error, 1)
	go func() {
		_, err := reader.ReadString('\n')
		finished <- err
	}()
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("stalled body error = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stalled SSE read ignored context cancellation")
	}
	codexHTTP1WaitClosed(t, closed)
}

func TestCodexHTTP1RejectsUntrustedCertificate(t *testing.T) {
	var requests atomic.Int32
	server := codexHTTP1TestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	transport := codexHTTP1TestTransport(t, nil)
	transport.TLSClientConfig.RootCAs = x509.NewCertPool()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	response, err := client.Get(server.URL)
	if response != nil {
		response.Body.Close()
	}
	var untrusted x509.UnknownAuthorityError
	if !errors.As(err, &untrusted) {
		t.Fatalf("TLS error = %v, want x509.UnknownAuthorityError", err)
	}
	if requests.Load() != 0 {
		t.Fatal("request reached the handler before validating certificate trust")
	}
}

func TestCodexHTTP1RejectsWrongHostname(t *testing.T) {
	var requests atomic.Int32
	server := codexHTTP1TestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	transport := codexHTTP1TestTransport(t, server)
	// Route a deliberately wrong URL hostname only to our loopback TLS fixture.
	// No external DNS or network request is made.
	transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
	}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	response, err := client.Get("https://wrong-host.invalid/")
	if response != nil {
		response.Body.Close()
	}
	var hostname x509.HostnameError
	if !errors.As(err, &hostname) {
		t.Fatalf("TLS error = %v, want x509.HostnameError", err)
	}
	if requests.Load() != 0 {
		t.Fatal("request reached the handler before validating the hostname")
	}
}

func TestCodexHTTP1DoesNotReplayDroppedPOSTWithIdempotencyKey(t *testing.T) {
	var posts atomic.Int32
	server := codexHTTP1TestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			fmt.Fprint(w, "warm connection")
			return
		}
		posts.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != `{"fixture":true}` {
			t.Errorf("POST body = %q, error = %v", body, err)
		}
		if r.Header.Get("Idempotency-Key") != "fixture-paid-post" {
			t.Error("fixture Idempotency-Key was lost")
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("POST negotiated a protocol that cannot be hijacked")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		conn.Close() // Ambiguous outcome: body received, response never sent.
	})
	client := &http.Client{Transport: codexHTTP1TestTransport(t, server), Timeout: 5 * time.Second}
	warm, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, readErr := io.Copy(io.Discard, warm.Body)
	closeErr := warm.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatal(err)
	}
	// Warming first makes this catch standard Transport's reused-connection
	// replay of a POST that carries an Idempotency-Key and a GetBody function.
	request, err := http.NewRequest(http.MethodPost, server.URL, strings.NewReader(`{"fixture":true}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Idempotency-Key", "fixture-paid-post")
	if request.GetBody == nil {
		t.Fatal("fixture must be replayable to exercise implicit transport retries")
	}
	response, err := client.Do(request)
	if response != nil {
		response.Body.Close()
	}
	if err == nil {
		t.Fatal("dropped POST unexpectedly succeeded")
	}
	if got := posts.Load(); got != 1 {
		t.Fatalf("POST sends = %d, want exactly one despite replayable body", got)
	}
}

func TestCodexHTTP1DoesNotInheritEnvironmentProxy(t *testing.T) {
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		t.Setenv(name, "http://127.0.0.1:1")
	}
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	server := codexHTTP1TestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "direct")
	})
	transport := codexHTTP1TestTransport(t, server)
	if transport.Proxy != nil {
		t.Fatal("empty proxy configuration must not install ProxyFromEnvironment")
	}
	host, _, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	transport.TLSClientConfig.ServerName = host
	transport.DialContext = func(ctx context.Context, _, addr string) (net.Conn, error) {
		if addr != "fixture-origin.invalid:443" {
			return nil, fmt.Errorf("unexpected proxy dial %q", addr)
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
	}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	// A non-loopback URL avoids the automatic loopback exception in Go's
	// ProxyFromEnvironment; the dialer still reaches only our local fixture.
	response, err := client.Get("https://fixture-origin.invalid/")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || string(body) != "direct" {
		t.Fatalf("direct response = %q, error = %v", body, err)
	}
}

func TestCodexHTTP1InvalidProxyFailsBeforeNetwork(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	for _, proxyURL := range []string{"not-a-proxy", "ftp://127.0.0.1:1"} {
		t.Run(proxyURL, func(t *testing.T) {
			client := &http.Client{Transport: newCodexHTTP1RoundTripper(proxyURL), Timeout: 3 * time.Second}
			response, err := client.Get(server.URL)
			if response != nil {
				response.Body.Close()
			}
			if err == nil {
				t.Fatal("invalid proxy configuration silently fell back to a network connection")
			}
			if got := requests.Load(); got != 0 {
				t.Fatalf("upstream request count = %d, want zero", got)
			}
		})
	}
}
