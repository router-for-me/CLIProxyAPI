package cliproxy

import (
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func routingState(strategy string, byProvider map[string]string) routingRuntimeState {
	return normalizedRoutingRuntimeState(&internalconfig.Config{
		Routing: internalconfig.RoutingConfig{Strategy: strategy, ProviderStrategies: byProvider},
	})
}

func TestRoutingStateProviderStrategies(t *testing.T) {
	state := routingState("", map[string]string{"codex": "p2c", "xai": "round-robin", "kimi": "bogus", "claude": "LeastUsed"})
	if state.providerStrategies != "claude=least-used,codex=p2c" {
		t.Fatalf("providerStrategies = %q", state.providerStrategies)
	}
	sel, ok := newRoutingSelector(state).(*coreauth.ProviderStrategySelector)
	if !ok {
		t.Fatalf("selector = %T, want *auth.ProviderStrategySelector", newRoutingSelector(state))
	}
	if _, ok := sel.Default.(*coreauth.RoundRobinSelector); !ok {
		t.Errorf("default = %T", sel.Default)
	}
	if _, ok := sel.ByProvider["codex"].(*coreauth.P2CSelector); !ok {
		t.Errorf("codex = %T", sel.ByProvider["codex"])
	}

	same := routingState("", map[string]string{"xai": "round-robin"})
	if same.providerStrategies != "" {
		t.Errorf("all-default map = %q, want empty", same.providerStrategies)
	}
	if _, ok := newRoutingSelector(same).(*coreauth.RoundRobinSelector); !ok {
		t.Errorf("all-default selector = %T, want today's *auth.RoundRobinSelector", newRoutingSelector(same))
	}
	if routingState("", map[string]string{"codex": "p2c"}) == routingState("", map[string]string{"codex": "random"}) {
		t.Error("a map change did not change the state")
	}
	for name, want := range map[string]any{"random": &coreauth.RandomSelector{}, "least-used": &coreauth.LeastUsedSelector{}, "p2c": &coreauth.P2CSelector{}} {
		got := newRoutingSelector(routingState(name, nil))
		if gotType, wantType := typeName(got), typeName(want); gotType != wantType {
			t.Errorf("global %s = %s, want %s", name, gotType, wantType)
		}
	}
	affinity := routingState("", map[string]string{"codex": "p2c"})
	affinity.sessionAffinity = true
	if _, ok := newRoutingSelector(affinity).(*coreauth.SessionAffinitySelector); !ok {
		t.Errorf("with affinity = %T, want the affinity selector around the strategy", newRoutingSelector(affinity))
	}
}

func typeName(v any) string {
	switch v.(type) {
	case *coreauth.RandomSelector:
		return "random"
	case *coreauth.LeastUsedSelector:
		return "least-used"
	case *coreauth.P2CSelector:
		return "p2c"
	}
	return "other"
}
