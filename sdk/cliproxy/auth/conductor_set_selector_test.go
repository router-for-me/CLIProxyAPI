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
