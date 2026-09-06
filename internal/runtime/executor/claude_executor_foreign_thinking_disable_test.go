package executor

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

const foreignStripClaudeCAIS = "CAISqwIKiAEIEBgCKkBHRlRBsNiptQUWfPoOhuQKwi5LnncZVO9bB5jqOs76D7uBtgktML0zqJtNmLHXHHcgD6lk4MQu4QBXzFd1lbC3Mg5jbGF1ZGUtZmFibGUtNTgBQgh0aGlua2luZ1okZDk3NDM5NzUtNGJiMC00OTM2LTllMjgtZDViMGQyMWJkYzQ4EgxCGh+XVFFFeySAjtAaDL/A1LltGu6MMJ+eXSIwsN0oBpDrqLv22UBfkMnTotnIbkvkOyb9xZHgigG6OZVHaI3gThm+maLKmgO5PrFLKlDFYp+YZksy/wKwszJlnLTPzAK+NUlfzagOE1ymtZTXhAYK260XyFYmg/te/C231+Fr/hoX+EJoUBnrn0gD7hqMISOT+TaFEuOXYsN517GfaxgB"

func TestSanitizeClaudeMessagesForClaudeUpstreamWithDebug_DisablesThinkingAfterForeignStrip(t *testing.T) {
	ctx := context.Background()

	t.Run("foreign unsigned non-empty thinking", func(t *testing.T) {
		body := []byte(`{
			"thinking":{"type":"adaptive","budget_tokens":2048},
			"output_config":{"effort":"high","format":{"type":"json"}},
			"messages":[
				{"role":"assistant","content":[
					{"type":"thinking","thinking":"foreign reasoning"},
					{"type":"text","text":"answer"}
				]},
				{"role":"user","content":[{"type":"text","text":"next"}]}
			]
		}`)
		out := sanitizeClaudeMessagesForClaudeUpstreamWithDebug(ctx, body, "claude-sonnet-4-5")
		assertThinkingDisabledAndBlocksStripped(t, out)
		if got := gjson.GetBytes(out, "output_config.format.type").String(); got != "json" {
			t.Fatalf("output_config.format should be preserved, got %q: %s", got, out)
		}
		if got := gjson.GetBytes(out, "messages.0.content.0.text").String(); got != "answer" {
			t.Fatalf("non-thinking content should be preserved, got %q: %s", got, out)
		}
	})

	t.Run("incompatible gemini-carrier signature", func(t *testing.T) {
		body := []byte(`{
			"thinking":{"type":"adaptive"},
			"output_config":{"effort":"high"},
			"messages":[
				{"role":"assistant","content":[
					{"type":"thinking","thinking":"gemini thought","signature":"skip_thought_signature_validator"},
					{"type":"redacted_thinking","data":"keep-until-downgrade"},
					{"type":"text","text":"answer"}
				]}
			]
		}`)
		out := sanitizeClaudeMessagesForClaudeUpstreamWithDebug(ctx, body, "claude-sonnet-4-5")
		assertThinkingDisabledAndBlocksStripped(t, out)
		if gjson.GetBytes(out, "output_config").Exists() {
			t.Fatalf("empty output_config should be deleted: %s", out)
		}
		if got := gjson.GetBytes(out, "messages.0.content.0.text").String(); got != "answer" {
			t.Fatalf("remaining text = %q, want answer: %s", got, out)
		}
	})

	t.Run("empty placeholder drop only does not downgrade", func(t *testing.T) {
		body := []byte(`{
			"thinking":{"type":"adaptive"},
			"output_config":{"effort":"high"},
			"messages":[
				{"role":"assistant","content":[
					{"type":"thinking","thinking":"","signature":""},
					{"type":"text","text":"answer"}
				]}
			]
		}`)
		out := sanitizeClaudeMessagesForClaudeUpstreamWithDebug(ctx, body, "claude-sonnet-4-5")
		assertThinkingStillActive(t, out, "adaptive", "high")
		if gjson.GetBytes(out, "messages.0.content.#(type==thinking)").Exists() {
			t.Fatalf("empty placeholder should still be dropped: %s", out)
		}
		if got := gjson.GetBytes(out, "messages.0.content.0.text").String(); got != "answer" {
			t.Fatalf("remaining text = %q, want answer: %s", got, out)
		}
	})

	t.Run("legal Claude CAIS only does not downgrade", func(t *testing.T) {
		body := []byte(`{
			"thinking":{"type":"adaptive"},
			"output_config":{"effort":"high"},
			"messages":[
				{"role":"assistant","content":[
					{"type":"thinking","thinking":"keep","signature":"` + foreignStripClaudeCAIS + `"},
					{"type":"text","text":"answer"}
				]}
			]
		}`)
		out := sanitizeClaudeMessagesForClaudeUpstreamWithDebug(ctx, body, "claude-fable-5")
		assertThinkingStillActive(t, out, "adaptive", "high")
		thinking := gjson.GetBytes(out, "messages.0.content.0")
		if thinking.Get("type").String() != "thinking" {
			t.Fatalf("legal CAIS thinking block should be preserved: %s", out)
		}
		if got := thinking.Get("signature").String(); got != foreignStripClaudeCAIS {
			t.Fatalf("CAIS signature = %q, want preserved", got)
		}
	})

	t.Run("kimi model skips sanitize and leaves body unchanged", func(t *testing.T) {
		body := []byte(`{"thinking":{"type":"adaptive"},"output_config":{"effort":"high"},"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"keep","signature":"skip_thought_signature_validator"},{"type":"text","text":"hello"}]}]}`)
		out := sanitizeClaudeMessagesForClaudeUpstreamWithDebug(ctx, body, "kimi-k2.5")
		if string(out) != string(body) {
			t.Fatalf("kimi body should be unchanged\n got: %s\nwant: %s", out, body)
		}
	})

	t.Run("foreign drop strips leftover legal thinking so disabled thinking is not mixed with CAIS", func(t *testing.T) {
		body := []byte(`{
			"thinking":{"type":"adaptive"},
			"output_config":{"effort":"high"},
			"messages":[
				{"role":"assistant","content":[
					{"type":"thinking","thinking":"foreign reasoning"},
					{"type":"thinking","thinking":"keep","signature":"` + foreignStripClaudeCAIS + `"},
					{"type":"text","text":"answer"}
				]}
			]
		}`)
		out := sanitizeClaudeMessagesForClaudeUpstreamWithDebug(ctx, body, "claude-fable-5")
		assertThinkingDisabledAndBlocksStripped(t, out)
		if gjson.GetBytes(out, "messages.0.content.#(type==thinking)").Exists() {
			t.Fatalf("leftover legal CAIS must be stripped after foreign drop: %s", out)
		}
	})
}

func TestDisableThinkingIfToolChoiceForced_StillRemovesThinkingBeforeSanitize(t *testing.T) {
	payload := []byte(`{
		"thinking":{"type":"adaptive"},
		"output_config":{"effort":"high"},
		"tool_choice":{"type":"any"},
		"messages":[
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"foreign reasoning"},
				{"type":"text","text":"answer"}
			]}
		]
	}`)
	out := disableThinkingIfToolChoiceForced(payload)
	out = sanitizeClaudeMessagesForClaudeUpstreamWithDebug(context.Background(), out, "claude-sonnet-4-5")
	if gjson.GetBytes(out, "thinking").Exists() {
		t.Fatalf("thinking should remain removed after forced tool_choice, got %s", gjson.GetBytes(out, "thinking").Raw)
	}
	if gjson.GetBytes(out, "output_config.effort").Exists() {
		t.Fatalf("output_config.effort should remain removed after forced tool_choice: %s", out)
	}
	if gjson.GetBytes(out, "messages.0.content.#(type==thinking)").Exists() {
		t.Fatalf("foreign thinking block should still be stripped: %s", out)
	}
}

func assertThinkingDisabledAndBlocksStripped(t *testing.T, body []byte) {
	t.Helper()
	if got := gjson.GetBytes(body, "thinking.type").String(); got != "disabled" {
		t.Fatalf("thinking.type = %q, want disabled: %s", got, body)
	}
	if gjson.GetBytes(body, "thinking.budget_tokens").Exists() {
		t.Fatalf("thinking.budget_tokens should be cleared: %s", body)
	}
	if gjson.GetBytes(body, "output_config.effort").Exists() {
		t.Fatalf("output_config.effort should be cleared: %s", body)
	}
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return
	}
	for i, message := range messages.Array() {
		content := message.Get("content")
		if !content.IsArray() {
			continue
		}
		for j, part := range content.Array() {
			switch part.Get("type").String() {
			case "thinking", "redacted_thinking":
				t.Fatalf("messages[%d].content[%d] still has %s: %s", i, j, part.Get("type").String(), body)
			}
		}
	}
}

func assertThinkingStillActive(t *testing.T, body []byte, wantType, wantEffort string) {
	t.Helper()
	if got := gjson.GetBytes(body, "thinking.type").String(); got != wantType {
		t.Fatalf("thinking.type = %q, want %q: %s", got, wantType, body)
	}
	if got := gjson.GetBytes(body, "output_config.effort").String(); got != wantEffort {
		t.Fatalf("output_config.effort = %q, want %q: %s", got, wantEffort, body)
	}
}
