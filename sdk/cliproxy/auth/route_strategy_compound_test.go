package auth

import (
	"context"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// TestManager_PickNextMixed_CompoundFailoverRotatesAcrossProviders pins the
// audit finding B1: on a compound-key route ("claude:42", "gemini:7"), the
// per-model strategy "failover" must rotate the starting provider across
// successive picks. Before the fix, pickNextMixedCompoundRoute always started
// from providers[0], so every pick landed on the same first route entry and the
// failover strategy was silently ignored.
func TestManager_PickNextMixed_CompoundFailoverRotatesAcrossProviders(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["claude"] = schedulerTestExecutor{}
	manager.executors["gemini"] = schedulerTestExecutor{}
	for _, auth := range []*Auth{
		{ID: "a-claude-row", Provider: "claude", Attributes: map[string]string{"provider_key": "claude:42"}},
		{ID: "a-gemini-row", Provider: "gemini", Attributes: map[string]string{"provider_key": "gemini:7"}},
	} {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", auth.ID, errRegister)
		}
	}

	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.RouteStrategyMetadataKey: "failover",
	}}
	providers := []string{"claude:42", "gemini:7"}

	first, _, _, errFirst := manager.pickNextMixed(context.Background(), providers, "", opts, nil)
	if errFirst != nil {
		t.Fatalf("pickNextMixed() first error = %v", errFirst)
	}
	if first == nil {
		t.Fatal("pickNextMixed() first auth = nil")
	}
	second, _, _, errSecond := manager.pickNextMixed(context.Background(), providers, "", opts, nil)
	if errSecond != nil {
		t.Fatalf("pickNextMixed() second error = %v", errSecond)
	}
	if second == nil {
		t.Fatal("pickNextMixed() second auth = nil")
	}
	if first.ID == second.ID {
		t.Fatalf("pickNextMixed(failover) returned the same provider twice in a row (%s); expected rotation across the route's compound providers", first.ID)
	}
}

// TestManager_PickNextMixed_CompoundPrioritySticksToFirstPinnedRow guards the
// other half of B1: with strategy "priority" on a compound-key route, the
// first route entry keeps serving every pick (fill-first), and rotation must
// not kick in.
func TestManager_PickNextMixed_CompoundPrioritySticksToFirstPinnedRow(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["claude"] = schedulerTestExecutor{}
	manager.executors["gemini"] = schedulerTestExecutor{}
	for _, auth := range []*Auth{
		{ID: "a-claude-row", Provider: "claude", Attributes: map[string]string{"provider_key": "claude:42"}},
		{ID: "a-gemini-row", Provider: "gemini", Attributes: map[string]string{"provider_key": "gemini:7"}},
	} {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", auth.ID, errRegister)
		}
	}

	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.RouteStrategyMetadataKey: "priority",
	}}
	providers := []string{"claude:42", "gemini:7"}

	for i := 0; i < 3; i++ {
		got, _, _, errPick := manager.pickNextMixed(context.Background(), providers, "", opts, nil)
		if errPick != nil {
			t.Fatalf("pickNextMixed() pick %d error = %v", i, errPick)
		}
		if got == nil || got.ID != "a-claude-row" {
			t.Fatalf("pickNextMixed(priority) pick %d auth = %#v, want a-claude-row", i, got)
		}
	}
}

// TestManager_PickNextMixed_CompoundFailoverResumesRotationAfterFailure pins
// the failover semantics: once the first route entry is exhausted (tried), the
// next pick moves to the second entry, and after the first entry recovers the
// rotation continues from where it left off instead of resetting to index 0.
func TestManager_PickNextMixed_CompoundFailoverResumesRotationAfterFailure(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["claude"] = schedulerTestExecutor{}
	manager.executors["gemini"] = schedulerTestExecutor{}
	for _, auth := range []*Auth{
		{ID: "a-claude-row", Provider: "claude", Attributes: map[string]string{"provider_key": "claude:42"}},
		{ID: "a-gemini-row", Provider: "gemini", Attributes: map[string]string{"provider_key": "gemini:7"}},
	} {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", auth.ID, errRegister)
		}
	}

	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.RouteStrategyMetadataKey: "failover",
	}}
	providers := []string{"claude:42", "gemini:7"}

	// First pick starts at index 0 (claude).
	first, _, _, errFirst := manager.pickNextMixed(context.Background(), providers, "", opts, nil)
	if errFirst != nil {
		t.Fatalf("pickNextMixed() first error = %v", errFirst)
	}
	if first == nil || first.ID != "a-claude-row" {
		t.Fatalf("pickNextMixed(failover) first auth = %#v, want a-claude-row", first)
	}

	// With claude marked tried, the pick fails over to gemini.
	tried := map[string]struct{}{first.ID: {}}
	second, _, _, errSecond := manager.pickNextMixed(context.Background(), providers, "", opts, tried)
	if errSecond != nil {
		t.Fatalf("pickNextMixed() second error = %v", errSecond)
	}
	if second == nil || second.ID != "a-gemini-row" {
		t.Fatalf("pickNextMixed(failover) failover auth = %#v, want a-gemini-row", second)
	}

	// Rotation must continue: the next pick (nothing tried) resumes at the
	// entry after the one that last served, not back at index 0.
	third, _, _, errThird := manager.pickNextMixed(context.Background(), providers, "", opts, nil)
	if errThird != nil {
		t.Fatalf("pickNextMixed() third error = %v", errThird)
	}
	if third == nil || third.ID != "a-claude-row" {
		t.Fatalf("pickNextMixed(failover) resumed auth = %#v, want a-claude-row (rotation continues, wraps to first)", third)
	}
}
