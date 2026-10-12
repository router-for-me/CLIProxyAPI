package cliproxy

import (
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestCapacityRoutingConfiguration(t *testing.T) {
	state := normalizedRoutingRuntimeState(&internalconfig.Config{
		Routing: internalconfig.RoutingConfig{Strategy: " MOST-REMAINING "},
	})
	if state.strategy != "most-remaining" {
		t.Fatalf("strategy = %q", state.strategy)
	}
	if _, ok := newRoutingSelector(state).(*coreauth.CapacitySelector); !ok {
		t.Fatalf("selector = %T", newRoutingSelector(state))
	}
	state.sessionAffinity = true
	selector := newRoutingSelector(state)
	if _, ok := selector.(*coreauth.SessionAffinitySelector); !ok {
		t.Fatalf("affinity selector = %T", selector)
	}
	selector.(coreauth.StoppableSelector).Stop()
}
