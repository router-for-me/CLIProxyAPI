package helps

import (
	"bufio"
	"context"
	stdtls "crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"golang.org/x/net/http2"
)

// A fresh TLS connection per request must fail these reuse tests.
func newChromePoolTestServer(t *testing.T, protocol string, handler http.Handler, configure ...func(*httptest.Server)) (*httptest.Server, *utlsRoundTripper, *atomic.Int32) {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.EnableHTTP2 = protocol == "h2"
	server.TLS = &stdtls.Config{NextProtos: []string{}}
	if protocol != "" {
		server.TLS.NextProtos = []string{protocol}
	}
	connections := &atomic.Int32{}
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	for _, apply := range configure {
		apply(server)
	}
	server.StartTLS()
	t.Cleanup(server.Close)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	transport := &utlsRoundTripper{dialer: &net.Dialer{}, rootCAs: roots}
	t.Cleanup(func() {
		if closer, ok := any(transport).(interface{ CloseIdleConnections() }); ok {
			closer.CloseIdleConnections()
		}
	})
	return server, transport, connections
}

func TestChromePoolProxyIdentitySeparatesConnections(t *testing.T) {
	server, fixture, connections := newChromePoolTestServer(t, "h2", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, "answer")
	}))
	for _, proxyURL := range []string{"http://first:test@localhost:1234", "http://second:test@localhost:1234", "http://first:test@localhost:1234"} {
		transport := cachedChromeRoundTripper(proxyURL, &cliproxyauth.Auth{ID: t.Name()})
		// Exercise cache isolation against a real TLS server without requiring
		// an external proxy. Dialing itself is already covered by proxy tests.
		transport.dialer = &net.Dialer{}
		transport.rootCAs = fixture.rootCAs
		t.Cleanup(transport.retire)
		consumeChromePoolResponse(t, chromePoolRequest(t, t.Context(), transport, server.URL))
	}
	if got := connections.Load(); got != 2 {
		t.Fatalf("connections = %d, want one per proxy authentication identity", got)
	}
}

func TestChromePoolHTTP2PostFailureNeverReplayed(t *testing.T) {
	for _, failure := range []string{"refused-stream", "goaway"} {
		t.Run(failure, func(t *testing.T) {
			var requests atomic.Int32
			server, transport, connections := newChromePoolTestServer(t, "h2", http.NotFoundHandler(), func(s *httptest.Server) {
				s.Config.TLSNextProto = map[string]func(*http.Server, *stdtls.Conn, http.Handler){
					"h2": func(_ *http.Server, conn *stdtls.Conn, _ http.Handler) {
						defer func() { _ = conn.Close() }()
						preface := make([]byte, len(http2.ClientPreface))
						if _, err := io.ReadFull(conn, preface); err != nil {
							return
						}
						framer := http2.NewFramer(conn, conn)
						if err := framer.WriteSettings(); err != nil {
							return
						}
						for {
							frame, err := framer.ReadFrame()
							if err != nil {
								return
							}
							if headers, ok := frame.(*http2.HeadersFrame); ok {
								requests.Add(1)
								if failure == "refused-stream" {
									_ = framer.WriteRSTStream(headers.StreamID, http2.ErrCodeRefusedStream)
								} else {
									_ = framer.WriteGoAway(0, http2.ErrCodeNo, nil)
								}
								return
							}
						}
					},
				}
			})
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader("offer"))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Idempotency-Key", "test")
			var replayed atomic.Int32
			req.GetBody = func() (io.ReadCloser, error) {
				replayed.Add(1)
				return io.NopCloser(strings.NewReader("offer")), nil
			}
			resp, err := transport.RoundTrip(req)
			if resp != nil {
				_ = resp.Body.Close()
			}
			if err == nil {
				t.Fatal("expected HTTP/2 rejection")
			}
			if requests.Load() != 1 || connections.Load() != 1 || replayed.Load() != 0 {
				t.Fatalf("requests=%d connections=%d GetBody=%d, want 1/1/0", requests.Load(), connections.Load(), replayed.Load())
			}
		})
	}
}

func TestChromePoolReusesAcrossClientsAndSeparatesCredentials(t *testing.T) {
	server, fixture, connections := newChromePoolTestServer(t, "h2", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, "answer")
	}))
	for _, credential := range []string{"a", "a", "b", "a"} {
		auth := &cliproxyauth.Auth{ID: t.Name() + credential}
		client := NewUtlsHTTPClient(t.Context(), nil, auth, 0)
		transport := client.Transport.(*fallbackRoundTripper).chrome.(*utlsRoundTripper)
		transport.rootCAs = fixture.rootCAs
		t.Cleanup(transport.retire)
		consumeChromePoolResponse(t, chromePoolRequest(t, t.Context(), transport, server.URL))
	}
	if got := connections.Load(); got != 2 {
		t.Fatalf("TLS connections = %d, want one per credential across recreated HTTP clients", got)
	}
}

func TestChromePoolSeparatesOrigins(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, "answer")
	})
	first, transport, firstCount := newChromePoolTestServer(t, "h2", handler)
	second, _, secondCount := newChromePoolTestServer(t, "h2", handler)
	transport.rootCAs.AddCert(second.Certificate())
	for _, address := range []string{first.URL, second.URL, first.URL, second.URL} {
		consumeChromePoolResponse(t, chromePoolRequest(t, t.Context(), transport, address))
	}
	if firstCount.Load() != 1 || secondCount.Load() != 1 {
		t.Fatalf("connections = %d/%d, want one per origin", firstCount.Load(), secondCount.Load())
	}
}

func TestChromePoolIdleCapacityClosesOldestConnections(t *testing.T) {
	server, transport, connections := newChromePoolTestServer(t, "h2", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, "answer")
	}))
	// Keep bodies leased so the requests must use separate physical sockets.
	responses := make([]*http.Response, chromeMaxIdleConnections+1)
	for i := range responses {
		responses[i] = chromePoolRequest(t, t.Context(), transport, server.URL)
	}
	for _, resp := range responses {
		consumeChromePoolResponse(t, resp)
	}
	if got := connections.Load(); got != int32(len(responses)) {
		t.Fatalf("connections = %d, want %d exclusively leased sockets", got, len(responses))
	}
	oldest := responses[0].Body.(*pooledUtlsBody).conn
	if oldest.usable() {
		t.Fatal("oldest connection remains usable after idle capacity overflow")
	}
	if !responses[1].Body.(*pooledUtlsBody).conn.usable() {
		t.Fatal("idle connection within capacity was closed")
	}
}

func TestChromePoolCacheEvictionClosesIdleConnections(t *testing.T) {
	server, fixture, _ := newChromePoolTestServer(t, "h2", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, "answer")
	}))
	transport := cachedChromeRoundTripper("", &cliproxyauth.Auth{ID: t.Name()})
	transport.rootCAs = fixture.rootCAs
	response := chromePoolRequest(t, t.Context(), transport, server.URL)
	consumeChromePoolResponse(t, response)
	for i := range chromeTransportCacheCapacity {
		cachedChromeRoundTripper("", &cliproxyauth.Auth{ID: fmt.Sprintf("%s-%d", t.Name(), i)})
	}
	if response.Body.(*pooledUtlsBody).conn.usable() {
		t.Fatal("cache eviction left an idle physical connection open")
	}
}

func TestChromePoolDialFailureClosesRequestBody(t *testing.T) {
	dialError := errors.New("test dial failure")
	transport := &utlsRoundTripper{dialer: contextDialerFunc(func(context.Context, string, string) (net.Conn, error) {
		return nil, dialError
	})}
	body := &trackedReadCloser{Reader: strings.NewReader("offer")}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://chatgpt.com/", body)
	if err != nil {
		t.Fatal(err)
	}
	_, err = transport.RoundTrip(req)
	if !errors.Is(err, dialError) || body.closeCount != 1 {
		t.Fatalf("error = %v, body close count = %d; want dial error and one close", err, body.closeCount)
	}
}

type chromeTrackedRequestBody struct {
	io.Reader
	closes atomic.Int32
}

func (b *chromeTrackedRequestBody) Close() error {
	b.closes.Add(1)
	return nil
}

func TestChromePoolHTTP1EarlyFailureClosesRequestBodyOnce(t *testing.T) {
	for _, retired := range []bool{false, true} {
		for _, failure := range []string{"invalid-header", "canceled"} {
			t.Run(fmt.Sprintf("%s/retired=%t", failure, retired), func(t *testing.T) {
				server, transport, _ := newChromePoolTestServer(t, "http/1.1", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					_, _ = io.WriteString(w, "answer")
				}))
				consumeChromePoolResponse(t, chromePoolRequest(t, t.Context(), transport, server.URL))
				if retired {
					transport.retire()
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				body := &chromeTrackedRequestBody{Reader: strings.NewReader("offer")}
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, body)
				if err != nil {
					t.Fatal(err)
				}
				if failure == "invalid-header" {
					req.Header.Set("Invalid Header", "value")
				} else {
					cancel()
				}
				resp, err := transport.RoundTrip(req)
				if resp != nil {
					_ = resp.Body.Close()
				}
				if err == nil {
					t.Fatal("expected request failure")
				}
				if got := body.closes.Load(); got != 1 {
					t.Fatalf("request body closed %d times, want exactly once", got)
				}
				if req.Body != body {
					t.Fatal("transport replaced the caller's request body")
				}
			})
		}
	}
}

type prefaceBlockingConn struct {
	net.Conn
	started   chan struct{}
	closed    chan struct{}
	stop      atomic.Bool
	writeOnce sync.Once
}

func (c *prefaceBlockingConn) Write([]byte) (int, error) {
	c.writeOnce.Do(func() { close(c.started) })
	<-c.closed
	return 0, net.ErrClosed
}

func (c *prefaceBlockingConn) Close() error {
	if c.stop.CompareAndSwap(false, true) {
		close(c.closed)
	}
	return c.Conn.Close()
}

func TestChromePoolCancellationDuringHTTP2Preface(t *testing.T) {
	conn, tracked, serverURL := newALPNTestConnection(t, "h2", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	blocked := &prefaceBlockingConn{Conn: tracked.Conn, started: make(chan struct{}), closed: make(chan struct{})}
	tracked.Conn = blocked
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		client, err := newUtlsClientConnection(ctx, conn, strings.TrimPrefix(serverURL, "https://"))
		if client != nil {
			client.closeConnection()
		}
		done <- err
	}()
	select {
	case <-blocked.started:
	case <-time.After(5 * time.Second):
		t.Fatal("HTTP/2 preface was not written")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("initialization error = %v, want cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not interrupt HTTP/2 initialization")
	}
}

func chromePoolRequest(t *testing.T, ctx context.Context, transport http.RoundTripper, address string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, address, strings.NewReader("offer"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func consumeChromePoolResponse(t *testing.T, resp *http.Response) {
	t.Helper()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestChromePoolReusesCompletedConnections(t *testing.T) {
	for _, protocol := range []string{"h2", "http/1.1", ""} {
		t.Run(protocol, func(t *testing.T) {
			var requests atomic.Int32
			server, transport, connections := newChromePoolTestServer(t, protocol, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				wantMajor := 1
				if protocol == "h2" {
					wantMajor = 2
				}
				if r.ProtoMajor != wantMajor {
					t.Errorf("HTTP major = %d, want %d", r.ProtoMajor, wantMajor)
				}
				body, err := io.ReadAll(r.Body)
				if err != nil || string(body) != "offer" {
					t.Errorf("body = %q, error = %v", body, err)
				}
				_, _ = io.WriteString(w, "answer")
			}))
			for range 3 {
				consumeChromePoolResponse(t, chromePoolRequest(t, t.Context(), transport, server.URL))
			}
			if got := requests.Load(); got != 3 {
				t.Fatalf("requests = %d, want 3", got)
			}
			if got := connections.Load(); got != 1 {
				t.Fatalf("physical TLS connections = %d, want 1 for three completed requests", got)
			}
		})
	}
}

func TestChromePoolReusesHTTP2AfterSSETerminal(t *testing.T) {
	released, release := chromePoolLifecycleGate()
	server, transport, connections := newChromePoolTestServer(t, "h2", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\"}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-released:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(release)
	for range 3 {
		resp := chromePoolRequest(t, t.Context(), transport, server.URL)
		scanner := bufio.NewScanner(resp.Body)
		if !scanner.Scan() || !strings.Contains(scanner.Text(), "response.completed") {
			t.Fatalf("missing terminal SSE event: %v", scanner.Err())
		}
		// Codex returns on the terminal event rather than waiting for HTTP EOF.
		if err := resp.Body.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if got := connections.Load(); got != 1 {
		t.Fatalf("TLS connections = %d, want 1 when closing at the terminal SSE event", got)
	}
}
