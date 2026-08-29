package executor

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"golang.org/x/sync/singleflight"
)

func resetAntigravityRefreshGroupForTest() {
	antigravityRefreshGroup = singleflight.Group{}
}

func useAntigravityRefreshTestTransport(t *testing.T, targetHost string) {
	t.Helper()

	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialer := net.Dialer{}
			return dialer.DialContext(ctx, network, targetHost)
		},
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
		ForceAttemptHTTP2: false,
	}
	originalBase := antigravityBaseTransport
	antigravityBaseTransport = transport
	antigravityTransports.Purge()
	t.Cleanup(func() {
		antigravityBaseTransport = originalBase
		antigravityTransports.Purge()
	})
}

func TestAntigravityEnsureAccessTokenUsesFiveMinuteSafetyWindow(t *testing.T) {
	t.Parallel()

	executor := &AntigravityExecutor{}
	now := time.Now()

	t.Run("uses token outside safety window", func(t *testing.T) {
		auth := &cliproxyauth.Auth{Metadata: map[string]any{
			"access_token": "still-valid-access",
			"expired":      now.Add(antigravityRequestTokenSafetyWindow + time.Minute).Format(time.RFC3339),
		}}

		token, updated, errToken := executor.ensureAccessToken(context.Background(), auth)
		if errToken != nil {
			t.Fatalf("ensureAccessToken() error = %v", errToken)
		}
		if token != "still-valid-access" || updated != nil {
			t.Fatalf("ensureAccessToken() = %q, %#v, want existing token and nil update", token, updated)
		}
	})

	t.Run("uses relative expiry with issued_at seconds", func(t *testing.T) {
		issuedAt := now.Add(-10 * time.Minute).Truncate(time.Second)
		auth := &cliproxyauth.Auth{Metadata: map[string]any{
			"access_token": "relative-expiry-access",
			"expires_in":   3600,
			"issued_at":    issuedAt.Unix(),
		}}

		token, updated, errToken := executor.ensureAccessToken(context.Background(), auth)
		if errToken != nil {
			t.Fatalf("ensureAccessToken() error = %v", errToken)
		}
		if token != "relative-expiry-access" || updated != nil {
			t.Fatalf("ensureAccessToken() = %q, %#v, want existing token and nil update", token, updated)
		}
	})

	t.Run("refreshes token inside safety window", func(t *testing.T) {
		auth := &cliproxyauth.Auth{Metadata: map[string]any{
			"access_token": "expiring-access",
			"expired":      now.Add(antigravityRequestTokenSafetyWindow - time.Minute).Format(time.RFC3339),
		}}

		token, updated, errToken := executor.ensureAccessToken(context.Background(), auth)
		if errToken == nil || !strings.Contains(errToken.Error(), "missing refresh token") {
			t.Fatalf("ensureAccessToken() error = %v, want refresh attempt", errToken)
		}
		if token != "" || updated != nil {
			t.Fatalf("ensureAccessToken() = %q, %#v, want empty result after failed refresh", token, updated)
		}
	})
}

func TestAntigravityRefresh_DeduplicatesConcurrentRefresh(t *testing.T) {
	resetAntigravityRefreshGroupForTest()
	t.Cleanup(resetAntigravityRefreshGroupForTest)
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	var tokenCalls int32
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			atomic.AddInt32(&tokenCalls, 1)
			once.Do(func() { close(started) })
			<-release
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{
				"access_token":"new-access",
				"refresh_token":"new-refresh",
				"token_type":"Bearer",
				"expires_in":3600
			}`)
		case "/v1internal:loadCodeAssist":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"paidTier":{"id":"tier","availableCredits":[]}}`)
		default:
			t.Errorf("unexpected antigravity test request path: %s", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	defer server.Close()

	serverURL, errParse := url.Parse(server.URL)
	if errParse != nil {
		t.Fatalf("parse test server URL: %v", errParse)
	}
	useAntigravityRefreshTestTransport(t, serverURL.Host)

	executor := &AntigravityExecutor{}
	authA := &cliproxyauth.Auth{
		ID:       "auth-a",
		Provider: "antigravity",
		Metadata: map[string]any{
			"refresh_token": "shared-refresh-token",
			"project_id":    "project-a",
		},
	}
	authB := &cliproxyauth.Auth{
		ID:       "auth-b",
		Provider: "antigravity",
		Metadata: map[string]any{
			"refresh_token": "shared-refresh-token",
			"project_id":    "project-b",
		},
	}

	results := make(chan *cliproxyauth.Auth, 2)
	errs := make(chan error, 2)
	runRefresh := func(auth *cliproxyauth.Auth, launched chan<- struct{}) {
		if launched != nil {
			close(launched)
		}
		updated, errRefresh := executor.Refresh(context.Background(), auth)
		results <- updated
		errs <- errRefresh
	}

	go runRefresh(authA, nil)
	<-started

	secondLaunched := make(chan struct{})
	go runRefresh(authB, secondLaunched)
	<-secondLaunched
	time.Sleep(20 * time.Millisecond)
	if got := atomic.LoadInt32(&tokenCalls); got != 1 {
		t.Fatalf("expected concurrent refresh to share a single upstream token call, got %d", got)
	}
	close(release)

	for i := 0; i < 2; i++ {
		if errRefresh := <-errs; errRefresh != nil {
			t.Fatalf("expected refresh to succeed, got %v", errRefresh)
		}
		updated := <-results
		if updated == nil {
			t.Fatal("expected refreshed auth, got nil")
		}
		if got := metaStringValue(updated.Metadata, "access_token"); got != "new-access" {
			t.Fatalf("access_token = %q, want new-access", got)
		}
		if got := metaStringValue(updated.Metadata, "refresh_token"); got != "new-refresh" {
			t.Fatalf("refresh_token = %q, want new-refresh", got)
		}
		if projectID := strings.TrimSpace(updated.Metadata["project_id"].(string)); projectID == "" {
			t.Fatalf("expected project_id to stay on refreshed auth: %#v", updated.Metadata)
		}
	}
	if got := atomic.LoadInt32(&tokenCalls); got != 1 {
		t.Fatalf("expected both refresh callers to share a single upstream token call, got %d", got)
	}
}
