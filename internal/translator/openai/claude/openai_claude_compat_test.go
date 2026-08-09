package claude

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertClaudeRequestToOpenAIWithCompatPreservesEmptySignatureThinking(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":""}]}]}`)

	withoutCompat := ConvertClaudeRequestToOpenAI("deepseek-v4", payload, false)
	if gjson.GetBytes(withoutCompat, "messages.0.reasoning_content").Exists() {
		t.Fatalf("default translation preserved empty-signature reasoning: %s", withoutCompat)
	}

	withCompat := ConvertClaudeRequestToOpenAIWithCompat("deepseek-v4", payload, false)
	if gjson.GetBytes(withCompat, "messages.0.reasoning_content").String() != "reason" {
		t.Fatalf("compat translation missing reasoning_content: %s", withCompat)
	}
}

func TestConvertClaudeRequestToOpenAIWithCompatPreservesThinkingWithToolCalls(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":""},{"type":"text","text":"Reading files."},{"type":"tool_use","id":"call_1","name":"Read","input":{"path":"main.go"}}]}]}`)

	result := ConvertClaudeRequestToOpenAIWithCompat("deepseek-v4", payload, false)
	assistant := gjson.GetBytes(result, "messages.0")
	if got := assistant.Get("reasoning_content").String(); got != "reason" {
		t.Fatalf("reasoning_content = %q, want %q; output: %s", got, "reason", result)
	}
	if !assistant.Get("tool_calls").Exists() {
		t.Fatalf("tool_calls missing from compatible translation: %s", result)
	}
}

func TestConvertClaudeRequestToOpenAIWithCompatDoesNotAddReasoningWithoutThinking(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"Read","input":{}}]}]}`)

	result := ConvertClaudeRequestToOpenAIWithCompat("deepseek-v4", payload, false)
	assistant := gjson.GetBytes(result, "messages.0")
	if assistant.Get("reasoning_content").Exists() {
		t.Fatalf("compatible translation added reasoning_content without thinking: %s", result)
	}
	if !assistant.Get("tool_calls").Exists() {
		t.Fatalf("tool_calls missing from compatible translation: %s", result)
	}
}

func TestConvertClaudeRequestToOpenAIWithCompatPreservesIncompatibleThinking(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":"claude#opaque"},{"type":"tool_use","id":"call_1","name":"Read","input":{}}]}]}`)

	result := ConvertClaudeRequestToOpenAIWithCompat("deepseek-v4", payload, false)
	assistant := gjson.GetBytes(result, "messages.0")
	if got := assistant.Get("reasoning_content").String(); got != "reason" {
		t.Fatalf("reasoning_content = %q, want %q; output: %s", got, "reason", result)
	}
	if !assistant.Get("tool_calls").Exists() {
		t.Fatalf("tool_calls missing from compatible translation: %s", result)
	}
}

func TestConvertClaudeRequestToOpenAIWithoutCompatDoesNotAddReasoningForToolCalls(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"Read","input":{}}]}]}`)

	result := ConvertClaudeRequestToOpenAI("deepseek-v4", payload, false)
	if gjson.GetBytes(result, "messages.0.reasoning_content").Exists() {
		t.Fatalf("default translation added reasoning_content: %s", result)
	}
}
