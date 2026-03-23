package chat_completions

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertCodexResponseToOpenAI_StreamIncludesCachedTokens(t *testing.T) {
	ctx := context.Background()
	var param any

	created := []byte(`data: {"type":"response.created","response":{"id":"resp_1","created_at":1700000000,"model":"gpt-5.2-codex"}}`)
	if out := ConvertCodexResponseToOpenAI(ctx, "gpt-5.2-codex", nil, nil, created, &param); len(out) != 0 {
		t.Fatalf("response.created should not emit chunks, got %d", len(out))
	}

	completed := []byte(`data: {"type":"response.completed","response":{"id":"resp_1","created_at":1700000000,"model":"gpt-5.2-codex","status":"completed","usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120,"input_tokens_details":{"cached_tokens":64},"output_tokens_details":{"reasoning_tokens":7}}}}`)
	out := ConvertCodexResponseToOpenAI(ctx, "gpt-5.2-codex", nil, nil, completed, &param)
	if len(out) != 1 {
		t.Fatalf("response.completed should emit one chunk, got %d", len(out))
	}

	chunk := gjson.Parse(out[0])
	if got := chunk.Get("usage.prompt_tokens_details.cached_tokens").Int(); got != 64 {
		t.Fatalf("cached_tokens mismatch: got %d, want %d", got, 64)
	}
	if got := chunk.Get("usage.completion_tokens_details.reasoning_tokens").Int(); got != 7 {
		t.Fatalf("reasoning_tokens mismatch: got %d, want %d", got, 7)
	}
}

func TestConvertCodexResponseToOpenAINonStreamIncludesCachedTokens(t *testing.T) {
	raw := []byte(`{"type":"response.completed","response":{"id":"resp_2","created_at":1700000001,"model":"gpt-5.2-codex","status":"completed","usage":{"input_tokens":88,"output_tokens":12,"total_tokens":100,"input_tokens_details":{"cached_tokens":33}},"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}}`)

	out := ConvertCodexResponseToOpenAINonStream(context.Background(), "gpt-5.2-codex", nil, nil, raw, nil)
	if out == "" {
		t.Fatalf("expected non-empty response")
	}

	resp := gjson.Parse(out)
	if got := resp.Get("usage.prompt_tokens_details.cached_tokens").Int(); got != 33 {
		t.Fatalf("cached_tokens mismatch: got %d, want %d", got, 33)
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
