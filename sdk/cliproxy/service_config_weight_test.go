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

func TestNormalizedRoutingRuntimeState_SessionAffinityRulesKeyStable(t *testing.T) {
	t.Parallel()

	withSuffix := normalizedRoutingRuntimeState(&internalconfig.Config{
		Routing: internalconfig.RoutingConfig{
			SessionAffinity: true,
			SessionAffinityRules: []internalconfig.SessionAffinityRule{
				{Provider: "XAI", Model: "grok-4.5(high)", MaxRequests: 10},
			},
		},
	})
	withoutSuffix := normalizedRoutingRuntimeState(&internalconfig.Config{
		Routing: internalconfig.RoutingConfig{
			SessionAffinity: true,
			SessionAffinityRules: []internalconfig.SessionAffinityRule{
				{Provider: "xai", Model: "grok-4.5", MaxRequests: 10},
			},
		},
	})

	if withSuffix.sessionAffinityRulesKey == "" {
		t.Fatal("expected non-empty rules key")
	}
	if withSuffix.sessionAffinityRulesKey != withoutSuffix.sessionAffinityRulesKey {
		t.Fatalf("rules key mismatch: %q vs %q", withSuffix.sessionAffinityRulesKey, withoutSuffix.sessionAffinityRulesKey)
	}
	if !routingRuntimeStateEqual(withSuffix, withoutSuffix) {
		t.Fatal("thinking-suffix-only model change should not rebuild routing state")
	}
	if len(withSuffix.sessionAffinityRules) != 1 {
		t.Fatalf("compiled rules len = %d, want 1", len(withSuffix.sessionAffinityRules))
	}
	if got := withSuffix.sessionAffinityRules[0].Model; got != "grok-4.5" {
		t.Fatalf("compiled model = %q, want grok-4.5", got)
	}
}

func TestNormalizedRoutingRuntimeState_SessionAffinityRulesOrderMatters(t *testing.T) {
	t.Parallel()

	// Equal-specificity order is preserved intentionally (first match wins at equal score).
	left := normalizedRoutingRuntimeState(&internalconfig.Config{
		Routing: internalconfig.RoutingConfig{
			SessionAffinityRules: []internalconfig.SessionAffinityRule{
				{Model: "a", MaxRequests: 1},
				{Model: "b", MaxRequests: 2},
			},
		},
	})
	right := normalizedRoutingRuntimeState(&internalconfig.Config{
		Routing: internalconfig.RoutingConfig{
			SessionAffinityRules: []internalconfig.SessionAffinityRule{
				{Model: "b", MaxRequests: 2},
				{Model: "a", MaxRequests: 1},
			},
		},
	})
	if left.sessionAffinityRulesKey == right.sessionAffinityRulesKey {
		t.Fatal("rule order should affect rules key (equal-specificity first-match semantics)")
	}
	if routingRuntimeStateEqual(left, right) {
		t.Fatal("reordered equal-specificity rules should be treated as a routing change")
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
