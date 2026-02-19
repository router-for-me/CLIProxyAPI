package claude

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type rewriteTransport struct {
	target string
	base   http.RoundTripper
}

func (t *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	newReq := req.Clone(req.Context())
	newReq.URL.Scheme = "http"
	newReq.URL.Host = strings.TrimPrefix(t.target, "http://")
	return t.base.RoundTrip(newReq)
}

func TestGenerateAuthURL(t *testing.T) {
	auth := NewClaudeAuth(nil, nil)
	pkce := &PKCECodes{CodeChallenge: "challenge"}
	url, state, err := auth.GenerateAuthURL("test-state", pkce)
	if err != nil {
		t.Fatalf("GenerateAuthURL failed: %v", err)
	}
	if state != "test-state" {
		t.Errorf("got state %q, want test-state", state)
	}
	if !strings.Contains(url, "code_challenge=challenge") {
		t.Errorf("url missing challenge: %s", url)
	}
}

func TestExchangeCodeForTokens(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := tokenResponse{
			AccessToken:  "test-access",
			RefreshToken: "test-refresh",
			ExpiresIn:    3600,
		}
		resp.Account.EmailAddress = "test@example.com"
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	client := &http.Client{
		Transport: &rewriteTransport{
			target: ts.URL,
			base:   http.DefaultTransport,
		},
	}

	auth := NewClaudeAuth(nil, client)
	pkce := &PKCECodes{CodeVerifier: "verifier"}
	resp, err := auth.ExchangeCodeForTokens(context.Background(), "code", "state", pkce)
	if err != nil {
		t.Fatalf("ExchangeCodeForTokens failed: %v", err)
	}

	if resp.TokenData.AccessToken != "test-access" {
		t.Errorf("got access token %q, want test-access", resp.TokenData.AccessToken)
	}
	if resp.TokenData.Email != "test@example.com" {
		t.Errorf("got email %q, want test@example.com", resp.TokenData.Email)
	}
}

func TestRefreshTokens(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := tokenResponse{
			AccessToken:  "new-access",
			RefreshToken: "new-refresh",
			ExpiresIn:    3600,
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	client := &http.Client{
		Transport: &rewriteTransport{
			target: ts.URL,
			base:   http.DefaultTransport,
		},
	}

	auth := NewClaudeAuth(nil, client)
	resp, err := auth.RefreshTokens(context.Background(), "old-refresh")
	if err != nil {
		t.Fatalf("RefreshTokens failed: %v", err)
	}

	if resp.AccessToken != "new-access" {
		t.Errorf("got access token %q, want new-access", resp.AccessToken)
	}
}
