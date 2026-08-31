package auth

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// TestSetSelectorStopsPreviousAffinitySelector pins audit finding P7: swapping
// the selector leaked the outgoing SessionAffinitySelector's SessionCache
// cleanup goroutine and its map forever. Manager.SetSelector must stop the
// outgoing selector when it holds resources and is being replaced by a
// different instance.
func TestSetSelectorStopsPreviousAffinitySelector(t *testing.T) {
	ctx := context.Background()
	provider := "selector-swap-provider"
	model := "selector-swap-model"

	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(schedulerTestExecutor{provider: provider})
	auth := &Auth{ID: provider + "-a", Provider: provider, Status: StatusActive}
	if _, errRegister := manager.Register(WithSkipPersist(ctx), auth); errRegister != nil {
		t.Fatalf("Register(): %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

	first := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Hour,
	})
	manager.SetSelector(first)

	// Establish a binding in the first selector's cache so its state is
	// observable after the swap.
	sessionOpts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.DerivedSessionIDMetadataKey: "swap-session",
	}}
	picked, _, errPick := manager.pickNext(ctx, provider, model, sessionOpts, nil)
	if errPick != nil {
		t.Fatalf("pickNext(): %v", errPick)
	}
	if picked == nil || picked.ID != auth.ID {
		t.Fatalf("pickNext() auth = %#v, want %s", picked, auth.ID)
	}
	snapshots := first.cache.Snapshot()
	if len(snapshots) != 1 {
		t.Fatalf("first selector snapshot = %d bindings, want 1", len(snapshots))
	}

	// Swap in a different selector as applyManagerConfig does on every
	// routing.* config change.
	manager.SetSelector(NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Hour,
	}))

	// The outgoing selector must have been stopped: its cache cleanup loop
	// terminated and no longer observes the binding it held.
	select {
	case <-first.cache.stopCh:
	case <-time.After(2 * time.Second):
		t.Fatal("outgoing SessionAffinitySelector was not stopped on swap; its cleanup goroutine and cache map leak")
	}
}

// TestSetSelectorKeepsSharedAffinitySelector guards the fix's ownership rule:
// re-assigning the same selector instance (a caller sharing one selector) must
// not stop it.
func TestSetSelectorKeepsSharedAffinitySelector(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	affinity := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Hour,
	})
	manager.SetSelector(affinity)
	// Same instance, second assignment.
	manager.SetSelector(affinity)

	select {
	case <-affinity.cache.stopCh:
		t.Fatal("re-assigning the same selector instance stopped it; shared selectors must survive re-assignment")
	default:
	}
}
