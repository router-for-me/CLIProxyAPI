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

func TestRefreshTokensUsesCustomTokenURL(t *testing.T) {
	auth := &CodexAuth{
		tokenURL: "https://custom.example.com/oauth/token",
		httpClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if got := req.URL.String(); got != "https://custom.example.com/oauth/token" {
					return nil, io.ErrUnexpectedEOF
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"access_token":"at","refresh_token":"rt","id_token":"","token_type":"Bearer","expires_in":3600}`)),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			}),
		},
	}

	_, err := auth.RefreshTokens(context.Background(), "dummy_refresh_token")
	if err != nil {
		t.Fatalf("RefreshTokens error: %v", err)
	}
}

func TestExchangeCodeForTokensWithRedirectUsesCustomTokenURL(t *testing.T) {
	auth := &CodexAuth{
		tokenURL: "https://custom.example.com/oauth/token",
		httpClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if got := req.URL.String(); got != "https://custom.example.com/oauth/token" {
					return nil, io.ErrUnexpectedEOF
				}
				body, err := io.ReadAll(req.Body)
				if err != nil {
					return nil, err
				}
				values, err := url.ParseQuery(string(body))
				if err != nil {
					return nil, err
				}
				if got := values.Get("redirect_uri"); got != "http://localhost:1455/auth/callback" {
					return nil, io.ErrUnexpectedEOF
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"access_token":"at","refresh_token":"rt","id_token":"","token_type":"Bearer","expires_in":3600}`)),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			}),
		},
	}

	_, err := auth.ExchangeCodeForTokensWithRedirect(context.Background(), "dummy_code", RedirectURI, &PKCECodes{CodeVerifier: "verifier"})
	if err != nil {
		t.Fatalf("ExchangeCodeForTokensWithRedirect error: %v", err)
	}
}
