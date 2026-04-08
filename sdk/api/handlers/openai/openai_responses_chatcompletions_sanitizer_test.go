package openai

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	responsesconverter "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/openai/openai/responses"
	"github.com/tidwall/gjson"
)

func TestSanitizeConvertedResponsesChatCompletions_ReordersInterleavedMessagesFromResponsesInput(t *testing.T) {
	input := []byte(`{
		"model": "skd/gpt5.4",
		"instructions": "system instructions",
		"input": [
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]},
			{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"bad\"}","call_id":"call_1"},
			{"type":"message","role":"developer","content":[{"type":"input_text","text":"Bash reported command-not-found; verify PATH before retrying."}]},
			{"type":"function_call_output","call_id":"call_1","output":"The Bash output indicates a command/setup failure that should be fixed before retrying."}
		],
		"stream": true
	}`)

	converted := responsesconverter.ConvertOpenAIResponsesRequestToOpenAIChatCompletions("skd/gpt5.4", input, true)
	sanitized := sanitizeConvertedResponsesChatCompletions(converted)

	messages := gjson.GetBytes(sanitized, "messages").Array()
	if len(messages) != 5 {
		t.Fatalf("expected 5 messages, got %d: %s", len(messages), gjson.GetBytes(sanitized, "messages").Raw)
	}

	if got := messages[0].Get("role").String(); got != "system" {
		t.Fatalf("message[0] role = %q, want system", got)
	}
	if got := messages[1].Get("role").String(); got != "user" {
		t.Fatalf("message[1] role = %q, want user", got)
	}
	if got := messages[2].Get("role").String(); got != "assistant" {
		t.Fatalf("message[2] role = %q, want assistant", got)
	}
	if got := messages[3].Get("role").String(); got != "tool" {
		t.Fatalf("message[3] role = %q, want tool", got)
	}
	if got := messages[4].Get("role").String(); got != "user" {
		t.Fatalf("message[4] role = %q, want user", got)
	}

	if got := messages[3].Get("tool_call_id").String(); got != "call_1" {
		t.Fatalf("tool_call_id = %q, want call_1", got)
	}

	assertToolCallChainsAreContiguous(t, sanitized)
}

func TestSanitizeConvertedResponsesChatCompletions_MergesConsecutiveAssistantToolCallMessages(t *testing.T) {
	input := []byte(`{
		"model": "skd/gpt5.4",
		"input": [
			{"type":"message","role":"user","content":[{"type":"input_text","text":"compare weather"}]},
			{"type":"function_call","name":"get_weather","arguments":"{\"city\":\"Paris\"}","call_id":"call_paris"},
			{"type":"function_call","name":"get_weather","arguments":"{\"city\":\"London\"}","call_id":"call_london"},
			{"type":"function_call_output","call_id":"call_paris","output":"sunny"},
			{"type":"function_call_output","call_id":"call_london","output":"rainy"}
		],
		"stream": true
	}`)

	converted := responsesconverter.ConvertOpenAIResponsesRequestToOpenAIChatCompletions("skd/gpt5.4", input, true)
	sanitized := sanitizeConvertedResponsesChatCompletions(converted)

	messages := gjson.GetBytes(sanitized, "messages").Array()
	if len(messages) != 4 {
		t.Fatalf("expected 4 messages, got %d: %s", len(messages), gjson.GetBytes(sanitized, "messages").Raw)
	}

	assistant := messages[1]
	if got := assistant.Get("role").String(); got != "assistant" {
		t.Fatalf("message[1] role = %q, want assistant", got)
	}
	if got := len(assistant.Get("tool_calls").Array()); got != 2 {
		t.Fatalf("assistant tool_calls len = %d, want 2", got)
	}

	assertToolCallChainsAreContiguous(t, sanitized)
}

func TestSanitizeConvertedResponsesChatCompletions_PreservesAlreadyValidSequences(t *testing.T) {
	payload := []byte(`{
		"model":"skd/gpt5.4",
		"messages":[
			{"role":"user","content":"hi"},
			{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"ping","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"ok"},
			{"role":"assistant","content":"done"}
		],
		"stream":true
	}`)

	sanitized := sanitizeConvertedResponsesChatCompletions(payload)
	if string(sanitized) != string(payload) {
		t.Fatalf("expected valid sequence to remain unchanged.\nGot:  %s\nWant: %s", sanitized, payload)
	}
}

func TestSanitizeConvertedResponsesChatCompletions_FixesCapturedFixtureShape(t *testing.T) {
	input := mustReadFixture(t, "responses_interleaved_tool_chain_fixture.json")

	converted := responsesconverter.ConvertOpenAIResponsesRequestToOpenAIChatCompletions("skd/gpt5.4", input, true)
	if idx := firstToolCallOrderingViolation(converted); idx == -1 {
		t.Fatalf("expected fixture to reproduce an ordering violation before sanitization: %s", gjson.GetBytes(converted, "messages").Raw)
	}

	sanitized := sanitizeConvertedResponsesChatCompletions(converted)
	if idx := firstToolCallOrderingViolation(sanitized); idx != -1 {
		t.Fatalf("expected sanitizer to remove ordering violation, first invalid message index=%d: %s", idx, gjson.GetBytes(sanitized, "messages").Raw)
	}

	messages := gjson.GetBytes(sanitized, "messages").Array()
	lastToolIdx := len(messages) - 2
	if got := messages[lastToolIdx].Get("role").String(); got != "tool" {
		t.Fatalf("message[%d] role = %q, want tool", lastToolIdx, got)
	}
	if got := messages[lastToolIdx+1].Get("role").String(); got != "user" {
		t.Fatalf("message[%d] role = %q, want user", lastToolIdx+1, got)
	}
}

func TestSanitizeConvertedResponsesChatCompletions_PreservesLargeNumericLiterals(t *testing.T) {
	payload := []byte(`{
		"model":"skd/gpt5.4",
		"seed":9007199254740993,
		"tools":[
			{
				"type":"function",
				"function":{
					"name":"ping",
					"parameters":{
						"type":"object",
						"properties":{
							"limit":{"type":"integer","maximum":9007199254740995}
						}
					}
				}
			}
		],
		"messages":[
			{"role":"user","content":"hi"},
			{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"ping","arguments":"{}"}}]},
			{"role":"user","content":"interleaved"},
			{"role":"tool","tool_call_id":"call_1","content":"ok"}
		],
		"stream":true
	}`)

	sanitized := sanitizeConvertedResponsesChatCompletions(payload)
	if !bytes.Contains(sanitized, []byte(`"seed":9007199254740993`)) {
		t.Fatalf("expected large top-level numeric literal to be preserved, got: %s", sanitized)
	}
	if !bytes.Contains(sanitized, []byte(`"maximum":9007199254740995`)) {
		t.Fatalf("expected nested tool-schema numeric literal to be preserved, got: %s", sanitized)
	}
}

func TestSanitizeConvertedResponsesChatCompletions_DoesNotMergeAssistantToolCallsAcrossToolResults(t *testing.T) {
	payload := []byte(`{
		"model":"skd/gpt5.4",
		"messages":[
			{"role":"user","content":"compare"},
			{"role":"assistant","tool_calls":[
				{"id":"call_a","type":"function","function":{"name":"weather","arguments":"{\"city\":\"Paris\"}"}},
				{"id":"call_b","type":"function","function":{"name":"weather","arguments":"{\"city\":\"London\"}"}}
			]},
			{"role":"tool","tool_call_id":"call_a","content":"sunny"},
			{"role":"assistant","tool_calls":[
				{"id":"call_c","type":"function","function":{"name":"weather","arguments":"{\"city\":\"Tokyo\"}"}}
			]},
			{"role":"tool","tool_call_id":"call_b","content":"rainy"},
			{"role":"tool","tool_call_id":"call_c","content":"humid"}
		],
		"stream":true
	}`)

	sanitized := sanitizeConvertedResponsesChatCompletions(payload)
	messages := gjson.GetBytes(sanitized, "messages").Array()
	if len(messages) != 6 {
		t.Fatalf("expected 6 messages, got %d: %s", len(messages), gjson.GetBytes(sanitized, "messages").Raw)
	}

	roleSequence := []string{
		messages[0].Get("role").String(),
		messages[1].Get("role").String(),
		messages[2].Get("role").String(),
		messages[3].Get("role").String(),
		messages[4].Get("role").String(),
		messages[5].Get("role").String(),
	}
	wantRoles := []string{"user", "assistant", "tool", "tool", "assistant", "tool"}
	if !reflect.DeepEqual(roleSequence, wantRoles) {
		t.Fatalf("role sequence = %#v, want %#v. messages=%s", roleSequence, wantRoles, gjson.GetBytes(sanitized, "messages").Raw)
	}

	firstAssistant := messages[1]
	secondAssistant := messages[4]
	if got := len(firstAssistant.Get("tool_calls").Array()); got != 2 {
		t.Fatalf("first assistant tool_calls len = %d, want 2", got)
	}
	if got := len(secondAssistant.Get("tool_calls").Array()); got != 1 {
		t.Fatalf("second assistant tool_calls len = %d, want 1", got)
	}
	if got := secondAssistant.Get("tool_calls.0.id").String(); got != "call_c" {
		t.Fatalf("second assistant tool_call id = %q, want call_c", got)
	}
	if idx := firstToolCallOrderingViolation(sanitized); idx != -1 {
		t.Fatalf("expected no ordering violation after delaying later assistant tool calls, got first invalid index=%d: %s", idx, gjson.GetBytes(sanitized, "messages").Raw)
	}
}

func TestSanitizeConvertedResponsesChatCompletions_MergesAssistantToolCallsAcrossBufferedMessagesBeforeToolResults(t *testing.T) {
	input := []byte(`{
		"model":"skd/gpt5.4",
		"instructions":"system instructions",
		"input":[
			{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"a\"}","call_id":"call_a"},
			{"type":"message","role":"developer","content":[{"type":"input_text","text":"diagnostic before another tool call"}]},
			{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"b\"}","call_id":"call_b"},
			{"type":"function_call_output","call_id":"call_a","output":"A done"},
			{"type":"function_call_output","call_id":"call_b","output":"B done"}
		],
		"stream":true
	}`)

	converted := responsesconverter.ConvertOpenAIResponsesRequestToOpenAIChatCompletions("skd/gpt5.4", input, true)
	sanitized := sanitizeConvertedResponsesChatCompletions(converted)
	messages := gjson.GetBytes(sanitized, "messages").Array()

	var assistantToolCallCounts []int
	for _, message := range messages {
		if message.Get("role").String() == "assistant" && message.Get("tool_calls").Exists() {
			assistantToolCallCounts = append(assistantToolCallCounts, len(message.Get("tool_calls").Array()))
		}
	}

	if !reflect.DeepEqual(assistantToolCallCounts, []int{2}) {
		t.Fatalf("assistant tool call grouping = %#v, want []int{2}. messages=%s", assistantToolCallCounts, gjson.GetBytes(sanitized, "messages").Raw)
	}
	if idx := firstToolCallOrderingViolation(sanitized); idx != -1 {
		t.Fatalf("expected no ordering violation after merging across buffered message, got first invalid index=%d: %s", idx, gjson.GetBytes(sanitized, "messages").Raw)
	}

	lastMessage := messages[len(messages)-1]
	if got := lastMessage.Get("role").String(); got != "user" {
		t.Fatalf("last message role = %q, want user. messages=%s", got, gjson.GetBytes(sanitized, "messages").Raw)
	}
	if got := lastMessage.Get("content.0.text").String(); got != "diagnostic before another tool call" {
		t.Fatalf("last buffered message text = %q, want diagnostic message", got)
	}
}

func TestSanitizeConvertedResponsesChatCompletions_BuffersToolResultsForDeferredAssistantToolCalls(t *testing.T) {
	payload := []byte(`{
		"model":"skd/gpt5.4",
		"messages":[
			{"role":"user","content":"compare"},
			{"role":"assistant","tool_calls":[
				{"id":"call_a","type":"function","function":{"name":"weather","arguments":"{\"city\":\"Paris\"}"}},
				{"id":"call_b","type":"function","function":{"name":"weather","arguments":"{\"city\":\"London\"}"}}
			]},
			{"role":"tool","tool_call_id":"call_a","content":"sunny"},
			{"role":"assistant","tool_calls":[
				{"id":"call_c","type":"function","function":{"name":"weather","arguments":"{\"city\":\"Tokyo\"}"}}
			]},
			{"role":"tool","tool_call_id":"call_c","content":"humid"},
			{"role":"tool","tool_call_id":"call_b","content":"rainy"}
		],
		"stream":true
	}`)

	sanitized := sanitizeConvertedResponsesChatCompletions(payload)
	messages := gjson.GetBytes(sanitized, "messages").Array()
	roleSequence := []string{
		messages[0].Get("role").String(),
		messages[1].Get("role").String(),
		messages[2].Get("role").String(),
		messages[3].Get("role").String(),
		messages[4].Get("role").String(),
		messages[5].Get("role").String(),
	}
	wantRoles := []string{"user", "assistant", "tool", "tool", "assistant", "tool"}
	if !reflect.DeepEqual(roleSequence, wantRoles) {
		t.Fatalf("role sequence = %#v, want %#v. messages=%s", roleSequence, wantRoles, gjson.GetBytes(sanitized, "messages").Raw)
	}
	if got := messages[3].Get("tool_call_id").String(); got != "call_b" {
		t.Fatalf("message[3] tool_call_id = %q, want call_b", got)
	}
	if got := messages[4].Get("tool_calls.0.id").String(); got != "call_c" {
		t.Fatalf("message[4] tool_call id = %q, want call_c", got)
	}
	if got := messages[5].Get("tool_call_id").String(); got != "call_c" {
		t.Fatalf("message[5] tool_call_id = %q, want call_c", got)
	}
	if idx := firstToolCallOrderingViolation(sanitized); idx != -1 {
		t.Fatalf("expected no ordering violation after buffering deferred tool result, got first invalid index=%d: %s", idx, gjson.GetBytes(sanitized, "messages").Raw)
	}
}

func TestMergeAssistantContent_StringString(t *testing.T) {
	dst := map[string]any{"content": "first"}
	src := map[string]any{"content": "second"}

	mergeAssistantContent(dst, src)

	if got := dst["content"]; got != "first\nsecond" {
		t.Fatalf("merged content = %#v, want %q", got, "first\nsecond")
	}
}

func TestMergeAssistantContent_ArrayArray(t *testing.T) {
	dst := map[string]any{
		"content": []any{
			map[string]any{"type": "text", "text": "first"},
		},
	}
	src := map[string]any{
		"content": []any{
			map[string]any{"type": "text", "text": "second"},
		},
	}

	mergeAssistantContent(dst, src)

	want := []any{
		map[string]any{"type": "text", "text": "first"},
		map[string]any{"type": "text", "text": "second"},
	}
	if !reflect.DeepEqual(dst["content"], want) {
		t.Fatalf("merged content = %#v, want %#v", dst["content"], want)
	}
}

func TestMergeAssistantContent_MixedStringAndArray(t *testing.T) {
	tests := []struct {
		name string
		dst  any
		src  any
		want []any
	}{
		{
			name: "string_then_array",
			dst:  "first",
			src: []any{
				map[string]any{"type": "text", "text": "second"},
			},
			want: []any{
				map[string]any{"type": "text", "text": "first"},
				map[string]any{"type": "text", "text": "second"},
			},
		},
		{
			name: "array_then_string",
			dst: []any{
				map[string]any{"type": "text", "text": "first"},
			},
			src: "second",
			want: []any{
				map[string]any{"type": "text", "text": "first"},
				map[string]any{"type": "text", "text": "second"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dst := map[string]any{"content": tt.dst}
			src := map[string]any{"content": tt.src}

			mergeAssistantContent(dst, src)

			if !reflect.DeepEqual(dst["content"], tt.want) {
				t.Fatalf("merged content = %#v, want %#v", dst["content"], tt.want)
			}
		})
	}
}

func assertToolCallChainsAreContiguous(t *testing.T, rawJSON []byte) {
	t.Helper()

	pending := make(map[string]struct{})
	messages := gjson.GetBytes(rawJSON, "messages").Array()
	for idx, message := range messages {
		role := message.Get("role").String()
		if len(pending) > 0 && role != "tool" {
			t.Fatalf("message[%d] role=%s interrupted pending tool calls: %s", idx, role, gjson.GetBytes(rawJSON, "messages").Raw)
		}

		if role == "assistant" {
			for _, toolCall := range message.Get("tool_calls").Array() {
				if id := toolCall.Get("id").String(); id != "" {
					pending[id] = struct{}{}
				}
			}
		}

		if role == "tool" {
			delete(pending, message.Get("tool_call_id").String())
		}
	}
}

func firstToolCallOrderingViolation(rawJSON []byte) int {
	pending := make(map[string]struct{})
	messages := gjson.GetBytes(rawJSON, "messages").Array()
	for idx, message := range messages {
		role := message.Get("role").String()
		if len(pending) > 0 && role != "tool" {
			return idx
		}

		if role == "assistant" {
			for _, toolCall := range message.Get("tool_calls").Array() {
				if id := toolCall.Get("id").String(); id != "" {
					pending[id] = struct{}{}
				}
			}
		}

		if role == "tool" {
			delete(pending, message.Get("tool_call_id").String())
		}
	}
	return -1
}

func mustReadFixture(t *testing.T, name string) []byte {
	t.Helper()

	path := filepath.Join("..", "..", "..", "..", "test", "testdata", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	return data
}
