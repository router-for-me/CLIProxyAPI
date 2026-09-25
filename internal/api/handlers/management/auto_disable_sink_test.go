package management

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// autoDisableStub is a minimal stand-in for store.UpstreamProviderStore's
// SetEntryAutoDisabled primitive. It records every call (event + code) and
// returns configured changed/err outcomes; writeDone lets tests wait for the
// sink's async goroutine to finish its DB write before asserting on renders.
type autoDisableStub struct {
	mu        sync.Mutex
	calls     []coreauth.AutoDisableEvent
	changed   bool
	err       error
	writeDone chan struct{}
}

func (s *autoDisableStub) SetEntryAutoDisabled(_ context.Context, entryID int64, code string) (bool, error) {
	s.mu.Lock()
	s.calls = append(s.calls, coreauth.AutoDisableEvent{EntryID: entryID, Code: code})
	changed, err := s.changed, s.err
	s.mu.Unlock()
	if s.writeDone != nil {
		select {
		case s.writeDone <- struct{}{}:
		default:
		}
	}
	return changed, err
}

func (s *autoDisableStub) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

// renderCounter counts re-render invocations and signals them on done so tests
// can synchronize on the sink's fire-and-forget goroutine.
type renderCounter struct {
	mu    sync.Mutex
	count int
	done  chan struct{}
}

func (r *renderCounter) fn() {
	r.mu.Lock()
	r.count++
	r.mu.Unlock()
	if r.done != nil {
		select {
		case r.done <- struct{}{}:
		default:
		}
	}
}

func (r *renderCounter) renderCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count
}

func newAutoDisableSinkHarness() (*autoDisableStub, *renderCounter, coreauth.AutoDisableSink) {
	stub := &autoDisableStub{writeDone: make(chan struct{}, 4)}
	rc := &renderCounter{done: make(chan struct{}, 4)}
	return stub, rc, newAutoDisableSink(stub, rc.fn)
}

func waitWrite(t *testing.T, stub *autoDisableStub) {
	t.Helper()
	select {
	case <-stub.writeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("no store write within timeout")
	}
}

func waitRender(t *testing.T, rc *renderCounter) {
	t.Helper()
	select {
	case <-rc.done:
	case <-time.After(5 * time.Second):
		t.Fatal("no re-render within timeout")
	}
}

// TestAutoDisableSink_PersistAndRender verifies the happy path: a positively-
// attributed event persists the auto-disable flag with its code and then
// triggers the re-render that drops the entry from selection.
func TestAutoDisableSink_PersistAndRender(t *testing.T) {
	stub, rc, sink := newAutoDisableSinkHarness()
	stub.changed = true
	sink(context.Background(), coreauth.AutoDisableEvent{Provider: "claude", EntryID: 7, Code: "401"})

	waitRender(t, rc)
	if rc.renderCount() != 1 {
		t.Fatalf("render count = %d, want 1", rc.renderCount())
	}
	stub.mu.Lock()
	calls := append([]coreauth.AutoDisableEvent(nil), stub.calls...)
	stub.mu.Unlock()
	if len(calls) != 1 || calls[0].EntryID != 7 || calls[0].Code != "401" {
		t.Fatalf("store calls = %+v, want single 401 write for entry 7", calls)
	}
}

// TestAutoDisableSink_InvalidEntryID verifies an unattributable event (no PG
// id) is skipped entirely: no store write, no re-render, no panic.
func TestAutoDisableSink_InvalidEntryID(t *testing.T) {
	stub, rc, sink := newAutoDisableSinkHarness()
	stub.changed = true

	sink(context.Background(), coreauth.AutoDisableEvent{Provider: "gemini", EntryID: 0, Code: "403"})
	sink(context.Background(), coreauth.AutoDisableEvent{Provider: "codex", EntryID: -3, Code: "500"})
	// The invalid path never spawns a goroutine; a short settle proves nothing
	// else fired.
	time.Sleep(50 * time.Millisecond)
	if stub.callCount() != 0 {
		t.Fatalf("store calls for EntryID<=0 = %d, want 0", stub.callCount())
	}
	if rc.renderCount() != 0 {
		t.Fatalf("render called for EntryID<=0: %d", rc.renderCount())
	}
}

// TestAutoDisableSink_StoreErrorNoRender verifies a failed persistence write
// is logged and never reaches the re-render.
func TestAutoDisableSink_StoreErrorNoRender(t *testing.T) {
	stub, rc, sink := newAutoDisableSinkHarness()
	stub.changed = true
	stub.err = errors.New("db down")

	sink(context.Background(), coreauth.AutoDisableEvent{Provider: "claude", EntryID: 9, Code: "500"})
	waitWrite(t, stub)
	select {
	case <-rc.done:
		t.Fatal("re-render happened despite store error")
	case <-time.After(100 * time.Millisecond):
	}
	if rc.renderCount() != 0 {
		t.Fatalf("render count = %d, want 0 on store error", rc.renderCount())
	}
}

// TestAutoDisableSink_NoChangeNoRender verifies the idempotent/manual-disable
// path: the store confirms nothing changed (already auto-disabled or manually
// disabled), so the config is already correct and no re-render fires.
func TestAutoDisableSink_NoChangeNoRender(t *testing.T) {
	stub, rc, sink := newAutoDisableSinkHarness()
	stub.changed = false

	sink(context.Background(), coreauth.AutoDisableEvent{Provider: "claude", EntryID: 11, Code: "401"})
	waitWrite(t, stub)
	select {
	case <-rc.done:
		t.Fatal("re-render happened though nothing changed")
	case <-time.After(100 * time.Millisecond):
	}
	if rc.renderCount() != 0 {
		t.Fatalf("render count = %d, want 0 when changed=false", rc.renderCount())
	}
}

// TestAutoDisableSink_HandlerWithoutStore verifies the Handler-level wiring:
// with no PG store attached, AutoDisableSink() returns nil so the auth
// manager's SetAutoDisableSink is a no-op.
func TestAutoDisableSink_HandlerWithoutStore(t *testing.T) {
	h := &Handler{}
	if sink := h.AutoDisableSink(); sink != nil {
		t.Fatal("AutoDisableSink() with nil store = non-nil, want nil (PG not configured)")
	}
}
