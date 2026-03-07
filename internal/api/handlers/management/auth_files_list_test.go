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

func TestListAuthFiles_UsesNestedRelativePathSnapshot(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	firstPath := filepath.Join(authDir, "a", "foo.json")
	secondPath := filepath.Join(authDir, "b", "foo.json")
	if err := os.MkdirAll(filepath.Dir(firstPath), 0o700); err != nil {
		t.Fatalf("failed to create first dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(secondPath), 0o700); err != nil {
		t.Fatalf("failed to create second dir: %v", err)
	}
	firstContent := []byte(`{"type":"codex","email":"a@example.com"}`)
	secondContent := []byte(`{"type":"codex","email":"nested-longer@example.com","note":"this file is intentionally longer"}`)
	if err := os.WriteFile(firstPath, firstContent, 0o600); err != nil {
		t.Fatalf("failed to write first auth file: %v", err)
	}
	if err := os.WriteFile(secondPath, secondContent, 0o600); err != nil {
		t.Fatalf("failed to write second auth file: %v", err)
	}
	firstInfo, err := os.Stat(firstPath)
	if err != nil {
		t.Fatalf("failed to stat first auth file: %v", err)
	}
	secondInfo, err := os.Stat(secondPath)
	if err != nil {
		t.Fatalf("failed to stat second auth file: %v", err)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	for _, record := range []*coreauth.Auth{
		{
			ID:       filepath.Join("a", "foo.json"),
			FileName: filepath.Join("a", "foo.json"),
			Provider: "codex",
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"path": firstPath,
			},
		},
		{
			ID:       filepath.Join("b", "foo.json"),
			FileName: filepath.Join("b", "foo.json"),
			Provider: "codex",
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"path": secondPath,
			},
		},
	} {
		if _, err := manager.Register(context.Background(), record); err != nil {
			t.Fatalf("failed to register auth record %s: %v", record.ID, err)
		}
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
	if len(payload.Files) != 2 {
		t.Fatalf("expected 2 auth files, got %d", len(payload.Files))
	}

	entriesByPath := make(map[string]map[string]any, len(payload.Files))
	for _, file := range payload.Files {
		path, _ := file["path"].(string)
		entriesByPath[path] = file
	}
	if got := int64(entriesByPath[firstPath]["size"].(float64)); got != firstInfo.Size() {
		t.Fatalf("expected first size %d, got %d", firstInfo.Size(), got)
	}
	if got := int64(entriesByPath[secondPath]["size"].(float64)); got != secondInfo.Size() {
		t.Fatalf("expected second size %d, got %d", secondInfo.Size(), got)
	}
}

func TestListAuthFiles_HidesDeletedNestedManagedFile(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	rootPath := filepath.Join(authDir, "foo.json")
	nestedPath := filepath.Join(authDir, "nested", "foo.json")
	if err := os.MkdirAll(filepath.Dir(nestedPath), 0o700); err != nil {
		t.Fatalf("failed to create nested dir: %v", err)
	}
	if err := os.WriteFile(rootPath, []byte(`{"type":"codex"}`), 0o600); err != nil {
		t.Fatalf("failed to write root auth file: %v", err)
	}
	if err := os.WriteFile(nestedPath, []byte(`{"type":"codex"}`), 0o600); err != nil {
		t.Fatalf("failed to write nested auth file: %v", err)
	}
	if err := os.Remove(nestedPath); err != nil {
		t.Fatalf("failed to remove nested auth file: %v", err)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	record := &coreauth.Auth{
		ID:       filepath.Join("nested", "foo.json"),
		FileName: filepath.Join("nested", "foo.json"),
		Provider: "codex",
		Status:   coreauth.StatusDisabled,
		Disabled: true,
		Attributes: map[string]string{
			"path": nestedPath,
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
	if len(payload.Files) != 0 {
		t.Fatalf("expected deleted nested auth to be hidden, got %d entries", len(payload.Files))
	}
}
