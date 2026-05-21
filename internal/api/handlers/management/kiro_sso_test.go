package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestImportKiroSSOTokenRawToken(t *testing.T) {
	h, store := newKiroSSOImportTestHandler(t)
	body := `{
		"access_token": "secret-access",
		"refresh_token": "secret-refresh",
		"profile_arn": "arn:aws:codecatalyst:us-east-1:123456789012:space/test/profile/dev",
		"email": "user@example.com",
		"api_region": "us-east-1"
	}`

	response := performKiroSSOImport(t, h, body, "")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "secret-access") || strings.Contains(response.Body.String(), "secret-refresh") {
		t.Fatalf("response leaked token material: %s", response.Body.String())
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.items) != 1 {
		t.Fatalf("saved items = %d, want 1", len(store.items))
	}
	auth := store.items["kiro-user-example.com.json"]
	if auth == nil {
		t.Fatalf("expected sanitized kiro auth file, got %#v", store.items)
	}
	if auth.Provider != "kiro" {
		t.Fatalf("provider = %q, want kiro", auth.Provider)
	}
	if auth.Metadata["access_token"] != "secret-access" {
		t.Fatalf("access token was not persisted")
	}
	if auth.Attributes["profile_arn"] == "" {
		t.Fatalf("profile_arn attribute missing")
	}
}

func TestImportKiroSSOTokenWrappedPayload(t *testing.T) {
	h, store := newKiroSSOImportTestHandler(t)
	body := `{
		"name": "team-kiro.json",
		"label": "Team Kiro",
		"prefix": "team/",
		"disabled": true,
		"note": "managed by sync job",
		"priority": 7,
		"token": {
			"accessToken": "secret-access",
			"profileArn": "arn:aws:codecatalyst:us-east-1:123456789012:space/test/profile/dev",
			"apiRegion": "us-west-2"
		}
	}`

	response := performKiroSSOImport(t, h, body, "")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	auth := store.items["team-kiro.json"]
	if auth == nil {
		t.Fatalf("expected team-kiro.json to be saved")
	}
	if auth.Label != "Team Kiro" {
		t.Fatalf("label = %q, want Team Kiro", auth.Label)
	}
	if !auth.Disabled || auth.Status != coreauth.StatusDisabled {
		t.Fatalf("disabled/status = %v/%s, want disabled", auth.Disabled, auth.Status)
	}
	if auth.Prefix != "team/" {
		t.Fatalf("prefix = %q, want team/", auth.Prefix)
	}
	if auth.Attributes["priority"] != "7" {
		t.Fatalf("priority attribute = %q, want 7", auth.Attributes["priority"])
	}
}

func TestImportKiroSSOTokenRequiresProfileARN(t *testing.T) {
	h, _ := newKiroSSOImportTestHandler(t)
	body := `{"access_token":"secret-access"}`

	response := performKiroSSOImport(t, h, body, "")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["error"] != "profile_arn is required" {
		t.Fatalf("error = %v", payload["error"])
	}
}

func newKiroSSOImportTestHandler(t *testing.T) (*Handler, *memoryAuthStore) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	store := &memoryAuthStore{items: make(map[string]*coreauth.Auth)}
	manager := coreauth.NewManager(store, nil, nil)
	h := NewHandler(&config.Config{AuthDir: t.TempDir()}, "", manager)
	h.tokenStore = store
	return h, store
}

func performKiroSSOImport(t *testing.T, h *Handler, body, query string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	target := "/v0/management/kiro-sso-token"
	if query != "" {
		target += "?" + query
	}
	ctx.Request = httptest.NewRequest(http.MethodPost, target, bytes.NewBufferString(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.ImportKiroSSOToken(ctx)
	return recorder
}
