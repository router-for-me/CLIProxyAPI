package antigravity

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

func TestBuildAuthURL(t *testing.T) {
	auth := NewAntigravityAuth(nil, nil)
	url := auth.BuildAuthURL("test-state", "http://localhost:8317/callback")
	if !strings.Contains(url, "state=test-state") {
		t.Errorf("url missing state: %s", url)
	}
	if !strings.Contains(url, "redirect_uri=http%3A%2F%2Flocalhost%3A8317%2Fcallback") {
		t.Errorf("url missing redirect_uri: %s", url)
	}
}

func TestExchangeCodeForTokens(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := TokenResponse{
			AccessToken:  "test-access-token",
			RefreshToken: "test-refresh-token",
			ExpiresIn:    3600,
			TokenType:    "Bearer",
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

	auth := NewAntigravityAuth(nil, client)
	resp, err := auth.ExchangeCodeForTokens(context.Background(), "test-code", "http://localhost/callback")
	if err != nil {
		t.Fatalf("ExchangeCodeForTokens failed: %v", err)
	}

	if resp.AccessToken != "test-access-token" {
		t.Errorf("got access token %q, want test-access-token", resp.AccessToken)
	}
}

func TestFetchUserInfo(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(userInfo{Email: "test@example.com"})
	}))
	defer ts.Close()

	client := &http.Client{
		Transport: &rewriteTransport{
			target: ts.URL,
			base:   http.DefaultTransport,
		},
	}

	auth := NewAntigravityAuth(nil, client)
	email, err := auth.FetchUserInfo(context.Background(), "test-token")
	if err != nil {
		t.Fatalf("FetchUserInfo failed: %v", err)
	}

	if email != "test@example.com" {
		t.Errorf("got email %q, want test@example.com", email)
	}
}

func TestFetchProjectID(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"cloudaicompanionProject": "test-project-123",
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

	auth := NewAntigravityAuth(nil, client)
	projectID, err := auth.FetchProjectID(context.Background(), "test-token")
	if err != nil {
		t.Fatalf("FetchProjectID failed: %v", err)
	}

	if projectID != "test-project-123" {
		t.Errorf("got projectID %q, want test-project-123", projectID)
	}
}
