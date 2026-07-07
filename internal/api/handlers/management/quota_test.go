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
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestResetQuota_UsesAuthIndex(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	manager := coreauth.NewManager(nil, nil, nil)
	next := time.Now().Add(time.Hour)
	auth := &coreauth.Auth{
		ID:             "reset-auth-id",
		FileName:       "reset-auth-file.json",
		Provider:       "claude",
		Status:         coreauth.StatusError,
		StatusMessage:  "quota exhausted",
		Unavailable:    true,
		NextRetryAfter: next,
		Quota:          coreauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: next, BackoffLevel: 2},
		ModelStates: map[string]*coreauth.ModelState{
			"claude-reset-model": {
				Status:         coreauth.StatusError,
				StatusMessage:  "quota exhausted",
				Unavailable:    true,
				NextRetryAfter: next,
				Quota:          coreauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: next, BackoffLevel: 2},
			},
		},
	}
	authIndex := auth.EnsureIndex()
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/reset-quota", strings.NewReader(`{"auth_index":"`+authIndex+`"}`))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	h.ResetQuota(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &payload); errUnmarshal != nil {
		t.Fatalf("failed to decode response: %v", errUnmarshal)
	}
	if payload["auth_index"] != authIndex {
		t.Fatalf("auth_index = %#v, want %q", payload["auth_index"], authIndex)
	}

	updated, ok := manager.GetByID("reset-auth-id")
	if !ok || updated == nil {
		t.Fatalf("expected auth record to exist after reset")
	}
	if updated.Status != coreauth.StatusActive || updated.StatusMessage != "" || updated.Unavailable || !updated.NextRetryAfter.IsZero() {
		t.Fatalf("updated auth state = status %q message %q unavailable %v next %v", updated.Status, updated.StatusMessage, updated.Unavailable, updated.NextRetryAfter)
	}
	if updated.Quota.Exceeded || updated.Quota.Reason != "" || !updated.Quota.NextRecoverAt.IsZero() || updated.Quota.BackoffLevel != 0 {
		t.Fatalf("updated auth quota = %+v, want cleared", updated.Quota)
	}
	state := updated.ModelStates["claude-reset-model"]
	if state == nil {
		t.Fatalf("expected model state to remain")
	}
	if state.Status != coreauth.StatusActive || state.StatusMessage != "" || state.Unavailable || !state.NextRetryAfter.IsZero() {
		t.Fatalf("updated model state = status %q message %q unavailable %v next %v", state.Status, state.StatusMessage, state.Unavailable, state.NextRetryAfter)
	}
	if state.Quota.Exceeded || state.Quota.Reason != "" || !state.Quota.NextRecoverAt.IsZero() || state.Quota.BackoffLevel != 0 {
		t.Fatalf("updated model quota = %+v, want cleared", state.Quota)
	}
}

func TestResetQuota_DoesNotAcceptAuthIDOrFileName(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:       "reset-auth-id-only",
		FileName: "reset-auth-file-only.json",
		Provider: "claude",
		Status:   coreauth.StatusError,
	}
	authIndex := auth.EnsureIndex()
	if authIndex == auth.ID || authIndex == auth.FileName {
		t.Fatalf("test auth_index unexpectedly matches id or file name: %q", authIndex)
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	tests := []struct {
		name     string
		body     string
		wantCode int
	}{
		{name: "auth_id field ignored", body: `{"auth_id":"reset-auth-id-only"}`, wantCode: http.StatusBadRequest},
		{name: "id field ignored", body: `{"id":"reset-auth-id-only"}`, wantCode: http.StatusBadRequest},
		{name: "file name is not an index", body: `{"auth_index":"reset-auth-file-only.json"}`, wantCode: http.StatusNotFound},
		{name: "auth id is not an index", body: `{"auth_index":"reset-auth-id-only"}`, wantCode: http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(rec)
			req := httptest.NewRequest(http.MethodPost, "/v0/management/reset-quota", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			ctx.Request = req
			h.ResetQuota(ctx)

			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d with body %s", rec.Code, tt.wantCode, rec.Body.String())
			}
		})
	}
}

func TestPatchAuthFileStatus_EnableClearsQuotaCooldownAndPersists(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	authFile := filepath.Join(authDir, "quota-disabled.json")
	if errWrite := os.WriteFile(authFile, []byte(`{"type":"claude","email":"quota@example.com","disabled":true}`), 0o600); errWrite != nil {
		t.Fatalf("write auth file: %v", errWrite)
	}

	store := sdkAuth.NewFileTokenStore()
	store.SetBaseDir(authDir)
	manager := coreauth.NewManager(store, nil, nil)
	next := time.Now().Add(time.Hour)
	auth := &coreauth.Auth{
		ID:             "quota-disabled.json",
		FileName:       "quota-disabled.json",
		Provider:       "claude",
		Disabled:       true,
		Status:         coreauth.StatusDisabled,
		StatusMessage:  "disabled via management API",
		Unavailable:    true,
		NextRetryAfter: next,
		Quota:          coreauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: next, BackoffLevel: 2},
		Attributes: map[string]string{
			"path": authFile,
		},
		Metadata: map[string]any{
			"type":     "claude",
			"email":    "quota@example.com",
			"disabled": true,
		},
		ModelStates: map[string]*coreauth.ModelState{
			"claude-quota-model": {
				Status:         coreauth.StatusError,
				StatusMessage:  "quota exhausted",
				Unavailable:    true,
				NextRetryAfter: next,
				Quota:          coreauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: next, BackoffLevel: 2},
			},
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/status", strings.NewReader(`{"name":"quota-disabled.json","disabled":false}`))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	h.PatchAuthFileStatus(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	updated, ok := manager.GetByID("quota-disabled.json")
	if !ok || updated == nil {
		t.Fatalf("updated auth missing")
	}
	if updated.Disabled || updated.Status != coreauth.StatusActive || updated.StatusMessage != "" || updated.Unavailable || !updated.NextRetryAfter.IsZero() {
		t.Fatalf("updated auth state = disabled %v status %q message %q unavailable %v next %v", updated.Disabled, updated.Status, updated.StatusMessage, updated.Unavailable, updated.NextRetryAfter)
	}
	if updated.Quota.Exceeded || updated.Quota.Reason != "" || !updated.Quota.NextRecoverAt.IsZero() || updated.Quota.BackoffLevel != 0 {
		t.Fatalf("updated quota = %+v, want cleared", updated.Quota)
	}
	state := updated.ModelStates["claude-quota-model"]
	if state == nil {
		t.Fatalf("updated model state missing")
	}
	if state.Status != coreauth.StatusActive || state.StatusMessage != "" || state.Unavailable || !state.NextRetryAfter.IsZero() {
		t.Fatalf("updated model state = status %q message %q unavailable %v next %v", state.Status, state.StatusMessage, state.Unavailable, state.NextRetryAfter)
	}
	if state.Quota.Exceeded || state.Quota.Reason != "" || !state.Quota.NextRecoverAt.IsZero() || state.Quota.BackoffLevel != 0 {
		t.Fatalf("updated model quota = %+v, want cleared", state.Quota)
	}

	raw, errRead := os.ReadFile(authFile)
	if errRead != nil {
		t.Fatalf("read auth file: %v", errRead)
	}
	var persisted map[string]any
	if errUnmarshal := json.Unmarshal(raw, &persisted); errUnmarshal != nil {
		t.Fatalf("unmarshal persisted auth: %v", errUnmarshal)
	}
	if disabled, _ := persisted["disabled"].(bool); disabled {
		t.Fatalf("persisted disabled = true, want false; raw=%s", string(raw))
	}
}
