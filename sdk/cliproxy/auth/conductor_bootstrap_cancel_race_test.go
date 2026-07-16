package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// When the client context is already cancelled AND a chunk carrying a real
// upstream status is already waiting, readStreamBootstrap must return that
// status (so the account is cooled) rather than the cancellation. A plain
// select would let Go pick at random and drop it.
func TestReadStreamBootstrap_PrefersReadyStatusOverCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancellation already visible before the read

	realErr := errors.New("HTTP 429 rate limited")
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Err: realErr}
	close(ch)

	_, _, err := readStreamBootstrap(ctx, ch)
	if !errors.Is(err, realErr) {
		t.Fatalf("readStreamBootstrap returned %v, want the real upstream error %v (a coincident cancel must not drop a ready status)", err, realErr)
	}
}

// A ready payload chunk is likewise preferred over the cancellation, so a
// bootstrap that actually produced a first byte is not misreported as cancelled.
func TestReadStreamBootstrap_PrefersReadyPayloadOverCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte("data: {}\n\n")}

	buffered, closed, err := readStreamBootstrap(ctx, ch)
	if err != nil {
		t.Fatalf("err = %v, want nil (a ready payload must win over the cancel)", err)
	}
	if closed {
		t.Fatalf("closed = true, want false")
	}
	if len(buffered) != 1 || len(buffered[0].Payload) == 0 {
		t.Fatalf("buffered payload not returned: %+v", buffered)
	}
}

// With nothing waiting on an already-cancelled context, readStreamBootstrap must
// report the cancellation immediately WITHOUT blocking — a late status is caught
// by drainAndCoolOnStatus on the caller's abandon path, and a silent, never-
// closing upstream must not hang the first-byte-timeout deadline here.
func TestReadStreamBootstrap_ReportsCancelWhenNothingReady(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ch := make(chan cliproxyexecutor.StreamChunk) // open, empty — nothing ready, never closes

	done := make(chan error, 1)
	go func() {
		_, _, err := readStreamBootstrap(ctx, ch)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled when no chunk is waiting", err)
		}
	case <-time.After(time.Second):
		t.Fatal("readStreamBootstrap blocked on a silent stream; it must return promptly on cancel")
	}
}

// When the producer winds down under a cancel WITHOUT any upstream status (just
// closes), readStreamBootstrap reports closed with no error; the caller then
// treats it as an empty stream (status 0), which MarkResult's guard skips.
func TestReadStreamBootstrap_ClosedEmptyUnderCancelReportsClosed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ch := make(chan cliproxyexecutor.StreamChunk)
	close(ch) // producer wound down, delivered nothing

	buffered, closed, err := readStreamBootstrap(ctx, ch)
	if err != nil {
		t.Fatalf("err = %v, want nil on a clean close under cancel", err)
	}
	if !closed {
		t.Fatalf("closed = false, want true")
	}
	if len(buffered) != 0 {
		t.Fatalf("buffered = %+v, want empty", buffered)
	}
}

// drainAndCoolOnStatus is the safety net for a real status that lands AFTER the
// bootstrap read already gave up (first-byte timeout or client cancel): draining
// the abandoned stream must still cool the account.
func TestDrainAndCoolOnStatus_CoolsOnLateStatus(t *testing.T) {
	m := NewManager(nil, nil, nil)
	auth := &Auth{ID: "late-429", Provider: "claude", Status: StatusActive}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("register: %v", err)
	}

	ch := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		time.Sleep(15 * time.Millisecond) // arrives after the read already gave up
		ch <- cliproxyexecutor.StreamChunk{Err: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "rate limited"}}
		close(ch)
	}()

	m.drainAndCoolOnStatus(ch, auth, "claude", "gpt-5.5")

	if !eventually(500*time.Millisecond, func() bool {
		st := markResultTestAuth(t, m, auth.ID).ModelStates["gpt-5.5"]
		return st != nil && !st.NextRetryAfter.IsZero()
	}) {
		t.Fatal("a late 429 drained from an abandoned attempt must cool the account")
	}
}

// drainAndCoolOnStatus must NOT cool when the abandoned stream carries no real
// status (e.g. only a cancellation error or a clean close).
func TestDrainAndCoolOnStatus_IgnoresStatuslessDrain(t *testing.T) {
	m := NewManager(nil, nil, nil)
	auth := &Auth{ID: "no-status", Provider: "claude", Status: StatusActive}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("register: %v", err)
	}

	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Err: context.Canceled} // status 0
	close(ch)

	m.drainAndCoolOnStatus(ch, auth, "claude", "gpt-5.5")

	time.Sleep(50 * time.Millisecond)
	got := markResultTestAuth(t, m, auth.ID)
	if got.Failed != 0 {
		t.Fatalf("Failed = %d, want 0 (a status-less drain must not penalize the account)", got.Failed)
	}
	if st := got.ModelStates["gpt-5.5"]; st != nil && !st.NextRetryAfter.IsZero() {
		t.Fatalf("a status-less drain cooled the account: NextRetryAfter=%v", st.NextRetryAfter)
	}
}

func eventually(within time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}
