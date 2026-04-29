package responses

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// Regression for #2194: streaming `done` events must replay the accumulated text
// from prior `output_text.delta` events. Without this, downstream Responses-API
// consumers (e.g. Codex CLI's -o output-last-message) read empty payloads and
// produce blank output even though the deltas carried the full text.
func TestConvertClaudeResponseToOpenAIResponses_AccumulatesTextIntoDoneEvents(t *testing.T) {
	stream := []string{
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-haiku-4-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello "}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"world"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":2}}`,
		`data: {"type":"message_stop"}`,
	}

	var param any
	var allChunks [][]byte
	for _, line := range stream {
		chunks := ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude-haiku-4-5", nil, nil, []byte(line), &param)
		allChunks = append(allChunks, chunks...)
	}

	// Each emitted chunk is a single SSE frame: "event: <name>\ndata: <json>".
	checked := map[string]bool{
		"response.output_text.done":  false,
		"response.content_part.done": false,
		"response.output_item.done":  false,
	}
	for _, chunk := range allChunks {
		text := string(chunk)
		var eventName, payload string
		for _, line := range strings.Split(text, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				eventName = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				payload = strings.TrimPrefix(line, "data: ")
			}
		}
		if payload == "" {
			continue
		}
		switch eventName {
		case "response.output_text.done":
			if got := gjson.Get(payload, "text").String(); got != "Hello world" {
				t.Fatalf("output_text.done text = %q, want %q (payload=%s)", got, "Hello world", payload)
			}
			checked[eventName] = true
		case "response.content_part.done":
			if got := gjson.Get(payload, "part.text").String(); got != "Hello world" {
				t.Fatalf("content_part.done part.text = %q, want %q (payload=%s)", got, "Hello world", payload)
			}
			checked[eventName] = true
		case "response.output_item.done":
			if got := gjson.Get(payload, "item.content.0.text").String(); got != "Hello world" {
				t.Fatalf("output_item.done item.content.0.text = %q, want %q (payload=%s)", got, "Hello world", payload)
			}
			// Codex's ResponseItem deserializer requires `type: "message"` and
			// `role: "assistant"`. See codex-rs/protocol/src/models.rs.
			if got := gjson.Get(payload, "item.type").String(); got != "message" {
				t.Fatalf("output_item.done item.type = %q, want %q (payload=%s)", got, "message", payload)
			}
			if got := gjson.Get(payload, "item.role").String(); got != "assistant" {
				t.Fatalf("output_item.done item.role = %q, want %q (payload=%s)", got, "assistant", payload)
			}
			checked[eventName] = true
		}
	}
	for name, ok := range checked {
		if !ok {
			var dump bytes.Buffer
			for _, chunk := range allChunks {
				dump.Write(chunk)
				dump.WriteByte('\n')
			}
			t.Fatalf("did not see expected event %q in stream output:\n%s", name, dump.String())
		}
	}
}
