package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestDownloadCodexCLIOAuthFile_Success(t *testing.T) {
	gin.SetMode(gin.TestMode)

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	_, _ = manager.Register(context.Background(), &coreauth.Auth{
		ID:       "codex-1",
		Provider: "codex",
		FileName: "codex-main.json",
		Metadata: map[string]any{
			"id_token":      "id-token-1",
			"access_token":  "access-token-1",
			"refresh_token": "refresh-token-1",
			"account_id":    "account-1",
		},
	})

	h := &Handler{authManager: manager}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/codex-cli-oauth-file?name=codex-main.json", nil)

	h.DownloadCodexCLIOAuthFile(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var payload codexCLIOAuthFile
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode payload: %v", err)
	}
	if payload.AuthMethod != "chatgpt" {
		t.Fatalf("expected auth_method chatgpt, got %q", payload.AuthMethod)
	}
	if payload.Tokens.IDToken != "id-token-1" || payload.Tokens.AccessToken != "access-token-1" || payload.Tokens.RefreshToken != "refresh-token-1" {
		t.Fatalf("unexpected token payload: %+v", payload.Tokens)
	}
}

func TestDownloadCodexCLIOAuthFile_Errors(t *testing.T) {
	gin.SetMode(gin.TestMode)

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	_, _ = manager.Register(context.Background(), &coreauth.Auth{
		ID:       "claude-1",
		Provider: "claude",
		FileName: "claude-main.json",
	})
	_, _ = manager.Register(context.Background(), &coreauth.Auth{
		ID:       "codex-2",
		Provider: "codex",
		FileName: "codex-missing.json",
		Metadata: map[string]any{
			"access_token": "token-only",
		},
	})

	h := &Handler{authManager: manager}

	cases := []struct {
		name       string
		query      string
		statusCode int
	}{
		{name: "missing name", query: "", statusCode: http.StatusBadRequest},
		{name: "not found", query: "name=missing.json", statusCode: http.StatusNotFound},
		{name: "wrong provider", query: "name=claude-main.json", statusCode: http.StatusBadRequest},
		{name: "missing token fields", query: "name=codex-missing.json", statusCode: http.StatusBadRequest},
	}

	for _, tc := range cases {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		url := "/v0/management/codex-cli-oauth-file"
		if tc.query != "" {
			url += "?" + tc.query
		}
		c.Request = httptest.NewRequest(http.MethodGet, url, nil)
		h.DownloadCodexCLIOAuthFile(c)
		if rec.Code != tc.statusCode {
			t.Fatalf("%s: expected %d, got %d body=%s", tc.name, tc.statusCode, rec.Code, rec.Body.String())
		}
	}
}
