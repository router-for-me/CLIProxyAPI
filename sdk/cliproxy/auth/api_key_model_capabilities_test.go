package auth

import (
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestAttachResolvedAPIKeyModelInfoUsesSelectedCredential(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{ClaudeKey: []internalconfig.ClaudeKey{
		{
			APIKey: "key-high",
			Prefix: "tenant",
			Models: []internalconfig.ClaudeModel{{
				Name: "shared-upstream", Alias: "public-model",
				Thinking: &registry.ThinkingSupport{Levels: []string{"high"}},
			}},
		},
		{
			APIKey: "key-max",
			Prefix: "tenant",
			Models: []internalconfig.ClaudeModel{{
				Name: "shared-upstream", Alias: "public-model",
				Thinking: &registry.ThinkingSupport{Levels: []string{"max"}},
			}},
		},
	}})

	authHigh := configuredCapabilityTestAuth("auth-high", "key-high")
	authMax := configuredCapabilityTestAuth("auth-max", "key-max")
	registerCapabilityTestAuth(t, manager, authHigh)
	registerCapabilityTestAuth(t, manager, authMax)

	assertResolvedThinkingLevels(t, manager.attachResolvedAPIKeyModelInfo(cliproxyexecutor.Request{}, authHigh, "tenant/public-model", "shared-upstream"), "high")
	assertResolvedThinkingLevels(t, manager.attachResolvedAPIKeyModelInfo(cliproxyexecutor.Request{}, authMax, "tenant/public-model", "shared-upstream"), "max")
}

func TestAttachResolvedAPIKeyModelInfoUsesExactDuplicateCredentialConfig(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	highModels := []internalconfig.ClaudeModel{{
		Name: "shared-upstream", Alias: "public-model",
		Thinking: &registry.ThinkingSupport{Levels: []string{"high"}},
	}}
	maxModels := []internalconfig.ClaudeModel{{
		Name: "shared-upstream", Alias: "public-model",
		Thinking: &registry.ThinkingSupport{Levels: []string{"max"}},
	}}
	manager.SetConfig(&internalconfig.Config{ClaudeKey: []internalconfig.ClaudeKey{
		{APIKey: "shared-key", Prefix: "tenant", Models: highModels},
		{APIKey: "shared-key", Prefix: "tenant", Models: maxModels},
	}})

	authHigh := configuredCapabilityTestAuth("auth-duplicate-high", "shared-key")
	authHigh.Attributes[AttributeConfigIndex] = "0"
	authMax := configuredCapabilityTestAuth("auth-duplicate-max", "shared-key")
	authMax.Attributes[AttributeConfigIndex] = "1"
	registerCapabilityTestAuth(t, manager, authHigh)
	registerCapabilityTestAuth(t, manager, authMax)

	assertResolvedThinkingLevels(t, manager.attachResolvedAPIKeyModelInfo(cliproxyexecutor.Request{}, authHigh, "tenant/public-model", "shared-upstream"), "high")
	assertResolvedThinkingLevels(t, manager.attachResolvedAPIKeyModelInfo(cliproxyexecutor.Request{}, authMax, "tenant/public-model", "shared-upstream"), "max")
}

func TestAttachResolvedAPIKeyModelInfoPrefersExactConfiguredSuffix(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	auth := configuredCapabilityTestAuth("auth-suffix", "key-suffix")
	manager.SetConfig(&internalconfig.Config{ClaudeKey: []internalconfig.ClaudeKey{{
		APIKey: "key-suffix",
		Prefix: "tenant",
		Models: []internalconfig.ClaudeModel{
			{Name: "shared-upstream(high)", Alias: "public-high", Thinking: &registry.ThinkingSupport{Levels: []string{"high"}}},
			{Name: "shared-upstream(low)", Alias: "public-low", Thinking: &registry.ThinkingSupport{Levels: []string{"low"}}},
			{Name: "alias-upstream", Alias: "public(high)", Thinking: &registry.ThinkingSupport{Levels: []string{"high"}}},
			{Name: "alias-upstream", Alias: "public(low)", Thinking: &registry.ThinkingSupport{Levels: []string{"low"}}},
		},
	}}})
	registerCapabilityTestAuth(t, manager, auth)

	req := manager.attachResolvedAPIKeyModelInfo(cliproxyexecutor.Request{}, auth, "tenant/public-low", "shared-upstream(low)")
	assertResolvedThinkingLevels(t, req, "low")

	models, _, _, routing := manager.executionModelCandidatesWithAlias(auth, "tenant/shared-upstream(low)")
	if len(models) != 1 || models[0] != "shared-upstream(low)" {
		t.Fatalf("direct suffixed models = %v, want [shared-upstream(low)]", models)
	}
	directReq := attachResolvedAPIKeyModelInfo(routing, cliproxyexecutor.Request{}, auth, "tenant/shared-upstream(low)", models[0])
	assertResolvedThinkingLevels(t, directReq, "low")

	aliasModels, _, _, aliasRouting := manager.executionModelCandidatesWithAlias(auth, "tenant/public(low)")
	if len(aliasModels) != 1 || aliasModels[0] != "alias-upstream(low)" {
		t.Fatalf("suffixed alias models = %v, want [alias-upstream(low)]", aliasModels)
	}
	aliasReq := attachResolvedAPIKeyModelInfo(aliasRouting, cliproxyexecutor.Request{}, auth, "tenant/public(low)", aliasModels[0])
	assertResolvedThinkingLevels(t, aliasReq, "low")
}

func TestAPIKeyModelRoutingClonesPublishedConfig(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	cfg := &internalconfig.Config{ClaudeKey: []internalconfig.ClaudeKey{{
		APIKey: "key-clone",
		Prefix: "tenant",
		Models: []internalconfig.ClaudeModel{{
			Name: "shared-upstream", Alias: "public",
			Thinking: &registry.ThinkingSupport{Levels: []string{"high"}},
		}},
	}}}
	manager.SetConfig(cfg)
	cfg.ClaudeKey[0].Models[0].Alias = "mutated"
	cfg.ClaudeKey[0].Models[0].Thinking.Levels[0] = "max"

	auth := configuredCapabilityTestAuth("auth-clone", "key-clone")
	registerCapabilityTestAuth(t, manager, auth)
	models, _, _, routing := manager.executionModelCandidatesWithAlias(auth, "tenant/public")
	if len(models) != 1 || models[0] != "shared-upstream" {
		t.Fatalf("cloned execution models = %v, want [shared-upstream]", models)
	}
	req := attachResolvedAPIKeyModelInfo(routing, cliproxyexecutor.Request{}, auth, "tenant/public", models[0])
	assertResolvedThinkingLevels(t, req, "high")
}

func TestAPIKeyModelRoutingKeepsOneExecutionSnapshotAcrossReload(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	auth := configuredCapabilityTestAuth("auth-reload", "key-reload")
	buildConfig := func(level string) *internalconfig.Config {
		return &internalconfig.Config{ClaudeKey: []internalconfig.ClaudeKey{{
			APIKey: "key-reload",
			Prefix: "tenant",
			Models: []internalconfig.ClaudeModel{{
				Name: "shared-upstream", Alias: "public",
				Thinking: &registry.ThinkingSupport{Levels: []string{level}},
			}},
		}}}
	}
	manager.SetConfig(buildConfig("high"))
	registerCapabilityTestAuth(t, manager, auth)
	models, _, _, oldRouting := manager.executionModelCandidatesWithAlias(auth, "tenant/public")
	if len(models) != 1 || models[0] != "shared-upstream" {
		t.Fatalf("execution models = %v, want [shared-upstream]", models)
	}

	manager.SetConfig(buildConfig("max"))
	oldReq := attachResolvedAPIKeyModelInfo(oldRouting, cliproxyexecutor.Request{}, auth, "tenant/public", models[0])
	assertResolvedThinkingLevels(t, oldReq, "high")
	newReq := manager.attachResolvedAPIKeyModelInfo(cliproxyexecutor.Request{}, auth, "tenant/public", models[0])
	assertResolvedThinkingLevels(t, newReq, "max")
}

func TestAttachResolvedAPIKeyModelInfoSupportsKeylessOpenAICompatibility(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{OpenAICompatibility: []internalconfig.OpenAICompatibility{{
		Name:    "keyless",
		Prefix:  "tenant",
		BaseURL: "https://example.com/v1",
		Models: []internalconfig.OpenAICompatibilityModel{
			{
				Name: "shared-upstream", Alias: "public-model", ForceMapping: true, IsCompat: true,
				Thinking: &registry.ThinkingSupport{Levels: []string{"high"}},
			},
			{
				Name: "fallback-upstream", Alias: "public-model",
				Thinking: &registry.ThinkingSupport{Levels: []string{"high"}},
			},
		},
	}}})
	auth := &Auth{
		ID:       "auth-keyless",
		Provider: "openai-compatibility:keyless",
		Prefix:   "tenant",
		Attributes: map[string]string{
			AttributeSource: "config:keyless[0]",
			"compat_name":   "keyless",
			"provider_key":  "openai-compatibility:keyless",
		},
	}
	registerCapabilityTestAuth(t, manager, auth)
	models, _, aliasResult, routing := manager.executionModelCandidatesWithAlias(auth, "tenant/public-model")
	if len(models) != 2 || models[0] != "shared-upstream" || models[1] != "fallback-upstream" {
		t.Fatalf("keyless execution models = %v, want [shared-upstream fallback-upstream]", models)
	}
	if !aliasResult.ForceMapping || aliasResult.UpstreamModel != "shared-upstream" {
		t.Fatalf("keyless force mapping result = %+v, want shared-upstream force mapping", aliasResult)
	}
	fallbackAliasResult := resolveAttemptAliasResult(routing, auth, "tenant/public-model", "fallback-upstream", aliasResult)
	if fallbackAliasResult.ForceMapping {
		t.Fatalf("fallback alias result = %+v, want force mapping disabled", fallbackAliasResult)
	}
	req := attachResolvedAPIKeyModelInfo(routing, cliproxyexecutor.Request{}, auth, "tenant/public-model", models[0])
	assertResolvedThinkingLevels(t, req, "high")
	info, ok := ResolvedAPIKeyModelInfo(req)
	if !ok || info == nil || !info.IsCompat {
		t.Fatal("OpenAI compatibility model IsCompat = false, want true")
	}
}

func TestAttachResolvedAPIKeyModelInfoBindsUnknownConfiguredCapability(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	auth := configuredCapabilityTestAuth("auth-fallback", "key-fallback")
	manager.SetConfig(&internalconfig.Config{ClaudeKey: []internalconfig.ClaudeKey{{
		APIKey: "key-fallback",
		Prefix: "tenant",
		Models: []internalconfig.ClaudeModel{{Name: "unknown-upstream", Alias: "unknown-public"}},
	}}})
	registerCapabilityTestAuth(t, manager, auth)

	req := manager.attachResolvedAPIKeyModelInfo(cliproxyexecutor.Request{}, auth, "tenant/unknown-public", "unknown-upstream")
	info, ok := ResolvedAPIKeyModelInfo(req)
	if !ok || info == nil || info.UserDefined || info.Thinking != nil {
		t.Fatalf("ResolvedAPIKeyModelInfo() = (%+v, %t), want authoritative empty capability", info, ok)
	}
	fallbackReq := manager.attachResolvedAPIKeyModelInfo(cliproxyexecutor.Request{}, auth, "tenant/not-configured", "not-configured")
	if fallbackInfo, fallbackOK := ResolvedAPIKeyModelInfo(fallbackReq); fallbackOK || fallbackInfo != nil {
		t.Fatalf("unconfigured model info = (%+v, %t), want registry fallback", fallbackInfo, fallbackOK)
	}
}

func registerCapabilityTestAuth(t *testing.T, manager *Manager, auth *Auth) {
	t.Helper()
	registered, errRegister := manager.Register(t.Context(), auth)
	if errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	if registered == nil {
		t.Fatal("Register() returned nil auth")
	}
}

func configuredCapabilityTestAuth(id, apiKey string) *Auth {
	return &Auth{
		ID:       id,
		Provider: "claude",
		Prefix:   "tenant",
		Attributes: map[string]string{
			AttributeAuthKind: AuthKindAPIKey,
			AttributeAPIKey:   apiKey,
			AttributeSource:   "config:claude[0]",
		},
	}
}

func assertResolvedThinkingLevels(t *testing.T, req cliproxyexecutor.Request, want ...string) {
	t.Helper()
	info, ok := ResolvedAPIKeyModelInfo(req)
	if !ok || info == nil || info.Thinking == nil {
		t.Fatalf("ResolvedAPIKeyModelInfo() = (%+v, %t), want thinking levels %v", info, ok, want)
	}
	if len(info.Thinking.Levels) != len(want) {
		t.Fatalf("thinking levels = %v, want %v", info.Thinking.Levels, want)
	}
	for i := range want {
		if info.Thinking.Levels[i] != want[i] {
			t.Fatalf("thinking levels = %v, want %v", info.Thinking.Levels, want)
		}
	}
}

func TestCodexAPIKeyModelIsCompat(t *testing.T) {
	cfg := &internalconfig.Config{CodexKey: []internalconfig.CodexKey{{
		APIKey:  "codex-key",
		BaseURL: "https://compat.example.com/v1",
		Models: []internalconfig.CodexModel{
			{Name: "deepseek-v4-flash", Alias: "deepseek-alias", IsCompat: true},
			{Name: "gpt-5.4", Alias: "codex-native"},
		},
	}}}
	auth := &Auth{
		Provider: "codex",
		Attributes: map[string]string{
			AttributeAuthKind: AuthKindAPIKey,
			AttributeAPIKey:   "codex-key",
			"base_url":        "https://compat.example.com/v1",
		},
	}

	if !CodexAPIKeyModelIsCompat(cfg, auth, "deepseek-v4-flash") {
		t.Fatal("upstream name IsCompat = false, want true")
	}
	if !CodexAPIKeyModelIsCompat(cfg, auth, "deepseek-alias") {
		t.Fatal("alias IsCompat = false, want true")
	}
	if !CodexAPIKeyModelIsCompat(cfg, auth, "deepseek-v4-flash(high)") {
		t.Fatal("suffix model IsCompat = false, want true")
	}
	if CodexAPIKeyModelIsCompat(cfg, auth, "gpt-5.4") {
		t.Fatal("native model IsCompat = true, want false")
	}
	if CodexAPIKeyModelIsCompat(cfg, auth, "missing-model") {
		t.Fatal("missing model IsCompat = true, want false")
	}
	if CodexAPIKeyModelIsCompat(cfg, &Auth{Provider: "claude", Attributes: auth.Attributes}, "deepseek-v4-flash") {
		t.Fatal("non-codex provider IsCompat = true, want false")
	}
	if CodexAPIKeyModelIsCompat(nil, auth, "deepseek-v4-flash") {
		t.Fatal("nil config IsCompat = true, want false")
	}
}
