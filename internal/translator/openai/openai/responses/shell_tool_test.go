package responses

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestLocalShellDeclarationSurvivesChatTranslation(t *testing.T) {
	request := []byte(`{"tools":[{"type":"shell","environment":{"type":"local"}}],"tool_choice":"auto","input":"Run echo SHELL_OK"}`)
	got := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("test", request, false)
	if gjson.GetBytes(got, "tools.#").Int() != 1 {
		t.Fatalf("local shell declaration was dropped from chat request: %s", got)
	}
	if gjson.GetBytes(got, "tools.0.function.parameters.properties.commands.items.type").String() != "string" {
		t.Fatalf("shell action must expose commands as strings: %s", got)
	}
}

const shellRequest = `{"tools":[{"type":"shell","environment":{"type":"local"}},{"type":"function","name":"__cpa_local_shell","parameters":{}},{"type":"custom","name":"__cpa_local_shell_1"},{"type":"namespace","name":"ns","tools":[{"type":"function","name":"lookup","parameters":{}}]}],"tool_choice":{"type":"shell"}}`

func shellUpstreamCall(name, args string) []byte {
	call := []byte(`{"index":0,"id":"call_shell","type":"function","function":{}}`)
	call, _ = sjson.SetBytes(call, "function.name", name)
	call, _ = sjson.SetBytes(call, "function.arguments", args)
	return call
}

func shellNonStream(request []byte, name, args string) []byte {
	upstream := []byte(`{"id":"resp_test","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[]}}]}`)
	upstream, _ = sjson.SetRawBytes(upstream, "choices.0.message.tool_calls.0", shellUpstreamCall(name, args))
	return ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(context.Background(), "test", request, nil, upstream, nil)
}

func shellStream(request []byte, name, args string) []gjson.Result {
	var state any
	var events []gjson.Result
	push := func(chunk []byte) {
		for _, output := range ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "test", request, nil, chunk, &state) {
			for _, line := range strings.Split(string(output), "\n") {
				if strings.HasPrefix(line, "data: ") {
					events = append(events, gjson.Parse(strings.TrimPrefix(line, "data: ")))
				}
			}
		}
	}
	for i, fragment := range []string{args[:len(args)/2], args[len(args)/2:]} {
		chunk := []byte(`{"id":"resp_test","choices":[{"index":0,"delta":{"tool_calls":[]}}]}`)
		call := shellUpstreamCall(name, fragment)
		if i == 1 {
			call, _ = sjson.DeleteBytes(call, "id")
			call, _ = sjson.DeleteBytes(call, "function.name")
		}
		chunk, _ = sjson.SetRawBytes(chunk, "choices.0.delta.tool_calls.0", call)
		push(append([]byte("data: "), chunk...))
	}
	push([]byte(`data: {"id":"resp_test","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`))
	push([]byte("data: [DONE]"))
	return events
}

func TestLocalShellRoundTrip(t *testing.T) {
	request := []byte(shellRequest)
	chat := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("test", request, false)
	name := gjson.GetBytes(chat, "tools.0.function.name").String()
	if name == "" || name == "__cpa_local_shell" || name == "__cpa_local_shell_1" || gjson.GetBytes(chat, "tools.#").Int() != 4 {
		t.Fatalf("synthetic tool collided with user tools: %s", chat)
	}
	if gjson.GetBytes(chat, "tool_choice.function.name").String() != name {
		t.Fatalf("shell tool_choice not mapped: %s", chat)
	}
	args := `{"commands":["echo SHELL_OK","pwd"],"timeout_ms":120000,"max_output_length":4096}`
	nonstream := shellNonStream(request, name, args)
	item := gjson.GetBytes(nonstream, "output.0")
	if item.Get("type").String() != "shell_call" || item.Get("action.commands.0").String() != "echo SHELL_OK" || item.Get("status").String() != "completed" || item.Get("name").Exists() || item.Get("arguments").Exists() {
		t.Fatalf("invalid shell item: %s", nonstream)
	}
	var done, completed gjson.Result
	for _, event := range shellStream(request, name, args) {
		switch event.Get("type").String() {
		case "response.output_item.added":
			if event.Get("item.type").String() != "shell_call" {
				t.Fatalf("wrong added item: %s", event.Raw)
			}
		case "response.output_item.done":
			done = event.Get("item")
		case "response.completed":
			completed = event.Get("response.output.0")
		case "response.function_call_arguments.delta", "response.function_call_arguments.done":
			t.Fatalf("shell leaked function event: %s", event.Raw)
		}
	}
	if !done.Exists() || !reflect.DeepEqual(done.Value(), item.Value()) || !reflect.DeepEqual(done.Value(), completed.Value()) {
		t.Fatalf("inconsistent final items: nonstream=%s done=%s completed=%s", item.Raw, done.Raw, completed.Raw)
	}
	output := `{"type":"shell_call_output","call_id":"call_shell","max_output_length":4096,"output":[{"stdout":"SHELL_OK\n","stderr":"warning","outcome":{"type":"exit","exit_code":0}},{"stdout":"partial","stderr":"","outcome":{"type":"timeout"}}]}`
	replay, _ := sjson.SetRawBytes(request, "input", []byte("["+item.Raw+`,`+`{"type":"function_call","call_id":"call_user","name":"__cpa_local_shell","arguments":"{}"},`+output+`,`+`{"type":"function_call_output","call_id":"call_user","output":"ok"}]`))
	replayed := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("test", replay, false)
	if gjson.GetBytes(replayed, "messages.0.tool_calls.#").Int() != 2 || gjson.GetBytes(replayed, "messages.0.tool_calls.0.function.name").String() != name || gjson.GetBytes(replayed, "messages.0.tool_calls.0.function.arguments").String() != args {
		t.Fatalf("shell history not grouped/replayed: %s", replayed)
	}
	content := gjson.GetBytes(replayed, "messages.1.content").String()
	if gjson.GetBytes(replayed, "messages.1.role").String() != "tool" || !reflect.DeepEqual(gjson.Parse(content).Value(), gjson.Parse(output).Value()) {
		t.Fatalf("shell output envelope lost: %s", replayed)
	}
	for _, tc := range []struct{ name, kind string }{{"__cpa_local_shell", "function_call"}, {"__cpa_local_shell_1", "custom_tool_call"}, {"ns__lookup", "function_call"}} {
		got := shellNonStream(request, tc.name, `{"input":"hello"}`)
		if gjson.GetBytes(got, "output.0.type").String() != tc.kind {
			t.Fatalf("user tool misclassified: %s", got)
		}
	}
	unknown := shellNonStream([]byte(`{}`), name, args)
	if gjson.GetBytes(unknown, "output.0.type").String() != "function_call" {
		t.Fatalf("synthetic name recognized without provenance: %s", unknown)
	}
}

func TestLocalShellInvalidActions(t *testing.T) {
	request := []byte(`{"tools":[{"type":"shell","environment":{"type":"local"}}]}`)
	for _, args := range []string{`{`, `{}`, `{"commands":[]}`, `{"commands":[42]}`, `{"commands":[" "]}`, `{"command":["echo","ok"]}`, `{"commands":["pwd"],"timeout_ms":-1}`, `{"commands":["pwd"],"timeout_ms":1.5}`, `{"commands":["pwd"],"env":{}}`} {
		t.Run(args, func(t *testing.T) {
			got := shellNonStream(request, "__cpa_local_shell", args)
			if gjson.GetBytes(got, "status").String() != "failed" || !strings.Contains(gjson.GetBytes(got, "error.message").String(), "shell") {
				t.Fatalf("invalid action accepted: %s", got)
			}
			failed := false
			for _, event := range shellStream(request, "__cpa_local_shell", args) {
				if event.Get("type").String() == "response.failed" {
					failed = true
				}
				if event.Get("type").String() == "response.completed" || event.Get("type").String() == "response.output_item.done" {
					t.Fatalf("invalid action completed: %s", event.Raw)
				}
			}
			if !failed {
				t.Fatal("invalid action did not fail streaming")
			}
		})
	}
}

func TestLocalShellOnlyExplicitLocalEnvironment(t *testing.T) {
	for _, environment := range []string{`{"type":"container_auto"}`, `{"type":"container_reference","container_id":"cntr_x"}`, `{}`} {
		request := []byte(`{"tools":[{"type":"shell","environment":` + environment + `}]}`)
		got := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("test", request, false)
		if gjson.GetBytes(got, "tools.#").Int() != 0 {
			t.Fatalf("hosted shell represented as local: %s", got)
		}
	}
	for _, choice := range []string{`"auto"`, `"required"`, `"none"`} {
		request := []byte(`{"tools":[{"type":"shell","environment":{"type":"local"}}],"tool_choice":` + choice + `}`)
		got := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("test", request, false)
		var expected string
		if errUnmarshal := json.Unmarshal([]byte(choice), &expected); errUnmarshal != nil {
			t.Fatal(errUnmarshal)
		}
		if gjson.GetBytes(got, "tool_choice").String() != expected {
			t.Fatalf("choice changed: %s", got)
		}
	}
}

func TestLocalShellHistoryWithoutDeclaration(t *testing.T) {
	for _, tools := range []string{``, `,"tools":[]`, `,"tools":[{"type":"function","name":"__cpa_local_shell_1","parameters":{}}]`} {
		request := []byte(`{"input":[{"type":"shell_call","call_id":"shell_1","action":{"commands":["pwd"]}},{"type":"shell_call_output","call_id":"shell_1","output":[{"stdout":"/workspace","stderr":"","outcome":{"type":"exit","exit_code":0}}]},{"type":"function_call","call_id":"user_1","name":"__cpa_local_shell","arguments":"{}"},{"type":"function_call_output","call_id":"user_1","output":"ok"}]` + tools + `}`)
		got := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("test", request, false)
		calls := gjson.GetBytes(got, "messages.0.tool_calls").Array()
		if len(calls) != 1 || calls[0].Get("id").String() != "shell_1" || gjson.GetBytes(got, "messages.2.tool_calls.0.id").String() != "user_1" {
			t.Fatalf("shell history dropped without declaration: %s", got)
		}
		name := calls[0].Get("function.name").String()
		if name == "__cpa_local_shell" || name == "__cpa_local_shell_1" && tools != "" && tools != `,"tools":[]` {
			t.Fatalf("history name collided: %s", got)
		}
		if gjson.Parse(gjson.GetBytes(got, "messages.1.content").String()).Get("output.0.stdout").String() != "/workspace" {
			t.Fatalf("shell result lost: %s", got)
		}
		if gjson.GetBytes(got, "tools.#").Int() != gjson.GetBytes(request, "tools.#").Int() {
			t.Fatalf("history re-enabled a tool: %s", got)
		}
	}
}
