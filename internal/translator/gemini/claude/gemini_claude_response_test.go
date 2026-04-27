package claude

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertGeminiResponseToClaudeStreamReportsCacheReadTokens(t *testing.T) {
	var param any
	ctx := context.WithValue(context.Background(), "cliproxy:upstream_provider", "vertex")
	chunks := ConvertGeminiResponseToClaude(
		ctx,
		"gemini-3-flash-preview",
		[]byte(`{"messages":[{"role":"user","content":"hi"}]}`),
		nil,
		[]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":105,"candidatesTokenCount":7,"totalTokenCount":112,"cachedContentTokenCount":100},"modelVersion":"gemini-3-flash-preview","responseId":"resp-1"}`),
		&param,
	)
	out := string(chunks[0])
	if !strings.Contains(out, `"cache_read_input_tokens":100`) {
		t.Fatalf("cache_read_input_tokens missing: %s", out)
	}
	if !strings.Contains(out, `"input_tokens":5`) {
		t.Fatalf("input_tokens should exclude cached tokens: %s", out)
	}
}

func TestConvertGeminiResponseToClaudeNonStreamReportsCacheReadTokens(t *testing.T) {
	ctx := context.WithValue(context.Background(), "cliproxy:upstream_provider", "vertex")
	out := ConvertGeminiResponseToClaudeNonStream(
		ctx,
		"gemini-3-flash-preview",
		[]byte(`{"messages":[{"role":"user","content":"hi"}]}`),
		nil,
		[]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":105,"candidatesTokenCount":7,"totalTokenCount":112,"cachedContentTokenCount":100},"modelVersion":"gemini-3-flash-preview","responseId":"resp-1"}`),
		nil,
	)
	usage := gjson.GetBytes(out, "usage")
	if got := usage.Get("cache_read_input_tokens").Int(); got != 100 {
		t.Fatalf("cache_read_input_tokens = %d, want 100; output=%s", got, string(out))
	}
	if got := usage.Get("input_tokens").Int(); got != 5 {
		t.Fatalf("input_tokens = %d, want 5; output=%s", got, string(out))
	}
}

func TestConvertGeminiResponseToClaudeStreamLeavesNonVertexUsageUnchanged(t *testing.T) {
	var param any
	chunks := ConvertGeminiResponseToClaude(
		context.Background(),
		"gemini-3-flash-preview",
		[]byte(`{"messages":[{"role":"user","content":"hi"}]}`),
		nil,
		[]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":105,"candidatesTokenCount":7,"totalTokenCount":112,"cachedContentTokenCount":100},"modelVersion":"gemini-3-flash-preview","responseId":"resp-1"}`),
		&param,
	)
	out := string(chunks[0])
	if strings.Contains(out, "cache_read_input_tokens") {
		t.Fatalf("non-vertex response should not include cache_read_input_tokens: %s", out)
	}
	if !strings.Contains(out, `"input_tokens":105`) {
		t.Fatalf("non-vertex input_tokens should remain promptTokenCount: %s", out)
	}
}
