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

func TestResponsesLocalInterruptRetainsConnectionHistory(t *testing.T) {
	local := newResponsesLocalInterrupt()
	for index := 0; index < 256; index++ {
		local.begin()
		local.observeCreated(fmt.Sprintf("r%d", index))
		local.observeTerminal("")
		local.end()
	}
	local.begin()
	local.observeCreated("current")
	if local.deliver([]byte(`{"type":"response.interrupt","response_id":"r0"}`)) != responsesLocalInterruptAlreadyTerminal {
		t.Fatal("older completed response was forgotten during the same connection")
	}
	if got := local.claim([]byte(`{"type":"response.interrupt","response_id":"r0"}`)); got != "" {
		t.Fatalf("old response claimed %q", got)
	}
	local.end()
}
