package auth

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type stubStoppableSelector struct {
	stopped atomic.Int32
}

func (s *stubStoppableSelector) Pick(_ context.Context, _, _ string, _ cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	if len(auths) == 0 {
		return nil, &Error{Code: "auth_not_found", Message: "no auth candidates"}
	}
	return auths[0], nil
}

func (s *stubStoppableSelector) Stop() {
	s.stopped.Add(1)
}

// waitForChannelClosed polls until ch is closed.
func waitForChannelClosed(t *testing.T, ch <-chan struct{}, message string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case <-ch:
			return
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal(message)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Replacing the built-in session affinity selector must release its resources;
// otherwise every routing config change leaks the replaced selector's
// background cache cleanup goroutine.
func TestManagerSetSelectorStopsReplacedAffinitySelector(t *testing.T) {
	t.Parallel()

	affinity := NewSessionAffinitySelector(&RoundRobinSelector{})
	manager := NewManager(nil, affinity, nil)

	manager.SetSelector(&RoundRobinSelector{})
	waitForChannelClosed(t, affinity.cache.stopCh, "replaced affinity selector was not stopped")
}

// Enabling affinity by wrapping the current selector via
// NewSessionAffinitySelector(previous) keeps the previous selector in service
// as the fallback, so SetSelector must not stop it.
func TestManagerSetSelectorDoesNotStopRetainedFallback(t *testing.T) {
	t.Parallel()

	inner := NewSessionAffinitySelector(&RoundRobinSelector{})
	defer inner.Stop()
	manager := NewManager(nil, inner, nil)

	outer := NewSessionAffinitySelector(inner)
	defer outer.Stop()
	manager.SetSelector(outer)

	select {
	case <-inner.cache.stopCh:
		t.Fatal("affinity selector retained as fallback was stopped")
	default:
	}
}

// Re-setting the same affinity selector instance must not stop it: the
// selector is still the active one and its resources must stay alive.
func TestManagerSetSelectorSameAffinityInstanceIsNotStopped(t *testing.T) {
	t.Parallel()

	affinity := NewSessionAffinitySelector(&RoundRobinSelector{})
	defer affinity.Stop()
	manager := NewManager(nil, affinity, nil)

	manager.SetSelector(affinity)
	select {
	case <-affinity.cache.stopCh:
		t.Fatal("re-set same affinity selector was stopped")
	default:
	}
	if current := manager.Selector(); current != Selector(affinity) {
		t.Fatalf("Selector() = %T, want the re-set affinity instance", current)
	}
}

// Custom selectors are owned by the SDK caller: SetSelector never stops them,
// even when they implement StoppableSelector. Only the built-in affinity
// selector is managed, because its Stop is idempotent and leaves Pick safe.
func TestManagerSetSelectorLeavesCustomSelectorLifecycleToCaller(t *testing.T) {
	t.Parallel()

	stub := &stubStoppableSelector{}
	manager := NewManager(nil, stub, nil)

	manager.SetSelector(&RoundRobinSelector{})
	if got := stub.stopped.Load(); got != 0 {
		t.Fatalf("custom selector Stop() calls = %d, want 0 (lifecycle owned by caller)", got)
	}
}
