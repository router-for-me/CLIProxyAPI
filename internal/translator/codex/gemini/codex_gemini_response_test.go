package gemini

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertCodexResponseToGemini_IncompleteTerminal(t *testing.T) {
	ctx := context.Background()
	terminal := []byte(`{"type":"response.incomplete","response":{"id":"resp_1","model":"gpt-5.5","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`)

	var param any
	streamOut := ConvertCodexResponseToGemini(ctx, "gemini-2.5-pro", nil, nil, append([]byte("data: "), terminal...), &param)
	if len(streamOut) != 1 {
		t.Fatalf("expected 1 streaming terminal chunk, got %d", len(streamOut))
	}
	if got := gjson.GetBytes(streamOut[0], "candidates.0.finishReason").String(); got != "MAX_TOKENS" {
		t.Fatalf("stream finishReason = %q, want MAX_TOKENS; payload=%s", got, streamOut[0])
	}

	nonStreamOut := ConvertCodexResponseToGeminiNonStream(ctx, "gemini-2.5-pro", nil, nil, terminal, nil)
	if got := gjson.GetBytes(nonStreamOut, "candidates.0.finishReason").String(); got != "MAX_TOKENS" {
		t.Fatalf("non-stream finishReason = %q, want MAX_TOKENS; payload=%s", got, nonStreamOut)
	}
}

func TestConvertCodexResponseToGemini_StreamEmptyOutputUsesOutputItemDoneMessageFallback(t *testing.T) {
	ctx := context.Background()
	originalRequest := []byte(`{"tools":[]}`)
	var param any

	chunks := [][]byte{
		[]byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]},\"output_index\":0}"),
		[]byte("data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}"),
	}

	var outputs [][]byte
	for _, chunk := range chunks {
		outputs = append(outputs, ConvertCodexResponseToGemini(ctx, "gemini-2.5-pro", originalRequest, nil, chunk, &param)...)
	}

	found := false
	for _, out := range outputs {
		if gjson.GetBytes(out, "candidates.0.content.parts.0.text").String() == "ok" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected fallback content from response.output_item.done message; outputs=%q", outputs)
	}
}

func TestConvertCodexResponseToGemini_StreamPartialImageEmitsInlineData(t *testing.T) {
	ctx := context.Background()
	originalRequest := []byte(`{"tools":[]}`)
	var param any

	chunk := []byte(`data: {"type":"response.image_generation_call.partial_image","item_id":"ig_123","output_format":"png","partial_image_b64":"aGVsbG8=","partial_image_index":0}`)
	out := ConvertCodexResponseToGemini(ctx, "gemini-2.5-pro", originalRequest, nil, chunk, &param)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}

	got := gjson.GetBytes(out[0], "candidates.0.content.parts.0.inlineData.data").String()
	if got != "aGVsbG8=" {
		t.Fatalf("expected inlineData.data %q, got %q; chunk=%s", "aGVsbG8=", got, string(out[0]))
	}

	gotMime := gjson.GetBytes(out[0], "candidates.0.content.parts.0.inlineData.mimeType").String()
	if gotMime != "image/png" {
		t.Fatalf("expected inlineData.mimeType %q, got %q; chunk=%s", "image/png", gotMime, string(out[0]))
	}

	out = ConvertCodexResponseToGemini(ctx, "gemini-2.5-pro", originalRequest, nil, chunk, &param)
	if len(out) != 0 {
		t.Fatalf("expected duplicate image chunk to be suppressed, got %d", len(out))
	}
}

func TestConvertCodexResponseToGemini_StreamImageGenerationCallDoneEmitsInlineData(t *testing.T) {
	ctx := context.Background()
	originalRequest := []byte(`{"tools":[]}`)
	var param any

	out := ConvertCodexResponseToGemini(ctx, "gemini-2.5-pro", originalRequest, nil, []byte(`data: {"type":"response.image_generation_call.partial_image","item_id":"ig_123","output_format":"png","partial_image_b64":"aGVsbG8=","partial_image_index":0}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}

	out = ConvertCodexResponseToGemini(ctx, "gemini-2.5-pro", originalRequest, nil, []byte(`data: {"type":"response.output_item.done","item":{"id":"ig_123","type":"image_generation_call","output_format":"png","result":"aGVsbG8="}}`), &param)
	if len(out) != 0 {
		t.Fatalf("expected output_item.done to be suppressed when identical to last partial image, got %d", len(out))
	}

	out = ConvertCodexResponseToGemini(ctx, "gemini-2.5-pro", originalRequest, nil, []byte(`data: {"type":"response.output_item.done","item":{"id":"ig_123","type":"image_generation_call","output_format":"jpeg","result":"Ymll"}}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}

	got := gjson.GetBytes(out[0], "candidates.0.content.parts.0.inlineData.data").String()
	if got != "Ymll" {
		t.Fatalf("expected inlineData.data %q, got %q; chunk=%s", "Ymll", got, string(out[0]))
	}

	gotMime := gjson.GetBytes(out[0], "candidates.0.content.parts.0.inlineData.mimeType").String()
	if gotMime != "image/jpeg" {
		t.Fatalf("expected inlineData.mimeType %q, got %q; chunk=%s", "image/jpeg", gotMime, string(out[0]))
	}
}

func TestConvertCodexResponseToGemini_NonStreamImageGenerationCallAddsInlineDataPart(t *testing.T) {
	ctx := context.Background()
	originalRequest := []byte(`{"tools":[]}`)

	raw := []byte(`{"type":"response.completed","response":{"id":"resp_123","created_at":1700000000,"usage":{"input_tokens":1,"output_tokens":1},"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]},{"type":"image_generation_call","output_format":"png","result":"aGVsbG8="}]}}`)
	out := ConvertCodexResponseToGeminiNonStream(ctx, "gemini-2.5-pro", originalRequest, nil, raw, nil)

	got := gjson.GetBytes(out, "candidates.0.content.parts.1.inlineData.data").String()
	if got != "aGVsbG8=" {
		t.Fatalf("expected inlineData.data %q, got %q; chunk=%s", "aGVsbG8=", got, string(out))
	}

	gotMime := gjson.GetBytes(out, "candidates.0.content.parts.1.inlineData.mimeType").String()
	if gotMime != "image/png" {
		t.Fatalf("expected inlineData.mimeType %q, got %q; chunk=%s", "image/png", gotMime, string(out))
	}
}

func TestConvertCodexResponseToGemini_StreamPreservesFunctionCallID(t *testing.T) {
	ctx := context.Background()
	originalRequest := []byte(`{"tools":[]}`)
	var param any

	out := ConvertCodexResponseToGemini(ctx, "gemini-2.5-pro", originalRequest, nil, []byte(`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_gateway","name":"lookup","arguments":"{\"query\":\"status\"}"}}`), &param)
	if len(out) != 0 {
		t.Fatalf("expected function call output to be buffered, got %d chunks", len(out))
	}

	out = ConvertCodexResponseToGemini(ctx, "gemini-2.5-pro", originalRequest, nil, []byte(`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1}}}`), &param)
	if len(out) == 0 {
		t.Fatal("expected buffered function call to be emitted on completion")
	}

	got := ""
	for _, chunk := range out {
		if value := gjson.GetBytes(chunk, "candidates.0.content.parts.0.functionCall.id").String(); value != "" {
			got = value
			break
		}
	}
	if got != "call_gateway" {
		t.Fatalf("expected functionCall.id %q, got %q; chunks=%q", "call_gateway", got, out)
	}
}

func TestConvertCodexResponseToGeminiNonStreamPreservesFunctionCallID(t *testing.T) {
	ctx := context.Background()
	originalRequest := []byte(`{"tools":[]}`)

	raw := []byte(`{"type":"response.completed","response":{"id":"resp_123","created_at":1700000000,"usage":{"input_tokens":1,"output_tokens":1},"output":[{"type":"function_call","call_id":"call_gateway","name":"lookup","arguments":"{\"query\":\"status\"}"}]}}`)
	out := ConvertCodexResponseToGeminiNonStream(ctx, "gemini-2.5-pro", originalRequest, nil, raw, nil)

	got := gjson.GetBytes(out, "candidates.0.content.parts.0.functionCall.id").String()
	if got != "call_gateway" {
		t.Fatalf("expected functionCall.id %q, got %q; chunk=%s", "call_gateway", got, string(out))
	}
}
