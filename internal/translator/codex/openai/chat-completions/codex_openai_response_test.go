package chat_completions

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertCodexResponseToOpenAI_IncompleteTerminal(t *testing.T) {
	ctx := context.Background()
	terminal := []byte(`{"type":"response.incomplete","response":{"id":"resp_1","model":"gpt-5.5","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`)

	var param any
	streamOut := ConvertCodexResponseToOpenAI(ctx, "gpt-5.5", nil, nil, append([]byte("data: "), terminal...), &param)
	if len(streamOut) != 1 {
		t.Fatalf("expected 1 streaming terminal chunk, got %d", len(streamOut))
	}
	if got := gjson.GetBytes(streamOut[0], "choices.0.finish_reason").String(); got != "length" {
		t.Fatalf("stream finish_reason = %q, want length; payload=%s", got, streamOut[0])
	}
	if got := gjson.GetBytes(streamOut[0], "choices.0.native_finish_reason").String(); got != "max_output_tokens" {
		t.Fatalf("stream native_finish_reason = %q, want max_output_tokens; payload=%s", got, streamOut[0])
	}

	var toolParam any
	_ = ConvertCodexResponseToOpenAI(ctx, "gpt-5.5", nil, nil, []byte(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_1","name":"lookup"}}`), &toolParam)
	toolStreamOut := ConvertCodexResponseToOpenAI(ctx, "gpt-5.5", nil, nil, append([]byte("data: "), terminal...), &toolParam)
	if got := gjson.GetBytes(toolStreamOut[0], "choices.0.finish_reason").String(); got != "length" {
		t.Fatalf("tool stream finish_reason = %q, want length; payload=%s", got, toolStreamOut[0])
	}

	nonStreamOut := ConvertCodexResponseToOpenAINonStream(ctx, "gpt-5.5", nil, nil, terminal, nil)
	if got := gjson.GetBytes(nonStreamOut, "choices.0.finish_reason").String(); got != "length" {
		t.Fatalf("non-stream finish_reason = %q, want length; payload=%s", got, nonStreamOut)
	}
}

func TestConvertCodexResponseToOpenAI_StreamSetsModelFromResponseCreated(t *testing.T) {
	ctx := context.Background()
	var param any

	modelName := "gpt-5.3-codex"

	out := ConvertCodexResponseToOpenAI(ctx, modelName, nil, nil, []byte(`data: {"type":"response.created","response":{"id":"resp_123","created_at":1700000000,"model":"gpt-5.3-codex"}}`), &param)
	if len(out) != 0 {
		t.Fatalf("expected no output for response.created, got %d chunks", len(out))
	}

	out = ConvertCodexResponseToOpenAI(ctx, modelName, nil, nil, []byte(`data: {"type":"response.output_text.delta","delta":"hello"}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}

	gotModel := gjson.GetBytes(out[0], "model").String()
	if gotModel != modelName {
		t.Fatalf("expected model %q, got %q", modelName, gotModel)
	}
}

func TestConvertCodexResponseToOpenAI_FirstChunkUsesRequestModelName(t *testing.T) {
	ctx := context.Background()
	var param any

	modelName := "gpt-5.3-codex"

	out := ConvertCodexResponseToOpenAI(ctx, modelName, nil, nil, []byte(`data: {"type":"response.output_text.delta","delta":"hello"}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}

	gotModel := gjson.GetBytes(out[0], "model").String()
	if gotModel != modelName {
		t.Fatalf("expected model %q, got %q", modelName, gotModel)
	}
}

func TestConvertCodexResponseToOpenAI_ToolCallChunkOmitsNullContentFields(t *testing.T) {
	ctx := context.Background()
	var param any

	out := ConvertCodexResponseToOpenAI(ctx, "gpt-5.4", nil, nil, []byte(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_123","name":"websearch"}}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}

	if gjson.GetBytes(out[0], "choices.0.delta.content").Exists() {
		t.Fatalf("expected content to be omitted, got %s", string(out[0]))
	}
	if gjson.GetBytes(out[0], "choices.0.delta.reasoning_content").Exists() {
		t.Fatalf("expected reasoning_content to be omitted, got %s", string(out[0]))
	}
	if !gjson.GetBytes(out[0], "choices.0.delta.tool_calls").Exists() {
		t.Fatalf("expected tool_calls to exist, got %s", string(out[0]))
	}
}

func TestConvertCodexResponseToOpenAI_ToolCallArgumentsDeltaOmitsNullContentFields(t *testing.T) {
	ctx := context.Background()
	var param any

	out := ConvertCodexResponseToOpenAI(ctx, "gpt-5.4", nil, nil, []byte(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_123","name":"websearch"}}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected tool call announcement chunk, got %d", len(out))
	}

	out = ConvertCodexResponseToOpenAI(ctx, "gpt-5.4", nil, nil, []byte(`data: {"type":"response.function_call_arguments.delta","delta":"{\"query\":\"OpenAI\"}"}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}

	if gjson.GetBytes(out[0], "choices.0.delta.content").Exists() {
		t.Fatalf("expected content to be omitted, got %s", string(out[0]))
	}
	if gjson.GetBytes(out[0], "choices.0.delta.reasoning_content").Exists() {
		t.Fatalf("expected reasoning_content to be omitted, got %s", string(out[0]))
	}
	if !gjson.GetBytes(out[0], "choices.0.delta.tool_calls.0.function.arguments").Exists() {
		t.Fatalf("expected tool call arguments delta to exist, got %s", string(out[0]))
	}
}

func TestConvertCodexResponseToOpenAI_CustomToolCallStreamDeltas(t *testing.T) {
	ctx := context.Background()
	var param any
	send := func(event string) [][]byte {
		return ConvertCodexResponseToOpenAI(ctx, "gpt-5.5", nil, nil, []byte("data: "+event), &param)
	}

	out := send(`{"type":"response.output_item.added","item":{"type":"custom_tool_call","call_id":"call_apply","name":"ApplyPatch","input":"unexpected input"}}`)
	if len(out) != 1 {
		t.Fatalf("expected 1 announcement chunk, got %d", len(out))
	}
	toolCall := gjson.GetBytes(out[0], "choices.0.delta.tool_calls.0")
	if got := toolCall.Get("index").Int(); got != 0 {
		t.Fatalf("expected tool index 0, got %d; chunk=%s", got, out[0])
	}
	if got := toolCall.Get("id").String(); got != "call_apply" {
		t.Fatalf("expected call id call_apply, got %q; chunk=%s", got, out[0])
	}
	if got := toolCall.Get("function.name").String(); got != "ApplyPatch" {
		t.Fatalf("expected tool name ApplyPatch, got %q; chunk=%s", got, out[0])
	}
	if args := toolCall.Get("function.arguments"); !args.Exists() || args.String() != "" {
		t.Fatalf("expected empty announced arguments, got %s; chunk=%s", args.Raw, out[0])
	}

	for _, delta := range []string{"*** Begin Patch\n", "*** End Patch"} {
		out = send(`{"type":"response.custom_tool_call_input.delta","delta":` + string(mustJSONMarshal(t, delta)) + `}`)
		if len(out) != 1 {
			t.Fatalf("expected 1 arguments delta chunk, got %d", len(out))
		}
		if got := gjson.GetBytes(out[0], "choices.0.delta.tool_calls.0.function.arguments").String(); got != delta {
			t.Fatalf("expected arguments delta %q, got %q; chunk=%s", delta, got, out[0])
		}
	}

	fullInput := "*** Begin Patch\n*** End Patch"
	out = send(`{"type":"response.custom_tool_call_input.done","input":` + string(mustJSONMarshal(t, fullInput)) + `}`)
	if len(out) != 0 {
		t.Fatalf("expected custom input done to be suppressed after deltas, got %d chunks", len(out))
	}
	out = send(`{"type":"response.output_item.done","item":{"type":"custom_tool_call","call_id":"call_apply","name":"ApplyPatch","input":` + string(mustJSONMarshal(t, fullInput)) + `}}`)
	if len(out) != 0 {
		t.Fatalf("expected output item done to be suppressed after deltas, got %d chunks", len(out))
	}

	out = send(`{"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`)
	if len(out) != 1 {
		t.Fatalf("expected 1 completion chunk, got %d", len(out))
	}
	if got := gjson.GetBytes(out[0], "choices.0.finish_reason").String(); got != "tool_calls" {
		t.Fatalf("expected finish reason tool_calls, got %q; chunk=%s", got, out[0])
	}
}

func TestConvertCodexResponseToOpenAI_EmptyCustomToolDeltaUsesDoneFallback(t *testing.T) {
	ctx := context.Background()
	var param any

	_ = ConvertCodexResponseToOpenAI(ctx, "gpt-5.5", nil, nil, []byte(`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"ctc_1","type":"custom_tool_call","call_id":"call_apply","name":"ApplyPatch","input":""}}`), &param)
	out := ConvertCodexResponseToOpenAI(ctx, "gpt-5.5", nil, nil, []byte(`data: {"type":"response.custom_tool_call_input.delta","item_id":"ctc_1","output_index":0,"delta":""}`), &param)
	if len(out) != 0 {
		t.Fatalf("expected empty delta to be suppressed, got %d chunks", len(out))
	}

	out = ConvertCodexResponseToOpenAI(ctx, "gpt-5.5", nil, nil, []byte(`data: {"type":"response.custom_tool_call_input.done","item_id":"ctc_1","output_index":0,"input":"full patch"}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected 1 done fallback chunk, got %d", len(out))
	}
	if got := gjson.GetBytes(out[0], "choices.0.delta.tool_calls.0.function.arguments").String(); got != "full patch" {
		t.Fatalf("expected full patch arguments, got %q; chunk=%s", got, out[0])
	}
}

func TestConvertCodexResponseToOpenAI_InterleavedToolCallsKeepStateByItem(t *testing.T) {
	ctx := context.Background()
	var param any
	send := func(event string) [][]byte {
		return ConvertCodexResponseToOpenAI(ctx, "gpt-5.5", nil, nil, []byte("data: "+event), &param)
	}

	out := send(`{"type":"response.output_item.added","output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"call_lookup","name":"lookup","arguments":""}}`)
	if got := gjson.GetBytes(out[0], "choices.0.delta.tool_calls.0.index").Int(); got != 0 {
		t.Fatalf("expected function call index 0, got %d; chunk=%s", got, out[0])
	}
	out = send(`{"type":"response.output_item.added","output_index":1,"item":{"id":"ctc_2","type":"custom_tool_call","call_id":"call_apply","name":"ApplyPatch","input":""}}`)
	if got := gjson.GetBytes(out[0], "choices.0.delta.tool_calls.0.index").Int(); got != 1 {
		t.Fatalf("expected custom call index 1, got %d; chunk=%s", got, out[0])
	}

	out = send(`{"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"{\"query\":"}`)
	if got := gjson.GetBytes(out[0], "choices.0.delta.tool_calls.0.index").Int(); got != 0 {
		t.Fatalf("expected interleaved function delta index 0, got %d; chunk=%s", got, out[0])
	}
	out = send(`{"type":"response.custom_tool_call_input.delta","output_index":1,"delta":""}`)
	if len(out) != 0 {
		t.Fatalf("expected empty custom delta to be suppressed, got %d chunks", len(out))
	}
	out = send(`{"type":"response.custom_tool_call_input.done","output_index":1,"input":"patch"}`)
	if len(out) != 1 {
		t.Fatalf("expected custom done fallback, got %d chunks", len(out))
	}
	if got := gjson.GetBytes(out[0], "choices.0.delta.tool_calls.0.index").Int(); got != 1 {
		t.Fatalf("expected output-index-routed custom fallback index 1, got %d; chunk=%s", got, out[0])
	}
	if got := gjson.GetBytes(out[0], "choices.0.delta.tool_calls.0.function.arguments").String(); got != "patch" {
		t.Fatalf("expected custom fallback arguments patch, got %q; chunk=%s", got, out[0])
	}

	for _, event := range []string{
		`{"type":"response.function_call_arguments.done","item_id":"fc_1","output_index":0,"arguments":"{\"query\":\"test\"}"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"call_lookup","name":"lookup","arguments":"{\"query\":\"test\"}"}}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"id":"ctc_2","type":"custom_tool_call","call_id":"call_apply","name":"ApplyPatch","input":"patch"}}`,
	} {
		if out = send(event); len(out) != 0 {
			t.Fatalf("expected terminal tool event to avoid duplicate output, got %d chunks for %s", len(out), event)
		}
	}
}

func TestConvertCodexResponseToOpenAI_CustomToolCallInputDoneFallback(t *testing.T) {
	ctx := context.Background()
	var param any

	_ = ConvertCodexResponseToOpenAI(ctx, "gpt-5.5", nil, nil, []byte(`data: {"type":"response.output_item.added","item":{"type":"custom_tool_call","call_id":"call_apply","name":"ApplyPatch","input":""}}`), &param)
	out := ConvertCodexResponseToOpenAI(ctx, "gpt-5.5", nil, nil, []byte(`data: {"type":"response.custom_tool_call_input.done","input":"full patch"}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected 1 fallback arguments chunk, got %d", len(out))
	}
	if got := gjson.GetBytes(out[0], "choices.0.delta.tool_calls.0.function.arguments").String(); got != "full patch" {
		t.Fatalf("expected full patch arguments, got %q; chunk=%s", got, out[0])
	}

	out = ConvertCodexResponseToOpenAI(ctx, "gpt-5.5", nil, nil, []byte(`data: {"type":"response.output_item.done","item":{"type":"custom_tool_call","call_id":"call_apply","name":"ApplyPatch","input":"full patch"}}`), &param)
	if len(out) != 0 {
		t.Fatalf("expected output item done to be suppressed after input done fallback, got %d chunks", len(out))
	}
}

func TestConvertCodexResponseToOpenAI_ToolCallOutputItemDoneFallbacks(t *testing.T) {
	t.Run("announced custom call emits arguments only", func(t *testing.T) {
		ctx := context.Background()
		var param any

		_ = ConvertCodexResponseToOpenAI(ctx, "gpt-5.5", nil, nil, []byte(`data: {"type":"response.output_item.added","item":{"type":"custom_tool_call","call_id":"call_first","name":"ApplyPatch","input":""}}`), &param)
		out := ConvertCodexResponseToOpenAI(ctx, "gpt-5.5", nil, nil, []byte(`data: {"type":"response.output_item.done","item":{"type":"custom_tool_call","call_id":"call_first","name":"ApplyPatch","input":"first patch"}}`), &param)
		if len(out) != 1 {
			t.Fatalf("expected 1 fallback arguments chunk, got %d", len(out))
		}
		toolCall := gjson.GetBytes(out[0], "choices.0.delta.tool_calls.0")
		if got := toolCall.Get("index").Int(); got != 0 {
			t.Fatalf("expected tool index 0, got %d; chunk=%s", got, out[0])
		}
		if toolCall.Get("id").Exists() || toolCall.Get("function.name").Exists() {
			t.Fatalf("expected arguments-only fallback, got %s", toolCall.Raw)
		}
		if got := toolCall.Get("function.arguments").String(); got != "first patch" {
			t.Fatalf("expected first patch arguments, got %q; chunk=%s", got, out[0])
		}

		_ = ConvertCodexResponseToOpenAI(ctx, "gpt-5.5", nil, nil, []byte(`data: {"type":"response.output_item.added","item":{"type":"custom_tool_call","call_id":"call_second","name":"ApplyPatch","input":""}}`), &param)
		out = ConvertCodexResponseToOpenAI(ctx, "gpt-5.5", nil, nil, []byte(`data: {"type":"response.output_item.done","item":{"type":"custom_tool_call","call_id":"call_second","name":"ApplyPatch","input":"second patch"}}`), &param)
		if len(out) != 1 {
			t.Fatalf("expected 1 second fallback arguments chunk, got %d", len(out))
		}
		if got := gjson.GetBytes(out[0], "choices.0.delta.tool_calls.0.index").Int(); got != 1 {
			t.Fatalf("expected second tool index 1, got %d; chunk=%s", got, out[0])
		}
	})

	t.Run("unannounced custom call emits complete call", func(t *testing.T) {
		ctx := context.Background()
		var param any
		out := ConvertCodexResponseToOpenAI(ctx, "gpt-5.5", nil, nil, []byte(`data: {"type":"response.output_item.done","item":{"type":"custom_tool_call","call_id":"call_apply","name":"ApplyPatch","input":"full patch"}}`), &param)
		if len(out) != 1 {
			t.Fatalf("expected 1 complete fallback chunk, got %d", len(out))
		}
		toolCall := gjson.GetBytes(out[0], "choices.0.delta.tool_calls.0")
		if got := toolCall.Get("id").String(); got != "call_apply" {
			t.Fatalf("expected call id call_apply, got %q; chunk=%s", got, out[0])
		}
		if got := toolCall.Get("function.name").String(); got != "ApplyPatch" {
			t.Fatalf("expected tool name ApplyPatch, got %q; chunk=%s", got, out[0])
		}
		if got := toolCall.Get("function.arguments").String(); got != "full patch" {
			t.Fatalf("expected full patch arguments, got %q; chunk=%s", got, out[0])
		}
	})

	t.Run("announced function call still falls back", func(t *testing.T) {
		ctx := context.Background()
		var param any

		_ = ConvertCodexResponseToOpenAI(ctx, "gpt-5.5", nil, nil, []byte(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_lookup","name":"lookup","arguments":""}}`), &param)
		out := ConvertCodexResponseToOpenAI(ctx, "gpt-5.5", nil, nil, []byte(`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_lookup","name":"lookup","arguments":"{\"query\":\"test\"}"}}`), &param)
		if len(out) != 1 {
			t.Fatalf("expected 1 function arguments fallback chunk, got %d", len(out))
		}
		if got := gjson.GetBytes(out[0], "choices.0.delta.tool_calls.0.function.arguments").String(); got != `{"query":"test"}` {
			t.Fatalf("expected function arguments fallback, got %q; chunk=%s", got, out[0])
		}
	})
}

func TestConvertCodexResponseToOpenAI_ToolCallStateFallsBackFromUnknownItemID(t *testing.T) {
	ctx := context.Background()
	var param any

	added := ConvertCodexResponseToOpenAI(
		ctx,
		"gpt-5.6-terra",
		nil,
		nil,
		[]byte(`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"TaskCreate","arguments":""}}`),
		&param,
	)
	if len(added) != 1 {
		t.Fatalf("added chunks = %d, want 1", len(added))
	}

	done := ConvertCodexResponseToOpenAI(
		ctx,
		"gpt-5.6-terra",
		nil,
		nil,
		[]byte(`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"TaskCreate","arguments":"{\"subject\":\"test\"}"}}`),
		&param,
	)
	if len(done) != 1 {
		t.Fatalf("done chunks = %d, want 1", len(done))
	}

	addedName := gjson.GetBytes(added[0], "choices.0.delta.tool_calls.0.function.name").String()
	doneName := gjson.GetBytes(done[0], "choices.0.delta.tool_calls.0.function.name").String()
	if got := addedName + doneName; got != "TaskCreate" {
		t.Fatalf("assembled tool name = %q, want %q", got, "TaskCreate")
	}

	toolCall := gjson.GetBytes(done[0], "choices.0.delta.tool_calls.0")
	if toolCall.Get("id").Exists() || toolCall.Get("function.name").Exists() {
		t.Fatalf("done chunk repeated tool identity: %s", toolCall.Raw)
	}
	if got := toolCall.Get("index").Int(); got != 0 {
		t.Fatalf("done tool index = %d, want 0", got)
	}
	if got := toolCall.Get("function.arguments").String(); got != `{"subject":"test"}` {
		t.Fatalf("done arguments = %q", got)
	}
}

func TestConvertCodexResponseToOpenAINonStream_CustomToolCall(t *testing.T) {
	ctx := context.Background()
	raw := []byte(`{"type":"response.completed","response":{"id":"resp_123","created_at":1700000000,"model":"gpt-5.5","status":"completed","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2},"output":[{"type":"custom_tool_call","call_id":"call_apply","name":"ApplyPatch","input":"full patch"}]}}`)

	out := ConvertCodexResponseToOpenAINonStream(ctx, "gpt-5.5", nil, nil, raw, nil)
	toolCall := gjson.GetBytes(out, "choices.0.message.tool_calls.0")
	if got := toolCall.Get("id").String(); got != "call_apply" {
		t.Fatalf("expected call id call_apply, got %q; response=%s", got, out)
	}
	if got := toolCall.Get("function.name").String(); got != "ApplyPatch" {
		t.Fatalf("expected tool name ApplyPatch, got %q; response=%s", got, out)
	}
	if got := toolCall.Get("function.arguments").String(); got != "full patch" {
		t.Fatalf("expected full patch arguments, got %q; response=%s", got, out)
	}
	if got := gjson.GetBytes(out, "choices.0.finish_reason").String(); got != "tool_calls" {
		t.Fatalf("expected finish reason tool_calls, got %q; response=%s", got, out)
	}
}

func TestConvertCodexResponseToOpenAI_StreamPartialImageEmitsDeltaImages(t *testing.T) {
	ctx := context.Background()
	var param any

	chunk := []byte(`data: {"type":"response.image_generation_call.partial_image","item_id":"ig_123","output_format":"png","partial_image_b64":"aGVsbG8=","partial_image_index":0}`)

	out := ConvertCodexResponseToOpenAI(ctx, "gpt-5.4", nil, nil, chunk, &param)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}

	gotURL := gjson.GetBytes(out[0], "choices.0.delta.images.0.image_url.url").String()
	if gotURL != "data:image/png;base64,aGVsbG8=" {
		t.Fatalf("expected image url %q, got %q; chunk=%s", "data:image/png;base64,aGVsbG8=", gotURL, string(out[0]))
	}

	out = ConvertCodexResponseToOpenAI(ctx, "gpt-5.4", nil, nil, chunk, &param)
	if len(out) != 0 {
		t.Fatalf("expected duplicate image chunk to be suppressed, got %d", len(out))
	}
}

func TestConvertCodexResponseToOpenAI_StreamImageGenerationCallDoneEmitsDeltaImages(t *testing.T) {
	ctx := context.Background()
	var param any

	out := ConvertCodexResponseToOpenAI(ctx, "gpt-5.4", nil, nil, []byte(`data: {"type":"response.image_generation_call.partial_image","item_id":"ig_123","output_format":"png","partial_image_b64":"aGVsbG8=","partial_image_index":0}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}

	out = ConvertCodexResponseToOpenAI(ctx, "gpt-5.4", nil, nil, []byte(`data: {"type":"response.output_item.done","item":{"id":"ig_123","type":"image_generation_call","output_format":"png","result":"aGVsbG8="}}`), &param)
	if len(out) != 0 {
		t.Fatalf("expected output_item.done to be suppressed when identical to last partial image, got %d", len(out))
	}

	out = ConvertCodexResponseToOpenAI(ctx, "gpt-5.4", nil, nil, []byte(`data: {"type":"response.output_item.done","item":{"id":"ig_123","type":"image_generation_call","output_format":"jpeg","result":"Ymll"}}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}

	gotURL := gjson.GetBytes(out[0], "choices.0.delta.images.0.image_url.url").String()
	if gotURL != "data:image/jpeg;base64,Ymll" {
		t.Fatalf("expected image url %q, got %q; chunk=%s", "data:image/jpeg;base64,Ymll", gotURL, string(out[0]))
	}
}

func TestConvertCodexResponseToOpenAI_NonStreamImageGenerationCallAddsMessageImages(t *testing.T) {
	ctx := context.Background()

	raw := []byte(`{"type":"response.completed","response":{"id":"resp_123","created_at":1700000000,"model":"gpt-5.4","status":"completed","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2},"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]},{"type":"image_generation_call","output_format":"png","result":"aGVsbG8="}]}}`)
	out := ConvertCodexResponseToOpenAINonStream(ctx, "gpt-5.4", nil, nil, raw, nil)

	gotURL := gjson.GetBytes(out, "choices.0.message.images.0.image_url.url").String()
	if gotURL != "data:image/png;base64,aGVsbG8=" {
		t.Fatalf("expected image url %q, got %q; chunk=%s", "data:image/png;base64,aGVsbG8=", gotURL, string(out))
	}
}

func TestConvertCodexResponseToOpenAI_StreamForwardsCacheWriteTokens(t *testing.T) {
	ctx := context.Background()
	var param any

	// Seed response.created so response.completed can reuse response metadata.
	_ = ConvertCodexResponseToOpenAI(ctx, "gpt-5.4", nil, nil, []byte(`data: {"type":"response.created","response":{"id":"resp_123","created_at":1700000000,"model":"gpt-5.4"}}`), &param)

	chunk := []byte(`data: {"type":"response.completed","response":{"id":"resp_123","created_at":1700000000,"model":"gpt-5.4","usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120,"input_tokens_details":{"cached_tokens":30,"cache_write_tokens":40},"output_tokens_details":{"reasoning_tokens":5}}}}`)
	out := ConvertCodexResponseToOpenAI(ctx, "gpt-5.4", nil, nil, chunk, &param)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}

	assertUsageMapping(t, out[0], 40, true)
}

func TestConvertCodexResponseToOpenAI_StreamOmitsMissingCacheWriteTokens(t *testing.T) {
	ctx := context.Background()
	var param any

	_ = ConvertCodexResponseToOpenAI(ctx, "gpt-5.4", nil, nil, []byte(`data: {"type":"response.created","response":{"id":"resp_123","created_at":1700000000,"model":"gpt-5.4"}}`), &param)

	chunk := []byte(`data: {"type":"response.completed","response":{"id":"resp_123","created_at":1700000000,"model":"gpt-5.4","usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120,"input_tokens_details":{"cached_tokens":30},"output_tokens_details":{"reasoning_tokens":5}}}}`)
	out := ConvertCodexResponseToOpenAI(ctx, "gpt-5.4", nil, nil, chunk, &param)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}

	assertUsageMapping(t, out[0], 0, false)
}

func TestConvertCodexResponseToOpenAI_StreamPreservesExplicitZeroCacheWriteTokens(t *testing.T) {
	ctx := context.Background()
	var param any

	_ = ConvertCodexResponseToOpenAI(ctx, "gpt-5.4", nil, nil, []byte(`data: {"type":"response.created","response":{"id":"resp_123","created_at":1700000000,"model":"gpt-5.4"}}`), &param)

	chunk := []byte(`data: {"type":"response.completed","response":{"id":"resp_123","created_at":1700000000,"model":"gpt-5.4","usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120,"input_tokens_details":{"cached_tokens":30,"cache_write_tokens":0},"output_tokens_details":{"reasoning_tokens":5}}}}`)
	out := ConvertCodexResponseToOpenAI(ctx, "gpt-5.4", nil, nil, chunk, &param)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}

	assertUsageMapping(t, out[0], 0, true)
}

func TestConvertCodexResponseToOpenAI_NonStreamForwardsCacheWriteTokens(t *testing.T) {
	ctx := context.Background()
	raw := []byte(`{"type":"response.completed","response":{"id":"resp_123","created_at":1700000000,"model":"gpt-5.4","status":"completed","usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120,"input_tokens_details":{"cached_tokens":30,"cache_write_tokens":40},"output_tokens_details":{"reasoning_tokens":5}},"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}}`)
	out := ConvertCodexResponseToOpenAINonStream(ctx, "gpt-5.4", nil, nil, raw, nil)
	assertUsageMapping(t, out, 40, true)
}

func TestConvertCodexResponseToOpenAI_NonStreamOmitsMissingCacheWriteTokens(t *testing.T) {
	ctx := context.Background()
	raw := []byte(`{"type":"response.completed","response":{"id":"resp_123","created_at":1700000000,"model":"gpt-5.4","status":"completed","usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120,"input_tokens_details":{"cached_tokens":30},"output_tokens_details":{"reasoning_tokens":5}},"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}}`)
	out := ConvertCodexResponseToOpenAINonStream(ctx, "gpt-5.4", nil, nil, raw, nil)
	assertUsageMapping(t, out, 0, false)
}

func TestConvertCodexResponseToOpenAI_NonStreamPreservesExplicitZeroCacheWriteTokens(t *testing.T) {
	ctx := context.Background()
	raw := []byte(`{"type":"response.completed","response":{"id":"resp_123","created_at":1700000000,"model":"gpt-5.4","status":"completed","usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120,"input_tokens_details":{"cached_tokens":30,"cache_write_tokens":0},"output_tokens_details":{"reasoning_tokens":5}},"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}}`)
	out := ConvertCodexResponseToOpenAINonStream(ctx, "gpt-5.4", nil, nil, raw, nil)
	assertUsageMapping(t, out, 0, true)
}

func mustJSONMarshal(t *testing.T, value any) []byte {
	t.Helper()
	data, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		t.Fatalf("failed to marshal test JSON: %v", errMarshal)
	}
	return data
}

func assertUsageMapping(t *testing.T, payload []byte, wantCachedCreation int64, expectCachedCreation bool) {
	t.Helper()

	if got := gjson.GetBytes(payload, "usage.prompt_tokens").Int(); got != 100 {
		t.Fatalf("expected prompt_tokens=100, got %d; payload=%s", got, string(payload))
	}
	if got := gjson.GetBytes(payload, "usage.completion_tokens").Int(); got != 20 {
		t.Fatalf("expected completion_tokens=20, got %d; payload=%s", got, string(payload))
	}
	if got := gjson.GetBytes(payload, "usage.total_tokens").Int(); got != 120 {
		t.Fatalf("expected total_tokens=120, got %d; payload=%s", got, string(payload))
	}
	if got := gjson.GetBytes(payload, "usage.prompt_tokens_details.cached_tokens").Int(); got != 30 {
		t.Fatalf("expected cached_tokens=30, got %d; payload=%s", got, string(payload))
	}
	if got := gjson.GetBytes(payload, "usage.completion_tokens_details.reasoning_tokens").Int(); got != 5 {
		t.Fatalf("expected reasoning_tokens=5, got %d; payload=%s", got, string(payload))
	}

	gotCachedCreation := gjson.GetBytes(payload, "usage.prompt_tokens_details.cached_creation_tokens")
	gotCacheWrite := gjson.GetBytes(payload, "usage.prompt_tokens_details.cache_write_tokens")
	if expectCachedCreation {
		if !gotCachedCreation.Exists() {
			t.Fatalf("expected cached_creation_tokens to exist, payload=%s", string(payload))
		}
		if gotCachedCreation.Int() != wantCachedCreation {
			t.Fatalf("expected cached_creation_tokens=%d, got %d; payload=%s", wantCachedCreation, gotCachedCreation.Int(), string(payload))
		}
		if !gotCacheWrite.Exists() {
			t.Fatalf("expected cache_write_tokens to exist, payload=%s", string(payload))
		}
		if gotCacheWrite.Int() != wantCachedCreation {
			t.Fatalf("expected cache_write_tokens=%d, got %d; payload=%s", wantCachedCreation, gotCacheWrite.Int(), string(payload))
		}
		return
	}
	if gotCachedCreation.Exists() {
		t.Fatalf("expected cached_creation_tokens to be omitted, payload=%s", string(payload))
	}
	if gotCacheWrite.Exists() {
		t.Fatalf("expected cache_write_tokens to be omitted, payload=%s", string(payload))
	}
}

func TestConvertCodexResponseToOpenAI_NonStreamMultiMessageEmptyTrailingKeepsContent(t *testing.T) {
	ctx := context.Background()
	raw := []byte(`{"type":"response.completed","response":{"id":"resp_1","created_at":1700000000,"model":"gpt-5.5","status":"completed","usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15},"output":[` +
		`{"type":"reasoning","summary":[{"type":"summary_text","text":"thinking"}]},` +
		`{"type":"message","content":[{"type":"output_text","text":"the real answer"}]},` +
		`{"type":"reasoning","summary":[{"type":"summary_text","text":"thinking again"}]},` +
		`{"type":"message","content":[{"type":"output_text","text":""}]}` +
		`]}}`)
	out := ConvertCodexResponseToOpenAINonStream(ctx, "gpt-5.5", nil, nil, raw, nil)

	got := gjson.GetBytes(out, "choices.0.message.content")
	if !got.Exists() || got.Type == gjson.Null {
		t.Fatalf("content was dropped to null by trailing empty message; resp=%s", string(out))
	}
	if got.String() != "the real answer" {
		t.Fatalf("expected content %q, got %q; resp=%s", "the real answer", got.String(), string(out))
	}
}

func TestConvertCodexResponseToOpenAI_StreamReasoningTextDeltaAndDone(t *testing.T) {
	ctx := context.Background()
	var param any

	deltaRaw := []byte(`data: {"type":"response.reasoning_text.delta","delta":"Thinking step 1"}`)
	streamOut := ConvertCodexResponseToOpenAI(ctx, "MiniMax-M3", nil, nil, deltaRaw, &param)
	if len(streamOut) != 1 {
		t.Fatalf("expected 1 streaming chunk for reasoning_text.delta, got %d", len(streamOut))
	}
	if got := gjson.GetBytes(streamOut[0], "choices.0.delta.reasoning_content").String(); got != "Thinking step 1" {
		t.Fatalf("expected reasoning_content %q, got %q; payload=%s", "Thinking step 1", got, streamOut[0])
	}
	if got := gjson.GetBytes(streamOut[0], "choices.0.delta.role").String(); got != "assistant" {
		t.Fatalf("expected role assistant, got %q; payload=%s", got, streamOut[0])
	}

	doneRaw := []byte(`data: {"type":"response.reasoning_text.done","text":"Thinking step 1"}`)
	doneOut := ConvertCodexResponseToOpenAI(ctx, "MiniMax-M3", nil, nil, doneRaw, &param)
	if len(doneOut) != 1 {
		t.Fatalf("expected 1 streaming chunk for reasoning_text.done, got %d", len(doneOut))
	}
	if got := gjson.GetBytes(doneOut[0], "choices.0.delta.reasoning_content").String(); got != "\n\n" {
		t.Fatalf("expected reasoning_content %q, got %q; payload=%s", "\n\n", got, doneOut[0])
	}
}

func TestConvertCodexResponseToOpenAI_NonStreamReasoningTextContent(t *testing.T) {
	ctx := context.Background()
	raw := []byte(`{"type":"response.completed","response":{"id":"resp_1","created_at":1700000000,"model":"MiniMax-M3","status":"completed","usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30,"output_tokens_details":{"reasoning_tokens":15}},"output":[` +
		`{"type":"reasoning","summary":[],"content":[{"type":"reasoning_text","text":"Full reasoning from MiniMax"}]},` +
		`{"type":"message","content":[{"type":"output_text","text":"Answer"}]}` +
		`]}}`)
	out := ConvertCodexResponseToOpenAINonStream(ctx, "MiniMax-M3", nil, nil, raw, nil)

	got := gjson.GetBytes(out, "choices.0.message.reasoning_content")
	if !got.Exists() || got.Type == gjson.Null {
		t.Fatalf("expected reasoning_content to exist, got null/missing; payload=%s", string(out))
	}
	if got.String() != "Full reasoning from MiniMax" {
		t.Fatalf("expected reasoning_content %q, got %q; payload=%s", "Full reasoning from MiniMax", got.String(), string(out))
	}
}

func TestConvertCodexResponseToOpenAI_NonStreamReasoningSummaryAndContent(t *testing.T) {
	ctx := context.Background()
	raw := []byte(`{"type":"response.completed","response":{"id":"resp_1","created_at":1700000000,"model":"MiniMax-M3","status":"completed","usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30,"output_tokens_details":{"reasoning_tokens":15}},"output":[` +
		`{"type":"reasoning","summary":[{"type":"summary_text","text":"Summary part"}],"content":[{"type":"reasoning_text","text":" and Content part"}]},` +
		`{"type":"message","content":[{"type":"output_text","text":"Answer"}]}` +
		`]}}`)
	out := ConvertCodexResponseToOpenAINonStream(ctx, "MiniMax-M3", nil, nil, raw, nil)

	got := gjson.GetBytes(out, "choices.0.message.reasoning_content")
	if !got.Exists() || got.Type == gjson.Null {
		t.Fatalf("expected reasoning_content to exist, got null/missing; payload=%s", string(out))
	}
	if got.String() != "Summary part and Content part" {
		t.Fatalf("expected reasoning_content %q, got %q; payload=%s", "Summary part and Content part", got.String(), string(out))
	}
}

func TestConvertCodexResponseToOpenAI_Issue5543_CacheWriteTokensAndServiceTier(t *testing.T) {
	ctx := context.Background()

	rawTerminal := []byte(`{"type":"response.completed","response":{"id":"resp_example","model":"example-model","service_tier":"default","output":[],"usage":{"input_tokens":7378,"output_tokens":6,"total_tokens":7384,"input_tokens_details":{"cached_tokens":7168,"cache_write_tokens":128}}}}`)

	t.Run("non-stream response retains cache_write_tokens and service_tier", func(t *testing.T) {
		out := ConvertCodexResponseToOpenAINonStream(ctx, "example-model", nil, nil, rawTerminal, nil)

		if got := gjson.GetBytes(out, "service_tier").String(); got != "default" {
			t.Fatalf("expected service_tier 'default', got %q; payload=%s", got, string(out))
		}
		if got := gjson.GetBytes(out, "usage.prompt_tokens").Int(); got != 7378 {
			t.Fatalf("expected usage.prompt_tokens=7378, got %d; payload=%s", got, string(out))
		}
		if got := gjson.GetBytes(out, "usage.completion_tokens").Int(); got != 6 {
			t.Fatalf("expected usage.completion_tokens=6, got %d; payload=%s", got, string(out))
		}
		if got := gjson.GetBytes(out, "usage.total_tokens").Int(); got != 7384 {
			t.Fatalf("expected usage.total_tokens=7384, got %d; payload=%s", got, string(out))
		}
		if got := gjson.GetBytes(out, "usage.prompt_tokens_details.cache_write_tokens").Int(); got != 128 {
			t.Fatalf("expected usage.prompt_tokens_details.cache_write_tokens=128, got %d; payload=%s", got, string(out))
		}
		if got := gjson.GetBytes(out, "usage.prompt_tokens_details.cached_creation_tokens").Int(); got != 128 {
			t.Fatalf("expected usage.prompt_tokens_details.cached_creation_tokens=128, got %d; payload=%s", got, string(out))
		}
		if got := gjson.GetBytes(out, "usage.prompt_tokens_details.cached_tokens").Int(); got != 7168 {
			t.Fatalf("expected usage.prompt_tokens_details.cached_tokens=7168, got %d; payload=%s", got, string(out))
		}
	})

	t.Run("streaming terminal chunk retains cache_write_tokens and service_tier", func(t *testing.T) {
		var param any
		chunk := append([]byte("data: "), rawTerminal...)
		chunks := ConvertCodexResponseToOpenAI(ctx, "example-model", nil, nil, chunk, &param)
		if len(chunks) != 1 {
			t.Fatalf("expected 1 chunk, got %d", len(chunks))
		}
		if got := gjson.GetBytes(chunks[0], "service_tier").String(); got != "default" {
			t.Fatalf("expected stream chunk service_tier 'default', got %q; payload=%s", got, string(chunks[0]))
		}
		if got := gjson.GetBytes(chunks[0], "usage.prompt_tokens").Int(); got != 7378 {
			t.Fatalf("expected stream chunk prompt_tokens=7378, got %d; payload=%s", got, string(chunks[0]))
		}
		if got := gjson.GetBytes(chunks[0], "usage.completion_tokens").Int(); got != 6 {
			t.Fatalf("expected stream chunk completion_tokens=6, got %d; payload=%s", got, string(chunks[0]))
		}
		if got := gjson.GetBytes(chunks[0], "usage.total_tokens").Int(); got != 7384 {
			t.Fatalf("expected stream chunk total_tokens=7384, got %d; payload=%s", got, string(chunks[0]))
		}
		if got := gjson.GetBytes(chunks[0], "usage.prompt_tokens_details.cache_write_tokens").Int(); got != 128 {
			t.Fatalf("expected stream chunk cache_write_tokens=128, got %d; payload=%s", got, string(chunks[0]))
		}
		if got := gjson.GetBytes(chunks[0], "usage.prompt_tokens_details.cached_creation_tokens").Int(); got != 128 {
			t.Fatalf("expected stream chunk cached_creation_tokens=128, got %d; payload=%s", got, string(chunks[0]))
		}
	})

	t.Run("streaming response.created carries over service_tier to deltas", func(t *testing.T) {
		var param any
		createdEvent := []byte(`data: {"type":"response.created","response":{"id":"resp_stream","created_at":1700000000,"model":"example-model","service_tier":"priority"}}`)
		createdChunks := ConvertCodexResponseToOpenAI(ctx, "example-model", nil, nil, createdEvent, &param)
		if len(createdChunks) != 0 {
			t.Fatalf("expected response.created to yield 0 chunks, got %d", len(createdChunks))
		}

		deltaEvent := []byte(`data: {"type":"response.output_text.delta","delta":"hello"}`)
		deltaChunks := ConvertCodexResponseToOpenAI(ctx, "example-model", nil, nil, deltaEvent, &param)
		if len(deltaChunks) != 1 {
			t.Fatalf("expected 1 delta chunk, got %d", len(deltaChunks))
		}
		if got := gjson.GetBytes(deltaChunks[0], "service_tier").String(); got != "priority" {
			t.Fatalf("expected delta chunk service_tier 'priority', got %q; payload=%s", got, string(deltaChunks[0]))
		}

		// Terminal event omitting service_tier preserves prior actual tier
		terminalWithoutTier := []byte(`data: {"type":"response.completed","response":{"id":"resp_stream","created_at":1700000000,"model":"example-model","usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}`)
		termChunks := ConvertCodexResponseToOpenAI(ctx, "example-model", nil, nil, terminalWithoutTier, &param)
		if len(termChunks) != 1 {
			t.Fatalf("expected 1 terminal chunk, got %d", len(termChunks))
		}
		if got := gjson.GetBytes(termChunks[0], "service_tier").String(); got != "priority" {
			t.Fatalf("expected terminal chunk to retain prior service_tier 'priority', got %q; payload=%s", got, string(termChunks[0]))
		}
	})

	t.Run("streaming in_progress updates tier and completed overrides tier", func(t *testing.T) {
		var param any
		_ = ConvertCodexResponseToOpenAI(ctx, "example-model", nil, nil, []byte(`data: {"type":"response.created","response":{"id":"resp_seq","created_at":1700000000,"model":"example-model","service_tier":"default"}}`), &param)

		// in_progress updates tier
		_ = ConvertCodexResponseToOpenAI(ctx, "example-model", nil, nil, []byte(`data: {"type":"response.in_progress","response":{"id":"resp_seq","service_tier":"priority"}}`), &param)

		deltaChunks := ConvertCodexResponseToOpenAI(ctx, "example-model", nil, nil, []byte(`data: {"type":"response.output_text.delta","delta":"hi"}`), &param)
		if len(deltaChunks) != 1 {
			t.Fatalf("expected 1 delta chunk, got %d", len(deltaChunks))
		}
		if got := gjson.GetBytes(deltaChunks[0], "service_tier").String(); got != "priority" {
			t.Fatalf("expected in_progress tier 'priority', got %q; chunk=%s", got, string(deltaChunks[0]))
		}

		// completed overrides tier
		completedEvent := []byte(`data: {"type":"response.completed","response":{"id":"resp_seq","service_tier":"scale","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`)
		completedChunks := ConvertCodexResponseToOpenAI(ctx, "example-model", nil, nil, completedEvent, &param)
		if len(completedChunks) != 1 {
			t.Fatalf("expected 1 completed chunk, got %d", len(completedChunks))
		}
		if got := gjson.GetBytes(completedChunks[0], "service_tier").String(); got != "scale" {
			t.Fatalf("expected completed tier 'scale', got %q; chunk=%s", got, string(completedChunks[0]))
		}
	})

	t.Run("large integer cache_write_tokens beyond int64 preserved without precision loss", func(t *testing.T) {
		beyondInt64Val := "999999999999999999999999999999" // > 2^63 - 1
		raw := []byte(`{"type":"response.completed","response":{"id":"resp_large","model":"example-model","service_tier":"default","usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120,"input_tokens_details":{"cache_write_tokens":` + beyondInt64Val + `}}}}`)
		out := ConvertCodexResponseToOpenAINonStream(ctx, "example-model", nil, nil, raw, nil)
		if got := gjson.GetBytes(out, "usage.prompt_tokens_details.cache_write_tokens").Raw; got != beyondInt64Val {
			t.Fatalf("expected exact raw large cache_write_tokens %s, got %s; payload=%s", beyondInt64Val, got, string(out))
		}
		if got := gjson.GetBytes(out, "usage.prompt_tokens_details.cached_creation_tokens").Raw; got != beyondInt64Val {
			t.Fatalf("expected exact raw large cached_creation_tokens %s, got %s; payload=%s", beyondInt64Val, got, string(out))
		}

		// Stream path
		var param any
		chunk := append([]byte("data: "), raw...)
		chunks := ConvertCodexResponseToOpenAI(ctx, "example-model", nil, nil, chunk, &param)
		if len(chunks) != 1 {
			t.Fatalf("expected 1 chunk, got %d", len(chunks))
		}
		if got := gjson.GetBytes(chunks[0], "usage.prompt_tokens_details.cache_write_tokens").Raw; got != beyondInt64Val {
			t.Fatalf("expected stream exact raw large cache_write_tokens %s, got %s; chunk=%s", beyondInt64Val, got, string(chunks[0]))
		}
		if got := gjson.GetBytes(chunks[0], "usage.prompt_tokens_details.cached_creation_tokens").Raw; got != beyondInt64Val {
			t.Fatalf("expected stream exact raw large cached_creation_tokens %s, got %s; chunk=%s", beyondInt64Val, got, string(chunks[0]))
		}
	})

	t.Run("large integer cache_write_tokens preserved without precision loss", func(t *testing.T) {
		largeVal := "9007199254740993" // 2^53 + 1
		raw := []byte(`{"type":"response.completed","response":{"id":"resp_large","model":"example-model","service_tier":"default","usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120,"input_tokens_details":{"cache_write_tokens":` + largeVal + `}}}}`)
		out := ConvertCodexResponseToOpenAINonStream(ctx, "example-model", nil, nil, raw, nil)
		if got := gjson.GetBytes(out, "usage.prompt_tokens_details.cache_write_tokens").Raw; got != largeVal {
			t.Fatalf("expected exact raw large cache_write_tokens %s, got %s; payload=%s", largeVal, got, string(out))
		}
		if got := gjson.GetBytes(out, "usage.prompt_tokens_details.cached_creation_tokens").Raw; got != largeVal {
			t.Fatalf("expected exact raw large cached_creation_tokens %s, got %s; payload=%s", largeVal, got, string(out))
		}
	})

	t.Run("invalid cache_write_tokens formats are rejected", func(t *testing.T) {
		invalidCases := []string{
			`{"input_tokens":10,"output_tokens":5,"total_tokens":15,"input_tokens_details":{"cache_write_tokens":-1}}`,
			`{"input_tokens":10,"output_tokens":5,"total_tokens":15,"input_tokens_details":{"cache_write_tokens":1.5}}`,
			`{"input_tokens":10,"output_tokens":5,"total_tokens":15,"input_tokens_details":{"cache_write_tokens":"128"}}`,
			`{"input_tokens":10,"output_tokens":5,"total_tokens":15,"input_tokens_details":{"cache_write_tokens":true}}`,
		}
		for _, usageJSON := range invalidCases {
			raw := []byte(`{"type":"response.completed","response":{"id":"resp_inv","model":"example-model","usage":` + usageJSON + `}}`)
			out := ConvertCodexResponseToOpenAINonStream(ctx, "example-model", nil, nil, raw, nil)
			if gjson.GetBytes(out, "usage.prompt_tokens_details.cache_write_tokens").Exists() {
				t.Fatalf("expected invalid cache_write_tokens to be omitted; payload=%s", string(out))
			}
			if gjson.GetBytes(out, "usage.prompt_tokens_details.cached_creation_tokens").Exists() {
				t.Fatalf("expected invalid cached_creation_tokens to be omitted; payload=%s", string(out))
			}

			// Streaming path
			var param any
			chunk := append([]byte("data: "), raw...)
			chunks := ConvertCodexResponseToOpenAI(ctx, "example-model", nil, nil, chunk, &param)
			if len(chunks) == 1 {
				if gjson.GetBytes(chunks[0], "usage.prompt_tokens_details.cache_write_tokens").Exists() {
					t.Fatalf("expected stream invalid cache_write_tokens to be omitted; chunk=%s", string(chunks[0]))
				}
				if gjson.GetBytes(chunks[0], "usage.prompt_tokens_details.cached_creation_tokens").Exists() {
					t.Fatalf("expected stream invalid cached_creation_tokens to be omitted; chunk=%s", string(chunks[0]))
				}
			}
		}
	})

	t.Run("invalid or whitespace service_tier is ignored", func(t *testing.T) {
		invalidTiers := []string{
			`"   "`,
			`""`,
			`123`,
			`true`,
			`null`,
		}
		for _, tierVal := range invalidTiers {
			raw := []byte(`{"type":"response.completed","response":{"id":"resp_tier","model":"example-model","service_tier":` + tierVal + `,"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`)
			out := ConvertCodexResponseToOpenAINonStream(ctx, "example-model", nil, nil, raw, nil)
			if gjson.GetBytes(out, "service_tier").Exists() {
				t.Fatalf("expected invalid service_tier %s to be omitted; payload=%s", tierVal, string(out))
			}
		}
	})
}

func TestConvertCodexResponseToOpenAI_RestoresNormalizedToolNames(t *testing.T) {
	ctx := context.Background()
	originalName := "mcp.server:search tool"
	normalizedName := "mcp_server_search_tool"
	originalRequest := []byte(`{
		"tools": [
			{
				"type": "function",
				"function": {
					"name": "` + originalName + `"
				}
			}
		]
	}`)

	// Test non-stream response
	rawNonStream := []byte(`{"type":"response.completed","response":{"id":"resp_1","created_at":1700000000,"model":"gpt-5.6-sol","status":"completed","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2},"output":[{"type":"function_call","call_id":"call_1","name":"` + normalizedName + `","arguments":"{}"}]}}`)
	outNonStream := ConvertCodexResponseToOpenAINonStream(ctx, "gpt-5.6-sol", originalRequest, nil, rawNonStream, nil)
	gotNameNonStream := gjson.GetBytes(outNonStream, "choices.0.message.tool_calls.0.function.name").String()
	if gotNameNonStream != originalName {
		t.Fatalf("non-stream expected restored name %q, got %q", originalName, gotNameNonStream)
	}

	// Test stream response
	var param any
	rawStreamAdded := []byte(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_1","name":"` + normalizedName + `"}}`)
	streamChunks := ConvertCodexResponseToOpenAI(ctx, "gpt-5.6-sol", originalRequest, nil, rawStreamAdded, &param)
	if len(streamChunks) != 1 {
		t.Fatalf("expected 1 stream chunk, got %d", len(streamChunks))
	}
	gotNameStream := gjson.GetBytes(streamChunks[0], "choices.0.delta.tool_calls.0.function.name").String()
	if gotNameStream != originalName {
		t.Fatalf("stream expected restored name %q, got %q", originalName, gotNameStream)
	}
}
