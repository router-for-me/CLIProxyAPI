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

// waitForStopCount polls until counter reaches want, tolerating the
// asynchronous, drain-aware stopping performed by SetSelector.
func waitForStopCount(t *testing.T, counter *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for counter.Load() != want {
		if time.Now().After(deadline) {
			t.Fatalf("Stop() calls = %d, want %d", counter.Load(), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
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

// Replacing a selector must release resources held by the previous one;
// otherwise every routing config change leaks the replaced selector's
// background cleanup goroutine and cache.
func TestManagerSetSelectorStopsReplacedStoppableSelector(t *testing.T) {
	t.Parallel()

	stub := &stubStoppableSelector{}
	manager := NewManager(nil, stub, nil)

	manager.SetSelector(&RoundRobinSelector{})
	waitForStopCount(t, &stub.stopped, 1)
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
// in this case. Because identity cannot be established, the previous value is
// conservatively kept alive — it may share pointer-backed state with the
// active one, so stopping it could break routing.
func TestManagerSetSelectorUncomparableSelectorDoesNotPanic(t *testing.T) {
	t.Parallel()

	var stopped atomic.Int32
	manager := NewManager(nil, uncomparableStoppableSelector{stopped: &stopped}, nil)

	manager.SetSelector(uncomparableStoppableSelector{stopped: &atomic.Int32{}})
	if got := stopped.Load(); got != 0 {
		t.Fatalf("ambiguous-identity selector Stop() calls = %d, want 0 (conservatively kept alive)", got)
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

// blockingStoppableSelector blocks inside Pick until released, simulating an
// in-flight credential selection during a routing config change.
type blockingStoppableSelector struct {
	stopped atomic.Int32
	entered chan struct{}
	proceed chan struct{}
}

func (s *blockingStoppableSelector) Pick(_ context.Context, _, _ string, _ cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	close(s.entered)
	<-s.proceed
	if len(auths) == 0 {
		return nil, &Error{Code: "auth_not_found", Message: "no auth candidates"}
	}
	return auths[0], nil
}

func (s *blockingStoppableSelector) Stop() {
	s.stopped.Add(1)
}

// Pick paths release m.mu before invoking the selector, so a hot reload must
// not stop the replaced selector while one of its Pick calls is still in
// flight; the stop happens once the in-flight pick has drained.
func TestManagerSetSelectorDefersStopUntilInFlightPickDrains(t *testing.T) {
	t.Parallel()

	stub := &blockingStoppableSelector{entered: make(chan struct{}), proceed: make(chan struct{})}
	manager := NewManager(nil, stub, nil)
	manager.RegisterExecutor(&replaceAwareExecutor{id: "blocking"})
	if _, err := manager.Register(context.Background(), &Auth{ID: "auth-a", Provider: "blocking"}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	pickDone := make(chan error, 1)
	go func() {
		// No route model: the auth carries no model list, so an explicit model
		// would be filtered out before the selector is ever invoked.
		_, errPick := manager.SelectAuth(context.Background(), "blocking", "", cliproxyexecutor.Options{})
		pickDone <- errPick
	}()
	<-stub.entered

	swapDone := make(chan struct{})
	go func() {
		manager.SetSelector(&RoundRobinSelector{})
		close(swapDone)
	}()
	select {
	case <-swapDone:
	case <-time.After(5 * time.Second):
		t.Fatal("SetSelector blocked while a pick was in flight")
	}

	// The in-flight pick is still parked, so the replaced selector must not
	// have been stopped yet. A short sleep gives a synchronous (broken)
	// implementation room to run.
	time.Sleep(50 * time.Millisecond)
	if got := stub.stopped.Load(); got != 0 {
		t.Fatalf("replaced selector Stop() calls = %d while a Pick was in flight, want 0", got)
	}

	close(stub.proceed)
	if errPick := <-pickDone; errPick != nil {
		t.Fatalf("SelectAuth() error = %v", errPick)
	}

	waitForStopCount(t, &stub.stopped, 1)
}

// Re-setting the same selector instance while one of its picks is in flight
// must preserve the in-flight tracking: a fresh generation would let a later
// replacement stop the selector while the original pick is still running.
func TestManagerSetSelectorSameInstanceKeepsInFlightTracking(t *testing.T) {
	t.Parallel()

	stub := &blockingStoppableSelector{entered: make(chan struct{}), proceed: make(chan struct{})}
	manager := NewManager(nil, stub, nil)
	manager.RegisterExecutor(&replaceAwareExecutor{id: "blocking"})
	if _, err := manager.Register(context.Background(), &Auth{ID: "auth-a", Provider: "blocking"}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	pickDone := make(chan error, 1)
	go func() {
		_, errPick := manager.SelectAuth(context.Background(), "blocking", "", cliproxyexecutor.Options{})
		pickDone <- errPick
	}()
	<-stub.entered

	// Same instance re-set mid-pick, then a real replacement. The replacement
	// must still wait for the pick that started before both swaps.
	manager.SetSelector(stub)
	manager.SetSelector(&RoundRobinSelector{})

	time.Sleep(50 * time.Millisecond)
	if got := stub.stopped.Load(); got != 0 {
		t.Fatalf("selector Stop() calls = %d while its Pick was still in flight, want 0", got)
	}

	close(stub.proceed)
	if errPick := <-pickDone; errPick != nil {
		t.Fatalf("SelectAuth() error = %v", errPick)
	}
	waitForStopCount(t, &stub.stopped, 1)
}

// A selector retained as an affinity fallback is parked, not stopped; once the
// wrapper chain that referenced it is retired and nothing references it
// anymore, it must eventually be stopped instead of leaking forever.
func TestManagerSetSelectorEventuallyStopsRetiredFallback(t *testing.T) {
	t.Parallel()

	stub := &stubStoppableSelector{}
	manager := NewManager(nil, stub, nil)

	direct := NewSessionAffinitySelector(stub)
	manager.SetSelector(direct)
	if got := stub.stopped.Load(); got != 0 {
		t.Fatalf("retained fallback selector Stop() calls = %d, want 0", got)
	}

	manager.SetSelector(&RoundRobinSelector{})
	waitForStopCount(t, &stub.stopped, 1)
	select {
	case <-direct.cache.stopCh:
	default:
		t.Fatal("retired affinity wrapper was not stopped")
	}
}

// A fallback shared by two affinity wrappers must survive the retirement of
// one wrapper while the other is still active.
func TestManagerSetSelectorSharedFallbackSurvivesWrapperRetirement(t *testing.T) {
	t.Parallel()

	stub := &stubStoppableSelector{}
	manager := NewManager(nil, stub, nil)

	first := NewSessionAffinitySelector(stub)
	manager.SetSelector(first)
	second := NewSessionAffinitySelector(stub)
	defer second.Stop()
	manager.SetSelector(second)

	// The first wrapper is retired, but the shared fallback still serves the
	// active second wrapper and must not be stopped.
	waitForChannelClosed(t, first.cache.stopCh, "first affinity wrapper was not stopped after retirement")
	if got := stub.stopped.Load(); got != 0 {
		t.Fatalf("shared fallback selector Stop() calls = %d while still referenced, want 0", got)
	}
}
