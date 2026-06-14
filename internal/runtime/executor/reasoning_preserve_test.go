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

func TestPreserveReasoningContent_OriginalWinsOverTranslated(t *testing.T) {
	original := []byte(`{
		"messages":[
			{"role":"user","content":"hello"},
			{"role":"assistant","content":"answer","reasoning_content":"original reasoning"}
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
	if got != "original reasoning" {
		t.Fatalf("original must fill in when translated lacks reasoning_content: got %q, want %q", got, "original reasoning")
	}
}

func TestPreserveReasoningContent_TranslatedOverrideTakesPrecedence(t *testing.T) {
	original := []byte(`{
		"messages":[
			{"role":"user","content":"hello"},
			{"role":"assistant","content":"answer","reasoning_content":"original reasoning"}
		]
	}`)
	translated := []byte(`{
		"messages":[
			{"role":"user","content":"hello"},
			{"role":"assistant","content":"answer","reasoning_content":"override reasoning"}
		]
	}`)

	out, err := preserveReasoningContent(original, translated)
	if err != nil {
		t.Fatalf("preserveReasoningContent() error = %v", err)
	}

	got := gjson.GetBytes(out, "messages.1.reasoning_content").String()
	if got != "override reasoning" {
		t.Fatalf("translated override must take precedence over original: got %q, want %q", got, "override reasoning")
	}
}

func TestPreserveReasoningContent_FilterRemovalIsRespected(t *testing.T) {
	original := []byte(`{
		"messages":[
			{"role":"user","content":"hello"},
			{"role":"assistant","content":"answer","reasoning_content":"original reasoning"},
			{"role":"user","content":"follow up"},
			{"role":"assistant","content":"answer2","reasoning_content":"original reasoning2"}
		]
	}`)
	translated := []byte(`{
		"messages":[
			{"role":"user","content":"hello"},
			{"role":"assistant","content":"answer"},
			{"role":"user","content":"follow up"},
			{"role":"assistant","content":"answer2","reasoning_content":"filtered replacement"}
		]
	}`)

	out, err := preserveReasoningContent(original, translated)
	if err != nil {
		t.Fatalf("preserveReasoningContent() error = %v", err)
	}

	rc1 := gjson.GetBytes(out, "messages.1.reasoning_content").String()
	if rc1 != "original reasoning" {
		t.Fatalf("first assistant: original fills in when translated lacks reasoning_content: got %q, want %q", rc1, "original reasoning")
	}

	rc2 := gjson.GetBytes(out, "messages.3.reasoning_content").String()
	if rc2 != "filtered replacement" {
		t.Fatalf("second assistant: translated override takes precedence: got %q, want %q", rc2, "filtered replacement")
	}
}

func TestConvertReasoningToThinkingContent_NoOpWhenThinkingModeInactive(t *testing.T) {
	payload := []byte(`{
		"messages":[
			{"role":"user","content":"hello"},
			{"role":"assistant","content":"answer","reasoning_content":"thinking..."}
		]
	}`)

	out, err := convertReasoningToThinkingContent(payload)
	if err != nil {
		t.Fatalf("convertReasoningToThinkingContent() returned error = %v", err)
	}

	rc := gjson.GetBytes(out, "messages.1.reasoning_content").String()
	if rc != "thinking..." {
		t.Fatalf("reasoning_content should be preserved when thinking mode is inactive, got %q", rc)
	}
	content := gjson.GetBytes(out, "messages.1.content")
	if content.IsArray() {
		t.Fatalf("content should remain a string when thinking mode is inactive")
	}
}

func TestConvertReasoningToThinkingContent_ConvertsWhenReasoningEffortSet(t *testing.T) {
	payload := []byte(`{
		"reasoning_effort":"high",
		"messages":[
			{"role":"user","content":"hello"},
			{"role":"assistant","content":"answer text","reasoning_content":"I need to think about this"}
		]
	}`)

	out, err := convertReasoningToThinkingContent(payload)
	if err != nil {
		t.Fatalf("convertReasoningToThinkingContent() returned error = %v", err)
	}

	rc := gjson.GetBytes(out, "messages.1.reasoning_content").String()
	if rc != "I need to think about this" {
		t.Fatalf("reasoning_content top-level field should be preserved for backward compat, got %q", rc)
	}

	contentType0 := gjson.GetBytes(out, "messages.1.content.0.type").String()
	if contentType0 != "thinking" {
		t.Fatalf("first content part should be thinking block, got type %q", contentType0)
	}
	thinkingText := gjson.GetBytes(out, "messages.1.content.0.thinking").String()
	if thinkingText != "I need to think about this" {
		t.Fatalf("thinking text = %q, want %q", thinkingText, "I need to think about this")
	}

	contentType1 := gjson.GetBytes(out, "messages.1.content.1.type").String()
	if contentType1 != "text" {
		t.Fatalf("second content part should be text block, got type %q", contentType1)
	}
	textContent := gjson.GetBytes(out, "messages.1.content.1.text").String()
	if textContent != "answer text" {
		t.Fatalf("text content = %q, want %q", textContent, "answer text")
	}
}

func TestConvertReasoningToThinkingContent_ConvertsWhenThinkingTypeEnabled(t *testing.T) {
	payload := []byte(`{
		"thinking":{"type":"enabled"},
		"messages":[
			{"role":"user","content":"hello"},
			{"role":"assistant","content":"","reasoning_content":"my reasoning","tool_calls":[{"id":"c1","type":"function","function":{"name":"fn","arguments":"{}"}}]}
		]
	}`)

	out, err := convertReasoningToThinkingContent(payload)
	if err != nil {
		t.Fatalf("convertReasoningToThinkingContent() returned error = %v", err)
	}

	rc := gjson.GetBytes(out, "messages.1.reasoning_content").String()
	if rc != "my reasoning" {
		t.Fatalf("reasoning_content should be preserved for backward compat, got %q", rc)
	}

	thinkingText := gjson.GetBytes(out, "messages.1.content.0.thinking").String()
	if thinkingText != "my reasoning" {
		t.Fatalf("thinking text = %q, want %q", thinkingText, "my reasoning")
	}

	if !gjson.GetBytes(out, "messages.1.tool_calls").Exists() {
		t.Fatalf("tool_calls should be preserved")
	}
}

func TestConvertReasoningToThinkingContent_PreservesContentArray(t *testing.T) {
	payload := []byte(`{
		"reasoning_effort":"medium",
		"messages":[
			{"role":"user","content":"hello"},
			{"role":"assistant","content":[{"type":"text","text":"part1"},{"type":"text","text":"part2"}],"reasoning_content":"thinking here"}
		]
	}`)

	out, err := convertReasoningToThinkingContent(payload)
	if err != nil {
		t.Fatalf("convertReasoningToThinkingContent() returned error = %v", err)
	}

	numParts := len(gjson.GetBytes(out, "messages.1.content").Array())
	if numParts != 3 {
		t.Fatalf("expected 3 content parts (1 thinking + 2 text), got %d", numParts)
	}

	if gjson.GetBytes(out, "messages.1.content.0.type").String() != "thinking" {
		t.Fatalf("first part should be thinking")
	}
	if gjson.GetBytes(out, "messages.1.content.1.text").String() != "part1" {
		t.Fatalf("second part text should be part1")
	}
	if gjson.GetBytes(out, "messages.1.content.2.text").String() != "part2" {
		t.Fatalf("third part text should be part2")
	}
}

func TestConvertReasoningToThinkingContent_SkipsEmptyReasoning(t *testing.T) {
	payload := []byte(`{
		"reasoning_effort":"high",
		"messages":[
			{"role":"user","content":"hello"},
			{"role":"assistant","content":"answer","reasoning_content":""}
		]
	}`)

	out, err := convertReasoningToThinkingContent(payload)
	if err != nil {
		t.Fatalf("convertReasoningToThinkingContent() returned error = %v", err)
	}

	if gjson.GetBytes(out, "messages.1.content").IsArray() {
		t.Fatalf("empty reasoning_content should not trigger conversion to content array")
	}
}

func TestConvertReasoningToThinkingContent_SkipsNonAssistantMessages(t *testing.T) {
	payload := []byte(`{
		"reasoning_effort":"high",
		"messages":[
			{"role":"user","content":"hello","reasoning_content":"should be ignored"},
			{"role":"assistant","content":"answer","reasoning_content":"valid reasoning"}
		]
	}`)

	out, err := convertReasoningToThinkingContent(payload)
	if err != nil {
		t.Fatalf("convertReasoningToThinkingContent() returned error = %v", err)
	}

	userRC := gjson.GetBytes(out, "messages.0.reasoning_content")
	if !userRC.Exists() {
		t.Fatalf("user message reasoning_content should not be touched")
	}

	assistantThinking := gjson.GetBytes(out, "messages.1.content.0.thinking").String()
	if assistantThinking != "valid reasoning" {
		t.Fatalf("assistant thinking = %q, want %q", assistantThinking, "valid reasoning")
	}
}
