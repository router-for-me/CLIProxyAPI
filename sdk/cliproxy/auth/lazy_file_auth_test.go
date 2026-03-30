package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

type lazyHydrationExecutor struct {
	lastAccessToken string
}

func (e *lazyHydrationExecutor) Identifier() string { return "claude" }

func (e *lazyHydrationExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if auth == nil {
		return cliproxyexecutor.Response{}, nil
	}
	if token, ok := auth.Metadata["access_token"].(string); ok {
		e.lastAccessToken = token
	}
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (e *lazyHydrationExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	ch := make(chan cliproxyexecutor.StreamChunk)
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}

func (e *lazyHydrationExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) { return auth, nil }

func (e *lazyHydrationExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *lazyHydrationExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestManagerExecuteHydratesDeferredFileBackedAuth(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	authPath := filepath.Join(tempDir, "claude-test.json")
	raw := map[string]any{
		"type":            "claude",
		"email":           "user@example.com",
		"access_token":    "token-123",
		"refresh_token":   "refresh-123",
		"token":           map[string]any{"access_token": "nested-token-123", "scope": "all"},
		"request_retry":   2,
		"disable_cooling": true,
		"expires_at":      time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339),
		"client_secret":   "drop-me",
	}
	body, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal auth json: %v", err)
	}
	if err = os.WriteFile(authPath, body, 0o600); err != nil {
		t.Fatalf("write auth json: %v", err)
	}

	manager := NewManager(nil, nil, nil)
	executor := &lazyHydrationExecutor{}
	manager.RegisterExecutor(executor)

	record := &Auth{
		ID:       "claude-test.json",
		FileName: "claude-test.json",
		Provider: "claude",
		Status:   StatusActive,
		Attributes: map[string]string{
			"path": authPath,
		},
		Metadata:  raw,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if _, err = manager.Register(context.Background(), record); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	stored, ok := manager.GetByID(record.ID)
	if !ok || stored == nil {
		t.Fatalf("expected auth to be registered")
	}
	if token, _ := stored.Metadata["access_token"].(string); token != "token-123" {
		t.Fatalf("expected compact auth metadata to keep access_token, got %q", token)
	}
	if _, hasClientSecret := stored.Metadata["client_secret"]; hasClientSecret {
		t.Fatalf("expected in-memory auth metadata to drop non-essential secrets")
	}
	if _, hasTokenMap := stored.Metadata["token"]; !hasTokenMap {
		t.Fatalf("expected in-memory auth metadata to keep token object")
	}
	if !stored.DeferredFileHydration() {
		t.Fatalf("expected deferred file hydration to be enabled")
	}

	registerSchedulerModels(t, "claude", "claude-sonnet", record.ID)

	if _, err = manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: "claude-sonnet"}, cliproxyexecutor.Options{}); err != nil {
		t.Fatalf("execute with deferred auth: %v", err)
	}
	if executor.lastAccessToken != "token-123" {
		t.Fatalf("expected hydrated access token, got %q", executor.lastAccessToken)
	}

	afterExec, ok := manager.GetByID(record.ID)
	if !ok || afterExec == nil {
		t.Fatalf("expected auth after execute")
	}
	if token, _ := afterExec.Metadata["access_token"].(string); token != "token-123" {
		t.Fatalf("expected compact auth metadata to keep access_token after execute, got %q", token)
	}
	if _, hasClientSecret := afterExec.Metadata["client_secret"]; hasClientSecret {
		t.Fatalf("expected manager snapshot to stay compact after execute")
	}
	if !afterExec.DeferredFileHydration() {
		t.Fatalf("expected deferred hydration flag to remain enabled in manager snapshot")
	}
}

func TestCompactMetadataForMemory_KeepsOAuthTokenFields(t *testing.T) {
	t.Parallel()

	meta := map[string]any{
		"type":          "antigravity",
		"access_token":  " access-top ",
		"refresh_token": " refresh-top ",
		"token": map[string]any{
			"access_token":  " nested-access ",
			"refresh_token": " nested-refresh ",
			"scope":         " all ",
		},
		"client_secret": "drop-me",
	}

	compact := CompactMetadataForMemory(meta)
	if compact == nil {
		t.Fatalf("expected compact metadata")
	}
	if token, _ := compact["access_token"].(string); token != "access-top" {
		t.Fatalf("expected top-level access token to be retained, got %q", token)
	}
	if token, _ := compact["refresh_token"].(string); token != "refresh-top" {
		t.Fatalf("expected top-level refresh token to be retained, got %q", token)
	}
	tokenMap, ok := compact["token"].(map[string]any)
	if !ok || len(tokenMap) == 0 {
		t.Fatalf("expected nested token object to be retained")
	}
	if token, _ := tokenMap["access_token"].(string); token != "nested-access" {
		t.Fatalf("expected nested access token to be retained, got %q", token)
	}
	if _, hasClientSecret := compact["client_secret"]; hasClientSecret {
		t.Fatalf("expected non-allowlisted metadata to be removed")
	}
}
