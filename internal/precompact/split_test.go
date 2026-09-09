package precompact

import (
	"encoding/json"
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func claudeBodyWithTurns(n int) []byte {
	msgs := make([]map[string]any, 0, n*2)
	for i := 0; i < n; i++ {
		msgs = append(msgs, map[string]any{"role": "user", "content": "question " + itoaTest(i)})
		msgs = append(msgs, map[string]any{"role": "assistant", "content": "answer " + itoaTest(i)})
	}
	body, _ := json.Marshal(map[string]any{
		"model":      "claude-3",
		"system":     "you are helpful",
		"tools":      []any{map[string]any{"name": "bash"}},
		"max_tokens": 1024,
		"messages":   msgs,
	})
	return body
}

func itoaTest(i int) string {
	return string(rune('0' + i%10))
}

func TestSplitMessagesKeepsRecentTurnsVerbatim(t *testing.T) {
	body := claudeBodyWithTurns(10)
	sp, ok := SplitMessages(sdktranslator.FormatClaude, body, 3)
	if !ok {
		t.Fatal("expected a split")
	}
	if len(sp.Recent) == 0 {
		t.Fatal("expected recent messages")
	}
	// The last 3 user turns means the last 6 messages (user+assistant pairs), possibly plus a
	// trailing assistant-less turn. Verify recent block is a byte-identical suffix of the raw messages.
	msgs := gjson.GetBytes(body, "messages").Array()
	tail := msgs[len(msgs)-len(sp.Recent):]
	for i, m := range tail {
		if m.Raw != sp.Recent[i] {
			t.Fatalf("recent[%d] not byte-identical:\n got: %s\nwant: %s", i, sp.Recent[i], m.Raw)
		}
	}
}

func TestSplitMessagesNoSplitWhenTooFewTurns(t *testing.T) {
	body := claudeBodyWithTurns(2)
	_, ok := SplitMessages(sdktranslator.FormatClaude, body, 6)
	if ok {
		t.Fatal("expected no split when fewer turns exist than keepTurns")
	}
}

func TestSplitMessagesNeverOrphansToolUse(t *testing.T) {
	// A user turn carrying a tool_result must not be treated as a fresh turn boundary,
	// so the cut never lands between a tool_use and its tool_result.
	msgs := []map[string]any{
		{"role": "user", "content": "do a thing"},
		{"role": "assistant", "content": []map[string]any{{"type": "tool_use", "id": "t1", "name": "bash", "input": map[string]any{}}}},
		{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": "t1", "content": "ok"}}},
		{"role": "assistant", "content": "done"},
		{"role": "user", "content": "next question"},
		{"role": "assistant", "content": "next answer"},
	}
	body, _ := json.Marshal(map[string]any{"system": "s", "messages": msgs})
	sp, ok := SplitMessages(sdktranslator.FormatClaude, body, 1)
	if !ok {
		t.Fatal("expected a split")
	}
	// The tool_result-carrying "user" message must stay paired with its tool_use in the same
	// partition (either both in Middle or both in Recent), never split across the cut.
	allMsgs := gjson.ParseBytes(body).Get("messages").Array()
	toolUseIdx, toolResultIdx := -1, -1
	for i, m := range allMsgs {
		for _, block := range m.Get("content").Array() {
			if block.Get("type").String() == "tool_use" {
				toolUseIdx = i
			}
			if block.Get("type").String() == "tool_result" {
				toolResultIdx = i
			}
		}
	}
	if toolUseIdx < 0 || toolResultIdx < 0 {
		t.Fatal("fixture missing tool_use/tool_result")
	}
	inRecent := func(idx int) bool { return idx >= len(allMsgs)-len(sp.Recent) }
	if inRecent(toolUseIdx) != inRecent(toolResultIdx) {
		t.Fatalf("tool_use (idx %d) and tool_result (idx %d) landed on opposite sides of the cut", toolUseIdx, toolResultIdx)
	}
}

func TestRebuildPreservesEverythingOutsideMessages(t *testing.T) {
	body := claudeBodyWithTurns(10)
	sp, ok := SplitMessages(sdktranslator.FormatClaude, body, 3)
	if !ok {
		t.Fatal("expected a split")
	}
	out, err := Rebuild(sdktranslator.FormatClaude, body, sp, "a short summary")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, field := range []string{"model", "system", "tools", "max_tokens"} {
		if gjson.GetBytes(out, field).Raw != gjson.GetBytes(body, field).Raw {
			t.Fatalf("field %q changed by Rebuild", field)
		}
	}
	msgs := gjson.GetBytes(out, "messages").Array()
	if len(msgs) < len(sp.Recent)+2 {
		t.Fatalf("expected summary turn plus recent turns, got %d messages", len(msgs))
	}
}

func TestTranscriptDropsThinkingBlocks(t *testing.T) {
	msg := `{"role":"assistant","content":[{"type":"thinking","thinking":"secret reasoning"},{"type":"text","text":"visible answer"}]}`
	out := Transcript([]string{msg})
	if contains(out, "secret reasoning") {
		t.Fatal("thinking content leaked into transcript")
	}
	if !contains(out, "visible answer") {
		t.Fatal("visible text missing from transcript")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
