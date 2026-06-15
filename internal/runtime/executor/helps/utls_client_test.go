package helps

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type utlsClientRoundTripFunc func(*http.Request) (*http.Response, error)

func (f utlsClientRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
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
