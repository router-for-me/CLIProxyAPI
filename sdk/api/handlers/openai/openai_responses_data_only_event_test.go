package openai

import (
	"bytes"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

// Upstream Responses streams may interleave a data-only event (no event: line), such as a
// Codex-style {"type":"codex.client.keepalive"} heartbeat sent after a long silence. The
// executor forwards one SSE line per chunk without blank-line delimiters, so the framer must not
// let that event shift event/data pairing for the rest of the stream.
func TestResponsesSSEFramerDataOnlyEventKeepsTerminal(t *testing.T) {
	lines := []string{
		"event: response.created",
		`data: {"type":"response.created","sequence_number":1,"response":{"id":"resp_1","status":"in_progress","output":[]}}`,
		": keep-alive",
		": keep-alive",
		`data: {"type":"codex.client.keepalive"}`,
		": keep-alive",
		"event: response.function_call_arguments.delta",
		`data: {"type":"response.function_call_arguments.delta","sequence_number":2,"item_id":"fc_1","output_index":0,"delta":"{\"path\":"}`,
		"event: response.output_item.done",
		`data: {"type":"response.output_item.done","sequence_number":3,"output_index":0,"item":{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_1","name":"write_file","arguments":"{\"path\":\"a\"}"}}`,
		"event: response.completed",
		`data: {"type":"response.completed","sequence_number":4,"response":{"id":"resp_1","status":"completed","output":[{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_1","name":"write_file","arguments":"{\"path\":\"a\"}"}]}}`,
	}
	for _, codexClient := range []bool{false, true} {
		framer := &responsesSSEFramer{failureEvent: "error", isCodexClient: codexClient, canComplete: func() bool { return true }}
		var out bytes.Buffer
		for _, line := range lines {
			framer.WriteChunk(&out, []byte(line))
		}
		framer.Flush(&out)

		var events []string
		for _, frame := range strings.Split(strings.TrimSpace(out.String()), "\n\n") {
			payload, ok := responsesSSEDataPayload([]byte(frame))
			if !ok {
				continue
			}
			payloadType := ojson.GetBytesNoCopy(payload, "type").String()
			if name := responsesSSEEventName([]byte(frame)); name != "" && name != payloadType {
				t.Fatalf("codexClient=%v: event %q carries %q payload:\n%s", codexClient, name, payloadType, frame)
			}
			events = append(events, payloadType)
		}
		if framer.terminalEvent != "response.completed" || len(events) == 0 || events[len(events)-1] != "response.completed" {
			t.Fatalf("codexClient=%v: terminal=%q events=%v; want response.completed delivered last\n%s", codexClient, framer.terminalEvent, events, out.String())
		}
	}
}

// A typed event may legally carry its data: field before its own event: field. Splitting it would
// orphan the event: line and shift pairing for every later event.
func TestResponsesSSEFramerKeepsTypedDataBeforeMatchingEvent(t *testing.T) {
	lines := []string{
		`data: {"type":"response.output_text.delta","sequence_number":1,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"hi"}`,
		"event: response.output_text.delta",
		`data: {"type":"response.completed","sequence_number":2,"response":{"id":"resp_1","status":"completed","output":[]}}`,
		"event: response.completed",
	}
	framer := &responsesSSEFramer{failureEvent: "error", canComplete: func() bool { return true }}
	var out bytes.Buffer
	for _, line := range lines {
		framer.WriteChunk(&out, []byte(line))
	}
	framer.Flush(&out)

	frames := strings.Split(strings.TrimSpace(out.String()), "\n\n")
	if len(frames) != 2 {
		t.Fatalf("got %d frames, want 2:\n%s", len(frames), out.String())
	}
	for i, want := range []string{"response.output_text.delta", "response.completed"} {
		payload, _ := responsesSSEDataPayload([]byte(frames[i]))
		if name, typ := responsesSSEEventName([]byte(frames[i])), ojson.GetBytesNoCopy(payload, "type").String(); name != want || typ != want {
			t.Fatalf("frame %d: event=%q type=%q, want %q:\n%s", i, name, typ, want, frames[i])
		}
	}
	if framer.terminalEvent != "response.completed" {
		t.Fatalf("terminal=%q, want response.completed", framer.terminalEvent)
	}
}
