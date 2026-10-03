package management

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// requestBodyStub is a minimal stand-in for the sweeper's needed store surface
// (DeleteRequestBodiesBefore). It returns configured count/err outcomes and
// records every call plus the cutoff it was handed so tests can assert both
// cadence and the retention horizon.
type requestBodyStub struct {
	mu     sync.Mutex
	calls  int
	count  int64
	err    error
	cutoff time.Time
	block  chan struct{} // when non-nil, each call waits here (in-flight tick control)
}

func (s *requestBodyStub) DeleteRequestBodiesBefore(_ context.Context, cutoff time.Time) (int64, error) {
	s.mu.Lock()
	s.calls++
	s.cutoff = cutoff
	count, err := s.count, s.err
	s.mu.Unlock()
	if s.block != nil {
		<-s.block
	}
	return count, err
}

func (s *requestBodyStub) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *requestBodyStub) lastCutoff() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cutoff
}

// TestRequestBodyRetentionSweeper_SweepDeletes verifies the happy path: a tick
// computes a ~30-day-old cutoff and forwards it to the store.
func TestRequestBodyRetentionSweeper_SweepDeletes(t *testing.T) {
	stub := &requestBodyStub{count: 7}
	s := newRequestBodyRetentionSweeper(stub, time.Hour) // long interval: drive sweep() directly

	s.sweep()
	if stub.callCount() != 1 {
		t.Fatalf("store call count = %d, want 1", stub.callCount())
	}
	want := time.Now().Add(-requestBodyRetention)
	got := stub.lastCutoff()
	if d := got.Sub(want); d < -5*time.Second || d > 5*time.Second {
		t.Fatalf("cutoff = %v, want ~%v (delta %v)", got, want, d)
	}
}

// TestRequestBodyRetentionSweeper_SweepNoRows verifies a tick that finds
// nothing to prune (count=0) still queries the store and does not panic.
func TestRequestBodyRetentionSweeper_SweepNoRows(t *testing.T) {
	stub := &requestBodyStub{count: 0}
	s := newRequestBodyRetentionSweeper(stub, time.Hour)

	s.sweep()
	if stub.callCount() != 1 {
		t.Fatalf("store call count = %d, want 1 (sweep still queried)", stub.callCount())
	}
}

// TestRequestBodyRetentionSweeper_StoreErrorSurvives verifies a failed prune is
// swallowed/logged, does not panic, and the loop keeps ticking after the error.
func TestRequestBodyRetentionSweeper_StoreErrorSurvives(t *testing.T) {
	stub := &requestBodyStub{count: 5, err: errors.New("db down")}

	s := newRequestBodyRetentionSweeper(stub, 5*time.Millisecond)
	s.Start()
	defer s.Stop()

	deadline := time.Now().Add(2 * time.Second)
	for stub.callCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if stub.callCount() == 0 {
		t.Fatal("sweeper never invoked the store")
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

// TestRequestBodyRetentionSweeper_NilPGNoop verifies a nil store makes every
// tick a silent no-op: no panic, and a started sweeper keeps running (parked)
// until stopped.
func TestRequestBodyRetentionSweeper_NilPGNoop(t *testing.T) {
	s := NewRequestBodyRetentionSweeper(nil)
	s.sweep()
	s.sweep()
	// A started nil-store sweeper must Start and Stop cleanly.
	s.Start()
	s.Stop()
}

// TestRequestBodyRetentionSweeper_StopTerminates verifies Stop closes the
// stopped channel promptly, is idempotent, and is safe before Start.
func TestRequestBodyRetentionSweeper_StopTerminates(t *testing.T) {
	stub := &requestBodyStub{count: 1}
	s := newRequestBodyRetentionSweeper(stub, time.Millisecond)

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

// TestRequestBodyRetentionSweeper_StopBeforeStartDoesNotLeak verifies the
// lifecycle regression: a Stop() before Start() must still close done, so a
// later Start() exits immediately instead of leaking a goroutine that the
// already-consumed stopOnce can never stop.
func TestRequestBodyRetentionSweeper_StopBeforeStartDoesNotLeak(t *testing.T) {
	stub := &requestBodyStub{count: 1}
	s := newRequestBodyRetentionSweeper(stub, time.Hour)

	s.Stop()  // before Start: no goroutine to wait on, but done must close
	s.Start() // run() observes the closed done and returns at once
	s.Stop()  // stopOnce already fired: must return without deadlocking

	select {
	case <-s.stopped:
		// good: the started goroutine exited
	case <-time.After(2 * time.Second):
		t.Fatal("goroutine leaked after Stop-before-Start lifecycle")
	}
}

// TestRequestBodyRetentionSweeper_NilReceiverSweep verifies sweep() on a nil
// receiver is a safe no-op (the panic guard also protects a nil receiver).
func TestRequestBodyRetentionSweeper_NilReceiverSweep(t *testing.T) {
	var s *RequestBodyRetentionSweeper
	s.sweep() // must not panic
	s.Start()
	s.Stop()
}
