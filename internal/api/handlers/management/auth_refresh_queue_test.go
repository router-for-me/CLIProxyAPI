package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestBuildAuthRefreshQueueEntrySkipsZeroNextRefreshAt(t *testing.T) {
	h := &Handler{}

	entry := h.buildAuthRefreshQueueEntry(coreauth.RefreshQueueEntry{
		Auth: &coreauth.Auth{
			ID:       "auth-zero",
			Provider: "gemini",
			Status:   coreauth.StatusActive,
		},
		NextRefreshAt: time.Time{},
	})
	if entry != nil {
		t.Fatalf("buildAuthRefreshQueueEntry() = %#v, want nil", entry)
	}
}

func TestGetAuthRefreshQueueReturnsQueuedAuths(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	ctx := context.Background()
	manager := coreauth.NewManager(nil, nil, nil)
	next := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	_, err := manager.Register(ctx, &coreauth.Auth{
		ID:               "auth-queue-1",
		Provider:         "gemini",
		FileName:         "gemini-auth.json",
		Status:           coreauth.StatusActive,
		NextRefreshAfter: next,
		Metadata: map[string]any{
			"email": "user@example.com",
		},
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	manager.StartAutoRefresh(ctx, time.Hour)
	defer manager.StopAutoRefresh()

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-refresh-queue", nil)

	h.GetAuthRefreshQueue(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["count"].(float64) != 1 {
		t.Fatalf("count = %#v, want 1", payload["count"])
	}
	if _, ok := payload["generated_at"].(string); !ok {
		t.Fatalf("generated_at = %#v, want string", payload["generated_at"])
	}
	queue, ok := payload["queue"].([]any)
	if !ok || len(queue) != 1 {
		t.Fatalf("queue = %#v, want one entry", payload["queue"])
	}
	entry := queue[0].(map[string]any)

	wantFields := []string{"id", "auth_index", "name", "provider", "status", "unavailable", "disabled", "next_refresh_at", "account_type", "account", "email"}
	for _, field := range wantFields {
		if _, ok := entry[field]; !ok {
			t.Fatalf("missing field %q in entry %#v", field, entry)
		}
	}
	for _, field := range []string{"label", "status_message", "last_refreshed_at", "last_refresh", "seconds_until_refresh", "refresh_due", "next_retry_after", "last_error"} {
		if _, ok := entry[field]; ok {
			t.Fatalf("field %q should be omitted, entry %#v", field, entry)
		}
	}
	if entry["id"] != "auth-queue-1" {
		t.Fatalf("id = %#v, want auth-queue-1", entry["id"])
	}
	if entry["name"] != "gemini-auth.json" {
		t.Fatalf("name = %#v, want gemini-auth.json", entry["name"])
	}
	if entry["provider"] != "gemini" {
		t.Fatalf("provider = %#v, want gemini", entry["provider"])
	}
	if entry["status"] != string(coreauth.StatusActive) {
		t.Fatalf("status = %#v, want active", entry["status"])
	}
	if entry["account_type"] != "oauth" || entry["account"] != "user@example.com" || entry["email"] != "user@example.com" {
		t.Fatalf("account fields = %#v", entry)
	}
	parsedNext, err := time.Parse(time.RFC3339, entry["next_refresh_at"].(string))
	if err != nil {
		t.Fatalf("next_refresh_at parse error: %v", err)
	}
	if !parsedNext.Equal(next) {
		t.Fatalf("next_refresh_at = %s, want %s", parsedNext, next)
	}
}

func TestGetAuthRefreshQueueWithoutManagerReturnsEmptyQueue(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-refresh-queue", nil)

	h.GetAuthRefreshQueue(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["count"].(float64) != 0 {
		t.Fatalf("count = %#v, want 0", payload["count"])
	}
	queue, ok := payload["queue"].([]any)
	if !ok || len(queue) != 0 {
		t.Fatalf("queue = %#v, want empty array", payload["queue"])
	}
}

func TestGetAuthRefreshQueueWithNilHandlerReturnsError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-refresh-queue", nil)

	var h *Handler
	h.GetAuthRefreshQueue(ginCtx)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["error"] != "handler not initialized" {
		t.Fatalf("error = %#v, want handler not initialized", payload["error"])
	}
}
