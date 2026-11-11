package claude

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestConvertClaudeRequestToOpenAIWithCompatPreservesEmptySignatureThinking(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":""}]}]}`)

	withoutCompat, _ := ConvertClaudeRequestToOpenAI("deepseek-v4", payload, false)
	if ojson.GetBytesNoCopy(withoutCompat, "messages.0.reasoning_content").Exists() {
		t.Fatalf("default translation preserved empty-signature reasoning: %s", withoutCompat)
	}

	withCompat, _ := ConvertClaudeRequestToOpenAIWithCompat("deepseek-v4", payload, false)
	if ojson.GetBytesNoCopy(withCompat, "messages.0.reasoning_content").String() != "reason" {
		t.Fatalf("compat translation missing reasoning_content: %s", withCompat)
	}
}

func TestConvertClaudeRequestToOpenAIWithCompatPreservesThinkingWithToolCalls(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":""},{"type":"text","text":"Reading files."},{"type":"tool_use","id":"call_1","name":"Read","input":{"path":"main.go"}}]}]}`)

	result, _ := ConvertClaudeRequestToOpenAIWithCompat("deepseek-v4", payload, false)
	assistant := ojson.GetBytesNoCopy(result, "messages.0")
	if got := assistant.Get("reasoning_content").String(); got != "reason" {
		t.Fatalf("reasoning_content = %q, want %q; output: %s", got, "reason", result)
	}
	if !assistant.Get("tool_calls").Exists() {
		t.Fatalf("tool_calls missing from compatible translation: %s", result)
	}
}

func TestConvertClaudeRequestToOpenAIWithCompatDoesNotAddReasoningWithoutThinking(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"Read","input":{}}]}]}`)

	result, _ := ConvertClaudeRequestToOpenAIWithCompat("deepseek-v4", payload, false)
	assistant := ojson.GetBytesNoCopy(result, "messages.0")
	if assistant.Get("reasoning_content").Exists() {
		t.Fatalf("compatible translation added reasoning_content without request thinking: %s", result)
	}
	if !assistant.Get("tool_calls").Exists() {
		t.Fatalf("tool_calls missing from compatible translation: %s", result)
	}
}

func TestConvertClaudeRequestToOpenAIWithCompatDoesNotAddReasoningWhenThinkingDisabled(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
	}{
		{
			name:    "thinking type disabled",
			payload: `{"thinking":{"type":"disabled"},"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"Read","input":{}}]}]}`,
		},
		{
			name:    "thinking budget zero",
			payload: `{"thinking":{"type":"enabled","budget_tokens":0},"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"Read","input":{}}]}]}`,
		},
		{
			name:    "output_config effort none",
			payload: `{"output_config":{"effort":"none"},"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"Read","input":{}}]}]}`,
		},
		{
			name:    "model suffix none overrides enabled body",
			payload: `{"model":"deepseek-v4(none)","thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"Read","input":{}}]}]}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, _ := ConvertClaudeRequestToOpenAIWithCompat("deepseek-v4", []byte(tc.payload), false)
			assistant := ojson.GetBytesNoCopy(result, "messages.0")
			if assistant.Get("reasoning_content").Exists() {
				t.Fatalf("compatible translation added reasoning_content when thinking disabled: %s", result)
			}
			if !assistant.Get("tool_calls").Exists() {
				t.Fatalf("tool_calls missing from compatible translation: %s", result)
			}
		})
	}
}

func TestConvertClaudeRequestToOpenAIWithCompatDoesNotAddReasoningWithoutThinkingForTextOnly(t *testing.T) {
	payload := []byte(`{"thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"assistant","content":[{"type":"text","text":"Plain answer without thinking."}]}]}`)

	result, _ := ConvertClaudeRequestToOpenAIWithCompat("deepseek-v4", payload, false)
	assistant := ojson.GetBytesNoCopy(result, "messages.0")
	if assistant.Get("reasoning_content").Exists() {
		t.Fatalf("compatible translation added reasoning_content to text-only assistant turn: %s", result)
	}
}

func TestConvertClaudeRequestToOpenAIWithCompatAddsPlaceholderReasoningForToolCallsWithoutThinking(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
	}{
		{
			name:    "enabled thinking with absent thinking block",
			payload: `{"thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"Read","input":{}}]}]}`,
		},
		{
			name:    "enabled thinking with whitespace-only thinking block",
			payload: `{"thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":" ","signature":""},{"type":"tool_use","id":"call_1","name":"Read","input":{}}]}]}`,
		},
		{
			name:    "adaptive thinking with absent thinking block",
			payload: `{"thinking":{"type":"adaptive"},"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"Read","input":{}}]}]}`,
		},
		{
			name:    "standalone output_config effort with absent thinking block",
			payload: `{"output_config":{"effort":"high"},"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"Read","input":{}}]}]}`,
		},
		{
			name:    "model suffix high overrides disabled body",
			payload: `{"model":"deepseek-v4(high)","thinking":{"type":"disabled"},"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"Read","input":{}}]}]}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, _ := ConvertClaudeRequestToOpenAIWithCompat("deepseek-v4", []byte(tc.payload), false)
			assistant := ojson.GetBytesNoCopy(result, "messages.0")
			if got := assistant.Get("reasoning_content").String(); got != " " {
				t.Fatalf("reasoning_content = %q, want 1-char placeholder; output: %s", got, result)
			}
			if !assistant.Get("tool_calls").Exists() {
				t.Fatalf("tool_calls missing from compatible translation: %s", result)
			}
		})
	}
}

func TestConvertClaudeRequestToOpenAIWithCompatPreservesIncompatibleThinking(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":"claude#opaque"},{"type":"tool_use","id":"call_1","name":"Read","input":{}}]}]}`)

	result, _ := ConvertClaudeRequestToOpenAIWithCompat("deepseek-v4", payload, false)
	assistant := ojson.GetBytesNoCopy(result, "messages.0")
	if got := assistant.Get("reasoning_content").String(); got != "reason" {
		t.Fatalf("reasoning_content = %q, want %q; output: %s", got, "reason", result)
	}
	if !assistant.Get("tool_calls").Exists() {
		t.Fatalf("tool_calls missing from compatible translation: %s", result)
	}
}

func TestConvertClaudeRequestToOpenAIWithoutCompatDoesNotAddReasoningForToolCalls(t *testing.T) {
	for _, payload := range []string{
		`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"Read","input":{}}]}]}`,
		`{"thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"Read","input":{}}]}]}`,
	} {
		result, _ := ConvertClaudeRequestToOpenAI("deepseek-v4", []byte(payload), false)
		if ojson.GetBytesNoCopy(result, "messages.0.reasoning_content").Exists() {
			t.Fatalf("default translation added reasoning_content: %s", result)
		}
	}
}
