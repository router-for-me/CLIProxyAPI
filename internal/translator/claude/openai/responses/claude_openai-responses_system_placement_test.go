package responses

import (
	"context"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

const placementFormatJSON = `"text":{"format":{"type":"json_schema","name":"facts","schema":{"type":"object","properties":{"facts":{"type":"string"}}}}}`

func convertPlacement(t *testing.T, ctx context.Context, model, body string) ojson.Result {
	t.Helper()
	out, errConvert := ConvertOpenAIResponsesRequestToClaudeWithCompatContext(ctx, model, []byte(body), false)
	if errConvert != nil {
		t.Fatal(errConvert)
	}
	return util.ParseGJSONBytesNoCopy(out)
}

func placementMessages(root ojson.Result) [][]byte {
	var messages [][]byte
	root.Get("messages").ForEach(func(_, message ojson.Result) bool {
		messages = append(messages, []byte(message.Raw))
		return true
	})
	return messages
}

// Codex appends system-level items to the end of input on later turns. The
// messages translated for one turn must stay a prefix of the next turn's
// messages and the top-level system blocks must not change, or Claude's prompt
// cache misses on everything after the first changed block.
func TestResponsesMidSessionSystemItemsKeepPromptPrefixBetweenTurns(t *testing.T) {
	turnTwo := `[
		{"type": "message", "role": "developer", "content": [{"type": "input_text", "text": "base instructions"}]},
		{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "first request"}]},
		{"type": "function_call", "call_id": "call_1", "name": "shell", "arguments": "{}"},
		{"type": "function_call_output", "call_id": "call_1", "output": "ok"},
		{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "first answer"}]},
		{"type": "message", "role": "developer", "content": [{"type": "input_text", "text": "<time_context>2026-11-06</time_context>"}]},
		{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "second request"}]}
	]`
	turnThree := strings.TrimSuffix(strings.TrimSpace(turnTwo), "]") + `,
		{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "second answer"}]},
		{"type": "message", "role": "developer", "content": [{"type": "input_text", "text": "<skills>changed</skills>"}]},
		{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "third request"}]}
	]`

	two := convertPlacement(t, context.Background(), "claude-opus-5", `{"model":"claude-opus-5","input":`+turnTwo+`}`)
	three := convertPlacement(t, context.Background(), "claude-opus-5", `{"model":"claude-opus-5","input":`+turnThree+`}`)

	if two.Get("system").Raw != three.Get("system").Raw {
		t.Fatalf("top-level system changed between turns:\n%s\n%s", two.Get("system").Raw, three.Get("system").Raw)
	}
	twoMessages := two.Get("messages").Array()
	threeMessages := three.Get("messages").Array()
	if len(threeMessages) <= len(twoMessages) {
		t.Fatalf("turn three has %d messages, want more than turn two's %d", len(threeMessages), len(twoMessages))
	}
	for idx, message := range twoMessages {
		if message.Raw != threeMessages[idx].Raw {
			t.Fatalf("messages[%d] changed between turns:\n%s\n%s", idx, message.Raw, threeMessages[idx].Raw)
		}
	}
	if got := three.Get("messages.#.role").Raw; got != `["user","assistant","user","assistant","user","system","assistant","user","system"]` {
		t.Fatalf("roles = %s", got)
	}
	if got := three.Get("messages.5.content.0.text").String(); got != "<time_context>2026-11-06</time_context>" {
		t.Fatalf("messages[5] text = %q", got)
	}
	if got := three.Get("messages.8.content.0.text").String(); got != "<skills>changed</skills>" {
		t.Fatalf("messages[8] text = %q", got)
	}
	if problems := claudeMessageInvariantProblems(placementMessages(three)); len(problems) > 0 {
		t.Fatalf("invariant problems: %v", problems)
	}
}

// A queued turn attaches to the next user message emitted, which may be the
// user message still being accumulated when the item arrives.
func TestResponsesMidSessionSystemItemFollowsPendingUserMessage(t *testing.T) {
	root := convertPlacement(t, context.Background(), "claude-opus-5", `{
		"model": "claude-opus-5",
		"instructions": "I1",
		"input": [
			{"type": "message", "role": "system", "content": [{"type": "input_text", "text": "S1"}]},
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "U1"}]},
			{"type": "message", "role": "developer", "content": "D1"},
			{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "A1"}]},
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "U2"}]}
		]
	}`)
	if got := root.Get("system.#.text").Raw; got != `["I1","S1"]` {
		t.Fatalf("system = %s, want only the leading items", got)
	}
	if got := root.Get("messages.#.role").Raw; got != `["user","system","assistant","user"]` {
		t.Fatalf("roles = %s, messages: %s", got, root.Get("messages").Raw)
	}
	if got := root.Get("messages.1.content.0.text").String(); got != "D1" {
		t.Fatalf("in-place turn text = %q, want D1", got)
	}
}

// A system-level item between a tool call and its output must not separate
// tool_use from tool_result. It follows the tool_result message.
func TestResponsesMidSessionSystemItemWaitsForToolResult(t *testing.T) {
	root := convertPlacement(t, context.Background(), "claude-opus-5", `{
		"model": "claude-opus-5",
		"input": [
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "run it"}]},
			{"type": "function_call", "call_id": "call_1", "name": "shell", "arguments": "{}"},
			{"type": "message", "role": "developer", "content": [{"type": "input_text", "text": "prefix approved"}]},
			{"type": "function_call_output", "call_id": "call_1", "output": "done"},
			{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "finished"}]},
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "thanks"}]}
		]
	}`)
	if got := root.Get("messages.#.role").Raw; got != `["user","assistant","user","system","assistant","user"]` {
		t.Fatalf("roles = %s, messages: %s", got, root.Get("messages").Raw)
	}
	if got := root.Get("messages.2.content.0.type").String(); got != "tool_result" {
		t.Fatalf("messages[2] first block = %q, want tool_result", got)
	}
	if root.Get("system").Exists() {
		t.Fatalf("system must stay empty: %s", root.Get("system").Raw)
	}
	if problems := claudeMessageInvariantProblems(placementMessages(root)); len(problems) > 0 {
		t.Fatalf("invariant problems: %v", problems)
	}
}

// Items with a part a role=system turn cannot carry, items that are not
// messages, and items no user message follows keep the top-level path. The
// structured-output instruction stays the last system block.
func TestResponsesMidSessionSystemItemTopLevelFallbacks(t *testing.T) {
	root := convertPlacement(t, context.Background(), "claude-opus-5", `{
		"model": "claude-opus-5",
		`+placementFormatJSON+`,
		"input": [
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "hello"}]},
			{"type": "custom_note", "role": "developer", "content": "typed"},
			{"type": "message", "role": "developer", "content": [
				{"type": "input_text", "text": "see image"},
				{"type": "input_image", "image_url": "https://example.com/a.png"}
			]},
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "next"}]},
			{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "answer"}]},
			{"type": "message", "role": "developer", "content": [{"type": "input_text", "text": "trailing"}]},
			{"type": "custom_note", "role": "developer", "content": "after_trailing"}
		]
	}`)
	if strings.Contains(root.Get("messages").Raw, `"role":"system"`) {
		t.Fatalf("no item may become a role=system turn: %s", root.Get("messages").Raw)
	}
	types := root.Get("system.#.type").Raw
	if types != `["text","text","input_image","text","text","text"]` {
		t.Fatalf("system block types = %s, system: %s", types, root.Get("system").Raw)
	}
	if got := root.Get("system.0.text").String(); got != "typed" {
		t.Fatalf("system[0] = %q, want the non-message item", got)
	}
	if got := root.Get("system.3.text").String(); got != "trailing" {
		t.Fatalf("system[3] = %q, want the trailing item before after_trailing", got)
	}
	if got := root.Get("system.4.text").String(); got != "after_trailing" {
		t.Fatalf("system[4] = %q, want after_trailing in source order", got)
	}
	if got := root.Get("system.5.text").String(); !strings.Contains(got, "facts") {
		t.Fatalf("structured-output instruction must stay last, got %q", got)
	}
}

// A request carrying only a structured-output format keeps its fallback user
// message now that the instruction is appended after the input loop.
func TestResponsesStructuredOutputOnlyKeepsFallbackUserMessage(t *testing.T) {
	root := convertPlacement(t, context.Background(), "claude-opus-5", `{"model":"claude-opus-5",`+placementFormatJSON+`}`)
	if got := len(root.Get("system").Array()); got != 1 {
		t.Fatalf("system blocks = %d, want 1: %s", got, root.Get("system").Raw)
	}
	if got := root.Get("messages.#.role").Raw; got != `["user"]` {
		t.Fatalf("roles = %s, want the fallback user message", got)
	}
}

// In-place turns keep cache_control exactly as top-level blocks do: block-level
// markers stay, a blank part's marker moves to the preceding block, and the
// item's marker lands on the last block unless that block already has one.
func TestResponsesMidSessionSystemItemKeepsCacheControl(t *testing.T) {
	root := convertPlacement(t, context.Background(), "claude-opus-5", `{
		"model": "claude-opus-5",
		"input": [
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "hi"}]},
			{"type": "message", "role": "developer", "cache_control": {"type": "ephemeral", "ttl": "1h"}, "content": [
				{"type": "input_text", "text": "first", "cache_control": {"type": "ephemeral"}},
				{"type": "input_text", "text": "second"},
				{"type": "input_text", "text": " ", "cache_control": {"type": "ephemeral", "ttl": "5m"}}
			]},
			{"type": "message", "role": "developer", "cache_control": {"type": "ephemeral"}, "content": [
				{"type": "input_text", "text": "third"}
			]},
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "go"}]}
		]
	}`)
	if got := root.Get("messages.#.role").Raw; got != `["user","system","system"]` {
		t.Fatalf("roles = %s, messages: %s", got, root.Get("messages").Raw)
	}
	first := root.Get("messages.1.content")
	if got := first.Get("0.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("block-level marker lost: %s", first.Raw)
	}
	if got := first.Get("1.cache_control.ttl").String(); got != "5m" {
		t.Fatalf("blank part marker must move to the preceding block: %s", first.Raw)
	}
	if got := len(first.Array()); got != 2 {
		t.Fatalf("blank part must not become a block: %s", first.Raw)
	}
	if got := root.Get("messages.2.content.0.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("item-level marker lost: %s", root.Get("messages.2").Raw)
	}
}

// Without a pinned decision, the requested model decides; a pinned decision
// overrides it in both directions, and the top-level path keeps source order.
func TestResponsesSystemItemPlacementFollowsPinnedDecision(t *testing.T) {
	body := `{
		"model": "m",
		"instructions": "I1",
		` + placementFormatJSON + `,
		"input": [
			{"type": "message", "role": "system", "content": [{"type": "input_text", "text": "S1"}]},
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "U1"}]},
			{"type": "message", "role": "developer", "content": "D1"},
			{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "A1"}]},
			{"type": "message", "role": "system", "content": [{"type": "input_text", "text": "S2"}]},
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "U2"}]}
		]
	}`
	for _, test := range []struct {
		name         string
		ctx          context.Context
		model        string
		wantTopLevel bool
	}{
		{name: "legacy model by default", ctx: context.Background(), model: "claude-sonnet-4-5", wantTopLevel: true},
		{name: "supported model by default", ctx: context.Background(), model: "claude-opus-5", wantTopLevel: false},
		{name: "pinned top-level", ctx: WithTopLevelSystemItems(context.Background(), true), model: "claude-opus-5", wantTopLevel: true},
		{name: "pinned in place", ctx: WithTopLevelSystemItems(context.Background(), false), model: "claude-sonnet-4-5", wantTopLevel: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := convertPlacement(t, test.ctx, test.model, body)
			texts := root.Get("system.#.text").Array()
			if len(texts) == 0 || !strings.Contains(texts[len(texts)-1].String(), "facts") {
				t.Fatalf("structured-output instruction must stay last: %s", root.Get("system").Raw)
			}
			gotSystem := root.Get("system.#.text").Raw
			gotRoles := root.Get("messages.#.role").Raw
			if test.wantTopLevel {
				if !strings.HasPrefix(gotSystem, `["I1","S1","D1","S2",`) || strings.Contains(gotRoles, "system") {
					t.Fatalf("want every item top-level in source order; system=%s roles=%s", gotSystem, gotRoles)
				}
				return
			}
			if !strings.HasPrefix(gotSystem, `["I1","S1",`) || gotRoles != `["user","system","assistant","user","system"]` {
				t.Fatalf("want D1 and S2 in place; system=%s roles=%s", gotSystem, gotRoles)
			}
		})
	}
}
