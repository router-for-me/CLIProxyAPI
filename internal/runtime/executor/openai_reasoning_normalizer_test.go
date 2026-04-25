package executor

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestNormalizeAssistantToolCallReasoningContent_PatchesWhenThinkingEnabled(t *testing.T) {
	body := []byte(`{
		"model":"test-model",
		"reasoning_effort":"high",
		"messages":[
			{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{}"}}]}
		]
	}`)

	out, patched, err := normalizeAssistantToolCallReasoningContent(body, true)
	if err != nil {
		t.Fatalf("normalizeAssistantToolCallReasoningContent() error = %v", err)
	}
	if patched != 1 {
		t.Fatalf("patched = %d, want 1", patched)
	}
	got := gjson.GetBytes(out, "messages.0.reasoning_content")
	if !got.Exists() {
		t.Fatal("messages.0.reasoning_content should exist")
	}
	if strings.TrimSpace(got.String()) == "" {
		t.Fatalf("messages.0.reasoning_content should be non-empty, got %q", got.String())
	}
}

func TestNormalizeAssistantToolCallReasoningContent_SkipsWhenThinkingDisabled(t *testing.T) {
	body := []byte(`{
		"model":"test-model",
		"messages":[
			{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{}"}}]}
		]
	}`)

	out, patched, err := normalizeAssistantToolCallReasoningContent(body, true)
	if err != nil {
		t.Fatalf("normalizeAssistantToolCallReasoningContent() error = %v", err)
	}
	if patched != 0 {
		t.Fatalf("patched = %d, want 0", patched)
	}
	if gjson.GetBytes(out, "messages.0.reasoning_content").Exists() {
		t.Fatalf("messages.0.reasoning_content should not be injected, got %q", gjson.GetBytes(out, "messages.0.reasoning_content").String())
	}
}

func TestNormalizeAssistantToolCallReasoningContent_UsesLatestReasoning(t *testing.T) {
	body := []byte(`{
		"model":"test-model",
		"reasoning":{"effort":"high"},
		"messages":[
			{"role":"assistant","content":"plan","reasoning_content":"r1"},
			{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{}"}}]}
		]
	}`)

	out, patched, err := normalizeAssistantToolCallReasoningContent(body, true)
	if err != nil {
		t.Fatalf("normalizeAssistantToolCallReasoningContent() error = %v", err)
	}
	if patched != 1 {
		t.Fatalf("patched = %d, want 1", patched)
	}
	if got := gjson.GetBytes(out, "messages.1.reasoning_content").String(); got != "r1" {
		t.Fatalf("messages.1.reasoning_content = %q, want %q", got, "r1")
	}
}
