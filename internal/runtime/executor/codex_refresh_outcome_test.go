package executor

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
