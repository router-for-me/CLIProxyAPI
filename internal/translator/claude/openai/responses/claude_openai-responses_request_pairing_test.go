package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

// findToolUseMsgIndex returns the index of the first assistant message whose
// content array contains a tool_use block, or -1.
func findToolUseMsgIndex(msgs []gjson.Result) int {
	for i, m := range msgs {
		if m.Get("role").String() != "assistant" {
			continue
		}
		content := m.Get("content")
		if !content.IsArray() {
			continue
		}
		found := false
		content.ForEach(func(_, b gjson.Result) bool {
			if b.Get("type").String() == "tool_use" {
				found = true
				return false
			}
			return true
		})
		if found {
			return i
		}
	}
	return -1
}

// msgHasToolResult reports whether the message is a user message whose content
// array contains at least one tool_result block.
func msgHasToolResult(m gjson.Result) bool {
	if m.Get("role").String() != "user" {
		return false
	}
	content := m.Get("content")
	if !content.IsArray() {
		return false
	}
	found := false
	content.ForEach(func(_, b gjson.Result) bool {
		if b.Get("type").String() == "tool_result" {
			found = true
			return false
		}
		return true
	})
	return found
}

// TestConvertOpenAIResponses_ReordersInterveningMessageAfterPair verifies that a
// non-tool message inserted between a tool_use and its tool_result (e.g. a
// compact continuation developer message) is moved AFTER the pair so the
// tool_use message is immediately followed by the matching tool_result message.
func TestConvertOpenAIResponses_ReordersInterveningMessageAfterPair(t *testing.T) {
	body := []byte(`{
		"model": "kiro-api/claude-opus-4-7-thinking",
		"input": [
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},
			{"type":"function_call","call_id":"toolu_a","name":"exec_command","arguments":"{\"cmd\":\"ls\"}"},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"[compact] resuming previous session"}]},
			{"type":"function_call_output","call_id":"toolu_a","output":"file1\nfile2"}
		],
		"tools": []
	}`)
	out := ConvertOpenAIResponsesRequestToClaude("kiro-api/claude-opus-4-7-thinking", body, false)
	msgs := gjson.GetBytes(out, "messages").Array()

	tuIdx := findToolUseMsgIndex(msgs)
	if tuIdx < 0 {
		t.Fatalf("no tool_use message found in output: %s", string(out))
	}
	if tuIdx+1 >= len(msgs) {
		t.Fatalf("tool_use message is last; no following message: %s", string(out))
	}
	if !msgHasToolResult(msgs[tuIdx+1]) {
		t.Fatalf("expected tool_result message immediately after tool_use; got role=%q content=%s\nfull=%s",
			msgs[tuIdx+1].Get("role").String(), msgs[tuIdx+1].Get("content").Raw, string(out))
	}
	// The intervening compact message must still be present somewhere after the pair.
	foundCompact := false
	for _, m := range msgs {
		if m.Get("content").String() == "[compact] resuming previous session" {
			foundCompact = true
		}
		m.Get("content").ForEach(func(_, b gjson.Result) bool {
			if b.Get("text").String() == "[compact] resuming previous session" {
				foundCompact = true
			}
			return true
		})
	}
	if !foundCompact {
		t.Fatalf("intervening compact message was dropped: %s", string(out))
	}
}

// TestConvertOpenAIResponses_PairingIdempotent verifies that an already
// correctly-ordered tool_use/tool_result sequence is unchanged by the
// sanitization pass (running it again produces identical bytes).
func TestConvertOpenAIResponses_PairingIdempotent(t *testing.T) {
	body := []byte(`{
		"model": "kiro-api/claude-opus-4-7-thinking",
		"input": [
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},
			{"type":"function_call","call_id":"toolu_a","name":"exec_command","arguments":"{\"cmd\":\"ls\"}"},
			{"type":"function_call_output","call_id":"toolu_a","output":"file1"}
		],
		"tools": []
	}`)
	out := ConvertOpenAIResponsesRequestToClaude("kiro-api/claude-opus-4-7-thinking", body, false)
	again := reorderClaudeToolUseResultPairs(out)
	if string(again) != string(out) {
		t.Fatalf("reorder is not idempotent.\nbefore=%s\nafter =%s", string(out), string(again))
	}
	msgs := gjson.GetBytes(out, "messages").Array()
	tuIdx := findToolUseMsgIndex(msgs)
	if tuIdx < 0 || tuIdx+1 >= len(msgs) || !msgHasToolResult(msgs[tuIdx+1]) {
		t.Fatalf("expected contiguous tool_use/tool_result pair: %s", string(out))
	}
}

// TestConvertOpenAIResponses_ParallelBatchReorder verifies that a parallel
// tool_use batch (multiple tool_use ids in one assistant message) keeps its
// matching multi-result user message together when an intervening message is
// moved out of the way.
func TestConvertOpenAIResponses_ParallelBatchReorder(t *testing.T) {
	body := []byte(`{
		"model": "kiro-api/claude-opus-4-7-thinking",
		"input": [
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},
			{"type":"function_call","call_id":"toolu_a","name":"exec_command","arguments":"{\"cmd\":\"ls\"}"},
			{"type":"function_call","call_id":"toolu_b","name":"exec_command","arguments":"{\"cmd\":\"pwd\"}"},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"[compact] note"}]},
			{"type":"function_call_output","call_id":"toolu_a","output":"a"},
			{"type":"function_call_output","call_id":"toolu_b","output":"b"}
		],
		"tools": []
	}`)
	out := ConvertOpenAIResponsesRequestToClaude("kiro-api/claude-opus-4-7-thinking", body, false)
	msgs := gjson.GetBytes(out, "messages").Array()

	tuIdx := findToolUseMsgIndex(msgs)
	if tuIdx < 0 {
		t.Fatalf("no tool_use message found: %s", string(out))
	}
	useIDs := map[string]bool{}
	msgs[tuIdx].Get("content").ForEach(func(_, b gjson.Result) bool {
		if b.Get("type").String() == "tool_use" {
			useIDs[b.Get("id").String()] = true
		}
		return true
	})
	if !useIDs["toolu_a"] || !useIDs["toolu_b"] {
		t.Fatalf("expected both tool_use ids in batch, got %v: %s", useIDs, string(out))
	}
	if tuIdx+1 >= len(msgs) || !msgHasToolResult(msgs[tuIdx+1]) {
		t.Fatalf("expected tool_result message right after batch tool_use: %s", string(out))
	}
	resIDs := map[string]bool{}
	msgs[tuIdx+1].Get("content").ForEach(func(_, b gjson.Result) bool {
		if b.Get("type").String() == "tool_result" {
			resIDs[b.Get("tool_use_id").String()] = true
		}
		return true
	})
	if !resIDs["toolu_a"] || !resIDs["toolu_b"] {
		t.Fatalf("expected both tool_result ids in batch, got %v: %s", resIDs, string(out))
	}
}

// findToolUseMsgIndexByID returns the index of the first assistant message whose
// content array contains a tool_use block with the given id, or -1.
func findToolUseMsgIndexByID(msgs []gjson.Result, id string) int {
	for i, m := range msgs {
		if m.Get("role").String() != "assistant" {
			continue
		}
		content := m.Get("content")
		if !content.IsArray() {
			continue
		}
		found := false
		content.ForEach(func(_, b gjson.Result) bool {
			if b.Get("type").String() == "tool_use" && b.Get("id").String() == id {
				found = true
				return false
			}
			return true
		})
		if found {
			return i
		}
	}
	return -1
}

// resultIDsInMessage returns the set of tool_result tool_use_ids in a user
// message whose content is an array.
func resultIDsInMessage(m gjson.Result) map[string]bool {
	ids := map[string]bool{}
	if m.Get("role").String() != "user" {
		return ids
	}
	content := m.Get("content")
	if !content.IsArray() {
		return ids
	}
	content.ForEach(func(_, b gjson.Result) bool {
		if b.Get("type").String() == "tool_result" {
			ids[b.Get("tool_use_id").String()] = true
		}
		return true
	})
	return ids
}

// TestConvertOpenAIResponses_ReorderSplitBatchesWithMergedResults reproduces the
// Reviewer bad case: two SEPARATE assistant tool_use batches (use:a then use:b,
// with a compact message wedged between them), whose function_call_output items
// are ADJACENT in the input and therefore get merged by the request converter
// into a SINGLE user message [tool_result:a, tool_result:b].
//
// The old "move the whole user message" strategy pulled [res:a, res:b] after
// use:a, orphaning res:b (use:b ends up with nothing after it) -> exactly the
// Bedrock 400 this pass is meant to prevent. After the fix, each tool_use must
// be immediately followed by a user message holding ONLY its own batch's
// tool_result(s): use:a -> [res:a], use:b -> [res:b], with no orphans.
func TestConvertOpenAIResponses_ReorderSplitBatchesWithMergedResults(t *testing.T) {
	body := []byte(`{
		"model": "kiro-api/claude-opus-4-7-thinking",
		"input": [
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},
			{"type":"function_call","call_id":"toolu_a","name":"exec_command","arguments":"{\"cmd\":\"ls\"}"},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"[compact] resuming previous session"}]},
			{"type":"function_call","call_id":"toolu_b","name":"exec_command","arguments":"{\"cmd\":\"pwd\"}"},
			{"type":"function_call_output","call_id":"toolu_a","output":"outA"},
			{"type":"function_call_output","call_id":"toolu_b","output":"outB"}
		],
		"tools": []
	}`)
	out := ConvertOpenAIResponsesRequestToClaude("kiro-api/claude-opus-4-7-thinking", body, false)
	msgs := gjson.GetBytes(out, "messages").Array()

	aIdx := findToolUseMsgIndexByID(msgs, "toolu_a")
	bIdx := findToolUseMsgIndexByID(msgs, "toolu_b")
	if aIdx < 0 || bIdx < 0 {
		t.Fatalf("missing tool_use messages a=%d b=%d: %s", aIdx, bIdx, string(out))
	}

	// use:a must be immediately followed by a user message containing res:a
	// (and NOT res:b - res:b belongs to use:b's batch).
	if aIdx+1 >= len(msgs) || !msgHasToolResult(msgs[aIdx+1]) {
		t.Fatalf("expected tool_result message right after use:a: %s", string(out))
	}
	aRes := resultIDsInMessage(msgs[aIdx+1])
	if !aRes["toolu_a"] {
		t.Fatalf("res:a not found after use:a, got %v: %s", aRes, string(out))
	}
	if aRes["toolu_b"] {
		t.Fatalf("res:b was wrongly merged after use:a (orphaning use:b): %v\n%s", aRes, string(out))
	}

	// use:b must be immediately followed by a user message containing res:b.
	if bIdx+1 >= len(msgs) || !msgHasToolResult(msgs[bIdx+1]) {
		t.Fatalf("expected tool_result message right after use:b (res:b orphaned!): %s", string(out))
	}
	bRes := resultIDsInMessage(msgs[bIdx+1])
	if !bRes["toolu_b"] {
		t.Fatalf("res:b not found after use:b, got %v: %s", bRes, string(out))
	}
	if bRes["toolu_a"] {
		t.Fatalf("res:a wrongly placed after use:b: %v\n%s", bRes, string(out))
	}

	// The compact message must survive somewhere.
	foundCompact := false
	for _, m := range msgs {
		if m.Get("content").String() == "[compact] resuming previous session" {
			foundCompact = true
		}
	}
	if !foundCompact {
		t.Fatalf("intervening compact message was dropped: %s", string(out))
	}
}

// TestConvertOpenAIResponses_PairingIdempotentAfterRealReorder verifies real
// idempotency (Reviewer issue #4): run reorder once on a sequence that ACTUALLY
// gets reordered, then run it AGAIN on that reordered result and assert the
// bytes are unchanged. This exercises the rebuild path, not just the identity
// short-circuit.
func TestConvertOpenAIResponses_PairingIdempotentAfterRealReorder(t *testing.T) {
	body := []byte(`{
		"model": "kiro-api/claude-opus-4-7-thinking",
		"input": [
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},
			{"type":"function_call","call_id":"toolu_a","name":"exec_command","arguments":"{\"cmd\":\"ls\"}"},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"[compact] resuming previous session"}]},
			{"type":"function_call","call_id":"toolu_b","name":"exec_command","arguments":"{\"cmd\":\"pwd\"}"},
			{"type":"function_call_output","call_id":"toolu_a","output":"outA"},
			{"type":"function_call_output","call_id":"toolu_b","output":"outB"}
		],
		"tools": []
	}`)
	// First pass happens inside Convert; capture the reordered output.
	first := ConvertOpenAIResponsesRequestToClaude("kiro-api/claude-opus-4-7-thinking", body, false)
	// Sanity: the first pass really moved something (otherwise this test would
	// degenerate into the identity short-circuit and prove nothing).
	aIdx := findToolUseMsgIndexByID(gjson.GetBytes(first, "messages").Array(), "toolu_a")
	if aIdx < 0 || aIdx+1 >= len(gjson.GetBytes(first, "messages").Array()) ||
		!msgHasToolResult(gjson.GetBytes(first, "messages").Array()[aIdx+1]) {
		t.Fatalf("precondition: first pass did not produce paired use:a/res:a: %s", string(first))
	}
	// Second pass over the already-reordered bytes must be a no-op.
	second := reorderClaudeToolUseResultPairs(first)
	if string(second) != string(first) {
		t.Fatalf("reorder not idempotent on a truly-reordered sequence.\nfirst =%s\nsecond=%s", string(first), string(second))
	}
}
