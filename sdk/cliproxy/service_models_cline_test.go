package cliproxy

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// clineFileSynthAuthForTest mirrors the auth the file synthesizer produces
// for a cline auth JSON file: provider cline, OAuth metadata, no Storage.
func clineFileSynthAuthForTest(id string) *coreauth.Auth {
	return &coreauth.Auth{
		ID:       id,
		FileName: id,
		Provider: "cline",
		Label:    "user@example.com",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			coreauth.AttributeSource:        "auths/" + id,
			coreauth.AttributePath:          "auths/" + id,
			coreauth.AttributeSourceBackend: coreauth.AuthSourceFile,
			"auth_kind":                     "oauth",
			"base_url":                      "http://127.0.0.1:1/api/v1",
		},
		Metadata: map[string]any{
			"type":          "cline",
			"auth_kind":     "oauth",
			"access_token":  "tok",
			"refresh_token": "rtok",
			"expired":       "2099-01-01T00:00:00Z",
			"email":         "user@example.com",
			"disabled":      false,
		},
	}
}

// TestClineAuthFileModifyKeepsModelsRegistered covers the reported regression:
// after a cline auth file is re-persisted by the server (minified, disabled
// field added), the incremental auth-update path must keep the provider's
// models registered instead of dropping them to zero.
func TestClineAuthFileModifyKeepsModelsRegistered(t *testing.T) {
	ctx := context.Background()
	s := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil)}
	authID := "cline-user@example.com.json"

	// Initial startup registration: store-loaded record shape (no Storage).
	live := clineFileSynthAuthForTest(authID)
	s.applyCoreAuthAddOrUpdate(ctx, live)
	for i := 0; i < 200 && len(GlobalModelRegistry().GetModelsForClient(authID)) == 0; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if got := len(GlobalModelRegistry().GetModelsForClient(authID)); got != 15 {
		t.Fatalf("initial registration = %d models, want 15", got)
	}

	// A successful request bumps the live record generation and triggers the
	// post-auth persist hook with the LIVE record.
	current, ok := s.coreManager.GetByID(authID)
	if !ok {
		t.Fatal("auth missing from core manager")
	}
	hooked := current.Clone()
	hooked.Generation = current.Generation + 1
	hooked.Metadata["disabled"] = false
	hooked.Metadata["last_refresh"] = "2026-09-28T00:00:00Z"
	s.handleAuthUpdates(ctx, []watcher.AuthUpdate{{
		Action: watcher.AuthUpdateActionModify, ID: authID, Auth: hooked,
	}})
	time.Sleep(300 * time.Millisecond)
	if got := len(GlobalModelRegistry().GetModelsForClient(authID)); got != 15 {
		t.Fatalf("after persist-hook update = %d models, want 15", got)
	}

	// The file watcher then fires for the re-persisted (minified + disabled) file,
	// dispatching the synthesized auth (generation 0).
	fileAuth := clineFileSynthAuthForTest(authID)
	fileAuth.Metadata["disabled"] = false
	s.handleAuthUpdates(ctx, []watcher.AuthUpdate{{
		Action: watcher.AuthUpdateActionModify, ID: authID, Auth: fileAuth,
	}})
	time.Sleep(300 * time.Millisecond)

	got := len(GlobalModelRegistry().GetModelsForClient(authID))
	t.Logf("models after persist-hook + file-write sequence: %d", got)
	if got != 15 {
		t.Fatalf("models after file-write sequence = %d, want 15", got)
	}

	// Endpoint-level visibility: quota/suspension/registration state for a sample model.
	for _, modelID := range []string{"anthropic/claude-sonnet-4-6", "cline-pass/glm-5.3"} {
		if !GlobalModelRegistry().ClientSupportsModel(authID, modelID) {
			t.Fatalf("client lost support for %s after file-write", modelID)
		}
	}
	avail := 0
	for _, m := range GlobalModelRegistry().GetAvailableModels("openai") {
		_ = m
		avail++
	}
	t.Logf("GetAvailableModels(openai) count: %d", avail)
}
