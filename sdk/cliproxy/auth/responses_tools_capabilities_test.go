package auth

import (
	"context"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func capabilitiesTestManager(t *testing.T, routes []internalconfig.ResponsesToolsRoute) *Manager {
	t.Helper()
	cfg := &internalconfig.Config{}
	cfg.ResponsesTools.Enabled = true
	cfg.ResponsesTools.Routes = routes
	cfg.NormalizeResponsesToolsConfig()
	if err := cfg.ValidateResponsesToolsConfig(); err != nil {
		t.Fatalf("config: %v", err)
	}
	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(cfg)
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("cap-auth", "codex", []*registry.ModelInfo{{ID: "gpt-5.6-sol"}})
	t.Cleanup(func() { reg.UnregisterClient("cap-auth") })
	if _, err := mgr.Register(context.Background(), &Auth{
		ID: "cap-auth", Provider: "codex",
		Attributes: map[string]string{"auth_kind": "oauth"},
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
	mgr.SetConfig(&internalconfig.Config{})
	if supported := mgr.ClientSearchSupported("gpt-5.6-sol"); supported != nil {
		t.Fatalf("disabled feature must stay nil, got %v", *supported)
	}
}
