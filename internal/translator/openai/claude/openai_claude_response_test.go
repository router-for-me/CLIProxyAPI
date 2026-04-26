package claude

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

type claudeSSEEvent struct {
	Name    string
	Payload []byte
}

func parseClaudeSSEEvents(chunks [][]byte) []claudeSSEEvent {
	events := make([]claudeSSEEvent, 0, len(chunks))
	for _, chunk := range chunks {
		var eventName string
		for _, line := range bytes.Split(chunk, []byte("\n")) {
			line = bytes.TrimSpace(bytes.TrimRight(line, "\r"))
			switch {
			case len(line) == 0:
				eventName = ""
			case bytes.HasPrefix(line, []byte("event:")):
				eventName = strings.TrimSpace(string(bytes.TrimSpace(line[len("event:"):])))
			case bytes.HasPrefix(line, []byte("data:")):
				events = append(events, claudeSSEEvent{
					Name:    eventName,
					Payload: bytes.TrimSpace(line[len("data:"):]),
				})
			}
		}
	}
	return events
}

func TestConvertOpenAIResponseToClaude_DoesNotEmitOrphanToolBlockOnFinish(t *testing.T) {
	ctx := context.Background()
	originalReq := []byte(`{"stream":true,"tools":[{"name":"do_work"}]}`)
	var param any

	first := ConvertOpenAIResponseToClaude(
		ctx,
		"test-model",
		originalReq,
		nil,
		[]byte(`data: {"id":"chatcmpl_1","model":"test-model","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"arguments":"{\"a\":1}"}}]}}]}`),
		&param,
	)
	second := ConvertOpenAIResponseToClaude(
		ctx,
		"test-model",
		originalReq,
		nil,
		[]byte(`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`),
		&param,
	)

	events := parseClaudeSSEEvents(append(first, second...))
	for _, event := range events {
		if event.Name != "content_block_delta" && event.Name != "content_block_stop" {
			continue
		}
		deltaType := gjson.GetBytes(event.Payload, "delta.type").String()
		if deltaType == "input_json_delta" || event.Name == "content_block_stop" {
			t.Fatalf("unexpected tool block event without a prior tool_use start: %s %s", event.Name, string(event.Payload))
		}
	}

	for _, event := range events {
		if event.Name != "message_delta" {
			continue
		}
		if got := gjson.GetBytes(event.Payload, "delta.stop_reason").String(); got != "tool_use" {
			t.Fatalf("delta.stop_reason = %q, want tool_use", got)
		}
	}
}

func TestConvertOpenAIResponseToClaude_StartsToolBlockOnlyOncePerToolIndex(t *testing.T) {
	ctx := context.Background()
	originalReq := []byte(`{"stream":true,"tools":[{"name":"do_work"}]}`)
	var param any

	_ = ConvertOpenAIResponseToClaude(
		ctx,
		"test-model",
		originalReq,
		nil,
		[]byte(`data: {"id":"chatcmpl_1","model":"test-model","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"do_work"}}]}}]}`),
		&param,
	)
	repeated := ConvertOpenAIResponseToClaude(
		ctx,
		"test-model",
		originalReq,
		nil,
		[]byte(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"do_work","arguments":"{\"a\":1}"}}]}}]}`),
		&param,
	)

	events := parseClaudeSSEEvents(repeated)
	for _, event := range events {
		if event.Name != "content_block_start" {
			continue
		}
		if got := gjson.GetBytes(event.Payload, "content_block.type").String(); got == "tool_use" {
			t.Fatalf("unexpected duplicate tool_use content_block_start: %s", string(event.Payload))
		}
	}
}

func TestConvertOpenAIResponseToClaudeNonStream_PreservesDanglingToolFinishReason(t *testing.T) {
	out := ConvertOpenAIResponseToClaudeNonStream(
		context.Background(),
		"test-model",
		[]byte(`{"tools":[{"name":"do_work"}]}`),
		nil,
		[]byte(`{"id":"chatcmpl_1","model":"test-model","choices":[{"finish_reason":"tool_calls","message":{"content":"<think>"}}]}`),
		nil,
	)

	if got := gjson.GetBytes(out, "stop_reason").String(); got != "tool_use" {
		t.Fatalf("stop_reason = %q, want tool_use", got)
	}
	if got := gjson.GetBytes(out, "content.0.type").String(); got != "text" {
		t.Fatalf("content.0.type = %q, want text", got)
	}
}
