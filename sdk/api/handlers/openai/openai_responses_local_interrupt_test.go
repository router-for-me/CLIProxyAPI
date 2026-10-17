package openai

import (
	"bytes"
	"fmt"
	"sync"
	"testing"
)

func TestResponsesLocalInterruptTracksObservedAndTerminalResponses(t *testing.T) {
	local := newResponsesLocalInterrupt()
	local.begin()
	if got := local.deliver([]byte(`{"type":"response.interrupt","response_id":"unobserved"}`)); got != responsesLocalInterruptRejected {
		t.Fatalf("interrupt before response.created = %d, want rejected", got)
	}
	local.observeResponseCreated("r1")
	if got := local.deliver([]byte(`{"type":"response.interrupt","response_id":"other"}`)); got != responsesLocalInterruptRejected {
		t.Fatalf("interrupt for another response = %d, want rejected", got)
	}
	interrupt := []byte(`{"type":"response.interrupt","response_id":"r1"}`)
	if got := local.deliver(interrupt); got != responsesLocalInterruptQueued {
		t.Fatalf("active response interrupt = %d, want queued", got)
	}
	if got := local.deliver(interrupt); got != responsesLocalInterruptQueued {
		t.Fatalf("duplicate active interrupt = %d, want handled", got)
	}
	if got := len(local.frames); got != 1 {
		t.Fatalf("queued frame signals = %d, want one", got)
	}
	<-local.frames
	if got := local.take(); !bytes.Equal(got, interrupt) {
		t.Fatalf("taken interrupt = %s, want %s", got, interrupt)
	}
	if got := local.take(); got != nil {
		t.Fatalf("duplicate interrupt payload = %s, want none", got)
	}
	if got := local.deliver(interrupt); got != responsesLocalInterruptQueued {
		t.Fatalf("interrupt queued before terminal = %d, want queued", got)
	}
	local.observeResponseTerminal("r1")
	if got := local.take(); got != nil {
		t.Fatalf("interrupt remained queued after terminal observation: %s", got)
	}
	local.end()
	if got := local.deliver(interrupt); got != responsesLocalInterruptAlreadyTerminal {
		t.Fatalf("late terminal response interrupt = %d, want handled", got)
	}
	local.begin()
	local.observeResponseCreated("r2")
	if got := local.deliver(interrupt); got != responsesLocalInterruptAlreadyTerminal {
		t.Fatalf("old response interrupt during next turn = %d, want handled", got)
	}
	current := []byte(`{"type":"response.interrupt","response_id":"r2"}`)
	if got := local.deliver(current); got != responsesLocalInterruptQueued {
		t.Fatalf("current response interrupt = %d, want queued", got)
	}
	local.observeResponseTerminal("r2")
	if got := local.take(); got != nil {
		t.Fatalf("terminal race left a pending interrupt: %s", got)
	}
	local.end()
}

func TestResponsesLocalInterruptTerminalWinsConcurrentDelivery(t *testing.T) {
	for attempt := 0; attempt < 100; attempt++ {
		local := newResponsesLocalInterrupt()
		local.begin()
		local.observeResponseCreated("r1")
		interrupt := []byte(`{"type":"response.interrupt","response_id":"r1"}`)
		start := make(chan struct{})
		var wait sync.WaitGroup
		wait.Add(2)
		go func() {
			defer wait.Done()
			<-start
			local.deliver(interrupt)
		}()
		go func() {
			defer wait.Done()
			<-start
			local.observeResponseTerminal("r1")
		}()
		close(start)
		wait.Wait()
		if got := local.take(); got != nil {
			t.Fatalf("attempt %d: interrupt escaped terminal observation: %s", attempt, got)
		}
		if got := local.deliver(interrupt); got != responsesLocalInterruptAlreadyTerminal {
			t.Fatalf("attempt %d: terminal interrupt = %d, want handled", attempt, got)
		}
		local.end()
	}
}

func TestResponsesLocalInterruptBoundsTerminalIDs(t *testing.T) {
	local := newResponsesLocalInterrupt()
	for id := 0; id <= responsesLocalTerminalResponseLimit; id++ {
		local.observeResponseTerminal(fmt.Sprintf("terminal-%03d", id))
	}
	if got := len(local.terminalIDs); got != responsesLocalTerminalResponseLimit {
		t.Fatalf("terminal ID count = %d, want %d", got, responsesLocalTerminalResponseLimit)
	}
	local.begin()
	local.observeResponseCreated("current")
	if got := local.deliver([]byte(`{"type":"response.interrupt","response_id":"terminal-000"}`)); got != responsesLocalInterruptRejected {
		t.Fatalf("evicted terminal ID interrupt = %d, want rejected", got)
	}
	local.end()
}

func TestResponsesLocalInterruptUsesObservedIDForTerminalWithoutID(t *testing.T) {
	local := newResponsesLocalInterrupt()
	local.begin()
	local.observeResponseCreated("r1")
	interrupt := []byte(`{"type":"response.interrupt","response_id":"r1"}`)
	if got := local.deliver(interrupt); got != responsesLocalInterruptQueued {
		t.Fatalf("active response interrupt = %d, want queued", got)
	}
	local.observeResponseTerminal("")
	if got := local.take(); got != nil {
		t.Fatalf("terminal without response ID left interrupt pending: %s", got)
	}
	if got := local.deliver(interrupt); got != responsesLocalInterruptAlreadyTerminal {
		t.Fatalf("late interrupt after terminal without ID = %d, want handled", got)
	}
	local.end()
}
