package executor

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestPreserveReasoningContent_PreservesEmptyStringReasoning(t *testing.T) {
	original := []byte(`{
		"messages":[
			{"role":"user","content":"hello"},
			{"role":"assistant","content":"answer","reasoning_content":""},
			{"role":"user","content":"follow up"}
		]
	}`)
	translated := []byte(`{
		"messages":[
			{"role":"user","content":"hello"},
			{"role":"assistant","content":"answer"},
			{"role":"user","content":"follow up"}
		]
	}`)

	out, err := preserveReasoningContent(original, translated)
	if err != nil {
		t.Fatalf("preserveReasoningContent() error = %v", err)
	}

	rc := gjson.GetBytes(out, "messages.1.reasoning_content")
	if !rc.Exists() {
		t.Fatalf("messages.1.reasoning_content should exist (even if empty)")
	}
	if rc.String() != "" {
		t.Fatalf("messages.1.reasoning_content = %q, want empty string", rc.String())
	}
}

func TestPreserveReasoningContent_DoesNotInheritReasoningForMissingMessages(t *testing.T) {
	// Multi-turn tool call chain: assistant with reasoning → tool → assistant without reasoning.
	// The second assistant originally had no reasoning_content, so it must not get one fabricated.
	original := []byte(`{
		"messages":[
			{"role":"user","content":"list files"},
			{"role":"assistant","content":"I'll list the files","reasoning_content":"let me check the directory"},
			{"role":"tool","tool_call_id":"call_1","content":"[file1.txt, file2.txt]"},
			{"role":"assistant","content":"Here are the files"}
		]
	}`)
	translated := []byte(`{
		"messages":[
			{"role":"user","content":"list files"},
			{"role":"assistant","content":"I'll list the files"},
			{"role":"tool","tool_call_id":"call_1","content":"[file1.txt, file2.txt]"},
			{"role":"assistant","content":"Here are the files"}
		]
	}`)

	out, err := preserveReasoningContent(original, translated)
	if err != nil {
		t.Fatalf("preserveReasoningContent() error = %v", err)
	}

	// First assistant (index 1) should get the original reasoning
	rc1 := gjson.GetBytes(out, "messages.1.reasoning_content").String()
	if rc1 != "let me check the directory" {
		t.Fatalf("messages.1.reasoning_content = %q, want %q", rc1, "let me check the directory")
	}

	// Second assistant (index 3) originally had no reasoning — must remain absent
	if gjson.GetBytes(out, "messages.3.reasoning_content").Exists() {
		t.Fatalf("messages.3.reasoning_content should not exist when original had none")
	}
}

func TestPreserveReasoningContent_NoOpWhenNoOriginalReasoning(t *testing.T) {
	original := []byte(`{
		"messages":[
			{"role":"user","content":"hello"},
			{"role":"assistant","content":"answer"}
		]
	}`)
	translated := []byte(`{
		"messages":[
			{"role":"user","content":"hello"},
			{"role":"assistant","content":"answer"}
		]
	}`)

	out, err := preserveReasoningContent(original, translated)
	if err != nil {
		t.Fatalf("preserveReasoningContent() error = %v", err)
	}

	if gjson.GetBytes(out, "messages.1.reasoning_content").Exists() {
		t.Fatalf("messages.1.reasoning_content should not exist when original has none")
	}
}

func TestPreserveReasoningContent_IgnoresNonAssistantMessages(t *testing.T) {
	original := []byte(`{
		"messages":[
			{"role":"system","content":"you are helpful"},
			{"role":"user","content":"hello"},
			{"role":"assistant","content":"answer","reasoning_content":"thinking..."},
			{"role":"tool","tool_call_id":"call_1","content":"data"}
		]
	}`)
	translated := []byte(`{
		"messages":[
			{"role":"system","content":"you are helpful"},
			{"role":"user","content":"hello"},
			{"role":"assistant","content":"answer"},
			{"role":"tool","tool_call_id":"call_1","content":"data"}
		]
	}`)

	out, err := preserveReasoningContent(original, translated)
	if err != nil {
		t.Fatalf("preserveReasoningContent() error = %v", err)
	}

	// Only assistant at index 2 should be affected
	if gjson.GetBytes(out, "messages.0.reasoning_content").Exists() {
		t.Fatalf("system message should not get reasoning_content")
	}
	if gjson.GetBytes(out, "messages.1.reasoning_content").Exists() {
		t.Fatalf("user message should not get reasoning_content")
	}
	if !gjson.GetBytes(out, "messages.2.reasoning_content").Exists() {
		t.Fatalf("assistant message should have reasoning_content preserved")
	}
	if gjson.GetBytes(out, "messages.3.reasoning_content").Exists() {
		t.Fatalf("tool message should not get reasoning_content")
	}
}

func TestPreserveReasoningContent_KeepsExistingNonEmptyReasoning(t *testing.T) {
	original := []byte(`{
		"messages":[
			{"role":"user","content":"hello"},
			{"role":"assistant","content":"answer","reasoning_content":"let me think..."}
		]
	}`)
	translated := []byte(`{
		"messages":[
			{"role":"user","content":"hello"},
			{"role":"assistant","content":"answer"}
		]
	}`)

	out, err := preserveReasoningContent(original, translated)
	if err != nil {
		t.Fatalf("preserveReasoningContent() error = %v", err)
	}

	got := gjson.GetBytes(out, "messages.1.reasoning_content").String()
	if got != "let me think..." {
		t.Fatalf("messages.1.reasoning_content = %q, want %q", got, "let me think...")
	}
}

func TestPreserveReasoningContent_KeepsTranslatedReasoningWhenOriginalLacksIt(t *testing.T) {
	// Original has no reasoning_content, but translated already has one — keep it.
	original := []byte(`{
		"messages":[
			{"role":"user","content":"hello"},
			{"role":"assistant","content":"answer"}
		]
	}`)
	translated := []byte(`{
		"messages":[
			{"role":"user","content":"hello"},
			{"role":"assistant","content":"answer","reasoning_content":"from upstream"}
		]
	}`)

	out, err := preserveReasoningContent(original, translated)
	if err != nil {
		t.Fatalf("preserveReasoningContent() error = %v", err)
	}

	got := gjson.GetBytes(out, "messages.1.reasoning_content").String()
	if got != "from upstream" {
		t.Fatalf("messages.1.reasoning_content = %q, want %q", got, "from upstream")
	}
}

func TestPreserveReasoningContent_SkipsWhenMessageCountMismatch(t *testing.T) {
	// Claude→OpenAI translation can merge content blocks, changing message count.
	// In this case the function should skip to avoid incorrect index-based matching.
	original := []byte(`{
		"messages":[
			{"role":"user","content":"hello"},
			{"role":"assistant","content":"answer","reasoning_content":"thinking..."}
		]
	}`)
	translated := []byte(`{
		"messages":[
			{"role":"system","content":"you are helpful"},
			{"role":"user","content":"hello"},
			{"role":"assistant","content":"answer"}
		]
	}`)

	out, err := preserveReasoningContent(original, translated)
	if err != nil {
		t.Fatalf("preserveReasoningContent() error = %v", err)
	}

	// Should not inject reasoning into a wrong index
	if gjson.GetBytes(out, "messages.1.reasoning_content").Exists() {
		t.Fatalf("user message at index 1 should not get reasoning_content from mismatched index")
	}
	// Translated assistant (index 2) should not get original's reasoning (index 1)
	if gjson.GetBytes(out, "messages.2.reasoning_content").Exists() {
		t.Fatalf("assistant should not get reasoning from mismatched index")
	}
}
