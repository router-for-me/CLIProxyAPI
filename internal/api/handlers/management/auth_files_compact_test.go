package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	fileauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestSyncAuthFileCompactAttribute(t *testing.T) {
	on := &coreauth.Auth{ID: "on", Metadata: map[string]any{"compact": "force_on"}}
	syncAuthFileCompactAttribute(on, &config.Config{})
	if on.Attributes["compact_mode"] != "force_on" || on.Attributes["compact_allowed"] != "true" {
		t.Fatalf("force_on attrs = %#v", on.Attributes)
	}

	off := &coreauth.Auth{ID: "off", Metadata: map[string]any{"compact": "force_off"}}
	syncAuthFileCompactAttribute(off, &config.Config{})
	if off.Attributes["compact_mode"] != "force_off" || off.Attributes["compact_allowed"] != "false" {
		t.Fatalf("force_off attrs = %#v", off.Attributes)
	}

	autoDenied := &coreauth.Auth{ID: "auto", Metadata: map[string]any{"compact": "auto"}}
	syncAuthFileCompactAttribute(autoDenied, &config.Config{CompactDefault: "deny"})
	if autoDenied.Attributes["compact_mode"] != "auto" || autoDenied.Attributes["compact_allowed"] != "false" {
		t.Fatalf("auto attrs = %#v", autoDenied.Attributes)
	}

	cleared := &coreauth.Auth{ID: "c", Attributes: map[string]string{"compact_mode": "force_on", "compact_allowed": "true"}}
	syncAuthFileCompactAttribute(cleared, &config.Config{}) // no Metadata["compact"] -> remove mode
	if _, ok := cleared.Attributes["compact_mode"]; ok {
		t.Fatalf("compact_mode should be removed, got %#v", cleared.Attributes)
	}
	if _, ok := cleared.Attributes["compact_allowed"]; ok {
		t.Fatalf("compact_allowed should be removed, got %#v", cleared.Attributes)
	}
}

func TestBuildAuthFileEntry_ExposesCompact(t *testing.T) {
	h := &Handler{}
	auth := &coreauth.Auth{ID: "x", Provider: "codex", Attributes: map[string]string{"runtime_only": "true", "compact_mode": "force_off"}}
	entry := h.buildAuthFileEntry(auth)
	if entry["compact"] != "force_off" {
		t.Fatalf("entry[compact] = %v, want force_off", entry["compact"])
	}
}

func TestPatchAuthFileFields_CompactAutoResolvesAgainstDefaultAndPersists(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	fileName := "codex.json"
	filePath := filepath.Join(authDir, fileName)
	store := fileauth.NewFileTokenStore()
	store.SetBaseDir(authDir)
	manager := coreauth.NewManager(store, nil, nil)
	record := &coreauth.Auth{
		ID:       fileName,
		FileName: fileName,
		Provider: "codex",
		Attributes: map[string]string{
			"path": filePath,
		},
		Metadata: map[string]any{
			"type": "codex",
		},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir, CompactDefault: "deny"}, manager)

	body := `{"name":"codex.json","compact":"AUTO"}`
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/fields", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	h.PatchAuthFileFields(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	updated, ok := manager.GetByID(fileName)
	if !ok || updated == nil {
		t.Fatalf("expected auth record to exist after patch")
	}
	if got := updated.Attributes["compact_mode"]; got != "auto" {
		t.Fatalf("compact_mode = %q, want auto", got)
	}
	if got := updated.Attributes["compact_allowed"]; got != "false" {
		t.Fatalf("compact_allowed = %q, want false", got)
	}
	if got, ok := updated.Metadata["compact"].(string); !ok || got != "auto" {
		t.Fatalf("metadata.compact = %#v, want auto", updated.Metadata["compact"])
	}

	raw, errRead := os.ReadFile(filePath)
	if errRead != nil {
		t.Fatalf("failed to read updated auth file: %v", errRead)
	}
	var data map[string]any
	if errUnmarshal := json.Unmarshal(raw, &data); errUnmarshal != nil {
		t.Fatalf("failed to unmarshal updated auth file: %v", errUnmarshal)
	}
	if got := data["compact"]; got != "auto" {
		t.Fatalf("persisted compact = %#v, want auto", got)
	}
}

func TestPatchAuthFileFields_InvalidCompactRejected(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	record := &coreauth.Auth{
		ID:       "codex.json",
		FileName: "codex.json",
		Provider: "codex",
		Attributes: map[string]string{
			"path": "/tmp/codex.json",
		},
		Metadata: map[string]any{
			"type": "codex",
		},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	body := `{"name":"codex.json","compact":"garbage"}`
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/fields", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	h.PatchAuthFileFields(ctx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, rec.Code, rec.Body.String())
	}

	updated, ok := manager.GetByID("codex.json")
	if !ok || updated == nil {
		t.Fatalf("expected auth record to exist after patch")
	}
	if _, exists := updated.Metadata["compact"]; exists {
		t.Fatalf("expected invalid compact not to persist, metadata = %#v", updated.Metadata)
	}
}
