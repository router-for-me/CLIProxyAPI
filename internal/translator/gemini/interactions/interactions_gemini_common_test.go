package interactions

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
)

func TestConvertInteractionsRequestToGeminiStringInput(t *testing.T) {
	out, _ := ConvertInteractionsRequestToGemini("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","input":"hello"}`), false)
	if got := ojson.GetBytesNoCopy(out, "contents.0.role").String(); got != "user" {
		t.Fatalf("role = %q, want user", got)
	}
	if got := ojson.GetBytesNoCopy(out, "contents.0.parts.0.text").String(); got != "hello" {
		t.Fatalf("text = %q, want hello", got)
	}
}

func TestConvertInteractionsRequestToGeminiMergesMultipartUserInput(t *testing.T) {
	input := []byte(`{"model":"gemini-3-flash","input":[{"type":"user_input","content":[{"type":"text","text":"What is in this image?"},{"type":"image","mime_type":"image/png","data":"iVBORw0KGgo="}]}]}`)

	out, errConvert := ConvertInteractionsRequestToGemini("gemini-3-flash", input, false)
	if errConvert != nil {
		t.Fatalf("ConvertInteractionsRequestToGemini error: %v", errConvert)
	}
	contents := ojson.GetBytesNoCopy(out, "contents").Array()
	if len(contents) != 1 {
		t.Fatalf("contents length = %d, want 1. Output: %s", len(contents), string(out))
	}
	if role := contents[0].Get("role").String(); role != "user" {
		t.Fatalf("contents.0.role = %q, want user. Output: %s", role, string(out))
	}
	parts := contents[0].Get("parts").Array()
	if len(parts) != 2 {
		t.Fatalf("contents.0.parts length = %d, want 2. Output: %s", len(parts), string(out))
	}
	if text := parts[0].Get("text").String(); text != "What is in this image?" {
		t.Fatalf("contents.0.parts.0.text = %q, want question text. Output: %s", text, string(out))
	}
	if data := parts[1].Get("inlineData.data").String(); data != "iVBORw0KGgo=" {
		t.Fatalf("contents.0.parts.1.inlineData.data = %q, want image data. Output: %s", data, string(out))
	}
}

func TestConvertInteractionsRequestToGeminiSystemAndGenerationConfig(t *testing.T) {
	out, _ := ConvertInteractionsRequestToGemini("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","system_instruction":{"text":"be brief"},"generation_config":{"max_output_tokens":32,"top_p":0.8},"input":"hi"}`), false)
	if got := ojson.GetBytesNoCopy(out, "systemInstruction.parts.0.text").String(); got != "be brief" {
		t.Fatalf("systemInstruction = %q, want be brief", got)
	}
	if got := ojson.GetBytesNoCopy(out, "generationConfig.maxOutputTokens").Int(); got != 32 {
		t.Fatalf("maxOutputTokens = %d, want 32", got)
	}
	if got := ojson.GetBytesNoCopy(out, "generationConfig.topP").Float(); got != 0.8 {
		t.Fatalf("topP = %v, want 0.8", got)
	}
}

func TestConvertInteractionsRequestToGeminiStringSystemInstruction(t *testing.T) {
	out, _ := ConvertInteractionsRequestToGemini("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","system_instruction":"be brief","input":"hi"}`), false)
	if got := ojson.GetBytesNoCopy(out, "systemInstruction.parts.0.text").String(); got != "be brief" {
		t.Fatalf("systemInstruction.parts.0.text = %q, want be brief. Output: %s", got, string(out))
	}
}

func TestConvertGeminiRequestToInteractionsStringSystemInstruction(t *testing.T) {
	out, _ := ConvertGeminiRequestToInteractions("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","systemInstruction":{"parts":[{"text":"be brief"},{"text":"answer directly"}]},"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`), false)
	sys := ojson.GetBytesNoCopy(out, "system_instruction")
	if sys.Type != ojson.String {
		t.Fatalf("system_instruction type = %v, want string. Output: %s", sys.Type, string(out))
	}
	if got := sys.String(); got != "be brief\nanswer directly" {
		t.Fatalf("system_instruction = %q, want merged text. Output: %s", got, string(out))
	}
	if ojson.GetBytesNoCopy(out, "system_instruction.parts").Exists() {
		t.Fatalf("system_instruction.parts should not be forwarded. Output: %s", string(out))
	}
}

func TestConvertGeminiResponseToInteractionsNonStream(t *testing.T) {
	out := convertGeminiResponseToInteractionsNonStreamDirect("gemini-3.5-flash", nil, nil, []byte(`{"responseId":"resp_1","candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3}}`))
	if got := ojson.GetBytesNoCopy(out, "steps.0.type").String(); got != "model_output" {
		t.Fatalf("step type = %q, want model_output", got)
	}
	if got := ojson.GetBytesNoCopy(out, "steps.0.content.0.text").String(); got != "ok" {
		t.Fatalf("text = %q, want ok", got)
	}
	if got := ojson.GetBytesNoCopy(out, "usage.total_tokens").Int(); got != 3 {
		t.Fatalf("total tokens = %d, want 3", got)
	}
}

func TestConvertGeminiResponseToInteractionsNonStreamSnakeCaseUsage(t *testing.T) {
	out := convertGeminiResponseToInteractionsNonStreamDirect("gemini-3.5-flash", nil, nil, []byte(`{"responseId":"resp_snake","candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usage_metadata":{"prompt_token_count":11,"candidates_token_count":22,"total_token_count":33,"thoughts_token_count":44,"cached_content_token_count":55}}`))
	for _, test := range []struct {
		path string
		want int64
	}{
		{"usage.input_tokens", 11},
		{"usage.output_tokens", 22},
		{"usage.reasoning_tokens", 44},
		{"usage.total_tokens", 33},
		{"usage.cached_tokens", 55},
	} {
		if got := ojson.GetBytesNoCopy(out, test.path).Int(); got != test.want {
			t.Fatalf("%s = %d, want %d. Output: %s", test.path, got, test.want, string(out))
		}
	}
}

func TestConvertInteractionsResponseToGeminiStreamFunctionCall(t *testing.T) {
	var param any
	created := ConvertInteractionsResponseToGemini(context.Background(), "gemini-3.1-flash-lite", nil, nil, []byte(`data: {"interaction":{"id":"i1","model":"gemini-3.1-flash-lite"},"event_type":"interaction.created"}`), &param)
	if len(created) != 0 {
		t.Fatalf("created output count = %d, want 0", len(created))
	}
	start := ConvertInteractionsResponseToGemini(context.Background(), "gemini-3.1-flash-lite", nil, nil, []byte(`data: {"index":0,"step":{"type":"function_call","id":"call_1","signature":"sig_1","name":"get_weather","arguments":{}},"event_type":"step.start"}`), &param)
	if len(start) != 0 {
		t.Fatalf("start output count = %d, want 0", len(start))
	}
	delta := ConvertInteractionsResponseToGemini(context.Background(), "gemini-3.1-flash-lite", nil, nil, []byte(`data: {"index":0,"delta":{"type":"arguments_delta","arguments":"{\"location\":\"北京\"}"},"event_type":"step.delta"}`), &param)
	if len(delta) != 1 {
		t.Fatalf("delta output count = %d, want 1", len(delta))
	}
	if got := ojson.GetBytesNoCopy(delta[0], "candidates.0.content.parts.0.functionCall.name").String(); got != "get_weather" {
		t.Fatalf("functionCall.name = %q, want get_weather. Payload: %s", got, string(delta[0]))
	}
	if got := ojson.GetBytesNoCopy(delta[0], "candidates.0.content.parts.0.functionCall.args.location").String(); got != "北京" {
		t.Fatalf("functionCall.args.location = %q, want 北京. Payload: %s", got, string(delta[0]))
	}
	if got := ojson.GetBytesNoCopy(delta[0], "candidates.0.content.parts.0.functionCall.id").String(); got != "call_1" {
		t.Fatalf("functionCall.id = %q, want call_1. Payload: %s", got, string(delta[0]))
	}
	if got := ojson.GetBytesNoCopy(delta[0], "candidates.0.content.parts.0.thoughtSignature").String(); got != "sig_1" {
		t.Fatalf("thoughtSignature = %q, want sig_1. Payload: %s", got, string(delta[0]))
	}
	stopped := ConvertInteractionsResponseToGemini(context.Background(), "gemini-3.1-flash-lite", nil, nil, []byte(`data: {"event_type":"step.stop","index":0}`), &param)
	if len(stopped) != 0 {
		t.Fatalf("step.stop after arguments_delta emitted a duplicate chunk: %s", stopped)
	}
	completed := ConvertInteractionsResponseToGemini(context.Background(), "gemini-3.1-flash-lite", nil, nil, []byte(`data: {"interaction":{"id":"i1","status":"requires_action","usage":{"total_input_tokens":2,"total_output_tokens":3,"total_tokens":5,"total_thought_tokens":1,"total_cached_tokens":4},"service_tier":"standard","model":"gemini-3.1-flash-lite"},"event_type":"interaction.completed"}`), &param)
	if len(completed) != 1 {
		t.Fatalf("completed output count = %d, want 1", len(completed))
	}
	if got := ojson.GetBytesNoCopy(completed[0], "candidates.0.finishReason").String(); got != "STOP" {
		t.Fatalf("finishReason = %q, want STOP. Payload: %s", got, string(completed[0]))
	}
	if got := ojson.GetBytesNoCopy(completed[0], "usageMetadata.promptTokenCount").Int(); got != 2 {
		t.Fatalf("promptTokenCount = %d, want 2. Payload: %s", got, string(completed[0]))
	}
	if got := ojson.GetBytesNoCopy(completed[0], "usageMetadata.candidatesTokenCount").Int(); got != 3 {
		t.Fatalf("candidatesTokenCount = %d, want 3. Payload: %s", got, string(completed[0]))
	}
	if got := ojson.GetBytesNoCopy(completed[0], "usageMetadata.totalTokenCount").Int(); got != 5 {
		t.Fatalf("totalTokenCount = %d, want 5. Payload: %s", got, string(completed[0]))
	}
	if got := ojson.GetBytesNoCopy(completed[0], "usageMetadata.promptTokensDetails.0.tokenCount").Int(); got != 2 {
		t.Fatalf("promptTokensDetails.0.tokenCount = %d, want 2. Payload: %s", got, string(completed[0]))
	}
	done := ConvertInteractionsResponseToGemini(context.Background(), "gemini-3.1-flash-lite", nil, nil, []byte(`event: done
data: [DONE]`), &param)
	if len(done) != 0 {
		t.Fatalf("done output count = %d, want 0", len(done))
	}
}

func TestConvertInteractionsResponseToGeminiStreamFragmentedArguments(t *testing.T) {
	const startLookup = `{"event_type":"step.start","index":0,"step":{"type":"function_call","id":"call_1","name":"lookup","signature":"sig_1","arguments":{}}}`
	const stopLookup = `{"event_type":"step.stop","index":0}`
	type functionCall struct{ id, name, args, signature string }
	for _, tt := range []struct {
		name   string
		events []string
		want   []functionCall
	}{
		{
			name: "split JSON object",
			events: []string{
				startLookup,
				`{"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":"{\"q\":"}}`,
				`{"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":"\"example\"}"}}`,
				stopLookup,
			},
			want: []functionCall{{"call_1", "lookup", `{"q":"example"}`, "sig_1"}},
		},
		{
			name: "single delta emits the equivalent call",
			events: []string{
				startLookup,
				`{"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":"{\"q\":\"example\"}"}}`,
				stopLookup,
			},
			want: []functionCall{{"call_1", "lookup", `{"q":"example"}`, "sig_1"}},
		},
		{
			name: "interleaved calls preserve whitespace inside strings",
			events: []string{
				startLookup,
				`{"event_type":"step.start","index":1,"step":{"type":"function_call","call_id":"call_2","name":"search","signature":"sig_2","arguments":{}}}`,
				`{"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":"{\"q\":\"New "}}`,
				`{"event_type":"step.delta","index":1,"delta":{"type":"arguments_delta","arguments":"{\"q\":\"exa"}}`,
				`{"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":" "}}`,
				`{"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":" York\"}"}}`,
				`{"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":" "}}`,
				stopLookup,
				`{"event_type":"step.delta","index":1,"delta":{"type":"arguments_delta","arguments":"mple\"}"}}`,
				`{"event_type":"step.stop","index":1}`,
			},
			want: []functionCall{{"call_1", "lookup", `{"q":"New   York"}`, "sig_1"}, {"call_2", "search", `{"q":"example"}`, "sig_2"}},
		},
		{
			name: "complete arguments before late function name",
			events: []string{
				`{"event_type":"step.start","index":0,"step":{"type":"function_call","id":"call_1","signature":"sig_1","arguments":{}}}`,
				`{"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":"{\"q\":\"example\"}"}}`,
				`{"event_type":"step.start","index":0,"step":{"type":"function_call","call_id":"call_1","name":"lookup","arguments":{}}}`,
				stopLookup,
			},
			want: []functionCall{{"call_1", "lookup", `{"q":"example"}`, "sig_1"}},
		},
		{
			name: "nested objects and escaped quotes across fragments",
			events: []string{
				startLookup,
				`{"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":"{\"items\":[{\"n\":1}"}}`,
				`{"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":",{\"n\":2}"}}`,
				`{"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":"],\"q\":\"left\\"}}`,
				`{"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":"\"}right\"}"}}`,
				stopLookup,
			},
			want: []functionCall{{"call_1", "lookup", `{"items":[{"n":1},{"n":2}],"q":"left\"}right"}`, "sig_1"}},
		},
		{
			name: "truncated arguments do not become an empty call",
			events: []string{
				startLookup,
				`{"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":"{\"q\":"}}`,
				stopLookup,
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var param any
			var calls []functionCall
			for _, event := range tt.events {
				for _, chunk := range ConvertInteractionsResponseToGemini(context.Background(), "gemini-test", nil, nil, []byte("data: "+event), &param) {
					for _, part := range ojson.GetBytesNoCopy(chunk, "candidates.0.content.parts").Array() {
						if call := part.Get("functionCall"); call.Exists() {
							calls = append(calls, functionCall{call.Get("id").String(), call.Get("name").String(), call.Get("args").Raw, part.Get("thoughtSignature").String()})
						}
					}
				}
			}
			if len(calls) != len(tt.want) {
				t.Fatalf("got %d function calls (%+v), want %d (%+v)", len(calls), calls, len(tt.want), tt.want)
			}
			for i, want := range tt.want {
				if calls[i] != want {
					t.Errorf("function call %d = %+v, want %+v", i, calls[i], want)
				}
			}
		})
	}
}

func TestConvertInteractionsResponseToGeminiStreamArgumentBufferLimit(t *testing.T) {
	var param any
	send := func(payload []byte) [][]byte {
		return ConvertInteractionsResponseToGemini(context.Background(), "gemini-test", nil, nil, payload, &param)
	}
	send([]byte(`{"event_type":"step.start","index":0,"step":{"type":"function_call","id":"call_1","name":"lookup","arguments":{}}}`))
	send([]byte(`{"event_type":"step.start","index":1,"step":{"type":"function_call","id":"call_2","name":"lookup","arguments":{}}}`))
	send([]byte(`{"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":"{\"q\":\""}}`))
	send([]byte(`{"event_type":"step.delta","index":1,"delta":{"type":"arguments_delta","arguments":"{\"q\":\""}}`))
	padding := strings.Repeat("x", 1<<20)
	delta0 := []byte(`{"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":"` + padding + `"}}`)
	delta1 := []byte(`{"event_type":"step.delta","index":1,"delta":{"type":"arguments_delta","arguments":"` + padding + `"}}`)
	for range 30 {
		if chunks := send(delta0); len(chunks) != 0 {
			t.Fatal("incomplete call emitted before step 0 closed")
		}
	}
	for range 25 {
		if chunks := send(delta1); len(chunks) != 0 {
			t.Fatal("incomplete call emitted before step 1 closed")
		}
	}
	if chunks := send([]byte(`{"event_type":"step.delta","index":1,"delta":{"type":"arguments_delta","arguments":"\"}"}}`)); len(chunks) != 0 {
		t.Fatal("call that exceeded the shared argument budget was emitted")
	}
	if chunks := send([]byte(`{"event_type":"step.delta","index":1,"delta":{"type":"arguments_delta","arguments":"{}"}}`)); len(chunks) != 0 {
		t.Fatal("later arguments restarted a rejected call")
	}
	send(delta0) // This fragment only fits if step 1's buffer was released.
	if chunks := send([]byte(`{"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":"\"}"}}`)); len(chunks) != 1 {
		t.Fatalf("call within budget emitted %d chunks, want 1", len(chunks))
	}
}

func TestConvertInteractionsResponseToGeminiStreamCompleteFunctionCall(t *testing.T) {
	for _, tt := range []struct {
		name string
		args string
	}{
		{name: "with arguments", args: `{"q":"example"}`},
		{name: "empty arguments", args: `{}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var param any
			send := func(payload string) [][]byte {
				return ConvertInteractionsResponseToGemini(context.Background(), "gemini-test", nil, nil, []byte("data: "+payload), &param)
			}
			if started := send(`{"event_type":"step.start","index":0,"step":{"type":"function_call","id":"call_1","name":"lookup","arguments":` + tt.args + `}}`); len(started) != 0 {
				t.Fatalf("step.start output = %s, want no chunks before the step closes", started)
			}
			const stop = `{"event_type":"step.stop","index":0}`
			stopped := send(stop)
			if len(stopped) != 1 {
				t.Fatalf("step.stop output count = %d, want one functionCall chunk", len(stopped))
			}
			parts := ojson.GetBytesNoCopy(stopped[0], "candidates.0.content.parts").Array()
			if len(parts) != 1 {
				t.Fatalf("step.stop parts = %d, want 1: %s", len(parts), stopped[0])
			}
			call := parts[0].Get("functionCall")
			if id, name, args := call.Get("id").String(), call.Get("name").String(), call.Get("args").Raw; id != "call_1" || name != "lookup" || args != tt.args {
				t.Fatalf("functionCall = %s, want id call_1, name lookup, and args %s", call.Raw, tt.args)
			}
			if duplicate := send(stop); len(duplicate) != 0 {
				t.Fatalf("repeated step.stop emitted a duplicate chunk: %s", duplicate)
			}
			completed := send(`{"event_type":"interaction.completed","interaction":{"status":"requires_action"}}`)
			if len(completed) != 1 {
				t.Fatalf("interaction.completed output count = %d, want one STOP chunk", len(completed))
			}
			candidate := ojson.GetBytesNoCopy(completed[0], "candidates.0")
			if got := candidate.Get("finishReason").String(); got != "STOP" {
				t.Fatalf("finishReason = %q, want STOP", got)
			}
			for _, part := range candidate.Get("content.parts").Array() {
				if part.Get("functionCall").Exists() {
					t.Fatalf("interaction.completed emitted a duplicate functionCall: %s", completed[0])
				}
			}
		})
	}
}

func TestConvertInteractionsResponseToGeminiStreamFinishMetadataUsage(t *testing.T) {
	var param any
	out := ConvertInteractionsResponseToGemini(context.Background(), "gemini-test", nil, nil, []byte(`data: {"event_type":"finish","metadata":{"total_usage":{"total_input_tokens":2,"total_output_tokens":6,"total_thought_tokens":3,"total_cached_tokens":1,"total_tokens":11}}}`), &param)
	if len(out) != 1 {
		t.Fatalf("output count = %d, want 1", len(out))
	}
	if got := ojson.GetBytesNoCopy(out[0], "candidates.0.finishReason").String(); got != "STOP" {
		t.Fatalf("finishReason = %q, want STOP. Payload: %s", got, string(out[0]))
	}
	if got := ojson.GetBytesNoCopy(out[0], "usageMetadata.promptTokenCount").Int(); got != 2 {
		t.Fatalf("promptTokenCount = %d, want 2. Payload: %s", got, string(out[0]))
	}
	if got := ojson.GetBytesNoCopy(out[0], "usageMetadata.candidatesTokenCount").Int(); got != 6 {
		t.Fatalf("candidatesTokenCount = %d, want 6. Payload: %s", got, string(out[0]))
	}
	if got := ojson.GetBytesNoCopy(out[0], "usageMetadata.thoughtsTokenCount").Int(); got != 3 {
		t.Fatalf("thoughtsTokenCount = %d, want 3. Payload: %s", got, string(out[0]))
	}
	if got := ojson.GetBytesNoCopy(out[0], "usageMetadata.cachedContentTokenCount").Int(); got != 1 {
		t.Fatalf("cachedContentTokenCount = %d, want 1. Payload: %s", got, string(out[0]))
	}
	if got := ojson.GetBytesNoCopy(out[0], "usageMetadata.totalTokenCount").Int(); got != 11 {
		t.Fatalf("totalTokenCount = %d, want 11. Payload: %s", got, string(out[0]))
	}
}

func TestConvertInteractionsResponseToGeminiNonStreamFunctionCall(t *testing.T) {
	raw := []byte(`{"id":"i1","model":"gemini-3.1-flash-lite","steps":[{"type":"function_call","call_id":"call_1","signature":"sig_1","name":"get_weather","arguments":{"location":"北京"}}],"usage":{"total_input_tokens":2,"total_output_tokens":3,"total_tokens":5}}`)
	out := ConvertInteractionsResponseToGeminiNonStream(context.Background(), "gemini-3.1-flash-lite", nil, nil, raw, nil)
	if got := ojson.GetBytesNoCopy(out, "candidates.0.content.parts.0.functionCall.name").String(); got != "get_weather" {
		t.Fatalf("functionCall.name = %q, want get_weather. Payload: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "candidates.0.content.parts.0.functionCall.args.location").String(); got != "北京" {
		t.Fatalf("functionCall.args.location = %q, want 北京. Payload: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "candidates.0.content.parts.0.thoughtSignature").String(); got != "sig_1" {
		t.Fatalf("thoughtSignature = %q, want sig_1. Payload: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "usageMetadata.totalTokenCount").Int(); got != 5 {
		t.Fatalf("totalTokenCount = %d, want 5. Payload: %s", got, string(out))
	}
}

func TestConvertInteractionsRequestToGeminiTurnInput(t *testing.T) {
	out, _ := ConvertInteractionsRequestToGemini("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","input":{"role":"user","steps":[{"type":"user_input","content":[{"text":"hi"}]}]}}`), false)
	if got := ojson.GetBytesNoCopy(out, "contents.0.parts.0.text").String(); got != "hi" {
		t.Fatalf("text = %q, want hi", got)
	}
}

func TestConvertInteractionsRequestToGeminiTurnArrayInput(t *testing.T) {
	out, _ := ConvertInteractionsRequestToGemini("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","input":[{"role":"user","steps":[{"type":"user_input","content":[{"text":"hi"}]}]},{"role":"assistant","steps":[{"type":"model_output","content":[{"text":"ok"}]}]}]}`), false)
	if got := ojson.GetBytesNoCopy(out, "contents.0.role").String(); got != "user" {
		t.Fatalf("contents.0.role = %q, want user. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "contents.0.parts.0.text").String(); got != "hi" {
		t.Fatalf("contents.0.parts.0.text = %q, want hi. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "contents.1.role").String(); got != "model" {
		t.Fatalf("contents.1.role = %q, want model. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "contents.1.parts.0.text").String(); got != "ok" {
		t.Fatalf("contents.1.parts.0.text = %q, want ok. Output: %s", got, string(out))
	}
}

func TestConvertInteractionsRequestToGeminiPreservesExpressibleTopLevelFields(t *testing.T) {
	out, _ := ConvertInteractionsRequestToGemini("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","tool_choice":{"type":"function","function":{"name":"lookup"}},"response_modalities":["text","image"],"service_tier":"priority","input":"hi"}`), false)
	if got := ojson.GetBytesNoCopy(out, "toolConfig.functionCallingConfig.mode").String(); got != "ANY" {
		t.Fatalf("toolConfig.functionCallingConfig.mode = %q, want ANY. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "toolConfig.functionCallingConfig.allowedFunctionNames.0").String(); got != "lookup" {
		t.Fatalf("allowedFunctionNames.0 = %q, want lookup. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "generationConfig.responseModalities.0").String(); got != "TEXT" {
		t.Fatalf("responseModalities.0 = %q, want TEXT. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "generationConfig.responseModalities.1").String(); got != "IMAGE" {
		t.Fatalf("responseModalities.1 = %q, want IMAGE. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "service_tier").String(); got != "priority" {
		t.Fatalf("service_tier = %q, want priority. Output: %s", got, string(out))
	}
}

func TestConvertInteractionsRequestToGeminiContentInput(t *testing.T) {
	out, _ := ConvertInteractionsRequestToGemini("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","input":{"role":"user","parts":[{"text":"hi"}]}}`), false)
	if got := ojson.GetBytesNoCopy(out, "contents.0.role").String(); got != "user" {
		t.Fatalf("contents.0.role = %q, want user", got)
	}
	if got := ojson.GetBytesNoCopy(out, "contents.0.parts.0.text").String(); got != "hi" {
		t.Fatalf("contents.0.parts.0.text = %q, want hi", got)
	}
}

func TestConvertInteractionsRequestToGeminiContentArrayInput(t *testing.T) {
	out, _ := ConvertInteractionsRequestToGemini("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","input":[{"role":"user","parts":[{"text":"hi"}]},{"role":"assistant","parts":[{"text":"ok"}]}]}`), false)
	if got := ojson.GetBytesNoCopy(out, "contents.0.role").String(); got != "user" {
		t.Fatalf("contents.0.role = %q, want user", got)
	}
	if got := ojson.GetBytesNoCopy(out, "contents.0.parts.0.text").String(); got != "hi" {
		t.Fatalf("contents.0.parts.0.text = %q, want hi", got)
	}
	if got := ojson.GetBytesNoCopy(out, "contents.1.role").String(); got != "model" {
		t.Fatalf("contents.1.role = %q, want model", got)
	}
	if got := ojson.GetBytesNoCopy(out, "contents.1.parts.0.text").String(); got != "ok" {
		t.Fatalf("contents.1.parts.0.text = %q, want ok", got)
	}
}

func TestConvertGeminiResponseToInteractionsNonStreamFunctionCall(t *testing.T) {
	out := convertGeminiResponseToInteractionsNonStreamDirect("gemini-3.5-flash", nil, nil, []byte(`{"responseId":"resp_1","candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{"q":"x"}}}]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3,"cachedContentTokenCount":4}}`))
	if got := ojson.GetBytesNoCopy(out, "steps.0.type").String(); got != "function_call" {
		t.Fatalf("step type = %q, want function_call", got)
	}
	if got := ojson.GetBytesNoCopy(out, "steps.0.name").String(); got != "lookup" {
		t.Fatalf("name = %q, want lookup", got)
	}
	if got := ojson.GetBytesNoCopy(out, "usage.cached_tokens").Int(); got != 4 {
		t.Fatalf("cached tokens = %d, want 4", got)
	}
}

func TestConvertGeminiResponseToInteractionsNonStreamFunctionCallPreservesCallID(t *testing.T) {
	out := convertGeminiResponseToInteractionsNonStreamDirect("gemini-3.5-flash", nil, nil, []byte(`{"responseId":"resp_1","candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"lookup","call_id":"call_response_1","args":{"q":"x"}}}]}}]}`))
	if got := ojson.GetBytesNoCopy(out, "steps.0.call_id").String(); got != "call_response_1" {
		t.Fatalf("steps.0.call_id = %q, want call_response_1", got)
	}
}

func TestConvertGeminiResponseToInteractionsStreamFunctionCallCallID(t *testing.T) {
	var param any
	out := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.5-flash", nil, nil, []byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"lookup","call_id":"call_stream_1","args":{"q":"x"}}}]}}]}`), &param)
	payload := findStepDeltaPayload(out)
	if len(payload) == 0 {
		t.Fatalf("step.delta payload not found")
	}
	startPayload := findEventPayload(out, "step.start")
	if got := ojson.GetBytesNoCopy(startPayload, "step.id").String(); got != "call_stream_1" {
		t.Fatalf("step.id = %q, want call_stream_1", got)
	}
	if got := ojson.GetBytesNoCopy(payload, "delta.arguments").String(); got != `{"q":"x"}` {
		t.Fatalf("delta.arguments = %q, want JSON string", got)
	}
}

func TestConvertGeminiResponseToInteractionsStreamFunctionCallThoughtSignature(t *testing.T) {
	var param any
	thoughtOut := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.5-flash", nil, nil, []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"thinking","thought":true}]}}]}`), &param)
	textOut := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.5-flash", nil, nil, []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"I will call the tool."}]}}]}`), &param)
	callOut := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.5-flash", nil, nil, []byte(`{"candidates":[{"content":{"role":"model","parts":[{"thoughtSignature":"sig-call","functionCall":{"name":"lookup","id":"call_1","args":{"q":"x"}}}]}}]}`), &param)

	out := append(append(thoughtOut, textOut...), callOut...)
	signaturePayload := findStepDeltaPayloadByType(out, "thought_signature")
	if len(signaturePayload) == 0 {
		t.Fatalf("thought_signature step.delta payload not found. Events: %s", eventTypes(out))
	}
	if got := ojson.GetBytesNoCopy(signaturePayload, "delta.signature").String(); got != "sig-call" {
		t.Fatalf("delta.signature = %q, want sig-call. Payload: %s", got, string(signaturePayload))
	}
	if got := ojson.GetBytesNoCopy(signaturePayload, "index").Int(); got != 2 {
		t.Fatalf("signature index = %d, want 2. Events: %s", got, eventTypes(out))
	}
	functionStartPayload := findNthEventPayload(out, "step.start", 3)
	if got := ojson.GetBytesNoCopy(functionStartPayload, "step.type").String(); got != "function_call" {
		t.Fatalf("fourth step type = %q, want function_call. Events: %s", got, eventTypes(out))
	}
	if got := ojson.GetBytesNoCopy(functionStartPayload, "step.id").String(); got != "call_1" {
		t.Fatalf("function call id = %q, want call_1. Payload: %s", got, string(functionStartPayload))
	}
	argumentsPayload := findStepDeltaPayloadByType(out, "arguments_delta")
	if got := ojson.GetBytesNoCopy(argumentsPayload, "delta.arguments").String(); got != `{"q":"x"}` {
		t.Fatalf("delta.arguments = %q, want JSON string. Payload: %s", got, string(argumentsPayload))
	}
}

func TestConvertGeminiResponseToInteractionsStreamStepLifecycle(t *testing.T) {
	var param any
	thoughtOut := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.5-flash", nil, nil, []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"thinking","thought":true}]}}]}`), &param)
	textOut := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.5-flash", nil, nil, []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"answer"}]}}]}`), &param)
	callOut := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.5-flash", nil, nil, []byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"lookup","id":"call_1","args":{"q":"x"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":4,"totalTokenCount":7,"thoughtsTokenCount":2}}`), &param)

	out := append(append(thoughtOut, textOut...), callOut...)
	if got := eventTypes(out); !bytes.Equal(got, []byte("interaction.created,interaction.status_update,step.start,step.delta,step.stop,step.start,step.delta,step.stop,step.start,step.delta,step.stop,interaction.completed,done")) {
		t.Fatalf("event sequence = %s", got)
	}
	if got := ojson.GetBytesNoCopy(findNthEventPayload(out, "step.start", 0), "step.type").String(); got != "thought" {
		t.Fatalf("first step type = %q, want thought", got)
	}
	if got := ojson.GetBytesNoCopy(findNthEventPayload(out, "step.start", 1), "step.type").String(); got != "model_output" {
		t.Fatalf("second step type = %q, want model_output", got)
	}
	if got := ojson.GetBytesNoCopy(findNthEventPayload(out, "step.start", 2), "step.type").String(); got != "function_call" {
		t.Fatalf("third step type = %q, want function_call", got)
	}
	if got := ojson.GetBytesNoCopy(findNthEventPayload(out, "step.delta", 0), "delta.type").String(); got != "thought_summary" {
		t.Fatalf("thought delta type = %q, want thought_summary", got)
	}
	if got := ojson.GetBytesNoCopy(findNthEventPayload(out, "step.delta", 2), "delta.type").String(); got != "arguments_delta" {
		t.Fatalf("function delta type = %q, want arguments_delta", got)
	}
	completed := findCompletedPayload(out)
	if got := ojson.GetBytesNoCopy(completed, "interaction.usage.total_input_tokens").Int(); got != 3 {
		t.Fatalf("total_input_tokens = %d, want 3. Payload: %s", got, string(completed))
	}
	if got := ojson.GetBytesNoCopy(completed, "interaction.usage.total_output_tokens").Int(); got != 4 {
		t.Fatalf("total_output_tokens = %d, want 4. Payload: %s", got, string(completed))
	}
	if got := ojson.GetBytesNoCopy(completed, "interaction.usage.total_thought_tokens").Int(); got != 2 {
		t.Fatalf("total_thought_tokens = %d, want 2. Payload: %s", got, string(completed))
	}
}

func TestConvertGeminiResponseToInteractionsStreamSnakeCaseUsage(t *testing.T) {
	var param any
	out := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.5-flash", nil, nil, []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usage_metadata":{"prompt_token_count":11,"candidates_token_count":22,"total_token_count":33,"thoughts_token_count":44,"cached_content_token_count":55}}`), &param)
	if got := countEventType(out, "interaction.completed"); got != 1 {
		t.Fatalf("interaction.completed count = %d, want 1. Events: %s", got, eventTypes(out))
	}
	completed := findCompletedPayload(out)
	for _, test := range []struct {
		path string
		want int64
	}{
		{"interaction.usage.total_input_tokens", 11},
		{"interaction.usage.total_output_tokens", 22},
		{"interaction.usage.total_thought_tokens", 44},
		{"interaction.usage.total_tokens", 33},
		{"interaction.usage.total_cached_tokens", 55},
	} {
		if got := ojson.GetBytesNoCopy(completed, test.path).Int(); got != test.want {
			t.Fatalf("%s = %d, want %d. Payload: %s", test.path, got, test.want, string(completed))
		}
	}
}

func TestConvertGeminiResponseToInteractionsStreamEmitsDoneAfterUsage(t *testing.T) {
	var param any
	out := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.5-flash", nil, nil, []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}`), &param)
	if got := countEventType(out, "interaction.completed"); got != 1 {
		t.Fatalf("interaction.completed count = %d, want 1. Events: %s", got, eventTypes(out))
	}
	if got := countEventType(out, "done"); got != 1 {
		t.Fatalf("done count = %d, want 1. Events: %s", got, eventTypes(out))
	}
	completed := findCompletedPayload(out)
	if got := ojson.GetBytesNoCopy(completed, "interaction.usage.total_tokens").Int(); got != 5 {
		t.Fatalf("completed total_tokens = %d, want 5. Payload: %s", got, string(completed))
	}

	doneOut := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.5-flash", nil, nil, []byte(`[DONE]`), &param)
	if len(doneOut) != 0 {
		t.Fatalf("literal [DONE] after completion emitted %q, want no events", doneOut)
	}
}

func TestConvertGeminiResponseToInteractionsStreamSplitFinishAndFilteredUsage(t *testing.T) {
	var param any
	finishOut := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.5-flash", nil, nil, []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`), &param)
	if got := countEventType(finishOut, "interaction.completed"); got != 0 {
		t.Fatalf("finish interaction.completed count = %d, want 0", got)
	}
	usageOut := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.5-flash", nil, nil, []byte(`{"candidates":[],"cpaUsageMetadata":{"promptTokenCount":11,"candidatesTokenCount":22,"totalTokenCount":33}}`), &param)
	if got := countEventType(usageOut, "interaction.completed"); got != 1 {
		t.Fatalf("usage interaction.completed count = %d, want 1. Events: %s", got, eventTypes(usageOut))
	}
	if got := countEventType(usageOut, "done"); got != 1 {
		t.Fatalf("usage done count = %d, want 1. Events: %s", got, eventTypes(usageOut))
	}
	completed := findCompletedPayload(usageOut)
	if got := ojson.GetBytesNoCopy(completed, "interaction.usage.total_tokens").Int(); got != 33 {
		t.Fatalf("completed total_tokens = %d, want 33. Payload: %s", got, string(completed))
	}
}

func TestConvertGeminiResponseToInteractionsStreamWaitsForPostFinishUsage(t *testing.T) {
	var param any
	contentOut := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.5-flash", nil, nil, []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"partial"}]}}],"usageMetadata":{"promptTokenCount":1,"totalTokenCount":1}}`), &param)
	finishOut := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.5-flash", nil, nil, []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"STOP"}]}`), &param)
	for _, out := range [][][]byte{contentOut, finishOut} {
		joined := bytes.Join(out, nil)
		if bytes.Contains(joined, []byte("interaction.completed")) || bytes.Contains(joined, []byte("[DONE]")) {
			t.Fatalf("stream completed before the post-finish usage frame: %q", joined)
		}
	}
	usageOut := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.5-flash", nil, nil, []byte(`{"candidates":[],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":22,"totalTokenCount":33}}`), &param)
	if got := countEventType(usageOut, "interaction.completed"); got != 1 {
		t.Fatalf("usage interaction.completed count = %d, want 1. Events: %s", got, eventTypes(usageOut))
	}
	if got := countEventType(usageOut, "done"); got != 1 {
		t.Fatalf("usage done count = %d, want 1. Events: %s", got, eventTypes(usageOut))
	}
	completed := findCompletedPayload(usageOut)
	if got := ojson.GetBytesNoCopy(completed, "interaction.usage.total_tokens").Int(); got != 33 {
		t.Fatalf("completed total_tokens = %d, want 33 (final usage, not the intermediate frame). Payload: %s", got, string(completed))
	}
}

func TestConvertGeminiResponseToInteractionsStreamIgnoresEmptyFinishReason(t *testing.T) {
	for _, finishReason := range []string{"", " "} {
		var param any
		out := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.5-flash", nil, nil, []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"`+finishReason+`"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}`), &param)
		if got := countEventType(out, "interaction.completed"); got != 0 {
			t.Fatalf("finishReason %q: interaction.completed count = %d, want 0", finishReason, got)
		}
		if got := countEventType(out, "done"); got != 0 {
			t.Fatalf("finishReason %q: done count = %d, want 0", finishReason, got)
		}
	}
}

func TestConvertGeminiResponseToInteractionsStreamEmitsTerminalOnce(t *testing.T) {
	var param any
	finishOut := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.5-flash", nil, nil, []byte(`{"candidates":[{"finishReason":"STOP"}]}`), &param)
	usageOut := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.5-flash", nil, nil, []byte(`{"candidates":[],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3}}`), &param)
	doneOut := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.5-flash", nil, nil, []byte(`[DONE]`), &param)

	if got := countEventType(finishOut, "step.stop"); got != 0 {
		t.Fatalf("finish step.stop count = %d, want 0", got)
	}
	if got := countEventType(finishOut, "interaction.completed"); got != 0 {
		t.Fatalf("finish interaction.completed count = %d, want 0", got)
	}
	if got := countEventType(usageOut, "step.stop"); got != 0 {
		t.Fatalf("usage step.stop count = %d, want 0", got)
	}
	if got := countEventType(usageOut, "interaction.completed"); got != 1 {
		t.Fatalf("usage interaction.completed count = %d, want 1", got)
	}
	if got := countEventType(usageOut, "done"); got != 1 {
		t.Fatalf("usage done count = %d, want 1", got)
	}
	if got := countEventType(doneOut, "interaction.completed"); got != 0 {
		t.Fatalf("done interaction.completed count = %d, want 0", got)
	}
	if got := countEventType(doneOut, "done"); got != 0 {
		t.Fatalf("done event count = %d, want 0 after usage already terminated the stream", got)
	}
	if len(doneOut) != 0 {
		t.Fatalf("literal [DONE] after completion emitted %q, want no events", doneOut)
	}
	payload := findCompletedPayload(usageOut)
	if got := ojson.GetBytesNoCopy(payload, "interaction.usage.total_tokens").Int(); got != 3 {
		t.Fatalf("completed total_tokens = %d, want 3. Payload: %s", got, string(payload))
	}
}

func TestConvertGeminiResponseToInteractionsStreamDoesNotCompleteOnNonTerminalUsage(t *testing.T) {
	var param any
	thoughtOut := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.5-flash-low", nil, nil, []byte(`{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"thinking"}]}}],"usageMetadata":{"promptTokenCount":124,"totalTokenCount":124}}`), &param)
	if got := countEventType(thoughtOut, "interaction.completed"); got != 0 {
		t.Fatalf("thought interaction.completed count = %d, want 0. Events: %s", got, eventTypes(thoughtOut))
	}
	if got := countEventType(thoughtOut, "step.stop"); got != 0 {
		t.Fatalf("thought step.stop count = %d, want 0. Events: %s", got, eventTypes(thoughtOut))
	}

	if got := countEventType(thoughtOut, "done"); got != 0 {
		t.Fatalf("thought done count = %d, want 0. Events: %s", got, eventTypes(thoughtOut))
	}

	textOut := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.5-flash-low", nil, nil, []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"好的，我将为您调用天气查询工具。"}]}}],"usageMetadata":{"promptTokenCount":124,"candidatesTokenCount":17,"totalTokenCount":452,"thoughtsTokenCount":311}}`), &param)
	callOut := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.5-flash-low", nil, nil, []byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"get_weather","args":{"location":"北京"},"id":"nriii75p"}}]}}],"usageMetadata":{"promptTokenCount":124,"candidatesTokenCount":33,"totalTokenCount":468,"thoughtsTokenCount":311}}`), &param)
	finishOut := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.5-flash-low", nil, nil, []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":124,"candidatesTokenCount":33,"totalTokenCount":468,"thoughtsTokenCount":311}}`), &param)

	out := append(append(append(thoughtOut, textOut...), callOut...), finishOut...)
	if got := countEventType(out, "interaction.completed"); got != 1 {
		t.Fatalf("interaction.completed count = %d, want 1. Events: %s", got, eventTypes(out))
	}
	if got := countEventType(out, "done"); got != 1 {
		t.Fatalf("done count = %d, want 1. Events: %s", got, eventTypes(out))
	}
	if got := eventTypes(out); !bytes.Equal(got, []byte("interaction.created,interaction.status_update,step.start,step.delta,step.stop,step.start,step.delta,step.stop,step.start,step.delta,step.stop,interaction.completed,done")) {
		t.Fatalf("event sequence = %s", got)
	}
	payload := findCompletedPayload(out)
	if got := ojson.GetBytesNoCopy(payload, "interaction.usage.total_tokens").Int(); got != 468 {
		t.Fatalf("completed total_tokens = %d, want 468. Payload: %s", got, string(payload))
	}
}

func TestConvertGeminiResponseToInteractionsStreamIgnoresTrafficOnlyUsageMetadata(t *testing.T) {
	var param any
	out := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.5-flash", nil, nil, []byte(`{"candidates":[{"content":{"role":"model","parts":[]}}],"usageMetadata":{"trafficType":"PROVISIONED_THROUGHPUT"}}`), &param)
	if got := countEventType(out, "interaction.completed"); got != 0 {
		t.Fatalf("interaction.completed count = %d, want 0. Events: %q", got, out)
	}
	if got := countEventType(out, "done"); got != 0 {
		t.Fatalf("done count = %d, want 0. Events: %q", got, out)
	}
}

func TestConvertGeminiResponseToInteractionsStreamCompletesOnDoneWithoutUsage(t *testing.T) {
	var param any
	finishOut := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.5-flash", nil, nil, []byte(`{"candidates":[{"finishReason":"STOP"}]}`), &param)
	doneOut := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.5-flash", nil, nil, []byte(`[DONE]`), &param)

	if got := countEventType(finishOut, "interaction.completed"); got != 0 {
		t.Fatalf("finish interaction.completed count = %d, want 0", got)
	}
	if got := countEventType(doneOut, "interaction.completed"); got != 1 {
		t.Fatalf("done interaction.completed count = %d, want 1", got)
	}
	if got := countEventType(doneOut, "done"); got != 1 {
		t.Fatalf("done event count = %d, want 1", got)
	}
}

func TestConvertInteractionsRequestToGeminiImageContent(t *testing.T) {
	out, _ := ConvertInteractionsRequestToGemini("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","input":[{"type":"user_input","content":[{"type":"image","mime_type":"image/png","data":"aGVsbG8="}]}]}`), false)
	if got := ojson.GetBytesNoCopy(out, "contents.0.parts.0.inlineData.mimeType").String(); got != "image/png" {
		t.Fatalf("mimeType = %q, want image/png", got)
	}
	if got := ojson.GetBytesNoCopy(out, "contents.0.parts.0.inlineData.data").String(); got != "aGVsbG8=" {
		t.Fatalf("data = %q, want aGVsbG8=", got)
	}
}

func TestConvertInteractionsRequestToGeminiModelOutputTypedContent(t *testing.T) {
	out, _ := ConvertInteractionsRequestToGemini("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","input":[{"type":"model_output","content":[{"type":"image","mime_type":"image/png","data":"aGVsbG8="},{"type":"document","mime_type":"application/pdf","file_uri":"gs://bucket/doc.pdf"}]}]}`), false)
	if got := ojson.GetBytesNoCopy(out, "contents.0.role").String(); got != "model" {
		t.Fatalf("contents.0.role = %q, want model. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "contents.0.parts.0.inlineData.mimeType").String(); got != "image/png" {
		t.Fatalf("image mimeType = %q, want image/png. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "contents.0.parts.0.inlineData.data").String(); got != "aGVsbG8=" {
		t.Fatalf("image data = %q, want aGVsbG8=. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "contents.0.parts.1.fileData.mimeType").String(); got != "application/pdf" {
		t.Fatalf("document mimeType = %q, want application/pdf. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "contents.0.parts.1.fileData.fileUri").String(); got != "gs://bucket/doc.pdf" {
		t.Fatalf("document fileUri = %q, want gs://bucket/doc.pdf. Output: %s", got, string(out))
	}
}

func TestConvertInteractionsRequestToGeminiThoughtTypedContent(t *testing.T) {
	out, _ := ConvertInteractionsRequestToGemini("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","input":[{"type":"thought","content":[{"type":"text","text":"thinking"},{"type":"audio","mime_type":"audio/wav","data":"UklGRg=="}]}]}`), false)
	if got := ojson.GetBytesNoCopy(out, "contents.0.parts.0.text").String(); got != "thinking" {
		t.Fatalf("thought text = %q, want thinking. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "contents.0.parts.0.thought").Bool(); !got {
		t.Fatalf("thought flag = false, want true. Output: %s", string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "contents.0.parts.1.inlineData.mimeType").String(); got != "audio/wav" {
		t.Fatalf("audio mimeType = %q, want audio/wav. Output: %s", got, string(out))
	}
}

func TestConvertGeminiResponseToInteractionsNonStreamImage(t *testing.T) {
	out := convertGeminiResponseToInteractionsNonStreamDirect("gemini-3.5-flash", nil, nil, []byte(`{"responseId":"resp_1","candidates":[{"content":{"role":"model","parts":[{"inlineData":{"mimeType":"image/png","data":"aGVsbG8="}}]}}]}`))
	if got := ojson.GetBytesNoCopy(out, "steps.0.content.0.type").String(); got != "image" {
		t.Fatalf("content type = %q, want image", got)
	}
}

func TestConvertInteractionsRequestToGeminiGenerationConfigAllFields(t *testing.T) {
	out, _ := ConvertInteractionsRequestToGemini("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","generation_config":{"max_output_tokens":32,"response_schema":{"type":"object"},"seed":42,"thinking_config":{"thinking_budget":1024,"include_thoughts":true},"context_window_compression":{"trigger_tokens":1000}},"input":"hi"}`), false)
	if got := ojson.GetBytesNoCopy(out, "generationConfig.maxOutputTokens").Int(); got != 32 {
		t.Fatalf("maxOutputTokens = %d, want 32", got)
	}
	if got := ojson.GetBytesNoCopy(out, "generationConfig.responseSchema.type").String(); got != "object" {
		t.Fatalf("responseSchema.type = %q, want object", got)
	}
	if got := ojson.GetBytesNoCopy(out, "generationConfig.seed").Int(); got != 42 {
		t.Fatalf("seed = %d, want 42", got)
	}
	if got := ojson.GetBytesNoCopy(out, "generationConfig.thinkingConfig.thinkingBudget").Int(); got != 1024 {
		t.Fatalf("thinkingBudget = %d, want 1024", got)
	}
	if got := ojson.GetBytesNoCopy(out, "generationConfig.thinkingConfig.includeThoughts").Bool(); !got {
		t.Fatalf("includeThoughts = false, want true")
	}
	if got := ojson.GetBytesNoCopy(out, "generationConfig.contextWindowCompression.triggerTokens").Int(); got != 1000 {
		t.Fatalf("triggerTokens = %d, want 1000", got)
	}
}

func TestConvertInteractionsRequestToGeminiGenerationConfigProtocolFields(t *testing.T) {
	out, _ := ConvertInteractionsRequestToGemini("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","generation_config":{"tool_choice":"auto","thinking_level":"high","thinking_summaries":"auto"},"stream":true,"input":"hi"}`), true)
	for _, path := range []string{
		"stream",
		"generationConfig.toolChoice",
		"generationConfig.thinkingLevel",
		"generationConfig.thinkingSummaries",
	} {
		if ojson.GetBytesNoCopy(out, path).Exists() {
			t.Fatalf("%s exists, want omitted. Output: %s", path, string(out))
		}
	}
	if got := ojson.GetBytesNoCopy(out, "toolConfig.functionCallingConfig.mode").String(); got != "AUTO" {
		t.Fatalf("toolConfig.functionCallingConfig.mode = %q, want AUTO. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "generationConfig.thinkingConfig.thinkingLevel").String(); got != "high" {
		t.Fatalf("thinkingLevel = %q, want high. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "generationConfig.thinkingConfig.includeThoughts").Bool(); !got {
		t.Fatalf("includeThoughts = false, want true. Output: %s", string(out))
	}
}

func TestConvertGeminiRequestToInteractionsFunctionCall(t *testing.T) {
	out, _ := ConvertGeminiRequestToInteractions("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","contents":[{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{"q":"x"}}}]},{"role":"user","parts":[{"functionResponse":{"name":"lookup","response":{"ok":true}}}]}]}`), false)
	if got := ojson.GetBytesNoCopy(out, "input.0.type").String(); got != "function_call" {
		t.Fatalf("input.0.type = %q, want function_call", got)
	}
	if got := ojson.GetBytesNoCopy(out, "input.0.name").String(); got != "lookup" {
		t.Fatalf("input.0.name = %q, want lookup", got)
	}
	if got := ojson.GetBytesNoCopy(out, "input.1.type").String(); got != "function_result" {
		t.Fatalf("input.1.type = %q, want function_result", got)
	}
}

func TestConvertGeminiRequestToInteractionsTextContentType(t *testing.T) {
	out, _ := ConvertGeminiRequestToInteractions("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`), false)
	if got := ojson.GetBytesNoCopy(out, "input.0.content.0.type").String(); got != "text" {
		t.Fatalf("content.0.type = %q, want text. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "input.0.content.0.text").String(); got != "hi" {
		t.Fatalf("content.0.text = %q, want hi. Output: %s", got, string(out))
	}
}

func TestConvertGeminiRequestToInteractionsMultimodal(t *testing.T) {
	out, _ := ConvertGeminiRequestToInteractions("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"audio/wav","data":"aGVsbG8="}}]}]}`), false)
	if got := ojson.GetBytesNoCopy(out, "input.0.type").String(); got != "user_input" {
		t.Fatalf("input.0.type = %q, want user_input", got)
	}
	if got := ojson.GetBytesNoCopy(out, "input.0.content.0.type").String(); got != "audio" {
		t.Fatalf("content.0.type = %q, want audio", got)
	}
	if got := ojson.GetBytesNoCopy(out, "input.0.content.0.mime_type").String(); got != "audio/wav" {
		t.Fatalf("mime_type = %q, want audio/wav", got)
	}
}

func TestConvertGeminiRequestToInteractionsThought(t *testing.T) {
	out, _ := ConvertGeminiRequestToInteractions("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","contents":[{"role":"model","parts":[{"text":"thinking","thought":true}]}]}`), false)
	if got := ojson.GetBytesNoCopy(out, "input.0.type").String(); got != "thought" {
		t.Fatalf("input.0.type = %q, want thought", got)
	}
}

func TestConvertInteractionsRequestToGeminiTurnWithModelRole(t *testing.T) {
	out, _ := ConvertInteractionsRequestToGemini("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","input":{"role":"model","steps":[{"type":"user_input","content":[{"text":"hi"}]},{"type":"model_output","content":[{"text":"ok"}]}]}}`), false)
	if got := ojson.GetBytesNoCopy(out, "contents.0.role").String(); got != "model" {
		t.Fatalf("contents.0.role = %q, want model", got)
	}
	if got := ojson.GetBytesNoCopy(out, "contents.1.role").String(); got != "model" {
		t.Fatalf("contents.1.role = %q, want model", got)
	}
}

func TestConvertInteractionsRequestToGeminiGenerationConfigPreservesLargeIntegers(t *testing.T) {
	out, _ := ConvertInteractionsRequestToGemini("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","generation_config":{"max_output_tokens":32,"large_identity":9223372036854775807},"input":"hi"}`), false)
	if got := ojson.GetBytesNoCopy(out, "generationConfig.maxOutputTokens").Int(); got != 32 {
		t.Fatalf("maxOutputTokens = %d, want 32", got)
	}
	if got := ojson.GetBytesNoCopy(out, "generationConfig.largeIdentity").String(); got != "9223372036854775807" {
		t.Fatalf("largeIdentity = %q, want 9223372036854775807", got)
	}
}

func TestConvertInteractionsRequestToGeminiFunctionCallPreservesCallID(t *testing.T) {
	out, _ := ConvertInteractionsRequestToGemini("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","input":[{"type":"function_call","name":"lookup","call_id":"call_1","arguments":{"q":"x"}}]}`), false)
	if got := ojson.GetBytesNoCopy(out, "contents.0.parts.0.functionCall.id").String(); got != "call_1" {
		t.Fatalf("functionCall.id = %q, want call_1", got)
	}
	if got := ojson.GetBytesNoCopy(out, "contents.0.parts.0.functionCall.name").String(); got != "lookup" {
		t.Fatalf("functionCall.name = %q, want lookup", got)
	}
	if got := ojson.GetBytesNoCopy(out, "contents.0.parts.0.functionCall.args.q").String(); got != "x" {
		t.Fatalf("functionCall.args.q = %q, want x", got)
	}
}

func TestConvertInteractionsRequestToGeminiFunctionResultPreservesCallID(t *testing.T) {
	out, _ := ConvertInteractionsRequestToGemini("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","input":[{"type":"function_result","name":"lookup","call_id":"call_1","result":{"ok":true}}]}`), false)
	if got := ojson.GetBytesNoCopy(out, "contents.0.parts.0.functionResponse.id").String(); got != "call_1" {
		t.Fatalf("functionResponse.id = %q, want call_1", got)
	}
	if got := ojson.GetBytesNoCopy(out, "contents.0.parts.0.functionResponse.name").String(); got != "lookup" {
		t.Fatalf("functionResponse.name = %q, want lookup", got)
	}
}

func TestConvertGeminiRequestToInteractionsFunctionCallPreservesID(t *testing.T) {
	out, _ := ConvertGeminiRequestToInteractions("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","contents":[{"role":"model","parts":[{"functionCall":{"name":"lookup","id":"call_1","args":{"q":"x"}}}]},{"role":"user","parts":[{"functionResponse":{"name":"lookup","id":"call_1","response":{"ok":true}}}]}]}`), false)
	if got := ojson.GetBytesNoCopy(out, "input.0.call_id").String(); got != "call_1" {
		t.Fatalf("input.0.call_id = %q, want call_1", got)
	}
	if got := ojson.GetBytesNoCopy(out, "input.1.call_id").String(); got != "call_1" {
		t.Fatalf("input.1.call_id = %q, want call_1", got)
	}
}

func TestConvertGeminiRequestToInteractionsFunctionCallPreservesCallID(t *testing.T) {
	out, _ := ConvertGeminiRequestToInteractions("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","contents":[{"role":"model","parts":[{"functionCall":{"name":"lookup","call_id":"call_request_1","args":{"q":"x"}}}]},{"role":"user","parts":[{"functionResponse":{"name":"lookup","call_id":"call_request_1","response":{"ok":true}}}]}]}`), false)
	if got := ojson.GetBytesNoCopy(out, "input.0.call_id").String(); got != "call_request_1" {
		t.Fatalf("input.0.call_id = %q, want call_request_1", got)
	}
	if got := ojson.GetBytesNoCopy(out, "input.1.call_id").String(); got != "call_request_1" {
		t.Fatalf("input.1.call_id = %q, want call_request_1", got)
	}
}

func TestConvertGeminiRequestToInteractionsGenerationConfig(t *testing.T) {
	out, _ := ConvertGeminiRequestToInteractions("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","generationConfig":{"maxOutputTokens":32,"topP":0.8,"thinkingConfig":{"thinkingBudget":1024}},"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`), false)
	if got := ojson.GetBytesNoCopy(out, "generation_config.max_output_tokens").Int(); got != 32 {
		t.Fatalf("max_output_tokens = %d, want 32", got)
	}
	if got := ojson.GetBytesNoCopy(out, "generation_config.top_p").Float(); got != 0.8 {
		t.Fatalf("top_p = %v, want 0.8", got)
	}
	if got := ojson.GetBytesNoCopy(out, "generation_config.thinking_config.thinking_budget").Int(); got != 1024 {
		t.Fatalf("thinking_budget = %d, want 1024", got)
	}
}

func findStepDeltaPayload(events [][]byte) []byte {
	return findEventPayload(events, "step.delta")
}

func findStepDeltaPayloadByType(events [][]byte, deltaType string) []byte {
	for _, event := range events {
		payload := ssePayload(event)
		if eventName(event, payload) == "step.delta" && ojson.GetBytesNoCopy(payload, "delta.type").String() == deltaType {
			return payload
		}
	}
	return nil
}

func findCompletedPayload(events [][]byte) []byte {
	return findEventPayload(events, "interaction.completed")
}

func findEventPayload(events [][]byte, eventType string) []byte {
	return findNthEventPayload(events, eventType, 0)
}

func findNthEventPayload(events [][]byte, eventType string, n int) []byte {
	for _, event := range events {
		payload := ssePayload(event)
		if eventName(event, payload) == eventType {
			if n == 0 {
				return payload
			}
			n--
		}
	}
	return nil
}

func eventTypes(events [][]byte) []byte {
	var out []byte
	for _, event := range events {
		payload := ssePayload(event)
		eventType := eventName(event, payload)
		if eventType == "" {
			continue
		}
		if len(out) > 0 {
			out = append(out, ',')
		}
		out = append(out, eventType...)
	}
	return out
}

func countEventType(events [][]byte, eventType string) int {
	count := 0
	for _, event := range events {
		payload := ssePayload(event)
		if eventName(event, payload) == eventType {
			count++
		}
	}
	return count
}

func eventName(event, payload []byte) string {
	if eventType := ojson.GetBytesNoCopy(payload, "event_type").String(); eventType != "" {
		return eventType
	}
	const prefix = "event: "
	lineEnd := bytes.IndexByte(event, '\n')
	if lineEnd < 0 || !bytes.HasPrefix(event, []byte(prefix)) {
		return ""
	}
	return string(event[len(prefix):lineEnd])
}

func ssePayload(event []byte) []byte {
	const prefix = "\ndata: "
	idx := bytes.Index(event, []byte(prefix))
	if idx < 0 {
		return nil
	}
	return bytes.TrimRight(event[idx+len(prefix):], "\r\n")
}

func TestConvertInteractionsRequestToGeminiBuiltinTools(t *testing.T) {
	t.Run("url_context only", func(t *testing.T) {
		inputJSON := []byte(`{
			"model":"gemini-3.5-flash",
			"input":"read url",
			"tools":[{"type":"url_context"}]
		}`)
		out, _ := ConvertInteractionsRequestToGemini("gemini-3.5-flash", inputJSON, false)
		tools := ojson.GetBytesNoCopy(out, "tools").Array()
		if len(tools) != 1 {
			t.Fatalf("expected 1 tool, got %d. Output: %s", len(tools), string(out))
		}
		if !tools[0].Get("urlContext").Exists() {
			t.Fatalf("expected urlContext tool, got %s", tools[0].Raw)
		}
		if tools[0].Get("type").Exists() {
			t.Fatalf("expected type field to be omitted from upstream tool, got %s", tools[0].Raw)
		}
	})

	t.Run("code_execution only", func(t *testing.T) {
		inputJSON := []byte(`{
			"model":"gemini-3.5-flash",
			"input":"execute code",
			"tools":[{"type":"code_execution"}]
		}`)
		out, _ := ConvertInteractionsRequestToGemini("gemini-3.5-flash", inputJSON, false)
		tools := ojson.GetBytesNoCopy(out, "tools").Array()
		if len(tools) != 1 {
			t.Fatalf("expected 1 tool, got %d. Output: %s", len(tools), string(out))
		}
		if !tools[0].Get("codeExecution").Exists() {
			t.Fatalf("expected codeExecution tool, got %s", tools[0].Raw)
		}
		if tools[0].Get("type").Exists() {
			t.Fatalf("expected type field to be omitted from upstream tool, got %s", tools[0].Raw)
		}
	})

	t.Run("google_search only", func(t *testing.T) {
		inputJSON := []byte(`{
			"model":"gemini-3.5-flash",
			"input":"search web",
			"tools":[{"type":"google_search"}]
		}`)
		out, _ := ConvertInteractionsRequestToGemini("gemini-3.5-flash", inputJSON, false)
		tools := ojson.GetBytesNoCopy(out, "tools").Array()
		if len(tools) != 1 {
			t.Fatalf("expected 1 tool, got %d. Output: %s", len(tools), string(out))
		}
		if !tools[0].Get("googleSearch").Exists() {
			t.Fatalf("expected googleSearch tool, got %s", tools[0].Raw)
		}
		if tools[0].Get("type").Exists() {
			t.Fatalf("expected type field to be omitted from upstream tool, got %s", tools[0].Raw)
		}
	})

	t.Run("reverse gemini to interactions builtin tools", func(t *testing.T) {
		inputJSON := []byte(`{
			"model":"gemini-3.5-flash",
			"contents":[{"role":"user","parts":[{"text":"hello"}]}],
			"tools":[{"urlContext":{}},{"codeExecution":{}},{"googleSearch":{}}]
		}`)
		out, _ := ConvertGeminiRequestToInteractions("gemini-3.5-flash", inputJSON, false)
		tools := ojson.GetBytesNoCopy(out, "tools").Array()
		if len(tools) != 3 {
			t.Fatalf("expected 3 tools, got %d. Output: %s", len(tools), string(out))
		}
		if got := tools[0].Get("type").String(); got != "url_context" {
			t.Fatalf("expected url_context, got %s", got)
		}
		if got := tools[1].Get("type").String(); got != "code_execution" {
			t.Fatalf("expected code_execution, got %s", got)
		}
		if got := tools[2].Get("type").String(); got != "google_search" {
			t.Fatalf("expected google_search, got %s", got)
		}
	})

	t.Run("reverse gemini to interactions with options preserved", func(t *testing.T) {
		inputJSON := []byte(`{
			"model":"gemini-3.5-flash",
			"contents":[{"role":"user","parts":[{"text":"hello"}]}],
			"tools":[{"googleSearch":{"mode":"search"}},{"codeExecution":{"sandbox":true}}]
		}`)
		out, _ := ConvertGeminiRequestToInteractions("gemini-3.5-flash", inputJSON, false)
		tools := ojson.GetBytesNoCopy(out, "tools").Array()
		if len(tools) != 2 {
			t.Fatalf("expected 2 tools, got %d. Output: %s", len(tools), string(out))
		}
		if tools[0].Get("google_search").Exists() {
			t.Fatalf("expected google_search wrapper omitted, got %s", tools[0].Raw)
		}
		if got := tools[0].Get("mode").String(); got != "search" {
			t.Fatalf("expected mode=search, got %s", tools[0].Raw)
		}
		if tools[1].Get("code_execution").Exists() {
			t.Fatalf("expected code_execution wrapper omitted, got %s", tools[1].Raw)
		}
		if got := tools[1].Get("sandbox").Bool(); !got {
			t.Fatalf("expected sandbox=true, got %s", tools[1].Raw)
		}
	})

	t.Run("nested parameters and aliases preserved", func(t *testing.T) {
		inputJSON := []byte(`{
			"model":"gemini-3.5-flash",
			"input":"test options",
			"tools":[
				{"type":"url_context","url_context":{"max_urls":3}},
				{"code_execution":{"environment":"sandbox"}},
				{"type":"web_search","google_search":{"mode":"search"}}
			]
		}`)
		out, _ := ConvertInteractionsRequestToGemini("gemini-3.5-flash", inputJSON, false)
		tools := ojson.GetBytesNoCopy(out, "tools").Array()
		if len(tools) != 3 {
			t.Fatalf("expected 3 tools, got %d. Output: %s", len(tools), string(out))
		}
		if got := tools[0].Get("urlContext.max_urls").Int(); got != 3 {
			t.Fatalf("expected urlContext.max_urls=3, got %d. Tool: %s", got, tools[0].Raw)
		}
		if got := tools[1].Get("codeExecution.environment").String(); got != "sandbox" {
			t.Fatalf("expected codeExecution.environment=sandbox, got %s. Tool: %s", got, tools[1].Raw)
		}
		if got := tools[2].Get("googleSearch.mode").String(); got != "search" {
			t.Fatalf("expected googleSearch.mode=search, got %s. Tool: %s", got, tools[2].Raw)
		}
	})

	t.Run("native composite tools preserved without truncation", func(t *testing.T) {
		inputJSON := []byte(`{
			"model":"gemini-3.5-flash",
			"input":"native composite tools",
			"tools":[{"googleSearch":{},"urlContext":{}}]
		}`)
		out, _ := ConvertInteractionsRequestToGemini("gemini-3.5-flash", inputJSON, false)
		tools := ojson.GetBytesNoCopy(out, "tools").Array()
		if len(tools) != 1 {
			t.Fatalf("expected 1 tool node, got %d. Output: %s", len(tools), string(out))
		}
		if !tools[0].Get("googleSearch").Exists() {
			t.Fatalf("expected googleSearch preserved in composite tool, got %s", tools[0].Raw)
		}
		if !tools[0].Get("urlContext").Exists() {
			t.Fatalf("expected urlContext preserved in composite tool, got %s", tools[0].Raw)
		}

		// Reverse conversion from Gemini composite tool
		geminiReq := []byte(`{
			"model":"gemini-3.5-flash",
			"contents":[{"role":"user","parts":[{"text":"hello"}]}],
			"tools":[{"googleSearch":{},"urlContext":{}}]
		}`)
		interactionsOut, _ := ConvertGeminiRequestToInteractions("gemini-3.5-flash", geminiReq, false)
		interTools := ojson.GetBytesNoCopy(interactionsOut, "tools").Array()
		if len(interTools) != 2 {
			t.Fatalf("expected 2 converted tools from composite, got %d. Output: %s", len(interTools), string(interactionsOut))
		}
		hasURL := false
		hasSearch := false
		for _, it := range interTools {
			if it.Get("type").String() == "url_context" {
				hasURL = true
			}
			if it.Get("type").String() == "google_search" {
				hasSearch = true
			}
		}
		if !hasURL || !hasSearch {
			t.Fatalf("expected both url_context and google_search converted from composite, got url=%v, search=%v", hasURL, hasSearch)
		}
	})

	t.Run("unrecognized tool retained and not silently dropped", func(t *testing.T) {
		inputJSON := []byte(`{
			"model":"gemini-3.5-flash",
			"input":"unrecognized tool",
			"tools":[{"type":"file_search","file_search":{"max_results":5}}]
		}`)
		out, _ := ConvertInteractionsRequestToGemini("gemini-3.5-flash", inputJSON, false)
		tools := ojson.GetBytesNoCopy(out, "tools").Array()
		if len(tools) != 1 {
			t.Fatalf("expected 1 tool retained, got %d. Output: %s", len(tools), string(out))
		}
		if got := tools[0].Get("type").String(); got != "file_search" {
			t.Fatalf("expected tool type file_search retained, got %s", tools[0].Raw)
		}
	})
}

func TestConvertInteractionsRequestToGemini_FunctionResponseJSONRef(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gemini-3.5-flash",
		"input": [
			{
				"type": "function_result",
				"name": "lookup",
				"call_id": "call_1",
				"result": {
					"schema": {
						"$ref": "#/components/schemas/ErrorModel"
					}
				}
			}
		]
	}`)
	out, _ := ConvertInteractionsRequestToGemini("gemini-3.5-flash", inputJSON, false)
	val := ojson.GetBytesNoCopy(out, "contents.0.parts.0.functionResponse.response.result")
	if val.Type != ojson.String {
		t.Fatalf("expected functionResponse.response.result to be string, got %s (raw: %s)", val.Type, val.Raw)
	}
	if !strings.Contains(val.String(), "#/components/schemas/ErrorModel") {
		t.Fatalf("expected string result to contain ref target, got %q", val.String())
	}
}

func TestConvertInteractionsResponseToGemini_FunctionCallArgsPreservesRef(t *testing.T) {
	raw := []byte(`{
		"id": "i1",
		"model": "gemini-3.1-flash-lite",
		"steps": [
			{
				"type": "function_call",
				"call_id": "call_1",
				"name": "validate_schema",
				"arguments": {
					"schema": {
						"$ref": "#/components/schemas/ErrorModel"
					}
				}
			}
		]
	}`)
	out := ConvertInteractionsResponseToGeminiNonStream(context.Background(), "gemini-3.1-flash-lite", nil, nil, raw, nil)
	args := ojson.GetBytesNoCopy(out, "candidates.0.content.parts.0.functionCall.args")
	if !args.IsObject() {
		t.Fatalf("expected functionCall.args to remain an object, got %s (raw: %s)", args.Type, args.Raw)
	}
	if got := args.Get("schema.$ref").String(); got != "#/components/schemas/ErrorModel" {
		t.Fatalf("expected schema.$ref to be preserved in args object, got %q", got)
	}
}

func TestConvertInteractionsResponseToGemini_FunctionResultWithRef(t *testing.T) {
	raw := []byte(`{
		"id": "i1",
		"model": "gemini-3.1-flash-lite",
		"steps": [
			{
				"type": "function_result",
				"call_id": "call_1",
				"name": "get_schema",
				"result": {
					"schema": {
						"$ref": "#/components/schemas/ErrorModel"
					}
				}
			}
		]
	}`)
	out := ConvertInteractionsResponseToGeminiNonStream(context.Background(), "gemini-3.1-flash-lite", nil, nil, raw, nil)
	val := ojson.GetBytesNoCopy(out, "candidates.0.content.parts.0.functionResponse.response.result")
	if val.Type != ojson.String {
		t.Fatalf("expected functionResponse.response.result to be string, got %s (raw: %s)", val.Type, val.Raw)
	}
	if !strings.Contains(val.String(), "#/components/schemas/ErrorModel") {
		t.Fatalf("expected string result to contain ref target, got %q", val.String())
	}
}

func TestConvertInteractionsRequestToGemini_ParallelToolCallsHistory(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gemini-3.5-flash",
		"input": [
			{"type": "user_input", "content": [{"type": "text", "text": "run tools"}]},
			{"type": "thought", "signature": "sig_turn1"},
			{"type": "function_call", "name": "f1", "call_id": "c1", "arguments": {"a": 1}},
			{"type": "function_call", "name": "f2", "call_id": "c2", "arguments": {"b": 2}},
			{"type": "function_call", "name": "f3", "call_id": "c3", "arguments": {"c": 3}},
			{"type": "function_result", "name": "f1", "call_id": "c1", "result": {"r": 1}},
			{"type": "function_result", "name": "f2", "call_id": "c2", "result": {"r": 2}},
			{"type": "function_result", "name": "f3", "call_id": "c3", "result": {"r": 3}}
		]
	}`)
	out, _ := ConvertInteractionsRequestToGemini("gemini-3.5-flash", inputJSON, false)
	contents := ojson.GetBytesNoCopy(out, "contents").Array()
	if len(contents) != 3 {
		t.Fatalf("expected 3 contents (user, model, user), got %d: %s", len(contents), string(out))
	}
	if err := signature.ValidateGeminiFunctionCallPairing(out); err != nil {
		t.Fatalf("ValidateGeminiFunctionCallPairing failed: %v", err)
	}
	modelParts := contents[1].Get("parts").Array()
	if len(modelParts) != 3 {
		t.Fatalf("expected 3 model parts, got %d", len(modelParts))
	}
	if sig := modelParts[0].Get("thoughtSignature").String(); sig != "sig_turn1" {
		t.Fatalf("expected first functionCall thoughtSignature to be %q, got %q", "sig_turn1", sig)
	}
	userParts := contents[2].Get("parts").Array()
	if len(userParts) != 3 {
		t.Fatalf("expected 3 user functionResponse parts, got %d", len(userParts))
	}
}

func TestConvertInteractionsRequestToGemini_ThoughtSummaryAndSignature(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gemini-3.5-flash",
		"input": [
			{"type": "thought", "summary": [{"type": "text", "text": "my thinking"}], "signature": "sig_thought"},
			{"type": "model_output", "content": [{"type": "text", "text": "my answer"}]}
		]
	}`)
	out, _ := ConvertInteractionsRequestToGemini("gemini-3.5-flash", inputJSON, false)
	contents := ojson.GetBytesNoCopy(out, "contents").Array()
	if len(contents) != 1 {
		t.Fatalf("expected 1 model content, got %d: %s", len(contents), string(out))
	}
	parts := contents[0].Get("parts").Array()
	hasThoughtText := false
	hasSig := false
	for _, p := range parts {
		if p.Get("thought").Bool() && strings.Contains(p.Get("text").String(), "my thinking") {
			hasThoughtText = true
		}
		if p.Get("thoughtSignature").String() == "sig_thought" {
			hasSig = true
		}
	}
	if !hasThoughtText {
		t.Fatalf("expected thought text from summary in parts, got: %s", string(out))
	}
	if !hasSig {
		t.Fatalf("expected thoughtSignature in parts, got: %s", string(out))
	}
}

func TestConvertGeminiResponseToInteractionsNonStream_ThoughtSignature(t *testing.T) {
	raw := []byte(`{
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [
					{"functionCall": {"name": "lookup", "id": "call_1", "args": {"q": "x"}}, "thoughtSignature": "sig_fc"},
					{"functionCall": {"name": "search", "id": "call_2", "args": {"q": "y"}}}
				]
			}
		}]
	}`)
	out := ConvertGeminiResponseToInteractionsNonStream(context.Background(), "gemini-3.5-flash", nil, nil, raw, nil)
	steps := ojson.GetBytesNoCopy(out, "steps").Array()
	if len(steps) != 3 {
		t.Fatalf("expected 3 steps (thought, function_call, function_call), got %d: %s", len(steps), string(out))
	}
	if steps[0].Get("type").String() != "thought" || steps[0].Get("signature").String() != "sig_fc" {
		t.Fatalf("step[0] expected thought with signature sig_fc, got: %s", steps[0].Raw)
	}
	if steps[1].Get("type").String() != "function_call" || steps[1].Get("name").String() != "lookup" {
		t.Fatalf("step[1] expected function_call lookup, got: %s", steps[1].Raw)
	}
	if steps[2].Get("type").String() != "function_call" || steps[2].Get("name").String() != "search" {
		t.Fatalf("step[2] expected function_call search, got: %s", steps[2].Raw)
	}
}

func TestConvertGeminiResponseToInteractionsStream_TrailingThoughtSignature(t *testing.T) {
	var param any
	chunk := []byte(`{"candidates":[{"content":{"parts":[{"text":"","thoughtSignature":"sig_trailing"}]}}]}`)
	out := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.5-flash", nil, nil, chunk, &param)
	foundSig := false
	for _, frame := range out {
		if bytes.Contains(frame, []byte("thought_signature")) && bytes.Contains(frame, []byte("sig_trailing")) {
			foundSig = true
			break
		}
	}
	if !foundSig {
		t.Fatalf("expected thought_signature delta in stream output, got: %v", string(bytes.Join(out, []byte("\n"))))
	}
}

func TestConvertGeminiRequestToInteractions_SignedFunctionCallPreserved(t *testing.T) {
	geminiReq := []byte(`{
		"model": "gemini-3.5-flash",
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "lookup", "id": "call_1", "args": {"q": "x"}}, "thoughtSignature": "sig_fc_pres"}
				]
			}
		]
	}`)
	out, _ := ConvertGeminiRequestToInteractions("gemini-3.5-flash", geminiReq, false)
	steps := ojson.GetBytesNoCopy(out, "input").Array()
	if len(steps) != 2 {
		t.Fatalf("expected 2 steps (thought, function_call), got %d: %s", len(steps), string(out))
	}
	if steps[0].Get("type").String() != "thought" || steps[0].Get("signature").String() != "sig_fc_pres" {
		t.Fatalf("step[0] expected thought with sig_fc_pres, got: %s", steps[0].Raw)
	}
	if steps[1].Get("type").String() != "function_call" || steps[1].Get("name").String() != "lookup" {
		t.Fatalf("step[1] expected function_call lookup, got: %s", steps[1].Raw)
	}
}

func TestConvertInteractionsRequestToGemini_InterleavedModelTurnSteps(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gemini-3.5-flash",
		"input": [
			{"type": "function_call", "name": "f1", "call_id": "c1", "arguments": {"a": 1}, "signature": "sig1"},
			{"type": "model_output", "content": [{"type": "text", "text": "explanation"}]},
			{"type": "function_call", "name": "f2", "call_id": "c2", "arguments": {"b": 2}},
			{"type": "function_result", "name": "f1", "call_id": "c1", "result": {"r": 1}},
			{"type": "function_result", "name": "f2", "call_id": "c2", "result": {"r": 2}}
		]
	}`)
	out, _ := ConvertInteractionsRequestToGemini("gemini-3.5-flash", inputJSON, false)
	contents := ojson.GetBytesNoCopy(out, "contents").Array()
	if len(contents) != 2 {
		t.Fatalf("expected 2 contents (model, user), got %d: %s", len(contents), string(out))
	}
	if err := signature.ValidateGeminiFunctionCallPairing(out); err != nil {
		t.Fatalf("ValidateGeminiFunctionCallPairing failed: %v", err)
	}
	modelParts := contents[0].Get("parts").Array()
	if len(modelParts) != 3 {
		t.Fatalf("expected 3 model parts (fc1, text, fc2), got %d: %s", len(modelParts), contents[0].Raw)
	}
	if sig := modelParts[0].Get("thoughtSignature").String(); sig != "sig1" {
		t.Fatalf("expected fc1 thoughtSignature = sig1, got %q", sig)
	}
	userParts := contents[1].Get("parts").Array()
	if len(userParts) != 2 {
		t.Fatalf("expected 2 user functionResponse parts, got %d: %s", len(userParts), contents[1].Raw)
	}
}

func TestConvertInteractionsRequestToGemini_ResponseToRequestRoundTrip(t *testing.T) {
	upstreamResponse := []byte(`{
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [
					{"functionCall": {"name": "lookup", "id": "call_1", "args": {"q": "x"}}, "thoughtSignature": "sig_rt_fc"},
					{"functionCall": {"name": "search", "id": "call_2", "args": {"q": "y"}}}
				]
			}
		}]
	}`)
	respOut := ConvertGeminiResponseToInteractionsNonStream(context.Background(), "gemini-3.5-flash", nil, nil, upstreamResponse, nil)
	steps := ojson.GetBytesNoCopy(respOut, "steps").Raw

	// Build Turn 2 input: user question + Turn 1 steps + tool results
	turn2Input := `{"model":"gemini-3.5-flash","input":[{"type":"user_input","content":[{"type":"text","text":"hello"}]}`
	for _, s := range ojson.Get(steps, "@this").Array() {
		turn2Input += "," + s.Raw
	}
	turn2Input += `,{"type":"function_result","name":"lookup","call_id":"call_1","result":{"ok":true}},{"type":"function_result","name":"search","call_id":"call_2","result":{"found":true}}]}`

	reqOut, _ := ConvertInteractionsRequestToGemini("gemini-3.5-flash", []byte(turn2Input), false)
	contents := ojson.GetBytesNoCopy(reqOut, "contents").Array()
	if len(contents) != 3 {
		t.Fatalf("expected 3 contents (user, model, user), got %d: %s", len(contents), string(reqOut))
	}
	if err := signature.ValidateGeminiFunctionCallPairing(reqOut); err != nil {
		t.Fatalf("ValidateGeminiFunctionCallPairing failed: %v", err)
	}
	modelParts := contents[1].Get("parts").Array()
	if len(modelParts) != 2 {
		t.Fatalf("expected 2 model parts, got %d", len(modelParts))
	}
	if sig := modelParts[0].Get("thoughtSignature").String(); sig != "sig_rt_fc" {
		t.Fatalf("expected round-tripped thoughtSignature = sig_rt_fc, got %q", sig)
	}
}

func TestConvertInteractionsRequestToGemini_MultipleSignaturesPreserved(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gemini-3.5-flash",
		"input": [
			{"type": "thought", "summary": [{"type": "text", "text": "thought 1"}], "signature": "sig_thought_1"},
			{"type": "thought", "signature": "sig_fc_1"},
			{"type": "function_call", "name": "f1", "call_id": "c1", "arguments": {}}
		]
	}`)
	out, _ := ConvertInteractionsRequestToGemini("gemini-3.5-flash", inputJSON, false)
	contents := ojson.GetBytesNoCopy(out, "contents").Array()
	if len(contents) != 1 {
		t.Fatalf("expected 1 model content, got %d: %s", len(contents), string(out))
	}
	parts := contents[0].Get("parts").Array()
	var foundS1, foundS2 bool
	for _, p := range parts {
		if p.Get("thoughtSignature").String() == "sig_thought_1" {
			foundS1 = true
		}
		if p.Get("functionCall").Exists() && p.Get("thoughtSignature").String() == "sig_fc_1" {
			foundS2 = true
		}
	}
	if !foundS1 || !foundS2 {
		t.Fatalf("expected both sig_thought_1 and sig_fc_1 preserved, got parts: %s", contents[0].Get("parts").Raw)
	}
}

func TestConvertInteractionsRequestToGemini_TrailingSignatureRoundTrip(t *testing.T) {
	upstreamResponse := []byte(`{
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [
					{"text": "answer"},
					{"text": "", "thoughtSignature": "sig_trailing_rt"}
				]
			}
		}]
	}`)
	respOut := ConvertGeminiResponseToInteractionsNonStream(context.Background(), "gemini-3.5-flash", nil, nil, upstreamResponse, nil)
	steps := ojson.GetBytesNoCopy(respOut, "steps").Raw

	turn2Input := `{"model":"gemini-3.5-flash","input":` + steps + `}`
	reqOut, _ := ConvertInteractionsRequestToGemini("gemini-3.5-flash", []byte(turn2Input), false)
	contents := ojson.GetBytesNoCopy(reqOut, "contents").Array()
	if len(contents) != 1 {
		t.Fatalf("expected 1 model content, got %d: %s", len(contents), string(reqOut))
	}
	parts := contents[0].Get("parts").Array()
	var foundText, foundSig bool
	for _, p := range parts {
		if p.Get("text").String() == "answer" {
			foundText = true
		}
		if p.Get("thoughtSignature").String() == "sig_trailing_rt" {
			foundSig = true
		}
	}
	if !foundText || !foundSig {
		t.Fatalf("expected both answer and sig_trailing_rt preserved, got parts: %s", contents[0].Get("parts").Raw)
	}
}

func TestConvertInteractionsRequestToGemini_ExplicitSignatureClearsPending(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gemini-3.5-flash",
		"input": [
			{"type": "thought", "signature": "sig_shared"},
			{"type": "function_call", "name": "f1", "call_id": "c1", "arguments": {}, "signature": "sig_shared"},
			{"type": "function_call", "name": "f2", "call_id": "c2", "arguments": {}}
		]
	}`)
	out, _ := ConvertInteractionsRequestToGemini("gemini-3.5-flash", inputJSON, false)
	contents := ojson.GetBytesNoCopy(out, "contents").Array()
	if len(contents) != 1 {
		t.Fatalf("expected 1 model content, got %d: %s", len(contents), string(out))
	}
	parts := contents[0].Get("parts").Array()
	if len(parts) != 2 {
		t.Fatalf("expected exactly 2 parts (no extra carrier), got %d: %s", len(parts), contents[0].Raw)
	}
	if sig := parts[0].Get("thoughtSignature").String(); sig != "sig_shared" {
		t.Fatalf("expected f1 thoughtSignature = sig_shared, got %q", sig)
	}
	if sig := parts[1].Get("thoughtSignature").String(); sig != "" {
		t.Fatalf("expected f2 thoughtSignature to be empty, got %q", sig)
	}
}

func TestConvertInteractionsResponseToGemini_ResponseFailed(t *testing.T) {
	tests := []struct {
		name       string
		payload    string
		wantCode   int64
		wantStatus string
		wantMsg    string
	}{
		{
			name:       "403_permission_denied",
			payload:    `data: {"event_type":"response.failed","error":{"message":"devin upstream error: permission_denied","code":"403"}}`,
			wantCode:   403,
			wantStatus: "PERMISSION_DENIED",
			wantMsg:    "permission_denied",
		},
		{
			name:       "429_resource_exhausted",
			payload:    `data: {"event_type":"interaction.failed","error":{"message":"quota exceeded","code":"resource_exhausted"}}`,
			wantCode:   429,
			wantStatus: "RESOURCE_EXHAUSTED",
			wantMsg:    "quota exceeded",
		},
		{
			name:       "503_unavailable_nested",
			payload:    `data: {"event_type":"response.failed","interaction":{"error":{"message":"service unavailable","code":"503"}}}`,
			wantCode:   503,
			wantStatus: "UNAVAILABLE",
			wantMsg:    "service unavailable",
		},
		{
			name:       "default_fallback",
			payload:    `data: {"event_type":"response.failed"}`,
			wantCode:   500,
			wantStatus: "INTERNAL",
			wantMsg:    "upstream error occurred",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var param any
			events := ConvertInteractionsResponseToGemini(context.Background(), "devin/kimi-k3", nil, nil, []byte(tt.payload), &param)
			if len(events) != 1 {
				t.Fatalf("expected 1 event, got %d", len(events))
			}
			gotCode := ojson.GetBytesNoCopy(events[0], "error.code").Int()
			if gotCode != tt.wantCode {
				t.Errorf("error.code = %d, want %d", gotCode, tt.wantCode)
			}
			gotStatus := ojson.GetBytesNoCopy(events[0], "error.status").String()
			if gotStatus != tt.wantStatus {
				t.Errorf("error.status = %q, want %q", gotStatus, tt.wantStatus)
			}
			gotMsg := ojson.GetBytesNoCopy(events[0], "error.message").String()
			if !strings.Contains(gotMsg, tt.wantMsg) {
				t.Errorf("error.message = %q, want containing %q", gotMsg, tt.wantMsg)
			}
		})
	}
}

func TestConvertGeminiRequestToInteractions_ResponseModalitiesAndImageConfig(t *testing.T) {
	t.Run("image modality and imageConfig map to top-level response_format", func(t *testing.T) {
		inputJSON := []byte(`{
			"contents":[{"role":"user","parts":[{"text":"Minimalist editorial weather card"}]}],
			"generationConfig":{
				"responseModalities":["IMAGE"],
				"imageConfig":{"aspectRatio":"16:9","imageSize":"1K"},
				"thinkingConfig":{"thinkingLevel":"minimal"}
			}
		}`)
		out, err := ConvertGeminiRequestToInteractions("gemini-3.1-flash-lite-image", inputJSON, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ojson.GetBytesNoCopy(out, "generation_config.response_modalities").Exists() {
			t.Fatalf("generation_config.response_modalities must be omitted, got: %s", string(out))
		}
		if ojson.GetBytesNoCopy(out, "generation_config.image_config").Exists() {
			t.Fatalf("generation_config.image_config must be omitted, got: %s", string(out))
		}
		if ojson.GetBytesNoCopy(out, "response_modalities").Exists() {
			t.Fatalf("response_modalities must be migrated to response_format, got: %s", string(out))
		}
		if got := ojson.GetBytesNoCopy(out, "response_format.type").String(); got != "image" {
			t.Fatalf("response_format.type = %q, want image; out=%s", got, string(out))
		}
		if got := ojson.GetBytesNoCopy(out, "response_format.aspect_ratio").String(); got != "16:9" {
			t.Fatalf("response_format.aspect_ratio = %q, want 16:9; out=%s", got, string(out))
		}
		if got := ojson.GetBytesNoCopy(out, "response_format.image_size").String(); got != "1K" {
			t.Fatalf("response_format.image_size = %q, want 1K; out=%s", got, string(out))
		}
		if got := ojson.GetBytesNoCopy(out, "generation_config.thinking_level").String(); got != "minimal" {
			t.Fatalf("generation_config.thinking_level = %q, want minimal; out=%s", got, string(out))
		}
	})

	t.Run("multiple modalities map to response_format array", func(t *testing.T) {
		inputJSON := []byte(`{
			"contents":[{"role":"user","parts":[{"text":"draw and explain"}]}],
			"generationConfig":{
				"responseModalities":["TEXT","IMAGE"],
				"imageConfig":{"aspectRatio":"4:3"}
			}
		}`)
		out, err := ConvertGeminiRequestToInteractions("gemini-3.1-flash-lite-image", inputJSON, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		formats := ojson.GetBytesNoCopy(out, "response_format").Array()
		if len(formats) != 2 {
			t.Fatalf("response_format len = %d, want 2; out=%s", len(formats), string(out))
		}
		if got := formats[0].Get("type").String(); got != "text" {
			t.Fatalf("response_format[0].type = %q, want text; out=%s", got, string(out))
		}
		if got := formats[1].Get("type").String(); got != "image" {
			t.Fatalf("response_format[1].type = %q, want image; out=%s", got, string(out))
		}
		if got := formats[1].Get("aspect_ratio").String(); got != "4:3" {
			t.Fatalf("response_format[1].aspect_ratio = %q, want 4:3; out=%s", got, string(out))
		}
	})

	t.Run("responseMimeType and responseSchema map to text response_format preserving schema keys", func(t *testing.T) {
		inputJSON := []byte(`{
			"contents":[{"role":"user","parts":[{"text":"json"}]}],
			"generationConfig":{
				"responseMimeType":"application/json",
				"responseSchema":{"type":"object","properties":{"userName":{"type":"string"}}}
			}
		}`)
		out, err := ConvertGeminiRequestToInteractions("gemini-3.5-flash", inputJSON, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ojson.GetBytesNoCopy(out, "generation_config.response_mime_type").Exists() {
			t.Fatalf("generation_config.response_mime_type must be omitted, got: %s", string(out))
		}
		if ojson.GetBytesNoCopy(out, "generation_config.response_schema").Exists() {
			t.Fatalf("generation_config.response_schema must be omitted, got: %s", string(out))
		}
		if got := ojson.GetBytesNoCopy(out, "response_format.type").String(); got != "text" {
			t.Fatalf("response_format.type = %q, want text; out=%s", got, string(out))
		}
		if got := ojson.GetBytesNoCopy(out, "response_format.mime_type").String(); got != "application/json" {
			t.Fatalf("response_format.mime_type = %q, want application/json; out=%s", got, string(out))
		}
		if got := ojson.GetBytesNoCopy(out, "response_format.schema.properties.userName.type").String(); got != "string" {
			t.Fatalf("response_format.schema.properties.userName.type = %q, want string; out=%s", got, string(out))
		}
	})
}

func TestConvertGeminiRequestToInteractions_GoogleSearchAndBuiltinToolOptions(t *testing.T) {
	inputJSON := []byte(`{
		"contents":[{"role":"user","parts":[{"text":"weather"}]}],
		"tools":[
			{"googleSearch":{"searchTypes":{"webSearch":{},"imageSearch":{}}}},
			{"urlContext":{"maxUrls":3}},
			{"codeExecution":{"sandbox":true}}
		]
	}`)
	out, err := ConvertGeminiRequestToInteractions("gemini-nano-banana-2.1", inputJSON, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tools := ojson.GetBytesNoCopy(out, "tools").Array()
	if len(tools) != 3 {
		t.Fatalf("expected 3 tools, got %d; out=%s", len(tools), string(out))
	}

	if got := tools[0].Get("type").String(); got != "google_search" {
		t.Fatalf("tools[0].type = %q, want google_search; tool=%s", got, tools[0].Raw)
	}
	if tools[0].Get("google_search").Exists() {
		t.Fatalf("tools[0] must not nest options under google_search; tool=%s", tools[0].Raw)
	}
	searchTypes := tools[0].Get("search_types").Array()
	if len(searchTypes) != 2 || searchTypes[0].String() != "web_search" || searchTypes[1].String() != "image_search" {
		t.Fatalf("tools[0].search_types = %s, want [web_search, image_search]; tool=%s", tools[0].Get("search_types").Raw, tools[0].Raw)
	}

	if got := tools[1].Get("type").String(); got != "url_context" {
		t.Fatalf("tools[1].type = %q, want url_context; tool=%s", got, tools[1].Raw)
	}
	if tools[1].Get("url_context").Exists() {
		t.Fatalf("tools[1] must not nest options under url_context; tool=%s", tools[1].Raw)
	}
	if got := tools[1].Get("max_urls").Int(); got != 3 {
		t.Fatalf("tools[1].max_urls = %d, want 3; tool=%s", got, tools[1].Raw)
	}

	if got := tools[2].Get("type").String(); got != "code_execution" {
		t.Fatalf("tools[2].type = %q, want code_execution; tool=%s", got, tools[2].Raw)
	}
	if tools[2].Get("code_execution").Exists() {
		t.Fatalf("tools[2] must not nest options under code_execution; tool=%s", tools[2].Raw)
	}
	if !tools[2].Get("sandbox").Bool() {
		t.Fatalf("tools[2].sandbox = false, want true; tool=%s", tools[2].Raw)
	}
}

func TestConvertGeminiRequestToInteractions_FunctionResponsePartsMedia(t *testing.T) {
	t.Run("inlineData image and PDF alongside response result", func(t *testing.T) {
		inputJSON := []byte(`{
			"model": "gemini-3.5-flash",
			"contents": [
				{"role": "model", "parts": [{"functionCall": {"name": "read_files", "id": "call_1", "args": {}}}]},
				{"role": "user", "parts": [{
					"functionResponse": {
						"name": "read_files",
						"id": "call_1",
						"response": {"result": "Loaded files"},
						"parts": [
							{"inlineData": {"mimeType": "image/png", "data": "iVBORw0KGgo="}},
							{"inlineData": {"mimeType": "application/pdf", "data": "JVBERi0xLjQK"}}
						]
					}
				}]}
			]
		}`)

		out, err := ConvertGeminiRequestToInteractions("gemini-3.5-flash", inputJSON, false)
		if err != nil {
			t.Fatalf("ConvertGeminiRequestToInteractions() error = %v", err)
		}
		step := ojson.GetBytesNoCopy(out, "input.1")
		if got := step.Get("type").String(); got != "function_result" {
			t.Fatalf("input.1.type = %q, want function_result; out=%s", got, string(out))
		}
		result := step.Get("result")
		if !result.IsArray() {
			t.Fatalf("expected function_result.result to be array, got %s; out=%s", result.Raw, string(out))
		}
		items := result.Array()
		if len(items) != 3 {
			t.Fatalf("expected 3 items (text + image + document), got %d: %s", len(items), result.Raw)
		}
		if items[0].Get("type").String() != "text" || items[0].Get("text").String() != "Loaded files" {
			t.Fatalf("items[0] = %s, want text 'Loaded files'", items[0].Raw)
		}
		if items[1].Get("type").String() != "image" || items[1].Get("mime_type").String() != "image/png" || items[1].Get("data").String() != "iVBORw0KGgo=" {
			t.Fatalf("items[1] = %s, want image/png item", items[1].Raw)
		}
		if items[2].Get("type").String() != "document" || items[2].Get("mime_type").String() != "application/pdf" || items[2].Get("data").String() != "JVBERi0xLjQK" {
			t.Fatalf("items[2] = %s, want application/pdf document item", items[2].Raw)
		}
	})

	t.Run("fileData audio URI with empty response", func(t *testing.T) {
		inputJSON := []byte(`{
			"model": "gemini-3.5-flash",
			"contents": [
				{"role": "model", "parts": [{"functionCall": {"name": "fetch_audio", "id": "call_2", "args": {}}}]},
				{"role": "user", "parts": [{
					"functionResponse": {
						"name": "fetch_audio",
						"id": "call_2",
						"response": {},
						"parts": [
							{"fileData": {"mimeType": "audio/wav", "fileUri": "https://example.com/a.wav"}}
						]
					}
				}]}
			]
		}`)

		out, err := ConvertGeminiRequestToInteractions("gemini-3.5-flash", inputJSON, false)
		if err != nil {
			t.Fatalf("ConvertGeminiRequestToInteractions() error = %v", err)
		}
		items := ojson.GetBytesNoCopy(out, "input.1.result").Array()
		if len(items) != 1 {
			t.Fatalf("expected 1 audio item (no empty text item), got %d; out=%s", len(items), string(out))
		}
		if items[0].Get("type").String() != "audio" || items[0].Get("mime_type").String() != "audio/wav" || items[0].Get("uri").String() != "https://example.com/a.wav" {
			t.Fatalf("items[0] = %s, want audio uri item", items[0].Raw)
		}
	})

	t.Run("preserves sibling response fields alongside result and nested parts", func(t *testing.T) {
		inputJSON := []byte(`{
			"model": "gemini-3.5-flash",
			"contents": [
				{"role": "model", "parts": [{"functionCall": {"name": "read_files", "id": "call_3", "args": {}}}]},
				{"role": "user", "parts": [{
					"functionResponse": {
						"name": "read_files",
						"id": "call_3",
						"response": {"result": "partial", "error": "second file failed"},
						"parts": [
							{"text": "extra note"},
							{"inline_data": {"mime_type": "image/png", "data": "iVBORw0KGgo="}}
						]
					}
				}]}
			]
		}`)

		out, err := ConvertGeminiRequestToInteractions("gemini-3.5-flash", inputJSON, false)
		if err != nil {
			t.Fatalf("ConvertGeminiRequestToInteractions() error = %v", err)
		}
		items := ojson.GetBytesNoCopy(out, "input.1.result").Array()
		if len(items) != 3 {
			t.Fatalf("expected 3 items, got %d; out=%s", len(items), string(out))
		}
		if items[0].Get("type").String() != "text" || !strings.Contains(items[0].Get("text").String(), "second file failed") {
			t.Fatalf("items[0] = %s, want text preserving error field", items[0].Raw)
		}
		if items[1].Get("type").String() != "text" || items[1].Get("text").String() != "extra note" {
			t.Fatalf("items[1] = %s, want nested text 'extra note'", items[1].Raw)
		}
		if items[2].Get("type").String() != "image" || items[2].Get("mime_type").String() != "image/png" {
			t.Fatalf("items[2] = %s, want image/png item", items[2].Raw)
		}
	})

	t.Run("streaming response delta preserves functionResponse.parts", func(t *testing.T) {
		var param any
		chunk := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionResponse":{"name":"read_files","id":"call_4","response":{"result":"Loaded"},"parts":[{"inlineData":{"mimeType":"image/png","data":"iVBORw0KGgo="}}]}}]}}]}`)
		events := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.5-flash", nil, nil, chunk, &param)
		delta := findStepDeltaPayloadByType(events, "function_result")
		if len(delta) == 0 {
			t.Fatalf("expected function_result step.delta event, got %d events", len(events))
		}
		items := ojson.GetBytesNoCopy(delta, "delta.result").Array()
		if len(items) != 2 || items[0].Get("text").String() != "Loaded" || items[1].Get("type").String() != "image" {
			t.Fatalf("delta.result = %s, want [text, image]", ojson.GetBytesNoCopy(delta, "delta.result").Raw)
		}
	})
}

func TestConvertGeminiRequestToInteractions_NormalizesNumericProtoSchemaTypes_Issue8032(t *testing.T) {
	inputJSON := []byte(`{
		"contents": [{"role": "user", "parts": [{"text": "hello"}]}],
		"tools": [{
			"functionDeclarations": [{
				"name": "search",
				"parameters": {
					"type": 6,
					"properties": {
						"query": {"type": 1},
						"tags": {"type": 5, "items": {"type": 1}}
					}
				}
			}]
		}],
		"generationConfig": {
			"responseSchema": {
				"type": 6,
				"properties": {
					"answer": {"type": 1}
				}
			}
		}
	}`)

	out, err := ConvertGeminiRequestToInteractions("gemini-3.5-flash", inputJSON, false)
	if err != nil {
		t.Fatalf("ConvertGeminiRequestToInteractions returned error: %v", err)
	}

	toolSchema := ojson.GetBytesNoCopy(out, "tools.0.parameters")
	for _, tc := range []struct {
		path string
		want string
	}{
		{"type", "object"},
		{"properties.query.type", "string"},
		{"properties.tags.type", "array"},
		{"properties.tags.items.type", "string"},
	} {
		got := toolSchema.Get(tc.path)
		if got.Type != ojson.String || got.String() != tc.want {
			t.Errorf("tools.0.parameters.%s = %s (type %v), want string %q", tc.path, got.Raw, got.Type, tc.want)
		}
	}

	respSchema := ojson.GetBytesNoCopy(out, "response_format.schema")
	if got := respSchema.Get("type"); got.Type != ojson.String || got.String() != "object" {
		t.Errorf("response_format.schema.type = %s (type %v), want string object", got.Raw, got.Type)
	}
	if got := respSchema.Get("properties.answer.type"); got.Type != ojson.String || got.String() != "string" {
		t.Errorf("response_format.schema.properties.answer.type = %s (type %v), want string", got.Raw, got.Type)
	}
}

func TestConvertInteractionsRequestToGeminiGenerationConfigPreservesArraysEmptyContainersAndEscapedKeys(t *testing.T) {
	input := []byte(`{
		"model": "gemini-3.5-flash",
		"input": "hi",
		"generation_config": {
			"response_schema": {
				"type": "object",
				"properties": {},
				"required": [],
				"any_of": [
					{"type": "string", "min_length": 1},
					{"type": "integer", "minimum": 0}
				]
			},
			"speech_config": {
				"multi_speaker_voice_config": {
					"speaker_voice_configs": [
						{
							"speaker": "Alice",
							"voice_config": {"prebuilt_voice_config": {"voice_name": "Kore"}}
						}
					]
				}
			},
			"nested_matrix": [[1, 2], [3, 4]],
			"special_keys": {
				"user.name": "a",
				"wild*card": "b",
				":leading": "c",
				"": "d",
				"0": "e"
			}
		}
	}`)

	out, errConvert := ConvertInteractionsRequestToGemini("gemini-3.5-flash", input, false)
	if errConvert != nil {
		t.Fatalf("ConvertInteractionsRequestToGemini error: %v", errConvert)
	}

	genCfg := ojson.GetBytesNoCopy(out, "generationConfig")

	anyOf := genCfg.Get("responseSchema.anyOf").Array()
	if len(anyOf) != 2 {
		t.Fatalf("responseSchema.anyOf len = %d, want 2. Output: %s", len(anyOf), genCfg.Raw)
	}
	if anyOf[0].Get("type").String() != "string" || anyOf[0].Get("minLength").Int() != 1 {
		t.Fatalf("responseSchema.anyOf[0] = %s, want both type and minLength", anyOf[0].Raw)
	}
	if minField := anyOf[1].Get("minimum"); anyOf[1].Get("type").String() != "integer" || !minField.Exists() || minField.Raw != "0" {
		t.Fatalf("responseSchema.anyOf[1] = %s, want both type and minimum", anyOf[1].Raw)
	}

	speakers := genCfg.Get("speechConfig.multiSpeakerVoiceConfig.speakerVoiceConfigs").Array()
	if len(speakers) != 1 {
		t.Fatalf("speakerVoiceConfigs len = %d, want 1. Output: %s", len(speakers), genCfg.Raw)
	}
	if speakers[0].Get("speaker").String() != "Alice" || speakers[0].Get("voiceConfig.prebuiltVoiceConfig.voiceName").String() != "Kore" {
		t.Fatalf("speakerVoiceConfigs[0] = %s, want both speaker and voiceConfig", speakers[0].Raw)
	}

	props := genCfg.Get("responseSchema.properties")
	if !props.Exists() || !props.IsObject() || len(props.Map()) != 0 {
		t.Fatalf("responseSchema.properties = %q (exists=%v), want empty object {}", props.Raw, props.Exists())
	}
	req := genCfg.Get("responseSchema.required")
	if !req.Exists() || !req.IsArray() || len(req.Array()) != 0 {
		t.Fatalf("responseSchema.required = %q (exists=%v), want empty array []", req.Raw, req.Exists())
	}

	matrix := genCfg.Get("nestedMatrix").Array()
	if len(matrix) != 2 || len(matrix[0].Array()) != 2 || len(matrix[1].Array()) != 2 {
		t.Fatalf("nestedMatrix = %s, want [[1,2],[3,4]]", genCfg.Get("nestedMatrix").Raw)
	}

	special := genCfg.Get("specialKeys").Map()
	for k, want := range map[string]string{
		"user.name": "a",
		"wild*card": "b",
		":leading":  "c",
		"":          "d",
		"0":         "e",
	} {
		if got, ok := special[k]; !ok || got.String() != want {
			t.Fatalf("specialKeys[%q] = %q (ok=%v), want %q. Raw: %s", k, got.String(), ok, want, genCfg.Get("specialKeys").Raw)
		}
	}
}

func TestConvertGeminiRequestToInteractionsPreservesArraysEmptyContainersAndRootArrays(t *testing.T) {
	input := []byte(`{
		"contents": [{"role": "user", "parts": [{"text": "hi"}]}],
		"generationConfig": {
			"speechConfig": {
				"multiSpeakerVoiceConfig": {
					"speakerVoiceConfigs": [
						{
							"speaker": "Alice",
							"voiceConfig": {"prebuiltVoiceConfig": {"voiceName": "Kore"}}
						}
					]
				}
			},
			"emptyMap": {},
			"emptyList": [],
			"nestedMatrix": [[1, 2], [3, 4]],
			"specialKeys": {
				"user.name": "a",
				"wild*card": "b",
				":leading": "c",
				"": "d",
				"0": "e"
			}
		},
		"tools": [
			{
				"googleSearch": {
					"excludeDomains": ["example.com", "test.org"],
					"customRules": [
						{"matchPattern": "*.pdf", "maxDepth": 2},
						{"matchPattern": "*.txt", "maxDepth": 1}
					],
					"emptyRules": []
				}
			}
		]
	}`)

	out, errConvert := ConvertGeminiRequestToInteractions("gemini-3.5-flash", input, false)
	if errConvert != nil {
		t.Fatalf("ConvertGeminiRequestToInteractions error: %v", errConvert)
	}

	genCfg := ojson.GetBytesNoCopy(out, "generation_config")
	speakers := genCfg.Get("speech_config.multi_speaker_voice_config.speaker_voice_configs").Array()
	if len(speakers) != 1 {
		t.Fatalf("speaker_voice_configs len = %d, want 1. Output: %s", len(speakers), genCfg.Raw)
	}
	if speakers[0].Get("speaker").String() != "Alice" || speakers[0].Get("voice_config.prebuilt_voice_config.voice_name").String() != "Kore" {
		t.Fatalf("speaker_voice_configs[0] = %s, want both speaker and voice_config", speakers[0].Raw)
	}

	emptyMap := genCfg.Get("empty_map")
	if !emptyMap.Exists() || !emptyMap.IsObject() || len(emptyMap.Map()) != 0 {
		t.Fatalf("empty_map = %q (exists=%v), want empty object {}", emptyMap.Raw, emptyMap.Exists())
	}
	emptyList := genCfg.Get("empty_list")
	if !emptyList.Exists() || !emptyList.IsArray() || len(emptyList.Array()) != 0 {
		t.Fatalf("empty_list = %q (exists=%v), want empty array []", emptyList.Raw, emptyList.Exists())
	}

	matrix := genCfg.Get("nested_matrix").Array()
	if len(matrix) != 2 || len(matrix[0].Array()) != 2 || len(matrix[1].Array()) != 2 {
		t.Fatalf("nested_matrix = %s, want [[1,2],[3,4]]", genCfg.Get("nested_matrix").Raw)
	}

	special := genCfg.Get("special_keys").Map()
	for k, want := range map[string]string{
		"user.name": "a",
		"wild*card": "b",
		":leading":  "c",
		"":          "d",
		"0":         "e",
	} {
		if got, ok := special[k]; !ok || got.String() != want {
			t.Fatalf("special_keys[%q] = %q (ok=%v), want %q. Raw: %s", k, got.String(), ok, want, genCfg.Get("special_keys").Raw)
		}
	}

	gs := ojson.GetBytesNoCopy(out, "tools.0")
	exclude := gs.Get("exclude_domains")
	if !exclude.IsArray() || len(exclude.Array()) != 2 || exclude.Array()[0].String() != "example.com" || exclude.Array()[1].String() != "test.org" {
		t.Fatalf("tools.0.exclude_domains = %s, want [\"example.com\",\"test.org\"]", exclude.Raw)
	}
	rules := gs.Get("custom_rules")
	if !rules.IsArray() || len(rules.Array()) != 2 {
		t.Fatalf("tools.0.custom_rules = %s, want 2-element array", rules.Raw)
	}
	if rules.Array()[0].Get("match_pattern").String() != "*.pdf" || rules.Array()[0].Get("max_depth").Int() != 2 {
		t.Fatalf("tools.0.custom_rules[0] = %s, want both match_pattern and max_depth", rules.Array()[0].Raw)
	}
	emptyRules := gs.Get("empty_rules")
	if !emptyRules.Exists() || !emptyRules.IsArray() || len(emptyRules.Array()) != 0 {
		t.Fatalf("tools.0.empty_rules = %q (exists=%v), want empty array []", emptyRules.Raw, emptyRules.Exists())
	}
}

func TestConvertInteractionsResponseToGeminiNonStream_ThoughtSummaryAndSignature(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		wantParts []struct {
			text    string
			thought bool
			sig     string
		}
	}{
		{
			name: "thought summary with signature",
			raw:  `{"id":"int_1","model":"gemini-3-pro","steps":[{"type":"thought","summary":[{"type":"text","text":"reasoning step"}],"signature":"sig_summary"},{"type":"model_output","content":[{"type":"text","text":"final answer"}]}]}`,
			wantParts: []struct {
				text    string
				thought bool
				sig     string
			}{
				{text: "reasoning step", thought: true, sig: "sig_summary"},
				{text: "final answer", thought: false, sig: ""},
			},
		},
		{
			name: "thought text with snake_case thought_signature",
			raw:  `{"id":"int_2","model":"gemini-3-pro","steps":[{"type":"thought","text":"plain thought","thought_signature":"sig_text"}]}`,
			wantParts: []struct {
				text    string
				thought bool
				sig     string
			}{
				{text: "plain thought", thought: true, sig: "sig_text"},
			},
		},
		{
			name: "thought content array attaches thoughtSignature to last part",
			raw:  `{"id":"int_3","model":"gemini-3-pro","steps":[{"type":"thought","content":[{"type":"text","text":"part 1"},{"type":"text","text":"part 2"}],"thoughtSignature":"sig_multi"}]}`,
			wantParts: []struct {
				text    string
				thought bool
				sig     string
			}{
				{text: "part 1", thought: true, sig: ""},
				{text: "part 2", thought: true, sig: "sig_multi"},
			},
		},
		{
			name: "signature-only thought step preserved",
			raw:  `{"id":"int_4","model":"gemini-3-pro","steps":[{"type":"thought","signature":"sig_only"},{"type":"model_output","content":[{"type":"text","text":"done"}]}]}`,
			wantParts: []struct {
				text    string
				thought bool
				sig     string
			}{
				{text: "", thought: true, sig: "sig_only"},
				{text: "done", thought: false, sig: ""},
			},
		},
		{
			name: "empty content array falls back to summary and preserves signature",
			raw:  `{"id":"int_5","model":"gemini-3-pro","steps":[{"type":"thought","content":[],"summary":[{"type":"text","text":"fallback summary"}],"signature":"sig_fallback"}]}`,
			wantParts: []struct {
				text    string
				thought bool
				sig     string
			}{
				{text: "fallback summary", thought: true, sig: "sig_fallback"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := ConvertInteractionsResponseToGeminiNonStream(context.Background(), "gemini-3-pro", nil, nil, []byte(tc.raw), nil)
			parts := ojson.GetBytesNoCopy(out, "candidates.0.content.parts").Array()
			if len(parts) != len(tc.wantParts) {
				t.Fatalf("parts count = %d, want %d. Output: %s", len(parts), len(tc.wantParts), string(out))
			}
			for i, want := range tc.wantParts {
				if got := parts[i].Get("text").String(); got != want.text {
					t.Errorf("parts[%d].text = %q, want %q. Part: %s", i, got, want.text, parts[i].Raw)
				}
				if got := parts[i].Get("thought").Bool(); got != want.thought {
					t.Errorf("parts[%d].thought = %v, want %v. Part: %s", i, got, want.thought, parts[i].Raw)
				}
				if got := parts[i].Get("thoughtSignature").String(); got != want.sig {
					t.Errorf("parts[%d].thoughtSignature = %q, want %q. Part: %s", i, got, want.sig, parts[i].Raw)
				}
			}
		})
	}
}

func TestConvertGeminiRequestToInteractions_ModelHistoryThoughtSignature(t *testing.T) {
	const validGeminiThoughtSig = "ErUDCrIDCAISrQMBEU0yD9ECvDhSY1DQJNUGafArdfd2mDfO8VQq7XjLx/91zESuo0QPSdkRFWkLeVIocSQmQULonYMOJcs6XDLV2LTRC9myb3MCCP9CUoWbEeqhAvXKTScyS3nwBDDVJYuDDbY3YvR4V86T/DnU3qufpaVZ3wQOiJVyBVZ515dYTN+XGq7SuUc3RpfAqVU06jgxaCM0WKV4Df5mGMJWb25e/aFG2Jc7upSqpf3n6aElj+4c/eWr4GdKd0TUIElXBZ0HEN/vNcWzD3F0S4MeVbk1LDakL6HG6oyaSS2gocxYNYxqm9mdMHaXYa4mIYqWqmqBEnbgcHp8H4fgqBxc3Cx8C3otV8IarO5OALaVDA3NaXB1zjLet1587kEpkCNr9OvrYOES2nCl/i4EgbPK01nlXo+Wwm5jsZU5nEG4/Z0bErzqC5TKwOsqpJ7afL2sPWI0IGrXhXL+QCumWCS5iUtwybSkL7CYSk9GC+iY+ev6FAmC4V5JEc4OaWOc9+m/29LniN/iPTSxtUQSZT94pUa3/irIIdH7ReAS3cpeM6OTvumR1PwNxXx3XM1mEGc="

	t.Run("non-empty thought part writes summary and survives sanitization", func(t *testing.T) {
		req := []byte(`{"model":"gemini-3-pro","contents":[{"role":"model","parts":[{"text":"deep thought","thought":true,"thoughtSignature":"` + validGeminiThoughtSig + `"}]}]}`)
		out, err := ConvertGeminiRequestToInteractions("gemini-3-pro", req, false)
		if err != nil {
			t.Fatalf("ConvertGeminiRequestToInteractions error: %v", err)
		}
		sanitized := helps.SanitizeGeminiInteractionsUnsupportedInputIDs(out, false)
		steps := ojson.GetBytesNoCopy(sanitized, "input").Array()
		if len(steps) != 1 {
			t.Fatalf("input len = %d, want 1. Output: %s", len(steps), string(sanitized))
		}
		if got := steps[0].Get("type").String(); got != "thought" {
			t.Fatalf("steps[0].type = %q, want thought", got)
		}
		if got := steps[0].Get("signature").String(); got != validGeminiThoughtSig {
			t.Fatalf("steps[0].signature = %q, want %q. Output: %s", got, validGeminiThoughtSig, string(sanitized))
		}
		if steps[0].Get("content").Exists() {
			t.Fatalf("thought step should use summary rather than content: %s", steps[0].Raw)
		}
		if got := steps[0].Get("summary.0.text").String(); got != "deep thought" {
			t.Fatalf("steps[0].summary.0.text = %q, want deep thought. Step: %s", got, steps[0].Raw)
		}
	})

	t.Run("non-empty model text preserves signature as thought step", func(t *testing.T) {
		req := []byte(`{"model":"gemini-3-pro","contents":[{"role":"model","parts":[{"text":"answer","thoughtSignature":"sig_answer"}]}]}`)
		out, err := ConvertGeminiRequestToInteractions("gemini-3-pro", req, false)
		if err != nil {
			t.Fatalf("ConvertGeminiRequestToInteractions error: %v", err)
		}
		steps := ojson.GetBytesNoCopy(out, "input").Array()
		if len(steps) != 2 {
			t.Fatalf("input len = %d, want 2. Output: %s", len(steps), string(out))
		}
		if steps[0].Get("type").String() != "model_output" || steps[0].Get("content.0.text").String() != "answer" {
			t.Fatalf("steps[0] = %s, want model_output answer", steps[0].Raw)
		}
		if steps[1].Get("type").String() != "thought" || steps[1].Get("signature").String() != "sig_answer" {
			t.Fatalf("steps[1] = %s, want thought with signature sig_answer", steps[1].Raw)
		}
	})

	t.Run("model inlineData preserves signature as thought step", func(t *testing.T) {
		req := []byte(`{"model":"gemini-3-pro","contents":[{"role":"model","parts":[{"inlineData":{"mimeType":"image/png","data":"iVBORw0KGgo="},"thoughtSignature":"sig_img"}]}]}`)
		out, err := ConvertGeminiRequestToInteractions("gemini-3-pro", req, false)
		if err != nil {
			t.Fatalf("ConvertGeminiRequestToInteractions error: %v", err)
		}
		steps := ojson.GetBytesNoCopy(out, "input").Array()
		if len(steps) != 2 {
			t.Fatalf("input len = %d, want 2. Output: %s", len(steps), string(out))
		}
		if steps[0].Get("type").String() != "model_output" || steps[0].Get("content.0.type").String() != "image" {
			t.Fatalf("steps[0] = %s, want model_output image", steps[0].Raw)
		}
		if steps[1].Get("type").String() != "thought" || steps[1].Get("signature").String() != "sig_img" {
			t.Fatalf("steps[1] = %s, want thought with signature sig_img", steps[1].Raw)
		}
	})

	t.Run("model fileData preserves signature as thought step", func(t *testing.T) {
		req := []byte(`{"model":"gemini-3-pro","contents":[{"role":"model","parts":[{"fileData":{"mimeType":"image/png","fileUri":"gs://bucket/img.png"},"thoughtSignature":"sig_file"}]}]}`)
		out, err := ConvertGeminiRequestToInteractions("gemini-3-pro", req, false)
		if err != nil {
			t.Fatalf("ConvertGeminiRequestToInteractions error: %v", err)
		}
		steps := ojson.GetBytesNoCopy(out, "input").Array()
		if len(steps) != 2 {
			t.Fatalf("input len = %d, want 2. Output: %s", len(steps), string(out))
		}
		if steps[0].Get("type").String() != "model_output" || steps[0].Get("content.0.uri").String() != "gs://bucket/img.png" {
			t.Fatalf("steps[0] = %s, want model_output file uri", steps[0].Raw)
		}
		if steps[1].Get("type").String() != "thought" || steps[1].Get("signature").String() != "sig_file" {
			t.Fatalf("steps[1] = %s, want thought with signature sig_file", steps[1].Raw)
		}
	})

	t.Run("standalone signature-only parts without text key preserved", func(t *testing.T) {
		req := []byte(`{"model":"gemini-3-pro","contents":[{"role":"model","parts":[{"thoughtSignature":"sig_bare"},{"thought":true,"thought_signature":"sig_thought_bare"}]}]}`)
		out, err := ConvertGeminiRequestToInteractions("gemini-3-pro", req, false)
		if err != nil {
			t.Fatalf("ConvertGeminiRequestToInteractions error: %v", err)
		}
		steps := ojson.GetBytesNoCopy(out, "input").Array()
		if len(steps) != 2 {
			t.Fatalf("input len = %d, want 2. Output: %s", len(steps), string(out))
		}
		if steps[0].Get("type").String() != "thought" || steps[0].Get("signature").String() != "sig_bare" {
			t.Fatalf("steps[0] = %s, want thought with signature sig_bare", steps[0].Raw)
		}
		if steps[1].Get("type").String() != "thought" || steps[1].Get("signature").String() != "sig_thought_bare" {
			t.Fatalf("steps[1] = %s, want thought with signature sig_thought_bare", steps[1].Raw)
		}
	})

	t.Run("non-stream response to follow-up request round-trips multi-part text summary and signature through sanitization", func(t *testing.T) {
		upstream := []byte(`{"id":"int_rt","model":"gemini-3-pro","steps":[{"type":"thought","summary":[{"type":"text","text":"step one"},{"type":"text","text":"step two"}],"signature":"` + validGeminiThoughtSig + `"},{"type":"model_output","content":[{"type":"text","text":"hello"}]},{"type":"thought","signature":"` + validGeminiThoughtSig + `"}]}`)
		geminiResp := ConvertInteractionsResponseToGeminiNonStream(context.Background(), "gemini-3-pro", nil, nil, upstream, nil)
		modelContent := ojson.GetBytesNoCopy(geminiResp, "candidates.0.content").Raw

		followUp := `{"model":"gemini-3-pro","contents":[{"role":"user","parts":[{"text":"hi"}]},` + modelContent + `,{"role":"user","parts":[{"text":"continue"}]}]}`
		interactionsReq, err := ConvertGeminiRequestToInteractions("gemini-3-pro", []byte(followUp), false)
		if err != nil {
			t.Fatalf("ConvertGeminiRequestToInteractions error: %v", err)
		}
		sanitized := helps.SanitizeGeminiInteractionsUnsupportedInputIDs(interactionsReq, false)
		steps := ojson.GetBytesNoCopy(sanitized, "input").Array()
		if len(steps) != 5 {
			t.Fatalf("input len = %d, want 5. Output: %s", len(steps), string(sanitized))
		}
		if steps[1].Get("type").String() != "thought" || steps[1].Get("signature").String() != validGeminiThoughtSig {
			t.Fatalf("steps[1] = %s, want thought with valid signature", steps[1].Raw)
		}
		if steps[1].Get("summary.#").Int() != 2 || steps[1].Get("summary.0.text").String() != "step one" || steps[1].Get("summary.1.text").String() != "step two" {
			t.Fatalf("steps[1].summary = %s, want [step one, step two]", steps[1].Raw)
		}
		if steps[2].Get("type").String() != "model_output" || steps[2].Get("content.0.text").String() != "hello" {
			t.Fatalf("steps[2] = %s, want model_output hello", steps[2].Raw)
		}
		if steps[3].Get("type").String() != "thought" || steps[3].Get("signature").String() != validGeminiThoughtSig {
			t.Fatalf("steps[3] = %s, want thought with valid signature", steps[3].Raw)
		}
	})

	t.Run("non-stream response to follow-up request round-trips mixed text and image thought summary", func(t *testing.T) {
		upstream := []byte(`{"id":"int_rt_img","model":"gemini-3-pro","steps":[{"type":"thought","summary":[{"type":"text","text":"sketch diagram"},{"type":"image","mime_type":"image/png","data":"iVBORw0KGgo="}],"signature":"` + validGeminiThoughtSig + `"},{"type":"model_output","content":[{"type":"text","text":"final answer"}]}]}`)
		geminiResp := ConvertInteractionsResponseToGeminiNonStream(context.Background(), "gemini-3-pro", nil, nil, upstream, nil)
		respParts := ojson.GetBytesNoCopy(geminiResp, "candidates.0.content.parts").Array()
		if len(respParts) != 3 {
			t.Fatalf("response parts count = %d, want 3. Output: %s", len(respParts), string(geminiResp))
		}
		if !respParts[0].Get("thought").Bool() || respParts[0].Get("text").String() != "sketch diagram" {
			t.Fatalf("respParts[0] = %s, want thought text part", respParts[0].Raw)
		}
		if !respParts[1].Get("thought").Bool() || respParts[1].Get("inlineData.mimeType").String() != "image/png" || respParts[1].Get("thoughtSignature").String() != validGeminiThoughtSig {
			t.Fatalf("respParts[1] = %s, want thought inlineData image part with thoughtSignature", respParts[1].Raw)
		}
		if respParts[2].Get("thought").Bool() || respParts[2].Get("text").String() != "final answer" {
			t.Fatalf("respParts[2] = %s, want non-thought model text part", respParts[2].Raw)
		}

		modelContent := ojson.GetBytesNoCopy(geminiResp, "candidates.0.content").Raw
		followUp := `{"model":"gemini-3-pro","contents":[{"role":"user","parts":[{"text":"hi"}]},` + modelContent + `,{"role":"user","parts":[{"text":"next"}]}]}`
		interactionsReq, err := ConvertGeminiRequestToInteractions("gemini-3-pro", []byte(followUp), false)
		if err != nil {
			t.Fatalf("ConvertGeminiRequestToInteractions error: %v", err)
		}
		sanitized := helps.SanitizeGeminiInteractionsUnsupportedInputIDs(interactionsReq, false)
		steps := ojson.GetBytesNoCopy(sanitized, "input").Array()
		if len(steps) != 4 {
			t.Fatalf("input len = %d, want 4. Output: %s", len(steps), string(sanitized))
		}
		if steps[1].Get("type").String() != "thought" || steps[1].Get("signature").String() != validGeminiThoughtSig {
			t.Fatalf("steps[1] = %s, want thought step with valid signature", steps[1].Raw)
		}
		if steps[1].Get("summary.#").Int() != 2 || steps[1].Get("summary.0.text").String() != "sketch diagram" || steps[1].Get("summary.1.type").String() != "image" || steps[1].Get("summary.1.data").String() != "iVBORw0KGgo=" {
			t.Fatalf("steps[1].summary = %s, want mixed text and image summary", steps[1].Raw)
		}
		if steps[2].Get("type").String() != "model_output" || steps[2].Get("content.0.text").String() != "final answer" {
			t.Fatalf("steps[2] = %s, want model_output final answer", steps[2].Raw)
		}
	})
}

func TestConvertInteractionsResponseToGemini_FunctionCallArgsNormalizedToObject(t *testing.T) {
	tests := []struct {
		name      string
		argsField string
		wantRaw   string
	}{
		{
			name:      "valid JSON object preserved",
			argsField: `,"arguments":{"city":"Tokyo","unit":"c"}`,
			wantRaw:   `{"city":"Tokyo","unit":"c"}`,
		},
		{
			name:      "args alias with JSON-encoded object string unwrapped",
			argsField: `,"args":" {\"city\":\"Tokyo\"} "`,
			wantRaw:   `{"city":"Tokyo"}`,
		},
		{
			name:      "JSON-encoded object string unwrapped",
			argsField: `,"arguments":"{\"city\":\"Tokyo\"}"`,
			wantRaw:   `{"city":"Tokyo"}`,
		},
		{
			name:      "JSON-encoded array string falls back to empty object",
			argsField: `,"arguments":"[1,2,3]"`,
			wantRaw:   `{}`,
		},
		{
			name:      "JSON-encoded scalar string falls back to empty object",
			argsField: `,"arguments":"123"`,
			wantRaw:   `{}`,
		},
		{
			name:      "JSON-encoded null string falls back to empty object",
			argsField: `,"arguments":"null"`,
			wantRaw:   `{}`,
		},
		{
			name:      "JSON-encoded string literal falls back to empty object",
			argsField: `,"arguments":"\"hello\""`,
			wantRaw:   `{}`,
		},
		{
			name:      "empty string falls back to empty object",
			argsField: `,"arguments":""`,
			wantRaw:   `{}`,
		},
		{
			name:      "invalid JSON string falls back to empty object",
			argsField: `,"arguments":"{broken"`,
			wantRaw:   `{}`,
		},
		{
			name:      "null value falls back to empty object",
			argsField: `,"arguments":null`,
			wantRaw:   `{}`,
		},
		{
			name:      "array value falls back to empty object",
			argsField: `,"arguments":[1,2]`,
			wantRaw:   `{}`,
		},
		{
			name:      "number scalar falls back to empty object",
			argsField: `,"arguments":42`,
			wantRaw:   `{}`,
		},
		{
			name:      "boolean scalar falls back to empty object",
			argsField: `,"arguments":true`,
			wantRaw:   `{}`,
		},
		{
			name:      "missing arguments defaults to empty object",
			argsField: "",
			wantRaw:   `{}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := []byte(`{
				"id": "i1",
				"model": "gemini-3.1-flash-lite",
				"steps": [
					{"type": "function_call", "call_id": "call_1", "name": "get_weather"` + tt.argsField + `}
				]
			}`)
			out := ConvertInteractionsResponseToGeminiNonStream(context.Background(), "gemini-3.1-flash-lite", nil, nil, raw, nil)
			args := ojson.GetBytesNoCopy(out, "candidates.0.content.parts.0.functionCall.args")
			if !args.Exists() || !args.IsObject() {
				t.Fatalf("functionCall.args = %s (type=%s), want JSON object. Output: %s", args.Raw, args.Type, string(out))
			}
			if args.Raw != tt.wantRaw {
				t.Fatalf("functionCall.args = %s, want %s. Output: %s", args.Raw, tt.wantRaw, string(out))
			}
		})
	}
}

func TestConvertInteractionsRequestToGemini_FunctionCallArgsNormalizedToObject(t *testing.T) {
	tests := []struct {
		name      string
		argsField string
		wantRaw   string
	}{
		{
			name:      "valid JSON object preserved",
			argsField: `,"arguments":{"city":"Tokyo","unit":"c"}`,
			wantRaw:   `{"city":"Tokyo","unit":"c"}`,
		},
		{
			name:      "JSON-encoded object string unwrapped",
			argsField: `,"arguments":" {\"city\":\"Tokyo\"} "`,
			wantRaw:   `{"city":"Tokyo"}`,
		},
		{
			name:      "JSON-encoded array string falls back to empty object",
			argsField: `,"arguments":"[1,2,3]"`,
			wantRaw:   `{}`,
		},
		{
			name:      "JSON-encoded scalar string falls back to empty object",
			argsField: `,"arguments":"123"`,
			wantRaw:   `{}`,
		},
		{
			name:      "empty string falls back to empty object",
			argsField: `,"arguments":""`,
			wantRaw:   `{}`,
		},
		{
			name:      "invalid JSON string falls back to empty object",
			argsField: `,"arguments":"{broken"`,
			wantRaw:   `{}`,
		},
		{
			name:      "null value falls back to empty object",
			argsField: `,"arguments":null`,
			wantRaw:   `{}`,
		},
		{
			name:      "array value falls back to empty object",
			argsField: `,"arguments":[1,2]`,
			wantRaw:   `{}`,
		},
		{
			name:      "number scalar falls back to empty object",
			argsField: `,"arguments":42`,
			wantRaw:   `{}`,
		},
		{
			name:      "boolean scalar falls back to empty object",
			argsField: `,"arguments":true`,
			wantRaw:   `{}`,
		},
		{
			name:      "missing arguments defaults to empty object",
			argsField: "",
			wantRaw:   `{}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := []byte(`{
				"model": "gemini-3.1-flash-lite",
				"input": [
					{"type": "user_input", "content": [{"type": "text", "text": "check weather"}]},
					{"type": "function_call", "call_id": "call_1", "name": "get_weather"` + tt.argsField + `},
					{"type": "function_result", "call_id": "call_1", "name": "get_weather", "result": {"temp": 22}}
				]
			}`)
			out, errConvert := ConvertInteractionsRequestToGemini("gemini-3.1-flash-lite", input, false)
			if errConvert != nil {
				t.Fatalf("ConvertInteractionsRequestToGemini error: %v", errConvert)
			}
			args := ojson.GetBytesNoCopy(out, "contents.1.parts.0.functionCall.args")
			if !args.Exists() || !args.IsObject() {
				t.Fatalf("functionCall.args = %s (type=%s), want JSON object. Output: %s", args.Raw, args.Type, string(out))
			}
			if args.Raw != tt.wantRaw {
				t.Fatalf("functionCall.args = %s, want %s. Output: %s", args.Raw, tt.wantRaw, string(out))
			}
		})
	}
}

func TestConvertInteractionsRequestToGemini_NativeFunctionCallArgsNormalizedToObject(t *testing.T) {
	tests := []struct {
		name      string
		argsField string
		wantRaw   string
	}{
		{
			name:      "valid object preserved",
			argsField: `,"args":{"q":"go"}`,
			wantRaw:   `{"q":"go"}`,
		},
		{
			name:      "JSON-encoded object string unwrapped",
			argsField: `,"args":"{\"q\":\"go\"}"`,
			wantRaw:   `{"q":"go"}`,
		},
		{
			name:      "array falls back to empty object",
			argsField: `,"args":[]`,
			wantRaw:   `{}`,
		},
		{
			name:      "null falls back to empty object",
			argsField: `,"args":null`,
			wantRaw:   `{}`,
		},
		{
			name:      "scalar falls back to empty object",
			argsField: `,"args":42`,
			wantRaw:   `{}`,
		},
		{
			name:      "invalid JSON string falls back to empty object",
			argsField: `,"args":"{bad"`,
			wantRaw:   `{}`,
		},
		{
			name:      "missing args defaults to empty object",
			argsField: "",
			wantRaw:   `{}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := []byte(`{
				"model": "gemini-3.1-flash-lite",
				"input": [
					{"role": "user", "parts": [{"text": "lookup"}]},
					{"role": "model", "parts": [{"functionCall": {"name": "lookup", "id": "call_1"` + tt.argsField + `}, "thoughtSignature": "sig_keep"}]},
					{"role": "user", "parts": [{"functionResponse": {"name": "lookup", "id": "call_1", "response": {"ok": true}}}]}
				]
			}`)
			out, errConvert := ConvertInteractionsRequestToGemini("gemini-3.1-flash-lite", input, false)
			if errConvert != nil {
				t.Fatalf("ConvertInteractionsRequestToGemini error: %v", errConvert)
			}
			part := ojson.GetBytesNoCopy(out, "contents.1.parts.0")
			args := part.Get("functionCall.args")
			if !args.Exists() || !args.IsObject() {
				t.Fatalf("functionCall.args = %s (type=%s), want JSON object. Output: %s", args.Raw, args.Type, string(out))
			}
			if args.Raw != tt.wantRaw {
				t.Fatalf("functionCall.args = %s, want %s. Output: %s", args.Raw, tt.wantRaw, string(out))
			}
			if got := part.Get("functionCall.name").String(); got != "lookup" {
				t.Fatalf("functionCall.name = %q, want lookup", got)
			}
			if got := part.Get("thoughtSignature").String(); got != "sig_keep" {
				t.Fatalf("thoughtSignature = %q, want sig_keep", got)
			}
		})
	}
}

func TestConvertInteractionsResponseToGemini_StreamCompleteStringArgumentsEmittedOnStop(t *testing.T) {
	t.Run("complete JSON object string in step.start emitted once on step.stop", func(t *testing.T) {
		var param any
		ctx := context.Background()
		startEvent := []byte(`data: {"event_type":"step.start","index":0,"step":{"type":"function_call","call_id":"call_1","name":"lookup","arguments":" {\"q\":1} ","signature":"sig_1"}}`)
		stopEvent := []byte(`data: {"event_type":"step.stop","index":0}`)

		if chunks := ConvertInteractionsResponseToGemini(ctx, "gemini-3.1-flash-lite", nil, nil, startEvent, &param); len(chunks) != 0 {
			t.Fatalf("step.start emitted %d chunks, want 0", len(chunks))
		}
		chunks := ConvertInteractionsResponseToGemini(ctx, "gemini-3.1-flash-lite", nil, nil, stopEvent, &param)
		if len(chunks) != 1 {
			t.Fatalf("step.stop emitted %d chunks, want 1", len(chunks))
		}
		part := ojson.GetBytesNoCopy(chunks[0], "candidates.0.content.parts.0")
		if got := part.Get("functionCall.name").String(); got != "lookup" {
			t.Fatalf("functionCall.name = %q, want lookup", got)
		}
		if got := part.Get("functionCall.id").String(); got != "call_1" {
			t.Fatalf("functionCall.id = %q, want call_1", got)
		}
		if got := part.Get("thoughtSignature").String(); got != "sig_1" {
			t.Fatalf("thoughtSignature = %q, want sig_1", got)
		}
		args := part.Get("functionCall.args")
		if !args.Exists() || !args.IsObject() || args.Raw != `{"q":1}` {
			t.Fatalf("functionCall.args = %s (type=%s), want %s", args.Raw, args.Type, `{"q":1}`)
		}
		if dup := ConvertInteractionsResponseToGemini(ctx, "gemini-3.1-flash-lite", nil, nil, stopEvent, &param); len(dup) != 0 {
			t.Fatalf("duplicate step.stop emitted %d chunks, want 0", len(dup))
		}
	})

	t.Run("incomplete or non-object string in step.start does not emit on step.stop", func(t *testing.T) {
		for _, argsRaw := range []string{`""`, `"{broken"`, `"[1,2]"`} {
			var param any
			ctx := context.Background()
			startEvent := []byte(`data: {"event_type":"step.start","index":0,"step":{"type":"function_call","call_id":"call_1","name":"lookup","arguments":` + argsRaw + `}}`)
			stopEvent := []byte(`data: {"event_type":"step.stop","index":0}`)

			_ = ConvertInteractionsResponseToGemini(ctx, "gemini-3.1-flash-lite", nil, nil, startEvent, &param)
			if chunks := ConvertInteractionsResponseToGemini(ctx, "gemini-3.1-flash-lite", nil, nil, stopEvent, &param); len(chunks) != 0 {
				t.Fatalf("args=%s: step.stop emitted %d chunks, want 0; payload=%s", argsRaw, len(chunks), string(chunks[0]))
			}
		}
	})
}

func TestConvertInteractionsResponseToGemini_FinishReasonMapping(t *testing.T) {
	cases := []struct {
		name         string
		status       string
		finishReason string
		want         string
	}{
		{name: "max_tokens", status: "completed", finishReason: "max_tokens", want: "MAX_TOKENS"},
		{name: "length", status: "completed", finishReason: "length", want: "MAX_TOKENS"},
		{name: "max_output_tokens", status: "incomplete", finishReason: "max_output_tokens", want: "MAX_TOKENS"},
		{name: "incomplete_status_only", status: "incomplete", finishReason: "", want: "MAX_TOKENS"},
		{name: "safety", status: "completed", finishReason: "safety", want: "SAFETY"},
		{name: "content_filter", status: "completed", finishReason: "content_filter", want: "SAFETY"},
		{name: "incomplete_with_content_filter", status: "incomplete", finishReason: "content_filter", want: "SAFETY"},
		{name: "recitation", status: "completed", finishReason: "recitation", want: "RECITATION"},
		{name: "uppercase_trimmed", status: "completed", finishReason: " MAX_TOKENS ", want: "MAX_TOKENS"},
		{name: "completed_default", status: "completed", finishReason: "", want: "STOP"},
		{name: "requires_action_default", status: "requires_action", finishReason: "", want: "STOP"},
	}

	buildFields := func(status, finishReason string) string {
		var b strings.Builder
		if status != "" {
			b.WriteString(`,"status":"`)
			b.WriteString(status)
			b.WriteString(`"`)
		}
		if finishReason != "" {
			b.WriteString(`,"finish_reason":"`)
			b.WriteString(finishReason)
			b.WriteString(`"`)
		}
		return b.String()
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			fields := buildFields(tc.status, tc.finishReason)

			// Non-stream flat payload
			flatRaw := []byte(`{"id":"i1","model":"gemini-3.5-flash"` + fields + `,"steps":[{"type":"model_output","content":[{"text":"partial"}]}]}`)
			flatOut := ConvertInteractionsResponseToGeminiNonStream(ctx, "gemini-3.5-flash", nil, nil, flatRaw, nil)
			if got := ojson.GetBytesNoCopy(flatOut, "candidates.0.finishReason").String(); got != tc.want {
				t.Fatalf("non-stream flat finishReason = %q, want %q; payload=%s", got, tc.want, string(flatOut))
			}

			// Non-stream nested interaction payload
			nestedRaw := []byte(`{"interaction":{"id":"i1","model":"gemini-3.5-flash"` + fields + `,"steps":[{"type":"model_output","content":[{"text":"partial"}]}]}}`)
			nestedOut := ConvertInteractionsResponseToGeminiNonStream(ctx, "gemini-3.5-flash", nil, nil, nestedRaw, nil)
			if got := ojson.GetBytesNoCopy(nestedOut, "candidates.0.finishReason").String(); got != tc.want {
				t.Fatalf("non-stream nested finishReason = %q, want %q; payload=%s", got, tc.want, string(nestedOut))
			}

			// Stream interaction.completed event
			var streamParam any
			streamEvent := []byte(`data: {"event_type":"interaction.completed","interaction":{"id":"i1","model":"gemini-3.5-flash"` + fields + `}}`)
			streamOut := ConvertInteractionsResponseToGemini(ctx, "gemini-3.5-flash", nil, nil, streamEvent, &streamParam)
			if len(streamOut) != 1 {
				t.Fatalf("stream interaction.completed chunk count = %d, want 1", len(streamOut))
			}
			if got := ojson.GetBytesNoCopy(streamOut[0], "candidates.0.finishReason").String(); got != tc.want {
				t.Fatalf("stream interaction.completed finishReason = %q, want %q; payload=%s", got, tc.want, string(streamOut[0]))
			}

			// Stream finish event with top-level status/finish_reason
			var finishParam any
			finishEvent := []byte(`data: {"event_type":"finish"` + fields + `}`)
			finishOut := ConvertInteractionsResponseToGemini(ctx, "gemini-3.5-flash", nil, nil, finishEvent, &finishParam)
			if len(finishOut) != 1 {
				t.Fatalf("stream finish chunk count = %d, want 1", len(finishOut))
			}
			if got := ojson.GetBytesNoCopy(finishOut[0], "candidates.0.finishReason").String(); got != tc.want {
				t.Fatalf("stream finish finishReason = %q, want %q; payload=%s", got, tc.want, string(finishOut[0]))
			}
		})
	}
}

func TestConvertInteractionsResponseToGemini_SynthesizedTotalTokenCountIncludesThoughtTokens(t *testing.T) {
	cases := []struct {
		name         string
		usageJSON    string
		wantPrompt   int64
		wantCand     int64
		wantThoughts int64
		wantTotal    int64
	}{
		{
			name:         "total_thought_tokens_included_when_total_tokens_absent",
			usageJSON:    `{"total_input_tokens":10,"total_output_tokens":20,"total_thought_tokens":30}`,
			wantPrompt:   10,
			wantCand:     20,
			wantThoughts: 30,
			wantTotal:    60,
		},
		{
			name:         "reasoning_tokens_included_when_total_tokens_absent",
			usageJSON:    `{"input_tokens":5,"output_tokens":7,"reasoning_tokens":13}`,
			wantPrompt:   5,
			wantCand:     7,
			wantThoughts: 13,
			wantTotal:    25,
		},
		{
			name:         "thought_tokens_only_synthesizes_total",
			usageJSON:    `{"total_thought_tokens":15}`,
			wantPrompt:   0,
			wantCand:     0,
			wantThoughts: 15,
			wantTotal:    15,
		},
		{
			name:         "explicit_total_tokens_preserved",
			usageJSON:    `{"total_input_tokens":10,"total_output_tokens":20,"total_thought_tokens":30,"total_tokens":99}`,
			wantPrompt:   10,
			wantCand:     20,
			wantThoughts: 30,
			wantTotal:    99,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()

			// Non-stream
			nonStreamRaw := []byte(`{"id":"i1","model":"gemini-3.5-flash","steps":[{"type":"model_output","content":[{"text":"ok"}]}],"usage":` + tc.usageJSON + `}`)
			nonStreamOut := ConvertInteractionsResponseToGeminiNonStream(ctx, "gemini-3.5-flash", nil, nil, nonStreamRaw, nil)
			if got := ojson.GetBytesNoCopy(nonStreamOut, "usageMetadata.thoughtsTokenCount").Int(); got != tc.wantThoughts {
				t.Fatalf("non-stream thoughtsTokenCount = %d, want %d; payload=%s", got, tc.wantThoughts, string(nonStreamOut))
			}
			if got := ojson.GetBytesNoCopy(nonStreamOut, "usageMetadata.totalTokenCount").Int(); got != tc.wantTotal {
				t.Fatalf("non-stream totalTokenCount = %d, want %d; payload=%s", got, tc.wantTotal, string(nonStreamOut))
			}
			if tc.wantPrompt > 0 {
				if got := ojson.GetBytesNoCopy(nonStreamOut, "usageMetadata.promptTokenCount").Int(); got != tc.wantPrompt {
					t.Fatalf("non-stream promptTokenCount = %d, want %d; payload=%s", got, tc.wantPrompt, string(nonStreamOut))
				}
			}
			if tc.wantCand > 0 {
				if got := ojson.GetBytesNoCopy(nonStreamOut, "usageMetadata.candidatesTokenCount").Int(); got != tc.wantCand {
					t.Fatalf("non-stream candidatesTokenCount = %d, want %d; payload=%s", got, tc.wantCand, string(nonStreamOut))
				}
			}

			// Stream interaction.completed
			var param any
			streamEvent := []byte(`data: {"event_type":"interaction.completed","interaction":{"id":"i1","model":"gemini-3.5-flash","status":"completed","usage":` + tc.usageJSON + `}}`)
			streamOut := ConvertInteractionsResponseToGemini(ctx, "gemini-3.5-flash", nil, nil, streamEvent, &param)
			if len(streamOut) != 1 {
				t.Fatalf("stream chunk count = %d, want 1", len(streamOut))
			}
			if got := ojson.GetBytesNoCopy(streamOut[0], "usageMetadata.thoughtsTokenCount").Int(); got != tc.wantThoughts {
				t.Fatalf("stream thoughtsTokenCount = %d, want %d; payload=%s", got, tc.wantThoughts, string(streamOut[0]))
			}
			if got := ojson.GetBytesNoCopy(streamOut[0], "usageMetadata.totalTokenCount").Int(); got != tc.wantTotal {
				t.Fatalf("stream totalTokenCount = %d, want %d; payload=%s", got, tc.wantTotal, string(streamOut[0]))
			}
		})
	}
}
