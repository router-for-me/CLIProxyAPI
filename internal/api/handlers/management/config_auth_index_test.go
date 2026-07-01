package management

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestCommandAuthConfigListsExposeAuthMetadataWithoutPseudoAPIKey(t *testing.T) {
	authCfg := &config.CommandAuthConfig{Command: "fetch-token", Args: []string{"--audience", "gemini"}}
	h := &Handler{cfg: &config.Config{GeminiKey: []config.GeminiKey{{
		BaseURL: "https://gemini.example.com",
		Auth:    authCfg,
	}}}}

	got := h.geminiKeysWithAuthIndex()
	if len(got) != 1 {
		t.Fatalf("gemini keys len = %d, want 1", len(got))
	}
	wantAuthKey := commandAuthConfigManagementKey(authCfg)
	if wantAuthKey == "" {
		t.Fatal("auth key is empty")
	}
	if got[0].APIKey != "" {
		t.Fatalf("response api-key = %q, want empty", got[0].APIKey)
	}
	if got[0].AuthKey != wantAuthKey {
		t.Fatalf("response auth-key = %q, want %q", got[0].AuthKey, wantAuthKey)
	}
	if got[0].AuthSource != coreauth.AttrAuthSourceCommand {
		t.Fatalf("response auth-source = %q, want command", got[0].AuthSource)
	}
	if h.cfg.GeminiKey[0].APIKey != "" {
		t.Fatalf("config api-key mutated to %q, want empty", h.cfg.GeminiKey[0].APIKey)
	}
}

func TestOpenAICompatCommandAuthConfigListExposesAuthMetadata(t *testing.T) {
	authCfg := &config.CommandAuthConfig{Command: "fetch-openai-token"}
	h := &Handler{cfg: &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
		Name:     "proxy",
		BaseURL:  "https://proxy.example.com/v1",
		ProxyURL: "http://proxy.local",
		Auth:     authCfg,
	}}}}

	got := h.openAICompatibilityWithAuthIndex()
	if len(got) != 1 {
		t.Fatalf("openai compatibility len = %d, want 1", len(got))
	}
	if len(got[0].APIKeyEntries) != 0 {
		t.Fatalf("api-key entries len = %d, want 0", len(got[0].APIKeyEntries))
	}
	wantAuthKey := commandAuthConfigManagementKey(authCfg)
	if got[0].AuthKey != wantAuthKey {
		t.Fatalf("response auth-key = %q, want %q", got[0].AuthKey, wantAuthKey)
	}
	if got[0].AuthSource != coreauth.AttrAuthSourceCommand {
		t.Fatalf("response auth-source = %q, want command", got[0].AuthSource)
	}
	if len(h.cfg.OpenAICompatibility[0].APIKeyEntries) != 0 {
		t.Fatalf("config api-key entries mutated to %#v, want empty", h.cfg.OpenAICompatibility[0].APIKeyEntries)
	}
}

func TestManagementConfigSnapshotExposesCommandAuthMetadata(t *testing.T) {
	codexAuth := &config.CommandAuthConfig{Command: "fetch-codex-token"}
	claudeAuth := &config.CommandAuthConfig{Command: "fetch-claude-token"}
	openAIAuth := &config.CommandAuthConfig{Command: "fetch-openai-token"}
	h := &Handler{cfg: &config.Config{
		CodexKey: []config.CodexKey{{
			BaseURL: "https://codex.example.com/v1",
			Auth:    codexAuth,
		}},
		ClaudeKey: []config.ClaudeKey{{
			BaseURL: "https://claude.example.com",
			Auth:    claudeAuth,
		}},
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:    "proxy",
			BaseURL: "https://proxy.example.com/v1",
			Auth:    openAIAuth,
		}},
	}}

	got := h.managementConfigSnapshot()
	if got.CodexKey[0].APIKey != "" {
		t.Fatalf("codex snapshot api-key = %q, want empty", got.CodexKey[0].APIKey)
	}
	if got.CodexKey[0].AuthKey != commandAuthConfigManagementKey(codexAuth) {
		t.Fatalf("codex snapshot auth-key = %q", got.CodexKey[0].AuthKey)
	}
	if got.ClaudeKey[0].APIKey != "" {
		t.Fatalf("claude snapshot api-key = %q, want empty", got.ClaudeKey[0].APIKey)
	}
	if got.ClaudeKey[0].AuthKey != commandAuthConfigManagementKey(claudeAuth) {
		t.Fatalf("claude snapshot auth-key = %q", got.ClaudeKey[0].AuthKey)
	}
	if len(got.OpenAICompatibility[0].APIKeyEntries) != 0 {
		t.Fatalf("openai snapshot api-key entries len = %d, want 0", len(got.OpenAICompatibility[0].APIKeyEntries))
	}
	if got.OpenAICompatibility[0].AuthKey != commandAuthConfigManagementKey(openAIAuth) {
		t.Fatalf("openai snapshot auth-key = %q", got.OpenAICompatibility[0].AuthKey)
	}

	if h.cfg.CodexKey[0].APIKey != "" || h.cfg.ClaudeKey[0].APIKey != "" || len(h.cfg.OpenAICompatibility[0].APIKeyEntries) != 0 {
		t.Fatalf("source config mutated: %#v", h.cfg)
	}
}

func TestManagementConfigSnapshotDeepCopiesConfigData(t *testing.T) {
	h := &Handler{cfg: &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"codex": {{Name: "source", Alias: "alias"}},
		},
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:    "proxy",
			BaseURL: "https://proxy.example.com/v1",
			Headers: map[string]string{
				"X-Test": "source",
			},
			Models: []config.OpenAICompatibilityModel{{
				Name:  "gpt-5",
				Alias: "gpt",
			}},
		}},
	}}

	got := h.managementConfigSnapshot()
	got.OAuthModelAlias["codex"][0].Alias = "snapshot"
	got.OpenAICompatibility[0].Headers["X-Test"] = "snapshot"
	got.OpenAICompatibility[0].Models[0].Alias = "snapshot"

	if h.cfg.OAuthModelAlias["codex"][0].Alias != "alias" {
		t.Fatalf("source OAuthModelAlias mutated to %q", h.cfg.OAuthModelAlias["codex"][0].Alias)
	}
	if h.cfg.OpenAICompatibility[0].Headers["X-Test"] != "source" {
		t.Fatalf("source header mutated to %q", h.cfg.OpenAICompatibility[0].Headers["X-Test"])
	}
	if h.cfg.OpenAICompatibility[0].Models[0].Alias != "gpt" {
		t.Fatalf("source model alias mutated to %q", h.cfg.OpenAICompatibility[0].Models[0].Alias)
	}
}

func TestNormalizeCommandAuthPseudoAPIKeyDropsDisplayOnlyValue(t *testing.T) {
	authCfg := &config.CommandAuthConfig{Command: "fetch-token"}
	pseudoKey := commandAuthConfigManagementKey(authCfg)

	codexEntry := config.CodexKey{APIKey: pseudoKey, Auth: authCfg, BaseURL: "https://codex.example.com"}
	normalizeCodexKey(&codexEntry)
	if codexEntry.APIKey != "" {
		t.Fatalf("codex api-key = %q, want empty", codexEntry.APIKey)
	}

	compatEntry := config.OpenAICompatibility{
		Name:    "proxy",
		BaseURL: "https://proxy.example.com",
		Auth:    authCfg,
		APIKeyEntries: []config.OpenAICompatibilityAPIKey{{
			APIKey: pseudoKey,
		}},
	}
	normalizeOpenAICompatibilityEntry(&compatEntry)
	if got := compatEntry.APIKeyEntries[0].APIKey; got != "" {
		t.Fatalf("openai compat api-key = %q, want empty", got)
	}
}
