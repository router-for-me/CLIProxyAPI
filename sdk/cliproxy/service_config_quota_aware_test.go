package cliproxy

import (
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestQuotaAwareRoutingSelector(t *testing.T) {
	state := normalizedRoutingRuntimeState(&internalconfig.Config{
		Routing: internalconfig.RoutingConfig{Strategy: "quota-aware"},
	})
	if state.strategy != "quota-aware" {
		t.Fatalf("strategy = %q, want quota-aware", state.strategy)
	}
	if _, ok := newRoutingSelector(state).(*coreauth.QuotaAwareSelector); !ok {
		t.Fatalf("selector type = %T, want *auth.QuotaAwareSelector", newRoutingSelector(state))
	}
}

func TestSessionAffinityAutoTTLRoutingState(t *testing.T) {
	state := normalizedRoutingRuntimeState(&internalconfig.Config{
		Routing: internalconfig.RoutingConfig{
			Strategy:           "quota-aware",
			SessionAffinity:    true,
			SessionAffinityTTL: "AUTO",
		},
	})
	if !state.sessionAffinityAutoTTL {
		t.Fatal("sessionAffinityAutoTTL = false, want true")
	}
	if state.sessionAffinityTTL != time.Hour {
		t.Fatalf("sessionAffinityTTL = %v, want default 1h", state.sessionAffinityTTL)
	}
	selector, ok := newRoutingSelector(state).(*coreauth.SessionAffinitySelector)
	if !ok {
		t.Fatalf("selector type = %T, want *auth.SessionAffinitySelector", newRoutingSelector(state))
	}
	selector.Stop()

	fixed := normalizedRoutingRuntimeState(&internalconfig.Config{
		Routing: internalconfig.RoutingConfig{SessionAffinity: true, SessionAffinityTTL: "5m"},
	})
	if fixed.sessionAffinityAutoTTL || fixed.sessionAffinityTTL != 5*time.Minute {
		t.Fatalf("fixed TTL state = %+v", fixed)
	}
}
