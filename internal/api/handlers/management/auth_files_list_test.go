package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestListAuthFiles_UsesAuthDirSnapshotForManagedFiles(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	fileName := "codex-user@example.com-plus.json"
	filePath := filepath.Join(authDir, fileName)
	content := []byte(`{"type":"codex","email":"user@example.com"}`)
	if err := os.WriteFile(filePath, content, 0o600); err != nil {
		t.Fatalf("failed to write auth file: %v", err)
	}
	info, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("failed to stat auth file: %v", err)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	record := &coreauth.Auth{
		ID:       fileName,
		FileName: fileName,
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"path": filePath,
		},
		Metadata: map[string]any{
			"email": "user@example.com",
		},
	}
	if _, err := manager.Register(context.Background(), record); err != nil {
		t.Fatalf("failed to register auth record: %v", err)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	resp := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(resp)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)

	h.ListAuthFiles(ctx)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected list status %d, got %d with body %s", http.StatusOK, resp.Code, resp.Body.String())
	}

	var payload struct {
		Files []map[string]any `json:"files"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode payload: %v", err)
	}
	if len(payload.Files) != 1 {
		t.Fatalf("expected 1 auth file, got %d", len(payload.Files))
	}
	if got := payload.Files[0]["name"]; got != fileName {
		t.Fatalf("expected file name %q, got %v", fileName, got)
	}
	if got := int64(payload.Files[0]["size"].(float64)); got != info.Size() {
		t.Fatalf("expected size %d, got %d", info.Size(), got)
	}
	if got := payload.Files[0]["source"]; got != "file" {
		t.Fatalf("expected source file, got %v", got)
	}
}

