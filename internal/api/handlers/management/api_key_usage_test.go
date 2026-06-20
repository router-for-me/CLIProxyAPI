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

	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "vast-auth",
		Provider: "openai-compatible-vast",
		Attributes: map[string]string{
			"api_key":     "vast-key",
			"base_url":    "https://www.vastnum.com/v1",
			"compat_name": "VAST",
		},
	}); err != nil {
		t.Fatalf("register vast openai-compat auth: %v", err)
	}
	manager.MarkResult(context.Background(), coreauth.Result{AuthID: "codex-auth", Provider: "codex", Model: "gpt-5", Success: true})
	manager.MarkResult(context.Background(), coreauth.Result{AuthID: "codex-auth", Provider: "codex", Model: "gpt-5", Success: false})
	manager.MarkResult(context.Background(), coreauth.Result{AuthID: "claude-auth", Provider: "claude", Model: "claude-4", Success: true})
	manager.MarkResult(context.Background(), coreauth.Result{AuthID: "vast-auth", Provider: "openai-compatible-vast", Model: "deepseek-v3", Success: true})
	manager.MarkResult(context.Background(), coreauth.Result{AuthID: "vast-auth", Provider: "openai-compatible-vast", Model: "deepseek-v3", Success: false})

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

	// OpenAI-compatible providers carry a namespaced auth.Provider (e.g.
	// "openai-compatible-vast") but must be grouped under their bare config name
	// ("vast") so the Management Center panel, which looks up by provider name,
	// can match recent-requests and totals. Regression for #3940.
	if _, ok := payload["openai-compatible-vast"]; ok {
		t.Fatalf("openai-compat auth should NOT be grouped under namespaced key %q", "openai-compatible-vast")
	}
	vastEntry, ok := payload["vast"]["https://www.vastnum.com/v1|vast-key"]
	if !ok {
		t.Fatalf("vast entry missing under bare provider key; payload keys = %v", payloadKeys(payload))
	}
	if vastEntry.Success != 1 || vastEntry.Failed != 1 {
		t.Fatalf("vast totals = %d/%d, want 1/1", vastEntry.Success, vastEntry.Failed)
	}
	if len(vastEntry.RecentRequests) != 20 {
		t.Fatalf("vast buckets len = %d, want 20", len(vastEntry.RecentRequests))
	}
	vastSuccess, vastFailed := sumRecentRequestBuckets(vastEntry.RecentRequests)
	if vastSuccess != 1 || vastFailed != 1 {
		t.Fatalf("vast totals = %d/%d, want 1/1", vastSuccess, vastFailed)
	}
}
func TestUsageProviderKey(t *testing.T) {
	tests := []struct {
		name string
		auth *coreauth.Auth
		want string
	}{
		{
			name: "nil auth",
			auth: nil,
			want: "unknown",
		},
		{
			name: "empty provider",
			auth: &coreauth.Auth{Provider: ""},
			want: "unknown",
		},
		{
			name: "non-openai-compat provider passes through",
			auth: &coreauth.Auth{Provider: "codex"},
			want: "codex",
		},
		{
			name: "openai-compat uses bare compat_name",
			auth: &coreauth.Auth{
				Provider:   "openai-compatible-vast",
				Attributes: map[string]string{"compat_name": "VAST"},
			},
			want: "vast",
		},
		{
			name: "openai-compat without compat_name strips namespace fallback",
			auth: &coreauth.Auth{
				Provider:   "openai-compatible-vast",
				Attributes: map[string]string{},
			},
			want: "vast",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := usageProviderKey(tt.auth); got != tt.want {
				t.Fatalf("usageProviderKey(%+v) = %q, want %q", tt.auth, got, tt.want)
			}
		})
	}
}

func payloadKeys(m map[string]map[string]apiKeyUsageEntry) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
