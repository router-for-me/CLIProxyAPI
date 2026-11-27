package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestPatchAuthFileStatusReenableClearsUnavailableAndRestoresSelectability_Issue8523(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	fileName := "codex-issue8523-status.json"
	filePath := filepath.Join(authDir, fileName)
	validExp := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	if errWrite := os.WriteFile(filePath, []byte("{\"type\":\"codex\",\"access_token\":\"tok\",\"expired\":\""+validExp+"\",\"disabled\":false}"), 0o600); errWrite != nil {
		t.Fatalf("write auth file: %v", errWrite)
	}

	reg := registry.GetGlobalRegistry()
	model := "issue8523-mgmt-gpt-6-luna"
	t.Cleanup(func() { reg.UnregisterClient(fileName) })

	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	auth := &coreauth.Auth{
		ID:       fileName,
		FileName: fileName,
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"path": filePath,
		},
		Metadata: map[string]any{
			"type":         "codex",
			"access_token": "tok",
			"expired":      validExp,
			"disabled":     false,
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	reg.RegisterClient(fileName, "codex", []*registry.ModelInfo{{ID: model}})

	// Serve a successful request so ModelStates is non-empty before disable.
	manager.MarkResult(context.Background(), coreauth.Result{
		AuthID:   fileName,
		Provider: "codex",
		Model:    model,
		Success:  true,
	})

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	h.SetPostAuthPersistHook(func(ctx context.Context, a *coreauth.Auth) error {
		if a == nil || a.ID == "" {
			return nil
		}
		if a.Disabled {
			reg.UnregisterClient(a.ID)
		} else {
			reg.RegisterClient(a.ID, a.Provider, []*registry.ModelInfo{{ID: model}})
		}
		manager.ReconcileRegistryModelStates(ctx, a.ID)
		return nil
	})

	// Disable via PatchAuthFileStatus.
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v8/management/credentials/status", strings.NewReader("{\"name\":\""+fileName+"\",\"disabled\":true}"))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req
	h.PatchAuthFileStatus(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("disable status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// Re-enable via PatchAuthFileStatus.
	rec = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(rec)
	req = httptest.NewRequest(http.MethodPatch, "/v8/management/credentials/status", strings.NewReader("{\"name\":\""+fileName+"\",\"disabled\":false}"))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req
	h.PatchAuthFileStatus(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("re-enable status = %d, body = %s", rec.Code, rec.Body.String())
	}

	got, ok := manager.GetByID(fileName)
	if !ok || got == nil {
		t.Fatal("auth missing after re-enable")
	}
	if got.Unavailable {
		t.Fatalf("re-enabled auth stuck with Unavailable=true (status=%q)", got.Status)
	}
	if !coreauth.IsAuthAvailableForModel(got, model, time.Now()) {
		t.Fatal("IsAuthAvailableForModel = false after re-enable, want true")
	}

	unavailable, status, _, _ := reconcileAuthFileCooldownState(got, time.Now().UTC())
	if unavailable || status != coreauth.StatusActive {
		t.Fatalf("reconcileAuthFileCooldownState = (unavailable=%v, status=%q), want (false, active)", unavailable, status)
	}
}

func TestPatchAuthFileFieldsReenableClearsUnavailable_Issue8523(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	fileName := "codex-issue8523-fields.json"
	filePath := filepath.Join(authDir, fileName)
	validExp := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	if errWrite := os.WriteFile(filePath, []byte("{\"type\":\"codex\",\"access_token\":\"tok\",\"expired\":\""+validExp+"\",\"disabled\":false}"), 0o600); errWrite != nil {
		t.Fatalf("write auth file: %v", errWrite)
	}

	reg := registry.GetGlobalRegistry()
	model := "issue8523-fields-gpt-6-luna"
	t.Cleanup(func() { reg.UnregisterClient(fileName) })

	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID:       fileName,
		FileName: fileName,
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"path": filePath,
		},
		Metadata: map[string]any{
			"type":         "codex",
			"access_token": "tok",
			"expired":      validExp,
			"disabled":     false,
		},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	reg.RegisterClient(fileName, "codex", []*registry.ModelInfo{{ID: model}})
	manager.MarkResult(context.Background(), coreauth.Result{
		AuthID:   fileName,
		Provider: "codex",
		Model:    model,
		Success:  true,
	})

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	h.SetPostAuthPersistHook(func(ctx context.Context, a *coreauth.Auth) error {
		if a == nil || a.ID == "" {
			return nil
		}
		if a.Disabled {
			reg.UnregisterClient(a.ID)
		} else {
			reg.RegisterClient(a.ID, a.Provider, []*registry.ModelInfo{{ID: model}})
		}
		manager.ReconcileRegistryModelStates(ctx, a.ID)
		return nil
	})

	// Disable via PatchAuthFileFields.
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v8/management/credentials/fields", strings.NewReader("{\"name\":\""+fileName+"\",\"disabled\":true}"))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req
	h.PatchAuthFileFields(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("fields disable status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// Re-enable via PatchAuthFileFields.
	rec = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(rec)
	req = httptest.NewRequest(http.MethodPatch, "/v8/management/credentials/fields", strings.NewReader("{\"name\":\""+fileName+"\",\"disabled\":false}"))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req
	h.PatchAuthFileFields(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("fields re-enable status = %d, body = %s", rec.Code, rec.Body.String())
	}

	got, _ := manager.GetByID(fileName)
	if got.Unavailable {
		t.Fatalf("re-enabled auth via PatchAuthFileFields stuck with Unavailable=true")
	}
	if !coreauth.IsAuthAvailableForModel(got, model, time.Now()) {
		t.Fatal("IsAuthAvailableForModel = false after PatchAuthFileFields re-enable, want true")
	}
}

func TestPatchAuthFileStatusEnableNoOpDoesNotClearActiveCooldown_Issue8523(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	fileName := "codex-issue8523-cooling.json"
	filePath := filepath.Join(authDir, fileName)
	validExp := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	if errWrite := os.WriteFile(filePath, []byte("{\"type\":\"codex\",\"access_token\":\"tok\",\"expired\":\""+validExp+"\",\"disabled\":false}"), 0o600); errWrite != nil {
		t.Fatalf("write auth file: %v", errWrite)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	retryAt := time.Now().Add(15 * time.Minute)
	if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID:             fileName,
		FileName:       fileName,
		Provider:       "codex",
		Status:         coreauth.StatusError,
		StatusMessage:  "quota exhausted",
		Unavailable:    true,
		NextRetryAfter: retryAt,
		Quota: coreauth.QuotaState{
			Exceeded:      true,
			Reason:        "credential_quota",
			NextRecoverAt: retryAt,
		},
		Attributes: map[string]string{
			"path": filePath,
		},
		Metadata: map[string]any{
			"type":         "codex",
			"access_token": "tok",
			"expired":      validExp,
			"disabled":     false,
		},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v8/management/credentials/status", strings.NewReader("{\"name\":\""+fileName+"\",\"disabled\":false}"))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req
	h.PatchAuthFileStatus(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	got, _ := manager.GetByID(fileName)
	if !got.Unavailable || !got.NextRetryAfter.After(time.Now()) || got.Status != coreauth.StatusError {
		t.Fatalf("no-op disabled=false cleared active cooldown: unavailable=%v status=%q nextRetry=%v",
			got.Unavailable, got.Status, got.NextRetryAfter)
	}
}

func TestOrphanedUnavailableManagementViewConsistentWithoutResetOrEdit_Issue8523(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	fileName := "codex-issue8523-orphaned-view.json"
	filePath := filepath.Join(authDir, fileName)
	validExp := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	if errWrite := os.WriteFile(filePath, []byte("{\"type\":\"codex\",\"access_token\":\"tok\",\"expired\":\""+validExp+"\",\"disabled\":false}"), 0o600); errWrite != nil {
		t.Fatalf("write auth file: %v", errWrite)
	}

	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	cfg := &config.Config{AuthDir: authDir}
	manager.SetConfig(cfg)

	// Register directly in the contradictory stuck state without any edit, toggle, or reset.
	if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID:          fileName,
		FileName:    fileName,
		Provider:    "codex",
		Status:      coreauth.StatusActive,
		Unavailable: true,
		Attributes: map[string]string{
			"path": filePath,
		},
		Metadata: map[string]any{
			"type":         "codex",
			"access_token": "tok",
			"expired":      validExp,
			"disabled":     false,
		},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	got, _ := manager.GetByID(fileName)
	if !coreauth.IsAuthAvailableForModel(got, "gpt-6-luna", time.Now()) {
		t.Fatal("IsAuthAvailableForModel = false for orphaned active auth, want true")
	}

	unavailable, status, statusMsg, nextRetry := reconcileAuthFileCooldownState(got, time.Now().UTC())
	if unavailable || status != coreauth.StatusActive || statusMsg != "" || !nextRetry.IsZero() {
		t.Fatalf("reconcileAuthFileCooldownState = (unavailable=%v, status=%q, msg=%q, next=%v), want (false, active, empty, zero)",
			unavailable, status, statusMsg, nextRetry)
	}

	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	payload := requestAuthFilesCooldowns(t, h, "?name="+fileName)
	if len(payload.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(payload.Files))
	}
	if payload.Files[0].Unavailable || payload.Files[0].Status != string(coreauth.StatusActive) {
		t.Fatalf("management list reported contradictory state: unavailable=%v status=%q",
			payload.Files[0].Unavailable, payload.Files[0].Status)
	}
}
