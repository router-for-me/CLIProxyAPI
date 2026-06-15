package helps

import (
	"context"
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
	t.Parallel()

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
	t.Parallel()

	tests := []struct {
		name     string
		proxyURL string
	}{
		{name: "direct", proxyURL: "direct"},
		{name: "http proxy", proxyURL: "http://proxy.example.com:8080"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

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
	t.Parallel()

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

func utlsClientRoundTrippers(t *testing.T, client *http.Client) (http.RoundTripper, http.RoundTripper) {
	t.Helper()

	fallback, ok := client.Transport.(*fallbackRoundTripper)
	if !ok {
		t.Fatalf("transport type = %T, want *fallbackRoundTripper", client.Transport)
	}
	return fallback.utls, fallback.fallback
}
