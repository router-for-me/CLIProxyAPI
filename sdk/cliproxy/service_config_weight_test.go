package cliproxy

import (
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestWeightedRoundRobinRoutingSelector(t *testing.T) {
	state := normalizedRoutingRuntimeState(&internalconfig.Config{
		Routing: internalconfig.RoutingConfig{Strategy: "wrr"},
	})
	if state.strategy != "weighted-round-robin" {
		t.Fatalf("strategy = %q, want weighted-round-robin", state.strategy)
	}
	if _, ok := newRoutingSelector(state).(*coreauth.WeightedRoundRobinSelector); !ok {
		t.Fatalf("selector type = %T, want *auth.WeightedRoundRobinSelector", newRoutingSelector(state))
	}
}

func TestServiceRejectsInvalidCredentialWeightConfigCommit(t *testing.T) {
	originalCfg := &internalconfig.Config{}
	service := &Service{cfg: originalCfg}
	invalidWeight := internalconfig.MaxCredentialWeight + 1
	newCfg := &internalconfig.Config{
		VertexCompatAPIKey: []internalconfig.VertexCompatKey{{
			APIKey: "vertex-key",
			Weight: &invalidWeight,
		}},
	}

	if service.applyConfigUpdateWithAuthSynthesis(nil, newCfg, true) {
		t.Fatal("hot config application accepted an invalid credential weight")
	}
	if service.cfg != originalCfg {
		t.Fatal("invalid hot config replaced the active config")
	}
	if service.configSequence != 0 {
		t.Fatalf("config sequence = %d, want 0", service.configSequence)
	}
}

func TestPowerOfTwoChoicesAndLeastUsedRoutingSelectors(t *testing.T) {
	cases := []struct {
		raw      string
		strategy string
	}{
		{"power-of-two-choices", "power-of-two-choices"},
		{"poweroftwochoices", "power-of-two-choices"},
		{"p2c", "power-of-two-choices"},
		{"two-random-choices", "power-of-two-choices"},
		{"least-used", "least-used"},
		{"leastused", "least-used"},
		{"least-busy", "least-used"},
	}
	for _, tc := range cases {
		state := normalizedRoutingRuntimeState(&internalconfig.Config{
			Routing: internalconfig.RoutingConfig{Strategy: tc.raw},
		})
		if state.strategy != tc.strategy {
			t.Fatalf("strategy for %q = %q, want %q", tc.raw, state.strategy, tc.strategy)
		}
		selector := newRoutingSelector(state)
		switch tc.strategy {
		case "power-of-two-choices":
			if _, ok := selector.(*coreauth.P2CSelector); !ok {
				t.Fatalf("selector type for %q = %T, want *auth.P2CSelector", tc.raw, selector)
			}
		case "least-used":
			if _, ok := selector.(*coreauth.LeastUsedSelector); !ok {
				t.Fatalf("selector type for %q = %T, want *auth.LeastUsedSelector", tc.raw, selector)
			}
		}
	}
}
