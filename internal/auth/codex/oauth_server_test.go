package codex

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestOAuthServer(t *testing.T) {
	port := 1456 // Use a different port to avoid conflicts
	server := NewOAuthServer(port)

	if err := server.Start(); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	defer server.Stop(context.Background())

	if !server.IsRunning() {
		t.Error("expected server to be running")
	}

	// Test Start already running
	if err := server.Start(); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Errorf("expected error for already running server, got %v", err)
	}

	// Test callback success
	resp, err := http.Get(fmt.Sprintf("http://localhost:%d/auth/callback?code=abc&state=xyz", port))
	if err != nil {
		t.Fatalf("callback request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK after redirect, got %d", resp.StatusCode)
	}

	result, err := server.WaitForCallback(1 * time.Second)
	if err != nil {
		t.Fatalf("WaitForCallback failed: %v", err)
	}
	if result.Code != "abc" || result.State != "xyz" {
		t.Errorf("expected code abc, state xyz, got %+v", result)
	}
}

func TestOAuthServer_Errors(t *testing.T) {
	port := 1457
	server := NewOAuthServer(port)
	if err := server.Start(); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	defer server.Stop(context.Background())

	// Test error callback
	resp, err := http.Get(fmt.Sprintf("http://localhost:%d/auth/callback?error=access_denied", port))
	if err != nil {
		t.Fatalf("callback request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request, got %d", resp.StatusCode)
	}

	result, _ := server.WaitForCallback(1 * time.Second)
	if result.Error != "access_denied" {
		t.Errorf("expected error access_denied, got %s", result.Error)
	}

	// Test missing code
	http.Get(fmt.Sprintf("http://localhost:%d/auth/callback?state=xyz", port))
	result, _ = server.WaitForCallback(1 * time.Second)
	if result.Error != "no_code" {
		t.Errorf("expected error no_code, got %s", result.Error)
	}

	// Test missing state
	http.Get(fmt.Sprintf("http://localhost:%d/auth/callback?code=abc", port))
	result, _ = server.WaitForCallback(1 * time.Second)
	if result.Error != "no_state" {
		t.Errorf("expected error no_state, got %s", result.Error)
	}

	// Test timeout
	_, err = server.WaitForCallback(10 * time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Errorf("expected timeout error, got %v", err)
	}
}

func TestOAuthServer_PortInUse(t *testing.T) {
	port := 1458
	server1 := NewOAuthServer(port)
	if err := server1.Start(); err != nil {
		t.Fatalf("failed to start server1: %v", err)
	}
	defer server1.Stop(context.Background())

	server2 := NewOAuthServer(port)
	if err := server2.Start(); err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Errorf("expected port in use error, got %v", err)
	}
}

func TestIsValidURL(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"https://example.com", true},
		{"http://example.com", true},
		{"javascript:alert(1)", false},
		{"ftp://example.com", false},
	}
	for _, tc := range cases {
		if isValidURL(tc.url) != tc.want {
			t.Errorf("isValidURL(%q) = %v, want %v", tc.url, isValidURL(tc.url), tc.want)
		}
	}
}
