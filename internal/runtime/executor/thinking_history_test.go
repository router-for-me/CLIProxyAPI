package executor

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestNormalizeOpenAIThinkingHistoryRepairsFromPreviousReasoning(t *testing.T) {
	body := []byte(`{
		"reasoning_effort":"high",
		"messages":[
			{"role":"assistant","content":"plan","reasoning_content":"r1"},
			{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"list_directory","arguments":"{}"}}]}
		]
	}`)

	out, _, downgraded, err := normalizeThinkingHistory(body, "openai")
	if err != nil {
		t.Fatalf("normalizeThinkingHistory() error = %v", err)
	}
	if downgraded {
		t.Fatalf("normalizeThinkingHistory() downgraded unexpectedly")
	}
	if got := gjson.GetBytes(out, "messages.1.reasoning_content").String(); got != "r1" {
		t.Fatalf("messages.1.reasoning_content = %q, want %q", got, "r1")
	}
}

func TestNormalizeOpenAIThinkingHistoryDowngradesWhenUnrepairable(t *testing.T) {
	body := []byte(`{
		"reasoning_effort":"high",
		"messages":[
			{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"list_directory","arguments":"{}"}}]}
		]
	}`)

	out, _, downgraded, err := normalizeThinkingHistory(body, "openai")
	if err != nil {
		t.Fatalf("normalizeThinkingHistory() error = %v", err)
	}
	if !downgraded {
		t.Fatalf("normalizeThinkingHistory() should downgrade thinking")
	}
	if gjson.GetBytes(out, "reasoning_effort").Exists() {
		t.Fatalf("reasoning_effort should be removed")
	}
}

func TestNormalizeClaudeThinkingHistoryRepairsFromText(t *testing.T) {
	body := []byte(`{
		"thinking":{"type":"enabled","budget_tokens":1024},
		"messages":[
			{"role":"assistant","content":[
				{"type":"text","text":"plan"},
				{"type":"tool_use","id":"toolu_1","name":"list_directory","input":{}}
			]}
		]
	}`)

	out, _, downgraded, err := normalizeThinkingHistory(body, "claude")
	if err != nil {
		t.Fatalf("normalizeThinkingHistory() error = %v", err)
	}
	if downgraded {
		t.Fatalf("normalizeThinkingHistory() downgraded unexpectedly")
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.type").String(); got != "thinking" {
		t.Fatalf("messages.0.content.0.type = %q, want %q", got, "thinking")
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.thinking").String(); got != "plan" {
		t.Fatalf("messages.0.content.0.thinking = %q, want %q", got, "plan")
	}
}

func TestNormalizeClaudeThinkingHistoryDowngradesWhenUnrepairable(t *testing.T) {
	body := []byte(`{
		"thinking":{"type":"enabled","budget_tokens":1024},
		"messages":[
			{"role":"assistant","content":[
				{"type":"tool_use","id":"toolu_1","name":"list_directory","input":{}}
			]}
		]
	}`)

	out, _, downgraded, err := normalizeThinkingHistory(body, "claude")
	if err != nil {
		t.Fatalf("normalizeThinkingHistory() error = %v", err)
	}
	if !downgraded {
		t.Fatalf("normalizeThinkingHistory() should downgrade thinking")
	}
	if gjson.GetBytes(out, "thinking").Exists() {
		t.Fatalf("thinking should be removed")
	}
}
