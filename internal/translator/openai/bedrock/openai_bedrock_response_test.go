package bedrock

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertBedrockResponseToOpenAINonStream_UsesModelNameFallback(t *testing.T) {
	raw := []byte(`{
		"output":{"message":{"content":[{"text":"ok"}]}},
		"stopReason":"end_turn",
		"usage":{"inputTokens":1,"outputTokens":2}
	}`)

	out := ConvertBedrockResponseToOpenAINonStream(context.Background(), "deepseek-r1", nil, nil, raw, nil)
	if got := gjson.GetBytes(out, "model").String(); got != "deepseek-r1" {
		t.Fatalf("model = %q, want %q, body=%s", got, "deepseek-r1", string(out))
	}
}

func TestConvertBedrockResponseToOpenAI_StreamUsesModelNameFallback(t *testing.T) {
	var param any
	chunks := ConvertBedrockResponseToOpenAI(context.Background(), "deepseek-r1", nil, nil, []byte(`{"type":"messageStart","p":"msg_1"}`), &param)
	if len(chunks) == 0 {
		t.Fatal("expected non-empty chunks")
	}
	got := string(chunks[0])
	if !strings.Contains(got, `"model":"deepseek-r1"`) {
		t.Fatalf("expected model fallback in stream chunk, got: %s", got)
	}
}

func TestExtractBedrockReasoningDelta(t *testing.T) {
	delta := gjson.Parse(`{"reasoningContent":{"reasoning":"think"}}`)
	got, ok := extractBedrockReasoningDelta(delta)
	if !ok {
		t.Fatal("expected reasoning delta to be detected")
	}
	if got != "think" {
		t.Fatalf("reasoning delta = %q, want %q", got, "think")
	}
}
