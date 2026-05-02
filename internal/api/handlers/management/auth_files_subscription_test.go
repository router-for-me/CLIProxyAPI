package management

import (
	"context"
	"encoding/base64"
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

func TestBuildAuthFileEntry_ExposesCodexSubscriptionFields(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	idToken := testCodexIDToken(t)
	manager := coreauth.NewManager(nil, nil, nil)
	record := &coreauth.Auth{
		ID:       "codex-user@example.com-plus.json",
		FileName: "codex-user@example.com-plus.json",
		Provider: "codex",
		Attributes: map[string]string{
			"runtime_only": "true",
		},
		Metadata: map[string]any{
			"type":     "codex",
			"email":    "user@example.com",
			"id_token": idToken,
		},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	entry := h.buildAuthFileEntry(record)
	if entry == nil {
		t.Fatalf("expected auth entry")
	}

	assertCodexSubscriptionFields(t, entry)
}

func TestListAuthFilesFromDisk_ExposesCodexSubscriptionFields(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	idToken := testCodexIDToken(t)
	fileData := map[string]any{
		"type":     "codex",
		"email":    "user@example.com",
		"id_token": idToken,
	}
	data, errMarshal := json.Marshal(fileData)
	if errMarshal != nil {
		t.Fatalf("failed to marshal auth file: %v", errMarshal)
	}
	if errWrite := os.WriteFile(filepath.Join(authDir, "codex-user@example.com-plus.json"), data, 0o600); errWrite != nil {
		t.Fatalf("failed to write auth file: %v", errWrite)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)

	h.ListAuthFiles(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var payload struct {
		Files []map[string]any `json:"files"`
	}
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &payload); errUnmarshal != nil {
		t.Fatalf("failed to decode list payload: %v", errUnmarshal)
	}
	if len(payload.Files) != 1 {
		t.Fatalf("expected 1 auth file, got %d", len(payload.Files))
	}

	assertCodexSubscriptionFields(t, payload.Files[0])
}

func testCodexIDToken(t *testing.T) string {
	t.Helper()

	header := map[string]any{
		"alg": "none",
		"typ": "JWT",
	}
	payload := map[string]any{
		"email": "user@example.com",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id":                "acct_123",
			"chatgpt_plan_type":                 "plus",
			"chatgpt_subscription_active_start": float64(1761955200),
			"chatgpt_subscription_active_until": float64(1764547200),
			"chatgpt_subscription_last_checked": "2026-04-01T02:03:04Z",
			"chatgpt_user_id":                   "user_123",
			"user_id":                           "user_123",
			"groups":                            []any{},
			"organizations":                     []any{},
		},
	}

	headerData, errMarshalHeader := json.Marshal(header)
	if errMarshalHeader != nil {
		t.Fatalf("failed to marshal jwt header: %v", errMarshalHeader)
	}
	payloadData, errMarshalPayload := json.Marshal(payload)
	if errMarshalPayload != nil {
		t.Fatalf("failed to marshal jwt payload: %v", errMarshalPayload)
	}

	encode := base64.RawURLEncoding.EncodeToString
	return encode(headerData) + "." + encode(payloadData) + ".signature"
}

func assertCodexSubscriptionFields(t *testing.T, entry map[string]any) {
	t.Helper()

	if got := entry["plan_type"]; got != "plus" {
		t.Fatalf("plan_type = %#v, want plus", got)
	}
	if got := entry["subscription_active_start"]; got != "2025-11-01T00:00:00Z" {
		t.Fatalf("subscription_active_start = %#v, want 2025-11-01T00:00:00Z", got)
	}
	if got := entry["subscription_active_until"]; got != "2025-12-01T00:00:00Z" {
		t.Fatalf("subscription_active_until = %#v, want 2025-12-01T00:00:00Z", got)
	}
	if got := entry["subscription_last_checked"]; got != "2026-04-01T02:03:04Z" {
		t.Fatalf("subscription_last_checked = %#v, want 2026-04-01T02:03:04Z", got)
	}

	subscription, ok := mapValue(entry["subscription"])
	if !ok {
		t.Fatalf("subscription = %T, want object", entry["subscription"])
	}
	if got := subscription["active_start"]; got != "2025-11-01T00:00:00Z" {
		t.Fatalf("subscription.active_start = %#v, want 2025-11-01T00:00:00Z", got)
	}
	if got := subscription["active_until"]; got != "2025-12-01T00:00:00Z" {
		t.Fatalf("subscription.active_until = %#v, want 2025-12-01T00:00:00Z", got)
	}
	if got := subscription["last_checked"]; got != "2026-04-01T02:03:04Z" {
		t.Fatalf("subscription.last_checked = %#v, want 2026-04-01T02:03:04Z", got)
	}

	idTokenClaims, ok := mapValue(entry["id_token"])
	if !ok {
		t.Fatalf("id_token = %T, want object", entry["id_token"])
	}
	if got := idTokenClaims["plan_type"]; got != "plus" {
		t.Fatalf("id_token.plan_type = %#v, want plus", got)
	}
	if got := idTokenClaims["chatgpt_subscription_active_until"]; got != "2025-12-01T00:00:00Z" {
		t.Fatalf("id_token.chatgpt_subscription_active_until = %#v, want 2025-12-01T00:00:00Z", got)
	}
}

func mapValue(v any) (map[string]any, bool) {
	switch typed := v.(type) {
	case map[string]any:
		return typed, true
	case gin.H:
		return typed, true
	default:
		return nil, false
	}
}
