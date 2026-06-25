package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func sumRecentRequestBuckets(buckets []coreauth.RecentRequestBucket) (int64, int64) {
	var success int64
	var failed int64
	for _, bucket := range buckets {
		success += bucket.Success
		failed += bucket.Failed
	}
	return success, failed
}

func TestGetAPIKeyUsage_GroupsByProviderAndAPIKey(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":  "codex-key",
			"base_url": "https://codex.example.com",
		},
	}); err != nil {
		t.Fatalf("register codex auth: %v", err)
	}
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "claude-auth",
		Provider: "claude",
		Attributes: map[string]string{
			"api_key":  "claude-key",
			"base_url": "https://claude.example.com",
		},
	}); err != nil {
		t.Fatalf("register claude auth: %v", err)
	}

	manager.MarkResult(context.Background(), coreauth.Result{AuthID: "codex-auth", Provider: "codex", Model: "gpt-5", Success: true})
	manager.MarkResult(context.Background(), coreauth.Result{AuthID: "codex-auth", Provider: "codex", Model: "gpt-5", Success: false})
	manager.MarkResult(context.Background(), coreauth.Result{AuthID: "claude-auth", Provider: "claude", Model: "claude-4", Success: true})

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodGet, "/v0/management/api-key-usage", nil)
	ginCtx.Request = req
	h.GetAPIKeyUsage(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload map[string]map[string]apiKeyUsageEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}

	codexEntry := payload["codex"]["https://codex.example.com|codex-key"]
	if codexEntry.Success != 1 || codexEntry.Failed != 1 {
		t.Fatalf("codex totals = %d/%d, want 1/1", codexEntry.Success, codexEntry.Failed)
	}
	if len(codexEntry.RecentRequests) != 20 {
		t.Fatalf("codex buckets len = %d, want 20", len(codexEntry.RecentRequests))
	}
	codexSuccess, codexFailed := sumRecentRequestBuckets(codexEntry.RecentRequests)
	if codexSuccess != 1 || codexFailed != 1 {
		t.Fatalf("codex totals = %d/%d, want 1/1", codexSuccess, codexFailed)
	}

	claudeEntry := payload["claude"]["https://claude.example.com|claude-key"]
	if claudeEntry.Success != 1 || claudeEntry.Failed != 0 {
		t.Fatalf("claude totals = %d/%d, want 1/0", claudeEntry.Success, claudeEntry.Failed)
	}
	if len(claudeEntry.RecentRequests) != 20 {
		t.Fatalf("claude buckets len = %d, want 20", len(claudeEntry.RecentRequests))
	}
	claudeSuccess, claudeFailed := sumRecentRequestBuckets(claudeEntry.RecentRequests)
	if claudeSuccess != 1 || claudeFailed != 0 {
		t.Fatalf("claude totals = %d/%d, want 1/0", claudeSuccess, claudeFailed)
	}
}

func TestGetAPIKeyUsage_IncludesCommandAuthCredentials(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "gemini-command-auth",
		Provider: "gemini",
		Attributes: map[string]string{
			coreauth.AttrAuthKind:       coreauth.AttrAuthKindAPIKey,
			coreauth.AttrAuthSource:     coreauth.AttrAuthSourceCommand,
			coreauth.AttrAuthCommand:    "fetch-token",
			coreauth.AttrAuthCommandKey: "command-identity",
			"base_url":                  "https://gemini.example.com",
		},
	}); err != nil {
		t.Fatalf("register command auth: %v", err)
	}

	manager.MarkResult(context.Background(), coreauth.Result{AuthID: "gemini-command-auth", Provider: "gemini", Model: "gemini-pro", Success: true})
	manager.MarkResult(context.Background(), coreauth.Result{AuthID: "gemini-command-auth", Provider: "gemini", Model: "gemini-pro", Success: false})

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodGet, "/v0/management/api-key-usage", nil)
	ginCtx.Request = req
	h.GetAPIKeyUsage(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload map[string]map[string]apiKeyUsageEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}

	entry := payload["gemini"]["https://gemini.example.com|"+commandAuthManagementKey("command-identity")]
	if entry.Success != 1 || entry.Failed != 1 {
		t.Fatalf("command auth totals = %d/%d, want 1/1", entry.Success, entry.Failed)
	}
	if entry.AuthSource != coreauth.AttrAuthSourceCommand {
		t.Fatalf("command auth source = %q, want %q", entry.AuthSource, coreauth.AttrAuthSourceCommand)
	}
	if entry.AuthKey != commandAuthManagementKey("command-identity") {
		t.Fatalf("command auth key = %q", entry.AuthKey)
	}
	if entry.AuthIndex == "" {
		t.Fatal("command auth index is empty")
	}
	success, failed := sumRecentRequestBuckets(entry.RecentRequests)
	if success != 1 || failed != 1 {
		t.Fatalf("command auth bucket totals = %d/%d, want 1/1", success, failed)
	}
}

func TestGetAPIKeyUsage_SeparatesCommandAuthCredentialsWithSameBaseURL(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	manager := coreauth.NewManager(nil, nil, nil)
	for _, auth := range []*coreauth.Auth{
		{
			ID:       "command-auth-a",
			Provider: "gemini",
			Attributes: map[string]string{
				coreauth.AttrAuthKind:       coreauth.AttrAuthKindAPIKey,
				coreauth.AttrAuthSource:     coreauth.AttrAuthSourceCommand,
				coreauth.AttrAuthCommand:    "fetch-token-a",
				coreauth.AttrAuthCommandKey: "command-identity-a",
				"base_url":                  "https://gemini.example.com",
			},
		},
		{
			ID:       "command-auth-b",
			Provider: "gemini",
			Attributes: map[string]string{
				coreauth.AttrAuthKind:       coreauth.AttrAuthKindAPIKey,
				coreauth.AttrAuthSource:     coreauth.AttrAuthSourceCommand,
				coreauth.AttrAuthCommand:    "fetch-token-b",
				coreauth.AttrAuthCommandKey: "command-identity-b",
				"base_url":                  "https://gemini.example.com",
			},
		},
	} {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("register command auth %s: %v", auth.ID, err)
		}
	}

	manager.MarkResult(context.Background(), coreauth.Result{AuthID: "command-auth-a", Provider: "gemini", Model: "gemini-pro", Success: true})
	manager.MarkResult(context.Background(), coreauth.Result{AuthID: "command-auth-b", Provider: "gemini", Model: "gemini-pro", Success: false})

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodGet, "/v0/management/api-key-usage", nil)
	ginCtx.Request = req
	h.GetAPIKeyUsage(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload map[string]map[string]apiKeyUsageEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}

	geminiBucket := payload["gemini"]
	firstKey := "https://gemini.example.com|" + commandAuthManagementKey("command-identity-a")
	secondKey := "https://gemini.example.com|" + commandAuthManagementKey("command-identity-b")
	first := geminiBucket[firstKey]
	second := geminiBucket[secondKey]
	if first.Success != 1 || first.Failed != 0 {
		t.Fatalf("first command auth totals = %d/%d, want 1/0 payload=%#v", first.Success, first.Failed, geminiBucket)
	}
	if second.Success != 0 || second.Failed != 1 {
		t.Fatalf("second command auth totals = %d/%d, want 0/1 payload=%#v", second.Success, second.Failed, geminiBucket)
	}
	if _, exists := geminiBucket["https://gemini.example.com|"]; exists {
		t.Fatalf("legacy command auth key should not be used when auth_key is available: %#v", geminiBucket)
	}
}

func TestGetAPIKeyUsage_GroupsOpenAICompatibleByCompatName(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "vast-auth",
		Provider: "openai-compatible-vast",
		Attributes: map[string]string{
			"api_key":     "vast-key",
			"base_url":    "https://www.vastnum.com/v1",
			"compat_name": "VAST",
		},
	}); err != nil {
		t.Fatalf("register vast auth: %v", err)
	}

	manager.MarkResult(context.Background(), coreauth.Result{AuthID: "vast-auth", Provider: "openai-compatible-vast", Model: "gpt-5", Success: true})

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodGet, "/v0/management/api-key-usage", nil)
	ginCtx.Request = req
	h.GetAPIKeyUsage(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload map[string]map[string]apiKeyUsageEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}

	if _, exists := payload["openai-compatible-vast"]; exists {
		t.Fatalf("unexpected namespaced provider bucket in payload: %#v", payload)
	}
	vastBucket, exists := payload["vast"]
	if !exists {
		t.Fatalf("missing compat provider bucket in payload: %#v", payload)
	}
	vastEntry := vastBucket["https://www.vastnum.com/v1|vast-key"]
	if vastEntry.Success != 1 || vastEntry.Failed != 0 {
		t.Fatalf("vast totals = %d/%d, want 1/0", vastEntry.Success, vastEntry.Failed)
	}
}
