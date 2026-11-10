package management

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestNormalizeClaudeCodeBaseURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "adds v1", in: "http://localhost:8317", want: "http://localhost:8317/v1"},
		{name: "keeps v1", in: "http://localhost:8317/v1", want: "http://localhost:8317/v1"},
		{name: "trims slash", in: "http://localhost:8317/", want: "http://localhost:8317/v1"},
		{name: "empty", in: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeClaudeCodeBaseURL(tt.in); got != tt.want {
				t.Fatalf("normalizeClaudeCodeBaseURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestWriteClaudeCodeSettingsMergesEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude", "settings.json")
	if errMkdir := os.MkdirAll(filepath.Dir(path), 0o700); errMkdir != nil {
		t.Fatalf("mkdir: %v", errMkdir)
	}
	initial := map[string]any{"env": map[string]any{"OTHER": "keep", "CLAUDE_CODE_MAX_CONTEXT_TOKENS": "200000"}}
	data, _ := json.Marshal(initial)
	if errWrite := os.WriteFile(path, data, 0o600); errWrite != nil {
		t.Fatalf("write initial: %v", errWrite)
	}

	err := writeClaudeCodeSettings(path, map[string]string{
		"ANTHROPIC_BASE_URL":             "http://localhost:8317",
		"ANTHROPIC_AUTH_TOKEN":           "sk-test",
		"CLAUDE_CODE_MAX_CONTEXT_TOKENS": "",
	})
	if err != nil {
		t.Fatalf("writeClaudeCodeSettings() error = %v", err)
	}
	settings, errRead := readClaudeCodeJSONMap(path)
	if errRead != nil {
		t.Fatalf("read settings: %v", errRead)
	}
	env := claudeCodeEnvMap(settings)
	if got := env["ANTHROPIC_BASE_URL"]; got != "http://localhost:8317/v1" {
		t.Fatalf("ANTHROPIC_BASE_URL = %q", got)
	}
	if got := env["ANTHROPIC_AUTH_TOKEN"]; got != "sk-test" {
		t.Fatalf("ANTHROPIC_AUTH_TOKEN = %q", got)
	}
	if got := env["OTHER"]; got != "keep" {
		t.Fatalf("OTHER = %q", got)
	}
	if _, ok := env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"]; ok {
		t.Fatalf("CLAUDE_CODE_MAX_CONTEXT_TOKENS should be removed")
	}
	if got, _ := settings["hasCompletedOnboarding"].(bool); !got {
		t.Fatalf("hasCompletedOnboarding should be true")
	}
}

func TestResetClaudeCodeSettingsRemovesManagedEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude", "settings.json")
	if errMkdir := os.MkdirAll(filepath.Dir(path), 0o700); errMkdir != nil {
		t.Fatalf("mkdir: %v", errMkdir)
	}
	initial := map[string]any{"env": map[string]any{"ANTHROPIC_BASE_URL": "http://localhost:8317/v1", "OTHER": "keep"}}
	data, _ := json.Marshal(initial)
	if errWrite := os.WriteFile(path, data, 0o600); errWrite != nil {
		t.Fatalf("write initial: %v", errWrite)
	}
	if errReset := resetClaudeCodeSettings(path); errReset != nil {
		t.Fatalf("resetClaudeCodeSettings() error = %v", errReset)
	}
	settings, errRead := readClaudeCodeJSONMap(path)
	if errRead != nil {
		t.Fatalf("read settings: %v", errRead)
	}
	env := claudeCodeEnvMap(settings)
	if _, ok := env["ANTHROPIC_BASE_URL"]; ok {
		t.Fatalf("ANTHROPIC_BASE_URL should be removed")
	}
	if got := env["OTHER"]; got != "keep" {
		t.Fatalf("OTHER = %q", got)
	}
}

func TestWriteClaudeCodeExaMCP(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	if errWrite := writeClaudeCodeExaMCP(path, true); errWrite != nil {
		t.Fatalf("enable Exa MCP: %v", errWrite)
	}
	data, errRead := readClaudeCodeJSONMap(path)
	if errRead != nil {
		t.Fatalf("read claude json: %v", errRead)
	}
	if !claudeCodeExaMCPEnabled(data) {
		t.Fatalf("Exa MCP should be enabled")
	}
	if errWrite := writeClaudeCodeExaMCP(path, false); errWrite != nil {
		t.Fatalf("disable Exa MCP: %v", errWrite)
	}
	data, errRead = readClaudeCodeJSONMap(path)
	if errRead != nil {
		t.Fatalf("read claude json: %v", errRead)
	}
	if claudeCodeExaMCPEnabled(data) {
		t.Fatalf("Exa MCP should be disabled")
	}
}

func TestClaudeCodeModelOptionsReturnsAllConfiguredModels(t *testing.T) {
	h := &Handler{cfg: &config.Config{ClaudeKey: []config.ClaudeKey{
		{Models: []config.ClaudeModel{
			{Name: "upstream-opus", Alias: "team/claude-opus-best"},
			{Name: "gpt-5", Alias: "team/gpt-latest"},
			{Name: "kimi-k2.5"},
		}},
	}}}
	models, defaults := h.claudeCodeModelOptions()
	if len(models) != 3 {
		t.Fatalf("model count = %d, want 3", len(models))
	}
	for _, key := range []string{"opus", "sonnet", "haiku"} {
		if got := defaults[key]; got != "" {
			t.Fatalf("%s default = %q, want empty", key, got)
		}
	}
	seen := map[string]bool{}
	for _, model := range models {
		seen[model.ID] = true
		if model.Family != "" {
			t.Fatalf("model %q family = %q, want empty", model.ID, model.Family)
		}
	}
	for _, want := range []string{"team/claude-opus-best", "team/gpt-latest", "kimi-k2.5"} {
		if !seen[want] {
			t.Fatalf("missing model %q in %#v", want, models)
		}
	}
}

func TestPutConfigYAMLTriggersConfigReloadHook(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if errWrite := os.WriteFile(cfgPath, []byte("api-keys:\n  - sk-old\n"), 0o600); errWrite != nil {
		t.Fatalf("write initial config: %v", errWrite)
	}
	cfg := &config.Config{}
	cfg.APIKeys = []string{"sk-old"}
	h := NewHandler(cfg, cfgPath, nil)
	reloadCh := make(chan *config.Config, 1)
	h.SetConfigReloadHook(func(_ context.Context, cfg *config.Config) {
		reloadCh <- cfg
	})

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/config.yaml", bytes.NewBufferString("api-keys:\n  - sk-new\n"))

	h.PutConfigYAML(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("PutConfigYAML status = %d, body = %s", rec.Code, rec.Body.String())
	}

	select {
	case reloaded := <-reloadCh:
		if len(reloaded.APIKeys) != 1 || reloaded.APIKeys[0] != "sk-new" {
			t.Fatalf("reloaded APIKeys = %#v, want [\"sk-new\"]", reloaded.APIKeys)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for SetConfigReloadHook after PutConfigYAML")
	}
}

func TestPostClaudeCodeSettingsSyncsAuthTokenToAPIKeys(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tmpDir := t.TempDir()
	origHomeDir := claudeCodeUserHomeDir
	claudeCodeUserHomeDir = func() (string, error) { return tmpDir, nil }
	defer func() { claudeCodeUserHomeDir = origHomeDir }()

	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if errWrite := os.WriteFile(cfgPath, []byte("api-keys:\n  - sk-existing\n"), 0o600); errWrite != nil {
		t.Fatalf("write initial config: %v", errWrite)
	}
	cfg := &config.Config{}
	cfg.APIKeys = []string{"sk-existing"}
	h := NewHandler(cfg, cfgPath, nil)
	reloadCh := make(chan *config.Config, 1)
	h.SetConfigReloadHook(func(_ context.Context, cfg *config.Config) {
		reloadCh <- cfg
	})

	payloadBytes, _ := json.Marshal(claudeCodeSettingsPayload{
		Env: map[string]string{
			"ANTHROPIC_BASE_URL":   "https://cli-proxy-api.daido.dev",
			"ANTHROPIC_AUTH_TOKEN": "sk-claude-setup-new",
		},
	})
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/claude-code-settings", bytes.NewReader(payloadBytes))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.PostClaudeCodeSettings(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("PostClaudeCodeSettings status = %d, body = %s", rec.Code, rec.Body.String())
	}

	select {
	case reloaded := <-reloadCh:
		if len(reloaded.APIKeys) != 2 || reloaded.APIKeys[1] != "sk-claude-setup-new" {
			t.Fatalf("reloaded APIKeys = %#v, want [\"sk-existing\", \"sk-claude-setup-new\"]", reloaded.APIKeys)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for SetConfigReloadHook after PostClaudeCodeSettings")
	}
}
