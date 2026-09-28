package auth

import (
	"context"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/responsestools"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func capabilitiesTestManager(t *testing.T, routes []internalconfig.ResponsesToolsRoute) *Manager {
	return capabilitiesTestManagerForModels(t, routes, "gpt-5.6-sol", "")
}

func capabilitiesTestManagerForModels(t *testing.T, routes []internalconfig.ResponsesToolsRoute, clientModel, upstreamModel string) *Manager {
	t.Helper()
	cfg := &internalconfig.Config{}
	enabled := true
	cfg.ResponsesTools.Enabled = &enabled
	cfg.ResponsesTools.Routes = routes
	cfg.NormalizeResponsesToolsConfig()
	if err := cfg.ValidateResponsesToolsConfig(); err != nil {
		t.Fatalf("config: %v", err)
	}
	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(cfg)
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("cap-auth", "codex", []*registry.ModelInfo{{ID: clientModel}})
	t.Cleanup(func() { reg.UnregisterClient("cap-auth") })
	attributes := map[string]string{"auth_kind": "oauth"}
	if upstreamModel != "" {
		attributes[homeUpstreamModelAttributeKey] = upstreamModel
	}
	if _, err := mgr.Register(context.Background(), &Auth{
		ID: "cap-auth", Provider: "codex",
		Attributes: attributes,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	mgr.RegisterExecutor(&fakeResponsesExecutor{provider: "codex", toFormat: "codex"})
	return mgr
}

func capRoute(clientSearch, customTools string) internalconfig.ResponsesToolsRoute {
	return internalconfig.ResponsesToolsRoute{
		Match: internalconfig.ResponsesToolsMatch{
			Provider: "codex", AuthKind: "oauth",
			UpstreamModel: "gpt-5.6-sol", UpstreamFormat: "codex",
		},
		ClientSearch: clientSearch, CustomTools: customTools,
	}
}

func TestClientSearchSupportedBridge(t *testing.T) {
	mgr := capabilitiesTestManager(t, []internalconfig.ResponsesToolsRoute{capRoute("bridge", "inherit")})
	supported := mgr.ClientSearchSupported("gpt-5.6-sol")
	if supported == nil || !*supported {
		t.Fatalf("bridge route must advertise support, got %v", supported)
	}
}

func TestClientSearchSupportedDisabledVetoes(t *testing.T) {
	mgr := capabilitiesTestManager(t, []internalconfig.ResponsesToolsRoute{capRoute("disabled", "inherit")})
	supported := mgr.ClientSearchSupported("gpt-5.6-sol")
	if supported == nil || *supported {
		t.Fatalf("disabled route must veto, got %v", supported)
	}
}

func TestClientSearchSupportedUnknownIsNil(t *testing.T) {
	mgr := capabilitiesTestManager(t, []internalconfig.ResponsesToolsRoute{capRoute("bridge", "inherit")})
	if supported := mgr.ClientSearchSupported("no-such-model"); supported != nil {
		t.Fatalf("unknown alias must not promote, got %v", *supported)
	}
}

func TestClientSearchSupportedOffIsNil(t *testing.T) {
	mgr := NewManager(nil, nil, nil)
	cfg := &internalconfig.Config{}
	disabled := false
	cfg.ResponsesTools.Enabled = &disabled
	mgr.SetConfig(cfg)
	if supported := mgr.ClientSearchSupported("gpt-5.6-sol"); supported != nil {
		t.Fatalf("disabled feature must stay nil, got %v", *supported)
	}
}

func TestClientSearchSupportedFollowsConventionWithoutRoutes(t *testing.T) {
	mgr := capabilitiesTestManager(t, nil)
	supported := mgr.ClientSearchSupported("gpt-5.6-sol")
	if supported == nil || !*supported {
		t.Fatalf("native upstream must advertise search support by convention, got %v", supported)
	}
}

func TestResponsesToolsReplayFollowsProviderConvention(t *testing.T) {
	mgr := capabilitiesTestManager(t, nil)
	if mgr.ResponsesToolsMayApplyToRoute("codex", "gpt-5.6-sol") {
		t.Fatal("a native Responses provider needs no rewriting and must keep passthrough")
	}
	if !mgr.ResponsesToolsMayApplyToRoute("xai", "grok") {
		t.Fatal("the xai executor strips tool_search, so its turns must take the bridge")
	}
	if !mgr.ResponsesToolsMayApplyToRoute("gemini", "gemini-3.8-flash") {
		t.Fatal("a non-native provider must take the tools bridge and therefore replay")
	}
}

// TestProviderNeedsToolsBridgeMatchesConventionPolicy locks the replay
// decision to the same convention the request path uses, so a turn is never
// sent as native passthrough while the request path still rewrites it.
func TestProviderNeedsToolsBridgeMatchesRequestToFormat(t *testing.T) {
	providers := []string{
		"codex", "xai", "meta", "claude", "gemini", "vertex", "aistudio",
		"kimi", "kimi-ai", "kimi.ai", "kimi.com", "antigravity", "devin",
		"openai-compatible", "unknown-provider", "",
	}
	for _, provider := range providers {
		format := requestToFormat(provider, nil, cliproxyexecutor.Request{}, cliproxyexecutor.Options{})
		want := responsestools.ConventionPolicy(responsestools.Route{
			Provider:       provider,
			UpstreamFormat: format.String(),
		}).ClientSearch == responsestools.ClientSearchBridge
		if got := providerNeedsToolsBridge(provider); got != want {
			t.Errorf("provider %q: providerNeedsToolsBridge=%v, convention implies %v", provider, got, want)
		}
	}
}

func TestResponsesToolsEnabledWithoutRoutes(t *testing.T) {
	mgr := capabilitiesTestManager(t, nil)
	if !mgr.ResponsesToolsEnabled() {
		t.Fatal("the convention policy must be active without explicit routes")
	}
}

func TestResponsesToolsMayApplyToClientModelResolvesUpstreamAlias(t *testing.T) {
	mgr := capabilitiesTestManagerForModels(t, []internalconfig.ResponsesToolsRoute{
		capRoute("bridge", "inherit"),
	}, "client-alias", "gpt-5.6-sol")

	if mgr.ResponsesToolsMayApplyToRoute("codex", "client-alias") {
		t.Fatal("public model alias must not be mistaken for the configured upstream model")
	}
	if !mgr.ResponsesToolsMayApplyToClientModel("client-alias", "codex", "cap-auth") {
		t.Fatal("active upstream route behind the client model alias must require tools replay")
	}
	if mgr.ResponsesToolsMayApplyToClientModel("client-alias", "gemini", "cap-auth") {
		t.Fatal("different provider must not require tools replay")
	}
	if mgr.ResponsesToolsMayApplyToClientModel("client-alias", "codex", "other-auth") {
		t.Fatal("different pinned auth must not require tools replay")
	}
}
