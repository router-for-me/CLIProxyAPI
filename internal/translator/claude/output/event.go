package output

import (
	"bytes"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// TerminalKind identifies a semantic Claude stream terminal.
type TerminalKind string

const (
	TerminalNone        TerminalKind = ""
	TerminalMessageStop TerminalKind = "message_stop"
	TerminalError       TerminalKind = "error"
)

// SSEEvent is one encoded Claude SSE event plus its structured lifecycle state.
type SSEEvent struct {
	name     string
	payload  []byte
	wire     []byte
	terminal TerminalKind
}

// Event encodes one Claude SSE event using the canonical wire framing.
func Event(name string, payload []byte) SSEEvent {
	return EventWithTrailingNewlines(name, payload, 2)
}

// EventWithTrailingNewlines encodes one Claude SSE event while preserving an
// adapter's established chunk separator.
func EventWithTrailingNewlines(name string, payload []byte, trailingNewlines int) SSEEvent {
	payloadCopy := bytes.Clone(payload)
	return SSEEvent{
		name:     name,
		payload:  payloadCopy,
		wire:     translatorcommon.AppendSSEEventBytes(nil, name, payloadCopy, trailingNewlines),
		terminal: terminalKind(name, payloadCopy),
	}
}

// AppendEvent appends one encoded Claude SSE event to an existing chunk.
func AppendEvent(out []byte, name string, payload []byte, trailingNewlines int) []byte {
	return translatorcommon.AppendSSEEventBytes(out, name, payload, trailingNewlines)
}

// AppendEventString appends one encoded Claude SSE event with a string payload.
func AppendEventString(out []byte, name, payload string, trailingNewlines int) []byte {
	return translatorcommon.AppendSSEEventString(out, name, payload, trailingNewlines)
}

// Name returns the SSE event name.
func (e SSEEvent) Name() string {
	return e.name
}

// Payload returns the JSON event payload.
func (e SSEEvent) Payload() []byte {
	return e.payload
}

// Bytes returns the encoded SSE event.
func (e SSEEvent) Bytes() []byte {
	return e.wire
}

// Terminal reports whether the event semantically ends the Claude stream.
func (e SSEEvent) Terminal() bool {
	return e.terminal != TerminalNone
}

// TerminalKind returns the semantic terminal classification.
func (e SSEEvent) TerminalKind() TerminalKind {
	return e.terminal
}

func terminalKind(name string, payload []byte) TerminalKind {
	switch name {
	case "message_stop":
		return TerminalMessageStop
	case "error":
		return TerminalError
	}
	switch gjson.GetBytes(payload, "type").String() {
	case "message_stop":
		return TerminalMessageStop
	case "error":
		return TerminalError
	default:
		return TerminalNone
	}
}

// Payload returns one Claude JSON payload from exactly one SSE event.
func Payload(raw []byte) ([]byte, bool) {
	trimmed := bytes.TrimSpace(raw)
	event, ok := ParseEvent(trimmed)
	if !ok || !gjson.ValidBytes(event.Payload()) {
		return nil, false
	}
	return bytes.Clone(event.Payload()), true
}

// Payloads returns ordered Claude JSON payloads from direct JSON or SSE.
func Payloads(raw []byte) [][]byte {
	trimmed := bytes.TrimSpace(raw)
	if gjson.ValidBytes(trimmed) {
		return [][]byte{bytes.Clone(trimmed)}
	}
	events := ParseEvents(trimmed)
	payloads := make([][]byte, 0, len(events))
	for _, event := range events {
		if gjson.ValidBytes(event.Payload()) {
			payloads = append(payloads, bytes.Clone(event.Payload()))
		}
	}
	return payloads
}

// ParseEvent parses one Claude SSE event without interpreting JSON string data
// as protocol metadata.
func ParseEvent(chunk []byte) (SSEEvent, bool) {
	events := ParseEvents(chunk)
	if len(events) != 1 {
		return SSEEvent{}, false
	}
	return events[0], true
}

// ParseEvents parses every Claude SSE event in a chunk.
func ParseEvents(chunk []byte) []SSEEvent {
	lines := bytes.Split(chunk, []byte("\n"))
	events := make([]SSEEvent, 0, len(lines)/2+1)
	var frame []byte
	var name string
	var data []byte
	flush := func() {
		if data == nil {
			frame = nil
			name = ""
			return
		}
		eventName := name
		if eventName == "" {
			eventName = gjson.GetBytes(data, "type").String()
		}
		event := Event(eventName, data)
		event.wire = bytes.Clone(frame)
		event.wire = append(event.wire, '\n', '\n')
		events = append(events, event)
		frame = nil
		name = ""
		data = nil
	}
	for _, rawLine := range lines {
		line := bytes.TrimSuffix(rawLine, []byte("\r"))
		if len(bytes.TrimSpace(line)) == 0 {
			flush()
			continue
		}
		if bytes.HasPrefix(line, []byte("event:")) && data != nil {
			flush()
		} else if bytes.HasPrefix(line, []byte("data:")) && data != nil && gjson.ValidBytes(data) {
			flush()
		}
		if len(frame) > 0 {
			frame = append(frame, '\n')
		}
		frame = append(frame, line...)
		switch {
		case bytes.HasPrefix(line, []byte("event:")):
			name = string(bytes.TrimSpace(line[len("event:"):]))
		case bytes.HasPrefix(line, []byte("data:")):
			value := bytes.TrimPrefix(line[len("data:"):], []byte(" "))
			if data != nil {
				data = append(data, '\n')
			}
			data = append(data, value...)
		}
	}
	flush()
	return events
}

// HasTerminalEvent reports whether a chunk contains a semantic Claude stream
// terminal event.
func HasTerminalEvent(chunk []byte) bool {
	for _, event := range ParseEvents(chunk) {
		if event.Terminal() {
			return true
		}
	}
	return false
}

// Stream builds an ordered Claude response lifecycle with monotonic content
// block indexes.
type Stream struct {
	events         []SSEEvent
	nextBlockIndex int
}

// Append records an already-built Claude event.
func (s *Stream) Append(event SSEEvent) {
	s.events = append(s.events, event)
}

// MessageStart emits the Claude message_start event.
func (s *Stream) MessageStart(id, model string, inputTokens int64) {
	payload := []byte(`{"type":"message_start","message":{"id":"","type":"message","role":"assistant","model":"","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}}`)
	payload, _ = sjson.SetBytes(payload, "message.id", id)
	payload, _ = sjson.SetBytes(payload, "message.model", model)
	payload, _ = sjson.SetBytes(payload, "message.usage.input_tokens", inputTokens)
	s.Append(Event("message_start", payload))
}

// StartBlock emits content_block_start and returns its allocated index.
func (s *Stream) StartBlock(contentBlock []byte) int {
	index := s.nextBlockIndex
	s.nextBlockIndex++
	payload := []byte(`{"type":"content_block_start","index":0,"content_block":{}}`)
	payload, _ = sjson.SetBytes(payload, "index", index)
	payload, _ = sjson.SetRawBytes(payload, "content_block", contentBlock)
	s.Append(Event("content_block_start", payload))
	return index
}

// BlockDelta emits content_block_delta for a previously allocated index.
func (s *Stream) BlockDelta(index int, delta []byte) {
	payload := []byte(`{"type":"content_block_delta","index":0,"delta":{}}`)
	payload, _ = sjson.SetBytes(payload, "index", index)
	payload, _ = sjson.SetRawBytes(payload, "delta", delta)
	s.Append(Event("content_block_delta", payload))
}

// StopBlock emits content_block_stop for a previously allocated index.
func (s *Stream) StopBlock(index int) {
	payload := []byte(`{"type":"content_block_stop","index":0}`)
	payload, _ = sjson.SetBytes(payload, "index", index)
	s.Append(Event("content_block_stop", payload))
}

// MessageDelta emits the terminal message metadata before message_stop.
func (s *Stream) MessageDelta(stopReason, stopSequence string, outputTokens int64) {
	payload := []byte(`{"type":"message_delta","delta":{"stop_reason":null,"stop_sequence":null},"usage":{"output_tokens":0}}`)
	if stopReason != "" {
		payload, _ = sjson.SetBytes(payload, "delta.stop_reason", stopReason)
	}
	if stopSequence != "" {
		payload, _ = sjson.SetBytes(payload, "delta.stop_sequence", stopSequence)
	}
	payload, _ = sjson.SetBytes(payload, "usage.output_tokens", outputTokens)
	s.Append(Event("message_delta", payload))
}

// MessageStop emits the semantic Claude stream terminal.
func (s *Stream) MessageStop() {
	s.Append(Event("message_stop", []byte(`{"type":"message_stop"}`)))
}

// Error emits a semantic Claude stream error terminal.
func (s *Stream) Error(payload []byte) {
	s.Append(Event("error", payload))
}

// Events returns the ordered structured events.
func (s *Stream) Events() []SSEEvent {
	return append([]SSEEvent(nil), s.events...)
}

// Bytes returns the ordered encoded event chunks.
func (s *Stream) Bytes() [][]byte {
	chunks := make([][]byte, len(s.events))
	for i, event := range s.events {
		chunks[i] = event.Bytes()
	}
	return chunks
}
