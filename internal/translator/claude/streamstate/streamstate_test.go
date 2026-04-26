package streamstate

import (
	"bytes"
	"testing"

	"github.com/tidwall/gjson"
)

type sseEvent struct {
	Name    string
	Payload []byte
}

func parseSSEEvents(chunks [][]byte) []sseEvent {
	events := make([]sseEvent, 0, len(chunks))
	for _, chunk := range chunks {
		var eventName string
		for _, line := range bytes.Split(chunk, []byte("\n")) {
			line = bytes.TrimSpace(bytes.TrimRight(line, "\r"))
			switch {
			case len(line) == 0:
				eventName = ""
			case bytes.HasPrefix(line, []byte("event:")):
				eventName = string(bytes.TrimSpace(line[len("event:"):]))
			case bytes.HasPrefix(line, []byte("data:")):
				events = append(events, sseEvent{
					Name:    eventName,
					Payload: bytes.TrimSpace(line[len("data:"):]),
				})
			}
		}
	}
	return events
}

func TestLifecycleBuffersToolInputUntilStart(t *testing.T) {
	lifecycle := NewLifecycle()

	if got := lifecycle.AppendToolInput("tool-0", `{"a":`); len(got) != 0 {
		t.Fatalf("expected no output before tool start, got %q", string(bytes.Join(got, nil)))
	}

	chunks := lifecycle.EnsureToolUse("tool-0", "", "do_work")
	events := parseSSEEvents(chunks)
	if len(events) != 2 {
		t.Fatalf("expected start + buffered delta, got %d events", len(events))
	}
	if events[0].Name != "content_block_start" {
		t.Fatalf("first event = %q, want content_block_start", events[0].Name)
	}
	if got := gjson.GetBytes(events[1].Payload, "delta.type").String(); got != "input_json_delta" {
		t.Fatalf("delta.type = %q, want input_json_delta", got)
	}
	if got := gjson.GetBytes(events[1].Payload, "delta.partial_json").String(); got != `{"a":` {
		t.Fatalf("delta.partial_json = %q, want %q", got, `{"a":`)
	}
}

func TestLifecycleStartsToolOnlyOnce(t *testing.T) {
	lifecycle := NewLifecycle()

	first := lifecycle.EnsureToolUse("tool-0", "call_1", "do_work")
	second := lifecycle.EnsureToolUse("tool-0", "call_1", "do_work")
	if len(first) == 0 {
		t.Fatal("expected initial tool start output")
	}
	if len(second) != 0 {
		t.Fatalf("expected duplicate start to emit nothing, got %q", string(bytes.Join(second, nil)))
	}
}

func TestLifecycleDropsUnstartedToolOnCloseAll(t *testing.T) {
	lifecycle := NewLifecycle()
	_ = lifecycle.AppendToolInput("tool-0", `{"a":1}`)

	chunks := lifecycle.CloseAllToolBlocks()
	if len(chunks) != 0 {
		t.Fatalf("expected no close events for unstarted tool block, got %q", string(bytes.Join(chunks, nil)))
	}
}
