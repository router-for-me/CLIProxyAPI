package claude

import (
	"net/http"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// TestBuildKiroPayload_HistoryWithToolUseButNoTools reproduces the 400 case
// observed in production: a follow-up Claude request whose history contains
// a previous assistant tool_use turn, but whose top-level `tools` array was
// not re-attached by the client (e.g. OpenCode after compaction).
//
// Expected behavior: the resulting Kiro payload's
// currentMessage.userInputMessageContext.tools must be a non-empty array,
// because Kiro rejects requests with history tool turns and empty tools as
// "Improperly formed request".
func TestBuildKiroPayload_HistoryWithToolUseButNoTools(t *testing.T) {
	claudeReq := `{
		"model": "claude-sonnet-4-5",
		"max_tokens": 1024,
		"messages": [
			{"role": "user", "content": "list files"},
			{"role": "assistant", "content": [
				{"type": "tool_use", "id": "tu_1", "name": "Bash", "input": {"command": "ls"}}
			]},
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "tu_1", "content": "file1\nfile2"}
			]},
			{"role": "user", "content": "now what?"}
		]
	}`

	out, _ := BuildKiroPayload([]byte(claudeReq), "claude-sonnet-4-5", "arn:test", "test", true, false, http.Header{}, nil)
	if len(out) == 0 {
		t.Fatal("expected non-empty payload")
	}

	tools := gjson.GetBytes(out, "conversationState.currentMessage.userInputMessage.userInputMessageContext.tools")
	if !tools.IsArray() {
		t.Fatalf("currentMessage.userInputMessageContext.tools is not an array: %s", tools.Raw)
	}
	if len(tools.Array()) == 0 {
		t.Fatalf("expected synthesized tools, got empty array. payload: %s", string(out))
	}
	// Confirm the synthesized stub references the historical tool name.
	found := false
	for _, t0 := range tools.Array() {
		if t0.Get("toolSpecification.name").String() == "Bash" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected stub tool spec named 'Bash', got: %s", tools.Raw)
	}
}

// TestBuildKiroPayload_HistoryWithToolUseAndExplicitTools confirms that when
// the client DOES attach tools, we don't double-add stubs.
func TestBuildKiroPayload_HistoryWithToolUseAndExplicitTools(t *testing.T) {
	claudeReq := `{
		"model": "claude-sonnet-4-5",
		"max_tokens": 1024,
		"tools": [
			{"name": "Bash", "description": "real desc", "input_schema": {"type": "object", "properties": {"command": {"type": "string"}}}}
		],
		"messages": [
			{"role": "user", "content": "list files"},
			{"role": "assistant", "content": [
				{"type": "tool_use", "id": "tu_1", "name": "Bash", "input": {"command": "ls"}}
			]},
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "tu_1", "content": "ok"}
			]},
			{"role": "user", "content": "next"}
		]
	}`

	out, _ := BuildKiroPayload([]byte(claudeReq), "claude-sonnet-4-5", "arn:test", "test", true, false, http.Header{}, nil)
	tools := gjson.GetBytes(out, "conversationState.currentMessage.userInputMessage.userInputMessageContext.tools")
	if !tools.IsArray() || len(tools.Array()) != 1 {
		t.Fatalf("expected exactly 1 tool, got: %s", tools.Raw)
	}
	if got := tools.Array()[0].Get("toolSpecification.description").String(); got != "real desc" {
		t.Fatalf("expected real description preserved, got %q (likely overwritten by stub)", got)
	}
}

// TestBuildKiroPayload_NoToolsNoHistoryToolUse is the baseline: a plain text
// turn with no tool use anywhere should not introduce any tools.
func TestBuildKiroPayload_NoToolsNoHistoryToolUse(t *testing.T) {
	claudeReq := `{
		"model": "claude-sonnet-4-5",
		"max_tokens": 256,
		"messages": [
			{"role": "user", "content": "hello"}
		]
	}`
	out, _ := BuildKiroPayload([]byte(claudeReq), "claude-sonnet-4-5", "arn:test", "test", false, true, http.Header{}, nil)
	tools := gjson.GetBytes(out, "conversationState.currentMessage.userInputMessage.userInputMessageContext.tools")
	if tools.Exists() && tools.IsArray() && len(tools.Array()) > 0 {
		t.Fatalf("did not expect tools to be synthesized for plain chat turn: %s", tools.Raw)
	}
}

// TestBuildKiroPayload_TrailingSystemMessageKeepsTools reproduces the case
// observed in production: Claude Code's mid-conversation-system beta appends a
// role:"system" message as the FINAL entry of the messages array. Without
// normalization the system message is skipped, no current user message is
// produced, and the converted tools are silently dropped from the payload —
// the model then hallucinates text-format tool calls.
//
// Expected behavior: the trailing system message is carried as user content
// and the client-declared tools land on
// currentMessage.userInputMessageContext.tools.
func TestBuildKiroPayload_TrailingSystemMessageKeepsTools(t *testing.T) {
	claudeReq := `{
		"model": "claude-sonnet-4-6",
		"max_tokens": 1024,
		"tools": [
			{"name": "Grep", "description": "search", "input_schema": {"type": "object", "properties": {"pattern": {"type": "string"}}}}
		],
		"messages": [
			{"role": "user", "content": "investigate the revision conflict"},
			{"role": "system", "content": "Available agent types for the Agent tool: ..."}
		]
	}`

	out, _ := BuildKiroPayload([]byte(claudeReq), "claude-sonnet-4-6", "arn:test", "test", true, false, http.Header{}, nil)
	if len(out) == 0 {
		t.Fatal("expected non-empty payload")
	}

	tools := gjson.GetBytes(out, "conversationState.currentMessage.userInputMessage.userInputMessageContext.tools")
	if !tools.IsArray() || len(tools.Array()) != 1 {
		t.Fatalf("expected exactly 1 tool on currentMessage, got: %s", tools.Raw)
	}
	if got := tools.Array()[0].Get("toolSpecification.name").String(); got != "Grep" {
		t.Fatalf("expected tool named 'Grep', got %q", got)
	}

	content := gjson.GetBytes(out, "conversationState.currentMessage.userInputMessage.content").String()
	if !strings.Contains(content, "investigate the revision conflict") {
		t.Fatalf("expected user text in current message content, got: %q", content)
	}
	if !strings.Contains(content, "Available agent types for the Agent tool") {
		t.Fatalf("expected system message text carried into current message content, got: %q", content)
	}
	if !strings.Contains(content, "<system-reminder>") {
		t.Fatalf("expected system text wrapped in <system-reminder> tags, got: %q", content)
	}
}

func TestBuildKiroPayload_MidConversationSystemStringPreservesOrder(t *testing.T) {
	claudeReq := `{
		"model": "claude-sonnet-4-6",
		"max_tokens": 1024,
		"messages": [
			{"role": "user", "content": "first user turn"},
			{"role": "assistant", "content": "first assistant turn"},
			{"role": "system", "content": "mid-conversation instruction"},
			{"role": "user", "content": "final user turn"}
		]
	}`

	out, _ := BuildKiroPayload([]byte(claudeReq), "claude-sonnet-4-6", "arn:test", "test", false, true, http.Header{}, nil)
	history := gjson.GetBytes(out, "conversationState.history").Array()
	if len(history) != 2 {
		t.Fatalf("expected user and assistant history entries, got %d: %s", len(history), string(out))
	}
	if got := history[0].Get("userInputMessage.content").String(); got != "first user turn" {
		t.Fatalf("unexpected first history content: %q", got)
	}
	if got := history[1].Get("assistantResponseMessage.content").String(); got != "first assistant turn" {
		t.Fatalf("unexpected second history content: %q", got)
	}

	current := gjson.GetBytes(out, "conversationState.currentMessage.userInputMessage.content").String()
	want := "<system-reminder>\nmid-conversation instruction\n</system-reminder>\nfinal user turn"
	if current != want {
		t.Fatalf("system and final user content reordered or changed:\nwant %q\n got %q", want, current)
	}
}

func TestBuildKiroPayload_MidConversationSystemArrayPreservesBlockOrder(t *testing.T) {
	claudeReq := `{
		"model": "claude-sonnet-4-6",
		"max_tokens": 1024,
		"messages": [
			{"role": "user", "content": "before system"},
			{"role": "system", "content": [
				{"type": "text", "text": "first system block"},
				{"type": "text", "text": "second system block"}
			]},
			{"role": "assistant", "content": "assistant after system"},
			{"role": "user", "content": "after assistant"}
		]
	}`

	out, _ := BuildKiroPayload([]byte(claudeReq), "claude-sonnet-4-6", "arn:test", "test", false, true, http.Header{}, nil)
	history := gjson.GetBytes(out, "conversationState.history").Array()
	if len(history) != 2 {
		t.Fatalf("expected merged user and assistant history entries, got %d: %s", len(history), string(out))
	}
	wantHistory := "before system\n<system-reminder>\nfirst system block\nsecond system block\n</system-reminder>"
	if got := history[0].Get("userInputMessage.content").String(); got != wantHistory {
		t.Fatalf("system blocks reordered or changed:\nwant %q\n got %q", wantHistory, got)
	}
	if got := history[1].Get("assistantResponseMessage.content").String(); got != "assistant after system" {
		t.Fatalf("assistant order changed: %q", got)
	}
	if got := gjson.GetBytes(out, "conversationState.currentMessage.userInputMessage.content").String(); got != "after assistant" {
		t.Fatalf("final user order changed: %q", got)
	}
}

// TestSynthesizeToolSpecsFromHistory_Dedup ensures repeated tool names yield a
// single stub.
func TestSynthesizeToolSpecsFromHistory_Dedup(t *testing.T) {
	hist := []KiroHistoryMessage{
		{AssistantResponseMessage: &KiroAssistantResponseMessage{
			ToolUses: []KiroToolUse{{Name: "Bash"}, {Name: "Bash"}, {Name: "Read"}},
		}},
		{AssistantResponseMessage: &KiroAssistantResponseMessage{
			ToolUses: []KiroToolUse{{Name: "Read"}, {Name: "Edit"}},
		}},
	}
	got := synthesizeToolSpecsFromHistory(hist)
	if len(got) != 3 {
		t.Fatalf("expected 3 unique stubs, got %d: %+v", len(got), got)
	}
	names := []string{}
	for _, g := range got {
		names = append(names, g.ToolSpecification.Name)
	}
	joined := strings.Join(names, ",")
	for _, want := range []string{"Bash", "Read", "Edit"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected %q in synthesized names %q", want, joined)
		}
	}
}
