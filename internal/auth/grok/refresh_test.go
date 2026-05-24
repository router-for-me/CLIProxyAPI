package grok

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

// TestRefreshAccessToken_FiftyGoroutinesShareOneCall verifies that 50 concurrent
// RefreshAccessToken calls for the same authID collapse into exactly one HTTP
// request to the token endpoint. This guards the rotating-refresh-token race
// (Critic C-B4): if N goroutines independently hit the token endpoint with the
// same refresh_token, only one succeeds and the rest burn the credential.
func TestRefreshAccessToken_FiftyGoroutinesShareOneCall(t *testing.T) {
	t.Parallel()

	var serverCallCount atomic.Int64

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverCallCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(TokenResponse{
			AccessToken:  "new-access-token",
			RefreshToken: "new-refresh-token",
			TokenType:    "Bearer",
			ExpiresIn:    3600,
		})
	}))
	defer ts.Close()

	// Build a GrokAuth whose httpClient rewrites all requests to the test server.
	// This intercepts the call to the real TokenURL constant inside refreshAccessTokenRaw.
	g := &GrokAuth{
		httpClient: &http.Client{
			Transport: &hostRewriteTransport{
				underlying: http.DefaultTransport,
				target:     ts.URL,
			},
		},
	}

	const goroutines = 50
	// Use t.Name() as part of authID so parallel test runs don't share
	// the same oauthflight slot (no reset needed).
	authID := "test-grok-auth-id/" + t.Name()
	refreshToken := "shared-refresh-token"

	var startWg, doneWg sync.WaitGroup
	startWg.Add(1)
	doneWg.Add(goroutines)

	errs := make([]error, goroutines)
	results := make([]*TokenResponse, goroutines)

	for i := range goroutines {
		go func(idx int) {
			defer doneWg.Done()
			startWg.Wait() // all goroutines start simultaneously
			results[idx], errs[idx] = g.RefreshAccessToken(context.Background(), authID, refreshToken)
		}(i)
	}

	startWg.Done() // release all goroutines
	doneWg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: unexpected error: %v", i, err)
		}
	}

	got := serverCallCount.Load()
	if got != 1 {
		t.Errorf("expected exactly 1 server call, got %d (single-flight not working)", got)
	}

	// All results should be identical non-nil values.
	for i, r := range results {
		if r == nil {
			t.Errorf("goroutine %d: got nil result", i)
			continue
		}
		if r.AccessToken != "new-access-token" {
			t.Errorf("goroutine %d: unexpected access token %q", i, r.AccessToken)
		}
	}
}

// hostRewriteTransport rewrites all requests to target, preserving path+query.
type hostRewriteTransport struct {
	underlying http.RoundTripper
	target     string // e.g. "http://127.0.0.1:PORT"
}

func (t *hostRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	parsed, _ := http.NewRequest(http.MethodGet, t.target, nil)
	clone.URL.Scheme = parsed.URL.Scheme
	clone.URL.Host = parsed.URL.Host
	return t.underlying.RoundTrip(clone)
}
