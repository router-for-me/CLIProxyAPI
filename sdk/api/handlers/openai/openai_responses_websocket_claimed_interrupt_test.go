package openai

import (
	"testing"
)

// Every frame deliver claims must stay answerable: deactivating or ending the
// turn may stop new claims, but must never silently drop a frame that was
// already accepted. The forward loop relies on the queued frame surviving
// end() so it can be answered instead of timing the client out.
func TestResponsesLocalInterruptRetainsClaimedFrameAfterEnd(t *testing.T) {
	local := newResponsesLocalInterrupt()
	local.begin(false)
	if !local.deliver([]byte(`{"type":"response.interrupt","response_id":"unknown"}`)) {
		t.Fatal("frame was not claimed while active")
	}
	local.deactivate()
	local.end()
	if local.deliver([]byte(`{"type":"response.interrupt","response_id":"unknown-2"}`)) {
		t.Fatal("frame claimed after end")
	}
	select {
	case frame := <-local.framesChan():
		if got := string(frame); got != `{"type":"response.interrupt","response_id":"unknown"}` {
			t.Fatalf("retained frame = %s", got)
		}
	default:
		t.Fatal("claimed frame was silently dropped by end()")
	}
	// The claim must stay spent: nothing is queued twice.
	select {
	case frame := <-local.framesChan():
		t.Fatalf("unexpected second frame: %s", frame)
	default:
	}
}

// A full queue must reject a second frame instead of claiming it: claiming
// without retaining it would leave the client waiting for a local answer
// that only ever addresses the first frame.
func TestResponsesLocalInterruptRejectsWhenQueueFull(t *testing.T) {
	local := newResponsesLocalInterrupt()
	local.begin(false)
	if !local.deliver([]byte(`{"type":"response.interrupt","response_id":"first"}`)) {
		t.Fatal("first frame was not claimed")
	}
	if local.deliver([]byte(`{"type":"response.interrupt","response_id":"second"}`)) {
		t.Fatal("second frame claimed while queue was full")
	}
	frame := local.takeClaimed()
	if got := string(frame); got != `{"type":"response.interrupt","response_id":"first"}` {
		t.Fatalf("claimed frame = %s", got)
	}
	if frame := local.takeClaimed(); frame != nil {
		t.Fatalf("unexpected second claimed frame: %s", frame)
	}
}

// Turn ownership must be part of claim activation (begin sets both under
// one lock): if begin ran in two steps, a reader could claim a frame for a
// websocket-owned turn in the gap and the forward loop would answer it with
// a fabricated local incomplete. This test pins the single-step API and
// its visible outcome; the one-critical-section guarantee is by construction.
func TestResponsesLocalInterruptWebsocketOwnershipIsAtomic(t *testing.T) {
	local := newResponsesLocalInterrupt()
	local.begin(true)
	if local.deliver([]byte(`{"type":"response.interrupt","response_id":"r1"}`)) {
		t.Fatal("frame claimed for a websocket-owned turn")
	}
	local.endWebsocketTurn()
	local.begin(false)
	if !local.deliver([]byte(`{"type":"response.interrupt","response_id":"r1"}`)) {
		t.Fatal("frame not claimed for an HTTP turn")
	}
}
