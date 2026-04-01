package codex

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestRefreshTokensWithRetry_NonRetryableOnlyAttemptsOnce(t *testing.T) {
	var calls int32
	auth := &CodexAuth{
		httpClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				atomic.AddInt32(&calls, 1)
				return &http.Response{
					StatusCode: http.StatusBadRequest,
					Body:       io.NopCloser(strings.NewReader(`{"error":"invalid_grant","code":"refresh_token_reused"}`)),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			}),
		},
	}

	_, err := auth.RefreshTokensWithRetry(context.Background(), "dummy_refresh_token", 3)
	if err == nil {
		t.Fatalf("expected error for non-retryable refresh failure")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "refresh_token_reused") {
		t.Fatalf("expected refresh_token_reused in error, got: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected 1 refresh attempt, got %d", got)
	}
}

func TestGenerateAuthURLWithRedirect_UsesProvidedRedirectURI(t *testing.T) {
	auth := &CodexAuth{}
	pkce := &PKCECodes{CodeChallenge: "test-challenge"}

	rawURL, err := auth.GenerateAuthURLWithRedirect("state-123", pkce, "http://example.com:21455/auth/callback")
	if err != nil {
		t.Fatalf("GenerateAuthURLWithRedirect() error = %v", err)
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse auth url: %v", err)
	}

	if got := parsed.Query().Get("redirect_uri"); got != "http://example.com:21455/auth/callback" {
		t.Fatalf("redirect_uri = %q, want %q", got, "http://example.com:21455/auth/callback")
	}
}

func TestRedirectURIForPublicBase(t *testing.T) {
	got, err := RedirectURIForPublicBase("https://proxy.example.com:8318", 21455)
	if err != nil {
		t.Fatalf("RedirectURIForPublicBase() error = %v", err)
	}

	want := "http://proxy.example.com:21455/auth/callback"
	if got != want {
		t.Fatalf("RedirectURIForPublicBase() = %q, want %q", got, want)
	}
}
