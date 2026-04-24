package management

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestPatchAuthFileFieldsUpdatesUserAgent(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	auth := &coreauth.Auth{
		ID:       "codex-auth.json",
		FileName: "codex-auth.json",
		Provider: "codex",
		Metadata: map[string]any{
			"email":      "codex@example.com",
			"user_agent": "old-ua",
			"user-agent": "legacy-old-ua",
		},
		Attributes: map[string]string{
			"path":              "/tmp/codex-auth.json",
			"header:User-Agent": "old-ua",
			"user_agent":        "legacy-old-ua",
			"user-agent":        "legacy-old-ua",
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	h.tokenStore = store

	body, err := json.Marshal(map[string]any{
		"name":       "codex-auth.json",
		"user_agent": "new-ua",
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/fields", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	h.PatchAuthFileFields(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	updated, ok := manager.GetByID("codex-auth.json")
	if !ok || updated == nil {
		t.Fatal("expected updated auth to exist")
	}
	if got, _ := updated.Metadata["user_agent"].(string); got != "new-ua" {
		t.Fatalf("Metadata[user_agent] = %q, want %q", got, "new-ua")
	}
	if _, ok := updated.Metadata["user-agent"]; ok {
		t.Fatal("Metadata[user-agent] should be removed")
	}
	if got := updated.Attributes["header:User-Agent"]; got != "new-ua" {
		t.Fatalf("Attributes[header:User-Agent] = %q, want %q", got, "new-ua")
	}
	if _, ok := updated.Attributes["user_agent"]; ok {
		t.Fatal("Attributes[user_agent] should be removed")
	}
	if _, ok := updated.Attributes["user-agent"]; ok {
		t.Fatal("Attributes[user-agent] should be removed")
	}
}

func TestBuildAuthFileEntryExposesUserAgent(t *testing.T) {
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	auth := &coreauth.Auth{
		ID:       "codex-auth.json",
		FileName: "codex-auth.json",
		Provider: "codex",
		Metadata: map[string]any{
			"email":      "codex@example.com",
			"user_agent": "codex-cli-test/1.0",
		},
		Attributes: map[string]string{
			"path": "/tmp/codex-auth.json",
		},
	}

	entry := h.buildAuthFileEntry(auth)
	if got, _ := entry["user_agent"].(string); got != "codex-cli-test/1.0" {
		t.Fatalf("entry[user_agent] = %q, want %q", got, "codex-cli-test/1.0")
	}
}

func TestBuildAuthFileEntryExposesWebsockets(t *testing.T) {
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	auth := &coreauth.Auth{
		ID:       "codex-auth.json",
		FileName: "codex-auth.json",
		Provider: "codex",
		Attributes: map[string]string{
			"path":       "/tmp/codex-auth.json",
			"websockets": "true",
		},
	}

	entry := h.buildAuthFileEntry(auth)
	if got, ok := entry["websockets"].(bool); !ok || !got {
		t.Fatalf("entry[websockets] = %#v, want true", entry["websockets"])
	}
}
