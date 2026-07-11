package auth

import (
	"context"
	"strings"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/modelconfig"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestAttachResolvedAPIKeyModelInfoPrefersExactConfiguredSuffix(t *testing.T) {
	m := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "configured-suffix-auth",
		Provider: "claude",
		Prefix:   "tenant",
		Attributes: map[string]string{
			AttributeAuthKind: AuthKindAPIKey,
			AttributeAPIKey:   "test-key",
			AttributeSource:   "config:claude[0]",
		},
	}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	m.SetConfig(&internalconfig.Config{ClaudeKey: []internalconfig.ClaudeKey{{
		APIKey: "test-key",
		Prefix: "tenant",
		Models: []internalconfig.ClaudeModel{
			{Name: "model", Alias: "public", Thinking: thinkingLevels("high")},
			{Name: "model(low)", Alias: "public-low", Thinking: thinkingLevels("low")},
		},
	}}})

	req := m.attachResolvedAPIKeyModelInfo(cliproxyexecutor.Request{}, auth, "tenant/public-low", "model(low)")
	info, ok := ResolvedAPIKeyModelInfo(req)
	if !ok || info.Thinking == nil || len(info.Thinking.Levels) != 1 || info.Thinking.Levels[0] != "low" {
		t.Fatalf("resolved model info = %+v, want exact suffixed definition", info)
	}
}

func thinkingLevels(levels ...string) *registry.ThinkingSupport {
	return &registry.ThinkingSupport{Levels: levels}
}

func TestCompileAPIKeyModelCapabilitiesUsesAliasAsUpstreamFallback(t *testing.T) {
	routes := make(map[string][]apiKeyModelCapabilityRoute)
	compileAPIKeyModelCapabilities(routes, []internalconfig.ClaudeModel{{
		Alias:    "public",
		Thinking: thinkingLevels("high"),
	}}, "claude")

	got := routes["public"]
	if len(got) != 1 || got[0].upstreamModel != "public" || got[0].modelInfo == nil || got[0].modelInfo.ID != "public" {
		t.Fatalf("alias-only capability routes = %+v, want public upstream capability", got)
	}
}

func TestCompileOpenAICompatModelCapabilitiesUsesAliasAsUpstreamFallback(t *testing.T) {
	routes := make(map[string][]apiKeyModelCapabilityRoute)
	compileOpenAICompatModelCapabilities(routes, []internalconfig.OpenAICompatibilityModel{{
		Alias:    "public",
		Image:    true,
		Thinking: thinkingLevels("high"),
	}})

	got := routes["public"]
	if len(got) != 1 || got[0].upstreamModel != "public" || got[0].executionKind != apiKeyModelExecutionImage || got[0].modelInfo == nil || got[0].modelInfo.ID != "public" || got[0].modelInfo.Thinking == nil {
		t.Fatalf("alias-only OpenAI-compatible routes = %+v, want public image/thinking capability", got)
	}
}

func TestOpenAICompatChatAliasSuffixFallsBackToUnsuffixedCapability(t *testing.T) {
	const (
		alias    = "shared"
		upstream = "upstream"
	)
	routes := make(map[string][]apiKeyModelCapabilityRoute)
	compileOpenAICompatModelCapabilities(routes, []internalconfig.OpenAICompatibilityModel{
		{Name: upstream, Alias: alias},
		{Name: upstream + "(high)", Alias: alias, Image: true},
	})
	auth := &Auth{
		ID:       "suffix-fallback-auth",
		Provider: "openai-compatibility:pool",
		Attributes: map[string]string{
			AttributeAPIKey: "key",
			AttributeSource: "config:pool[test]",
			"compat_name":   "pool",
		},
	}
	snapshot := &apiKeyModelRoutingSnapshot{
		capabilities: apiKeyModelCapabilityTable{auth.ID: routes},
	}

	route, configured, compatible := lookupAPIKeyModelCapability(
		snapshot,
		auth,
		alias+"(high)",
		upstream+"(high)",
		apiKeyModelExecutionChat,
	)
	if !configured || !compatible || route.upstreamModel != upstream || route.executionKind != apiKeyModelExecutionChat {
		t.Fatalf("chat capability = (%+v, %t, %t), want unsuffixed chat fallback", route, configured, compatible)
	}
	candidates := filterAPIKeyModelCandidates(snapshot, auth, alias+"(high)", []string{upstream + "(high)"}, false)
	if len(candidates) != 1 || candidates[0] != upstream+"(high)" {
		t.Fatalf("chat candidates = %v, want suffixed request routed through unsuffixed chat capability", candidates)
	}

	_, configured, compatible = matchAPIKeyModelCapabilityRoute([]apiKeyModelCapabilityRoute{{
		upstreamModel: upstream + "(high)",
		executionKind: apiKeyModelExecutionImage,
	}}, upstream+"(high)", apiKeyModelExecutionChat)
	if !configured || compatible {
		t.Fatalf("incompatible exact route = (configured %t, compatible %t), want configured but incompatible", configured, compatible)
	}
}

func TestEndpointSpecificForceMappingPreservesDirectUpstreamRequest(t *testing.T) {
	m := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "force-mapping-direct-auth",
		Provider: "openai-compatibility:compat",
		Prefix:   "tenant",
		Attributes: map[string]string{
			AttributeAPIKey: "key", AttributeSource: "config:compat[test]",
			"compat_name": "compat", "provider_key": "openai-compatibility:compat",
		},
	}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	m.SetConfig(&internalconfig.Config{OpenAICompatibility: []internalconfig.OpenAICompatibility{{
		Name: "compat", Prefix: "tenant",
		Models: []internalconfig.OpenAICompatibilityModel{{Name: "upstream", Alias: "public", ForceMapping: true}},
	}}})

	directFallback := m.resolveExecutionAliasResult(auth, "tenant/upstream")
	direct := aliasResultForAPIKeyModelCandidate(m.loadAPIKeyModelRouting(), auth, "tenant/upstream", "upstream", false, directFallback)
	if direct.ForceMapping || direct.OriginalAlias != "" {
		t.Fatalf("direct upstream result = %+v, want passthrough", direct)
	}

	aliasFallback := m.resolveExecutionAliasResult(auth, "tenant/public")
	alias := aliasResultForAPIKeyModelCandidate(m.loadAPIKeyModelRouting(), auth, "tenant/public", "upstream", false, aliasFallback)
	if !alias.ForceMapping || alias.OriginalAlias != "public" {
		t.Fatalf("alias result = %+v, want force-mapped public alias", alias)
	}
}

func TestOpenAICompatConfiguredModelFallsBackToStaticThinking(t *testing.T) {
	const upstream = "gpt-5.6-luna"
	static := registry.LookupStaticModelInfo(upstream)
	if static == nil || static.Thinking == nil {
		t.Fatalf("static model %s has no thinking metadata", upstream)
	}
	m := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "static-thinking-auth",
		Provider: "openai-compatibility:compat",
		Attributes: map[string]string{
			AttributeAPIKey: "key", AttributeSource: "config:compat[test]",
			"compat_name": "compat", "provider_key": "openai-compatibility:compat",
		},
	}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	m.SetConfig(&internalconfig.Config{OpenAICompatibility: []internalconfig.OpenAICompatibility{{
		Name: "compat", Models: []internalconfig.OpenAICompatibilityModel{{Name: upstream, Alias: "public"}},
	}}})
	req := m.attachResolvedAPIKeyModelInfo(cliproxyexecutor.Request{}, auth, "public", upstream)
	info, ok := ResolvedAPIKeyModelInfo(req)
	if !ok || info.Thinking == nil {
		t.Fatalf("resolved model info = %+v, want static thinking metadata", info)
	}
	if got, want := strings.Join(info.Thinking.Levels, ","), strings.Join(static.Thinking.Levels, ","); got != want {
		t.Fatalf("thinking levels = %q, want static levels %q", got, want)
	}
}

func TestConfiguredThinkingBindsAcrossAPIKeyProviders(t *testing.T) {
	support := thinkingLevels(" XHIGH ", "high", "none", "auto", "high")
	tests := []struct {
		name     string
		provider string
		attrs    map[string]string
		config   *internalconfig.Config
		wantType string
	}{
		{
			name: "gemini", provider: "gemini", wantType: "gemini",
			config: &internalconfig.Config{GeminiKey: []internalconfig.GeminiKey{{APIKey: "key", Models: []internalconfig.GeminiModel{{Name: "upstream", Alias: "public", Thinking: support}}}}},
		},
		{
			name: "interactions", provider: "gemini-interactions", wantType: "interactions",
			config: &internalconfig.Config{InteractionsKey: []internalconfig.GeminiKey{{APIKey: "key", Models: []internalconfig.GeminiModel{{Name: "upstream", Alias: "public", Thinking: support}}}}},
		},
		{
			name: "claude", provider: "claude", wantType: "claude",
			config: &internalconfig.Config{ClaudeKey: []internalconfig.ClaudeKey{{APIKey: "key", Models: []internalconfig.ClaudeModel{{Name: "upstream", Alias: "public", Thinking: support}}}}},
		},
		{
			name: "codex", provider: "codex", wantType: "codex",
			config: &internalconfig.Config{CodexKey: []internalconfig.CodexKey{{APIKey: "key", Models: []internalconfig.CodexModel{{Name: "upstream", Alias: "public", Thinking: support}}}}},
		},
		{
			name: "xai", provider: "xai", wantType: "xai",
			config: &internalconfig.Config{XAIKey: []internalconfig.XAIKey{{APIKey: "key", Models: []internalconfig.XAIModel{{Name: "upstream", Alias: "public", Thinking: support}}}}},
		},
		{
			name: "vertex", provider: "vertex", wantType: "gemini",
			config: &internalconfig.Config{VertexCompatAPIKey: []internalconfig.VertexCompatKey{{APIKey: "key", Models: []internalconfig.VertexCompatModel{{Name: "upstream", Alias: "public", Thinking: support}}}}},
		},
		{
			name: "openai compatibility", provider: "openai-compatibility:compat", wantType: "openai",
			attrs:  map[string]string{"compat_name": "compat", "provider_key": "openai-compatibility:compat"},
			config: &internalconfig.Config{OpenAICompatibility: []internalconfig.OpenAICompatibility{{Name: "compat", Models: []internalconfig.OpenAICompatibilityModel{{Name: "upstream", Alias: "public", Thinking: support}}}}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager(nil, nil, nil)
			attrs := map[string]string{
				AttributeAPIKey: "key",
				AttributeSource: "config:" + tc.name + "[test]",
			}
			for key, value := range tc.attrs {
				attrs[key] = value
			}
			auth := &Auth{ID: "auth-" + tc.name, Provider: tc.provider, Prefix: "tenant", Attributes: attrs}
			if _, err := m.Register(context.Background(), auth); err != nil {
				t.Fatalf("Register() error = %v", err)
			}
			m.SetConfig(tc.config)

			req := m.attachResolvedAPIKeyModelInfo(cliproxyexecutor.Request{}, auth, "tenant/public", "upstream")
			info, ok := ResolvedAPIKeyModelInfo(req)
			if !ok || info.Thinking == nil {
				t.Fatalf("resolved model info = %+v, want configured thinking", info)
			}
			if info.Type != tc.wantType {
				t.Fatalf("model type = %q, want %q", info.Type, tc.wantType)
			}
			wantLevels := []string{"xhigh", "high", "none", "auto"}
			if len(info.Thinking.Levels) != len(wantLevels) {
				t.Fatalf("thinking levels = %v, want %v", info.Thinking.Levels, wantLevels)
			}
			for i := range wantLevels {
				if info.Thinking.Levels[i] != wantLevels[i] {
					t.Fatalf("thinking levels = %v, want %v", info.Thinking.Levels, wantLevels)
				}
			}
			if !info.Thinking.ZeroAllowed || !info.Thinking.DynamicAllowed {
				t.Fatalf("thinking flags = %+v, want none/auto support", info.Thinking)
			}
		})
	}
}

func TestConfiguredThinkingPreservesStaticModelMetadata(t *testing.T) {
	static := registry.LookupStaticModelInfo("claude-opus-4-6")
	if static == nil || static.MaxCompletionTokens == 0 {
		t.Fatal("static claude-opus-4-6 metadata is unavailable")
	}
	info := modelconfig.ResolveModelInfo("claude-opus-4-6", "claude", thinkingLevels("high"))
	if info.MaxCompletionTokens != static.MaxCompletionTokens || info.ContextLength != static.ContextLength {
		t.Fatalf("configured metadata = %+v, want static limits preserved", info)
	}
}

func TestConfiguredThinkingIsAuthScopedAndReloaded(t *testing.T) {
	m := NewManager(nil, nil, nil)
	authHigh := &Auth{ID: "auth-high", Provider: "claude", Attributes: map[string]string{AttributeAPIKey: "high-key", AttributeSource: "config:claude[high]"}}
	authLow := &Auth{ID: "auth-low", Provider: "claude", Attributes: map[string]string{AttributeAPIKey: "low-key", AttributeSource: "config:claude[low]"}}
	for _, auth := range []*Auth{authHigh, authLow} {
		if _, err := m.Register(context.Background(), auth); err != nil {
			t.Fatalf("Register(%s) error = %v", auth.ID, err)
		}
	}
	setConfig := func(highLevel string) {
		m.SetConfig(&internalconfig.Config{ClaudeKey: []internalconfig.ClaudeKey{
			{APIKey: "high-key", Models: []internalconfig.ClaudeModel{{Name: "upstream", Alias: "public", Thinking: thinkingLevels(highLevel)}}},
			{APIKey: "low-key", Models: []internalconfig.ClaudeModel{{Name: "upstream", Alias: "public", Thinking: thinkingLevels("low")}}},
		}})
	}
	assertLevel := func(auth *Auth, want string) {
		t.Helper()
		req := m.attachResolvedAPIKeyModelInfo(cliproxyexecutor.Request{}, auth, "public", "upstream")
		info, ok := ResolvedAPIKeyModelInfo(req)
		if !ok || info.Thinking == nil || len(info.Thinking.Levels) != 1 || info.Thinking.Levels[0] != want {
			t.Fatalf("auth %s thinking = %+v, want %s", auth.ID, info, want)
		}
	}

	setConfig("high")
	assertLevel(authHigh, "high")
	assertLevel(authLow, "low")
	previousRouting := m.loadAPIKeyModelRouting()
	setConfig("xhigh")
	assertLevel(authHigh, "xhigh")
	assertLevel(authLow, "low")
	previousReq := attachResolvedAPIKeyModelInfo(previousRouting, cliproxyexecutor.Request{}, authHigh, "public", "upstream", false)
	previousInfo, ok := ResolvedAPIKeyModelInfo(previousReq)
	if !ok || previousInfo.Thinking == nil || len(previousInfo.Thinking.Levels) != 1 || previousInfo.Thinking.Levels[0] != "high" {
		t.Fatalf("previous routing snapshot changed after reload: %+v", previousInfo)
	}
}

func TestSetConfigSnapshotsCallerOwnedConfig(t *testing.T) {
	m := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auth", Provider: "claude", Attributes: map[string]string{AttributeAPIKey: "key", AttributeSource: "config:claude[0]"}}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	cfg := &internalconfig.Config{ClaudeKey: []internalconfig.ClaudeKey{{
		APIKey: "key",
		Models: []internalconfig.ClaudeModel{{Name: "upstream", Alias: "public", Thinking: thinkingLevels("high")}},
	}}}
	m.SetConfig(cfg)

	cfg.ClaudeKey[0].Models[0].Thinking.Levels[0] = "low"
	req := m.attachResolvedAPIKeyModelInfo(cliproxyexecutor.Request{}, auth, "public", "upstream")
	info, ok := ResolvedAPIKeyModelInfo(req)
	if !ok || info.Thinking == nil || len(info.Thinking.Levels) != 1 || info.Thinking.Levels[0] != "high" {
		t.Fatalf("configured thinking changed after caller mutation: %+v", info)
	}
}

func TestConfiguredThinkingDoesNotBindOAuthAuth(t *testing.T) {
	m := NewManager(nil, nil, nil)
	auth := &Auth{ID: "oauth", Provider: "claude", Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth, AttributeSource: "config:claude[oauth]"}}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	m.SetConfig(&internalconfig.Config{ClaudeKey: []internalconfig.ClaudeKey{{APIKey: "key", Models: []internalconfig.ClaudeModel{{Name: "upstream", Alias: "public", Thinking: thinkingLevels("high")}}}}})
	req := m.attachResolvedAPIKeyModelInfo(cliproxyexecutor.Request{}, auth, "public", "upstream")
	if _, ok := ResolvedAPIKeyModelInfo(req); ok {
		t.Fatal("OAuth auth unexpectedly received configured API-key capabilities")
	}
}

func TestOpenAICompatAliasPoolBindsSelectedCandidateCapabilities(t *testing.T) {
	m := NewManager(nil, nil, nil)
	auth := &Auth{
		ID: "compat-pool", Provider: "openai-compatibility:pool", Prefix: "tenant",
		Attributes: map[string]string{
			AttributeAPIKey: "key", AttributeSource: "config:pool[test]",
			"compat_name": "pool", "provider_key": "openai-compatibility:pool",
		},
	}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	m.SetConfig(&internalconfig.Config{OpenAICompatibility: []internalconfig.OpenAICompatibility{{
		Name: "pool", Prefix: "tenant",
		Models: []internalconfig.OpenAICompatibilityModel{
			{Name: "upstream-a", Alias: "shared", Thinking: thinkingLevels("high")},
			{Name: "upstream-b", Alias: "shared", Thinking: thinkingLevels("low")},
		},
	}}})

	for upstream, want := range map[string]string{"upstream-a": "high", "upstream-b": "low"} {
		req := m.attachResolvedAPIKeyModelInfo(cliproxyexecutor.Request{}, auth, "tenant/shared", upstream)
		info, ok := ResolvedAPIKeyModelInfo(req)
		if !ok || info.Thinking == nil || len(info.Thinking.Levels) != 1 || info.Thinking.Levels[0] != want {
			t.Fatalf("upstream %s thinking = %+v, want %s", upstream, info, want)
		}
	}
}

func TestKeylessOpenAICompatConfigAuthResolvesPrefixedAlias(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.SetConfig(&internalconfig.Config{
		SDKConfig: internalconfig.SDKConfig{ForceModelPrefix: true},
		OpenAICompatibility: []internalconfig.OpenAICompatibility{{
			Name: "keyless", Prefix: "tenant",
			Models: []internalconfig.OpenAICompatibilityModel{{Name: "upstream", Alias: "public"}},
		}},
	})
	auth := &Auth{
		ID:       "keyless-openai-compat",
		Provider: "openai-compatibility:keyless",
		Prefix:   "tenant",
		Attributes: map[string]string{
			"compat_name":   "keyless",
			"provider_key":  "keyless",
			AttributeSource: "config:keyless[0]",
		},
	}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if got := m.applyAPIKeyModelAlias(auth, "public"); got != "upstream" {
		t.Fatalf("resolved upstream model = %q, want upstream", got)
	}
	req := m.attachResolvedAPIKeyModelInfo(cliproxyexecutor.Request{}, auth, "tenant/public", "upstream")
	info, ok := ResolvedAPIKeyModelInfo(req)
	if !ok || info.Thinking == nil || len(info.Thinking.Levels) != 3 {
		t.Fatalf("keyless configured capability = %+v, want openai-compatibility defaults", info)
	}
}
