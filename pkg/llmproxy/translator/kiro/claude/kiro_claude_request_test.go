package claude

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestBuildKiroPayload(t *testing.T) {
	claudeBody := []byte(`{
		"model": "claude-3-sonnet",
		"max_tokens": 1024,
		"messages": [
			{"role": "user", "content": "hello"}
		],
		"system": "be helpful"
	}`)

	payload, thinking := BuildKiroPayload(claudeBody, "kiro-model", "arn:aws:kiro", "CLI", false, false, nil, nil)
	if thinking {
		t.Error("expected thinking to be false")
	}

	var p KiroPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}

	if p.ProfileArn != "arn:aws:kiro" {
		t.Errorf("expected profileArn arn:aws:kiro, got %s", p.ProfileArn)
	}

	if p.InferenceConfig.MaxTokens != 1024 {
		t.Errorf("expected maxTokens 1024, got %d", p.InferenceConfig.MaxTokens)
	}

	content := p.ConversationState.CurrentMessage.UserInputMessage.Content
	if !strings.Contains(content, "hello") {
		t.Errorf("expected content to contain 'hello', got %s", content)
	}
	if !strings.Contains(content, "be helpful") {
		t.Errorf("expected content to contain system prompt 'be helpful', got %s", content)
	}

	// Test agentic and chatOnly
	payload2, _ := BuildKiroPayload(claudeBody, "kiro-model", "arn", "CLI", true, true, nil, nil)
	if !strings.Contains(string(payload2), "CHUNKED WRITE PROTOCOL") {
		t.Error("Agentic prompt not found in payload")
	}
}

func TestBuildKiroPayload_Thinking(t *testing.T) {
	claudeBody := []byte(`{
		"model": "claude-3-sonnet",
		"messages": [{"role": "user", "content": "hi"}],
		"thinking": {"type": "enabled", "budget_tokens": 1000}
	}`)

	payload, thinking := BuildKiroPayload(claudeBody, "kiro-model", "arn", "CLI", false, false, nil, nil)
	if !thinking {
		t.Error("expected thinking to be true")
	}

	// json.Marshal escapes < and > by default
	if !strings.Contains(string(payload), "thinking_mode") {
		t.Error("expected thinking hint in payload")
	}
}

func TestBuildKiroPayload_ToolChoice(t *testing.T) {
	claudeBody := []byte(`{
		"model": "claude-3-sonnet",
		"messages": [{"role": "user", "content": "hi"}],
		"tools": [{"name": "my_tool", "description": "desc", "input_schema": {"type": "object"}}],
		"tool_choice": {"type": "tool", "name": "my_tool"}
	}`)

	payload, _ := BuildKiroPayload(claudeBody, "kiro-model", "arn", "CLI", false, false, nil, nil)
	if !strings.Contains(string(payload), "You MUST use the tool named 'my_tool'") {
		t.Error("expected tool_choice hint in payload")
	}
}

func TestIsThinkingEnabledWithHeaders(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		headers http.Header
		want    bool
	}{
		{"None", `{}`, nil, false},
		{"Claude Enabled", `{"thinking": {"type": "enabled", "budget_tokens": 1000}}`, nil, true},
		{"Claude Disabled", `{"thinking": {"type": "disabled"}}`, nil, false},
		{"OpenAI", `{"reasoning_effort": "high"}`, nil, true},
		{"Cursor", `{"system": "<thinking_mode>interleaved</thinking_mode>"}`, nil, true},
		{"Header", `{}`, http.Header{"Anthropic-Beta": []string{"interleaved-thinking-2025-05-14"}}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsThinkingEnabledWithHeaders([]byte(tc.body), tc.headers); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestConvertClaudeToolsToKiro(t *testing.T) {
	tools := gjson.Parse(`[
		{
			"name": "web_search",
			"description": "search the web",
			"input_schema": {"type": "object", "properties": {"query": {"type": "string"}}}
		},
		{
			"name": "long_name_" + strings.Repeat("a", 60),
			"description": "",
			"input_schema": {"type": "object"}
		}
	]`)

	kiroTools := convertClaudeToolsToKiro(tools)
	if len(kiroTools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(kiroTools))
	}

	if kiroTools[0].ToolSpecification.Name != "remote_web_search" {
		t.Errorf("expected remote_web_search, got %s", kiroTools[0].ToolSpecification.Name)
	}

	if kiroTools[1].ToolSpecification.Description == "" {
		t.Error("expected non-empty description for second tool")
	}
}

func TestProcessMessages(t *testing.T) {
	messages := gjson.Parse(`[
		{"role": "user", "content": "hello"},
		{"role": "assistant", "content": [{"type": "text", "text": "I can help."}, {"type": "tool_use", "id": "call_1", "name": "my_tool", "input": {"a": 1}}]},
		{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "call_1", "content": "result 1"}]}
	]`)

	history, currentMsg, currentToolResults := processMessages(messages, "model-1", "CLI")

	// Pre-requisite: my history should have user and assistant message
	if len(history) != 2 {
		t.Fatalf("expected 2 history messages, got %d", len(history))
	}

	if history[0].UserInputMessage == nil {
		t.Error("expected first history message to be user")
	}

	if history[1].AssistantResponseMessage == nil {
		t.Error("expected second history message to be assistant")
	}

	if currentMsg == nil {
		t.Fatal("expected currentMsg not to be nil")
	}

	if len(currentToolResults) != 1 {
		t.Errorf("expected 1 current tool result, got %d", len(currentToolResults))
	}

	if currentToolResults[0].ToolUseID != "call_1" {
		t.Errorf("expected toolUseId call_1, got %s", currentToolResults[0].ToolUseID)
	}
}

func TestProcessMessages_Orphaned(t *testing.T) {
	// Assistant message with tool_use is MISSING (simulating compaction)
	messages := gjson.Parse(`[
		{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "call_1", "content": "result 1"}]}
	]`)

	history, currentMsg, currentToolResults := processMessages(messages, "model-1", "CLI")

	if len(history) != 0 {
		t.Errorf("expected 0 history messages, got %d", len(history))
	}

	if len(currentToolResults) != 0 {
		t.Errorf("expected 0 current tool results (orphaned), got %d", len(currentToolResults))
	}

	if !strings.Contains(currentMsg.Content, "Tool results provided.") {
		t.Errorf("expected default content, got %s", currentMsg.Content)
	}
}

func TestProcessMessages_StartingWithAssistant(t *testing.T) {
	messages := gjson.Parse(`[
		{"role": "assistant", "content": "Hello"}
	]`)

	history, _, _ := processMessages(messages, "model-1", "CLI")

	// Should prepend a placeholder user message
	if len(history) != 2 {
		t.Fatalf("expected 2 history messages (placeholder user + assistant), got %d", len(history))
	}

	if history[0].UserInputMessage.Content != "." {
		t.Errorf("expected placeholder user content '.', got %s", history[0].UserInputMessage.Content)
	}
}

func TestBuildUserMessageStruct_SoftLimit(t *testing.T) {
	msg := gjson.Parse(`{
		"role": "user",
		"content": [
			{"type": "tool_result", "tool_use_id": "call_1", "is_error": true, "content": "SOFT_LIMIT_REACHED error"}
		]
	}`)
	
	_, results := BuildUserMessageStruct(msg, "model", "CLI")
	if len(results) != 1 {
		t.Fatalf("expected 1 tool result, got %d", len(results))
	}
	
	if results[0].Status != "success" {
		t.Errorf("expected status success for soft limit error, got %s", results[0].Status)
	}
	
	if !strings.Contains(results[0].Content[0].Text, "SOFT_LIMIT_REACHED") {
		t.Errorf("expected content to contain SOFT_LIMIT_REACHED, got %s", results[0].Content[0].Text)
	}
}

func TestBuildAssistantMessageStruct(t *testing.T) {
	// Simple text
	msg1 := gjson.Parse(`{"role": "assistant", "content": "hello"}`)
	res1 := BuildAssistantMessageStruct(msg1)
	if res1.Content != "hello" {
		t.Errorf("expected content hello, got %s", res1.Content)
	}
	
	// Array content with tool use
	msg2 := gjson.Parse(`{"role": "assistant", "content": [{"type": "text", "text": "using tool"}, {"type": "tool_use", "id": "c1", "name": "f1", "input": {"x": 1}}]}`)
	res2 := BuildAssistantMessageStruct(msg2)
	if res2.Content != "using tool" {
		t.Errorf("expected content 'using tool', got %s", res2.Content)
	}
	if len(res2.ToolUses) != 1 || res2.ToolUses[0].Name != "f1" {
		t.Errorf("expected tool call f1, got %v", res2.ToolUses)
	}
	
	// Empty content with tool use
	msg3 := gjson.Parse(`{"role": "assistant", "content": [{"type": "tool_use", "id": "c1", "name": "f1", "input": {"x": 1}}]}`)
	res3 := BuildAssistantMessageStruct(msg3)
	if res3.Content == "" {
		t.Error("expected non-empty default content for assistant tool use")
	}
}

func TestShortenToolNameIfNeeded(t *testing.T) {
	tests := []struct {
		name     string
		expected string
	}{
		{"short_name", "short_name"},
		{strings.Repeat("a", 65), strings.Repeat("a", 64)},
		{"mcp__server__long_tool_name_that_exceeds_sixty_four_characters_limit", "mcp__long_tool_name_that_exceeds_sixty_four_characters_limit"},
		{"mcp__" + strings.Repeat("a", 70), "mcp__" + strings.Repeat("a", 59)},
	}
	for _, tt := range tests {
		got := shortenToolNameIfNeeded(tt.name)
		if got != tt.expected {
			t.Errorf("shortenToolNameIfNeeded(%s) = %s, want %s", tt.name, got, tt.expected)
		}
	}
}

func TestExtractClaudeToolChoiceHint(t *testing.T) {
	tests := []struct {
		body     string
		expected string
	}{
		{`{"tool_choice": {"type": "any"}}`, "MUST use at least one"},
		{`{"tool_choice": {"type": "tool", "name": "t1"}}`, "MUST use the tool named 't1'"},
		{`{"tool_choice": {"type": "auto"}}`, ""},
		{`{}`, ""},
	}
	for _, tt := range tests {
		got := extractClaudeToolChoiceHint([]byte(tt.body))
		if tt.expected == "" {
			if got != "" {
				t.Errorf("extractClaudeToolChoiceHint(%s) = %s, want empty", tt.body, got)
			}
		} else if !strings.Contains(got, tt.expected) {
			t.Errorf("extractClaudeToolChoiceHint(%s) = %s, want it to contain %s", tt.body, got, tt.expected)
		}
	}
}
