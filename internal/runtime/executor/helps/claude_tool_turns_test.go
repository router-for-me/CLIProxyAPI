package helps

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestRepairDanglingClaudeToolUses(t *testing.T) {
	t.Run("merges an interrupting user prompt after an error result", func(t *testing.T) {
		input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"cmd":"curl"}}]},{"role":"user","content":"fix the regression"}]}`)
		got := RepairDanglingClaudeToolUses(input)
		messages := claudeMessagesForTest(t, got, 2)
		blocks := messages[1].Get("content").Array()
		if len(blocks) != 2 {
			t.Fatalf("user blocks = %d, want 2: %s", len(blocks), messages[1].Raw)
		}
		assertInterruptedClaudeToolResult(t, blocks[0], "t1", "")
		if blocks[1].Get("type").String() != "text" || blocks[1].Get("text").String() != "fix the regression" {
			t.Fatalf("user text not preserved: %s", blocks[1].Raw)
		}
	})

	t.Run("adds a user result after a trailing assistant", func(t *testing.T) {
		input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"t_last","name":"Read","input":{}}]}]}`)
		got := RepairDanglingClaudeToolUses(input)
		messages := claudeMessagesForTest(t, got, 2)
		if messages[1].Get("role").String() != "user" {
			t.Fatalf("inserted role = %q, want user", messages[1].Get("role").String())
		}
		assertInterruptedClaudeToolResult(t, messages[1].Get("content.0"), "t_last", "")
	})

	t.Run("leaves a canonical pairing byte-identical", func(t *testing.T) {
		input := []byte(`{ "messages": [ {"role":"assistant","content":[{"type":"tool_use","id":"done1","name":"Bash"}]}, {"role":"user","content":[{"type":"tool_result","tool_use_id":"done1","content":"success"},{"type":"text","text":"continue"}]} ], "max_tokens": 42 }`)
		got := RepairDanglingClaudeToolUses(input)
		if !bytes.Equal(got, input) {
			t.Fatalf("canonical pairing mutated:\n got %s\nwant %s", got, input)
		}
	})

	t.Run("orders partial parallel results by tool use", func(t *testing.T) {
		input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"p1","name":"Bash"},{"type":"tool_use","id":"p2","name":"Bash"}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"p1","content":"p1 done"}]}]}`)
		got := RepairDanglingClaudeToolUses(input)
		messages := claudeMessagesForTest(t, got, 2)
		blocks := messages[1].Get("content").Array()
		if len(blocks) != 2 {
			t.Fatalf("user blocks = %d, want 2: %s", len(blocks), messages[1].Raw)
		}
		if blocks[0].Get("tool_use_id").String() != "p1" || blocks[0].Get("content").String() != "p1 done" {
			t.Fatalf("existing p1 result was not kept first: %s", blocks[0].Raw)
		}
		assertInterruptedClaudeToolResult(t, blocks[1], "p2", "")
	})

	t.Run("inserts a result between consecutive assistants", func(t *testing.T) {
		input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"mid","name":"Bash"}]},{"role":"assistant","content":[{"type":"text","text":"continue"}]}]}`)
		got := RepairDanglingClaudeToolUses(input)
		messages := claudeMessagesForTest(t, got, 3)
		assertInterruptedClaudeToolResult(t, messages[1].Get("content.0"), "mid", "")
		if messages[2].Get("role").String() != "assistant" || messages[2].Get("content.0.text").String() != "continue" {
			t.Fatalf("trailing assistant mutated: %s", messages[2].Raw)
		}
	})

	t.Run("is idempotent", func(t *testing.T) {
		input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash"}]},{"role":"user","content":"stop"}]}`)
		once := RepairDanglingClaudeToolUses(input)
		twice := RepairDanglingClaudeToolUses(once)
		if !bytes.Equal(twice, once) {
			t.Fatalf("second repair mutated:\n once %s\n twice %s", once, twice)
		}
	})
}

func TestRepairDanglingClaudeToolUsesCanonicalizesResultCarriers(t *testing.T) {
	t.Run("normalizes tool role and top level call id", func(t *testing.T) {
		input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash"}]},{"role":"tool","tool_call_id":"t1","content":"real output"}]}`)
		got := RepairDanglingClaudeToolUses(input)
		messages := claudeMessagesForTest(t, got, 2)
		if messages[1].Get("role").String() != "user" || messages[1].Get("tool_call_id").Exists() {
			t.Fatalf("tool message not normalized: %s", messages[1].Raw)
		}
		result := messages[1].Get("content.0")
		if result.Get("type").String() != "tool_result" || result.Get("tool_use_id").String() != "t1" || result.Get("content").String() != "real output" {
			t.Fatalf("tool result not preserved: %s", result.Raw)
		}
		if result.Get("is_error").Bool() {
			t.Fatalf("real tool output was marked as an error: %s", result.Raw)
		}
	})

	t.Run("does not accept an id alias inside a result block", func(t *testing.T) {
		input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash"}]},{"role":"user","content":[{"type":"tool_result","id":"t1","content":"real output"}]}]}`)
		got := RepairDanglingClaudeToolUses(input)
		messages := claudeMessagesForTest(t, got, 2)
		blocks := messages[1].Get("content").Array()
		if messages[1].Get("role").String() != "user" || len(blocks) != 2 {
			t.Fatalf("malformed result carrier not normalized: %s", messages[1].Raw)
		}
		assertInterruptedClaudeToolResult(t, blocks[0], "t1", "")
		if blocks[1].Get("type").String() != "text" || !strings.Contains(blocks[1].Get("text").String(), "real output") {
			t.Fatalf("id alias was treated as a valid result or lost: %s", messages[1].Raw)
		}
	})

	t.Run("does not treat a generic top level id as a tool result id", func(t *testing.T) {
		input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash"}]},{"role":"tool","id":"t1","content":"untrusted alias"}]}`)
		got := RepairDanglingClaudeToolUses(input)
		messages := claudeMessagesForTest(t, got, 2)
		blocks := messages[1].Get("content").Array()
		if len(blocks) != 2 {
			t.Fatalf("user blocks = %d, want 2: %s", len(blocks), messages[1].Raw)
		}
		assertInterruptedClaudeToolResult(t, blocks[0], "t1", "")
		if blocks[1].Get("type").String() != "text" || blocks[1].Get("text").String() != "untrusted alias" {
			t.Fatalf("generic id payload was not downgraded safely: %s", messages[1].Raw)
		}
	})

	t.Run("merges split parallel carriers without inventing errors", func(t *testing.T) {
		input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"p1","name":"one"},{"type":"tool_use","id":"p2","name":"two"}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"p2","content":"two"}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"p1","content":"one"}]}]}`)
		got := RepairDanglingClaudeToolUses(input)
		messages := claudeMessagesForTest(t, got, 2)
		blocks := messages[1].Get("content").Array()
		if len(blocks) != 2 || blocks[0].Get("tool_use_id").String() != "p1" || blocks[1].Get("tool_use_id").String() != "p2" {
			t.Fatalf("split results not merged in call order: %s", messages[1].Raw)
		}
		for _, block := range blocks {
			if block.Get("is_error").Bool() {
				t.Fatalf("real result replaced by an error: %s", block.Raw)
			}
		}
	})

	t.Run("downgrades duplicate orphan and malformed results", func(t *testing.T) {
		input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"one"}]},{"role":"tool","content":[{"type":"tool_result","tool_use_id":"t1","content":"first"},{"type":"tool_result","tool_use_id":"t1","content":"duplicate"},{"type":"tool_result","tool_use_id":"ghost","content":"orphan"},{"type":"tool_result","content":"missing id"},{"type":"text","text":"keep me"}]}]}`)
		got := RepairDanglingClaudeToolUses(input)
		messages := claudeMessagesForTest(t, got, 2)
		blocks := messages[1].Get("content").Array()
		toolResultCount := 0
		var text strings.Builder
		for _, block := range blocks {
			if block.Get("type").String() == "tool_result" {
				toolResultCount++
				if block.Get("tool_use_id").String() != "t1" {
					t.Fatalf("unexpected protocol result survived: %s", block.Raw)
				}
			}
			if block.Get("type").String() == "text" {
				text.WriteString(block.Get("text").String())
			}
		}
		if toolResultCount != 1 {
			t.Fatalf("tool result count = %d, want 1: %s", toolResultCount, messages[1].Raw)
		}
		for _, want := range []string{"duplicate", "orphan", "missing id", "keep me"} {
			if !strings.Contains(text.String(), want) {
				t.Fatalf("downgraded text lost %q: %s", want, messages[1].Raw)
			}
		}
	})

	t.Run("downgrades a standalone orphan result", func(t *testing.T) {
		input := []byte(`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"ghost","content":"diagnostic"}]}]}`)
		got := RepairDanglingClaudeToolUses(input)
		messages := claudeMessagesForTest(t, got, 1)
		block := messages[0].Get("content.0")
		if block.Get("type").String() != "text" || !strings.Contains(block.Get("text").String(), "diagnostic") {
			t.Fatalf("standalone orphan not downgraded: %s", messages[0].Raw)
		}
	})
}

func TestRepairDanglingClaudeToolUsesRelocatesInterveningSystemTurns(t *testing.T) {
	t.Run("adopts a real result across a system turn", func(t *testing.T) {
		input := []byte(`{"messages":[{"role":"user","content":"start"},{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read"}]},{"role":"system","content":"new context","clear_at":"next_user_message"},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"real output"}]}]}`)
		got := RepairDanglingClaudeToolUses(input)
		messages := claudeMessagesForTest(t, got, 4)
		result := messages[2].Get("content.0")
		if messages[2].Get("role").String() != "user" || result.Get("tool_use_id").String() != "t1" || result.Get("content").String() != "real output" {
			t.Fatalf("real result was not adopted: %s", messages[2].Raw)
		}
		if result.Get("is_error").Bool() {
			t.Fatalf("real result was replaced by an interruption: %s", result.Raw)
		}
		if messages[3].Raw != `{"role":"system","content":"new context","clear_at":"next_user_message"}` {
			t.Fatalf("system turn was not replayed intact after the result: %s", messages[3].Raw)
		}
		if twice := RepairDanglingClaudeToolUses(got); !bytes.Equal(twice, got) {
			t.Fatalf("real-result relocation is not idempotent:\n once %s\n twice %s", got, twice)
		}
	})

	t.Run("merges split results before replaying systems in source order", func(t *testing.T) {
		input := []byte(`{"messages":[{"role":"user","content":"start"},{"role":"assistant","content":[{"type":"tool_use","id":"p1","name":"one"},{"type":"tool_use","id":"p2","name":"two"}]},{"role":"system","content":"first"},{"role":"user","content":[{"type":"tool_result","tool_use_id":"p2","content":"two"}]},{"role":"system","content":[{"type":"text","text":"second"}],"clear_at":"next_user_message"},{"role":"user","content":[{"type":"tool_result","tool_use_id":"p1","content":"one"}]},{"role":"system","content":"third"},{"role":"assistant","content":"continue"}]}`)
		got := RepairDanglingClaudeToolUses(input)
		messages := claudeMessagesForTest(t, got, 7)
		blocks := messages[2].Get("content").Array()
		if len(blocks) != 2 || blocks[0].Get("tool_use_id").String() != "p1" || blocks[1].Get("tool_use_id").String() != "p2" {
			t.Fatalf("split real results were not merged in call order: %s", messages[2].Raw)
		}
		for _, block := range blocks {
			if block.Get("is_error").Bool() {
				t.Fatalf("real result was replaced by an interruption: %s", block.Raw)
			}
		}
		if messages[3].Get("content").String() != "first" || messages[4].Get("content.0.text").String() != "second" || messages[5].Get("content").String() != "third" {
			t.Fatalf("system turn order changed: %s", got)
		}
		if messages[4].Get("clear_at").String() != "next_user_message" {
			t.Fatalf("system metadata was not preserved: %s", messages[4].Raw)
		}
		if messages[6].Get("role").String() != "assistant" || messages[6].Get("content").String() != "continue" {
			t.Fatalf("following assistant turn changed: %s", messages[6].Raw)
		}
	})

	t.Run("places an interrupt and its synthetic result before the system turn", func(t *testing.T) {
		input := []byte(`{"messages":[{"role":"user","content":"start"},{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash"}]},{"role":"system","content":"new constraint"},{"role":"user","content":"change direction"}]}`)
		got := RepairDanglingClaudeToolUses(input)
		messages := claudeMessagesForTest(t, got, 4)
		blocks := messages[2].Get("content").Array()
		if len(blocks) != 2 {
			t.Fatalf("canonical user blocks = %d, want 2: %s", len(blocks), messages[2].Raw)
		}
		assertInterruptedClaudeToolResult(t, blocks[0], "t1", "")
		if blocks[1].Get("type").String() != "text" || blocks[1].Get("text").String() != "change direction" {
			t.Fatalf("interrupt text was not preserved: %s", messages[2].Raw)
		}
		if messages[3].Get("role").String() != "system" || messages[3].Get("content").String() != "new constraint" {
			t.Fatalf("system turn was not moved after the canonical user turn: %s", messages[3].Raw)
		}
	})

	t.Run("inserts a fallback before terminal systems without duplication", func(t *testing.T) {
		input := []byte(`{"messages":[{"role":"user","content":"start"},{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash"}]},{"role":"system","content":"first"},{"role":"system","content":"second"},{"role":"assistant","content":"continue"}]}`)
		once := RepairDanglingClaudeToolUses(input)
		messages := claudeMessagesForTest(t, once, 6)
		assertInterruptedClaudeToolResult(t, messages[2].Get("content.0"), "t1", "")
		if messages[3].Get("content").String() != "first" || messages[4].Get("content").String() != "second" {
			t.Fatalf("terminal systems were lost or reordered: %s", once)
		}
		if messages[5].Get("role").String() != "assistant" {
			t.Fatalf("following assistant turn changed: %s", messages[5].Raw)
		}
		twice := RepairDanglingClaudeToolUses(once)
		if !bytes.Equal(twice, once) {
			t.Fatalf("system relocation is not idempotent:\n once %s\n twice %s", once, twice)
		}
	})
}

func TestRepairDanglingClaudeToolUsesPreservesToolsetAndServerToolRules(t *testing.T) {
	t.Run("copies toolset name onto a synthetic result", func(t *testing.T) {
		input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"browser1","toolset_name":"browser","name":"navigate","input":{}}]},{"role":"user","content":"stop"}]}`)
		got := RepairDanglingClaudeToolUses(input)
		messages := claudeMessagesForTest(t, got, 2)
		result := messages[1].Get("content.0")
		assertInterruptedClaudeToolResult(t, result, "browser1", "browser")
		if !result.Get("content").IsArray() || result.Get("content.0.type").String() != "text" {
			t.Fatalf("toolset result must use typed content blocks: %s", result.Raw)
		}
	})

	t.Run("copies toolset name onto a real result", func(t *testing.T) {
		input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"browser1","toolset_name":"browser","name":"navigate","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"browser1","content":[{"type":"text","text":"done"}]}]}]}`)
		got := RepairDanglingClaudeToolUses(input)
		messages := claudeMessagesForTest(t, got, 2)
		result := messages[1].Get("content.0")
		if result.Get("toolset_name").String() != "browser" || result.Get("content.0.text").String() != "done" || result.Get("is_error").Bool() {
			t.Fatalf("real toolset result was not aligned: %s", result.Raw)
		}
	})

	t.Run("drops an unresolved server call before preserving interrupt text", func(t *testing.T) {
		input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"server_tool_use","id":"srv1","name":"web_search","input":{}},{"type":"tool_use","id":"client1","name":"Read","input":{}}]},{"role":"user","content":"change direction"}]}`)
		got := RepairDanglingClaudeToolUses(input)
		messages := claudeMessagesForTest(t, got, 2)
		assistantBlocks := messages[0].Get("content").Array()
		if len(assistantBlocks) != 1 || assistantBlocks[0].Get("type").String() != "tool_use" {
			t.Fatalf("unresolved server call was not removed safely: %s", messages[0].Raw)
		}
		assertInterruptedClaudeToolResult(t, messages[1].Get("content.0"), "client1", "")
		if messages[1].Get("content.1.text").String() != "change direction" {
			t.Fatalf("interrupt text was not preserved: %s", messages[1].Raw)
		}
	})

	t.Run("drops an unresolved MCP call before preserving interrupt text", func(t *testing.T) {
		input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"mcp_tool_use","id":"mcp1","server_name":"example","name":"lookup","input":{}},{"type":"tool_use","id":"client1","name":"Read","input":{}}]},{"role":"user","content":"change direction"}]}`)
		got := RepairDanglingClaudeToolUses(input)
		messages := claudeMessagesForTest(t, got, 2)
		assistantBlocks := messages[0].Get("content").Array()
		if len(assistantBlocks) != 1 || assistantBlocks[0].Get("type").String() != "tool_use" {
			t.Fatalf("unresolved MCP call was not removed safely: %s", messages[0].Raw)
		}
		assertInterruptedClaudeToolResult(t, messages[1].Get("content.0"), "client1", "")
		if messages[1].Get("content.1.text").String() != "change direction" {
			t.Fatalf("interrupt text was not preserved: %s", messages[1].Raw)
		}
	})

	t.Run("keeps a completed server call", func(t *testing.T) {
		input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"server_tool_use","id":"srv1","name":"web_search","input":{}},{"type":"web_search_tool_result","tool_use_id":"srv1","content":[]},{"type":"tool_use","id":"client1","name":"Read","input":{}}]},{"role":"user","content":"change direction"}]}`)
		got := RepairDanglingClaudeToolUses(input)
		messages := claudeMessagesForTest(t, got, 2)
		if messages[0].Get("content.0.type").String() != "server_tool_use" || messages[0].Get("content.1.type").String() != "web_search_tool_result" {
			t.Fatalf("completed server call was removed: %s", messages[0].Raw)
		}
	})

	t.Run("keeps an unresolved server call when the user turn has only results", func(t *testing.T) {
		input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"server_tool_use","id":"srv1","name":"web_search","input":{}},{"type":"tool_use","id":"client1","name":"Read","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"client1","content":"done"}]}]}`)
		got := RepairDanglingClaudeToolUses(input)
		if !bytes.Equal(got, input) {
			t.Fatalf("valid server resume history mutated:\n got %s\nwant %s", got, input)
		}
	})
}

func claudeMessagesForTest(t *testing.T, payload []byte, want int) []gjson.Result {
	t.Helper()
	messages := gjson.GetBytes(payload, "messages").Array()
	if len(messages) != want {
		t.Fatalf("message count = %d, want %d: %s", len(messages), want, payload)
	}
	return messages
}

func assertInterruptedClaudeToolResult(t *testing.T, block gjson.Result, toolUseID, toolsetName string) {
	t.Helper()
	if block.Get("type").String() != "tool_result" || block.Get("tool_use_id").String() != toolUseID {
		t.Fatalf("expected interrupted result for %s: %s", toolUseID, block.Raw)
	}
	content := block.Get("content").String()
	if block.Get("content").IsArray() {
		content = block.Get("content.0.text").String()
	}
	if content != interruptedClaudeToolResultContent || !block.Get("is_error").Bool() {
		t.Fatalf("synthetic result is not an error: %s", block.Raw)
	}
	if block.Get("toolset_name").String() != toolsetName {
		t.Fatalf("toolset_name = %q, want %q: %s", block.Get("toolset_name").String(), toolsetName, block.Raw)
	}
}
