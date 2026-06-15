package helps

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	tls "github.com/refraction-networking/utls"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"golang.org/x/net/http2"
)

type utlsClientRoundTripFunc func(*http.Request) (*http.Response, error)

func (f utlsClientRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type staticAddressDialer struct {
	address string
}

func (d staticAddressDialer) Dial(network, _ string) (net.Conn, error) {
	return net.Dial(network, d.address)
}

func TestNewUtlsHTTPClientReusesCachedRoundTrippers(t *testing.T) {
	resetUtlsRoundTripperCache(t)

	first := NewUtlsHTTPClient(context.Background(), nil, nil, 0)
	second := NewUtlsHTTPClient(context.Background(), nil, nil, 0)

	firstUtls, firstFallback := utlsClientRoundTrippers(t, first)
	secondUtls, secondFallback := utlsClientRoundTrippers(t, second)
	if firstUtls != secondUtls {
		t.Fatal("expected default uTLS RoundTripper to be cached")
	}
	if firstFallback != secondFallback {
		t.Fatal("expected default fallback RoundTripper to be cached")
	}
}

func TestNewUtlsHTTPClientReusesCachedRoundTrippersForProxyKeys(t *testing.T) {
	resetUtlsRoundTripperCache(t)

	tests := []struct {
		name     string
		proxyURL string
	}{
		{name: "direct", proxyURL: "direct"},
		{name: "http proxy", proxyURL: "http://proxy.example.com:8080"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			auth := &cliproxyauth.Auth{ProxyURL: tt.proxyURL}
			first := NewUtlsHTTPClient(context.Background(), nil, auth, 0)
			second := NewUtlsHTTPClient(context.Background(), nil, auth, 0)

			firstUtls, firstFallback := utlsClientRoundTrippers(t, first)
			secondUtls, secondFallback := utlsClientRoundTrippers(t, second)
			if firstUtls != secondUtls {
				t.Fatalf("expected uTLS RoundTripper to be cached for proxy %q", tt.proxyURL)
			}
			if firstFallback != secondFallback {
				t.Fatalf("expected fallback RoundTripper to be cached for proxy %q", tt.proxyURL)
			}
		})
	}
}

func TestNewUtlsHTTPClientUsesContextRoundTripperForProtectedHost(t *testing.T) {
	called := false
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", utlsClientRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		called = true
		if req.URL.Hostname() != "chatgpt.com" {
			t.Fatalf("hostname = %q, want chatgpt.com", req.URL.Hostname())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("{}")),
			Request:    req,
		}, nil
	}))

	client := NewUtlsHTTPClient(ctx, nil, nil, 0)
	resp, err := client.Get("https://chatgpt.com/backend-api/codex/responses")
	if err != nil {
		t.Fatalf("client.Get returned error: %v", err)
	}
	if errClose := resp.Body.Close(); errClose != nil {
		t.Fatalf("response body close returned error: %v", errClose)
	}
	if !called {
		t.Fatal("expected context RoundTripper to handle protected host request")
	}
}

func TestNewUtlsHTTPClientReusesSingleUtlsConnectionForConcurrentProtectedRequests(t *testing.T) {
	resetUtlsRoundTripperCache(t)

	var acceptedConnections atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Host != "api.anthropic.com" {
			t.Errorf("host = %q, want api.anthropic.com", req.Host)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "{}")
	}))
	server.EnableHTTP2 = true
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			acceptedConnections.Add(1)
		}
	}
	server.StartTLS()
	defer server.Close()

	serverTLSConfig := server.Client().Transport.(*http.Transport).TLSClientConfig
	serverHost, _, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split server address: %v", err)
	}
	restoreTLSConfig := overrideUtlsTLSConfig(t, func(_ string) *tls.Config {
		return &tls.Config{
			ServerName: serverHost,
			RootCAs:    serverTLSConfig.RootCAs,
			NextProtos: []string{"h2"},
		}
	})
	defer restoreTLSConfig()

	utlsRT := &utlsRoundTripper{
		connections: make(map[string]*http2.ClientConn),
		pending:     make(map[string]*sync.Cond),
		dialer:      staticAddressDialer{address: server.Listener.Addr().String()},
	}
	utlsRoundTripperCache.mu.Lock()
	utlsRoundTripperCache.items[""] = cachedUtlsRoundTripper{
		utls:     utlsRT,
		fallback: http.DefaultTransport,
	}
	utlsRoundTripperCache.mu.Unlock()

	const requestCount = 32
	var wg sync.WaitGroup
	errs := make(chan error, requestCount)
	for i := 0; i < requestCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client := NewUtlsHTTPClient(context.Background(), nil, nil, 0)
			resp, err := client.Get("https://api.anthropic.com/v1/messages")
			if err != nil {
				errs <- err
				return
			}
			defer func() {
				if errClose := resp.Body.Close(); errClose != nil {
					errs <- errClose
				}
			}()
			if resp.StatusCode != http.StatusOK {
				errs <- fmt.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	if got := acceptedConnections.Load(); got != 1 {
		t.Fatalf("accepted connections = %d, want 1", got)
	}
	utlsRT.mu.Lock()
	cachedConnections := len(utlsRT.connections)
	utlsRT.mu.Unlock()
	if cachedConnections != 1 {
		t.Fatalf("cached uTLS connections = %d, want 1", cachedConnections)
	}
}

func TestNewUtlsHTTPClientDoesNotGrowCachePastLimit(t *testing.T) {
	resetUtlsRoundTripperCache(t)

	for i := 0; i < maxCachedUtlsRoundTrippers; i++ {
		auth := &cliproxyauth.Auth{ProxyURL: fmt.Sprintf("http://proxy-%d.example.com:8080", i)}
		NewUtlsHTTPClient(context.Background(), nil, auth, 0)
	}

	utlsRoundTripperCache.mu.RLock()
	cachedCount := len(utlsRoundTripperCache.items)
	utlsRoundTripperCache.mu.RUnlock()
	if cachedCount != maxCachedUtlsRoundTrippers {
		t.Fatalf("cached round trippers = %d, want %d", cachedCount, maxCachedUtlsRoundTrippers)
	}

	overflowAuth := &cliproxyauth.Auth{ProxyURL: "http://overflow.example.com:8080"}
	client := NewUtlsHTTPClient(context.Background(), nil, overflowAuth, 0)
	overflowUtls, overflowFallback := utlsClientRoundTrippers(t, client)
	if overflowUtls == nil {
		t.Fatal("expected overflow uTLS RoundTripper to be returned")
	}
	if overflowFallback == nil {
		t.Fatal("expected overflow fallback RoundTripper to be returned")
	}

	utlsRoundTripperCache.mu.RLock()
	cachedCount = len(utlsRoundTripperCache.items)
	_, overflowCached := utlsRoundTripperCache.items[overflowAuth.ProxyURL]
	utlsRoundTripperCache.mu.RUnlock()
	if cachedCount != maxCachedUtlsRoundTrippers {
		t.Fatalf("cached round trippers after overflow = %d, want %d", cachedCount, maxCachedUtlsRoundTrippers)
	}
	if overflowCached {
		t.Fatal("expected overflow proxy key not to be cached")
	}
}

func overrideUtlsTLSConfig(t *testing.T, next func(string) *tls.Config) func() {
	t.Helper()

	previous := newUtlsTLSConfig
	newUtlsTLSConfig = next
	return func() {
		newUtlsTLSConfig = previous
	}
}

func utlsClientRoundTrippers(t *testing.T, client *http.Client) (http.RoundTripper, http.RoundTripper) {
	t.Helper()

	fallback, ok := client.Transport.(*fallbackRoundTripper)
	if !ok {
		t.Fatalf("transport type = %T, want *fallbackRoundTripper", client.Transport)
	}
	return fallback.utls, fallback.fallback
}

func resetUtlsRoundTripperCache(t *testing.T) {
	t.Helper()

	utlsRoundTripperCache.mu.Lock()
	previous := utlsRoundTripperCache.items
	utlsRoundTripperCache.items = make(map[string]cachedUtlsRoundTripper)
	utlsRoundTripperCache.mu.Unlock()

	t.Cleanup(func() {
		utlsRoundTripperCache.mu.Lock()
		utlsRoundTripperCache.items = previous
		utlsRoundTripperCache.mu.Unlock()
	})
}
