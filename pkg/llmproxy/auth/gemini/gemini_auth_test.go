package gemini

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/config"
	"golang.org/x/oauth2"
)

func TestGetAuthenticatedClient_ExistingToken(t *testing.T) {
	auth := NewGeminiAuth()

	// Valid token that hasn't expired
	token := &oauth2.Token{
		AccessToken:  "valid-access",
		RefreshToken: "valid-refresh",
		Expiry:       time.Now().Add(1 * time.Hour),
	}

	ts := &GeminiTokenStorage{
		Token: token,
	}

	cfg := &config.Config{}
	client, err := auth.GetAuthenticatedClient(context.Background(), ts, cfg, nil)
	if err != nil {
		t.Fatalf("GetAuthenticatedClient failed: %v", err)
	}

	if client == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestGeminiTokenStorage_SaveAndLoad(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "gemini-token.json")

	ts := &GeminiTokenStorage{
		Token:     "raw-token-data",
		ProjectID: "test-project",
		Email:     "test@example.com",
		Type:      "gemini",
	}

	err := ts.SaveTokenToFile(path)
	if err != nil {
		t.Fatalf("SaveTokenToFile failed: %v", err)
	}

	// Load it back
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	if len(data) == 0 {
		t.Fatal("saved file is empty")
	}
}

func TestGeminiAuth_CreateTokenStorage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth2/v1/userinfo" {
			_, _ = fmt.Fprint(w, `{"email":"test@example.com"}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	auth := NewGeminiAuth()
	conf := &oauth2.Config{
		Endpoint: oauth2.Endpoint{
			AuthURL:  server.URL + "/auth",
			TokenURL: server.URL + "/token",
		},
	}
	token := &oauth2.Token{AccessToken: "token123"}

	ctx := context.Background()
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		mockReq, _ := http.NewRequest(req.Method, server.URL+"/oauth2/v1/userinfo", req.Body)
		return http.DefaultClient.Do(mockReq)
	})

	ctx = context.WithValue(ctx, oauth2.HTTPClient, &http.Client{Transport: transport})

	ts, err := auth.createTokenStorage(ctx, conf, token, "project-123")
	if err != nil {
		t.Fatalf("createTokenStorage failed: %v", err)
	}

	if ts.Email != "test@example.com" || ts.ProjectID != "project-123" {
		t.Errorf("unexpected ts: %+v", ts)
	}
}

func TestGetAuthenticatedClient_Proxy(t *testing.T) {
	auth := NewGeminiAuth()
	ts := &GeminiTokenStorage{
		Token: map[string]any{"access_token": "token"},
	}
	cfg := &config.Config{}
	cfg.ProxyURL = "http://proxy.com:8080"

	client, err := auth.GetAuthenticatedClient(context.Background(), ts, cfg, nil)
	if err != nil {
		t.Fatalf("GetAuthenticatedClient failed: %v", err)
	}
	if client == nil {
		t.Fatal("client is nil")
	}

	// Check SOCKS5 proxy
	cfg.ProxyURL = "socks5://user:pass@socks5.com:1080"
	_, _ = auth.GetAuthenticatedClient(context.Background(), ts, cfg, nil)
}

type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
