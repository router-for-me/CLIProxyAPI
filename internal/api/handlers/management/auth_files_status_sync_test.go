package management

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestPatchAuthFileStatusInvokesPostAuthPersistHook(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	fileName := "codex-test.json"
	filePath := filepath.Join(authDir, fileName)
	if errWrite := os.WriteFile(filePath, []byte(`{"type":"codex","disabled":false}`), 0o600); errWrite != nil {
		t.Fatalf("write auth file: %v", errWrite)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:       fileName,
		FileName: fileName,
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"path": filePath,
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)

	var hookCalls []*coreauth.Auth
	h.SetPostAuthPersistHook(func(_ context.Context, updated *coreauth.Auth) error {
		if updated != nil {
			hookCalls = append(hookCalls, updated.Clone())
		}
		return nil
	})

	// 1. Disable the credential
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/status", strings.NewReader(`{"name":"codex-test.json","disabled":true}`))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	h.PatchAuthFileStatus(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	if len(hookCalls) != 1 {
		t.Fatalf("expected 1 hook call after disable, got %d", len(hookCalls))
	}
	if !hookCalls[0].Disabled {
		t.Fatalf("expected hook call auth to be disabled, got %+v", hookCalls[0])
	}

	// 2. Re-enable the credential
	rec = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(rec)
	req = httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/status", strings.NewReader(`{"name":"codex-test.json","disabled":false}`))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	h.PatchAuthFileStatus(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	if len(hookCalls) != 2 {
		t.Fatalf("expected 2 hook calls after re-enable, got %d", len(hookCalls))
	}
	if hookCalls[1].Disabled {
		t.Fatalf("expected second hook call auth to be enabled, got %+v", hookCalls[1])
	}
}

func TestPatchAuthFileStatusRestoresModelsViaSyncHook(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	fileName := "codex-models.json"
	filePath := filepath.Join(authDir, fileName)
	if errWrite := os.WriteFile(filePath, []byte(`{"type":"codex","disabled":false}`), 0o600); errWrite != nil {
		t.Fatalf("write auth file: %v", errWrite)
	}

	reg := registry.GetGlobalRegistry()
	authID := fileName
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:       authID,
		FileName: fileName,
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"path": filePath,
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	// Initial model registration
	reg.RegisterClient(authID, "codex", []*registry.ModelInfo{
		{ID: "gpt-6-astra", DisplayName: "GPT-6 Astra"},
	})

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	// Wire postAuthPersistHook to synchronize registry models as runtimeAuthSyncHook does
	h.SetPostAuthPersistHook(func(_ context.Context, a *coreauth.Auth) error {
		if a == nil || a.ID == "" {
			return nil
		}
		if a.Disabled {
			reg.UnregisterClient(a.ID)
		} else {
			reg.RegisterClient(a.ID, a.Provider, []*registry.ModelInfo{
				{ID: "gpt-6-astra", DisplayName: "GPT-6 Astra"},
			})
		}
		return nil
	})

	// Step 1: Confirm models exist before disabling
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files/models?name="+fileName, nil)
	h.GetAuthFileModels(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("GetAuthFileModels initial status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || len(resp.Models) != 1 {
		t.Fatalf("expected 1 initial model, got: %s", rec.Body.String())
	}

	// Step 2: Disable the credential
	rec = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/status", strings.NewReader(`{"name":"codex-models.json","disabled":true}`))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	h.PatchAuthFileStatus(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("PatchAuthFileStatus disable failed: %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files/models?name="+fileName, nil)
	h.GetAuthFileModels(ctx)
	resp.Models = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || len(resp.Models) != 0 {
		t.Fatalf("expected 0 models after disable, got: %s", rec.Body.String())
	}

	// Step 3: Re-enable the credential
	rec = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(rec)
	req = httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/status", strings.NewReader(`{"name":"codex-models.json","disabled":false}`))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	h.PatchAuthFileStatus(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("PatchAuthFileStatus re-enable failed: %s", rec.Body.String())
	}

	// Step 4: Confirm models are restored without restart
	rec = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files/models?name="+fileName, nil)
	h.GetAuthFileModels(ctx)
	resp.Models = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || len(resp.Models) != 1 {
		t.Fatalf("expected 1 model after re-enable, got: %s", rec.Body.String())
	}
}

func TestPatchPluginVirtualSourceStatusInvokesPostAuthPersistHook(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	fileName := "source-sync.json"
	filePath := filepath.Join(authDir, fileName)
	if errWrite := os.WriteFile(filePath, []byte(`{"type":"gemini-cli","disabled":false}`), 0o600); errWrite != nil {
		t.Fatalf("write source auth file: %v", errWrite)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	for _, id := range []string{"source-sync.json", "virtual-project-a", "virtual-project-b"} {
		auth := pluginVirtualAuthForTest(authDir, fileName, id)
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register virtual auth %s: %v", id, errRegister)
		}
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)

	var hookCalls []*coreauth.Auth
	h.SetPostAuthPersistHook(func(_ context.Context, updated *coreauth.Auth) error {
		if updated != nil {
			hookCalls = append(hookCalls, updated.Clone())
		}
		return nil
	})

	// Disable the source file
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/status", strings.NewReader(`{"name":"source-sync.json","disabled":true}`))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	h.PatchAuthFileStatus(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	if len(hookCalls) != 3 {
		t.Fatalf("expected 3 hook calls for 3 virtual auths on disable, got %d", len(hookCalls))
	}
	for _, call := range hookCalls {
		if !call.Disabled {
			t.Fatalf("expected virtual auth %s to be disabled in hook call", call.ID)
		}
	}

	// Re-enable the source file
	hookCalls = nil
	rec = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(rec)
	req = httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/status", strings.NewReader(`{"name":"source-sync.json","disabled":false}`))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	h.PatchAuthFileStatus(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	if len(hookCalls) != 3 {
		t.Fatalf("expected 3 hook calls for 3 virtual auths on re-enable, got %d", len(hookCalls))
	}
	for _, call := range hookCalls {
		if call.Disabled {
			t.Fatalf("expected virtual auth %s to be enabled in hook call", call.ID)
		}
	}
}

func TestPatchAuthFileStatusHookErrorReturns500(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	fileName := "codex-hook-err.json"
	filePath := filepath.Join(authDir, fileName)
	if errWrite := os.WriteFile(filePath, []byte(`{"type":"codex","disabled":false}`), 0o600); errWrite != nil {
		t.Fatalf("write auth file: %v", errWrite)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:       fileName,
		FileName: fileName,
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"path": filePath,
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	h.SetPostAuthPersistHook(func(_ context.Context, _ *coreauth.Auth) error {
		return errors.New("simulated sync hook failure")
	})

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/status", strings.NewReader(`{"name":"codex-hook-err.json","disabled":true}`))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	h.PatchAuthFileStatus(ctx)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "simulated sync hook failure") {
		t.Fatalf("body = %s, want simulated sync hook failure error message", rec.Body.String())
	}
}

func TestPatchPluginVirtualSourceStatusHookErrorReturnsError(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	fileName := "source-hook-err.json"
	filePath := filepath.Join(authDir, fileName)
	if errWrite := os.WriteFile(filePath, []byte(`{"type":"gemini-cli","disabled":false}`), 0o600); errWrite != nil {
		t.Fatalf("write source auth file: %v", errWrite)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	auth := pluginVirtualAuthForTest(authDir, fileName, "source-hook-err.json")
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register virtual auth: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	h.SetPostAuthPersistHook(func(_ context.Context, _ *coreauth.Auth) error {
		return errors.New("plugin sync failed")
	})

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/status", strings.NewReader(`{"name":"source-hook-err.json","disabled":true}`))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	h.PatchAuthFileStatus(ctx)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "plugin sync failed") {
		t.Fatalf("body = %s, want plugin sync failed error message", rec.Body.String())
	}
}
