package translator

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestTranslateRequest_DefaultPipelineNormalizesOpenAIToolCallReasoning(t *testing.T) {
	from := Format("test-openai-source")
	Register(from, FormatOpenAI, func(_ string, _ []byte, _ bool) []byte {
		return []byte(`{
			"model":"test-model",
			"reasoning_effort":"high",
			"messages":[
				{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{}"}}]}
			]
		}`)
	}, ResponseTransform{})

	out := TranslateRequest(from, FormatOpenAI, "test-model", []byte(`{}`), false)

	if got := gjson.GetBytes(out, "messages.0.reasoning_content").String(); got == "" {
		t.Fatalf("messages.0.reasoning_content should be injected, got empty payload=%s", string(out))
	}
}

func TestTranslateRequest_DefaultPipelineNormalizesClaudeToolUsePrefix(t *testing.T) {
	from := Format("test-claude-source")
	Register(from, FormatClaude, func(_ string, _ []byte, _ bool) []byte {
		return []byte(`{
			"model":"claude-test",
			"thinking":{"type":"adaptive"},
			"messages":[
				{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"Read","input":{}}]}
			]
		}`)
	}, ResponseTransform{})

	out := TranslateRequest(from, FormatClaude, "claude-test", []byte(`{}`), false)

	if got := gjson.GetBytes(out, "messages.0.content.0.text").String(); got == "" {
		t.Fatalf("messages.0.content.0.text should be injected, payload=%s", string(out))
	}
	if got := gjson.GetBytes(out, "messages.0.content.1.type").String(); got != "tool_use" {
		t.Fatalf("messages.0.content.1.type = %q, want %q", got, "tool_use")
	}
}
