package management

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func registerBatchTestAuth(t *testing.T, m *coreauth.Manager, id, provider, fileName string) {
	t.Helper()
	_, err := m.Register(context.Background(), &coreauth.Auth{
		ID:       id,
		Provider: provider,
		FileName: fileName,
		Metadata: map[string]any{"email": id + "@example.com"},
	})
	if err != nil {
		t.Fatalf("register auth %s: %v", id, err)
	}
}

// ---- BatchPatchAuthFileStatus ----

func TestBatchPatchAuthFileStatus_UniformBody_DisablesAll(t *testing.T) {
	gin.SetMode(gin.TestMode)
	m := coreauth.NewManager(nil, nil, nil)
	registerBatchTestAuth(t, m, "auth-1", "claude", "auth1.json")
	registerBatchTestAuth(t, m, "auth-2", "claude", "auth2.json")

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, m)

	disabled := true
	reqBody, _ := json.Marshal(map[string]any{"names": []string{"auth-1", "auth-2"}, "disabled": disabled})
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/auth-files/batch-status", bytes.NewReader(reqBody))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.BatchPatchAuthFileStatus(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := int(resp["updated"].(float64)); got != 2 {
		t.Fatalf("expected updated=2, got %d", got)
	}

	for _, id := range []string{"auth-1", "auth-2"} {
		auth, ok := m.GetByID(id)
		if !ok {
			t.Fatalf("auth %s not found after batch disable", id)
		}
		if !auth.Disabled {
			t.Fatalf("auth %s: expected Disabled=true", id)
		}
		if auth.Status != coreauth.StatusDisabled {
			t.Fatalf("auth %s: expected Status=disabled, got %v", id, auth.Status)
		}
	}
}

func TestBatchPatchAuthFileStatus_PerItemBody_MixedEnable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	m := coreauth.NewManager(nil, nil, nil)
	registerBatchTestAuth(t, m, "auth-a", "claude", "a.json")
	registerBatchTestAuth(t, m, "auth-b", "claude", "b.json")

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, m)

	d1 := false
	d2 := true
	items := []map[string]any{
		{"name": "auth-a", "disabled": d1},
		{"name": "auth-b", "disabled": d2},
	}
	reqBody, _ := json.Marshal(items)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/auth-files/batch-status", bytes.NewReader(reqBody))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.BatchPatchAuthFileStatus(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	authA, _ := m.GetByID("auth-a")
	if authA.Disabled {
		t.Fatalf("auth-a should be enabled")
	}
	authB, _ := m.GetByID("auth-b")
	if !authB.Disabled {
		t.Fatalf("auth-b should be disabled")
	}
}

func TestBatchPatchAuthFileStatus_LookupByFileName(t *testing.T) {
	gin.SetMode(gin.TestMode)
	m := coreauth.NewManager(nil, nil, nil)
	registerBatchTestAuth(t, m, "auth-x", "claude", "myfile.json")

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, m)

	disabled := true
	reqBody, _ := json.Marshal(map[string]any{"names": []string{"myfile.json"}, "disabled": disabled})
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/auth-files/batch-status", bytes.NewReader(reqBody))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.BatchPatchAuthFileStatus(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	auth, _ := m.GetByID("auth-x")
	if !auth.Disabled {
		t.Fatal("expected auth to be disabled via fileName lookup")
	}
}

func TestBatchPatchAuthFileStatus_UnknownNameGoesToFailed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	m := coreauth.NewManager(nil, nil, nil)
	registerBatchTestAuth(t, m, "real-auth", "claude", "real.json")

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, m)

	disabled := true
	reqBody, _ := json.Marshal(map[string]any{"names": []string{"real-auth", "ghost-auth"}, "disabled": disabled})
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/auth-files/batch-status", bytes.NewReader(reqBody))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.BatchPatchAuthFileStatus(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp map[string]any
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if int(resp["updated"].(float64)) != 1 {
		t.Fatalf("expected updated=1, got %v", resp["updated"])
	}
	failed, _ := resp["failed"].([]any)
	if len(failed) != 1 || failed[0].(string) != "ghost-auth" {
		t.Fatalf("expected failed=[ghost-auth], got %v", failed)
	}
}

func TestBatchPatchAuthFileStatus_EmptyBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	m := coreauth.NewManager(nil, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, m)

	reqBody, _ := json.Marshal(map[string]any{"names": []string{}, "disabled": true})
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/auth-files/batch-status", bytes.NewReader(reqBody))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.BatchPatchAuthFileStatus(ctx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty names, got %d", rec.Code)
	}
}

// ---- BatchClearAuthErrors ----

func TestBatchClearAuthErrors_ByProvider(t *testing.T) {
	gin.SetMode(gin.TestMode)
	m := coreauth.NewManager(nil, nil, nil)

	registerBatchTestAuth(t, m, "err-1", "claude", "err1.json")
	registerBatchTestAuth(t, m, "err-2", "claude", "err2.json")
	registerBatchTestAuth(t, m, "err-3", "codex", "err3.json")

	// Mark err-1 and err-2 as having errors.
	for _, id := range []string{"err-1", "err-2"} {
		auth, _ := m.GetByID(id)
		auth.Unavailable = true
		auth.Status = coreauth.StatusError
		auth.StatusMessage = "something went wrong"
		auth.LastError = &coreauth.Error{Message: "upstream error", HTTPStatus: 503}
		m.Update(context.Background(), auth) //nolint
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, m)

	reqBody, _ := json.Marshal(map[string]any{"provider": "claude"})
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/auth-files/batch-clear-errors", bytes.NewReader(reqBody))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.BatchClearAuthErrors(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if int(resp["cleared"].(float64)) != 2 {
		t.Fatalf("expected cleared=2, got %v", resp["cleared"])
	}

	for _, id := range []string{"err-1", "err-2"} {
		auth, _ := m.GetByID(id)
		if auth.Unavailable {
			t.Fatalf("auth %s: Unavailable should be cleared", id)
		}
		if auth.LastError != nil {
			t.Fatalf("auth %s: LastError should be nil after clear", id)
		}
		if auth.Status == coreauth.StatusError {
			t.Fatalf("auth %s: Status should not be error after clear", id)
		}
	}

	// codex auth should be untouched.
	errAuth3, _ := m.GetByID("err-3")
	if errAuth3.Unavailable {
		t.Fatal("err-3 (codex) should not have been cleared by claude provider filter")
	}
}

func TestBatchClearAuthErrors_ByName(t *testing.T) {
	gin.SetMode(gin.TestMode)
	m := coreauth.NewManager(nil, nil, nil)

	registerBatchTestAuth(t, m, "clr-a", "claude", "clr-a.json")
	registerBatchTestAuth(t, m, "clr-b", "claude", "clr-b.json")

	for _, id := range []string{"clr-a", "clr-b"} {
		auth, _ := m.GetByID(id)
		auth.Unavailable = true
		auth.Status = coreauth.StatusError
		m.Update(context.Background(), auth) //nolint
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, m)

	// Only clear clr-a by ID.
	reqBody, _ := json.Marshal(map[string]any{"names": []string{"clr-a"}})
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/auth-files/batch-clear-errors", bytes.NewReader(reqBody))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.BatchClearAuthErrors(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	authA, _ := m.GetByID("clr-a")
	if authA.Unavailable {
		t.Fatal("clr-a should have been cleared")
	}
	authB, _ := m.GetByID("clr-b")
	if !authB.Unavailable {
		t.Fatal("clr-b should not have been cleared")
	}
}

func TestBatchClearAuthErrors_MissingBothNamesAndProvider(t *testing.T) {
	gin.SetMode(gin.TestMode)
	m := coreauth.NewManager(nil, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, m)

	reqBody, _ := json.Marshal(map[string]any{})
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/auth-files/batch-clear-errors", bytes.NewReader(reqBody))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.BatchClearAuthErrors(ctx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestBatchClearAuthErrors_QuotaStateCleared(t *testing.T) {
	gin.SetMode(gin.TestMode)
	m := coreauth.NewManager(nil, nil, nil)

	registerBatchTestAuth(t, m, "quota-auth", "claude", "quota.json")

	auth, _ := m.GetByID("quota-auth")
	auth.Quota = coreauth.QuotaState{Exceeded: true, Reason: "quota", BackoffLevel: 3}
	auth.Status = coreauth.StatusError
	m.Update(context.Background(), auth) //nolint

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, m)

	reqBody, _ := json.Marshal(map[string]any{"names": []string{"quota-auth"}})
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/auth-files/batch-clear-errors", bytes.NewReader(reqBody))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.BatchClearAuthErrors(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	updated, _ := m.GetByID("quota-auth")
	if updated.Quota.Exceeded {
		t.Fatal("QuotaState.Exceeded should be cleared")
	}
	if updated.Quota.BackoffLevel != 0 {
		t.Fatalf("QuotaState.BackoffLevel should be 0, got %d", updated.Quota.BackoffLevel)
	}
}
