package claude

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type parsedSSEEvent struct {
	Event string
	Data  map[string]any
}

func parseSSEEvents(t *testing.T, chunks []string) []parsedSSEEvent {
	t.Helper()

	var events []parsedSSEEvent
	for _, chunk := range chunks {
		for _, block := range strings.Split(chunk, "\n\n") {
			block = strings.TrimSpace(block)
			if block == "" {
				continue
			}

			var eventName string
			var dataStr string

			for _, line := range strings.Split(block, "\n") {
				if strings.HasPrefix(line, "event: ") {
					eventName = strings.TrimSpace(strings.TrimPrefix(line, "event: "))
				} else if strings.HasPrefix(line, "data: ") {
					dataStr = strings.TrimSpace(strings.TrimPrefix(line, "data: "))
				}
			}

			if eventName == "" || dataStr == "" {
				continue
			}

			var data map[string]any
			if err := json.Unmarshal([]byte(dataStr), &data); err != nil {
				t.Fatalf("failed to parse SSE JSON data %q: %v", dataStr, err)
			}
			events = append(events, parsedSSEEvent{Event: eventName, Data: data})
		}
	}

	return events
}

func floatIndexToInt(v any) (int, bool) {
	f, ok := v.(float64)
	if !ok {
		return 0, false
	}
	return int(f), true
}

func expectedStartTypeForDelta(deltaType string) (string, bool) {
	switch deltaType {
	case "thinking_delta":
		return "thinking", true
	case "text_delta":
		return "text", true
	case "input_json_delta":
		return "tool_use", true
	default:
		return "", false
	}
}

func TestConvertCodexResponseToClaude_DoesNotReuseContentBlockIndexesAcrossTypes(t *testing.T) {
	originalRequest := []byte(`{"tools":[{"name":"dummy_tool","input_schema":{"type":"object"}}]}`)

	var state any
	var outputs []string

	inputs := []string{
		`{"type":"response.created","response":{"id":"r1","model":"gpt-5.2"}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"dummy_tool"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"x\":1}"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call"}}`,
		`{"type":"response.reasoning_summary_part.added","output_index":0}`,
		`{"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"Thinking..."}`,
		`{"type":"response.reasoning_summary_part.done","output_index":0}`,
		`{"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":2}}}`,
	}

	for _, in := range inputs {
		raw := []byte("data: " + in)
		out := ConvertCodexResponseToClaude(context.Background(), "", originalRequest, nil, raw, &state)
		outputs = append(outputs, out...)
	}

	events := parseSSEEvents(t, outputs)

	startTypeByIndex := make(map[int]string)
	var toolUseIndex, thinkingIndex *int

	for _, ev := range events {
		typ, _ := ev.Data["type"].(string)
		switch typ {
		case "content_block_start":
			idx, ok := floatIndexToInt(ev.Data["index"])
			if !ok {
				t.Fatalf("content_block_start missing numeric index: %#v", ev.Data["index"])
			}

			contentBlock, ok := ev.Data["content_block"].(map[string]any)
			if !ok {
				t.Fatalf("content_block_start missing content_block object: %#v", ev.Data["content_block"])
			}
			cbType, _ := contentBlock["type"].(string)
			if cbType == "" {
				t.Fatalf("content_block_start missing content_block.type: %#v", contentBlock)
			}

			if prev, exists := startTypeByIndex[idx]; exists && prev != cbType {
				t.Fatalf("content_block index %d reused for different types: %q then %q", idx, prev, cbType)
			}
			startTypeByIndex[idx] = cbType

			switch cbType {
			case "tool_use":
				tmp := idx
				toolUseIndex = &tmp
			case "thinking":
				tmp := idx
				thinkingIndex = &tmp
			}
		case "content_block_delta":
			idx, ok := floatIndexToInt(ev.Data["index"])
			if !ok {
				t.Fatalf("content_block_delta missing numeric index: %#v", ev.Data["index"])
			}
			delta, ok := ev.Data["delta"].(map[string]any)
			if !ok {
				t.Fatalf("content_block_delta missing delta object: %#v", ev.Data["delta"])
			}
			deltaType, _ := delta["type"].(string)
			if deltaType == "" {
				t.Fatalf("content_block_delta missing delta.type: %#v", delta)
			}
			expectedStartType, ok := expectedStartTypeForDelta(deltaType)
			if !ok {
				continue
			}
			startType, exists := startTypeByIndex[idx]
			if !exists {
				t.Fatalf("content_block_delta for index %d (%s) without a prior content_block_start", idx, deltaType)
			}
			if startType != expectedStartType {
				t.Fatalf("content_block_delta type %q mismatched for index %d: started as %q", deltaType, idx, startType)
			}
		case "content_block_stop":
			idx, ok := floatIndexToInt(ev.Data["index"])
			if !ok {
				t.Fatalf("content_block_stop missing numeric index: %#v", ev.Data["index"])
			}
			if _, exists := startTypeByIndex[idx]; !exists {
				t.Fatalf("content_block_stop for unknown index %d", idx)
			}
		}
	}

	if toolUseIndex == nil || thinkingIndex == nil {
		t.Fatalf("expected both tool_use and thinking content_block_start events; got tool_use=%v thinking=%v", toolUseIndex, thinkingIndex)
	}
	if *toolUseIndex == *thinkingIndex {
		t.Fatalf("tool_use and thinking blocks share the same index %d; indexes must be unique within a message", *toolUseIndex)
	}
}
