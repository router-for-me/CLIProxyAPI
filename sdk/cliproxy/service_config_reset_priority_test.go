package cliproxy

import (
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestResetPriorityRoutingSelector(t *testing.T) {
	state := normalizedRoutingRuntimeState(&internalconfig.Config{
		Routing: internalconfig.RoutingConfig{
			Strategy:                   "reset-priority",
			ResetPriorityUrgentSeconds: 1200,
			ResetPriorityJitterSeconds: 30,
			ResetPriorityFallback:      "round-robin",
		},
	})
	if state.strategy != "reset-priority" {
		t.Fatalf("strategy = %q, want reset-priority", state.strategy)
	}
	if state.resetPriorityUrgentSeconds != 1200 || state.resetPriorityJitterSeconds != 30 {
		t.Fatalf("reset-priority windows = %d/%d, want 1200/30", state.resetPriorityUrgentSeconds, state.resetPriorityJitterSeconds)
	}
	if state.resetPriorityFallback != "round-robin" {
		t.Fatalf("reset-priority fallback = %q, want round-robin", state.resetPriorityFallback)
	}
	if _, ok := newRoutingSelector(state).(*coreauth.ResetPrioritySelector); !ok {
		t.Fatalf("selector type = %T, want *auth.ResetPrioritySelector", newRoutingSelector(state))
	}
}

func TestResetPriorityRoutingSelectorDefaults(t *testing.T) {
	state := normalizedRoutingRuntimeState(&internalconfig.Config{
		Routing: internalconfig.RoutingConfig{
			Strategy:              "reset-priority",
			ResetPriorityFallback: "unsupported",
		},
	})
	if state.resetPriorityUrgentSeconds != 900 || state.resetPriorityJitterSeconds != 60 {
		t.Fatalf("default reset-priority windows = %d/%d, want 900/60", state.resetPriorityUrgentSeconds, state.resetPriorityJitterSeconds)
	}
	if state.resetPriorityFallback != "weighted" {
		t.Fatalf("default reset-priority fallback = %q, want weighted", state.resetPriorityFallback)
	}
}
