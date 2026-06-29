package chat_completions

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

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

func TestConvertCodexResponseToOpenAI_LegacyFunctionsEmitFunctionCall(t *testing.T) {
	ctx := context.Background()
	var param any
	original := []byte(`{"model":"gpt-4o","functions":[{"name":"Edit","parameters":{"type":"object"}}],"function_call":"auto"}`)

	out := ConvertCodexResponseToOpenAI(ctx, "gpt-5.5", original, nil, []byte(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_123","name":"Edit"}}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected 1 function_call chunk, got %d", len(out))
	}
	if gjson.GetBytes(out[0], "choices.0.delta.tool_calls").Exists() {
		t.Fatalf("legacy functions response should not emit tool_calls: %s", string(out[0]))
	}
	if got := gjson.GetBytes(out[0], "choices.0.delta.function_call.name").String(); got != "Edit" {
		t.Fatalf("function_call.name = %q, want Edit; chunk=%s", got, string(out[0]))
	}

	out = ConvertCodexResponseToOpenAI(ctx, "gpt-5.5", original, nil, []byte(`data: {"type":"response.function_call_arguments.delta","delta":"{\"file\":\"App.tsx\"}"}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected 1 arguments chunk, got %d", len(out))
	}
	if got := gjson.GetBytes(out[0], "choices.0.delta.function_call.arguments").String(); got != `{"file":"App.tsx"}` {
		t.Fatalf("function_call.arguments = %q; chunk=%s", got, string(out[0]))
	}

	out = ConvertCodexResponseToOpenAI(ctx, "gpt-5.5", original, nil, []byte(`data: {"type":"response.completed","response":{"status":"completed"}}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected 1 completed chunk, got %d", len(out))
	}
	if got := gjson.GetBytes(out[0], "choices.0.finish_reason").String(); got != "function_call" {
		t.Fatalf("finish_reason = %q, want function_call; chunk=%s", got, string(out[0]))
	}
}

func TestConvertCodexResponseToOpenAI_CustomToolCallEmitsToolCall(t *testing.T) {
	ctx := context.Background()
	var param any

	out := ConvertCodexResponseToOpenAI(ctx, "gpt-5.5", nil, nil, []byte(`data: {"type":"response.output_item.added","item":{"type":"custom_tool_call","call_id":"call_apply","name":"ApplyPatch"}}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected custom tool call announcement chunk, got %d", len(out))
	}
	if got := gjson.GetBytes(out[0], "choices.0.delta.tool_calls.0.id").String(); got != "call_apply" {
		t.Fatalf("tool call id = %q, want call_apply; chunk=%s", got, string(out[0]))
	}
	if got := gjson.GetBytes(out[0], "choices.0.delta.tool_calls.0.function.name").String(); got != "ApplyPatch" {
		t.Fatalf("tool call name = %q, want ApplyPatch; chunk=%s", got, string(out[0]))
	}

	out = ConvertCodexResponseToOpenAI(ctx, "gpt-5.5", nil, nil, []byte(`data: {"type":"response.output_item.done","item":{"type":"custom_tool_call","call_id":"call_apply","name":"ApplyPatch","input":"*** Begin Patch\n*** End Patch"}}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected custom tool input chunk, got %d", len(out))
	}
	if got := gjson.GetBytes(out[0], "choices.0.delta.tool_calls.0.function.arguments").String(); got != "*** Begin Patch\n*** End Patch" {
		t.Fatalf("tool call arguments = %q; chunk=%s", got, string(out[0]))
	}

	out = ConvertCodexResponseToOpenAI(ctx, "gpt-5.5", nil, nil, []byte(`data: {"type":"response.completed","response":{"status":"completed"}}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected completion chunk, got %d", len(out))
	}
	if got := gjson.GetBytes(out[0], "choices.0.finish_reason").String(); got != "tool_calls" {
		t.Fatalf("finish_reason = %q, want tool_calls; chunk=%s", got, string(out[0]))
	}
}

func TestConvertCodexResponseToOpenAI_CustomToolCallInputDoneEmitsArguments(t *testing.T) {
	ctx := context.Background()
	var param any

	out := ConvertCodexResponseToOpenAI(ctx, "gpt-5.5", nil, nil, []byte(`data: {"type":"response.output_item.added","item":{"type":"custom_tool_call","call_id":"call_apply","name":"ApplyPatch"}}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected custom tool call announcement chunk, got %d", len(out))
	}

	out = ConvertCodexResponseToOpenAI(ctx, "gpt-5.5", nil, nil, []byte(`data: {"type":"response.custom_tool_call_input.done","input":"patch text"}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected custom tool input done chunk, got %d", len(out))
	}
	if got := gjson.GetBytes(out[0], "choices.0.delta.tool_calls.0.function.arguments").String(); got != "patch text" {
		t.Fatalf("tool call arguments = %q; chunk=%s", got, string(out[0]))
	}
}

func TestConvertCodexResponseToOpenAI_NonStreamCustomToolCall(t *testing.T) {
	ctx := context.Background()
	raw := []byte(`{"type":"response.completed","response":{"id":"resp_123","created_at":1700000000,"model":"gpt-5.5","status":"completed","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2},"output":[{"type":"custom_tool_call","call_id":"call_apply","name":"ApplyPatch","input":"patch text"}]}}`)

	out := ConvertCodexResponseToOpenAINonStream(ctx, "gpt-5.5", nil, nil, raw, nil)
	if got := gjson.GetBytes(out, "choices.0.message.tool_calls.0.function.name").String(); got != "ApplyPatch" {
		t.Fatalf("tool call name = %q, want ApplyPatch; response=%s", got, string(out))
	}
	if got := gjson.GetBytes(out, "choices.0.message.tool_calls.0.function.arguments").String(); got != "patch text" {
		t.Fatalf("tool call arguments = %q; response=%s", got, string(out))
	}
	if got := gjson.GetBytes(out, "choices.0.finish_reason").String(); got != "tool_calls" {
		t.Fatalf("finish_reason = %q, want tool_calls; response=%s", got, string(out))
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
