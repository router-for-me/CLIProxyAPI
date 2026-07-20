package chat_completions

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const applyPatchInput = "*** Begin Patch\n*** Add File: cursor-round-trip.txt\n+ok\n*** End Patch"

type streamedChatToolCall struct {
	ID            string
	Name          string
	Arguments     string
	Announcements int
}

func translateCodexStreamEvents(t *testing.T, originalRequest []byte, events ...string) [][]byte {
	t.Helper()

	var param any
	var chunks [][]byte
	for _, event := range events {
		translated := ConvertCodexResponseToOpenAI(
			context.Background(),
			"gpt-5.6-sol",
			originalRequest,
			nil,
			[]byte("data: "+event),
			&param,
		)
		chunks = append(chunks, translated...)
	}
	return chunks
}

func collectStreamedChatToolCalls(t *testing.T, chunks [][]byte) ([]streamedChatToolCall, string) {
	t.Helper()

	calls := make(map[int]*streamedChatToolCall)
	maxIndex := -1
	finishReason := ""
	for _, chunk := range chunks {
		root := gjson.ParseBytes(chunk)
		for _, toolCall := range root.Get("choices.0.delta.tool_calls").Array() {
			index := int(toolCall.Get("index").Int())
			call := calls[index]
			if call == nil {
				call = &streamedChatToolCall{}
				calls[index] = call
			}
			if index > maxIndex {
				maxIndex = index
			}
			if id := toolCall.Get("id"); id.Exists() && id.String() != "" {
				call.ID = id.String()
				call.Announcements++
			}
			if name := toolCall.Get("function.name"); name.Exists() && name.String() != "" {
				call.Name = name.String()
			}
			if arguments := toolCall.Get("function.arguments"); arguments.Exists() {
				call.Arguments += arguments.String()
			}
		}
		if reason := root.Get("choices.0.finish_reason"); reason.Exists() && reason.String() != "" {
			finishReason = reason.String()
		}
	}

	result := make([]streamedChatToolCall, 0, maxIndex+1)
	for index := 0; index <= maxIndex; index++ {
		call := calls[index]
		if call == nil {
			t.Fatalf("missing streamed tool call index %d", index)
		}
		result = append(result, *call)
	}
	return result, finishReason
}

func customToolRequest(name string) []byte {
	request := []byte(`{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"Apply the patch."}],"tools":[{"type":"custom","name":"","description":"Apply a freeform patch.","format":{"type":"text"}}]}`)
	request, _ = sjson.SetBytes(request, "tools.0.name", name)
	return request
}

func TestApplyPatchCustomToolRoundTrip(t *testing.T) {
	originalRequest := customToolRequest("ApplyPatch")
	chunks := translateCodexStreamEvents(t, originalRequest,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"ctc_1","type":"custom_tool_call","call_id":"call_apply_patch","name":"ApplyPatch","input":"","status":"in_progress"}}`,
		`{"type":"response.custom_tool_call_input.delta","output_index":0,"item_id":"ctc_1","delta":"*** Begin Patch\n*** Add File: cursor-round-trip.txt\n"}`,
		`{"type":"response.custom_tool_call_input.delta","output_index":0,"item_id":"ctc_1","delta":"+ok\n*** End Patch"}`,
		`{"type":"response.custom_tool_call_input.done","output_index":0,"item_id":"ctc_1","input":"*** Begin Patch\n*** Add File: cursor-round-trip.txt\n+ok\n*** End Patch"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"ctc_1","type":"custom_tool_call","call_id":"call_apply_patch","name":"ApplyPatch","input":"*** Begin Patch\n*** Add File: cursor-round-trip.txt\n+ok\n*** End Patch","status":"completed"}}`,
		`{"type":"response.completed","response":{"id":"resp_tool","status":"completed","model":"gpt-5.6-sol","output":[{"id":"ctc_1","type":"custom_tool_call","call_id":"call_apply_patch","name":"ApplyPatch","input":"*** Begin Patch\n*** Add File: cursor-round-trip.txt\n+ok\n*** End Patch","status":"completed"}]}}`,
	)

	calls, finishReason := collectStreamedChatToolCalls(t, chunks)
	if len(calls) != 1 {
		t.Fatalf("expected one Chat tool call, got %d; chunks=%q", len(calls), chunks)
	}
	call := calls[0]
	if call.ID != "call_apply_patch" || call.Name != "ApplyPatch" {
		t.Fatalf("custom call metadata was not preserved: %+v", call)
	}
	if call.Arguments != applyPatchInput {
		t.Fatalf("custom input = %q, want %q", call.Arguments, applyPatchInput)
	}
	if call.Announcements != 1 {
		t.Fatalf("custom call announced %d times, want once", call.Announcements)
	}
	if finishReason != "tool_calls" {
		t.Fatalf("finish_reason = %q, want tool_calls", finishReason)
	}

	followUp := []byte(`{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"Apply the patch."},{"role":"assistant","content":null,"tool_calls":[{"id":"","type":"function","function":{"name":"","arguments":""}}]},{"role":"tool","tool_call_id":"","content":"Done!"}],"tools":[{"type":"custom","name":"ApplyPatch","description":"Apply a freeform patch.","format":{"type":"text"}}]}`)
	followUp, _ = sjson.SetBytes(followUp, "messages.1.tool_calls.0.id", call.ID)
	followUp, _ = sjson.SetBytes(followUp, "messages.1.tool_calls.0.function.name", call.Name)
	followUp, _ = sjson.SetBytes(followUp, "messages.1.tool_calls.0.function.arguments", call.Arguments)
	followUp, _ = sjson.SetBytes(followUp, "messages.2.tool_call_id", call.ID)
	followUp, _ = sjson.SetBytes(followUp, "service_tier", "fast")

	upstream := ConvertOpenAIRequestToCodex("gpt-5.6-sol", followUp, true)
	if got := gjson.GetBytes(upstream, "service_tier").String(); got != "priority" {
		t.Fatalf("service_tier = %q, want priority; output=%s", got, upstream)
	}
	items := gjson.GetBytes(upstream, "input").Array()
	if len(items) != 3 {
		t.Fatalf("expected user, custom call, and custom output; got %d: %s", len(items), gjson.GetBytes(upstream, "input").Raw)
	}
	if got := items[1].Get("type").String(); got != "custom_tool_call" {
		t.Fatalf("follow-up call type = %q, want custom_tool_call; item=%s", got, items[1].Raw)
	}
	if got := items[1].Get("input").String(); got != applyPatchInput {
		t.Fatalf("follow-up custom input = %q, want %q", got, applyPatchInput)
	}
	if got := items[2].Get("type").String(); got != "custom_tool_call_output" {
		t.Fatalf("follow-up output type = %q, want custom_tool_call_output; item=%s", got, items[2].Raw)
	}
	if got := items[2].Get("call_id").String(); got != "call_apply_patch" {
		t.Fatalf("follow-up output call_id = %q, want call_apply_patch", got)
	}

	finalChunks := translateCodexStreamEvents(t, followUp,
		`{"type":"response.output_text.delta","output_index":0,"item_id":"msg_1","delta":"Patch applied and verified."}`,
		`{"type":"response.completed","response":{"id":"resp_final","status":"completed","model":"gpt-5.6-sol","output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"Patch applied and verified."}]}]}}`,
	)
	var finalText string
	var finalReason string
	for _, chunk := range finalChunks {
		finalText += gjson.GetBytes(chunk, "choices.0.delta.content").String()
		if reason := gjson.GetBytes(chunk, "choices.0.finish_reason"); reason.Exists() {
			finalReason = reason.String()
		}
	}
	if finalText != "Patch applied and verified." || finalReason != "stop" {
		t.Fatalf("final continuation text=%q reason=%q", finalText, finalReason)
	}
}

func TestCustomToolStreamingFallbacksEmitInputExactlyOnce(t *testing.T) {
	originalRequest := customToolRequest("ApplyPatch")
	added := `{"type":"response.output_item.added","output_index":0,"item":{"id":"ctc_1","type":"custom_tool_call","call_id":"call_1","name":"ApplyPatch","input":"","status":"in_progress"}}`
	deltaABC := `{"type":"response.custom_tool_call_input.delta","output_index":0,"item_id":"ctc_1","delta":"abc"}`
	deltaDEF := `{"type":"response.custom_tool_call_input.delta","output_index":0,"item_id":"ctc_1","delta":"def"}`
	done := `{"type":"response.custom_tool_call_input.done","output_index":0,"item_id":"ctc_1","input":"abcdef"}`
	itemDone := `{"type":"response.output_item.done","output_index":0,"item":{"id":"ctc_1","type":"custom_tool_call","call_id":"call_1","name":"ApplyPatch","input":"abcdef","status":"completed"}}`
	completed := `{"type":"response.completed","response":{"id":"resp_1","status":"completed","model":"gpt-5.6-sol","output":[{"id":"ctc_1","type":"custom_tool_call","call_id":"call_1","name":"ApplyPatch","input":"abcdef","status":"completed"}]}}`

	tests := []struct {
		name   string
		events []string
	}{
		{name: "multiple deltas then done", events: []string{added, deltaABC, deltaDEF, done, itemDone, completed}},
		{name: "done fallback without deltas", events: []string{added, done, itemDone, completed}},
		{name: "output item done fallback", events: []string{added, deltaABC, itemDone, completed}},
		{name: "missing added buffers until item done", events: []string{deltaABC, deltaDEF, done, itemDone, completed}},
		{name: "completed only fallback", events: []string{completed}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			chunks := translateCodexStreamEvents(t, originalRequest, test.events...)
			calls, finishReason := collectStreamedChatToolCalls(t, chunks)
			if len(calls) != 1 {
				t.Fatalf("expected one call, got %d; chunks=%q", len(calls), chunks)
			}
			call := calls[0]
			if call.ID != "call_1" || call.Name != "ApplyPatch" {
				t.Fatalf("call metadata = %+v", call)
			}
			if call.Arguments != "abcdef" {
				t.Fatalf("streamed input = %q, want exactly %q", call.Arguments, "abcdef")
			}
			if call.Announcements != 1 {
				t.Fatalf("call announced %d times, want once", call.Announcements)
			}
			if finishReason != "tool_calls" {
				t.Fatalf("finish_reason = %q, want tool_calls", finishReason)
			}
		})
	}
}

func TestCustomToolStreamingSupportsSequentialCalls(t *testing.T) {
	originalRequest := customToolRequest("ApplyPatch")
	chunks := translateCodexStreamEvents(t, originalRequest,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"ctc_1","type":"custom_tool_call","call_id":"call_1","name":"ApplyPatch","input":""}}`,
		`{"type":"response.custom_tool_call_input.done","output_index":0,"item_id":"ctc_1","input":"first"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"ctc_1","type":"custom_tool_call","call_id":"call_1","name":"ApplyPatch","input":"first"}}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"id":"ctc_2","type":"custom_tool_call","call_id":"call_2","name":"ApplyPatch","input":""}}`,
		`{"type":"response.custom_tool_call_input.done","output_index":1,"item_id":"ctc_2","input":"second"}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"id":"ctc_2","type":"custom_tool_call","call_id":"call_2","name":"ApplyPatch","input":"second"}}`,
		`{"type":"response.completed","response":{"status":"completed","output":[{"id":"ctc_1","type":"custom_tool_call","call_id":"call_1","name":"ApplyPatch","input":"first"},{"id":"ctc_2","type":"custom_tool_call","call_id":"call_2","name":"ApplyPatch","input":"second"}]}}`,
	)
	calls, finishReason := collectStreamedChatToolCalls(t, chunks)
	if len(calls) != 2 {
		t.Fatalf("expected two sequential calls, got %d; chunks=%q", len(calls), chunks)
	}
	if calls[0].ID != "call_1" || calls[0].Arguments != "first" || calls[1].ID != "call_2" || calls[1].Arguments != "second" {
		t.Fatalf("unexpected sequential calls: %+v", calls)
	}
	if finishReason != "tool_calls" {
		t.Fatalf("finish_reason = %q, want tool_calls", finishReason)
	}
}

func TestMixedParallelCustomAndFunctionCallsKeepIndependentInputs(t *testing.T) {
	originalRequest := []byte(`{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"Run both."}],"tools":[{"type":"custom","name":"ApplyPatch","format":{"type":"text"}},{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]}`)
	chunks := translateCodexStreamEvents(t, originalRequest,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"ctc_1","type":"custom_tool_call","call_id":"call_custom","name":"ApplyPatch","input":""}}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"id":"fc_1","type":"function_call","call_id":"call_function","name":"lookup","arguments":""}}`,
		`{"type":"response.custom_tool_call_input.delta","output_index":0,"item_id":"ctc_1","delta":"patch"}`,
		`{"type":"response.function_call_arguments.delta","output_index":1,"item_id":"fc_1","delta":"{\"q\":"}`,
		`{"type":"response.custom_tool_call_input.done","output_index":0,"item_id":"ctc_1","input":"patch"}`,
		`{"type":"response.function_call_arguments.delta","output_index":1,"item_id":"fc_1","delta":"1}"}`,
		`{"type":"response.function_call_arguments.done","output_index":1,"item_id":"fc_1","arguments":"{\"q\":1}"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"ctc_1","type":"custom_tool_call","call_id":"call_custom","name":"ApplyPatch","input":"patch"}}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"id":"fc_1","type":"function_call","call_id":"call_function","name":"lookup","arguments":"{\"q\":1}"}}`,
		`{"type":"response.completed","response":{"status":"completed","output":[{"id":"ctc_1","type":"custom_tool_call","call_id":"call_custom","name":"ApplyPatch","input":"patch"},{"id":"fc_1","type":"function_call","call_id":"call_function","name":"lookup","arguments":"{\"q\":1}"}]}}`,
	)
	calls, finishReason := collectStreamedChatToolCalls(t, chunks)
	if len(calls) != 2 {
		t.Fatalf("expected two parallel calls, got %d; chunks=%q", len(calls), chunks)
	}
	if calls[0].ID != "call_custom" || calls[0].Name != "ApplyPatch" || calls[0].Arguments != "patch" {
		t.Fatalf("custom parallel call corrupted: %+v", calls[0])
	}
	if calls[1].ID != "call_function" || calls[1].Name != "lookup" || calls[1].Arguments != `{"q":1}` {
		t.Fatalf("function parallel call corrupted: %+v", calls[1])
	}
	if finishReason != "tool_calls" {
		t.Fatalf("finish_reason = %q, want tool_calls", finishReason)
	}
}

func TestConvertCodexResponseToOpenAINonStreamSupportsCustomToolCall(t *testing.T) {
	originalRequest := customToolRequest("ApplyPatch")
	raw := []byte(`{"type":"response.completed","response":{"id":"resp_1","created_at":1700000000,"model":"gpt-5.6-sol","status":"completed","output":[{"id":"ctc_1","type":"custom_tool_call","call_id":"call_1","name":"ApplyPatch","input":"raw patch"}]}}`)
	out := ConvertCodexResponseToOpenAINonStream(context.Background(), "gpt-5.6-sol", originalRequest, nil, raw, nil)

	toolCall := gjson.GetBytes(out, "choices.0.message.tool_calls.0")
	if toolCall.Get("id").String() != "call_1" || toolCall.Get("function.name").String() != "ApplyPatch" || toolCall.Get("function.arguments").String() != "raw patch" {
		t.Fatalf("non-stream custom tool call was not preserved: %s", out)
	}
	if got := gjson.GetBytes(out, "choices.0.finish_reason").String(); got != "tool_calls" {
		t.Fatalf("finish_reason = %q, want tool_calls; payload=%s", got, out)
	}
}

func TestCustomToolNameShorteningRestoresResponseAndFollowUp(t *testing.T) {
	longName := "ApplyPatch_" + strings.Repeat("namespace_", 8)
	if len(longName) <= 64 {
		t.Fatalf("test name length = %d, want greater than 64", len(longName))
	}
	shortName := shortenNameIfNeeded(longName)
	originalRequest := customToolRequest(longName)
	chunks := translateCodexStreamEvents(t, originalRequest,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"ctc_1","type":"custom_tool_call","call_id":"call_1","name":"`+shortName+`","input":"patch"}}`,
		`{"type":"response.completed","response":{"status":"completed","output":[{"id":"ctc_1","type":"custom_tool_call","call_id":"call_1","name":"`+shortName+`","input":"patch"}]}}`,
	)
	calls, _ := collectStreamedChatToolCalls(t, chunks)
	if len(calls) != 1 || calls[0].Name != longName {
		t.Fatalf("shortened response name was not restored: %+v", calls)
	}

	followUp := []byte(`{"messages":[{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"","arguments":"patch"}}]},{"role":"tool","tool_call_id":"call_1","content":"done"}],"tools":[{"type":"custom","name":"","format":{"type":"text"}}]}`)
	followUp, _ = sjson.SetBytes(followUp, "messages.0.tool_calls.0.function.name", longName)
	followUp, _ = sjson.SetBytes(followUp, "tools.0.name", longName)
	upstream := ConvertOpenAIRequestToCodex("gpt-5.6-sol", followUp, true)
	if got := gjson.GetBytes(upstream, "tools.0.name").String(); got != shortName {
		t.Fatalf("custom declaration name = %q, want shortened %q", got, shortName)
	}
	if got := gjson.GetBytes(upstream, "input.0.type").String(); got != "custom_tool_call" {
		t.Fatalf("history call type = %q, want custom_tool_call; input=%s", got, gjson.GetBytes(upstream, "input").Raw)
	}
	if got := gjson.GetBytes(upstream, "input.0.name").String(); got != shortName {
		t.Fatalf("history custom name = %q, want %q", got, shortName)
	}
	if got := gjson.GetBytes(upstream, "input.1.type").String(); got != "custom_tool_call_output" {
		t.Fatalf("history output type = %q, want custom_tool_call_output", got)
	}
}

func TestStandardFunctionEnvelopeUsesCurrentCustomDeclaration(t *testing.T) {
	input := []byte(`{"messages":[{"role":"assistant","tool_calls":[{"id":"call_custom","type":"function","function":{"name":"ApplyPatch","arguments":"raw patch"}}]},{"role":"tool","tool_call_id":"call_custom","content":"done"}],"tools":[{"type":"custom","name":"ApplyPatch","format":{"type":"text"}}]}`)
	out := ConvertOpenAIRequestToCodex("gpt-5.6-sol", input, true)
	items := gjson.GetBytes(out, "input").Array()
	if len(items) != 2 {
		t.Fatalf("expected custom call and output, got %d: %s", len(items), gjson.GetBytes(out, "input").Raw)
	}
	if got := items[0].Get("type").String(); got != "custom_tool_call" {
		t.Fatalf("call type = %q, want custom_tool_call; item=%s", got, items[0].Raw)
	}
	if got := items[0].Get("input").String(); got != "raw patch" {
		t.Fatalf("custom input = %q, want raw patch", got)
	}
	if got := items[1].Get("type").String(); got != "custom_tool_call_output" {
		t.Fatalf("output type = %q, want custom_tool_call_output; item=%s", got, items[1].Raw)
	}
}

func TestFunctionEnvelopeRemainsFunctionWhenCustomNameIsAmbiguous(t *testing.T) {
	input := []byte(`{"messages":[{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"shared","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"done"}],"tools":[{"type":"custom","name":"shared","format":{"type":"text"}},{"type":"function","function":{"name":"shared","parameters":{"type":"object"}}}]}`)
	out := ConvertOpenAIRequestToCodex("gpt-5.6-sol", input, true)
	items := gjson.GetBytes(out, "input").Array()
	if len(items) != 2 || items[0].Get("type").String() != "function_call" || items[1].Get("type").String() != "function_call_output" {
		t.Fatalf("ambiguous name was guessed as custom: %s", gjson.GetBytes(out, "input").Raw)
	}
}

func TestMissingCustomCallIDSynthesizesUniquePair(t *testing.T) {
	input := []byte(`{"messages":[{"role":"assistant","tool_calls":[{"type":"function","function":{"name":"ApplyPatch","arguments":"patch"}}]},{"role":"tool","content":"done"}],"tools":[{"type":"custom","name":"ApplyPatch","format":{"type":"text"}}]}`)
	out := ConvertOpenAIRequestToCodex("gpt-5.6-sol", input, true)
	items := gjson.GetBytes(out, "input").Array()
	if len(items) != 2 || items[0].Get("type").String() != "custom_tool_call" || items[1].Get("type").String() != "custom_tool_call_output" {
		t.Fatalf("missing-ID custom pair was not preserved: %s", gjson.GetBytes(out, "input").Raw)
	}
	callID := items[0].Get("call_id").String()
	if callID == "" || items[1].Get("call_id").String() != callID {
		t.Fatalf("synthesized call IDs do not match: %s", gjson.GetBytes(out, "input").Raw)
	}
}

func TestExistingFunctionCallRoundTripRemainsFunction(t *testing.T) {
	input := []byte(`{"messages":[{"role":"assistant","tool_calls":[{"id":"call_lookup","type":"function","function":{"name":"lookup","arguments":"{\"q\":1}"}}]},{"role":"tool","tool_call_id":"call_lookup","content":"found"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]}`)
	out := ConvertOpenAIRequestToCodex("gpt-5.6-sol", input, true)
	items := gjson.GetBytes(out, "input").Array()
	if len(items) != 2 || items[0].Get("type").String() != "function_call" || items[1].Get("type").String() != "function_call_output" {
		t.Fatalf("function call family regressed: %s", gjson.GetBytes(out, "input").Raw)
	}
	if items[0].Get("arguments").String() != `{"q":1}` || items[1].Get("call_id").String() != "call_lookup" {
		t.Fatalf("function call data regressed: %s", gjson.GetBytes(out, "input").Raw)
	}
}

func TestCustomDeclarationAndToolChoicePreserveFieldsAndShortName(t *testing.T) {
	longName := "ApplyPatch_" + strings.Repeat("namespace_", 8)
	shortName := shortenNameIfNeeded(longName)
	input := []byte(`{"tools":[{"type":"custom","name":"","description":"Apply a freeform patch.","format":{"type":"text"}}],"tool_choice":{"type":"custom","name":"","vendor_extension":"keep"}}`)
	input, _ = sjson.SetBytes(input, "tools.0.name", longName)
	input, _ = sjson.SetBytes(input, "tool_choice.name", longName)

	out := ConvertOpenAIRequestToCodex("gpt-5.6-sol", input, true)
	tool := gjson.GetBytes(out, "tools.0")
	if tool.Get("type").String() != "custom" || tool.Get("name").String() != shortName {
		t.Fatalf("custom declaration type/name regressed: %s", tool.Raw)
	}
	if tool.Get("description").String() != "Apply a freeform patch." || tool.Get("format.type").String() != "text" {
		t.Fatalf("custom declaration fields were not preserved: %s", tool.Raw)
	}

	choice := gjson.GetBytes(out, "tool_choice")
	if choice.Get("type").String() != "custom" || choice.Get("name").String() != shortName {
		t.Fatalf("custom tool choice type/name regressed: %s", choice.Raw)
	}
	if choice.Get("vendor_extension").String() != "keep" {
		t.Fatalf("custom tool choice fields were not preserved: %s", choice.Raw)
	}
}
