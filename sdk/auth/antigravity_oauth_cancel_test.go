package auth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func reserveFreePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to reserve free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if errClose := ln.Close(); errClose != nil {
		t.Fatalf("failed to release reserved port: %v", errClose)
	}
	return port
}

func TestAntigravityCallbackServerDuplicateRequestDoesNotBlock(t *testing.T) {
	port := reserveFreePort(t)
	srv, boundPort, resultCh, err := startAntigravityCallbackServer(port)
	if err != nil {
		t.Fatalf("startAntigravityCallbackServer failed: %v", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if errShutdown := srv.Shutdown(shutdownCtx); errShutdown != nil {
			t.Errorf("callback server shutdown failed: %v", errShutdown)
		}
	}()

	client := &http.Client{Timeout: 2 * time.Second}

	resp1, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/oauth-callback?code=first&state=s1", boundPort))
	if err != nil {
		t.Fatalf("first callback request failed: %v", err)
	}
	_ = resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first callback status = %d, want %d", resp1.StatusCode, http.StatusOK)
	}

	// Send a duplicate callback while resultCh is still full (unread).
	resp2, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/oauth-callback?code=second&state=s2", boundPort))
	if err != nil {
		t.Fatalf("duplicate callback request blocked or failed: %v", err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("duplicate callback status = %d, want %d", resp2.StatusCode, http.StatusOK)
	}

	select {
	case got := <-resultCh:
		if got.Code != "first" || got.State != "s1" {
			t.Fatalf("resultCh received %+v, want first/s1", got)
		}
	default:
		t.Fatalf("expected first callback result in resultCh")
	}
}

func TestAntigravityLoginRespectsContextCancel(t *testing.T) {
	port := reserveFreePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan error, 1)
	go func() {
		_, err := (AntigravityAuthenticator{}).Login(ctx, &config.Config{}, &LoginOptions{
			NoBrowser:    true,
			CallbackPort: port,
		})
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Login returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Login did not return promptly after context cancellation")
	}
}
