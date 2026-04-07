package executor

import (
	"context"
	"net/http"
	"os"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func setAntigravityEnvironmentProxy(t *testing.T, proxyURL string) {
	t.Helper()

	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY"} {
		oldValue, hadValue := os.LookupEnv(key)
		if err := os.Setenv(key, proxyURL); err != nil {
			t.Fatalf("Setenv(%s): %v", key, err)
		}
		cleanupKey := key
		cleanupOldValue := oldValue
		cleanupHadValue := hadValue
		t.Cleanup(func() {
			if cleanupHadValue {
				_ = os.Setenv(cleanupKey, cleanupOldValue)
				return
			}
			_ = os.Unsetenv(cleanupKey)
		})
	}
}

func TestNewAntigravityHTTPClientReusesSharedEnvironmentProxyTransport(t *testing.T) {
	setAntigravityEnvironmentProxy(t, "http://env-proxy.example.com:8080")

	clientA := newAntigravityHTTPClient(context.Background(), &config.Config{}, &cliproxyauth.Auth{}, 0)
	clientB := newAntigravityHTTPClient(context.Background(), &config.Config{}, &cliproxyauth.Auth{}, 0)

	transportA, okA := clientA.Transport.(*http.Transport)
	if !okA {
		t.Fatalf("clientA transport type = %T, want *http.Transport", clientA.Transport)
	}
	transportB, okB := clientB.Transport.(*http.Transport)
	if !okB {
		t.Fatalf("clientB transport type = %T, want *http.Transport", clientB.Transport)
	}

	if transportA != transportB {
		t.Fatal("expected Antigravity environment proxy transport to be shared across clients")
	}
	if transportA == helps.EnvironmentProxyTransport() {
		t.Fatal("expected Antigravity transport to use its HTTP/1.1 clone, not the generic environment proxy transport")
	}
	if transportA.ForceAttemptHTTP2 {
		t.Fatal("expected Antigravity transport to keep HTTP/2 disabled")
	}
}
