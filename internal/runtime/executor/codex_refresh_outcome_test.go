package executor

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	codexauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestCodexUnknownRefreshSurvivesForcedRefreshAndFileAlias(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credential.json")
	if errWrite := os.WriteFile(path, []byte(`{"type":"codex","access_token":"valid","refresh_token":"alias-grant","expired":"2099-01-01T00:00:00Z"}`), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	alias := filepath.Join(t.TempDir(), "alias.json")
	if errSymlink := os.Symlink(path, alias); errSymlink != nil {
		t.Skipf("symlink unavailable: %v", errSymlink)
	}
	calls := 0
	original := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"access_token":`)), Request: req}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = original })
	for _, filename := range []string{path, alias} {
		manager := cliproxyauth.NewManager(nil, nil, nil)
		manager.RegisterExecutor(NewCodexExecutor(&config.Config{}))
		auth := &cliproxyauth.Auth{ID: filename, FileName: filename, Provider: "codex", Status: cliproxyauth.StatusActive, Attributes: map[string]string{cliproxyauth.AttributePath: filename}, Metadata: map[string]any{"access_token": "valid", "refresh_token": "alias-grant", "expired": "2099-01-01T00:00:00Z"}}
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatal(errRegister)
		}
		if _, errRefresh := manager.ForceRefreshAuth(context.Background(), auth.ID); !codexauth.IsRefreshOutcomeUnknown(errRefresh) {
			t.Fatalf("expected unknown outcome, got %v", errRefresh)
		}
		updated, _ := manager.GetByID(auth.ID)
		if !updated.RequiresSignIn || updated.Unavailable {
			t.Fatal("must require refresh recovery but preserve valid access")
		}
	}
	if calls != 1 {
		t.Fatalf("force refresh or file alias replayed unresolved grant: calls=%d, want 1", calls)
	}
}

func TestCodexExecutorPreSendRefreshFailureRecoversOnLaterAttempt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "codex.json")
	now := time.Now().UTC().Truncate(time.Second)
	expiry := now.Add(48 * time.Hour)
	initialJSON := `{"type":"codex","access_token":"valid-access","refresh_token":"wake-ordinary-grant","expired":"` + expiry.Format(time.RFC3339) + `"}`
	if errWrite := os.WriteFile(path, []byte(initialJSON), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}

	var serverCalls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		serverCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"rotated-access","refresh_token":"rotated-grant","expires_in":86400}`)
	}))
	t.Cleanup(server.Close)
	parsedServer, errParse := url.Parse(server.URL)
	if errParse != nil {
		t.Fatal(errParse)
	}

	closedListener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatal(errListen)
	}
	closedAddr := closedListener.Addr().String()
	if errClose := closedListener.Close(); errClose != nil {
		t.Fatal(errClose)
	}

	var failDial atomic.Bool
	failDial.Store(true)
	baseTransport := server.Client().Transport.(*http.Transport).Clone()
	baseTransport.TLSClientConfig = baseTransport.TLSClientConfig.Clone()
	baseTransport.TLSClientConfig.ServerName = "127.0.0.1"
	baseTransport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		var dialer net.Dialer
		if failDial.Load() {
			return dialer.DialContext(ctx, network, closedAddr)
		}
		return dialer.DialContext(ctx, network, parsedServer.Host)
	}
	t.Cleanup(baseTransport.CloseIdleConnections)

	originalTransport := http.DefaultTransport
	http.DefaultTransport = baseTransport
	t.Cleanup(func() { http.DefaultTransport = originalTransport })

	cfg := &config.Config{AuthDir: dir}
	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(NewCodexExecutor(cfg))
	auth := &cliproxyauth.Auth{
		ID:         "codex.json",
		FileName:   "codex.json",
		Provider:   "codex",
		Status:     cliproxyauth.StatusActive,
		Attributes: map[string]string{cliproxyauth.AttributePath: path},
		Metadata: map[string]any{
			"type":          "codex",
			"access_token":  "valid-access",
			"refresh_token": "wake-ordinary-grant",
			"expired":       expiry.Format(time.RFC3339),
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatal(errRegister)
	}

	if _, errFirst := manager.ForceRefreshAuth(context.Background(), auth.ID); errFirst == nil {
		t.Fatal("expected pre-send dial failure on first refresh")
	} else if codexauth.IsRefreshOutcomeUnknown(errFirst) {
		t.Fatalf("pre-send dial failure became permanent unknown outcome: %v", errFirst)
	}

	afterFailure, _ := manager.GetByID(auth.ID)
	if afterFailure.RequiresSignIn || afterFailure.RejectedRefreshToken != "" {
		t.Fatalf("pre-send failure permanently rejected refresh token: RequiresSignIn=%v, RejectedRefreshToken=%q", afterFailure.RequiresSignIn, afterFailure.RejectedRefreshToken)
	}
	if afterFailure.NextRefreshAfter.IsZero() || !afterFailure.NextRefreshAfter.Before(now.Add(time.Hour)) {
		t.Fatalf("NextRefreshAfter = %v, want short retry backoff rather than token expiry %v", afterFailure.NextRefreshAfter, expiry)
	}

	failDial.Store(false)
	refreshed, errSecond := manager.ForceRefreshAuth(context.Background(), auth.ID)
	if errSecond != nil {
		t.Fatalf("recovery refresh after network restored failed: %v", errSecond)
	}
	if got, _ := refreshed.Metadata["access_token"].(string); got != "rotated-access" {
		t.Fatalf("refreshed access_token = %q, want rotated-access", got)
	}
	if got, _ := refreshed.Metadata["refresh_token"].(string); got != "rotated-grant" {
		t.Fatalf("refreshed refresh_token = %q, want rotated-grant", got)
	}
	if serverCalls.Load() != 1 {
		t.Fatalf("serverCalls = %d, want 1", serverCalls.Load())
	}
}
