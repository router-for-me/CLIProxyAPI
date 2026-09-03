package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

const claudeOAuthUsageFixture = `{
  "five_hour": {"utilization": 100.0, "resets_at": "2026-09-02T16:19:59+00:00"},
  "seven_day": {"utilization": 52.0, "resets_at": "2026-09-07T14:59:59+00:00"},
  "limits": [
    {"kind": "session", "group": "session", "percent": 100, "resets_at": "2026-09-02T16:19:59+00:00", "scope": null},
    {"kind": "weekly_all", "group": "weekly", "percent": 52, "resets_at": "2026-09-07T14:59:59+00:00", "scope": null},
    {"kind": "weekly_scoped", "group": "weekly", "percent": 51, "resets_at": "2026-09-07T14:59:59+00:00", "scope": {"model": {"id": null, "display_name": "Opus"}}}
  ]
}`

type authFileQuotaResponse struct {
	Files []authFileQuotaEntry `json:"files"`
}

func runAuthFileQuota(t *testing.T, h *Handler, rawQuery string) authFileQuotaResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files/quota?"+rawQuery, nil)
	h.GetAuthFileQuota(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var out authFileQuotaResponse
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &out); errUnmarshal != nil {
		t.Fatalf("decode response: %v", errUnmarshal)
	}
	return out
}

func TestGetAuthFileQuotaReportsClaudeOAuthWindows(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var headersMu sync.Mutex
	var gotAuthorization, gotBeta string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headersMu.Lock()
		gotAuthorization = r.Header.Get("Authorization")
		gotBeta = r.Header.Get("anthropic-beta")
		headersMu.Unlock()
		if r.Header.Get("Authorization") == "Bearer expired-token" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"type":"authentication_error","message":"secret detail"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(claudeOAuthUsageFixture))
	}))
	defer upstream.Close()
	previousURL := claudeOAuthUsageURL
	claudeOAuthUsageURL = upstream.URL
	defer func() { claudeOAuthUsageURL = previousURL }()

	manager := coreauth.NewManager(nil, nil, nil)
	oauthAuth := &coreauth.Auth{
		ID:         "claude-oauth.json",
		FileName:   "claude-oauth.json",
		Provider:   "claude",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"path": "/tmp/claude-oauth.json"},
		Metadata:   map[string]any{"access_token": "live-token", "email": "alice@example.com"},
	}
	registerAuthForLookupTest(t, manager, oauthAuth)
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID:         "claude-key",
		Provider:   "claude",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"api_key": "sk-ant-key"},
	})
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID:         "codex.json",
		FileName:   "codex.json",
		Provider:   "codex",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"path": "/tmp/codex.json"},
		Metadata:   map[string]any{"access_token": "codex-token"},
	})
	h := &Handler{authManager: manager}

	out := runAuthFileQuota(t, h, "")
	if len(out.Files) != 1 {
		t.Fatalf("files = %#v, want only the Claude OAuth credential", out.Files)
	}
	if gotAuthorization != "Bearer live-token" {
		t.Fatalf("Authorization = %q, want the metadata access token", gotAuthorization)
	}
	if gotBeta != claudeOAuthUsageBeta {
		t.Fatalf("anthropic-beta = %q, want %q", gotBeta, claudeOAuthUsageBeta)
	}
	entry := out.Files[0]
	if entry.Name != "claude-oauth.json" || entry.Email != "alice@example.com" || entry.Provider != "claude" {
		t.Fatalf("entry identity = %#v", entry)
	}
	if entry.AuthIndex != oauthAuth.EnsureIndex() {
		t.Fatalf("auth_index = %q, want %q", entry.AuthIndex, oauthAuth.EnsureIndex())
	}
	if entry.Error != "" || entry.StatusCode != http.StatusOK {
		t.Fatalf("entry error = %q status = %d", entry.Error, entry.StatusCode)
	}
	want := []authFileQuotaWindow{
		{Kind: "session", Utilization: 100, ResetsAt: "2026-09-02T16:19:59+00:00"},
		{Kind: "weekly_all", Utilization: 52, ResetsAt: "2026-09-07T14:59:59+00:00"},
		{Kind: "weekly_scoped", Utilization: 51, ResetsAt: "2026-09-07T14:59:59+00:00", Scope: "Opus"},
	}
	if len(entry.Windows) != len(want) {
		t.Fatalf("windows = %#v, want %#v", entry.Windows, want)
	}
	for i := range want {
		if entry.Windows[i] != want[i] {
			t.Fatalf("window %d = %#v, want %#v", i, entry.Windows[i], want[i])
		}
	}

	// A failing credential reports its own error without hiding its siblings,
	// and the upstream error body stays out of the response.
	expiredAuth := &coreauth.Auth{
		ID:         "claude-expired.json",
		FileName:   "claude-expired.json",
		Provider:   "claude",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"path": "/tmp/claude-expired.json"},
		Metadata:   map[string]any{"access_token": "expired-token"},
	}
	registerAuthForLookupTest(t, manager, expiredAuth)
	out = runAuthFileQuota(t, h, "")
	if len(out.Files) != 2 {
		t.Fatalf("files = %#v, want two Claude OAuth credentials", out.Files)
	}
	var expired *authFileQuotaEntry
	for i := range out.Files {
		if out.Files[i].Name == "claude-expired.json" {
			expired = &out.Files[i]
		}
	}
	if expired == nil {
		t.Fatalf("expired credential missing from %#v", out.Files)
	}
	if expired.StatusCode != http.StatusUnauthorized || expired.Error != "upstream returned status 401" || len(expired.Windows) != 0 {
		t.Fatalf("expired entry = %#v", *expired)
	}

	// auth_index narrows the fan-out to one credential.
	out = runAuthFileQuota(t, h, "auth_index="+expiredAuth.EnsureIndex())
	if len(out.Files) != 1 || out.Files[0].Name != "claude-expired.json" {
		t.Fatalf("filtered files = %#v, want only the expired credential", out.Files)
	}
}

func TestParseClaudeOAuthUsageFallsBackToLegacyWindows(t *testing.T) {
	windows, errParse := parseClaudeOAuthUsage([]byte(`{
	  "five_hour": {"utilization": 12.5, "resets_at": "2026-09-02T16:19:59+00:00"},
	  "seven_day": {"utilization": 3, "resets_at": null}
	}`))
	if errParse != nil {
		t.Fatalf("parse: %v", errParse)
	}
	want := []authFileQuotaWindow{
		{Kind: "session", Utilization: 12.5, ResetsAt: "2026-09-02T16:19:59+00:00"},
		{Kind: "weekly_all", Utilization: 3},
	}
	if len(windows) != len(want) {
		t.Fatalf("windows = %#v, want %#v", windows, want)
	}
	for i := range want {
		if windows[i] != want[i] {
			t.Fatalf("window %d = %#v, want %#v", i, windows[i], want[i])
		}
	}
	if _, errParse = parseClaudeOAuthUsage([]byte(`not json`)); errParse == nil {
		t.Fatal("expected a parse error for a non-JSON body")
	}
}
