package executor

import (
	"context"
	"crypto/tls"
	"errors"
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

const antigravityOAuth429RetryDelay = "0.479s"

func antigravityOAuth429Body() string {
	return `{
		"error": {
			"code": 429,
			"message": "Resource has been exhausted (e.g. check quota).",
			"details": [{
				"@type": "type.googleapis.com/google.rpc.RetryInfo",
				"retryDelay": "` + antigravityOAuth429RetryDelay + `"
			}]
		}
	}`
}

func startAntigravityOAuthStatusServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			t.Errorf("unexpected antigravity test request path: %s", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	serverURL, errParse := url.Parse(server.URL)
	if errParse != nil {
		t.Fatalf("parse test server URL: %v", errParse)
	}
	useAntigravityRefreshTestTransport(t, serverURL.Host)
	return server
}

func expiredAntigravityRefreshAuth(id string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       id,
		Provider: "antigravity",
		Metadata: map[string]any{
			"refresh_token": "oauth-refresh-token",
			"access_token":  "expired-access",
			"expired":       time.Now().Add(-time.Hour).Format(time.RFC3339),
			"project_id":    "project-refresh",
			"type":          "antigravity",
		},
	}
}

// TestAntigravityRefresh429IsTransientRateLimit pins oauth2.googleapis.com 429s
// as a token-endpoint throttle. Without TransientRateLimit, the short Retry-After
// looks like an exhausted model quota and the conductor climbs BackoffLevel
// toward the 30-minute ceiling.
func TestAntigravityRefresh429IsTransientRateLimit(t *testing.T) {
	resetAntigravityRefreshGroupForTest()
	t.Cleanup(resetAntigravityRefreshGroupForTest)
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)
	cliproxyauth.SetQuotaCooldownDisabled(false)
	t.Cleanup(func() { cliproxyauth.SetQuotaCooldownDisabled(false) })

	startAntigravityOAuthStatusServer(t, http.StatusTooManyRequests, antigravityOAuth429Body())

	executor := &AntigravityExecutor{}
	auth := expiredAntigravityRefreshAuth("auth-antigravity-refresh-429")
	_, errRefresh := executor.Refresh(context.Background(), auth)
	if errRefresh == nil {
		t.Fatal("expected token refresh 429")
	}

	var classified interface{ TransientRateLimit() bool }
	if !errors.As(errRefresh, &classified) {
		t.Fatalf("refresh 429 carries no classification: %T", errRefresh)
	}
	if !classified.TransientRateLimit() {
		t.Fatal("expected oauth token-refresh 429 to be transient so the conductor skips the quota ladder")
	}

	var hinted interface{ RetryAfter() *time.Duration }
	if !errors.As(errRefresh, &hinted) || hinted.RetryAfter() == nil {
		t.Fatalf("expected Retry-After on the refresh 429, got %v", errRefresh)
	}
	wantHint, errHint := time.ParseDuration(antigravityOAuth429RetryDelay)
	if errHint != nil {
		t.Fatalf("parse fixture retry delay: %v", errHint)
	}
	if got := *hinted.RetryAfter(); got != wantHint {
		t.Fatalf("retryAfter = %v, want provider hint %v", got, wantHint)
	}

	var status interface{ StatusCode() int }
	if !errors.As(errRefresh, &status) || status.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("status = %#v, want 429", errRefresh)
	}

	manager := cliproxyauth.NewManager(nil, nil, nil)
	registered := &cliproxyauth.Auth{
		ID:       auth.ID,
		Provider: "antigravity",
		Metadata: map[string]any{"type": "antigravity"},
		ModelStates: map[string]*cliproxyauth.ModelState{
			"gemini-3.6-flash": {
				Status: cliproxyauth.StatusActive,
				Quota:  cliproxyauth.QuotaState{BackoffLevel: 0},
			},
		},
	}
	if _, errRegister := manager.Register(cliproxyauth.WithSkipPersist(context.Background()), registered); errRegister != nil {
		t.Fatalf("Register returned error: %v", errRegister)
	}

	hint := *hinted.RetryAfter()
	result := cliproxyauth.Result{
		AuthID:             registered.ID,
		Provider:           "antigravity",
		Model:              "gemini-3.6-flash",
		Success:            false,
		RetryAfter:         &hint,
		TransientRateLimit: classified.TransientRateLimit(),
		Error: &cliproxyauth.Error{
			Code:       "rate_limit",
			Message:    errRefresh.Error(),
			Retryable:  true,
			HTTPStatus: http.StatusTooManyRequests,
		},
	}

	now := time.Now()
	manager.MarkResult(context.Background(), result)

	updated, ok := manager.GetByID(registered.ID)
	if !ok || updated == nil || updated.ModelStates["gemini-3.6-flash"] == nil {
		t.Fatalf("expected model state after refresh 429")
	}
	state := updated.ModelStates["gemini-3.6-flash"]
	if state.Quota.BackoffLevel != 0 {
		t.Fatalf("expected BackoffLevel 0 on the transient refresh-429 path, got %d", state.Quota.BackoffLevel)
	}
	// Quota first step is 1s; transient floor is 10s. A 479ms hint on the quota
	// ladder would recover in ~1s and increment BackoffLevel.
	if got := state.Quota.NextRecoverAt.Sub(now); got < 9*time.Second {
		t.Fatalf("refresh 429 took the quota ladder: NextRecoverAt delta %v, want at least the 10s transient floor", got)
	}

	manager.MarkResult(context.Background(), result)
	updated, ok = manager.GetByID(registered.ID)
	if !ok || updated == nil || updated.ModelStates["gemini-3.6-flash"] == nil {
		t.Fatalf("expected model state after repeated refresh 429")
	}
	if got := updated.ModelStates["gemini-3.6-flash"].Quota.BackoffLevel; got != 0 {
		t.Fatalf("repeated refresh 429 advanced quota BackoffLevel to %d", got)
	}
}

func TestAntigravityRefresh401IsNotTransientRateLimit(t *testing.T) {
	resetAntigravityRefreshGroupForTest()
	t.Cleanup(resetAntigravityRefreshGroupForTest)

	startAntigravityOAuthStatusServer(t, http.StatusUnauthorized, `{"error":"invalid_grant"}`)

	_, errRefresh := (&AntigravityExecutor{}).Refresh(context.Background(), expiredAntigravityRefreshAuth("auth-antigravity-refresh-401"))
	if errRefresh == nil {
		t.Fatal("expected token refresh 401")
	}
	var status interface{ StatusCode() int }
	if !errors.As(errRefresh, &status) || status.StatusCode() != http.StatusUnauthorized {
		t.Fatalf("status = %#v, want 401", errRefresh)
	}
	var classified interface{ TransientRateLimit() bool }
	if errors.As(errRefresh, &classified) && classified.TransientRateLimit() {
		t.Fatal("refresh 401 classified as transient rate limit")
	}
}
