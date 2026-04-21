package auth

import (
	"context"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

func TestNormalizeRoutingGroupStrategies(t *testing.T) {
	t.Parallel()

	got := NormalizeRoutingGroupStrategies(map[string]string{
		" MiniMax ": "SF",
		"":          "fill-first",
		"bad":       "unknown",
		"Kimi":      "ff",
	})

	if len(got) != 2 {
		t.Fatalf("NormalizeRoutingGroupStrategies() len = %d, want 2", len(got))
	}
	if got["minimax"] != RoutingStrategySequentialFill {
		t.Fatalf("NormalizeRoutingGroupStrategies().minimax = %q, want %q", got["minimax"], RoutingStrategySequentialFill)
	}
	if got["kimi"] != RoutingStrategyFillFirst {
		t.Fatalf("NormalizeRoutingGroupStrategies().kimi = %q, want %q", got["kimi"], RoutingStrategyFillFirst)
	}
}

func TestManagerSelectorForAuths_UsesRoutingGroupOverride(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.SetConfig(&internalconfig.Config{
		Routing: internalconfig.RoutingConfig{
			GroupStrategies: map[string]string{
				"minimax": "fill-first",
			},
		},
	})

	auths := []*Auth{
		{ID: "b", Provider: "claude", Attributes: map[string]string{"routing_group": "MiniMax"}},
		{ID: "a", Provider: "claude", Attributes: map[string]string{"routing_group": "MiniMax"}},
	}

	selector := manager.selectorForAuths(auths)
	if _, ok := selector.(*FillFirstSelector); !ok {
		t.Fatalf("selectorForAuths() = %T, want *FillFirstSelector", selector)
	}
	if manager.useSchedulerFastPath() {
		t.Fatal("useSchedulerFastPath() = true, want false when routing group strategies are configured")
	}

	got1, err := selector.Pick(context.Background(), "claude", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() first error = %v", err)
	}
	got2, err := selector.Pick(context.Background(), "claude", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() second error = %v", err)
	}
	if got1 == nil || got2 == nil {
		t.Fatalf("Pick() returned nil auths: first=%v second=%v", got1, got2)
	}
	if got1.ID != "a" || got2.ID != "a" {
		t.Fatalf("FillFirst override picked %q and %q, want both %q", got1.ID, got2.ID, "a")
	}
}

func TestManagerSelectorForAuths_CachesSequentialFillPerGroup(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.SetConfig(&internalconfig.Config{
		Routing: internalconfig.RoutingConfig{
			GroupStrategies: map[string]string{
				"minimax": "sf",
			},
		},
	})

	auths := []*Auth{
		{ID: "a", Provider: "claude", Attributes: map[string]string{"routing_group": "MiniMax"}},
		{ID: "b", Provider: "claude", Attributes: map[string]string{"routing_group": "MiniMax"}},
		{ID: "c", Provider: "claude", Attributes: map[string]string{"routing_group": "MiniMax"}},
	}

	selector1 := manager.selectorForAuths(auths)
	selector2 := manager.selectorForAuths(auths)
	if selector1 != selector2 {
		t.Fatal("selectorForAuths() did not reuse the cached selector for the same routing group")
	}

	got1, err := selector1.Pick(context.Background(), "claude", "gemini-3.1-flash", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() first error = %v", err)
	}
	got2, err := selector2.Pick(context.Background(), "claude", "gemini-3.1-flash", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() second error = %v", err)
	}
	if got1 == nil || got2 == nil {
		t.Fatalf("Pick() returned nil auths: first=%v second=%v", got1, got2)
	}
	if got1.ID != got2.ID {
		t.Fatalf("SequentialFill override did not stick: first=%q second=%q", got1.ID, got2.ID)
	}
}

func TestManagerSelectorForAuths_FallsBackToGlobalSelectorForMixedGroups(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.SetConfig(&internalconfig.Config{
		Routing: internalconfig.RoutingConfig{
			GroupStrategies: map[string]string{
				"minimax": "fill-first",
				"kimi":    "sf",
			},
		},
	})

	auths := []*Auth{
		{ID: "a", Provider: "claude", Attributes: map[string]string{"routing_group": "MiniMax"}},
		{ID: "b", Provider: "openai-compatibility", Attributes: map[string]string{"routing_group": "kimi"}},
	}

	selector := manager.selectorForAuths(auths)
	if selector != manager.selector {
		t.Fatalf("selectorForAuths() = %T, want fallback global selector %T", selector, manager.selector)
	}
}
