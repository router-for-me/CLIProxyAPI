package output

import (
	"bytes"
	"testing"

	"github.com/tidwall/gjson"
)

func TestEventEncodingPreservesClaudeSSEWireFormat(t *testing.T) {
	payload := []byte(`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"hello"}}`)

	got := Event("content_block_delta", payload)
	want := []byte("event: content_block_delta\ndata: " + string(payload) + "\n\n")
	if !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("Event().Bytes() = %q, want %q", got.Bytes(), want)
	}
	if got.Name() != "content_block_delta" {
		t.Fatalf("Event().Name() = %q", got.Name())
	}
	if got.Terminal() {
		t.Fatal("content_block_delta must not be terminal")
	}
}

func TestTerminalEventsCarryStructuredContract(t *testing.T) {
	messageStop := Event("message_stop", []byte(`{"type":"message_stop"}`))
	if !messageStop.Terminal() {
		t.Fatal("message_stop must be terminal")
	}
	if messageStop.TerminalKind() != TerminalMessageStop {
		t.Fatalf("message_stop terminal kind = %q", messageStop.TerminalKind())
	}

	errorEvent := Event("error", []byte(`{"type":"error","error":{"type":"api_error","message":"boom"}}`))
	if !errorEvent.Terminal() {
		t.Fatal("error must be terminal")
	}
	if errorEvent.TerminalKind() != TerminalError {
		t.Fatalf("error terminal kind = %q", errorEvent.TerminalKind())
	}
}

func TestLifecycleProducesStableIndexesAndTerminalOrder(t *testing.T) {
	var stream Stream
	stream.MessageStart("msg-1", "model-1", 7)
	textIndex := stream.StartBlock([]byte(`{"type":"text","text":""}`))
	stream.BlockDelta(textIndex, []byte(`{"type":"text_delta","text":"hello"}`))
	stream.StopBlock(textIndex)
	toolIndex := stream.StartBlock([]byte(`{"type":"tool_use","id":"call-1","name":"run","input":{}}`))
	stream.BlockDelta(toolIndex, []byte(`{"type":"input_json_delta","partial_json":"{}"}`))
	stream.StopBlock(toolIndex)
	stream.MessageDelta("tool_use", "", 11)
	stream.MessageStop()

	events := stream.Events()
	if len(events) != 9 {
		t.Fatalf("event count = %d, want 9", len(events))
	}
	wantNames := []string{"message_start", "content_block_start", "content_block_delta", "content_block_stop", "content_block_start", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}
	for i, want := range wantNames {
		if got := events[i].Name(); got != want {
			t.Fatalf("event %d name = %q, want %q", i, got, want)
		}
	}
	if textIndex != 0 || toolIndex != 1 {
		t.Fatalf("block indexes = (%d, %d), want (0, 1)", textIndex, toolIndex)
	}
	if got := gjson.GetBytes(events[0].Payload(), "message.usage.input_tokens").Int(); got != 7 {
		t.Fatalf("message_start input_tokens = %d, want 7", got)
	}
	if got := gjson.GetBytes(events[4].Payload(), "index").Int(); got != 1 {
		t.Fatalf("tool block index = %d, want 1", got)
	}
	if got := gjson.GetBytes(events[7].Payload(), "delta.stop_reason").String(); got != "tool_use" {
		t.Fatalf("message_delta stop_reason = %q, want tool_use", got)
	}
	if got := gjson.GetBytes(events[7].Payload(), "usage.output_tokens").Int(); got != 11 {
		t.Fatalf("message_delta output_tokens = %d, want 11", got)
	}
	if !events[8].Terminal() {
		t.Fatal("last event must be terminal")
	}
}

func TestParseEventRejectsPayloadTextFalsePositive(t *testing.T) {
	chunk := []byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"\\\"type\\\":\\\"message_stop\\\" event: error\"}}\n\n")

	event, ok := ParseEvent(chunk)
	if !ok {
		t.Fatal("ParseEvent() did not parse valid SSE")
	}
	if event.Terminal() {
		t.Fatal("payload text must not mark an event terminal")
	}
}

func TestParseEventRecognizesTerminalByEventAndPayloadType(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		kind TerminalKind
	}{
		{name: "message stop event", raw: "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", kind: TerminalMessageStop},
		{name: "message stop payload", raw: "data: {\"type\":\"message_stop\"}\n\n", kind: TerminalMessageStop},
		{name: "error event", raw: "event: error\ndata: {\"type\":\"error\"}\n\n", kind: TerminalError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event, ok := ParseEvent([]byte(tt.raw))
			if !ok {
				t.Fatal("ParseEvent() did not parse event")
			}
			if got := event.TerminalKind(); got != tt.kind {
				t.Fatalf("TerminalKind() = %q, want %q", got, tt.kind)
			}
		})
	}
}

func TestPayloadAcceptsSingleClaudeSSEEvent(t *testing.T) {
	payload := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`)
	tests := []struct {
		name string
		raw  []byte
	}{
		{name: "data only", raw: append([]byte("data: "), payload...)},
		{name: "named event", raw: Event("content_block_delta", payload).Bytes()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Payload(tt.raw)
			if !ok {
				t.Fatal("Payload() rejected Claude payload")
			}
			if !bytes.Equal(got, payload) {
				t.Fatalf("Payload() = %q, want %q", got, payload)
			}
		})
	}
}

func TestPayloadRejectsMultiEventChunk(t *testing.T) {
	chunk := append(Event("ping", []byte(`{"type":"ping"}`)).Bytes(), Event("message_stop", []byte(`{"type":"message_stop"}`)).Bytes()...)
	if payload, ok := Payload(chunk); ok {
		t.Fatalf("Payload() accepted multi-event chunk: %q", payload)
	}
}

func TestPayloadsReturnsOrderedDirectAndSSEPayloads(t *testing.T) {
	first := []byte(`{"type":"ping"}`)
	second := []byte(`{"type":"message_stop"}`)
	chunk := append(Event("ping", first).Bytes(), Event("message_stop", second).Bytes()...)

	got := Payloads(chunk)
	if len(got) != 2 || !bytes.Equal(got[0], first) || !bytes.Equal(got[1], second) {
		t.Fatalf("Payloads() = %q, want [%q %q]", got, first, second)
	}

	dataOnly := append(append(append([]byte("data: "), first...), '\n'), append([]byte("data: "), second...)...)
	got = Payloads(dataOnly)
	if len(got) != 2 || !bytes.Equal(got[0], first) || !bytes.Equal(got[1], second) {
		t.Fatalf("Payloads(data-only) = %q, want [%q %q]", got, first, second)
	}

	direct := Payloads(first)
	if len(direct) != 1 || !bytes.Equal(direct[0], first) {
		t.Fatalf("Payloads(direct) = %q, want [%q]", direct, first)
	}
}
