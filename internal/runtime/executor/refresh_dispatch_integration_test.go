package executor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	grokauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/grok"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// redirectingTransport rewrites every request that targets fromURL to toURL,
// leaving the path, query, and body unchanged. Used to intercept calls to the
// real xAI TokenURL constant without monkey-patching globals.
type redirectingTransport struct {
	base    http.RoundTripper
	fromURL string
	toURL   string
}

func (t *redirectingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	from, _ := url.Parse(t.fromURL)
	to, _ := url.Parse(t.toURL)
	if req.URL.Host == from.Host {
		cloned := req.Clone(req.Context())
		cloned.URL.Scheme = to.Scheme
		cloned.URL.Host = to.Host
		cloned.Host = to.Host
		base := t.base
		if base == nil {
			base = http.DefaultTransport
		}
		return base.RoundTrip(cloned)
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

// TestRefreshDispatchInvokesGrokExecutorRefresh asserts that when Refresh is
// called on a *GrokExecutor with an auth that has a non-empty refresh_token,
// the Grok-specific token-exchange path runs — not a no-op inherited from
// OpenAICompatExecutor. The test verifies this by:
//
//  1. Counting HTTP hits on a fake xAI token endpoint.
//  2. Asserting the counter == 1 after Refresh returns.
//  3. Asserting auth.Attributes["access_token"] is updated in-memory.
//
// This is the tripwire for the second v1 architectural defect: if GrokExecutor
// ever loses its own Refresh implementation and falls back to the
// OpenAICompatExecutor no-op, the refresh counter stays at 0 and the test
// fails immediately.
func TestRefreshDispatchInvokesGrokExecutorRefresh(t *testing.T) {
	const (
		oldRefreshToken = "old-rtk-sentinel"
		newAccessToken  = "new-atk-xyz"
		newRefreshToken = "new-rtk-xyz"
	)

	var refreshCount int32

	// Fake xAI token server — counts refresh calls and returns new tokens.
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth2/token" {
			if err := r.ParseForm(); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if r.FormValue("grant_type") != "refresh_token" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if r.FormValue("refresh_token") != oldRefreshToken {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			atomic.AddInt32(&refreshCount, 1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  newAccessToken,
				"refresh_token": newRefreshToken,
				"token_type":    "Bearer",
				"expires_in":    3600,
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer tokenSrv.Close()

	// Build a GrokAuth whose HTTP client rewrites all requests destined for
	// the real grokauth.TokenURL to our fake server, intercepting the refresh
	// call without touching any global state.
	fakeClient := &http.Client{
		Transport: &redirectingTransport{
			fromURL: grokauth.TokenURL,
			toURL:   tokenSrv.URL,
		},
	}

	// Override grokClientFactory on the executor so Refresh uses our fake client.
	e := NewGrokExecutor(&config.Config{})
	e.grokClientFactory = func(_ *config.Config) *grokauth.GrokAuth {
		return grokauth.NewGrokAuthWithClient(fakeClient)
	}

	auth := &cliproxyauth.Auth{
		ID:       "grok-refresh-test",
		Provider: "grok",
		Attributes: map[string]string{
			"access_token":  "old-atk",
			"refresh_token": oldRefreshToken,
		},
	}

	updated, err := e.Refresh(context.Background(), auth)
	if err != nil {
		t.Fatalf("GrokExecutor.Refresh() returned unexpected error: %v", err)
	}

	// Assert (a): fake token server was hit exactly once.
	if got := atomic.LoadInt32(&refreshCount); got != 1 {
		t.Errorf("token server refresh counter = %d; want 1 — GrokExecutor.Refresh did not invoke the Grok token exchange", got)
	}

	// Assert (b): access_token updated in-memory (Attributes).
	if updated == nil {
		t.Fatal("Refresh() returned nil auth")
	}
	if got := updated.Attributes["access_token"]; got != newAccessToken {
		t.Errorf("auth.Attributes[access_token] = %q; want %q", got, newAccessToken)
	}

	// Assert (c): refresh_token rotated.
	if got := updated.Attributes["refresh_token"]; got != newRefreshToken {
		t.Errorf("auth.Attributes[refresh_token] = %q; want %q", got, newRefreshToken)
	}
}
