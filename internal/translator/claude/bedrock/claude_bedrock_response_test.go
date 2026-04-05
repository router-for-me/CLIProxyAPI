package bedrock

import (
	"context"
	"strings"
	"testing"
)

func joinSSEChunks(chunks [][]byte) string {
	var b strings.Builder
	for _, c := range chunks {
		b.Write(c)
	}
	return b.String()
}

func translateBedrockEvent(t *testing.T, param *any, event string) string {
	t.Helper()
	out := ConvertBedrockStreamResponseToClaude(context.Background(), "bedrock-test-model", nil, nil, []byte(event), param)
	return joinSSEChunks(out)
}

func TestConvertBedrockStreamResponseToClaude_AutoStartsTextBlockOnDelta(t *testing.T) {
	var param any

	_ = translateBedrockEvent(t, &param, `{"type":"messageStart","p":"msg_test"}`)
	got := translateBedrockEvent(t, &param, `{"type":"contentBlockDelta","contentBlockIndex":0,"delta":{"text":"hello from bedrock"}}`)

	if !strings.Contains(got, `event: content_block_start`) {
		t.Fatalf("expected content_block_start, got: %s", got)
	}
	if !strings.Contains(got, `"type":"content_block_start","index":0`) {
		t.Fatalf("expected text start with index 0, got: %s", got)
	}
	if !strings.Contains(got, `"type":"content_block_delta","index":0`) {
		t.Fatalf("expected text delta with index 0, got: %s", got)
	}
	if strings.Contains(got, `"index":-1`) {
		t.Fatalf("unexpected negative index: %s", got)
	}

	stop := translateBedrockEvent(t, &param, `{"type":"contentBlockStop","contentBlockIndex":0}`)
	if !strings.Contains(stop, `"type":"content_block_stop","index":0`) {
		t.Fatalf("expected content_block_stop index 0, got: %s", stop)
	}
}

func TestConvertBedrockStreamResponseToClaude_ReasoningThenTextWithoutStarts(t *testing.T) {
	var param any

	_ = translateBedrockEvent(t, &param, `{"type":"messageStart","p":"msg_test_2"}`)

	reasoning := translateBedrockEvent(t, &param, `{"type":"contentBlockDelta","contentBlockIndex":0,"delta":{"reasoningContent":{"text":"think first"}}}`)
	if !strings.Contains(reasoning, `"type":"content_block_start","index":0,"content_block":{"type":"thinking"`) {
		t.Fatalf("expected thinking block start index 0, got: %s", reasoning)
	}
	if !strings.Contains(reasoning, `"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"think first"}}`) {
		t.Fatalf("expected thinking delta index 0, got: %s", reasoning)
	}

	text := translateBedrockEvent(t, &param, `{"type":"contentBlockDelta","contentBlockIndex":1,"delta":{"text":"then answer"}}`)
	if !strings.Contains(text, `"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}`) {
		t.Fatalf("expected text block start index 1, got: %s", text)
	}
	if !strings.Contains(text, `"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"then answer"}}`) {
		t.Fatalf("expected text delta index 1, got: %s", text)
	}

	// No contentBlockStop from upstream: translator should close open blocks on messageStop.
	final := translateBedrockEvent(t, &param, `{"type":"messageStop","stopReason":"end_turn"}`)
	if !strings.Contains(final, `"type":"content_block_stop","index":0`) {
		t.Fatalf("expected reasoning block stop on messageStop, got: %s", final)
	}
	if !strings.Contains(final, `"type":"content_block_stop","index":1`) {
		t.Fatalf("expected text block stop on messageStop, got: %s", final)
	}
	if !strings.Contains(final, `event: message_delta`) || !strings.Contains(final, `event: message_stop`) {
		t.Fatalf("expected final message events, got: %s", final)
	}
	if strings.Contains(final, `"index":-1`) {
		t.Fatalf("unexpected negative index in final output: %s", final)
	}
}

func TestConvertBedrockStreamResponseToClaude_ToolDeltaWithoutStartUsesFallbackToolMetadata(t *testing.T) {
	var param any

	_ = translateBedrockEvent(t, &param, `{"type":"messageStart","p":"msg_tool"}`)
	got := translateBedrockEvent(t, &param, `{"type":"contentBlockDelta","contentBlockIndex":3,"delta":{"toolUse":{"input":"{\"k\":\"v\"}"}}}`)

	if !strings.Contains(got, `"type":"content_block_start","index":0,"content_block":{"type":"tool_use"`) {
		t.Fatalf("expected tool_use block start, got: %s", got)
	}
	if !strings.Contains(got, `"id":"toolu_bedrock_3"`) {
		t.Fatalf("expected fallback tool id, got: %s", got)
	}
	if !strings.Contains(got, `"name":"bedrock_tool"`) {
		t.Fatalf("expected fallback tool name, got: %s", got)
	}
	if !strings.Contains(got, `"type":"input_json_delta","partial_json":"{\"k\":\"v\"}"`) {
		t.Fatalf("expected tool input delta, got: %s", got)
	}
	if strings.Contains(got, `"index":-1`) {
		t.Fatalf("unexpected negative index: %s", got)
	}
}

func TestConvertBedrockStreamResponseToClaude_ToolStartUsesStartDotToolUseNameAndID(t *testing.T) {
	var param any

	_ = translateBedrockEvent(t, &param, `{"type":"messageStart","p":"msg_tool_start"}`)
	start := translateBedrockEvent(t, &param, `{"type":"contentBlockStart","contentBlockIndex":2,"start":{"toolUse":{"name":"mcp__acp__Read","toolUseId":"tooluse_real_123","type":"tool_use"}}}`)

	if !strings.Contains(start, `"type":"content_block_start","index":0,"content_block":{"type":"tool_use"`) {
		t.Fatalf("expected tool_use content_block_start, got: %s", start)
	}
	if !strings.Contains(start, `"id":"tooluse_real_123"`) {
		t.Fatalf("expected real toolUseId to be preserved, got: %s", start)
	}
	if !strings.Contains(start, `"name":"mcp__acp__Read"`) {
		t.Fatalf("expected real tool name to be preserved, got: %s", start)
	}
	if strings.Contains(start, `"name":"bedrock_tool"`) {
		t.Fatalf("unexpected fallback tool name, got: %s", start)
	}

	delta := translateBedrockEvent(t, &param, `{"type":"contentBlockDelta","contentBlockIndex":2,"delta":{"toolUse":{"input":"{\"file_path\":\"/tmp/a\"}"}}}`)
	if !strings.Contains(delta, `"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"file_path\":\"/tmp/a\"}"}`) {
		t.Fatalf("expected tool input delta on same index, got: %s", delta)
	}
	if strings.Contains(delta, `"index":-1`) {
		t.Fatalf("unexpected negative index: %s", delta)
	}
}

func TestConvertBedrockResponseToClaude_PreservesThinkingWhenSameAsText(t *testing.T) {
	raw := []byte(`{
		"output": {
			"message": {
				"role": "assistant",
				"content": [
					{"text":"\n\nhello world"},
					{"reasoningContent":{"reasoningText":{"text":"hello world\n"}}}
				]
			}
		},
		"stopReason": "end_turn",
		"usage": {"inputTokens": 3, "outputTokens": 5}
	}`)

	got := string(ConvertBedrockResponseToClaude(context.Background(), "deepseek-r1", nil, nil, raw, nil))

	if strings.Count(got, `"type":"text"`) != 1 {
		t.Fatalf("expected exactly one text block, got: %s", got)
	}
	if !strings.Contains(got, `"type":"thinking","thinking":"hello world\n"`) {
		t.Fatalf("expected thinking block to be preserved, got: %s", got)
	}
}

func TestConvertBedrockResponseToClaude_KeepsThinkingWhenDifferentFromText(t *testing.T) {
	raw := []byte(`{
		"output": {
			"message": {
				"role": "assistant",
				"content": [
					{"text":"final answer"},
					{"reasoningContent":{"reasoningText":{"text":"internal reasoning"}}}
				]
			}
		},
		"stopReason": "end_turn",
		"usage": {"inputTokens": 3, "outputTokens": 5}
	}`)

	got := string(ConvertBedrockResponseToClaude(context.Background(), "deepseek-r1", nil, nil, raw, nil))

	if !strings.Contains(got, `"type":"text","text":"final answer"`) {
		t.Fatalf("expected text block to be present, got: %s", got)
	}
	if !strings.Contains(got, `"type":"thinking","thinking":"internal reasoning"`) {
		t.Fatalf("expected thinking block to be present, got: %s", got)
	}
}
