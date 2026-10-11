package claudemaster

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
)

// startDrainServer runs the real server proxy with an inference handler the test controls.
func startDrainServer(t *testing.T, inference http.HandlerFunc) (*Proxy, *http.Transport) {
	t.Helper()
	stateDir := filepath.Join(canonicalTestTempDir(t), "state")
	certs, err := loadOrCreatePersistentCertificate(stateDir, []net.IP{net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := startServerProxy(certs, "127.0.0.1:0", "", inference)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proxy.Close() })
	return proxy, clientTransport(t, issueTestClient(t, stateDir, "dev-box-1", 30), proxy)
}

func drainPost(transport *http.Transport) (int, http.Header, string, error) {
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	resp, err := client.Post("https://"+masterAPIHost+"/v1/messages", "application/json", strings.NewReader(`{}`))
	if err != nil {
		return 0, nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(body), nil
}

// A stopping server lets a running request finish, and answers a new one with Anthropic's
// retryable 529 meanwhile; Drain returns as soon as the running request is done.
func TestDrainLetsRunningRequestsFinishAndTurnsNewOnesAway(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once bool
	proxy, transport := startDrainServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !once {
			once = true
			close(entered)
			select { // the context too, so a failing assertion cannot hang the cleanup
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		_, _ = io.WriteString(w, "served")
	})
	type result struct {
		status int
		body   string
		err    error
	}
	first := make(chan result, 1)
	go func() {
		status, _, body, err := drainPost(transport)
		first <- result{status, body, err}
	}()
	<-entered
	drained := make(chan struct{})
	go func() { proxy.Drain(5 * time.Second); close(drained) }()
	deadline := time.Now().Add(2 * time.Second)
	for !proxy.isDraining() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case <-drained:
		t.Fatal("Drain returned while a request was still running")
	case <-time.After(100 * time.Millisecond):
	}
	status, headers, body, err := drainPost(transport)
	if err != nil || status != 529 || headers.Get("X-Should-Retry") != "true" || !strings.Contains(body, `"overloaded_error"`) {
		t.Fatalf("a request during the drain must get a retryable 529 overloaded_error: %d %v %q %v", status, headers, body, err)
	}
	close(release)
	got := <-first
	if got.err != nil || got.status != 200 || got.body != "served" {
		t.Fatalf("the running request must finish normally: %+v", got)
	}
	select {
	case <-drained:
	case <-time.After(2 * time.Second):
		t.Fatal("Drain did not return after the running request finished")
	}
}

// A request that does not finish in time is left to Close: Drain returns at its timeout.
func TestDrainGivesUpAtItsTimeout(t *testing.T) {
	entered := make(chan struct{})
	proxy, transport := startDrainServer(t, func(_ http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
	})
	go func() { _, _, _, _ = drainPost(transport) }()
	<-entered
	start := time.Now()
	proxy.Drain(200 * time.Millisecond)
	if waited := time.Since(start); waited < 150*time.Millisecond || waited > 2*time.Second {
		t.Fatalf("Drain waited %v, want about its 200ms timeout", waited)
	}
	_ = proxy.Close()
}

// With nothing running, Drain returns at once; a second Drain, or one after Close, is a no-op.
func TestDrainWithNothingRunningReturnsAtOnce(t *testing.T) {
	proxy, _ := startDrainServer(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "served") })
	start := time.Now()
	proxy.Drain(5 * time.Second)
	proxy.Drain(5 * time.Second)
	_ = proxy.Close()
	proxy.Drain(5 * time.Second)
	if time.Since(start) > time.Second {
		t.Fatal("an idle Drain must not wait")
	}
}

// The core reports a cancelled request as 499. The session gets Anthropic's retryable 529,
// on both the protocol and the native path, never the 499 with the generic line.
func TestCancelledRequestIsARetryable529(t *testing.T) {
	for name, write := range map[string]func(context.Context, http.ResponseWriter, *interfaces.ErrorMessage){
		"protocol": writeBackendUpstreamError,
		"native":   writeBackendNativeUpstreamError,
	} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			write(context.Background(), rec, &interfaces.ErrorMessage{StatusCode: 499, Error: context.Canceled})
			body := rec.Body.String()
			if rec.Code != 529 || rec.Header().Get("X-Should-Retry") != "true" || !strings.Contains(body, `"overloaded_error"`) || strings.Contains(body, "Configured inference failed") {
				t.Fatalf("got %d %v %q", rec.Code, rec.Header(), body)
			}
		})
	}
	// any other status keeps today's path
	rec := httptest.NewRecorder()
	writeBackendUpstreamError(context.Background(), rec, &interfaces.ErrorMessage{StatusCode: 502, Error: errors.New("x")})
	if rec.Code != 502 {
		t.Fatalf("a 502 must stay a 502: %d", rec.Code)
	}
}

// Behind a balancer a draining server keeps serving the connections it has, and closes each after
// its response, so the client's next request opens a new connection (to the other server).
func TestBalancedDrainServesExistingConnectionsAndClosesThem(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var first bool
	proxy, transport := startDrainServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !first {
			first = true
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		_, _ = io.WriteString(w, "served")
	})
	proxy.setBalanced(true)
	go func() { _, _, _, _ = drainPost(transport) }()
	<-entered
	drained := make(chan struct{})
	go func() { proxy.Drain(5 * time.Second); close(drained) }()
	deadline := time.Now().Add(2 * time.Second)
	for !proxy.isDraining() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	resp, err := client.Post("https://"+masterAPIHost+"/v1/messages", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "served" || !resp.Close {
		t.Fatalf("a request during a balanced drain must be served and close its connection: %d %q close=%v", resp.StatusCode, body, resp.Close)
	}
	select {
	case <-drained:
		t.Fatal("Drain returned while a request was still running")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case <-drained:
	case <-time.After(3 * time.Second):
		t.Fatal("Drain did not return after the running request finished")
	}
}

// A balanced drain closes idle keep-alive connections at once, so a client's next request cannot land on the
// old server: it opens a new connection, which the balancer sends to the new one.
func TestBalancedDrainClosesIdleConnectionsAtOnce(t *testing.T) {
	proxy, transport := startDrainServer(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "served") })
	proxy.setBalanced(true)
	var conns atomic.Int32
	dial := transport.DialContext
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		conns.Add(1)
		return dial(ctx, network, addr)
	}
	if status, _, body, err := drainPost(transport); err != nil || status != 200 || body != "served" {
		t.Fatalf("first request: %d %q %v", status, body, err)
	}
	if status, _, _, err := drainPost(transport); err != nil || status != 200 || conns.Load() != 1 {
		t.Fatalf("second request before the drain should reuse the idle connection: %d %v conns=%d", status, err, conns.Load())
	}
	go proxy.Drain(5 * time.Second)
	deadline := time.Now().Add(2 * time.Second)
	for !proxy.isDraining() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // the server closes idle connections; the client notices on its next use
	status, _, body, err := drainPost(transport)
	if err != nil || status != 200 || body != "served" {
		t.Fatalf("request after the drain started: %d %q %v", status, body, err)
	}
	if conns.Load() != 2 {
		t.Fatalf("the request after the drain reused the old idle connection (connections dialled: %d)", conns.Load())
	}
}
