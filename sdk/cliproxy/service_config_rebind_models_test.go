package cliproxy

import (
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestNormalizedRoutingRuntimeStateSessionAffinityRebindOnModels(t *testing.T) {
	cfg := &internalconfig.Config{
		Routing: internalconfig.RoutingConfig{
			SessionAffinity:               true,
			SessionAffinityRebindOnModels: []string{" Claude-Opus-5-5 ", "claude-fable-5-2", "CLAUDE-FABLE-5-2"},
		},
	}
	state := normalizedRoutingRuntimeState(cfg)
	if got, want := state.sessionAffinityRebindOnModels, "claude-fable-5-2,claude-opus-5-5"; got != want {
		t.Fatalf("normalized rebind-on models = %q, want %q", got, want)
	}

	cfg.Routing.SessionAffinityRebindOnModels = []string{"claude-sonnet-5-5"}
	if state == normalizedRoutingRuntimeState(cfg) {
		t.Fatal("routing state did not change after rebind-on model list changed")
	}

	cfg.Routing.SessionAffinity = false
	if got := normalizedRoutingRuntimeState(cfg).sessionAffinityRebindOnModels; got != "" {
		t.Fatalf("rebind-on models = %q with session affinity disabled, want empty", got)
	}
}
