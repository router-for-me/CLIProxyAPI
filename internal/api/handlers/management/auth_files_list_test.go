package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestListAuthFilesReflectsManagerBackedAuthChanges(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	nestedDir := filepath.Join(authDir, "nested", "child")
	if err := os.MkdirAll(nestedDir, 0o700); err != nil {
		t.Fatalf("failed to create nested auth dir: %v", err)
	}

	rootPath := filepath.Join(authDir, "root.json")
	nestedPath := filepath.Join(nestedDir, "nested.json")
	if err := os.WriteFile(rootPath, []byte(`{"type":"codex","email":"root@example.com"}`), 0o600); err != nil {
		t.Fatalf("failed to write root auth file: %v", err)
	}
	if err := os.WriteFile(nestedPath, []byte(`{"type":"codex","email":"nested@example.com"}`), 0o600); err != nil {
		t.Fatalf("failed to write nested auth file: %v", err)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	rootAuth := &coreauth.Auth{
		ID:       filepath.Base(rootPath),
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"path": rootPath,
		},
		Metadata: map[string]any{
			"type":  "codex",
			"email": "root@example.com",
		},
	}
	nestedID, err := filepath.Rel(authDir, nestedPath)
	if err != nil {
		t.Fatalf("failed to build nested auth ID: %v", err)
	}
	nestedAuth := &coreauth.Auth{
		ID:       nestedID,
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"path": nestedPath,
		},
		Metadata: map[string]any{
			"type":  "codex",
			"email": "nested@example.com",
		},
	}
	if _, errRegister := manager.Register(context.Background(), rootAuth); errRegister != nil {
		t.Fatalf("failed to register root auth: %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), nestedAuth); errRegister != nil {
		t.Fatalf("failed to register nested auth: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)

	names := listAuthFileNames(t, h)
	sort.Strings(names)
	expected := []string{filepath.Base(rootPath), nestedID}
	sort.Strings(expected)
	if len(names) != len(expected) {
		t.Fatalf("expected %d auth files, got %d (%v)", len(expected), len(names), names)
	}
	for i := range expected {
		if names[i] != expected[i] {
			t.Fatalf("expected auth file names %v, got %v", expected, names)
		}
	}

	if err := os.Remove(rootPath); err != nil {
		t.Fatalf("failed to remove root auth file: %v", err)
	}
	if err := os.Remove(nestedPath); err != nil {
		t.Fatalf("failed to remove nested auth file: %v", err)
	}
	rootAuth.Disabled = true
	rootAuth.Status = coreauth.StatusDisabled
	nestedAuth.Disabled = true
	nestedAuth.Status = coreauth.StatusDisabled
	if _, errUpdate := manager.Update(context.Background(), rootAuth); errUpdate != nil {
		t.Fatalf("failed to update root auth: %v", errUpdate)
	}
	if _, errUpdate := manager.Update(context.Background(), nestedAuth); errUpdate != nil {
		t.Fatalf("failed to update nested auth: %v", errUpdate)
	}

	names = listAuthFileNames(t, h)
	if len(names) != 0 {
		t.Fatalf("expected removed auth files to be hidden, got %v", names)
	}
}

func listAuthFileNames(t *testing.T, h *Handler) []string {
	t.Helper()

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)
	ctx.Request = req
	h.ListAuthFiles(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected list status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var payload struct {
		Files []struct {
			Name string `json:"name"`
		} `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode auth files payload: %v", err)
	}

	names := make([]string, 0, len(payload.Files))
	for _, file := range payload.Files {
		names = append(names, file.Name)
	}
	return names
}
