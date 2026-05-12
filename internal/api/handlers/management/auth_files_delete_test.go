package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestDeleteAuthFile_UsesAuthPathFromManager(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	tempDir := t.TempDir()
	authDir := filepath.Join(tempDir, "auth")
	externalDir := filepath.Join(tempDir, "external")
	if errMkdirAuth := os.MkdirAll(authDir, 0o700); errMkdirAuth != nil {
		t.Fatalf("failed to create auth dir: %v", errMkdirAuth)
	}
	if errMkdirExternal := os.MkdirAll(externalDir, 0o700); errMkdirExternal != nil {
		t.Fatalf("failed to create external dir: %v", errMkdirExternal)
	}

	fileName := "codex-user@example.com-plus.json"
	shadowPath := filepath.Join(authDir, fileName)
	realPath := filepath.Join(externalDir, fileName)
	if errWriteShadow := os.WriteFile(shadowPath, []byte(`{"type":"codex","email":"shadow@example.com"}`), 0o600); errWriteShadow != nil {
		t.Fatalf("failed to write shadow file: %v", errWriteShadow)
	}
	if errWriteReal := os.WriteFile(realPath, []byte(`{"type":"codex","email":"real@example.com"}`), 0o600); errWriteReal != nil {
		t.Fatalf("failed to write real file: %v", errWriteReal)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	record := &coreauth.Auth{
		ID:          "legacy/" + fileName,
		FileName:    fileName,
		Provider:    "codex",
		Status:      coreauth.StatusError,
		Unavailable: true,
		Attributes: map[string]string{
			"path": realPath,
		},
		Metadata: map[string]any{
			"type":  "codex",
			"email": "real@example.com",
		},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	h.tokenStore = &memoryAuthStore{}

	deleteRec := httptest.NewRecorder()
	deleteCtx, _ := gin.CreateTestContext(deleteRec)
	deleteReq := httptest.NewRequest(http.MethodDelete, "/v0/management/auth-files?name="+url.QueryEscape(fileName), nil)
	deleteCtx.Request = deleteReq
	h.DeleteAuthFile(deleteCtx)

	if deleteRec.Code != http.StatusOK {
		t.Fatalf("expected delete status %d, got %d with body %s", http.StatusOK, deleteRec.Code, deleteRec.Body.String())
	}
	if _, errStatReal := os.Stat(realPath); !os.IsNotExist(errStatReal) {
		t.Fatalf("expected managed auth file to be removed, stat err: %v", errStatReal)
	}
	if _, errStatShadow := os.Stat(shadowPath); errStatShadow != nil {
		t.Fatalf("expected shadow auth file to remain, stat err: %v", errStatShadow)
	}

	listRec := httptest.NewRecorder()
	listCtx, _ := gin.CreateTestContext(listRec)
	listReq := httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)
	listCtx.Request = listReq
	h.ListAuthFiles(listCtx)

	if listRec.Code != http.StatusOK {
		t.Fatalf("expected list status %d, got %d with body %s", http.StatusOK, listRec.Code, listRec.Body.String())
	}
	var listPayload map[string]any
	if errUnmarshal := json.Unmarshal(listRec.Body.Bytes(), &listPayload); errUnmarshal != nil {
		t.Fatalf("failed to decode list payload: %v", errUnmarshal)
	}
	filesRaw, ok := listPayload["files"].([]any)
	if !ok {
		t.Fatalf("expected files array, payload: %#v", listPayload)
	}
	if len(filesRaw) != 0 {
		t.Fatalf("expected removed auth to be hidden from list, got %d entries", len(filesRaw))
	}
}

func TestDeleteAuthFile_FallbackToAuthDirPath(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	fileName := "fallback-user.json"
	filePath := filepath.Join(authDir, fileName)
	if errWrite := os.WriteFile(filePath, []byte(`{"type":"codex"}`), 0o600); errWrite != nil {
		t.Fatalf("failed to write auth file: %v", errWrite)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	h.tokenStore = &memoryAuthStore{}

	deleteRec := httptest.NewRecorder()
	deleteCtx, _ := gin.CreateTestContext(deleteRec)
	deleteReq := httptest.NewRequest(http.MethodDelete, "/v0/management/auth-files?name="+url.QueryEscape(fileName), nil)
	deleteCtx.Request = deleteReq
	h.DeleteAuthFile(deleteCtx)

	if deleteRec.Code != http.StatusOK {
		t.Fatalf("expected delete status %d, got %d with body %s", http.StatusOK, deleteRec.Code, deleteRec.Body.String())
	}
	if _, errStat := os.Stat(filePath); !os.IsNotExist(errStat) {
		t.Fatalf("expected auth file to be removed from auth dir, stat err: %v", errStat)
	}
}
