package gemini

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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

func TestGeminiTokenStorage_SaveTokenToFile_RejectsTraversalPath(t *testing.T) {
	ts := &GeminiTokenStorage{Token: "raw-token-data"}
	badPath := t.TempDir() + "/../gemini-token.json"

	err := ts.SaveTokenToFile(badPath)
	if err == nil {
		t.Fatal("expected error for traversal path")
	}
	if !strings.Contains(err.Error(), "invalid token file path") {
		t.Fatalf("expected invalid path error, got %v", err)
	}
}

func TestGeminiAuth_CreateTokenStorage(t *testing.T) {
	auth := NewGeminiAuth()
	conf := &oauth2.Config{
		Endpoint: oauth2.Endpoint{
			AuthURL:  "https://example.com/auth",
			TokenURL: "https://example.com/token",
		},
	}
	token := &oauth2.Token{AccessToken: "token123"}

	ctx := context.Background()
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Path, "/oauth2/v1/userinfo") {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"email":"test@example.com"}`)),
				Header:     make(http.Header),
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Body:       io.NopCloser(strings.NewReader("")),
			Header:     make(http.Header),
		}, nil
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

func TestStartOAuthCallbackListener_Fallback(t *testing.T) {
	busy, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", DefaultCallbackPort))
	if err != nil {
		t.Skipf("default callback port %d unavailable: %v", DefaultCallbackPort, err)
	}
	defer func() {
		if closeErr := busy.Close(); closeErr != nil {
			t.Fatalf("busy.Close failed: %v", closeErr)
		}
	}()

	listener, port, err := startOAuthCallbackListener(DefaultCallbackPort)
	if err != nil {
		t.Fatalf("startOAuthCallbackListener failed: %v", err)
	}
	defer func() {
		if closeErr := listener.Close(); closeErr != nil {
			t.Fatalf("listener.Close failed: %v", closeErr)
		}
	}()

	if port == DefaultCallbackPort {
		t.Fatalf("expected fallback port, got default %d", port)
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
