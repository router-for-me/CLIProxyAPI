package helps

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// TestNewProxyAwareHTTPClientRelayPriority pins that a relay-bound auth sends
// requests to the relay with the header contract, ignoring ProxyURL entirely.
func TestNewProxyAwareHTTPClientRelayPriority(t *testing.T) {
	var gotTarget, gotPath string
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTarget = r.Header.Get("x-relay-target")
		gotPath = r.Header.Get("x-relay-path")
		w.WriteHeader(http.StatusOK)
	}))
	defer relay.Close()

	auth := &cliproxyauth.Auth{ProxyURL: "http://must-be-ignored:1", RelayBaseURL: relay.URL}
	client := NewProxyAwareHTTPClient(context.Background(), &config.Config{}, auth, 0)
	req, _ := http.NewRequest(http.MethodGet, "https://api.example.com/v1/x", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	resp.Body.Close()
	if gotTarget != "https://api.example.com" || gotPath != "/v1/x" {
		t.Fatalf("relay headers = %q / %q", gotTarget, gotPath)
	}
}

// TestNewUtlsHTTPClientRelayPriority pins the same contract for the uTLS
// client used by Claude/Codex executors.
func TestNewUtlsHTTPClientRelayPriority(t *testing.T) {
	var gotTarget string
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTarget = r.Header.Get("x-relay-target")
		w.WriteHeader(http.StatusOK)
	}))
	defer relay.Close()

	auth := &cliproxyauth.Auth{ProxyURL: "http://must-be-ignored:1", RelayBaseURL: relay.URL}
	client := NewUtlsHTTPClient(context.Background(), &config.Config{}, auth, 0)
	req, _ := http.NewRequest(http.MethodGet, "https://api.example.com/v1/x", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	resp.Body.Close()
	if gotTarget != "https://api.example.com" {
		t.Fatalf("relay target = %q", gotTarget)
	}
}

// TestNewProxyAwareHTTPClientPlainProxyUnaffected guards the existing proxy
// path: without relay fields the client still builds (no panic, no relay).
func TestNewProxyAwareHTTPClientPlainProxyUnaffected(t *testing.T) {
	auth := &cliproxyauth.Auth{ProxyURL: "http://127.0.0.1:1"}
	client := NewProxyAwareHTTPClient(context.Background(), &config.Config{}, auth, 0)
	if client == nil {
		t.Fatal("nil client")
	}
	if client.Transport == nil {
		t.Fatal("proxy auth must produce a transport")
	}
}
