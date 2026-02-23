package claude

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertCodexResponseToClaude(t *testing.T) {
	ctx := context.Background()
	var param any

	// response.created
	raw := []byte(`data: {"type": "response.created", "response": {"id": "resp_123", "model": "gpt-4o"}}`)
	got := ConvertCodexResponseToClaude(ctx, "claude-3", nil, nil, raw, &param)
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(got))
	}
	if !strings.Contains(got[0], `"id":"resp_123"`) {
		t.Errorf("unexpected output: %s", got[0])
	}

	// response.output_text.delta
	raw = []byte(`data: {"type": "response.output_text.delta", "delta": "hello"}`)
	got = ConvertCodexResponseToClaude(ctx, "claude-3", nil, nil, raw, &param)
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(got))
	}
	if !strings.Contains(got[0], `"text":"hello"`) {
		t.Errorf("unexpected output: %s", got[0])
	}
}

func TestConvertCodexResponseToClaudeNonStream(t *testing.T) {
	raw := []byte(`{"type": "response.completed", "response": {
		"id": "resp_123",
		"model": "gpt-4o",
		"output": [
			{"type": "message", "content": [
				{"type": "output_text", "text": "hello"}
			]}
		],
		"usage": {"input_tokens": 10, "output_tokens": 5}
	}}`)

	got := ConvertCodexResponseToClaudeNonStream(context.Background(), "claude-3", nil, nil, raw, nil)
	res := gjson.Parse(got)
	if res.Get("id").String() != "resp_123" {
		t.Errorf("expected id resp_123, got %s", res.Get("id").String())
	}
	if res.Get("content.0.text").String() != "hello" {
		t.Errorf("unexpected content: %s", got)
	}
}

func TestConvertCodexResponseToClaude_FunctionCallArgumentsDone(t *testing.T) {
	ctx := context.Background()
	var param any

	raw := []byte(`data: {"type":"response.function_call_arguments.done","arguments":"{\"x\":1}","output_index":0}`)
	got := ConvertCodexResponseToClaude(ctx, "gpt-5.3-codex", nil, nil, raw, &param)
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(got))
	}
	if !strings.Contains(got[0], `"content_block_delta"`) {
		t.Fatalf("expected content_block_delta event, got %q", got[0])
	}
	if !strings.Contains(got[0], `"input_json_delta"`) {
		t.Fatalf("expected input_json_delta event, got %q", got[0])
	}
	if !strings.Contains(got[0], `\"x\":1`) {
		t.Fatalf("expected arguments payload, got %q", got[0])
	}
}

func TestConvertCodexResponseToClaude_DeduplicatesFunctionCallArgumentsDoneWhenDeltaReceived(t *testing.T) {
	ctx := context.Background()
	var param any

	doneRaw := []byte(`data: {"type":"response.function_call_arguments.done","arguments":"{\"x\":1}","output_index":0}`)

	// Send delta first to set HasReceivedArgumentsDelta=true.
	deltaRaw := []byte(`data: {"type":"response.function_call_arguments.delta","delta":"{\"x\":","output_index":0}`)
	gotDelta := ConvertCodexResponseToClaude(ctx, "gpt-5.3-codex", nil, nil, deltaRaw, &param)
	if len(gotDelta) != 1 {
		t.Fatalf("expected 1 chunk for delta, got %d", len(gotDelta))
	}

	gotDone := ConvertCodexResponseToClaude(ctx, "gpt-5.3-codex", nil, nil, doneRaw, &param)
	if len(gotDone) != 1 || gotDone[0] != "" {
		t.Fatalf("expected empty chunk for done event when delta already received, got len=%d, chunk=%q", len(gotDone), gotDone)
	}
}
