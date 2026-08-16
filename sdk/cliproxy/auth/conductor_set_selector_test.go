package auth

import (
	"context"
	"sync/atomic"
	"testing"

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

// Replacing a selector must release resources held by the previous one;
// otherwise every routing config change leaks the replaced selector's
// background cleanup goroutine and cache.
func TestManagerSetSelectorStopsReplacedStoppableSelector(t *testing.T) {
	t.Parallel()

	stub := &stubStoppableSelector{}
	manager := NewManager(nil, stub, nil)

	manager.SetSelector(&RoundRobinSelector{})
	if got := stub.stopped.Load(); got != 1 {
		t.Fatalf("replaced selector Stop() calls = %d, want 1", got)
	}
}

// Re-setting the same selector instance must not stop it: the selector is
// still the active one and its resources must stay alive.
func TestManagerSetSelectorSameInstanceIsNotStopped(t *testing.T) {
	t.Parallel()

	stub := &stubStoppableSelector{}
	manager := NewManager(nil, stub, nil)

	manager.SetSelector(stub)
	if got := stub.stopped.Load(); got != 0 {
		t.Fatalf("re-set same selector Stop() calls = %d, want 0", got)
	}
	if current := manager.Selector(); current != Selector(stub) {
		t.Fatalf("Selector() = %T, want the re-set stub instance", current)
	}
}

// uncomparableStoppableSelector carries a slice, so values of this type panic
// on a direct == interface comparison. The public Selector interface does not
// require comparable implementations, so SetSelector must tolerate them.
type uncomparableStoppableSelector struct {
	tags    []string
	stopped *atomic.Int32
}

func (s uncomparableStoppableSelector) Pick(_ context.Context, _, _ string, _ cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	if len(auths) == 0 {
		return nil, &Error{Code: "auth_not_found", Message: "no auth candidates"}
	}
	return auths[0], nil
}

func (s uncomparableStoppableSelector) Stop() {
	s.stopped.Add(1)
}

// Replacing an uncomparable custom selector with another value of the same
// type must not panic: a bare previous != selector comparison panics exactly
// in this case. The replaced instance is still stopped because identity
// cannot be established for uncomparable types.
func TestManagerSetSelectorUncomparableSelectorDoesNotPanic(t *testing.T) {
	t.Parallel()

	var stopped atomic.Int32
	manager := NewManager(nil, uncomparableStoppableSelector{stopped: &stopped}, nil)

	manager.SetSelector(uncomparableStoppableSelector{stopped: &atomic.Int32{}})
	if got := stopped.Load(); got != 1 {
		t.Fatalf("replaced uncomparable selector Stop() calls = %d, want 1", got)
	}
}

// Enabling affinity by wrapping the current selector via
// NewSessionAffinitySelector(previous) keeps the previous selector in service
// as the fallback, so SetSelector must not stop it. The same holds when the
// fallback is nested inside another affinity wrapper.
func TestManagerSetSelectorDoesNotStopRetainedFallback(t *testing.T) {
	t.Parallel()

	stub := &stubStoppableSelector{}
	manager := NewManager(nil, stub, nil)

	direct := NewSessionAffinitySelector(stub)
	defer direct.Stop()
	manager.SetSelector(direct)
	if got := stub.stopped.Load(); got != 0 {
		t.Fatalf("retained fallback selector Stop() calls = %d, want 0", got)
	}

	nested := NewSessionAffinitySelector(direct)
	defer nested.Stop()
	manager.SetSelector(nested)
	if got := stub.stopped.Load(); got != 0 {
		t.Fatalf("nested retained fallback selector Stop() calls = %d, want 0", got)
	}
	select {
	case <-direct.cache.stopCh:
		t.Fatalf("direct affinity wrapper was stopped while still serving as nested fallback")
	default:
	}
}
