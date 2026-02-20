package iflow

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

func TestAuthorizationURL(t *testing.T) {
	auth := NewIFlowAuth(nil, nil)
	url, redirect := auth.AuthorizationURL("test-state", 12345)
	if !strings.Contains(url, "state=test-state") {
		t.Errorf("url missing state: %s", url)
	}
	if redirect != "http://localhost:12345/oauth2callback" {
		t.Errorf("got redirect %q, want http://localhost:12345/oauth2callback", redirect)
	}
}

func TestExchangeCodeForTokens(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "token") {
			resp := map[string]any{
				"access_token":  "test-access",
				"refresh_token": "test-refresh",
				"expires_in":    3600,
			}
			_ = json.NewEncoder(w).Encode(resp)
		} else if strings.Contains(r.URL.Path, "getUserInfo") {
			resp := map[string]any{
				"success": true,
				"data": map[string]any{
					"email":  "test@example.com",
					"apiKey": "test-api-key",
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
		} else if strings.Contains(r.URL.Path, "apikey") {
			resp := map[string]any{
				"success": true,
				"data": map[string]any{
					"apiKey": "test-api-key",
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
		}
	}))
	defer ts.Close()

	client := &http.Client{
		Transport: &rewriteTransport{
			target: ts.URL,
			base:   http.DefaultTransport,
		},
	}

	auth := NewIFlowAuth(nil, client)
	resp, err := auth.ExchangeCodeForTokens(context.Background(), "code", "redirect")
	if err != nil {
		t.Fatalf("ExchangeCodeForTokens failed: %v", err)
	}

	if resp.AccessToken != "test-access" {
		t.Errorf("got access token %q, want test-access", resp.AccessToken)
	}
	if resp.APIKey != "test-api-key" {
		t.Errorf("got API key %q, want test-api-key", resp.APIKey)
	}
}
