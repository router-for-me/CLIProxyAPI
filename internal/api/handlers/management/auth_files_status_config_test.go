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

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func setupConfigBackedClaudeStatusTest(t *testing.T) (*Handler, *coreauth.Manager, string, string) {
	t.Helper()
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("routing:\n  strategy: round-robin\nclaude-api-key:\n  - api-key: sk-test\n    base-url: https://claude.example.com\n"), 0o600); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	cfg := &config.Config{
		AuthDir: tmpDir,
		Routing: config.RoutingConfig{Strategy: "round-robin"},
		ClaudeKey: []config.ClaudeKey{{
			APIKey:  "sk-test",
			BaseURL: "https://claude.example.com",
		}},
	}
	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandler(cfg, configPath, manager)
	h.mu.Lock()
	h.syncRuntimeConfigLocked(context.Background())
	h.mu.Unlock()

	authID, _ := synthesizer.NewStableIDGenerator().Next("claude:apikey", "sk-test", "https://claude.example.com")
	if auth, ok := manager.GetByID(authID); !ok || auth == nil {
		t.Fatalf("expected config-backed auth %s to be synthesized", authID)
	}

	return h, manager, configPath, authID
}

func TestSyncRuntimeConfigRemovesDeletedConfigBackedOpenAICompatKey(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	cfg := &config.Config{
		AuthDir: tmpDir,
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:    "minimax",
			BaseURL: "https://api.minimax.example.com/v1",
			APIKeyEntries: []config.OpenAICompatibilityAPIKey{
				{APIKey: "sk-active"},
				{APIKey: "sk-deleted"},
			},
			Models: []config.OpenAICompatibilityModel{{
				Name:  "minimax-m2",
				Alias: "minimax-m2",
			}},
		}},
	}
	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandler(cfg, configPath, manager)

	h.mu.Lock()
	h.syncRuntimeConfigLocked(context.Background())
	h.mu.Unlock()

	idGen := synthesizer.NewStableIDGenerator()
	activeID, _ := idGen.Next("openai-compatibility:minimax", "sk-active", "https://api.minimax.example.com/v1", "")
	deletedID, _ := idGen.Next("openai-compatibility:minimax", "sk-deleted", "https://api.minimax.example.com/v1", "")
	deletedAuth, ok := manager.GetByID(deletedID)
	if !ok || deletedAuth == nil {
		t.Fatalf("expected deleted candidate auth %s before config removal", deletedID)
	}
	deletedIndex := deletedAuth.EnsureIndex()

	cfg.OpenAICompatibility[0].APIKeyEntries = cfg.OpenAICompatibility[0].APIKeyEntries[:1]
	h.mu.Lock()
	h.syncRuntimeConfigLocked(context.Background())
	h.mu.Unlock()

	if _, ok := manager.GetByID(activeID); !ok {
		t.Fatalf("expected remaining config-backed auth %s to stay registered", activeID)
	}
	if auth, ok := manager.GetByID(deletedID); ok {
		t.Fatalf("expected deleted config-backed auth %s to be removed, got disabled=%v status=%s", deletedID, auth.Disabled, auth.Status)
	}

	resolver := newMonitorSourceResolver(cfg, manager)
	if ref := resolver.Resolve("", deletedIndex); ref.EntityKind != "unknown" {
		t.Fatalf("deleted auth index resolved to %+v, want unknown", ref)
	}
}

func TestPatchAuthFileStatusConfigBackedReturnsConfigVersionForNextSave(t *testing.T) {
	h, manager, configPath, authID := setupConfigBackedClaudeStatusTest(t)

	initialSnap, err := h.readConfigSnapshot()
	if err != nil {
		t.Fatalf("failed to read initial config snapshot: %v", err)
	}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/status", strings.NewReader(`{"name":"`+authID+`","disabled":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", configETag(initialSnap.version))
	ctx.Request = req

	h.PatchAuthFileStatus(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("PatchAuthFileStatus status = %d, want %d with body %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	nextVersion := rec.Header().Get(configVersionHeader)
	if nextVersion == "" {
		t.Fatalf("expected %s response header", configVersionHeader)
	}
	if nextVersion == initialSnap.version {
		t.Fatalf("expected config version to change after disabling config-backed auth")
	}
	var resp map[string]any
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &resp); errDecode != nil {
		t.Fatalf("failed to decode response body: %v", errDecode)
	}
	if got := resp["config-version"]; got != nextVersion {
		t.Fatalf("response config-version = %v, want %s", got, nextVersion)
	}

	disabled, ok := manager.GetByID(authID)
	if !ok || disabled == nil {
		t.Fatalf("expected auth %s after status patch", authID)
	}
	if !disabled.Disabled || disabled.Status != coreauth.StatusDisabled {
		t.Fatalf("expected auth disabled after status patch, got disabled=%v status=%s", disabled.Disabled, disabled.Status)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config file: %v", err)
	}
	if !strings.Contains(string(data), "disabled: true") {
		t.Fatalf("expected disabled state to be persisted, got config:\n%s", string(data))
	}

	nextRec := httptest.NewRecorder()
	nextCtx, _ := gin.CreateTestContext(nextRec)
	nextReq := httptest.NewRequest(http.MethodPut, "/v0/management/routing/strategy", strings.NewReader(`{"value":"sf"}`))
	nextReq.Header.Set("Content-Type", "application/json")
	nextReq.Header.Set("If-Match", configETag(nextVersion))
	nextCtx.Request = nextReq

	h.PutRoutingStrategy(nextCtx)

	if nextRec.Code != http.StatusOK {
		t.Fatalf("PutRoutingStrategy after auth status save status = %d, want %d with body %s", nextRec.Code, http.StatusOK, nextRec.Body.String())
	}
}

func TestPatchAuthFileStatusConfigBackedRejectsStaleVersion(t *testing.T) {
	h, manager, configPath, authID := setupConfigBackedClaudeStatusTest(t)

	initialSnap, err := h.readConfigSnapshot()
	if err != nil {
		t.Fatalf("failed to read initial config snapshot: %v", err)
	}
	if errWrite := os.WriteFile(configPath, []byte("debug: true\nrouting:\n  strategy: round-robin\nclaude-api-key:\n  - api-key: sk-test\n    base-url: https://claude.example.com\n"), 0o600); errWrite != nil {
		t.Fatalf("failed to externally update config file: %v", errWrite)
	}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/status", strings.NewReader(`{"name":"`+authID+`","disabled":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", configETag(initialSnap.version))
	ctx.Request = req

	h.PatchAuthFileStatus(ctx)

	if rec.Code != http.StatusConflict {
		t.Fatalf("PatchAuthFileStatus status = %d, want %d with body %s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	if got := rec.Header().Get(configVersionHeader); got == "" {
		t.Fatalf("expected %s response header on conflict", configVersionHeader)
	}
	auth, ok := manager.GetByID(authID)
	if !ok || auth == nil {
		t.Fatalf("expected auth %s to remain available", authID)
	}
	if auth.Disabled || auth.Status == coreauth.StatusDisabled {
		t.Fatalf("expected runtime auth to remain active after stale config write, got disabled=%v status=%s", auth.Disabled, auth.Status)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config file: %v", err)
	}
	if strings.Contains(string(data), "disabled: true") {
		t.Fatalf("config file changed after stale status patch:\n%s", string(data))
	}
	if !h.cfg.Debug {
		t.Fatalf("expected handler config to reload external config after conflict")
	}
}

func TestPatchAuthFileStatusConfigBackedMatchesBaseURL(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("claude-api-key:\n  - api-key: shared-key\n    base-url: https://a.example.com\n  - api-key: shared-key\n    base-url: https://b.example.com\n"), 0o600); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	cfg := &config.Config{
		AuthDir: tmpDir,
		ClaudeKey: []config.ClaudeKey{
			{APIKey: "shared-key", BaseURL: "https://a.example.com"},
			{APIKey: "shared-key", BaseURL: "https://b.example.com"},
		},
	}
	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandler(cfg, configPath, manager)
	h.mu.Lock()
	h.syncRuntimeConfigLocked(context.Background())
	h.mu.Unlock()

	authAID, _ := synthesizer.NewStableIDGenerator().Next("claude:apikey", "shared-key", "https://a.example.com")
	authBID, _ := synthesizer.NewStableIDGenerator().Next("claude:apikey", "shared-key", "https://b.example.com")

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/status", strings.NewReader(`{"name":"`+authBID+`","disabled":true}`))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	h.PatchAuthFileStatus(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("PatchAuthFileStatus status = %d, want %d with body %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if h.cfg.ClaudeKey[0].Disabled {
		t.Fatalf("first matching API key with a different base URL should remain enabled")
	}
	if !h.cfg.ClaudeKey[1].Disabled {
		t.Fatalf("target matching API key and base URL should be disabled")
	}
	authA, ok := manager.GetByID(authAID)
	if !ok || authA == nil || authA.Disabled {
		t.Fatalf("expected auth %s to remain enabled", authAID)
	}
	authB, ok := manager.GetByID(authBID)
	if !ok || authB == nil || !authB.Disabled {
		t.Fatalf("expected auth %s to be disabled", authBID)
	}
}

func TestPatchAuthFileStatusConfigBackedMatchesOpenAICompatDuplicateByID(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("openai-compatibility:\n  - name: kimi\n    base-url: https://api.kimi.example.com/v1\n    api-key-entries:\n      - api-key: sk-duplicate\n        disabled: true\n      - api-key: sk-duplicate\n    models:\n      - name: kimi-k2\n        alias: kimi-k2\n"), 0o600); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	cfg := &config.Config{
		AuthDir: tmpDir,
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:    "kimi",
			BaseURL: "https://api.kimi.example.com/v1",
			APIKeyEntries: []config.OpenAICompatibilityAPIKey{
				{APIKey: "sk-duplicate", Disabled: true},
				{APIKey: "sk-duplicate"},
			},
			Models: []config.OpenAICompatibilityModel{{
				Name:  "kimi-k2",
				Alias: "kimi-k2",
			}},
		}},
	}
	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandler(cfg, configPath, manager)
	h.mu.Lock()
	h.syncRuntimeConfigLocked(context.Background())
	h.mu.Unlock()

	idGen := synthesizer.NewStableIDGenerator()
	firstID, _ := idGen.Next("openai-compatibility:kimi", "sk-duplicate", "https://api.kimi.example.com/v1", "")
	secondID, _ := idGen.Next("openai-compatibility:kimi", "sk-duplicate", "https://api.kimi.example.com/v1", "")

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/status", strings.NewReader(`{"name":"`+secondID+`","disabled":true}`))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	h.PatchAuthFileStatus(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("PatchAuthFileStatus status = %d, want %d with body %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !h.cfg.OpenAICompatibility[0].APIKeyEntries[0].Disabled {
		t.Fatal("first duplicate entry should remain disabled")
	}
	if !h.cfg.OpenAICompatibility[0].APIKeyEntries[1].Disabled {
		t.Fatal("target duplicate entry should be disabled by its stable auth ID")
	}
	firstAuth, ok := manager.GetByID(firstID)
	if !ok || firstAuth == nil || !firstAuth.Disabled {
		t.Fatalf("expected first auth %s to remain disabled", firstID)
	}
	secondAuth, ok := manager.GetByID(secondID)
	if !ok || secondAuth == nil || !secondAuth.Disabled {
		t.Fatalf("expected second auth %s to be disabled", secondID)
	}
}

func TestPatchAuthFileStatusConfigBackedEnablesDisabledOpenAICompatProvider(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("openai-compatibility:\n  - name: minimax\n    disabled: true\n    base-url: https://api.minimax.example.com/v1\n    api-key-entries:\n      - api-key: sk-provider-disabled\n    models:\n      - name: minimax-m2\n        alias: minimax-m2\n"), 0o600); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	cfg := &config.Config{
		AuthDir: tmpDir,
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:     "minimax",
			Disabled: true,
			BaseURL:  "https://api.minimax.example.com/v1",
			APIKeyEntries: []config.OpenAICompatibilityAPIKey{{
				APIKey: "sk-provider-disabled",
			}},
			Models: []config.OpenAICompatibilityModel{{
				Name:  "minimax-m2",
				Alias: "minimax-m2",
			}},
		}},
	}
	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandler(cfg, configPath, manager)
	h.mu.Lock()
	h.syncRuntimeConfigLocked(context.Background())
	h.mu.Unlock()

	authID, _ := synthesizer.NewStableIDGenerator().Next("openai-compatibility:minimax", "sk-provider-disabled", "https://api.minimax.example.com/v1", "")
	auth, ok := manager.GetByID(authID)
	if !ok || auth == nil || !auth.Disabled {
		t.Fatalf("expected disabled provider auth %s to be present as disabled", authID)
	}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/status", strings.NewReader(`{"name":"`+authID+`","disabled":false}`))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	h.PatchAuthFileStatus(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("PatchAuthFileStatus status = %d, want %d with body %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if h.cfg.OpenAICompatibility[0].Disabled {
		t.Fatal("expected enabling provider-disabled auth to clear provider disabled state")
	}
	if h.cfg.OpenAICompatibility[0].APIKeyEntries[0].Disabled {
		t.Fatal("expected target key to remain enabled")
	}
	activeAuth, ok := manager.GetByID(authID)
	if !ok || activeAuth == nil || activeAuth.Disabled || activeAuth.Status == coreauth.StatusDisabled {
		t.Fatalf("expected auth %s active after enable, got %#v", authID, activeAuth)
	}
}
