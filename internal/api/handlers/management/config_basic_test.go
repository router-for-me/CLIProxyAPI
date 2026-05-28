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
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestPutRoutingStrategyAcceptsSequentialFillAlias(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("routing:\n  strategy: round-robin\n"), 0o600); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	cfg := &config.Config{
		Routing: config.RoutingConfig{Strategy: "round-robin"},
	}
	h := NewHandler(cfg, configPath, coreauth.NewManager(nil, nil, nil))

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/routing/strategy", strings.NewReader(`{"value":"sf"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.PutRoutingStrategy(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("PutRoutingStrategy status = %d, want %d with body %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := h.cfg.Routing.Strategy; got != "sequential-fill" {
		t.Fatalf("handler config strategy = %q, want %q", got, "sequential-fill")
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read saved config: %v", err)
	}
	if !strings.Contains(string(data), "sequential-fill") {
		t.Fatalf("saved config = %q, want it to contain %q", string(data), "sequential-fill")
	}
}

func TestPutUsageRetentionDaysPersistsAndUpdatesPlugin(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)
	usage.CloseDatabasePlugin()
	t.Cleanup(usage.CloseDatabasePlugin)

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("usage-retention-days: 30\n"), 0o600); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	cfg := &config.Config{
		AuthDir:                 tmpDir,
		UsagePersistenceEnabled: true,
		UsageRetentionDays:      30,
	}
	if err := usage.InitDatabasePlugin(context.Background(), "", "", tmpDir, cfg.UsageRetentionDays); err != nil {
		t.Fatalf("InitDatabasePlugin failed: %v", err)
	}
	defer usage.CloseDatabasePlugin()

	h := NewHandler(cfg, configPath, coreauth.NewManager(nil, nil, nil))
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/usage-retention-days", strings.NewReader(`{"value":45}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.PutUsageRetentionDays(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("PutUsageRetentionDays status = %d, want %d with body %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := h.cfg.UsageRetentionDays; got != 45 {
		t.Fatalf("handler config retention = %d, want 45", got)
	}
	if got := usage.GetDatabasePlugin(); got == nil {
		t.Fatalf("expected database plugin to remain available")
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read saved config: %v", err)
	}
	if !strings.Contains(string(data), "usage-retention-days: 45") {
		t.Fatalf("saved config = %q, want it to contain usage-retention-days: 45", string(data))
	}
}

func TestGetConfigYAMLReturnsVersionHeaders(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("port: 8317\n"), 0o600); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	h := NewHandler(&config.Config{}, configPath, coreauth.NewManager(nil, nil, nil))
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/config.yaml", nil)

	h.GetConfigYAML(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("GetConfigYAML status = %d, want %d with body %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := rec.Header().Get(configVersionHeader); got == "" {
		t.Fatalf("expected %s response header", configVersionHeader)
	}
	if got := rec.Header().Get(configETagHeader); got == "" {
		t.Fatalf("expected %s response header", configETagHeader)
	}
}

func TestGetConfigIncludesAPIKeyAuthIndex(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{
		GeminiKey: []config.GeminiKey{{
			APIKey:  "sk-shared",
			BaseURL: "https://gemini.example.com",
		}},
		CodexKey: []config.CodexKey{{
			APIKey:  "sk-shared",
			BaseURL: "https://codex.example.com",
		}},
		ClaudeKey: []config.ClaudeKey{{
			APIKey:  "sk-shared",
			BaseURL: "https://claude.example.com",
		}},
		VertexCompatAPIKey: []config.VertexCompatKey{{
			APIKey:   "sk-shared",
			BaseURL:  "https://vertex.example.com",
			ProxyURL: "http://proxy.example.com",
		}},
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:    "kimi",
			BaseURL: "https://api.kimi.com/v1",
			APIKeyEntries: []config.OpenAICompatibilityAPIKey{{
				APIKey: "sk-shared",
			}},
		}},
	}

	idGen := synthesizer.NewStableIDGenerator()
	geminiID, _ := idGen.Next("gemini:apikey", "sk-shared", "https://gemini.example.com")
	codexID, _ := idGen.Next("codex:apikey", "sk-shared", "https://codex.example.com")
	claudeID, _ := idGen.Next("claude:apikey", "sk-shared", "https://claude.example.com")
	vertexID, _ := idGen.Next("vertex:apikey", "sk-shared", "https://vertex.example.com", "http://proxy.example.com")
	openAIID, _ := idGen.Next("openai-compatibility:kimi", "sk-shared", "https://api.kimi.com/v1", "")
	manager := coreauth.NewManager(nil, nil, nil)
	registerConfigAuth := func(id, provider, baseURL, proxyURL string, attrs map[string]string) {
		t.Helper()
		if attrs == nil {
			attrs = map[string]string{}
		}
		attrs["api_key"] = "sk-shared"
		attrs["base_url"] = baseURL
		if _, err := manager.Register(context.Background(), &coreauth.Auth{
			ID:         id,
			Provider:   provider,
			ProxyURL:   proxyURL,
			Attributes: attrs,
		}); err != nil {
			t.Fatalf("register %s auth: %v", provider, err)
		}
	}
	registerConfigAuth(geminiID, "gemini", "https://gemini.example.com", "", nil)
	registerConfigAuth(codexID, "codex", "https://codex.example.com", "", nil)
	registerConfigAuth(vertexID, "vertex", "https://vertex.example.com", "http://proxy.example.com", nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       claudeID,
		Provider: "claude",
		Attributes: map[string]string{
			"api_key":  "sk-shared",
			"base_url": "https://claude.example.com",
		},
	}); err != nil {
		t.Fatalf("register claude auth: %v", err)
	}
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       openAIID,
		Provider: "kimi",
		Attributes: map[string]string{
			"api_key":      "sk-shared",
			"base_url":     "https://api.kimi.com/v1",
			"compat_name":  "kimi",
			"provider_key": "kimi",
		},
	}); err != nil {
		t.Fatalf("register openai-compatible auth: %v", err)
	}

	h := NewHandler(cfg, writeTestConfigFile(t), manager)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/config", nil)

	h.GetConfig(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("GetConfig status = %d, want %d with body %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body struct {
		ClaudeAPIKey []struct {
			AuthIndex string `json:"auth-index"`
		} `json:"claude-api-key"`
		GeminiAPIKey []struct {
			AuthIndex string `json:"auth-index"`
		} `json:"gemini-api-key"`
		CodexAPIKey []struct {
			AuthIndex string `json:"auth-index"`
		} `json:"codex-api-key"`
		VertexAPIKey []struct {
			AuthIndex string `json:"auth-index"`
		} `json:"vertex-api-key"`
		OpenAICompatibility []struct {
			APIKeyEntries []struct {
				AuthIndex string `json:"auth-index"`
			} `json:"api-key-entries"`
		} `json:"openai-compatibility"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode config response: %v", err)
	}
	geminiIndex := body.GeminiAPIKey[0].AuthIndex
	codexIndex := body.CodexAPIKey[0].AuthIndex
	claudeIndex := body.ClaudeAPIKey[0].AuthIndex
	vertexIndex := body.VertexAPIKey[0].AuthIndex
	openAIIndex := body.OpenAICompatibility[0].APIKeyEntries[0].AuthIndex
	for name, index := range map[string]string{
		"gemini":            geminiIndex,
		"codex":             codexIndex,
		"claude":            claudeIndex,
		"vertex":            vertexIndex,
		"openai-compatible": openAIIndex,
	} {
		if index == "" {
			t.Fatalf("expected %s auth-index in full config response", name)
		}
	}
	if claudeIndex == "" {
		t.Fatal("expected claude auth-index in full config response")
	}
	if openAIIndex == "" {
		t.Fatal("expected openai-compatible auth-index in full config response")
	}
	indexes := map[string]struct{}{}
	for _, index := range []string{geminiIndex, codexIndex, claudeIndex, vertexIndex, openAIIndex} {
		if _, exists := indexes[index]; exists {
			t.Fatalf("expected same api key across channels to have distinct auth indexes, duplicate %q", index)
		}
		indexes[index] = struct{}{}
	}
}

func TestPutConfigYAMLRejectsStaleVersion(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	original := []byte("port: 8317\n")
	if err := os.WriteFile(configPath, original, 0o600); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	h := NewHandler(&config.Config{}, configPath, coreauth.NewManager(nil, nil, nil))
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/config.yaml", strings.NewReader("port: 9000\n"))
	ctx.Request.Header.Set("If-Match", `"sha256:stale"`)

	h.PutConfigYAML(ctx)

	if rec.Code != http.StatusConflict {
		t.Fatalf("PutConfigYAML status = %d, want %d with body %s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode conflict response: %v", err)
	}
	if got := resp["error"]; got != "config_conflict" {
		t.Fatalf("conflict error = %v, want config_conflict", got)
	}
	if got := resp["current-version"]; got == "" {
		t.Fatalf("expected current-version in conflict response: %#v", resp)
	}
	if got := resp["submitted-version"]; got != "sha256:stale" {
		t.Fatalf("submitted-version = %v, want sha256:stale", got)
	}
	if got := rec.Header().Get(configVersionHeader); got == "" {
		t.Fatalf("expected %s response header on conflict", configVersionHeader)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config file: %v", err)
	}
	if string(data) != string(original) {
		t.Fatalf("config file changed after conflict: %q", string(data))
	}
}

func TestStructuredConfigWriteRejectsStaleVersionAndRestoresMemory(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("routing:\n  strategy: round-robin\n"), 0o600); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	cfg := &config.Config{
		Routing: config.RoutingConfig{Strategy: "round-robin"},
	}
	h := NewHandler(cfg, configPath, coreauth.NewManager(nil, nil, nil))
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/routing/strategy", strings.NewReader(`{"value":"sf"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Request.Header.Set("If-Match", `"sha256:stale"`)

	h.PutRoutingStrategy(ctx)

	if rec.Code != http.StatusConflict {
		t.Fatalf("PutRoutingStrategy status = %d, want %d with body %s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	if got := h.cfg.Routing.Strategy; got != "round-robin" {
		t.Fatalf("handler config strategy = %q, want restored round-robin", got)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config file: %v", err)
	}
	if !strings.Contains(string(data), "round-robin") {
		t.Fatalf("saved config = %q, want original round-robin", string(data))
	}
}

func TestCleanupMonitorLogsDeletesExpiredRecords(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)
	usage.CloseDatabasePlugin()
	t.Cleanup(usage.CloseDatabasePlugin)

	tmpDir := t.TempDir()
	if err := usage.InitDatabasePlugin(context.Background(), "", "", tmpDir, 30); err != nil {
		t.Fatalf("InitDatabasePlugin failed: %v", err)
	}
	defer usage.CloseDatabasePlugin()
	plugin := usage.GetDatabasePlugin()
	if plugin == nil {
		t.Fatalf("expected database plugin to be initialized")
	}

	oldTime := time.Now().Add(-45 * 24 * time.Hour)
	newTime := time.Now().Add(-5 * 24 * time.Hour)
	added, skipped, err := plugin.ImportRecords(usage.StatisticsSnapshot{
		APIs: map[string]usage.APISnapshot{
			"api-test": {
				Models: map[string]usage.ModelSnapshot{
					"model-old": {
						Details: []usage.RequestDetail{
							{Timestamp: oldTime, Source: "source-old", Failed: false, Tokens: usage.TokenStats{TotalTokens: 1}},
						},
					},
					"model-new": {
						Details: []usage.RequestDetail{
							{Timestamp: newTime, Source: "source-new", Failed: false, Tokens: usage.TokenStats{TotalTokens: 1}},
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("ImportRecords failed: %v", err)
	}
	if added != 2 || skipped != 0 {
		t.Fatalf("unexpected import result: added=%d skipped=%d", added, skipped)
	}

	h := NewHandler(&config.Config{UsagePersistenceEnabled: true, UsageRetentionDays: 30}, "", coreauth.NewManager(nil, nil, nil))
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/custom/monitor-cleanup", nil)

	h.CleanupMonitorLogs(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("CleanupMonitorLogs status = %d, want %d with body %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Deleted int64 `json:"deleted"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
	if resp.Deleted < 1 {
		t.Fatalf("expected at least one deleted record, got %d", resp.Deleted)
	}
}
