package responses

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// collectStreamEvents drives the streaming converter over the given upstream
// chunks and returns the emitted event names plus every streamed item that
// carries an empty tool name.
func collectStreamEvents(t *testing.T, request []byte, chunks []string) (events []string, emptyNamed int) {
	t.Helper()
	var param any
	for _, line := range chunks {
		for _, out := range ConvertOpenAIChatCompletionsResponseToOpenAIResponses(
			context.Background(), "test-model", request, request, []byte(line), &param) {
			for _, l := range strings.Split(string(out), "\n") {
				if strings.HasPrefix(l, "event: ") {
					events = append(events, strings.TrimPrefix(l, "event: "))
					continue
				}
				if !strings.HasPrefix(l, "data: ") {
					continue
				}
				payload := strings.TrimPrefix(l, "data: ")
				if !gjson.Valid(payload) {
					continue
				}
				item := gjson.Parse(payload).Get("item")
				if !item.Exists() {
					continue
				}
				switch item.Get("type").String() {
				case "function_call", "custom_tool_call":
					if strings.TrimSpace(item.Get("name").String()) == "" {
						emptyNamed++
					}
				}
			}
		}
	}
	return events, emptyNamed
}

func hasEvent(events []string, want string) bool {
	for _, e := range events {
		if e == want {
			return true
		}
	}
	return false
}

// A function_call left in the history without a name (written by an earlier
// malformed upstream response) must not be replayed upstream: strict providers
// reject the whole request with "missing a function name", which makes the
// session permanently unusable.
func TestConvertOpenAIResponsesRequest_DropsHistoricalToolCallWithoutName(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"type":"function_call","call_id":"call_bad","name":"","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_bad","output":"result"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("test-model", raw, false)

	gjson.GetBytes(out, "messages").ForEach(func(_, msg gjson.Result) bool {
		msg.Get("tool_calls").ForEach(func(_, tc gjson.Result) bool {
			if strings.TrimSpace(tc.Get("function.name").String()) == "" {
				t.Errorf("tool call with empty function.name leaked upstream: %s", out)
			}
			return true
		})
		return true
	})
	// The orphaned output must still survive so the conversation can continue.
	if !strings.Contains(string(out), "result") {
		t.Errorf("orphaned function_call_output was lost: %s", out)
	}
}

// Valid history must keep round-tripping unchanged.
func TestConvertOpenAIResponsesRequest_KeepsNamedToolCall(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"type":"function_call","call_id":"call_ok","name":"shell","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_ok","output":"ok"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("test-model", raw, false)

	if got := gjson.GetBytes(out, "messages.0.tool_calls.0.function.name").String(); got != "shell" {
		t.Fatalf("expected tool name %q, got %q (%s)", "shell", got, out)
	}
}

// A non-streaming response whose tool call has no name cannot be represented as
// a Responses function_call, so the whole response is reported as failed rather
// than emitting an item the client would replay forever.
func TestConvertOpenAIChatCompletionsResponseNonStream_FailsOnToolCallWithoutName(t *testing.T) {
	req := []byte(`{"model":"test-model","input":[{"type":"message","role":"user","content":"hi"}]}`)
	upstream := []byte(`{
		"id":"chatcmpl-x","object":"chat.completion","model":"test-model",
		"choices":[{"index":0,"finish_reason":"tool_calls","message":{
			"role":"assistant",
			"tool_calls":[{"id":"call_bad","type":"function","function":{"name":"","arguments":"{}"}}]
		}}]
	}`)

	out := ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(
		context.Background(), "test-model", req, req, upstream, nil)

	if status := gjson.GetBytes(out, "status").String(); status != "failed" {
		t.Errorf("expected status failed, got %q (%s)", status, out)
	}
	if code := gjson.GetBytes(out, "error.code").String(); code != "invalid_upstream_tool_call" {
		t.Errorf("expected invalid_upstream_tool_call, got %q (%s)", code, out)
	}
	if n := len(gjson.GetBytes(out, "output").Array()); n != 0 {
		t.Errorf("expected no output items, got %d (%s)", n, out)
	}
}

// Mixing a valid and an invalid tool call still fails the whole response:
// dropping one call out of a parallel batch would silently change what the
// assistant asked for.
func TestConvertOpenAIChatCompletionsResponseNonStream_FailsWhenAnyToolCallLacksName(t *testing.T) {
	req := []byte(`{"model":"test-model","input":[{"type":"message","role":"user","content":"hi"}]}`)
	upstream := []byte(`{
		"id":"chatcmpl-x","object":"chat.completion","model":"test-model",
		"choices":[{"index":0,"finish_reason":"tool_calls","message":{
			"role":"assistant",
			"tool_calls":[
				{"id":"call_ok","type":"function","function":{"name":"shell","arguments":"{}"}},
				{"id":"call_bad","type":"function","function":{"name":"","arguments":"{}"}}
			]
		}}]
	}`)

	out := ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(
		context.Background(), "test-model", req, req, upstream, nil)

	if status := gjson.GetBytes(out, "status").String(); status != "failed" {
		t.Errorf("expected status failed, got %q (%s)", status, out)
	}
	if strings.Contains(string(out), "call_ok") {
		t.Errorf("must not emit the valid half of an invalid batch: %s", out)
	}
}

// A stream that never carries a tool name ends with response.failed instead of
// completing with an unusable function_call item.
func TestConvertOpenAIChatCompletionsResponseStream_FailsOnToolCallWithoutName(t *testing.T) {
	request := []byte(`{"model":"test-model","input":[{"type":"message","role":"user","content":"hi"}]}`)
	events, emptyNamed := collectStreamEvents(t, request, []string{
		`data: {"id":"chatcmpl-x","object":"chat.completion.chunk","model":"test-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_bad","type":"function","function":{"name":"","arguments":""}}]}}]}`,
		`data: {"id":"chatcmpl-x","object":"chat.completion.chunk","model":"test-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}`,
		`data: {"id":"chatcmpl-x","object":"chat.completion.chunk","model":"test-model","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	})

	if emptyNamed > 0 {
		t.Errorf("%d streamed item(s) carried an empty tool name: %v", emptyNamed, events)
	}
	if !hasEvent(events, "response.failed") {
		t.Errorf("expected response.failed, got %v", events)
	}
	if hasEvent(events, "response.completed") {
		t.Errorf("must not complete a response with a nameless tool call: %v", events)
	}
}

// Names streamed in fragments must keep working: the guard only triggers when no
// name arrives at all.
func TestConvertOpenAIChatCompletionsResponseStream_KeepsFragmentedToolName(t *testing.T) {
	request := []byte(`{"model":"test-model","input":[{"type":"message","role":"user","content":"hi"}]}`)
	events, emptyNamed := collectStreamEvents(t, request, []string{
		`data: {"id":"chatcmpl-x","object":"chat.completion.chunk","model":"test-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_ok","type":"function","function":{"name":"sh","arguments":""}}]}}]}`,
		`data: {"id":"chatcmpl-x","object":"chat.completion.chunk","model":"test-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"ell","arguments":"{}"}}]}}]}`,
		`data: {"id":"chatcmpl-x","object":"chat.completion.chunk","model":"test-model","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	})

	if emptyNamed > 0 {
		t.Errorf("fragmented tool name was mistaken for an empty one: %v", events)
	}
	if !hasEvent(events, "response.completed") {
		t.Errorf("expected response.completed for a valid tool call, got %v", events)
	}
	if hasEvent(events, "response.failed") {
		t.Errorf("valid fragmented tool name must not fail: %v", events)
	}
}
