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

	rc1 := gjson.GetBytes(out, "messages.1.reasoning_content").String()
	if rc1 != "let me check the directory" {
		t.Fatalf("messages.1.reasoning_content = %q, want %q", rc1, "let me check the directory")
	}

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

func TestPreserveReasoningContent_OrdinalMatchingAcrossMessageCountMismatch(t *testing.T) {
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

	rc := gjson.GetBytes(out, "messages.2.reasoning_content")
	if !rc.Exists() {
		t.Fatalf("assistant message (ordinal 0) should have reasoning_content preserved despite message count mismatch")
	}
	if rc.String() != "thinking..." {
		t.Fatalf("messages.2.reasoning_content = %q, want %q", rc.String(), "thinking...")
	}
}

func TestPreserveReasoningContent_OrdinalMatchingMultipleAssistants(t *testing.T) {
	original := []byte(`{
		"messages":[
			{"role":"user","content":"hello"},
			{"role":"assistant","content":"answer1","reasoning_content":"think1"},
			{"role":"user","content":"more"},
			{"role":"assistant","content":"answer2","reasoning_content":"think2"}
		]
	}`)
	translated := []byte(`{
		"messages":[
			{"role":"system","content":"system"},
			{"role":"user","content":"hello"},
			{"role":"assistant","content":"answer1"},
			{"role":"user","content":"more"},
			{"role":"assistant","content":"answer2"}
		]
	}`)

	out, err := preserveReasoningContent(original, translated)
	if err != nil {
		t.Fatalf("preserveReasoningContent() error = %v", err)
	}

	rc1 := gjson.GetBytes(out, "messages.2.reasoning_content")
	if !rc1.Exists() || rc1.String() != "think1" {
		t.Fatalf("first assistant (ordinal 0): got %q, want %q", rc1.String(), "think1")
	}

	rc2 := gjson.GetBytes(out, "messages.4.reasoning_content")
	if !rc2.Exists() || rc2.String() != "think2" {
		t.Fatalf("second assistant (ordinal 1): got %q, want %q", rc2.String(), "think2")
	}
}

func TestPreserveReasoningContent_OrdinalMatchingWithToolCalls(t *testing.T) {
	original := []byte(`{
		"messages":[
			{"role":"user","content":"list files"},
			{"role":"assistant","content":"I'll check","reasoning_content":"need to ls","tool_calls":[{"id":"c1","type":"function","function":{"name":"ls","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"c1","content":"file1.txt"},
			{"role":"assistant","content":"Here are the files","reasoning_content":"got the list"}
		]
	}`)
	translated := []byte(`{
		"messages":[
			{"role":"user","content":"list files"},
			{"role":"assistant","content":"I'll check","tool_calls":[{"id":"c1","type":"function","function":{"name":"ls","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"c1","content":"file1.txt"},
			{"role":"assistant","content":"Here are the files"}
		]
	}`)

	out, err := preserveReasoningContent(original, translated)
	if err != nil {
		t.Fatalf("preserveReasoningContent() error = %v", err)
	}

	rc1 := gjson.GetBytes(out, "messages.1.reasoning_content").String()
	if rc1 != "need to ls" {
		t.Fatalf("first assistant reasoning = %q, want %q", rc1, "need to ls")
	}

	rc2 := gjson.GetBytes(out, "messages.3.reasoning_content").String()
	if rc2 != "got the list" {
		t.Fatalf("second assistant reasoning = %q, want %q", rc2, "got the list")
	}
}

func TestPreserveReasoningContent_PartialAssistantReasoning(t *testing.T) {
	original := []byte(`{
		"messages":[
			{"role":"user","content":"hello"},
			{"role":"assistant","content":"answer1","reasoning_content":"thinking..."},
			{"role":"user","content":"more"},
			{"role":"assistant","content":"answer2"}
		]
	}`)
	translated := []byte(`{
		"messages":[
			{"role":"user","content":"hello"},
			{"role":"assistant","content":"answer1"},
			{"role":"user","content":"more"},
			{"role":"assistant","content":"answer2"}
		]
	}`)

	out, err := preserveReasoningContent(original, translated)
	if err != nil {
		t.Fatalf("preserveReasoningContent() error = %v", err)
	}

	rc1 := gjson.GetBytes(out, "messages.1.reasoning_content").String()
	if rc1 != "thinking..." {
		t.Fatalf("first assistant reasoning = %q, want %q", rc1, "thinking...")
	}

	if gjson.GetBytes(out, "messages.3.reasoning_content").Exists() {
		t.Fatalf("second assistant should not have reasoning_content when original had none")
	}
}
