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

func TestConvertBedrockResponseToOpenAI_StreamDefersDoneUntilMetadata(t *testing.T) {
	var param any

	_ = ConvertBedrockResponseToOpenAI(context.Background(), "deepseek-r1", nil, nil, []byte(`{"type":"messageStart","p":"msg_1"}`), &param)
	stopChunks := ConvertBedrockResponseToOpenAI(context.Background(), "deepseek-r1", nil, nil, []byte(`{"type":"messageStop","stopReason":"end_turn"}`), &param)
	stopCombined := strings.Join(byteSlicesToStrings(stopChunks), "")
	if strings.Contains(stopCombined, "data: [DONE]") {
		t.Fatalf("expected no [DONE] on messageStop before metadata, got: %s", stopCombined)
	}

	metaChunks := ConvertBedrockResponseToOpenAI(context.Background(), "deepseek-r1", nil, nil, []byte(`{"type":"metadata","usage":{"inputTokens":11,"outputTokens":7}}`), &param)
	metaCombined := strings.Join(byteSlicesToStrings(metaChunks), "")
	if !strings.Contains(metaCombined, `"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}`) {
		t.Fatalf("expected usage chunk in metadata output, got: %s", metaCombined)
	}
	if !strings.Contains(metaCombined, "data: [DONE]") {
		t.Fatalf("expected [DONE] after metadata, got: %s", metaCombined)
	}
}

func byteSlicesToStrings(chunks [][]byte) []string {
	out := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		out = append(out, string(chunk))
	}
	return out
}

func TestConvertBedrockResponseToOpenAI_ContentBlockStartReadsStartToolUse(t *testing.T) {
	var param any

	_ = ConvertBedrockResponseToOpenAI(context.Background(), "deepseek-r1", nil, nil, []byte(`{"type":"messageStart","p":"msg_2"}`), &param)
	chunks := ConvertBedrockResponseToOpenAI(
		context.Background(),
		"deepseek-r1",
		nil,
		nil,
		[]byte(`{"type":"contentBlockStart","contentBlockIndex":2,"start":{"toolUse":{"toolUseId":"tooluse_123","name":"mcp__acp__Read"}}}`),
		&param,
	)
	if len(chunks) == 0 {
		t.Fatal("expected non-empty tool start chunks")
	}
	got := strings.Join(byteSlicesToStrings(chunks), "")
	if !strings.Contains(got, `"tool_calls":[{"index":0,"id":"tooluse_123","type":"function","function":{"name":"mcp__acp__Read","arguments":""}}]`) {
		t.Fatalf("expected tool call start delta with id/name, got: %s", got)
	}
}

func TestConvertBedrockResponseToOpenAIResponsesNonStream_BridgesToResponsesSchema(t *testing.T) {
	raw := []byte(`{
		"output":{"message":{"content":[{"text":"ok"}]}},
		"stopReason":"end_turn",
		"usage":{"inputTokens":3,"outputTokens":5}
	}`)

	out := ConvertBedrockResponseToOpenAIResponsesNonStream(context.Background(), "deepseek-v3.2", nil, nil, raw, nil)
	if !gjson.ValidBytes(out) {
		t.Fatalf("output is not valid json: %s", string(out))
	}
	if got := gjson.GetBytes(out, "object").String(); got != "response" {
		t.Fatalf("object = %q, want %q; body=%s", got, "response", string(out))
	}
	if got := gjson.GetBytes(out, "output.0.type").String(); got == "" {
		t.Fatalf("expected response output items, body=%s", string(out))
	}
}

func TestConvertBedrockResponseToOpenAIResponsesStream_BridgesThroughChatChunks(t *testing.T) {
	var param any

	chunks := ConvertBedrockResponseToOpenAIResponsesStream(
		context.Background(),
		"deepseek-v3.2",
		nil,
		nil,
		[]byte(`{"type":"messageStart","p":"msg_bridge"}`),
		&param,
	)
	if len(chunks) == 0 {
		t.Fatal("expected non-empty response stream chunks")
	}
	joined := strings.Join(byteSlicesToStrings(chunks), "")
	if !strings.Contains(joined, "event: response.created") {
		t.Fatalf("expected OpenAI Responses SSE event, got: %s", joined)
	}
}
