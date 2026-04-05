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

