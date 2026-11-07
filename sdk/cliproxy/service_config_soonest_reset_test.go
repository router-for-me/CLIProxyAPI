package cliproxy

import (
	"context"
	"strconv"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestNormalizedRoutingRuntimeStateSoonestResetAliases(t *testing.T) {
	for _, input := range []string{"soonest-reset", "soonestreset", "reset-first", "resetfirst", " Reset-First ", "SOONEST-RESET"} {
		state := normalizedRoutingRuntimeState(&internalconfig.Config{
			Routing: internalconfig.RoutingConfig{Strategy: input},
		})
		if state.strategy != "soonest-reset" {
			t.Fatalf("strategy for %q = %q, want soonest-reset", input, state.strategy)
		}
		if selector := newRoutingSelector(state); selector == nil {
			t.Fatalf("newRoutingSelector(%q) = nil", input)
		} else if _, ok := selector.(*coreauth.SoonestResetSelector); !ok {
			t.Fatalf("selector type for %q = %T, want *auth.SoonestResetSelector", input, selector)
		}
	}
	for _, input := range []string{"soonest", "reset"} {
		state := normalizedRoutingRuntimeState(&internalconfig.Config{
			Routing: internalconfig.RoutingConfig{Strategy: input},
		})
		if state.strategy != "round-robin" {
			t.Fatalf("strategy for %q = %q, want round-robin", input, state.strategy)
		}
	}
}

func TestSoonestResetRoutingSelectorWithSessionAffinity(t *testing.T) {
	state := normalizedRoutingRuntimeState(&internalconfig.Config{
		Routing: internalconfig.RoutingConfig{Strategy: "reset-first", SessionAffinity: true},
	})
	selector := newRoutingSelector(state)
	affinity, ok := selector.(*coreauth.SessionAffinitySelector)
	if !ok {
		t.Fatalf("selector type = %T, want *auth.SessionAffinitySelector", selector)
	}
	t.Cleanup(affinity.Stop)

	// Without a session the wrapper defers to its fallback. A fill-first or round-robin
	// fallback would start at "a"; soonest-reset prefers the earlier weekly reset on "b".
	observedAt := time.Now().Add(-time.Minute)
	reset := func(d time.Duration) map[string]string {
		return map[string]string{"Anthropic-Ratelimit-Unified-7d-Reset": strconv.FormatInt(time.Now().Add(d).Unix(), 10)}
	}
	auths := []*coreauth.Auth{
		{ID: "a", Provider: "claude", Quota: coreauth.QuotaState{ObservedAt: observedAt, Signals: reset(72 * time.Hour)}},
		{ID: "b", Provider: "claude", Quota: coreauth.QuotaState{ObservedAt: observedAt, Signals: reset(24 * time.Hour)}},
	}
	picked, err := selector.Pick(context.Background(), "claude", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if picked == nil || picked.ID != "b" {
		t.Fatalf("Pick() = %v, want b", picked)
	}
}
