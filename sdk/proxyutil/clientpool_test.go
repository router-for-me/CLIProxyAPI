package proxyutil

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// countingListener wraps a net.Listener to atomically count accepted
// connections. This is the ground truth for "how many TCP connections
// were created".
// ---------------------------------------------------------------------------
type countingListener struct {
	net.Listener
	accepted atomic.Int64
}

func (cl *countingListener) Accept() (net.Conn, error) {
	conn, err := cl.Listener.Accept()
	if err != nil {
		return nil, err
	}
	cl.accepted.Add(1)
	return conn, nil
}

func (cl *countingListener) Accepted() int64 {
	return cl.accepted.Load()
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// startCountingServer starts an HTTP server on a random port that counts
// every accepted TCP connection. Returns the listener (for reading the
// counter) and a base URL for making requests.
//
// The server is configured with IdleTimeout to keep connections alive for
// reuse, which is essential for the connection-pool tests.
func startCountingServer(t *testing.T) (*countingListener, string) {
	t.Helper()

	rawListener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("net.Listen: %v", errListen)
	}

	cl := &countingListener{Listener: rawListener}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})

	server := &http.Server{
		Handler:     mux,
		IdleTimeout: 120 * time.Second,
	}
	go func() { _ = server.Serve(cl) }()

	t.Cleanup(func() {
		_ = server.Close()
		_ = cl.Close()
	})

	baseURL := "http://" + cl.Addr().String()
	return cl, baseURL
}

// makeRequest sends a single GET to url using the provided client.
// It fully drains and closes the response body, which is required for
// HTTP/1.1 connection reuse — an unread body prevents the connection
// from returning to the idle pool.
func makeRequest(client *http.Client, url string) error {
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	io.ReadAll(resp.Body) // drain body fully for connection reuse
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Benchmark: shared Transport pool vs new Transport per request
// ---------------------------------------------------------------------------

// BenchmarkNewTransportPerRequest creates a fresh *http.Transport (and thus
// a fresh connection pool) for every request, simulating the pre-optimization
// pattern.
func BenchmarkNewTransportPerRequest(b *testing.B) {
	rawListener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		b.Fatalf("net.Listen: %v", errListen)
	}
	cl := &countingListener{Listener: rawListener}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})
	server := &http.Server{Handler: mux, IdleTimeout: 120 * time.Second}
	go func() { _ = server.Serve(cl) }()
	defer func() { _ = server.Close(); _ = cl.Close() }()

	baseURL := "http://" + cl.Addr().String()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Old pattern: new Transport every time
		transport := &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 100,
			IdleConnTimeout:     90 * time.Second,
		}
		client := &http.Client{Transport: transport}
		if err := makeRequest(client, baseURL); err != nil {
			b.Fatalf("request %d: %v", i, err)
		}
		transport.CloseIdleConnections()
	}
}

// BenchmarkSharedTransportPool reuses a single shared Transport across all
// requests, simulating the post-optimization pattern.
func BenchmarkSharedTransportPool(b *testing.B) {
	rawListener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		b.Fatalf("net.Listen: %v", errListen)
	}
	cl := &countingListener{Listener: rawListener}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})
	server := &http.Server{Handler: mux, IdleTimeout: 120 * time.Second}
	go func() { _ = server.Serve(cl) }()
	defer func() { _ = server.Close(); _ = cl.Close() }()

	baseURL := "http://" + cl.Addr().String()

	// New pattern: single shared Transport
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 100,
		IdleConnTimeout:     90 * time.Second,
	}
	client := &http.Client{Transport: transport}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := makeRequest(client, baseURL); err != nil {
			b.Fatalf("request %d: %v", i, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Functional test: high-concurrency connection count comparison
// ---------------------------------------------------------------------------

// TestConnectionPoolReducesTCPConns verifies that sharing a Transport across
// requests results in significantly fewer TCP connections than creating a new
// Transport per request.
//
// The test runs two scenarios with the same total request count:
//
//   - "old pattern": each request creates its own *http.Transport and
//     immediately closes idle connections (simulating the pre-optimization
//     pattern where Transport is discarded after each request)
//   - "new pattern": requests reuse a single *http.Transport, so idle
//     connections are reused between sequential requests on the same worker
//
// Expected result: the shared pattern should use far fewer TCP connections
// than total requests (roughly 1 per worker), while the old pattern creates
// ~1 connection per request.
func TestConnectionPoolReducesTCPConns(t *testing.T) {
	const (
		totalReqs = 200 // total number of HTTP requests
		workers   = 5   // concurrent workers for both scenarios
	)

	t.Run("old_pattern_new_transport_per_request", func(t *testing.T) {
		listener, baseURL := startCountingServer(t)

		var wg sync.WaitGroup
		var errors atomic.Int64

		reqsPerWorker := totalReqs / workers
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < reqsPerWorker; i++ {
					// Old pattern: fresh Transport per request, then discard it
					transport := &http.Transport{
						MaxIdleConns:        100,
						MaxIdleConnsPerHost: 2,
						IdleConnTimeout:     90 * time.Second,
					}
					client := &http.Client{Transport: transport}
					if err := makeRequest(client, baseURL); err != nil {
						errors.Add(1)
					}
					// Immediately discard the connection pool — this is what
					// happens when Transport is not reused.
					transport.CloseIdleConnections()
				}
			}()
		}
		wg.Wait()

		accepted := listener.Accepted()
		errCount := errors.Load()
		t.Logf("old pattern: total_requests=%d, tcp_connections_accepted=%d, errors=%d",
			totalReqs, accepted, errCount)

		if errCount > 0 {
			t.Fatalf("unexpected errors: %d", errCount)
		}
		// With no connection reuse, each request needs a new TCP conn.
		if accepted < int64(totalReqs)/2 {
			t.Fatalf("expected at least %d connections for old pattern, got %d",
				totalReqs/2, accepted)
		}
	})

	t.Run("new_pattern_shared_transport", func(t *testing.T) {
		listener, baseURL := startCountingServer(t)

		// New pattern: single shared Transport with generous idle limits
		sharedTransport := &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 100, // allow all connections to be kept idle
			IdleConnTimeout:     90 * time.Second,
		}
		sharedClient := &http.Client{Transport: sharedTransport}

		reqsPerWorker := totalReqs / workers

		var wg sync.WaitGroup
		var errors atomic.Int64

		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < reqsPerWorker; i++ {
					if err := makeRequest(sharedClient, baseURL); err != nil {
						errors.Add(1)
					}
				}
			}()
		}
		wg.Wait()

		accepted := listener.Accepted()
		errCount := errors.Load()
		t.Logf("new pattern: total_requests=%d, tcp_connections_accepted=%d, errors=%d",
			totalReqs, accepted, errCount)

		if errCount > 0 {
			t.Fatalf("unexpected errors: %d", errCount)
		}
		// With connection reuse, each worker reuses its connection for
		// sequential requests. We expect ~workers connections total.
		// Allow up to workers*2 for connection churn (e.g., server closing
		// idle connections between requests).
		if accepted > int64(workers*2) {
			t.Fatalf("expected at most %d connections for shared transport with %d workers, got %d — connections are not being reused",
				workers*2, workers, accepted)
		}
	})
}

// TestSharedTransportReturnsSameInstance verifies that SharedTransport returns
// the same *http.Transport instance for the same proxy URL, and different
// instances for different URLs.
func TestSharedTransportReturnsSameInstance(t *testing.T) {
	ResetTransportPool()

	t1 := SharedTransport("direct")
	t2 := SharedTransport("direct")
	if t1 != t2 {
		t.Fatal("SharedTransport should return the same instance for the same URL")
	}

	// "none" is semantically equivalent to "direct" but has a different Raw
	// value, so it gets a separate cache entry. This is acceptable because
	// the cost is just one extra Transport instance.
	t3 := SharedTransport("none")
	// Verify both are non-nil direct transports (Proxy == nil)
	if t1.Proxy != nil || t3.Proxy != nil {
		t.Fatal("both 'direct' and 'none' should produce transports with Proxy == nil")
	}

	// Different proxy URL should return a different instance
	t4 := SharedTransport("http://proxy.example.com:8080")
	if t4 == t1 {
		t.Fatal("SharedTransport should return a different instance for a different proxy URL")
	}

	ResetTransportPool()
}

// TestSharedHTTPClientReusesTransport verifies that SharedHTTPClient creates
// lightweight clients that share the underlying Transport.
func TestSharedHTTPClientReusesTransport(t *testing.T) {
	ResetTransportPool()

	c1 := SharedHTTPClient("direct", 10)
	c2 := SharedHTTPClient("direct", 30)

	if c1.Transport != c2.Transport {
		t.Fatal("SharedHTTPClient should share the same Transport for the same proxy URL")
	}
	if c1.Timeout == c2.Timeout {
		t.Fatal("SharedHTTPClient should respect different timeout values")
	}

	ResetTransportPool()
}

// TestResetTransportPool verifies that ResetTransportPool clears cached transports.
func TestResetTransportPool(t *testing.T) {
	ResetTransportPool()

	_ = SharedTransport("direct")
	_ = SharedTransport("http://proxy.example.com:8080")

	ResetTransportPool()

	// After reset, new calls should create fresh instances
	t1 := SharedTransport("direct")
	if t1 == nil {
		t.Fatal("expected non-nil transport after reset")
	}

	ResetTransportPool()
}

// TestSharedDirectTransportCaching verifies that SharedDirectTransport returns
// the same instance on repeated calls.
func TestSharedDirectTransportCaching(t *testing.T) {
	ResetTransportPool()

	d1 := SharedDirectTransport()
	d2 := SharedDirectTransport()
	if d1 != d2 {
		t.Fatal("SharedDirectTransport should return the same instance")
	}

	ResetTransportPool()
}

// TestConcurrentSharedTransportAccess verifies that SharedTransport is safe
// for concurrent access from multiple goroutines.
func TestConcurrentSharedTransportAccess(t *testing.T) {
	ResetTransportPool()

	const goroutines = 100
	var wg sync.WaitGroup
	transports := make([]*http.Transport, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			transports[idx] = SharedTransport("direct")
		}(i)
	}
	wg.Wait()

	// All goroutines should get the same instance
	first := transports[0]
	for i, tr := range transports {
		if tr != first {
			t.Fatalf("goroutine %d got a different Transport instance", i)
		}
	}

	ResetTransportPool()
}
