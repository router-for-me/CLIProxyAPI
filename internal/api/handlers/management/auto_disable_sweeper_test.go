package management

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// reenableStub is a minimal stand-in for the sweeper's needed store surface
// (ReenableExpiredAutoDisabled). It returns configured count/err outcomes and
// records every call so tests can assert cadence and ordering.
type reenableStub struct {
	mu    sync.Mutex
	calls int
	count int64
	err   error
	block chan struct{} // when non-nil, each call waits here (in-flight tick control)
}

func (s *reenableStub) ReenableExpiredAutoDisabled(_ context.Context) (int64, error) {
	s.mu.Lock()
	s.calls++
	count, err := s.count, s.err
	s.mu.Unlock()
	if s.block != nil {
		<-s.block
	}
	return count, err
}

func (s *reenableStub) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// atomicRenderCounter counts re-render invocations without any blocking so
// assertions never deadlock against the sweeper goroutine.
type atomicRenderCounter struct {
	mu    sync.Mutex
	count int
}

func (r *atomicRenderCounter) fn() {
	r.mu.Lock()
	r.count++
	r.mu.Unlock()
}

func (r *atomicRenderCounter) renderCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count
}

// TestAutoDisableSweeper_TickReenabledRenders verifies the happy path: a tick
// that re-enables rows (count>0) triggers exactly one render.
func TestAutoDisableSweeper_TickReenabledRenders(t *testing.T) {
	stub := &reenableStub{count: 3}
	rc := &atomicRenderCounter{}
	s := newAutoDisableSweeper(stub, rc.fn, time.Hour) // long interval: drive sweep() directly

	s.sweep()
	if rc.renderCount() != 1 {
		t.Fatalf("render count = %d, want 1", rc.renderCount())
	}
	if stub.callCount() != 1 {
		t.Fatalf("store call count = %d, want 1", stub.callCount())
	}

	// A second tick re-enabling more rows renders again.
	s.sweep()
	if rc.renderCount() != 2 {
		t.Fatalf("render count after second tick = %d, want 2", rc.renderCount())
	}
}

// TestAutoDisableSweeper_TickNoRowsNoRender verifies that a tick which finds
// nothing to re-enable (count=0) does not render (the config is already
// correct; nothing changed).
func TestAutoDisableSweeper_TickNoRowsNoRender(t *testing.T) {
	stub := &reenableStub{count: 0}
	rc := &atomicRenderCounter{}
	s := newAutoDisableSweeper(stub, rc.fn, time.Hour)

	s.sweep()
	if rc.renderCount() != 0 {
		t.Fatalf("render count = %d, want 0 when no rows re-enabled", rc.renderCount())
	}
	if stub.callCount() != 1 {
		t.Fatalf("store call count = %d, want 1 (sweep still queried)", stub.callCount())
	}
}

// TestAutoDisableSweeper_StoreErrorNoRender verifies a failed re-enable is
// logged, never renders, does not panic, and the loop continues to the next
// tick (the error path must not consume/crash the goroutine).
func TestAutoDisableSweeper_StoreErrorNoRender(t *testing.T) {
	stub := &reenableStub{count: 5, err: errors.New("db down")}
	rc := &atomicRenderCounter{}

	// Short interval so Start exercises the real loop across an error tick,
	// then flip the stub to success and observe a later tick still running.
	s := newAutoDisableSweeper(stub, rc.fn, 5*time.Millisecond)
	s.Start()
	defer s.Stop()

	deadline := time.Now().Add(2 * time.Second)
	for stub.callCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if stub.callCount() == 0 {
		t.Fatal("sweeper never invoked the store")
	}
	if rc.renderCount() != 0 {
		t.Fatalf("render count = %d, want 0 on store error", rc.renderCount())
	}
	// The loop must survive the error and tick again.
	stub.mu.Lock()
	stub.err = nil
	stub.mu.Unlock()
	seenBefore := stub.callCount()
	deadline = time.Now().Add(2 * time.Second)
	for stub.callCount() <= seenBefore && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if stub.callCount() <= seenBefore {
		t.Fatal("sweeper stopped ticking after a store error")
	}
}

// TestAutoDisableSweeper_NilPGNoop verifies a nil pg store makes every tick a
// silent no-op: no panic, no render, and a started sweeper keeps running
// (parked) until stopped.
func TestAutoDisableSweeper_NilPGNoop(t *testing.T) {
	rc := &atomicRenderCounter{}
	// render is nil too (the nil-handler site passes nil both); with a nil pg
	// the tick returns before render can ever be consulted.
	s := NewAutoDisableSweeper(nil, rc.fn)
	s.sweep()
	s.sweep()
	if rc.renderCount() != 0 {
		t.Fatalf("render count = %d, want 0 with nil pg", rc.renderCount())
	}
	// A started nil-pg sweeper must Start and Stop cleanly.
	s.Start()
	s.Stop()
}

// TestAutoDisableSweeper_StopTerminates verifies Stop closes the stopped
// channel promptly (the goroutine exits) and that Stop is idempotent + safe
// before Start.
func TestAutoDisableSweeper_StopTerminates(t *testing.T) {
	stub := &reenableStub{count: 1}
	rc := &atomicRenderCounter{}
	s := newAutoDisableSweeper(stub, rc.fn, time.Millisecond)

	// Stop before Start: no-op, no panic, must not leak.
	s.Stop()

	s.Start()
	// Let it run a few ticks so there is something to stop mid-flight.
	time.Sleep(20 * time.Millisecond)

	stopped := make(chan struct{})
	go func() {
		s.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
		// good
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not terminate the sweeper goroutine within timeout")
	}
	// Idempotent second Stop must not deadlock.
	done := make(chan struct{})
	go func() { s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("second Stop did not return (Stop not idempotent)")
	}
}

// TestAutoDisableSweeper_NilReceiverSweep verifies sweep() on a nil receiver is
// a safe no-op (the panic guard is also protection for a nil *AutoDisableSweeper).
func TestAutoDisableSweeper_NilReceiverSweep(t *testing.T) {
	var s *AutoDisableSweeper
	s.sweep() // must not panic
	s.Start()
	s.Stop()
}
