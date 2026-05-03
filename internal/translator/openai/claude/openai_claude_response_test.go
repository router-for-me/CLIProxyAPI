package claude

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponseToClaude_StreamToolCallIgnoresEmptyNameDeltas(t *testing.T) {
	events := convertOpenAIStreamTestEvents(t, []string{
		`data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1,"model":"gpt-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Bash","arguments":""}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1,"model":"gpt-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"","arguments":"{\""}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1,"model":"gpt-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"","arguments":"command"}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1,"model":"gpt-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"","arguments":"\":\"pwd\"}"}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1,"model":"gpt-test","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	})

	assertSingleBashToolUse(t, events)
	assertToolArgsAndStopReason(t, events)
}

func TestConvertOpenAIResponseToClaude_StreamToolCallSuppressesRepeatedNameStart(t *testing.T) {
	events := convertOpenAIStreamTestEvents(t, []string{
		`data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1,"model":"gpt-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Bash","arguments":""}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1,"model":"gpt-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"Bash","arguments":"{\"command\":\"pwd\"}"}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1,"model":"gpt-test","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	})

	assertSingleBashToolUse(t, events)
	assertToolArgsAndStopReason(t, events)
}

func convertOpenAIStreamTestEvents(t *testing.T, chunks []string) []gjson.Result {
	t.Helper()

	originalRequest := []byte(`{"stream":true,"tools":[{"name":"Bash"}]}`)
	var param any
	var events []gjson.Result

	for _, chunk := range chunks {
		out := ConvertOpenAIResponseToClaude(context.Background(), "gpt-test", originalRequest, nil, []byte(chunk), &param)
		for _, raw := range out {
			event := parseSSEDataJSON(t, string(raw))
			events = append(events, event)
		}
	}

	return events
}

func parseSSEDataJSON(t *testing.T, raw string) gjson.Result {
	t.Helper()

	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		parsed := gjson.Parse(payload)
		if !parsed.Exists() {
			t.Fatalf("invalid SSE JSON payload: %q", payload)
		}
		return parsed
	}

	t.Fatalf("missing SSE data line: %q", raw)
	return gjson.Result{}
}

func assertSingleBashToolUse(t *testing.T, events []gjson.Result) {
	t.Helper()

	var toolStarts []gjson.Result
	for _, event := range events {
		if event.Get("type").String() == "content_block_start" && event.Get("content_block.type").String() == "tool_use" {
			toolStarts = append(toolStarts, event)
		}
	}

	if len(toolStarts) != 1 {
		t.Fatalf("tool_use content_block_start count = %d, want 1; starts=%v", len(toolStarts), toolStarts)
	}
	if got := toolStarts[0].Get("content_block.name").String(); got != "Bash" {
		t.Fatalf("tool name = %q, want Bash", got)
	}
}

func assertToolArgsAndStopReason(t *testing.T, events []gjson.Result) {
	t.Helper()

	var gotArgs, gotStopReason string
	for _, event := range events {
		switch event.Get("type").String() {
		case "content_block_delta":
			if event.Get("delta.type").String() == "input_json_delta" {
				gotArgs += event.Get("delta.partial_json").String()
			}
		case "message_delta":
			gotStopReason = event.Get("delta.stop_reason").String()
		}
	}

	if !strings.Contains(gotArgs, `"command":"pwd"`) {
		t.Fatalf("tool arguments = %q, want command pwd", gotArgs)
	}
	if gotStopReason != "tool_use" {
		t.Fatalf("stop_reason = %q, want tool_use", gotStopReason)
	}
}
