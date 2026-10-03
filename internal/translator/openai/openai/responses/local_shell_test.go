package responses

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestResponsesLocalShellToolIsNotDropped(t *testing.T) {
	raw := []byte(`{
		"model":"fixture",
		"tool_choice":"auto",
		"tools":[{"type":"shell","environment":{"type":"local"}}]
	}`)
	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("fixture", raw, false)
	if got := gjson.GetBytes(out, "tools.#").Int(); got != 1 {
		t.Fatalf("translated tool count = %d, want 1; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "tools.0.function.name").String(); got != reservedLocalShellChatToolName {
		t.Fatalf("translated shell name = %q, want %q", got, reservedLocalShellChatToolName)
	}
	if got := gjson.GetBytes(out, "tools.0.function.parameters.required.0").String(); got != "command" {
		t.Fatalf("required command missing: %s", out)
	}
}

func TestResponsesLocalShellCallAndOutputReplay(t *testing.T) {
	raw := []byte(`{
		"model":"fixture",
		"tools":[{"type":"shell","environment":{"type":"local"}}],
		"input":[
			{"type":"local_shell_call","id":"ls_1","call_id":"call_1","status":"completed","action":{"type":"exec","command":["Write-Output","SHELL_OK"],"env":{"A":"B"},"timeout_ms":123,"working_directory":"O:\\cpa"}},
			{"type":"local_shell_call_output","call_id":"call_1","output":{"stdout":"SHELL_OK\r\n","stderr":"","exit_code":0}}
		]
	}`)
	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("fixture", raw, false)
	if got := gjson.GetBytes(out, "messages.0.tool_calls.0.function.name").String(); got != reservedLocalShellChatToolName {
		t.Fatalf("replayed call name = %q, want %q; messages=%s", got, reservedLocalShellChatToolName, gjson.GetBytes(out, "messages").Raw)
	}
	args := gjson.GetBytes(out, "messages.0.tool_calls.0.function.arguments").String()
	if !gjson.Valid(args) {
		t.Fatalf("replayed arguments are not valid JSON: %q", args)
	}
	if got := gjson.Get(args, "command.1").String(); got != "SHELL_OK" {
		t.Fatalf("replayed command = %q, want SHELL_OK", got)
	}
	if got := gjson.GetBytes(out, "messages.1.role").String(); got != "tool" {
		t.Fatalf("replayed output role = %q, want tool", got)
	}
	if got := gjson.GetBytes(out, "messages.1.tool_call_id").String(); got != "call_1" {
		t.Fatalf("replayed output call id = %q, want call_1", got)
	}
	var content string
	if err := json.Unmarshal([]byte(gjson.GetBytes(out, "messages.1.content").Raw), &content); err != nil {
		t.Fatalf("replayed output content is not JSON string: %v", err)
	}
	if !strings.Contains(content, "SHELL_OK") {
		t.Fatalf("replayed output missing stdout: %q", content)
	}
}

func TestResponsesLocalShellNonStreamCall(t *testing.T) {
	request := []byte(`{"tools":[{"type":"shell","environment":{"type":"local"}}]}`)
	response := []byte(`{"id":"resp_shell","object":"chat.completion","choices":[{"message":{"tool_calls":[{"id":"call_1","type":"function","function":{"name":"__cpa_local_shell","arguments":"{\"command\":[\"Write-Output\",\"SHELL_OK\"],\"env\":{},\"timeout_ms\":12,\"working_directory\":\"O:\\\\cpa\"}"}}]}}]}`)
	out := ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(context.Background(), "fixture", request, request, response, nil)
	if got := gjson.GetBytes(out, "output.0.type").String(); got != "local_shell_call" {
		t.Fatalf("output type = %q, want local_shell_call; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "output.0.action.command.1").String(); got != "SHELL_OK" {
		t.Fatalf("command[1] = %q, want SHELL_OK", got)
	}
	if got := gjson.GetBytes(out, "output.0.name").String(); got != "" {
		t.Fatalf("synthetic function name leaked to Responses output: %q", got)
	}
}

func TestResponsesLocalShellStreamingCall(t *testing.T) {
	request := []byte(`{"tools":[{"type":"shell","environment":{"type":"local"}}]}`)
	chunks := []string{
		`data: {"id":"resp_shell","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"__cpa_local_shell","arguments":""}}]}}]}`,
		`data: {"id":"resp_shell","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"command\":[\"Write-Output\",\"SHELL_OK\"]}"}}]}}]}`,
		`data: {"id":"resp_shell","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	}
	var param any
	var events [][]byte
	for _, chunk := range chunks {
		events = append(events, ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "fixture", request, request, []byte(chunk), &param)...)
	}
	var added, done, completed bool
	for _, event := range events {
		_, payload := parseOpenAIResponsesSSEEvent(t, event)
		switch payload.Get("type").String() {
		case "response.output_item.added":
			if payload.Get("item.type").String() == "local_shell_call" {
				added = true
			}
		case "response.output_item.done":
			if payload.Get("item.type").String() == "local_shell_call" &&
				payload.Get("item.action.command.1").String() == "SHELL_OK" {
				done = true
			}
		case "response.completed":
			for _, item := range payload.Get("response.output").Array() {
				if item.Get("type").String() == "local_shell_call" {
					completed = true
				}
			}
		}
	}
	if !added || !done || !completed {
		t.Fatalf("local shell stream events added=%v done=%v completed=%v", added, done, completed)
	}
}

func TestResponsesLocalShellAvoidsReservedNameCollision(t *testing.T) {
	raw := []byte(`{
		"tools":[
			{"type":"function","name":"__cpa_local_shell","parameters":{"type":"object"}},
			{"type":"shell","environment":{"type":"local"}}
		]
	}`)
	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("fixture", raw, false)
	if got := gjson.GetBytes(out, "tools.#").Int(); got != 2 {
		t.Fatalf("translated tool count = %d, want 2; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "tools.1.function.name").String(); got != "__cpa_local_shell_1" {
		t.Fatalf("synthetic shell name = %q, want __cpa_local_shell_1", got)
	}
}
