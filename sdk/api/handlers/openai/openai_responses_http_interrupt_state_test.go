package openai

import (
	"fmt"
	"sync"
	"testing"
)

func TestResponsesLocalInterruptQueuedFrameCannotCrossTurns(t *testing.T) {
	local := newResponsesLocalInterrupt()
	local.begin()
	local.observeCreated("r1")
	old := []byte(`{"type":"response.interrupt","response_id":"r1"}`)
	if local.deliver(old) != responsesLocalInterruptQueued {
		t.Fatal("current interrupt was rejected")
	}
	queued := <-local.framesChan()
	// Completion wins after the consumer receives the frame but before it claims it.
	local.observeTerminal("r1")
	local.end()
	local.begin()
	local.observeCreated("r2")
	if got := local.claim(queued); got != "" {
		t.Fatalf("stale queued frame claimed %q", got)
	}
	if local.deliver(old) != responsesLocalInterruptAlreadyTerminal {
		t.Fatal("completed response interrupt was not idempotent")
	}
	current := []byte(`{"type":"response.interrupt","response_id":"r2"}`)
	if local.deliver(current) != responsesLocalInterruptQueued {
		t.Fatal("stale frame prevented a valid current interrupt")
	}
	if got := local.claim(<-local.framesChan()); got != "r2" {
		t.Fatalf("claimed %q, want current response", got)
	}
	if local.deliver(current) != responsesLocalInterruptAlreadyTerminal {
		t.Fatal("interrupted response was not recorded as terminal")
	}
	local.end()
}

func TestResponsesLocalInterruptCompletionRacesDelivery(t *testing.T) {
	for attempt := 0; attempt < 100; attempt++ {
		local := newResponsesLocalInterrupt()
		local.begin()
		local.observeCreated("r1")
		payload := []byte(`{"type":"response.interrupt","response_id":"r1"}`)
		start := make(chan struct{})
		var wait sync.WaitGroup
		wait.Add(2)
		go func() {
			defer wait.Done()
			<-start
			local.deliver(payload)
		}()
		go func() {
			defer wait.Done()
			<-start
			local.observeTerminal("r1")
		}()
		close(start)
		wait.Wait()
		if got := local.claim(payload); got != "" {
			t.Fatalf("attempt %d: completed response was claimed as %q", attempt, got)
		}
		select {
		case <-local.framesChan():
			t.Fatalf("attempt %d: completion left a queued frame", attempt)
		default:
		}
		local.end()
	}
}

func TestResponsesLocalInterruptRejectsUnobservedResponses(t *testing.T) {
	local := newResponsesLocalInterrupt()
	local.begin()
	payload := []byte(`{"type":"response.interrupt","response_id":"r1"}`)
	if local.deliver(payload) != responsesLocalInterruptRejected {
		t.Fatal("interrupt before response.created was accepted")
	}
	local.observeCreated("r1")
	unknown := []byte(`{"type":"response.interrupt","response_id":"unknown"}`)
	if local.deliver(unknown) != responsesLocalInterruptRejected {
		t.Fatal("unknown response interrupt was accepted")
	}
	local.observeTerminal("")
	if local.deliver(payload) != responsesLocalInterruptAlreadyTerminal {
		t.Fatal("terminal event without ID did not use the observed response ID")
	}
	local.end()
}

func TestResponsesLocalInterruptBoundsTerminalHistory(t *testing.T) {
	for _, path := range []string{"completion", "end", "claim", "prewarm"} {
		t.Run(path, func(t *testing.T) {
			local := newResponsesLocalInterrupt()
			const historyLimit = 256
			const turns = 3*historyLimit + 17
			interrupt := func(index int) []byte {
				return []byte(fmt.Sprintf(`{"type":"response.interrupt","response_id":"r%d"}`, index))
			}
			for index := 0; index < turns; index++ {
				id := fmt.Sprintf("r%d", index)
				if path == "prewarm" {
					// Synthetic prewarms finish without starting an upstream turn.
					local.observeTerminal(id)
				} else {
					local.begin()
					local.observeCreated(id)
					switch path {
					case "completion":
						local.observeTerminal("")
					case "claim":
						if local.deliver(interrupt(index)) != responsesLocalInterruptQueued {
							t.Fatal("current interrupt was rejected")
						}
						if got := local.claim(<-local.framesChan()); got != id {
							t.Fatalf("claimed %q, want %q", got, id)
						}
					}
					local.end()
				}
				// Duplicate terminal events must not consume history slots.
				local.observeTerminal(id)
				if got := len(local.terminalIDs); got > historyLimit {
					t.Fatalf("turn %d: retained %d terminal IDs, limit %d", index, got, historyLimit)
				}
				if index == historyLimit-1 && local.deliver(interrupt(0)) != responsesLocalInterruptAlreadyTerminal {
					t.Fatal("history evicted a response before reaching capacity")
				}
			}
			// Repeated observations of an older retained ID must not evict recent IDs.
			for index := 0; index < 2*historyLimit; index++ {
				local.observeTerminal(fmt.Sprintf("r%d", turns-historyLimit))
			}
			local.begin()
			defer local.end()
			local.observeCreated("current")
			for index := 0; index < turns; index++ {
				want := responsesLocalInterruptRejected
				if index >= turns-historyLimit {
					want = responsesLocalInterruptAlreadyTerminal
				}
				if got := local.deliver(interrupt(index)); got != want {
					t.Fatalf("response r%d: delivery = %v, want %v", index, got, want)
				}
				if got := local.claim(interrupt(index)); got != "" {
					t.Fatalf("old response claimed %q", got)
				}
			}
			current := []byte(`{"type":"response.interrupt","response_id":"current"}`)
			if local.deliver(current) != responsesLocalInterruptQueued {
				t.Fatal("old interrupts prevented a valid current interrupt")
			}
			if got := local.claim(<-local.framesChan()); got != "current" {
				t.Fatalf("claimed %q, want current response", got)
			}
			if local.deliver(current) != responsesLocalInterruptAlreadyTerminal {
				t.Fatal("interrupted response was not recorded as terminal")
			}
		})
	}
}
