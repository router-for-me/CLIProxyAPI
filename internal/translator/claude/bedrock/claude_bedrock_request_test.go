package bedrock

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertClaudeRequestToBedrock_DeepSeekOmitsThinkingFields(t *testing.T) {
	in := []byte(`{
		"max_tokens":3000,
		"thinking":{"type":"enabled","budget_tokens":2000},
		"messages":[{"role":"user","content":"hello"}]
	}`)

	out := ConvertClaudeRequestToBedrock("us.deepseek.r1-v1:0", in, false)

	if gjson.GetBytes(out, "additionalModelRequestFields.thinking").Exists() {
		t.Fatalf("deepseek request should not include additionalModelRequestFields.thinking: %s", string(out))
	}
	if gjson.GetBytes(out, "additionalModelRequestFields.reasoningConfig").Exists() {
		t.Fatalf("deepseek request should not include additionalModelRequestFields.reasoningConfig: %s", string(out))
	}
}

func TestConvertClaudeRequestToBedrock_ClaudeKeepsThinking(t *testing.T) {
	in := []byte(`{
		"max_tokens":3000,
		"thinking":{"type":"enabled","budget_tokens":2000},
		"messages":[{"role":"user","content":"hello"}]
	}`)

	out := ConvertClaudeRequestToBedrock("anthropic.claude-sonnet-4-20250514-v1:0", in, false)

	thinkingType := gjson.GetBytes(out, "additionalModelRequestFields.thinking.type").String()
	if thinkingType != "enabled" {
		t.Fatalf("expected claude thinking.type=enabled, got %q, body=%s", thinkingType, string(out))
	}
}

func TestConvertClaudeRequestToBedrock_TranslateModelHintKeepsThinkingForClaude(t *testing.T) {
	in := []byte(`{
		"max_tokens":3000,
		"thinking":{"type":"enabled","budget_tokens":2000},
		"messages":[{"role":"user","content":"hello"}]
	}`)

	out := ConvertClaudeRequestToBedrock("claude", in, false)

	thinkingType := gjson.GetBytes(out, "additionalModelRequestFields.thinking.type").String()
	if thinkingType != "enabled" {
		t.Fatalf("expected claude thinking.type=enabled, got %q, body=%s", thinkingType, string(out))
	}
}

func TestConvertClaudeRequestToBedrock_InferenceProfileRouteOmitsThinkingWithoutClaudeHint(t *testing.T) {
	in := []byte(`{
		"max_tokens":3000,
		"thinking":{"type":"enabled","budget_tokens":2000},
		"messages":[{"role":"user","content":"hello"}]
	}`)

	out := ConvertClaudeRequestToBedrock("arn:aws:bedrock:us-west-2:123456789012:application-inference-profile/egz01y3lkius", in, false)

	if gjson.GetBytes(out, "additionalModelRequestFields.thinking").Exists() {
		t.Fatalf("non-claude inference-profile request should not include thinking: %s", string(out))
	}
}

func TestConvertClaudeRequestToBedrock_GLMUsesReasoningConfig(t *testing.T) {
	in := []byte(`{
		"max_tokens":3000,
		"thinking":{"type":"enabled","budget_tokens":20000},
		"messages":[{"role":"user","content":"hello"}]
	}`)

	out := ConvertClaudeRequestToBedrock("zai.glm-5", in, false)

	if gjson.GetBytes(out, "additionalModelRequestFields.thinking").Exists() {
		t.Fatalf("glm request should not include additionalModelRequestFields.thinking: %s", string(out))
	}
	if got := gjson.GetBytes(out, "additionalModelRequestFields.reasoningConfig.type").String(); got != "enabled" {
		t.Fatalf("expected reasoningConfig.type=enabled, got %q, body=%s", got, string(out))
	}
	if got := gjson.GetBytes(out, "additionalModelRequestFields.reasoningConfig.maxReasoningEffort").String(); got != "high" {
		t.Fatalf("expected maxReasoningEffort=high, got %q, body=%s", got, string(out))
	}
}

func TestConvertClaudeRequestToBedrock_ReordersTextBetweenToolUse(t *testing.T) {
	// Bedrock Converse requires tool_use blocks to be consecutive in assistant
	// messages. A text block between tool_use blocks causes ValidationException.
	in := []byte(`{
		"max_tokens":3000,
		"messages":[
			{"role":"user","content":"hi"},
			{"role":"assistant","content":[
				{"type":"tool_use","id":"tu1","name":"Read","input":{}},
				{"type":"text","text":"interleaved text"},
				{"type":"tool_use","id":"tu2","name":"Write","input":{}}
			]}
		]
	}`)

	out := ConvertClaudeRequestToBedrock("anthropic.claude-sonnet-4-20250514-v1:0", in, false)

	// Verify message order: text first, then toolUse
	msg1Content := gjson.GetBytes(out, "messages.1.content").Array()
	if len(msg1Content) != 3 {
		t.Fatalf("expected 3 content blocks, got %d: %s", len(msg1Content), string(out))
	}
	// First block should be text
	if gjson.GetBytes(out, "messages.1.content.0.text").String() != "interleaved text" {
		t.Fatalf("first block should be text, got: %s", string(out))
	}
	// Second and third should be toolUse
	if !gjson.GetBytes(out, "messages.1.content.1.toolUse").Exists() {
		t.Fatalf("second block should be toolUse, got: %s", string(out))
	}
	if !gjson.GetBytes(out, "messages.1.content.2.toolUse").Exists() {
		t.Fatalf("third block should be toolUse, got: %s", string(out))
	}
}

func TestReorderBedrockAssistantBlocks_NoOpWhenAlreadyOrdered(t *testing.T) {
	in := []byte(`[{"text":"hello"},{"toolUse":{"toolUseId":"1","name":"Read","input":{}}}]`)
	out := reorderBedrockAssistantBlocks(in)
	if string(out) != string(in) {
		t.Fatalf("expected no change, got: %s", string(out))
	}
}

func TestReorderBedrockAssistantBlocks_NoOpForTextOnly(t *testing.T) {
	in := []byte(`[{"text":"hello"},{"text":"world"}]`)
	out := reorderBedrockAssistantBlocks(in)
	if string(out) != string(in) {
		t.Fatalf("expected no change, got: %s", string(out))
	}
}

func TestReorderBedrockAssistantBlocks_NoOpForToolUseOnly(t *testing.T) {
	in := []byte(`[{"toolUse":{"toolUseId":"1","name":"Read","input":{}}},{"toolUse":{"toolUseId":"2","name":"Write","input":{}}}]`)
	out := reorderBedrockAssistantBlocks(in)
	if string(out) != string(in) {
		t.Fatalf("expected no change, got: %s", string(out))
	}
}

