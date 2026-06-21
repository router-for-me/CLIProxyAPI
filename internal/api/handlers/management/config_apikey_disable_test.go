package management

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestSetConfigAPIKeyExcludedAll(t *testing.T) {
	gotDisable := setConfigAPIKeyExcludedAll([]string{"gpt-5"}, true)
	if len(gotDisable) != 2 || gotDisable[0] != "gpt-5" || gotDisable[1] != "*" {
		t.Fatalf("unexpected disable list: %#v", gotDisable)
	}
	gotEnable := setConfigAPIKeyExcludedAll([]string{"gpt-5", "*"}, false)
	if len(gotEnable) != 1 || gotEnable[0] != "gpt-5" {
		t.Fatalf("unexpected enable list: %#v", gotEnable)
	}
}

func TestToggleConfigAPIKeyExcludedAll_Codex(t *testing.T) {
	cfg := &config.Config{
		CodexKey: []config.CodexKey{{
			APIKey:  "sk-test",
			BaseURL: "https://example.com/v1",
		}},
	}
	idGen := synthesizer.NewStableIDGenerator()
	authID, _ := idGen.Next("codex:apikey", "sk-test", "https://example.com/v1")
	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":  "sk-test",
			"base_url": "https://example.com/v1",
			"source":   "config:codex[abc]",
		},
	}

	handled, err := toggleConfigAPIKeyExcludedAll(cfg, auth, true)
	if err != nil || !handled {
		t.Fatalf("toggle disable: handled=%v err=%v", handled, err)
	}
	if len(cfg.CodexKey[0].ExcludedModels) != 1 || cfg.CodexKey[0].ExcludedModels[0] != "*" {
		t.Fatalf("expected excluded-models [*], got %#v", cfg.CodexKey[0].ExcludedModels)
	}

	handled, err = toggleConfigAPIKeyExcludedAll(cfg, auth, false)
	if err != nil || !handled {
		t.Fatalf("toggle enable: handled=%v err=%v", handled, err)
	}
	if len(cfg.CodexKey[0].ExcludedModels) != 0 {
		t.Fatalf("expected excluded-models cleared, got %#v", cfg.CodexKey[0].ExcludedModels)
	}
}

func TestToggleConfigAPIKeyExcludedAll_OpenAICompatCommandAuth(t *testing.T) {
	cfg := &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:    "proxy",
			BaseURL: "https://proxy.example.com/v1",
			Auth:    &config.CommandAuthConfig{Command: "fetch-token"},
		}},
	}
	compat := &cfg.OpenAICompatibility[0]
	idGen := synthesizer.NewStableIDGenerator()
	idParts := append(synthesizer.CommandAuthIDParts(compat.Auth), strings.TrimSpace(compat.BaseURL), strings.TrimSpace(compat.ProxyURL))
	authID, _ := idGen.Next("openai-compatibility:proxy", idParts...)
	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"source":                    "config:proxy[abc]",
			coreauth.AttrAuthKind:       coreauth.AttrAuthKindAPIKey,
			coreauth.AttrAuthSource:     coreauth.AttrAuthSourceCommand,
			coreauth.AttrAuthCommand:    "fetch-token",
			coreauth.AttrAuthCommandKey: config.CommandAuthIdentity(compat.Auth),
		},
	}

	handled, err := toggleConfigAPIKeyExcludedAll(cfg, auth, true)
	if err != nil || !handled {
		t.Fatalf("toggle disable: handled=%v err=%v", handled, err)
	}
	if !cfg.OpenAICompatibility[0].Disabled {
		t.Fatalf("expected provider disabled, got %#v", cfg.OpenAICompatibility[0])
	}

	handled, err = toggleConfigAPIKeyExcludedAll(cfg, auth, false)
	if err != nil || !handled {
		t.Fatalf("toggle enable: handled=%v err=%v", handled, err)
	}
	if cfg.OpenAICompatibility[0].Disabled {
		t.Fatalf("expected provider re-enabled, got %#v", cfg.OpenAICompatibility[0])
	}
}
