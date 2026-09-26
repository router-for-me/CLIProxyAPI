package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
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

func TestSelectedCodexConfigurationUpdateCapability(t *testing.T) {
	const upstream = "gpt-6-luna"
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{CodexKey: []internalconfig.CodexKey{
		{APIKey: "key-disabled", Prefix: "tenant", Models: []internalconfig.CodexModel{
			{Name: upstream, Alias: "public", SupportConfigurationUpdate: false},
			{Name: "gpt-6-astra", Alias: "other", SupportConfigurationUpdate: true},
		}},
		{APIKey: "key-enabled", Prefix: "tenant", Models: []internalconfig.CodexModel{
			{Name: upstream, Alias: "public", SupportConfigurationUpdate: true},
			{Name: "gpt-6-astra", Alias: "other", SupportConfigurationUpdate: false},
		}},
		{APIKey: "key-unlisted", Prefix: "tenant"},
		{APIKey: "key-base-only", Prefix: "tenant", BaseURL: "https://expected.example/v1"},
	}, ClaudeKey: []internalconfig.ClaudeKey{{APIKey: "claude-unlisted", Prefix: "tenant"}}})

	keyAuth := func(id, key string) *Auth {
		auth := configuredCapabilityTestAuth(id, key)
		auth.Provider = "codex"
		return auth
	}
	disabled := keyAuth("codex-disabled", "key-disabled")
	enabled := keyAuth("codex-enabled", "key-enabled")
	unlisted := keyAuth("codex-unlisted", "key-unlisted")
	for _, auth := range []*Auth{disabled, enabled, unlisted} {
		registerCapabilityTestAuth(t, manager, auth)
	}
	oauth := &Auth{
		ID: "codex-oauth", Provider: "codex", Prefix: "tenant",
		Attributes: map[string]string{"plan_type": "free"},
		Metadata:   map[string]any{"access_token": "fake-token"},
	}
	registerCapabilityTestAuth(t, manager, oauth)

	assertSelected := func(t *testing.T, auth *Auth, routeModel, upstreamModel string, want bool) cliproxyexecutor.Request {
		t.Helper()
		req := manager.attachResolvedAPIKeyModelInfo(cliproxyexecutor.Request{}, auth, routeModel, upstreamModel)
		info, ok := ResolvedModelInfo(req)
		if !ok || info == nil || info.SupportConfigurationUpdate != want {
			t.Fatalf("selected %s %q -> %q capability = (%+v, %t), want %t", auth.ID, routeModel, upstreamModel, info, ok, want)
		}
		return req
	}

	t.Run("same upstream and public alias remain isolated across OAuth and keys", func(t *testing.T) {
		assertSelected(t, oauth, "tenant/public", upstream, true)
		assertSelected(t, enabled, "tenant/public", upstream, true)
		assertSelected(t, disabled, "tenant/public", upstream, false)
		assertSelected(t, oauth, "tenant/"+upstream, upstream, true)
		assertSelected(t, disabled, "tenant/"+upstream, upstream, false)
		assertSelected(t, enabled, "tenant/public(high)", upstream+"(high)", true)
		assertSelected(t, disabled, "tenant/public(high)", upstream+"(high)", false)
	})

	t.Run("two models on one key can have opposite capabilities", func(t *testing.T) {
		assertSelected(t, disabled, "tenant/other", "gpt-6-astra", true)
		assertSelected(t, disabled, "tenant/public", upstream, false)
		assertSelected(t, enabled, "tenant/other", "gpt-6-astra", false)
		assertSelected(t, enabled, "tenant/public", upstream, true)
	})

	t.Run("unlisted models default to false without losing thinking", func(t *testing.T) {
		req := assertSelected(t, unlisted, "tenant/"+upstream, upstream, false)
		info, _ := ResolvedModelInfo(req)
		builtin := registry.LookupStaticModelInfo(upstream)
		if builtin == nil || builtin.Thinking == nil || info.Thinking == nil || len(info.Thinking.Levels) != len(builtin.Thinking.Levels) {
			t.Fatalf("unlisted model thinking = %+v, want builtin %+v", info, builtin)
		}
		for i, level := range builtin.Thinking.Levels {
			if info.Thinking.Levels[i] != level {
				t.Fatalf("unlisted model thinking levels = %v, want %v", info.Thinking.Levels, builtin.Thinking.Levels)
			}
		}
		assertSelected(t, unlisted, "tenant/"+upstream+"(high)", upstream+"(high)", false)
		unknown := assertSelected(t, unlisted, "tenant/unknown", "unknown", false)
		if info, _ := ResolvedModelInfo(unknown); info.Thinking != nil {
			t.Fatalf("unknown unlisted model thinking = %+v, want nil", info.Thinking)
		}
		mismatched := keyAuth("codex-mismatched", "not-configured")
		registerCapabilityTestAuth(t, manager, mismatched)
		req = manager.attachResolvedAPIKeyModelInfo(cliproxyexecutor.Request{}, mismatched, "tenant/"+upstream, upstream)
		if info, ok := ResolvedModelInfo(req); ok || info != nil {
			t.Fatalf("unmatched key model info = (%+v, %t), want registry fallback", info, ok)
		}
		matchingBase := keyAuth("codex-matching-base", "key-base-only")
		matchingBase.Attributes[AttributeConfigIndex] = "3"
		matchingBase.Attributes["base_url"] = "https://expected.example/v1"
		registerCapabilityTestAuth(t, manager, matchingBase)
		assertSelected(t, matchingBase, "tenant/"+upstream, upstream, false)
		wrongBase := keyAuth("codex-wrong-base", "key-base-only")
		wrongBase.Attributes[AttributeConfigIndex] = "3"
		wrongBase.Attributes["base_url"] = "https://wrong.example/v1"
		registerCapabilityTestAuth(t, manager, wrongBase)
		req = manager.attachResolvedAPIKeyModelInfo(cliproxyexecutor.Request{}, wrongBase, "tenant/"+upstream, upstream)
		if info, ok := ResolvedModelInfo(req); ok || info != nil {
			t.Fatalf("unmatched base URL model info = (%+v, %t), want registry fallback", info, ok)
		}
	})

	t.Run("other providers without models keep registry fallback", func(t *testing.T) {
		claude := configuredCapabilityTestAuth("claude-unlisted", "claude-unlisted")
		registerCapabilityTestAuth(t, manager, claude)
		req := manager.attachResolvedAPIKeyModelInfo(cliproxyexecutor.Request{}, claude, "tenant/"+upstream, upstream)
		if info, ok := ResolvedModelInfo(req); ok || info != nil {
			t.Fatalf("unlisted Claude model info = (%+v, %t), want registry fallback", info, ok)
		}
	})

	t.Run("OAuth uses selected plan catalogue", func(t *testing.T) {
		freeReq := manager.attachResolvedAPIKeyModelInfo(cliproxyexecutor.Request{}, oauth, "tenant/gpt-6-astra", "gpt-6-astra")
		if info, ok := ResolvedModelInfo(freeReq); ok || info != nil {
			t.Fatalf("free OAuth pro-only model info = (%+v, %t), want fallback", info, ok)
		}
		pro := *oauth
		pro.Attributes = map[string]string{"plan_type": "pro"}
		assertSelected(t, &pro, "tenant/gpt-6-astra", "gpt-6-astra", true)
		if info, ok := ResolvedAPIKeyModelInfo(manager.attachResolvedAPIKeyModelInfo(cliproxyexecutor.Request{}, oauth, "tenant/"+upstream, upstream)); ok || info != nil {
			t.Fatalf("OAuth API-key info = (%+v, %t), want none", info, ok)
		}
	})

	t.Run("reusing a request cannot retain another credential's capability", func(t *testing.T) {
		req := manager.attachResolvedAPIKeyModelInfo(cliproxyexecutor.Request{}, oauth, "tenant/"+upstream, upstream)
		req = manager.attachResolvedAPIKeyModelInfo(req, disabled, "tenant/public", upstream)
		if info, ok := ResolvedModelInfo(req); !ok || info == nil || info.SupportConfigurationUpdate {
			t.Fatalf("key after OAuth capability = (%+v, %t), want false", info, ok)
		}
		req = manager.attachResolvedAPIKeyModelInfo(req, enabled, "tenant/public", upstream)
		if info, ok := ResolvedModelInfo(req); !ok || info == nil || !info.SupportConfigurationUpdate {
			t.Fatalf("enabled key capability = (%+v, %t), want true", info, ok)
		}
		req = manager.attachResolvedAPIKeyModelInfo(req, oauth, "tenant/"+upstream, upstream)
		if info, ok := ResolvedAPIKeyModelInfo(req); ok || info != nil {
			t.Fatalf("OAuth retained API-key info = (%+v, %t), want none", info, ok)
		}
		req = manager.attachResolvedAPIKeyModelInfo(req, &Auth{Provider: "claude"}, "unknown", "unknown")
		if info, ok := ResolvedModelInfo(req); ok || info != nil {
			t.Fatalf("unmatched request retained capability = (%+v, %t), want fallback", info, ok)
		}
	})

	t.Run("reload keeps the old request snapshot", func(t *testing.T) {
		models, _, _, oldRouting := manager.executionModelCandidatesWithAlias(enabled, "tenant/public")
		if len(models) != 1 || models[0] != upstream {
			t.Fatalf("execution models = %v, want [%s]", models, upstream)
		}
		oldReq := attachResolvedAPIKeyModelInfo(oldRouting, cliproxyexecutor.Request{}, enabled, "tenant/public", models[0])
		manager.SetConfig(&internalconfig.Config{CodexKey: []internalconfig.CodexKey{{
			APIKey: "key-enabled", Prefix: "tenant", Models: []internalconfig.CodexModel{{Name: upstream, Alias: "public"}},
		}}})
		assertSelected(t, enabled, "tenant/public", upstream, false)
		if info, ok := ResolvedModelInfo(oldReq); !ok || info == nil || !info.SupportConfigurationUpdate {
			t.Fatalf("old request capability = (%+v, %t), want true", info, ok)
		}
		oldReqAgain := attachResolvedAPIKeyModelInfo(oldRouting, cliproxyexecutor.Request{}, enabled, "tenant/public", models[0])
		if info, ok := ResolvedModelInfo(oldReqAgain); !ok || info == nil || !info.SupportConfigurationUpdate {
			t.Fatalf("old routing capability = (%+v, %t), want true", info, ok)
		}
	})
}

func TestSelectedCodexConfigurationUpdateCapabilityDuplicateKey(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{CodexKey: []internalconfig.CodexKey{
		{APIKey: "shared-key", Prefix: "tenant", BaseURL: "https://one.example/v1", Models: []internalconfig.CodexModel{{Name: "gpt-6-luna", Alias: "public", SupportConfigurationUpdate: true}}},
		{APIKey: "shared-key", Prefix: "tenant", BaseURL: "https://two.example/v1", Models: []internalconfig.CodexModel{{Name: "gpt-6-luna", Alias: "public"}}},
	}})
	for i, want := range []bool{true, false} {
		auth := configuredCapabilityTestAuth("codex-shared-"+strconv.Itoa(i), "shared-key")
		auth.Provider = "codex"
		auth.Attributes[AttributeConfigIndex] = strconv.Itoa(i)
		auth.Attributes["base_url"] = []string{"https://one.example/v1", "https://two.example/v1"}[i]
		registerCapabilityTestAuth(t, manager, auth)
		req := manager.attachResolvedAPIKeyModelInfo(cliproxyexecutor.Request{}, auth, "tenant/public", "gpt-6-luna")
		info, ok := ResolvedModelInfo(req)
		if !ok || info == nil || info.SupportConfigurationUpdate != want {
			t.Fatalf("key at index %d capability = (%+v, %t), want %t", i, info, ok, want)
		}
	}
}

type selectedCapabilityCaptureExecutor struct {
	provider string
	requests []cliproxyexecutor.Request
}

func (e *selectedCapabilityCaptureExecutor) Identifier() string { return e.provider }

func (e *selectedCapabilityCaptureExecutor) Execute(_ context.Context, _ *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.requests = append(e.requests, req)
	return cliproxyexecutor.Response{}, nil
}

func (e *selectedCapabilityCaptureExecutor) ExecuteStream(_ context.Context, _ *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.requests = append(e.requests, req)
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(`data: {"type":"response.completed"}` + "\n\n")}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *selectedCapabilityCaptureExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *selectedCapabilityCaptureExecutor) CountTokens(_ context.Context, _ *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.requests = append(e.requests, req)
	return cliproxyexecutor.Response{}, nil
}

func (e *selectedCapabilityCaptureExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestSelectedCodexConfigurationUpdateCapabilityExecutionModelOverride(t *testing.T) {
	const model = "gpt-6-luna"
	tests := []struct {
		name           string
		provider       string
		key            string
		config         *internalconfig.Config
		selectionModel string
		executionModel string
		wantBound      bool
		wantUpdate     bool
		oauth          bool
	}{
		{
			name: "unlisted_codex_key_defaults_off", provider: "codex", key: "key-unlisted",
			config:         &internalconfig.Config{CodexKey: []internalconfig.CodexKey{{APIKey: "key-unlisted", Prefix: "tenant"}}},
			selectionModel: "tenant/" + model, executionModel: model, wantBound: true,
		},
		{
			name: "configured_execution_model_not_selection_model", provider: "codex", key: "key-explicit",
			config: &internalconfig.Config{CodexKey: []internalconfig.CodexKey{{APIKey: "key-explicit", Prefix: "tenant", Models: []internalconfig.CodexModel{
				{Name: model, Alias: "public", SupportConfigurationUpdate: true},
				{Name: "gpt-6-astra", Alias: "other", SupportConfigurationUpdate: false},
			}}}},
			selectionModel: "tenant/public", executionModel: "gpt-6-astra", wantBound: true,
		},
		{
			name: "configured_execution_model_enabled", provider: "codex", key: "key-explicit",
			config: &internalconfig.Config{CodexKey: []internalconfig.CodexKey{{APIKey: "key-explicit", Prefix: "tenant", Models: []internalconfig.CodexModel{
				{Name: model, Alias: "public", SupportConfigurationUpdate: true},
			}}}},
			selectionModel: "tenant/public", executionModel: model, wantBound: true, wantUpdate: true,
		},
		{
			name: "unlisted_execution_model_cannot_inherit_oauth_capability", provider: "codex", key: "key-explicit",
			config: &internalconfig.Config{CodexKey: []internalconfig.CodexKey{{APIKey: "key-explicit", Prefix: "tenant", Models: []internalconfig.CodexModel{
				{Name: "other-configured-model", Alias: "public", SupportConfigurationUpdate: true},
			}}}},
			selectionModel: "tenant/public", executionModel: model, wantBound: true,
		},
		{
			name: "codex_oauth_uses_execution_model", provider: "codex", oauth: true,
			config:         &internalconfig.Config{},
			selectionModel: "tenant/gpt-6-astra", executionModel: model, wantBound: true, wantUpdate: true,
		},
		{
			name: "unmatched_codex_key_keeps_fallback", provider: "codex", key: "key-unmatched",
			config:         &internalconfig.Config{CodexKey: []internalconfig.CodexKey{{APIKey: "key-configured", Prefix: "tenant"}}},
			selectionModel: "tenant/" + model, executionModel: model,
		},
		{
			name: "non_codex_override_keeps_fallback", provider: "claude", key: "key-claude",
			config:         &internalconfig.Config{ClaudeKey: []internalconfig.ClaudeKey{{APIKey: "key-claude", Prefix: "tenant", Models: []internalconfig.ClaudeModel{{Name: model}}}}},
			selectionModel: "tenant/" + model, executionModel: model,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			manager.SetConfig(tc.config)
			executor := &selectedCapabilityCaptureExecutor{provider: tc.provider}
			manager.RegisterExecutor(executor)
			auth := configuredCapabilityTestAuth("override-"+tc.name, tc.key)
			auth.Provider = tc.provider
			if tc.oauth {
				auth.Attributes = map[string]string{"plan_type": "free"}
				auth.Metadata = map[string]any{"access_token": "fake-token"}
			}
			registerCapabilityTestAuth(t, manager, auth)
			reg := registry.GetGlobalRegistry()
			reg.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: tc.selectionModel, SupportConfigurationUpdate: true}})
			reg.RegisterClient("oauth-same-name-"+tc.name, "codex", []*registry.ModelInfo{{ID: model, SupportConfigurationUpdate: true}})
			t.Cleanup(func() {
				reg.UnregisterClient(auth.ID)
				reg.UnregisterClient("oauth-same-name-" + tc.name)
			})
			manager.RefreshSchedulerEntry(auth.ID)

			request := cliproxyexecutor.Request{Model: tc.executionModel}
			opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.AuthSelectionModelMetadataKey: tc.selectionModel}}
			check := func(t *testing.T, method string) {
				t.Helper()
				if len(executor.requests) != 1 {
					t.Fatalf("%s executor requests = %d, want 1", method, len(executor.requests))
				}
				execReq := executor.requests[0]
				executor.requests = nil
				if execReq.Model != tc.executionModel {
					t.Fatalf("%s executor model = %q, want %q", method, execReq.Model, tc.executionModel)
				}
				info, ok := ResolvedModelInfo(execReq)
				if ok != tc.wantBound || (tc.wantBound && (info == nil || info.SupportConfigurationUpdate != tc.wantUpdate || info.ID != tc.executionModel)) {
					t.Fatalf("%s execution model capability = (%+v, %t), want bound=%t update=%t model=%q", method, info, ok, tc.wantBound, tc.wantUpdate, tc.executionModel)
				}
				if tc.name == "unlisted_execution_model_cannot_inherit_oauth_capability" && (info.Thinking == nil || len(info.Thinking.Levels) == 0) {
					t.Fatalf("%s unlisted execution model lost static thinking capability: %+v", method, info)
				}
			}
			t.Run("Execute", func(t *testing.T) {
				if _, errExecute := manager.Execute(t.Context(), []string{tc.provider}, request, opts); errExecute != nil {
					t.Fatalf("Execute() error = %v", errExecute)
				}
				check(t, "Execute")
			})
			t.Run("ExecuteStream", func(t *testing.T) {
				stream, errStream := manager.ExecuteStream(t.Context(), []string{tc.provider}, request, opts)
				if errStream != nil {
					t.Fatalf("ExecuteStream() error = %v", errStream)
				}
				for range stream.Chunks {
				}
				check(t, "ExecuteStream")
			})
			t.Run("ExecuteCount", func(t *testing.T) {
				if _, errCount := manager.ExecuteCount(t.Context(), []string{tc.provider}, request, opts); errCount != nil {
					t.Fatalf("ExecuteCount() error = %v", errCount)
				}
				check(t, "ExecuteCount")
			})
		})
	}
}

type selectedCapabilityHomeDispatcher struct {
	auth *Auth
}

func (selectedCapabilityHomeDispatcher) HeartbeatOK() bool { return true }

func (d selectedCapabilityHomeDispatcher) RPopAuth(context.Context, string, string, http.Header, int) ([]byte, error) {
	return json.Marshal(homeAuthDispatchResponse{
		Model: "gpt-6-luna", Auth: *d.auth,
		ModelInfo: &homeDispatchModelInfo{ID: "home-selected-model"},
	})
}

func (selectedCapabilityHomeDispatcher) AbortAmbiguousDispatch() {}

func TestSelectedCodexConfigurationUpdateCapabilityHomeExecutionModelOverride(t *testing.T) {
	auth := configuredCapabilityTestAuth("home-codex-unlisted", "home-key")
	auth.Provider = "codex"
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		Home:     internalconfig.HomeConfig{Enabled: true},
		CodexKey: []internalconfig.CodexKey{{APIKey: "home-key", Prefix: "tenant"}},
	})
	manager.PublishHomeDispatch(selectedCapabilityHomeDispatcher{auth: auth}, executionregistry.New(), 1)
	executor := &selectedCapabilityCaptureExecutor{provider: "codex"}
	manager.RegisterExecutor(executor)

	for _, tc := range []struct {
		name, model, wantInfoID string
		selectionModel          string
	}{
		{name: "restored_execution_model", model: "gpt-6-luna", selectionModel: "tenant/gpt-6-luna", wantInfoID: "gpt-6-luna"},
		{name: "home_selection_precedence", model: "tenant/gpt-6-luna", wantInfoID: "home-selected-model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := cliproxyexecutor.Request{Model: tc.model}
			opts := cliproxyexecutor.Options{}
			if tc.selectionModel != "" {
				opts.Metadata = map[string]any{cliproxyexecutor.AuthSelectionModelMetadataKey: tc.selectionModel}
			}
			check := func(t *testing.T) {
				t.Helper()
				if len(executor.requests) != 1 {
					t.Fatalf("Home executor requests = %d, want 1", len(executor.requests))
				}
				execReq := executor.requests[0]
				executor.requests = nil
				info, ok := ResolvedModelInfo(execReq)
				if !ok || info == nil || info.ID != tc.wantInfoID || info.SupportConfigurationUpdate {
					t.Fatalf("Home execution model %q capability = (%+v, %t), want ID=%q update=false", execReq.Model, info, ok, tc.wantInfoID)
				}
			}
			t.Run("Execute", func(t *testing.T) {
				if _, errExecute := manager.Execute(t.Context(), []string{"codex"}, request, opts); errExecute != nil {
					t.Fatalf("Execute() error = %v", errExecute)
				}
				check(t)
			})
			t.Run("ExecuteStream", func(t *testing.T) {
				stream, errStream := manager.ExecuteStream(t.Context(), []string{"codex"}, request, opts)
				if errStream != nil {
					t.Fatalf("ExecuteStream() error = %v", errStream)
				}
				for range stream.Chunks {
				}
				check(t)
			})
			t.Run("ExecuteCount", func(t *testing.T) {
				if _, errCount := manager.ExecuteCount(t.Context(), []string{"codex"}, request, opts); errCount != nil {
					t.Fatalf("ExecuteCount() error = %v", errCount)
				}
				check(t)
			})
		})
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
