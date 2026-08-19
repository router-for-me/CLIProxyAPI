package responses

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func parseOpenAIResponsesSSEEvent(t *testing.T, chunk []byte) (string, gjson.Result) {
	t.Helper()

	lines := strings.Split(string(chunk), "\n")
	if len(lines) < 2 {
		t.Fatalf("unexpected SSE chunk: %q", chunk)
	}

	event := strings.TrimSpace(strings.TrimPrefix(lines[0], "event:"))
	dataLine := strings.TrimSpace(strings.TrimPrefix(lines[1], "data:"))
	if !gjson.Valid(dataLine) {
		t.Fatalf("invalid SSE data JSON: %q", dataLine)
	}
	return event, gjson.Parse(dataLine)
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponses_ResponseCompletedWaitsForDone(t *testing.T) {
	t.Parallel()

	request := []byte(`{"model":"gpt-5.4","tool_choice":"auto","parallel_tool_calls":true}`)

	tests := []struct {
		name           string
		in             []string
		doneInputIndex int // Index in tt.in where the terminal [DONE] chunk arrives and response.completed must be emitted.
		hasUsage       bool
		inputTokens    int64
		outputTokens   int64
		totalTokens    int64
	}{
		{
			// A provider may send finish_reason first and only attach usage in a later chunk (e.g. Vertex AI),
			// so response.completed must wait for [DONE] to include that usage.
			name: "late usage after finish reason",
			in: []string{
				`data: {"id":"resp_late_usage","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":"assistant","content":null,"reasoning_content":null,"tool_calls":[{"index":0,"id":"call_late_usage","type":"function","function":{"name":"read","arguments":""}}]},"finish_reason":null}]}`,
				`data: {"id":"resp_late_usage","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":null,"content":null,"reasoning_content":null,"tool_calls":[{"index":0,"function":{"arguments":"{\"filePath\":\"C:\\\\repo\\\\README.md\"}"}}]},"finish_reason":"tool_calls"}]}`,
				`data: {"id":"resp_late_usage","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`,
				`data: [DONE]`,
			},
			doneInputIndex: 3,
			hasUsage:       true,
			inputTokens:    11,
			outputTokens:   7,
			totalTokens:    18,
		},
		{
			// When usage arrives on the same chunk as finish_reason, we still expect a
			// single response.completed event and it should remain deferred until [DONE].
			name: "usage on finish reason chunk",
			in: []string{
				`data: {"id":"resp_usage_same_chunk","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":"assistant","content":null,"reasoning_content":null,"tool_calls":[{"index":0,"id":"call_usage_same_chunk","type":"function","function":{"name":"read","arguments":""}}]},"finish_reason":null}]}`,
				`data: {"id":"resp_usage_same_chunk","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":null,"content":null,"reasoning_content":null,"tool_calls":[{"index":0,"function":{"arguments":"{\"filePath\":\"C:\\\\repo\\\\README.md\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":13,"completion_tokens":5,"total_tokens":18}}`,
				`data: [DONE]`,
			},
			doneInputIndex: 2,
			hasUsage:       true,
			inputTokens:    13,
			outputTokens:   5,
			totalTokens:    18,
		},
		{
			name: "no finish reason",
			in: []string{
				`data: {"id":"resp_no_finish_reason","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"}}]}`,
				`data: [DONE]`,
			},
			doneInputIndex: 1,
			hasUsage:       false,
		},
		{
			// An OpenAI-compatible streams from a buggy server might never send usage, so response.completed should
			// still wait for [DONE] but omit the usage object entirely.
			name: "no usage chunk",
			in: []string{
				`data: {"id":"resp_no_usage","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":"assistant","content":null,"reasoning_content":null,"tool_calls":[{"index":0,"id":"call_no_usage","type":"function","function":{"name":"read","arguments":""}}]},"finish_reason":null}]}`,
				`data: {"id":"resp_no_usage","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":null,"content":null,"reasoning_content":null,"tool_calls":[{"index":0,"function":{"arguments":"{\"filePath\":\"C:\\\\repo\\\\README.md\"}"}}]},"finish_reason":"tool_calls"}]}`,
				`data: [DONE]`,
			},
			doneInputIndex: 2,
			hasUsage:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			completedCount := 0
			completedInputIndex := -1
			var createdData gjson.Result
			var inProgressData gjson.Result
			var completedData gjson.Result

			// Reuse converter state across input lines to simulate one streaming response.
			var param any

			for i, line := range tt.in {
				// One upstream chunk can emit multiple downstream SSE events.
				for _, chunk := range ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "model", request, request, []byte(line), &param) {
					event, data := parseOpenAIResponsesSSEEvent(t, chunk)
					if event == "response.created" {
						createdData = data
						continue
					}
					if event == "response.in_progress" {
						inProgressData = data
						continue
					}
					if event != "response.completed" {
						continue
					}

					completedCount++
					completedInputIndex = i
					completedData = data
					if i < tt.doneInputIndex {
						t.Fatalf("unexpected early response.completed on input index %d", i)
					}
				}
			}

			if completedCount != 1 {
				t.Fatalf("expected exactly 1 response.completed event, got %d", completedCount)
			}
			if completedInputIndex != tt.doneInputIndex {
				t.Fatalf("expected response.completed on terminal [DONE] chunk at input index %d, got %d", tt.doneInputIndex, completedInputIndex)
			}
			if got := createdData.Get("response.model").String(); got != "gpt-5.4" {
				t.Fatalf("response.created models = %q, want gpt-5.4", got)
			}
			if got := inProgressData.Get("response.model").String(); got != "gpt-5.4" {
				t.Fatalf("response.in_progress models = %q, want gpt-5.4", got)
			}

			// Missing upstream usage should stay omitted in the final completed event.
			if !tt.hasUsage {
				if completedData.Get("response.usage").Exists() {
					t.Fatalf("expected response.completed to omit usage when none was provided, got %s", completedData.Get("response.usage").Raw)
				}
				return
			}

			// When usage is present, the final response.completed event must preserve the usage values.
			if got := completedData.Get("response.usage.input_tokens").Int(); got != tt.inputTokens {
				t.Fatalf("unexpected response.usage.input_tokens: got %d want %d", got, tt.inputTokens)
			}
			if got := completedData.Get("response.usage.output_tokens").Int(); got != tt.outputTokens {
				t.Fatalf("unexpected response.usage.output_tokens: got %d want %d", got, tt.outputTokens)
			}
			if got := completedData.Get("response.usage.total_tokens").Int(); got != tt.totalTokens {
				t.Fatalf("unexpected response.usage.total_tokens: got %d want %d", got, tt.totalTokens)
			}
		})
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponses_FinalizesOpenMessageAtStreamEnd(t *testing.T) {
	t.Parallel()

	request := []byte(`{"model":"gpt-5.4"}`)
	tests := []struct {
		name  string
		chunk string
	}{
		{
			name:  "missing finish reason",
			chunk: `data: {"id":"resp_missing_finish_reason","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"}}]}`,
		},
		{
			name:  "null finish reason",
			chunk: `data: {"id":"resp_null_finish_reason","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"},"finish_reason":null}]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var param any
			var events []string
			var textDone gjson.Result
			var partDone gjson.Result
			var itemDone gjson.Result
			var completed gjson.Result

			for _, line := range []string{tt.chunk, `data: [DONE]`} {
				for _, chunk := range ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "model", request, request, []byte(line), &param) {
					event, data := parseOpenAIResponsesSSEEvent(t, chunk)
					events = append(events, event)
					switch event {
					case "response.output_text.done":
						textDone = data
					case "response.content_part.done":
						partDone = data
					case "response.output_item.done":
						itemDone = data
					case "response.completed":
						completed = data
					}
				}
			}

			wantEvents := []string{
				"response.created",
				"response.in_progress",
				"response.output_item.added",
				"response.content_part.added",
				"response.output_text.delta",
				"response.output_text.done",
				"response.content_part.done",
				"response.output_item.done",
				"response.completed",
			}
			if len(events) != len(wantEvents) {
				t.Fatalf("events = %v, want %v", events, wantEvents)
			}
			for i := range wantEvents {
				if events[i] != wantEvents[i] {
					t.Fatalf("event %d = %q, want %q; events = %v", i, events[i], wantEvents[i], events)
				}
			}
			if got := textDone.Get("text").String(); got != "hello" {
				t.Fatalf("output_text.done text = %q, want hello", got)
			}
			if got := partDone.Get("part.text").String(); got != "hello" {
				t.Fatalf("content_part.done text = %q, want hello", got)
			}
			if got := itemDone.Get("item.content.0.text").String(); got != "hello" {
				t.Fatalf("output_item.done text = %q, want hello", got)
			}
			if got := completed.Get("response.status").String(); got != "completed" {
				t.Fatalf("response.completed status = %q, want completed", got)
			}
		})
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponses_MultipleToolCallsRemainSeparate(t *testing.T) {
	in := []string{
		`data: {"id":"resp_test","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":"assistant","content":null,"reasoning_content":null,"tool_calls":[{"index":0,"id":"call_read","type":"function","function":{"name":"read","arguments":""}}]},"finish_reason":null}]}`,
		`data: {"id":"resp_test","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":null,"content":null,"reasoning_content":null,"tool_calls":[{"index":0,"function":{"arguments":"{\"filePath\":\"C:\\\\repo\",\"limit\":400,\"offset\":1}"}}]},"finish_reason":null}]}`,
		`data: {"id":"resp_test","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":"assistant","content":null,"reasoning_content":null,"tool_calls":[{"index":1,"id":"call_glob","type":"function","function":{"name":"glob","arguments":""}}]},"finish_reason":null}]}`,
		`data: {"id":"resp_test","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":null,"content":null,"reasoning_content":null,"tool_calls":[{"index":1,"function":{"arguments":"{\"path\":\"C:\\\\repo\",\"pattern\":\"*.{yml,yaml}\"}"}}]},"finish_reason":null}]}`,
		`data: {"id":"resp_test","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":null,"content":null,"reasoning_content":null,"tool_calls":null},"finish_reason":"tool_calls"}],"usage":{"completion_tokens":10,"total_tokens":20,"prompt_tokens":10}}`,
		`data: [DONE]`,
	}

	request := []byte(`{"model":"gpt-5.4","tool_choice":"auto","parallel_tool_calls":true}`)

	var param any
	var out [][]byte
	for _, line := range in {
		out = append(out, ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "model", request, request, []byte(line), &param)...)
	}

	addedNames := map[string]string{}
	doneArgs := map[string]string{}
	doneNames := map[string]string{}
	outputItems := map[string]gjson.Result{}

	for _, chunk := range out {
		ev, data := parseOpenAIResponsesSSEEvent(t, chunk)
		switch ev {
		case "response.output_item.added":
			if data.Get("item.type").String() != "function_call" {
				continue
			}
			addedNames[data.Get("item.call_id").String()] = data.Get("item.name").String()
		case "response.output_item.done":
			if data.Get("item.type").String() != "function_call" {
				continue
			}
			callID := data.Get("item.call_id").String()
			doneArgs[callID] = data.Get("item.arguments").String()
			doneNames[callID] = data.Get("item.name").String()
		case "response.completed":
			output := data.Get("response.output")
			for _, item := range output.Array() {
				if item.Get("type").String() == "function_call" {
					outputItems[item.Get("call_id").String()] = item
				}
			}
		}
	}

	if len(addedNames) != 2 {
		t.Fatalf("expected 2 function_call added events, got %d", len(addedNames))
	}
	if len(doneArgs) != 2 {
		t.Fatalf("expected 2 function_call done events, got %d", len(doneArgs))
	}

	if addedNames["call_read"] != "read" {
		t.Fatalf("unexpected added name for call_read: %q", addedNames["call_read"])
	}
	if addedNames["call_glob"] != "glob" {
		t.Fatalf("unexpected added name for call_glob: %q", addedNames["call_glob"])
	}

	if !gjson.Valid(doneArgs["call_read"]) {
		t.Fatalf("invalid JSON args for call_read: %q", doneArgs["call_read"])
	}
	if !gjson.Valid(doneArgs["call_glob"]) {
		t.Fatalf("invalid JSON args for call_glob: %q", doneArgs["call_glob"])
	}
	if strings.Contains(doneArgs["call_read"], "}{") {
		t.Fatalf("call_read args were concatenated: %q", doneArgs["call_read"])
	}
	if strings.Contains(doneArgs["call_glob"], "}{") {
		t.Fatalf("call_glob args were concatenated: %q", doneArgs["call_glob"])
	}

	if doneNames["call_read"] != "read" {
		t.Fatalf("unexpected done name for call_read: %q", doneNames["call_read"])
	}
	if doneNames["call_glob"] != "glob" {
		t.Fatalf("unexpected done name for call_glob: %q", doneNames["call_glob"])
	}

	if got := gjson.Get(doneArgs["call_read"], "filePath").String(); got != `C:\repo` {
		t.Fatalf("unexpected filePath for call_read: %q", got)
	}
	if got := gjson.Get(doneArgs["call_glob"], "path").String(); got != `C:\repo` {
		t.Fatalf("unexpected path for call_glob: %q", got)
	}
	if got := gjson.Get(doneArgs["call_glob"], "pattern").String(); got != "*.{yml,yaml}" {
		t.Fatalf("unexpected pattern for call_glob: %q", got)
	}

	if len(outputItems) != 2 {
		t.Fatalf("expected 2 function_call items in response.output, got %d", len(outputItems))
	}
	if outputItems["call_read"].Get("name").String() != "read" {
		t.Fatalf("unexpected response.output name for call_read: %q", outputItems["call_read"].Get("name").String())
	}
	if outputItems["call_glob"].Get("name").String() != "glob" {
		t.Fatalf("unexpected response.output name for call_glob: %q", outputItems["call_glob"].Get("name").String())
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponses_MultiChoiceToolCallsUseDistinctOutputIndexes(t *testing.T) {
	in := []string{
		`data: {"id":"resp_multi_choice","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":"assistant","content":null,"reasoning_content":null,"tool_calls":[{"index":0,"id":"call_choice0","type":"function","function":{"name":"glob","arguments":""}}]},"finish_reason":null},{"index":1,"delta":{"role":"assistant","content":null,"reasoning_content":null,"tool_calls":[{"index":0,"id":"call_choice1","type":"function","function":{"name":"read","arguments":""}}]},"finish_reason":null}]}`,
		`data: {"id":"resp_multi_choice","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":null,"content":null,"reasoning_content":null,"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":\"C:\\\\repo\",\"pattern\":\"*.go\"}"}}]},"finish_reason":null},{"index":1,"delta":{"role":null,"content":null,"reasoning_content":null,"tool_calls":[{"index":0,"function":{"arguments":"{\"filePath\":\"C:\\\\repo\\\\README.md\",\"limit\":20,\"offset\":1}"}}]},"finish_reason":null}]}`,
		`data: {"id":"resp_multi_choice","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":null,"content":null,"reasoning_content":null,"tool_calls":null},"finish_reason":"tool_calls"},{"index":1,"delta":{"role":null,"content":null,"reasoning_content":null,"tool_calls":null},"finish_reason":"tool_calls"}],"usage":{"completion_tokens":10,"total_tokens":20,"prompt_tokens":10}}`,
		`data: [DONE]`,
	}

	request := []byte(`{"model":"gpt-5.4","tool_choice":"auto","parallel_tool_calls":true}`)

	var param any
	var out [][]byte
	for _, line := range in {
		out = append(out, ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "model", request, request, []byte(line), &param)...)
	}

	type fcEvent struct {
		outputIndex int64
		name        string
		arguments   string
	}

	added := map[string]fcEvent{}
	done := map[string]fcEvent{}

	for _, chunk := range out {
		ev, data := parseOpenAIResponsesSSEEvent(t, chunk)
		switch ev {
		case "response.output_item.added":
			if data.Get("item.type").String() != "function_call" {
				continue
			}
			callID := data.Get("item.call_id").String()
			added[callID] = fcEvent{
				outputIndex: data.Get("output_index").Int(),
				name:        data.Get("item.name").String(),
			}
		case "response.output_item.done":
			if data.Get("item.type").String() != "function_call" {
				continue
			}
			callID := data.Get("item.call_id").String()
			done[callID] = fcEvent{
				outputIndex: data.Get("output_index").Int(),
				name:        data.Get("item.name").String(),
				arguments:   data.Get("item.arguments").String(),
			}
		}
	}

	if len(added) != 2 {
		t.Fatalf("expected 2 function_call added events, got %d", len(added))
	}
	if len(done) != 2 {
		t.Fatalf("expected 2 function_call done events, got %d", len(done))
	}

	if added["call_choice0"].name != "glob" {
		t.Fatalf("unexpected added name for call_choice0: %q", added["call_choice0"].name)
	}
	if added["call_choice1"].name != "read" {
		t.Fatalf("unexpected added name for call_choice1: %q", added["call_choice1"].name)
	}
	if added["call_choice0"].outputIndex == added["call_choice1"].outputIndex {
		t.Fatalf("expected distinct output indexes for different choices, both got %d", added["call_choice0"].outputIndex)
	}

	if !gjson.Valid(done["call_choice0"].arguments) {
		t.Fatalf("invalid JSON args for call_choice0: %q", done["call_choice0"].arguments)
	}
	if !gjson.Valid(done["call_choice1"].arguments) {
		t.Fatalf("invalid JSON args for call_choice1: %q", done["call_choice1"].arguments)
	}
	if done["call_choice0"].outputIndex == done["call_choice1"].outputIndex {
		t.Fatalf("expected distinct done output indexes for different choices, both got %d", done["call_choice0"].outputIndex)
	}
	if done["call_choice0"].name != "glob" {
		t.Fatalf("unexpected done name for call_choice0: %q", done["call_choice0"].name)
	}
	if done["call_choice1"].name != "read" {
		t.Fatalf("unexpected done name for call_choice1: %q", done["call_choice1"].name)
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponses_MixedMessageAndToolUseDistinctOutputIndexes(t *testing.T) {
	in := []string{
		`data: {"id":"resp_mixed","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":"assistant","content":"hello","reasoning_content":null,"tool_calls":null},"finish_reason":null},{"index":1,"delta":{"role":"assistant","content":null,"reasoning_content":null,"tool_calls":[{"index":0,"id":"call_choice1","type":"function","function":{"name":"read","arguments":""}}]},"finish_reason":null}]}`,
		`data: {"id":"resp_mixed","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":null,"content":null,"reasoning_content":null,"tool_calls":null},"finish_reason":"stop"},{"index":1,"delta":{"role":null,"content":null,"reasoning_content":null,"tool_calls":[{"index":0,"function":{"arguments":"{\"filePath\":\"C:\\\\repo\\\\README.md\",\"limit\":20,\"offset\":1}"}}]},"finish_reason":"tool_calls"}],"usage":{"completion_tokens":10,"total_tokens":20,"prompt_tokens":10}}`,
		`data: [DONE]`,
	}

	request := []byte(`{"model":"gpt-5.4","tool_choice":"auto","parallel_tool_calls":true}`)

	var param any
	var out [][]byte
	for _, line := range in {
		out = append(out, ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "model", request, request, []byte(line), &param)...)
	}

	var messageOutputIndex int64 = -1
	var toolOutputIndex int64 = -1

	for _, chunk := range out {
		ev, data := parseOpenAIResponsesSSEEvent(t, chunk)
		if ev != "response.output_item.added" {
			continue
		}
		switch data.Get("item.type").String() {
		case "message":
			if data.Get("item.id").String() == "msg_resp_mixed_0" {
				messageOutputIndex = data.Get("output_index").Int()
			}
		case "function_call":
			if data.Get("item.call_id").String() == "call_choice1" {
				toolOutputIndex = data.Get("output_index").Int()
			}
		}
	}

	if messageOutputIndex < 0 {
		t.Fatal("did not find message output index")
	}
	if toolOutputIndex < 0 {
		t.Fatal("did not find tool output index")
	}
	if messageOutputIndex == toolOutputIndex {
		t.Fatalf("expected distinct output indexes for message and tool call, both got %d", messageOutputIndex)
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponses_CompletedOmitsTopLevelOutputText(t *testing.T) {
	in := []string{
		`data: {"id":"resp_output_text","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":"assistant","content":"hello ","reasoning_content":null,"tool_calls":null},"finish_reason":null}]}`,
		`data: {"id":"resp_output_text","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":null,"content":"world","reasoning_content":null,"tool_calls":null},"finish_reason":"stop"}],"usage":{"completion_tokens":2,"total_tokens":4,"prompt_tokens":2}}`,
		`data: [DONE]`,
	}

	request := []byte(`{"model":"gpt-5.4"}`)

	var param any
	var completed gjson.Result
	for _, line := range in {
		for _, chunk := range ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "model", request, request, []byte(line), &param) {
			ev, data := parseOpenAIResponsesSSEEvent(t, chunk)
			if ev == "response.completed" {
				completed = data
			}
		}
	}

	if !completed.Exists() {
		t.Fatal("expected response.completed event")
	}
	if completed.Get("response.output_text").Exists() {
		t.Fatalf("response.output_text should be omitted to match native Responses output: %s", completed.Get("response.output_text").Raw)
	}
	if got := completed.Get("response.output.0.content.0.text").String(); got != "hello world" {
		t.Fatalf("response.output text = %q, want %q", got, "hello world")
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponses_ToolCallCompletedOmitsTopLevelOutputText(t *testing.T) {
	in := []string{
		`data: {"id":"resp_tool_output_text","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":"assistant","content":"I will call the weather tool.","reasoning_content":null,"tool_calls":null},"finish_reason":null}]}`,
		`data: {"id":"resp_tool_output_text","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":"assistant","content":null,"reasoning_content":null,"tool_calls":[{"index":0,"id":"call_weather","type":"function","function":{"name":"get_weather","arguments":""}}]},"finish_reason":null}]}`,
		`data: {"id":"resp_tool_output_text","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":null,"content":null,"reasoning_content":null,"tool_calls":[{"index":0,"function":{"arguments":"{\"location\":\"北京\",\"unit\":\"celsius\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"completion_tokens":10,"total_tokens":20,"prompt_tokens":10}}`,
		`data: [DONE]`,
	}

	request := []byte(`{"model":"gpt-5.4","tool_choice":"auto","parallel_tool_calls":true}`)

	var param any
	var completed gjson.Result
	for _, line := range in {
		for _, chunk := range ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "model", request, request, []byte(line), &param) {
			ev, data := parseOpenAIResponsesSSEEvent(t, chunk)
			if ev == "response.completed" {
				completed = data
			}
		}
	}

	if !completed.Exists() {
		t.Fatal("expected response.completed event")
	}
	if completed.Get("response.output_text").Exists() {
		t.Fatalf("response.output_text should be omitted to match native Responses output: %s", completed.Get("response.output_text").Raw)
	}
	if got := completed.Get("response.output.0.content.0.text").String(); got != "I will call the weather tool." {
		t.Fatalf("response output text = %q, want %q", got, "I will call the weather tool.")
	}
	if got := completed.Get("response.output.1.arguments").String(); !strings.Contains(got, "北京") {
		t.Fatalf("response function call arguments = %q, want Beijing argument", got)
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponses_FunctionCallDoneAndCompletedOutputStayAscending(t *testing.T) {
	in := []string{
		`data: {"id":"resp_order","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":"assistant","content":null,"reasoning_content":null,"tool_calls":[{"index":0,"id":"call_glob","type":"function","function":{"name":"glob","arguments":""}}]},"finish_reason":null}]}`,
		`data: {"id":"resp_order","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":null,"content":null,"reasoning_content":null,"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":\"C:\\\\repo\",\"pattern\":\"*.go\"}"}}]},"finish_reason":null}]}`,
		`data: {"id":"resp_order","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":"assistant","content":null,"reasoning_content":null,"tool_calls":[{"index":1,"id":"call_read","type":"function","function":{"name":"read","arguments":""}}]},"finish_reason":null}]}`,
		`data: {"id":"resp_order","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":null,"content":null,"reasoning_content":null,"tool_calls":[{"index":1,"function":{"arguments":"{\"filePath\":\"C:\\\\repo\\\\README.md\",\"limit\":20,\"offset\":1}"}}]},"finish_reason":null}]}`,
		`data: {"id":"resp_order","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":null,"content":null,"reasoning_content":null,"tool_calls":null},"finish_reason":"tool_calls"}],"usage":{"completion_tokens":10,"total_tokens":20,"prompt_tokens":10}}`,
		`data: [DONE]`,
	}

	request := []byte(`{"model":"gpt-5.4","tool_choice":"auto","parallel_tool_calls":true}`)

	var param any
	var out [][]byte
	for _, line := range in {
		out = append(out, ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "model", request, request, []byte(line), &param)...)
	}

	var doneIndexes []int64
	var completedOrder []string

	for _, chunk := range out {
		ev, data := parseOpenAIResponsesSSEEvent(t, chunk)
		switch ev {
		case "response.output_item.done":
			if data.Get("item.type").String() == "function_call" {
				doneIndexes = append(doneIndexes, data.Get("output_index").Int())
			}
		case "response.completed":
			for _, item := range data.Get("response.output").Array() {
				if item.Get("type").String() == "function_call" {
					completedOrder = append(completedOrder, item.Get("call_id").String())
				}
			}
		}
	}

	if len(doneIndexes) != 2 {
		t.Fatalf("expected 2 function_call done indexes, got %d", len(doneIndexes))
	}
	if doneIndexes[0] >= doneIndexes[1] {
		t.Fatalf("expected ascending done output indexes, got %v", doneIndexes)
	}
	if len(completedOrder) != 2 {
		t.Fatalf("expected 2 function_call items in completed output, got %d", len(completedOrder))
	}
	if completedOrder[0] != "call_glob" || completedOrder[1] != "call_read" {
		t.Fatalf("unexpected completed function_call order: %v", completedOrder)
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream_OmitsTopLevelOutputText(t *testing.T) {
	request := []byte(`{"model":"gpt-5.4"}`)
	raw := []byte(`{"id":"chatcmpl_output_text","object":"chat.completion","created":1773896263,"model":"model","choices":[{"index":0,"message":{"role":"assistant","content":"ping"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)

	resp := ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(context.Background(), "model", request, request, raw, nil)
	data := gjson.ParseBytes(resp)

	if data.Get("output_text").Exists() {
		t.Fatalf("output_text should be omitted to match native Responses output: %s", resp)
	}
	if got := data.Get("output.0.content.0.text").String(); got != "ping" {
		t.Fatalf("output text = %q, want %q; response=%s", got, "ping", resp)
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponses_RestoresNamespaceFunctionCall(t *testing.T) {
	originalRequest := []byte(`{
		"model":"deepseek-v4-flash",
		"tools":[
			{
				"type":"namespace",
				"name":"mcp__test_mcp__",
				"tools":[{"type":"function","name":"add_numbers","parameters":{"type":"object","properties":{}}}]
			}
		]
	}`)
	chunks := []string{
		`data: {"id":"chatcmpl_namespace_stream","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_ns","type":"function","function":{"name":"mcp__test_mcp__add_numbers","arguments":""}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_namespace_stream","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"a\":3,\"b\":5}"}}]},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	}

	var param any
	var added gjson.Result
	var done gjson.Result
	var completed gjson.Result
	for _, line := range chunks {
		for _, chunk := range ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "model", originalRequest, nil, []byte(line), &param) {
			event, data := parseOpenAIResponsesSSEEvent(t, chunk)
			switch event {
			case "response.output_item.added":
				if data.Get("item.type").String() == "function_call" {
					added = data
				}
			case "response.output_item.done":
				if data.Get("item.type").String() == "function_call" {
					done = data
				}
			case "response.completed":
				completed = data
			}
		}
	}

	for _, tc := range []struct {
		label string
		got   gjson.Result
	}{
		{"added", added},
		{"done", done},
	} {
		if !tc.got.Exists() {
			t.Fatalf("expected function_call %s event", tc.label)
		}
		if got := tc.got.Get("item.name").String(); got != "add_numbers" {
			t.Fatalf("%s item.name = %q, want add_numbers", tc.label, got)
		}
		if got := tc.got.Get("item.namespace").String(); got != "mcp__test_mcp__" {
			t.Fatalf("%s item.namespace = %q, want mcp__test_mcp__", tc.label, got)
		}
	}
	if !completed.Exists() {
		t.Fatal("expected response.completed event")
	}
	if got := completed.Get("response.output.0.name").String(); got != "add_numbers" {
		t.Fatalf("completed output name = %q, want add_numbers", got)
	}
	if got := completed.Get("response.output.0.namespace").String(); got != "mcp__test_mcp__" {
		t.Fatalf("completed output namespace = %q, want mcp__test_mcp__", got)
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream_RestoresNamespaceFunctionCall(t *testing.T) {
	originalRequest := []byte(`{
		"model":"deepseek-v4-flash",
		"tools":[
			{
				"type":"namespace",
				"name":"mcp__test_mcp__",
				"tools":[{"type":"function","name":"add_numbers","parameters":{"type":"object","properties":{}}}]
			}
		]
	}`)
	raw := []byte(`{"id":"chatcmpl_namespace_nonstream","object":"chat.completion","created":1773896263,"model":"model","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call_ns","type":"function","function":{"name":"mcp__test_mcp__add_numbers","arguments":"{\"a\":3,\"b\":5}"}}]},"finish_reason":"tool_calls"}]}`)

	resp := ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(context.Background(), "model", originalRequest, nil, raw, nil)
	data := gjson.ParseBytes(resp)

	if got := data.Get("output.0.name").String(); got != "add_numbers" {
		t.Fatalf("non-stream output name = %q, want add_numbers; response=%s", got, resp)
	}
	if got := data.Get("output.0.namespace").String(); got != "mcp__test_mcp__" {
		t.Fatalf("non-stream output namespace = %q, want mcp__test_mcp__; response=%s", got, resp)
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponses_CustomToolNameArrivesLate(t *testing.T) {
	originalRequest := []byte(`{
		"model":"gpt-5.4",
		"tools":[{"type":"custom","name":"exec"}]
	}`)
	chunks := []string{
		`data: {"id":"chatcmpl_custom_late_name","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_exec","type":"function","function":{"arguments":""}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_custom_late_name","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"exec","arguments":""}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_custom_late_name","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"input\":\"pwd\"}"}}]},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	}

	var param any
	var added gjson.Result
	var inputDone gjson.Result
	var itemDone gjson.Result
	var completed gjson.Result
	for _, line := range chunks {
		for _, chunk := range ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "model", originalRequest, nil, []byte(line), &param) {
			event, data := parseOpenAIResponsesSSEEvent(t, chunk)
			switch event {
			case "response.output_item.added":
				if data.Get("item.call_id").String() == "call_exec" {
					added = data
				}
			case "response.custom_tool_call_input.done":
				inputDone = data
			case "response.output_item.done":
				if data.Get("item.call_id").String() == "call_exec" {
					itemDone = data
				}
			case "response.completed":
				completed = data
			case "response.function_call_arguments.delta", "response.function_call_arguments.done":
				t.Fatalf("unexpected function call event %q: %s", event, chunk)
			}
		}
	}

	for _, tc := range []struct {
		label string
		got   gjson.Result
		path  string
	}{
		{"added", added, "item"},
		{"done", itemDone, "item"},
		{"completed", completed, "response.output.0"},
	} {
		if !tc.got.Exists() {
			t.Fatalf("expected %s event", tc.label)
		}
		if got := tc.got.Get(tc.path + ".type").String(); got != "custom_tool_call" {
			t.Fatalf("%s type = %q, want custom_tool_call", tc.label, got)
		}
		if got := tc.got.Get(tc.path + ".id").String(); got != "ctc_call_exec" {
			t.Fatalf("%s id = %q, want ctc_call_exec", tc.label, got)
		}
		if got := tc.got.Get(tc.path + ".name").String(); got != "exec" {
			t.Fatalf("%s name = %q, want exec", tc.label, got)
		}
	}
	if got := inputDone.Get("item_id").String(); got != "ctc_call_exec" {
		t.Fatalf("custom input done item_id = %q, want ctc_call_exec", got)
	}
	if got := inputDone.Get("input").String(); got != "pwd" {
		t.Fatalf("custom input done input = %q, want pwd", got)
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponses_CustomToolNameAndIDAreMissing(t *testing.T) {
	originalRequest := []byte(`{"model":"gpt-5.4","tools":[{"type":"custom","name":"exec"}]}`)
	chunks := []string{
		`data: {"id":"chatcmpl_custom_missing_fields","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"type":"function","function":{"arguments":"{\"input\":\"pwd\"}"}}]},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	}

	var param any
	var added gjson.Result
	var done gjson.Result
	var completed gjson.Result
	for _, line := range chunks {
		for _, chunk := range ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "model", originalRequest, nil, []byte(line), &param) {
			event, data := parseOpenAIResponsesSSEEvent(t, chunk)
			switch event {
			case "response.output_item.added":
				added = data
			case "response.output_item.done":
				done = data
			case "response.completed":
				completed = data
			}
		}
	}

	wantCallID := "call_chatcmpl_custom_missing_fields_0_0"
	for _, tc := range []struct {
		label string
		got   gjson.Result
		path  string
	}{
		{"added", added, "item"},
		{"done", done, "item"},
		{"completed", completed, "response.output.0"},
	} {
		if got := tc.got.Get(tc.path + ".type").String(); got != "custom_tool_call" {
			t.Fatalf("%s type = %q, want custom_tool_call", tc.label, got)
		}
		if got := tc.got.Get(tc.path + ".id").String(); got != "ctc_"+wantCallID {
			t.Fatalf("%s id = %q, want %q", tc.label, got, "ctc_"+wantCallID)
		}
		if got := tc.got.Get(tc.path + ".call_id").String(); got != wantCallID {
			t.Fatalf("%s call_id = %q, want %q", tc.label, got, wantCallID)
		}
		if got := tc.got.Get(tc.path + ".name").String(); got != "exec" {
			t.Fatalf("%s name = %q, want exec", tc.label, got)
		}
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponses_ToolCallIDMayArriveLateOrBeMissing(t *testing.T) {
	tests := []struct {
		name       string
		chunks     []string
		wantCallID string
	}{
		{
			name: "late id",
			chunks: []string{
				`data: {"id":"chatcmpl_late_id","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"type":"function","function":{"name":"read","arguments":"{\"file"}}]},"finish_reason":null}]}`,
				`data: {"id":"chatcmpl_late_id","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_late","function":{"arguments":"Path\":\"README.md\"}"}}]},"finish_reason":"tool_calls"}]}`,
			},
			wantCallID: "call_late",
		},
		{
			name: "missing id",
			chunks: []string{
				`data: {"id":"chatcmpl_missing_id","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"type":"function","function":{"name":"read","arguments":"{\"filePath\":\"README.md\"}"}}]},"finish_reason":"tool_calls"}]}`,
			},
			wantCallID: "call_chatcmpl_missing_id_0_0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var param any
			var events []string
			var added gjson.Result
			var argsDelta gjson.Result
			var argsDone gjson.Result
			var itemDone gjson.Result
			for _, line := range append(tt.chunks, `data: [DONE]`) {
				for _, chunk := range ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "model", nil, nil, []byte(line), &param) {
					event, data := parseOpenAIResponsesSSEEvent(t, chunk)
					events = append(events, event)
					switch event {
					case "response.output_item.added":
						added = data
					case "response.function_call_arguments.delta":
						argsDelta = data
					case "response.function_call_arguments.done":
						argsDone = data
					case "response.output_item.done":
						itemDone = data
					}
				}
			}

			wantItemID := "fc_" + tt.wantCallID
			if got := added.Get("item.id").String(); got != wantItemID {
				t.Fatalf("added item id = %q, want %q; events=%v", got, wantItemID, events)
			}
			if got := added.Get("item.call_id").String(); got != tt.wantCallID {
				t.Fatalf("added call id = %q, want %q", got, tt.wantCallID)
			}
			if got := argsDelta.Get("item_id").String(); got != wantItemID {
				t.Fatalf("arguments delta item id = %q, want %q", got, wantItemID)
			}
			if got := argsDelta.Get("delta").String(); got != `{"filePath":"README.md"}` {
				t.Fatalf("arguments delta = %q, want full buffered arguments", got)
			}
			if got := argsDone.Get("item_id").String(); got != wantItemID {
				t.Fatalf("arguments done item id = %q, want %q", got, wantItemID)
			}
			if got := itemDone.Get("item.id").String(); got != wantItemID {
				t.Fatalf("item done id = %q, want %q", got, wantItemID)
			}
		})
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponses_RestoresAdditionalNamespaceFunctionCall(t *testing.T) {
	originalRequest := []byte(`{
		"model":"gpt-5.4",
		"input":[{
			"type":"additional_tools",
			"tools":[{
				"type":"namespace",
				"name":"collaboration",
				"tools":[{"type":"function","name":"send_message","parameters":{"type":"object","properties":{}}}]
			}]
		}]
	}`)
	chunks := []string{
		`data: {"id":"chatcmpl_additional_namespace_stream","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_send","type":"function","function":{"name":"collaboration__send_message","arguments":""}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_additional_namespace_stream","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"target\":\"worker\",\"message\":\"ping\"}"}}]},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	}

	var param any
	var added gjson.Result
	var done gjson.Result
	var completed gjson.Result
	for _, line := range chunks {
		for _, chunk := range ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "model", originalRequest, nil, []byte(line), &param) {
			event, data := parseOpenAIResponsesSSEEvent(t, chunk)
			switch event {
			case "response.output_item.added":
				added = data
			case "response.output_item.done":
				done = data
			case "response.completed":
				completed = data
			}
		}
	}

	for _, tc := range []struct {
		label string
		got   gjson.Result
		path  string
	}{
		{"added", added, "item"},
		{"done", done, "item"},
		{"completed", completed, "response.output.0"},
	} {
		if got := tc.got.Get(tc.path + ".name").String(); got != "send_message" {
			t.Fatalf("%s name = %q, want send_message", tc.label, got)
		}
		if got := tc.got.Get(tc.path + ".namespace").String(); got != "collaboration" {
			t.Fatalf("%s namespace = %q, want collaboration", tc.label, got)
		}
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream_RestoresAdditionalNamespaceFunctionCall(t *testing.T) {
	originalRequest := []byte(`{
		"model":"gpt-5.4",
		"input":[{
			"type":"additional_tools",
			"tools":[{
				"type":"namespace",
				"name":"collaboration",
				"tools":[{"type":"function","name":"send_message","parameters":{"type":"object","properties":{}}}]
			}]
		}]
	}`)
	raw := []byte(`{"id":"chatcmpl_additional_namespace_nonstream","object":"chat.completion","created":1773896263,"model":"model","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call_send","type":"function","function":{"name":"collaboration__send_message","arguments":"{\"target\":\"worker\",\"message\":\"ping\"}"}}]},"finish_reason":"tool_calls"}]}`)

	resp := ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(context.Background(), "model", originalRequest, nil, raw, nil)
	data := gjson.ParseBytes(resp)
	if got := data.Get("output.0.name").String(); got != "send_message" {
		t.Fatalf("non-stream output name = %q, want send_message; response=%s", got, resp)
	}
	if got := data.Get("output.0.namespace").String(); got != "collaboration" {
		t.Fatalf("non-stream output namespace = %q, want collaboration; response=%s", got, resp)
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponses_RestoresAdditionalNamespaceCustomToolCall(t *testing.T) {
	originalRequest := []byte(`{
		"model":"gpt-5.4",
		"input":[{
			"type":"additional_tools",
			"tools":[{
				"type":"namespace",
				"name":"functions",
				"tools":[{"type":"custom","name":"exec"}]
			}]
		}]
	}`)
	chunks := []string{
		`data: {"id":"chatcmpl_additional_namespace_custom_stream","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_exec","type":"function","function":{"name":"functions__exec","arguments":""}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_additional_namespace_custom_stream","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"input\":\"pwd\"}"}}]},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	}

	var param any
	var added gjson.Result
	var inputDone gjson.Result
	var done gjson.Result
	var completed gjson.Result
	for _, line := range chunks {
		for _, chunk := range ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "model", originalRequest, nil, []byte(line), &param) {
			event, data := parseOpenAIResponsesSSEEvent(t, chunk)
			switch event {
			case "response.output_item.added":
				added = data
			case "response.custom_tool_call_input.done":
				inputDone = data
			case "response.output_item.done":
				done = data
			case "response.completed":
				completed = data
			case "response.function_call_arguments.delta", "response.function_call_arguments.done":
				t.Fatalf("unexpected function call event %q: %s", event, chunk)
			}
		}
	}

	for _, tc := range []struct {
		label string
		got   gjson.Result
		path  string
	}{
		{"added", added, "item"},
		{"done", done, "item"},
		{"completed", completed, "response.output.0"},
	} {
		if !tc.got.Exists() {
			t.Fatalf("expected %s event", tc.label)
		}
		if got := tc.got.Get(tc.path + ".type").String(); got != "custom_tool_call" {
			t.Fatalf("%s type = %q, want custom_tool_call", tc.label, got)
		}
		if got := tc.got.Get(tc.path + ".name").String(); got != "exec" {
			t.Fatalf("%s name = %q, want exec", tc.label, got)
		}
		if got := tc.got.Get(tc.path + ".namespace").String(); got != "functions" {
			t.Fatalf("%s namespace = %q, want functions", tc.label, got)
		}
	}
	if got := inputDone.Get("input").String(); got != "pwd" {
		t.Fatalf("custom input = %q, want pwd", got)
	}
	if got := done.Get("item.input").String(); got != "pwd" {
		t.Fatalf("done input = %q, want pwd", got)
	}
	if got := completed.Get("response.output.0.input").String(); got != "pwd" {
		t.Fatalf("completed input = %q, want pwd", got)
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream_RestoresAdditionalNamespaceCustomToolCall(t *testing.T) {
	originalRequest := []byte(`{
		"model":"gpt-5.4",
		"input":[{
			"type":"additional_tools",
			"tools":[{
				"type":"namespace",
				"name":"functions",
				"tools":[{"type":"custom","name":"exec"}]
			}]
		}]
	}`)
	raw := []byte(`{"id":"chatcmpl_additional_namespace_custom_nonstream","object":"chat.completion","created":1773896263,"model":"model","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call_exec","type":"function","function":{"name":"functions__exec","arguments":"{\"input\":\"pwd\"}"}}]},"finish_reason":"tool_calls"}]}`)

	resp := ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(context.Background(), "model", originalRequest, nil, raw, nil)
	data := gjson.ParseBytes(resp)
	if got := data.Get("output.0.type").String(); got != "custom_tool_call" {
		t.Fatalf("output type = %q, want custom_tool_call; response=%s", got, resp)
	}
	if got := data.Get("output.0.name").String(); got != "exec" {
		t.Fatalf("output name = %q, want exec; response=%s", got, resp)
	}
	if got := data.Get("output.0.namespace").String(); got != "functions" {
		t.Fatalf("output namespace = %q, want functions; response=%s", got, resp)
	}
	if got := data.Get("output.0.input").String(); got != "pwd" {
		t.Fatalf("output input = %q, want pwd; response=%s", got, resp)
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponses_DoesNotCompleteReasoningOnlyStream(t *testing.T) {
	request := []byte(`{"model":"deepseek-v4-flash"}`)
	chunks := []string{
		`data: {"id":"resp_reasoning_only","object":"chat.completion.chunk","created":1773896263,"model":"deepseek-v4-flash","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"still thinking"},"finish_reason":null}]}`,
		`data: [DONE]`,
	}

	var param any
	reasoningSeen := false
	for _, line := range chunks {
		for _, chunk := range ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "deepseek-v4-flash", request, request, []byte(line), &param) {
			event, _ := parseOpenAIResponsesSSEEvent(t, chunk)
			if event == "response.reasoning_summary_text.delta" {
				reasoningSeen = true
			}
			if event == "response.completed" {
				t.Fatalf("reasoning-only stream was finalized as response.completed: %s", chunk)
			}
		}
	}
	if !reasoningSeen {
		t.Fatal("test stream did not exercise reasoning output")
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponses_IncompleteToolStreamDoesNotFinalizeAsCompleted(t *testing.T) {
	request := []byte(`{"model":"gpt-5.6-terra"}`)

	tests := []struct {
		name   string
		chunks []string
	}{
		{
			name: "zero argument bytes without finish reason",
			chunks: []string{
				`data: {"id":"resp_interrupted_tool","object":"chat.completion.chunk","created":1773896263,"model":"gpt-5.6-terra","choices":[{"index":0,"delta":{"role":"assistant","content":null,"reasoning_content":null,"tool_calls":[{"index":0,"id":"call_patch","type":"function","function":{"name":"apply_patch","arguments":""}}]},"finish_reason":null}]}`,
				`data: [DONE]`,
			},
		},
		{
			name: "partial json arguments without finish reason",
			chunks: []string{
				`data: {"id":"resp_interrupted_partial","object":"chat.completion.chunk","created":1773896263,"model":"gpt-5.6-terra","choices":[{"index":0,"delta":{"role":"assistant","content":null,"reasoning_content":null,"tool_calls":[{"index":0,"id":"call_patch","type":"function","function":{"name":"apply_patch","arguments":"{\"filePath\":\"foo"}}]},"finish_reason":null}]}`,
				`data: [DONE]`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var param any
			for _, line := range tt.chunks {
				for _, chunk := range ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "gpt-5.6-terra", request, request, []byte(line), &param) {
					event, data := parseOpenAIResponsesSSEEvent(t, chunk)
					if event == "response.completed" {
						t.Fatalf("incomplete tool stream was finalized as response.completed: %s", chunk)
					}
					if event == "response.output_item.done" {
						t.Fatalf("incomplete tool stream emitted output_item.done: %s", chunk)
					}
					if event == "response.function_call_arguments.done" {
						t.Fatalf("incomplete tool stream emitted function_call_arguments.done: %s", chunk)
					}
					_ = data
				}
			}
		})
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponses_FinishReasonLengthEmitsIncomplete(t *testing.T) {
	request := []byte(`{"model":"gpt-5.6-luna"}`)
	chunks := []string{
		`data: {"id":"resp_length_tool","object":"chat.completion.chunk","created":1773896263,"model":"gpt-5.6-luna","choices":[{"index":0,"delta":{"role":"assistant","content":null,"reasoning_content":null,"tool_calls":[{"index":0,"id":"call_patch","type":"function","function":{"name":"apply_patch","arguments":""}}]},"finish_reason":null}]}`,
		`data: {"id":"resp_length_tool","object":"chat.completion.chunk","created":1773896263,"model":"gpt-5.6-luna","choices":[{"index":0,"delta":{},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
		`data: [DONE]`,
	}

	var param any
	var incompleteSeen bool
	var itemDoneSeen bool
	for _, line := range chunks {
		for _, chunk := range ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "gpt-5.6-luna", request, request, []byte(line), &param) {
			event, data := parseOpenAIResponsesSSEEvent(t, chunk)
			if event == "response.completed" {
				t.Fatalf("stream with finish_reason=length was finalized as response.completed: %s", chunk)
			}
			if event == "response.output_item.done" {
				itemDoneSeen = true
				if got := data.Get("item.status").String(); got != "incomplete" {
					t.Fatalf("item.status = %q, want incomplete", got)
				}
				if got := data.Get("item.arguments").String(); got == "{}" {
					t.Fatalf("item.arguments synthesized empty object {}, want raw args or empty string")
				}
			}
			if event == "response.incomplete" {
				incompleteSeen = true
				if got := data.Get("response.status").String(); got != "incomplete" {
					t.Fatalf("response.status = %q, want incomplete", got)
				}
				if got := data.Get("response.incomplete_details.reason").String(); got != "max_output_tokens" {
					t.Fatalf("response.incomplete_details.reason = %q, want max_output_tokens", got)
				}
				if got := data.Get("response.output.0.status").String(); got != "incomplete" {
					t.Fatalf("response.output.0.status = %q, want incomplete", got)
				}
			}
		}
	}
	if !itemDoneSeen {
		t.Fatal("expected response.output_item.done event for finish_reason=length")
	}
	if !incompleteSeen {
		t.Fatal("expected response.incomplete event for finish_reason=length")
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponses_FinishReasonContentFilterEmitsIncomplete(t *testing.T) {
	request := []byte(`{"model":"gpt-5.6-luna"}`)
	chunks := []string{
		`data: {"id":"resp_filter_tool","object":"chat.completion.chunk","created":1773896263,"model":"gpt-5.6-luna","choices":[{"index":0,"delta":{"role":"assistant","content":null,"reasoning_content":null,"tool_calls":[{"index":0,"id":"call_patch","type":"function","function":{"name":"apply_patch","arguments":""}}]},"finish_reason":null}]}`,
		`data: {"id":"resp_filter_tool","object":"chat.completion.chunk","created":1773896263,"model":"gpt-5.6-luna","choices":[{"index":0,"delta":{},"finish_reason":"content_filter"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
		`data: [DONE]`,
	}

	var param any
	var incompleteSeen bool
	var itemDoneSeen bool
	for _, line := range chunks {
		for _, chunk := range ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "gpt-5.6-luna", request, request, []byte(line), &param) {
			event, data := parseOpenAIResponsesSSEEvent(t, chunk)
			if event == "response.completed" {
				t.Fatalf("stream with finish_reason=content_filter was finalized as response.completed: %s", chunk)
			}
			if event == "response.output_item.done" {
				itemDoneSeen = true
				if got := data.Get("item.status").String(); got != "incomplete" {
					t.Fatalf("item.status = %q, want incomplete", got)
				}
			}
			if event == "response.incomplete" {
				incompleteSeen = true
				if got := data.Get("response.status").String(); got != "incomplete" {
					t.Fatalf("response.status = %q, want incomplete", got)
				}
				if got := data.Get("response.incomplete_details.reason").String(); got != "content_filter" {
					t.Fatalf("response.incomplete_details.reason = %q, want content_filter", got)
				}
			}
		}
	}
	if !itemDoneSeen {
		t.Fatal("expected response.output_item.done event for finish_reason=content_filter")
	}
	if !incompleteSeen {
		t.Fatal("expected response.incomplete event for finish_reason=content_filter")
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream_FinishReasonLength(t *testing.T) {
	raw := []byte(`{"id":"chatcmpl_len","object":"chat.completion","created":1773896263,"model":"gpt-5.6","choices":[{"index":0,"message":{"role":"assistant","content":"truncated text"},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
	out := ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(context.Background(), "gpt-5.6", nil, nil, raw, nil)
	data := gjson.ParseBytes(out)
	if got := data.Get("status").String(); got != "incomplete" {
		t.Fatalf("status = %q, want incomplete; out=%s", got, out)
	}
	if got := data.Get("incomplete_details.reason").String(); got != "max_output_tokens" {
		t.Fatalf("incomplete_details.reason = %q, want max_output_tokens; out=%s", got, out)
	}
	if got := data.Get("output.0.status").String(); got != "incomplete" {
		t.Fatalf("output.0.status = %q, want incomplete; out=%s", got, out)
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream_FinishReasonContentFilter(t *testing.T) {
	raw := []byte(`{"id":"chatcmpl_filter","object":"chat.completion","created":1773896263,"model":"gpt-5.6","choices":[{"index":0,"message":{"role":"assistant","content":"blocked text"},"finish_reason":"content_filter"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
	out := ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(context.Background(), "gpt-5.6", nil, nil, raw, nil)
	data := gjson.ParseBytes(out)
	if got := data.Get("status").String(); got != "incomplete" {
		t.Fatalf("status = %q, want incomplete; out=%s", got, out)
	}
	if got := data.Get("incomplete_details.reason").String(); got != "content_filter" {
		t.Fatalf("incomplete_details.reason = %q, want content_filter; out=%s", got, out)
	}
	if got := data.Get("output.0.status").String(); got != "incomplete" {
		t.Fatalf("output.0.status = %q, want incomplete; out=%s", got, out)
	}
}
