package management

import (
	"context"
	"strings"
	"testing"
	"time"

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

func TestToggleConfigAPIKeyExcludedAll_OpenAICompatCommandAuthSynthesizesDisabledAuth(t *testing.T) {
	cfg := &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:     "proxy",
			BaseURL:  "https://proxy.example.com/v1",
			ProxyURL: "http://proxy.local",
			Disabled: true,
			Auth:     &config.CommandAuthConfig{Command: "fetch-token"},
		}},
	}

	auths, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{
		Config:      cfg,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if err != nil {
		t.Fatalf("synthesize disabled command auth: %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("auths len = %d, want 1", len(auths))
	}
	auth := auths[0]
	if !auth.Disabled || auth.Status != coreauth.StatusDisabled {
		t.Fatalf("auth disabled/status = %v/%s, want true/%s", auth.Disabled, auth.Status, coreauth.StatusDisabled)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register disabled command auth: %v", err)
	}
	loaded, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("disabled synthesized auth %q not found in manager", auth.ID)
	}

	handled, err := toggleConfigAPIKeyExcludedAll(cfg, loaded, false)
	if err != nil || !handled {
		t.Fatalf("toggle enable after reload: handled=%v err=%v", handled, err)
	}
	if cfg.OpenAICompatibility[0].Disabled {
		t.Fatalf("expected provider re-enabled from disabled synthesized auth, got %#v", cfg.OpenAICompatibility[0])
	}
}
