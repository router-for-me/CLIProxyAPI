package multiagentv2

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func testRewriteCodexOrphan(payload []byte, enabled bool) []byte {
	headers := http.Header{"X-Openai-Subagent": []string{"collab_spawn"}}
	return RewriteCodexOrphanDelegationInput(context.Background(), headers, payload, enabled)
}

func TestRewriteCodexOrphanDelegationInput(t *testing.T) {
	t.Run("disabled leaves payload unchanged", func(t *testing.T) {
		payload := []byte(`{
			"model": "deepseek-v4-pro",
			"input": [
				{
					"type": "function_call_output",
					"name": "create_thread",
					"namespace": "codex_app",
					"output": "<codex_delegation><message>handoff</message></codex_delegation>"
				}
			]
		}`)
		got := testRewriteCodexOrphan(payload, false)
		if string(got) != string(payload) {
			t.Fatalf("expected payload unchanged when disabled, got: %s", string(got))
		}
	})

	t.Run("missing subagent header leaves payload unchanged", func(t *testing.T) {
		payload := []byte(`{
			"model": "deepseek-v4-pro",
			"input": [
				{
					"type": "function_call_output",
					"name": "create_thread",
					"namespace": "codex_app",
					"output": "<codex_delegation><message>handoff</message></codex_delegation>"
				}
			]
		}`)
		got := RewriteCodexOrphanDelegationInput(context.Background(), http.Header{}, payload, true)
		if string(got) != string(payload) {
			t.Fatalf("expected payload unchanged without header, got: %s", string(got))
		}
	})

	t.Run("different subagent header leaves payload unchanged", func(t *testing.T) {
		payload := []byte(`{
			"model": "deepseek-v4-pro",
			"input": [
				{
					"type": "function_call_output",
					"name": "create_thread",
					"namespace": "codex_app",
					"output": "<codex_delegation><message>handoff</message></codex_delegation>"
				}
			]
		}`)
		headers := http.Header{"X-Openai-Subagent": []string{"other_subagent"}}
		got := RewriteCodexOrphanDelegationInput(context.Background(), headers, payload, true)
		if string(got) != string(payload) {
			t.Fatalf("expected payload unchanged with wrong header, got: %s", string(got))
		}
	})

	t.Run("rewrites orphan create_thread without call_id", func(t *testing.T) {
		payload := []byte(`{
			"model": "deepseek-v4-pro",
			"input": [
				{
					"type": "function_call_output",
					"name": "create_thread",
					"namespace": "codex_app",
					"output": "<codex_delegation><message>handoff</message></codex_delegation>"
				},
				{
					"type": "message",
					"role": "user",
					"content": [{"type": "input_text", "text": "please continue"}]
				}
			]
		}`)
		got := testRewriteCodexOrphan(payload, true)
		parsed := gjson.ParseBytes(got)

		item0 := parsed.Get("input.0")
		if item0.Get("type").String() != "message" {
			t.Fatalf("input.0.type = %q, want %q", item0.Get("type").String(), "message")
		}
		if item0.Get("role").String() != "user" {
			t.Fatalf("input.0.role = %q, want %q", item0.Get("role").String(), "user")
		}
		wantText := "Tool output from codex_app__create_thread:\n<codex_delegation><message>handoff</message></codex_delegation>"
		if text := item0.Get("content.0.text").String(); text != wantText {
			t.Fatalf("input.0.content.0.text = %q, want %q", text, wantText)
		}

		item1 := parsed.Get("input.1")
		if item1.Get("type").String() != "message" || item1.Get("content.0.text").String() != "please continue" {
			t.Fatalf("input.1 corrupted: %s", item1.Raw)
		}
	})

	t.Run("case-insensitive header key and value works", func(t *testing.T) {
		payload := []byte(`{
			"model": "deepseek-v4-pro",
			"input": [
				{
					"type": "function_call_output",
					"name": "create_thread",
					"namespace": "codex_app",
					"output": "msg"
				}
			]
		}`)
		headers := http.Header{"x-openai-subagent": []string{"COLLAB_SPAWN"}}
		got := RewriteCodexOrphanDelegationInput(context.Background(), headers, payload, true)
		parsed := gjson.ParseBytes(got)

		item0 := parsed.Get("input.0")
		if item0.Get("type").String() != "message" || item0.Get("role").String() != "user" {
			t.Fatalf("case-insensitive header should rewrite, got: %s", item0.Raw)
		}
	})

	t.Run("rewrites orphan send_message_to_thread with stale call_id", func(t *testing.T) {
		payload := []byte(`{
			"model": "deepseek-v4-pro",
			"input": [
				{
					"type": "function_call_output",
					"call_id": "call_stale_123",
					"name": "send_message_to_thread",
					"namespace": "codex_app",
					"output": "<codex_delegation>msg</codex_delegation>"
				}
			]
		}`)
		got := testRewriteCodexOrphan(payload, true)
		parsed := gjson.ParseBytes(got)

		item0 := parsed.Get("input.0")
		if item0.Get("type").String() != "message" || item0.Get("role").String() != "user" {
			t.Fatalf("input.0 not rewritten: %s", item0.Raw)
		}
		wantText := "Tool output from codex_app__send_message_to_thread:\n<codex_delegation>msg</codex_delegation>"
		if text := item0.Get("content.0.text").String(); text != wantText {
			t.Fatalf("input.0.content.0.text = %q, want %q", text, wantText)
		}
	})

	t.Run("preserves paired create_thread tool call", func(t *testing.T) {
		payload := []byte(`{
			"model": "deepseek-v4-pro",
			"input": [
				{
					"type": "function_call",
					"call_id": "call_active_123",
					"name": "create_thread",
					"namespace": "codex_app",
					"arguments": "{}"
				},
				{
					"type": "function_call_output",
					"call_id": "call_active_123",
					"name": "create_thread",
					"namespace": "codex_app",
					"output": "<codex_delegation>valid</codex_delegation>"
				}
			]
		}`)
		got := testRewriteCodexOrphan(payload, true)
		parsed := gjson.ParseBytes(got)

		item1 := parsed.Get("input.1")
		if item1.Get("type").String() != "function_call_output" {
			t.Fatalf("paired call output was modified: %s", item1.Raw)
		}
		if item1.Get("call_id").String() != "call_active_123" {
			t.Fatalf("call_id changed: %s", item1.Raw)
		}
	})

	t.Run("preserves non-whitelisted tools", func(t *testing.T) {
		payload := []byte(`{
			"model": "deepseek-v4-pro",
			"input": [
				{
					"type": "function_call_output",
					"name": "automation_update",
					"namespace": "codex_app",
					"output": "ignored"
				},
				{
					"type": "function_call_output",
					"name": "create_thread",
					"namespace": "other_namespace",
					"output": "ignored"
				}
			]
		}`)
		got := testRewriteCodexOrphan(payload, true)
		parsed := gjson.ParseBytes(got)

		if parsed.Get("input.0.type").String() != "function_call_output" {
			t.Fatalf("automation_update should not be rewritten: %s", parsed.Get("input.0").Raw)
		}
		if parsed.Get("input.1.type").String() != "function_call_output" {
			t.Fatalf("other_namespace should not be rewritten: %s", parsed.Get("input.1").Raw)
		}
	})

	t.Run("handles empty output", func(t *testing.T) {
		payload := []byte(`{
			"model": "deepseek-v4-pro",
			"input": [
				{
					"type": "function_call_output",
					"name": "create_thread",
					"namespace": "codex_app",
					"output": ""
				}
			]
		}`)
		got := testRewriteCodexOrphan(payload, true)
		parsed := gjson.ParseBytes(got)

		item0 := parsed.Get("input.0")
		if item0.Get("type").String() != "message" || item0.Get("role").String() != "user" {
			t.Fatalf("input.0 not rewritten: %s", item0.Raw)
		}
		wantText := "Tool output from codex_app__create_thread:\n"
		if text := item0.Get("content.0.text").String(); text != wantText {
			t.Fatalf("input.0.content.0.text = %q, want %q", text, wantText)
		}
	})

	t.Run("call_id whitespace difference is not paired", func(t *testing.T) {
		payload := []byte(`{
			"model": "deepseek-v4-pro",
			"input": [
				{
					"type": "function_call",
					"call_id": "call_1",
					"name": "create_thread",
					"namespace": "codex_app",
					"arguments": "{}"
				},
				{
					"type": "function_call_output",
					"call_id": " call_1 ",
					"name": "create_thread",
					"namespace": "codex_app",
					"output": "mismatch"
				}
			]
		}`)
		got := testRewriteCodexOrphan(payload, true)
		parsed := gjson.ParseBytes(got)

		if parsed.Get("input.1.type").String() != "message" {
			t.Fatalf("call_id with whitespace mismatch should be treated as orphan: %s", parsed.Get("input.1").Raw)
		}
	})

	t.Run("preserves structured image output in delegation as exact text", func(t *testing.T) {
		rawOutput := `[{"type":"input_text","text":"diagram"},{"type":"input_image","image_url":"https://example.com/img.png"}]`
		payload := []byte(`{
			"model": "deepseek-v4-pro",
			"input": [
				{
					"type": "function_call_output",
					"name": "create_thread",
					"namespace": "codex_app",
					"output": ` + rawOutput + `
				}
			]
		}`)
		got := testRewriteCodexOrphan(payload, true)
		parsed := gjson.ParseBytes(got)

		item0 := parsed.Get("input.0")
		if item0.Get("type").String() != "message" || item0.Get("role").String() != "user" {
			t.Fatalf("input.0 not rewritten: %s", item0.Raw)
		}
		wantText := "Tool output from codex_app__create_thread:\n" + rawOutput
		if text := item0.Get("content.0.text").String(); text != wantText {
			t.Fatalf("input.0.content.0.text = %q, want %q", text, wantText)
		}
	})

	t.Run("call and output in same request are paired regardless of order", func(t *testing.T) {
		payload := []byte(`{
			"model": "deepseek-v4-pro",
			"input": [
				{
					"type": "function_call_output",
					"call_id": "call_future_1",
					"name": "create_thread",
					"namespace": "codex_app",
					"output": "early"
				},
				{
					"type": "function_call",
					"call_id": "call_future_1",
					"name": "create_thread",
					"namespace": "codex_app",
					"arguments": "{}"
				}
			]
		}`)
		got := testRewriteCodexOrphan(payload, true)
		parsed := gjson.ParseBytes(got)

		if parsed.Get("input.0.type").String() != "function_call_output" {
			t.Fatalf("output should remain paired function_call_output: %s", parsed.Get("input.0").Raw)
		}
		if parsed.Get("input.1.type").String() != "function_call" {
			t.Fatalf("call should remain function_call: %s", parsed.Get("input.1").Raw)
		}
	})

	t.Run("duplicate output consumes call once; second output is orphan", func(t *testing.T) {
		payload := []byte(`{
			"model": "deepseek-v4-pro",
			"input": [
				{
					"type": "function_call",
					"call_id": "call_once",
					"name": "create_thread",
					"namespace": "codex_app",
					"arguments": "{}"
				},
				{
					"type": "function_call_output",
					"call_id": "call_once",
					"name": "create_thread",
					"namespace": "codex_app",
					"output": "first"
				},
				{
					"type": "function_call_output",
					"call_id": "call_once",
					"name": "create_thread",
					"namespace": "codex_app",
					"output": "second"
				}
			]
		}`)
		got := testRewriteCodexOrphan(payload, true)
		parsed := gjson.ParseBytes(got)

		if parsed.Get("input.1.type").String() != "function_call_output" {
			t.Fatalf("first output should pair with call: %s", parsed.Get("input.1").Raw)
		}
		if parsed.Get("input.2.type").String() != "message" {
			t.Fatalf("second output should be orphan: %s", parsed.Get("input.2").Raw)
		}
	})

	t.Run("custom tool call does not pair with function call output", func(t *testing.T) {
		payload := []byte(`{
			"model": "deepseek-v4-pro",
			"input": [
				{
					"type": "custom_tool_call",
					"call_id": "call_custom",
					"name": "create_thread",
					"input": "{}"
				},
				{
					"type": "function_call_output",
					"call_id": "call_custom",
					"name": "create_thread",
					"namespace": "codex_app",
					"output": "orphan"
				}
			]
		}`)
		got := testRewriteCodexOrphan(payload, true)
		parsed := gjson.ParseBytes(got)

		if parsed.Get("input.1.type").String() != "message" {
			t.Fatalf("function_call_output should not pair with custom_tool_call: %s", parsed.Get("input.1").Raw)
		}
	})

	t.Run("assistant message tool_calls does not pair with Responses function_call_output", func(t *testing.T) {
		payload := []byte(`{
			"model": "deepseek-v4-pro",
			"input": [
				{
					"type": "message",
					"role": "assistant",
					"tool_calls": [{"id": "call_in_msg", "type": "function", "function": {"name": "create_thread"}}]
				},
				{
					"type": "function_call_output",
					"call_id": "call_in_msg",
					"name": "create_thread",
					"namespace": "codex_app",
					"output": "orphan"
				}
			]
		}`)
		got := testRewriteCodexOrphan(payload, true)
		parsed := gjson.ParseBytes(got)

		if parsed.Get("input.1.type").String() != "message" {
			t.Fatalf("function_call_output should not pair with assistant message tool_calls: %s", parsed.Get("input.1").Raw)
		}
	})

	t.Run("exact namespace and name required", func(t *testing.T) {
		payload := []byte(`{
			"model": "deepseek-v4-pro",
			"input": [
				{
					"type": "function_call_output",
					"name": "codex_app__create_thread",
					"output": "orphan"
				},
				{
					"type": "function_call_output",
					"name": "create_thread",
					"namespace": " codex_app ",
					"output": "orphan"
				},
				{
					"type": "function_call_output",
					"name": "other_tool",
					"namespace": "codex_app",
					"output": "orphan"
				}
			]
		}`)
		got := testRewriteCodexOrphan(payload, true)
		parsed := gjson.ParseBytes(got)

		for i := 0; i < 3; i++ {
			if itemType := parsed.Get(fmt.Sprintf("input.%d.type", i)).String(); itemType != "function_call_output" {
				t.Fatalf("input.%d should not be rewritten: %s", i, parsed.Get(fmt.Sprintf("input.%d", i)).Raw)
			}
		}
	})
}

func TestTranslateRequestWithCodexMultiAgentV2OrphanDelegation(t *testing.T) {
	payload := []byte(`{
		"model": "test-model",
		"stream": false,
		"input": [
			{
				"type": "function_call_output",
				"name": "create_thread",
				"namespace": "codex_app",
				"output": "<codex_delegation><message>handoff</message></codex_delegation>"
			},
			{
				"type": "message",
				"role": "user",
				"content": [{"type": "input_text", "text": "please continue"}]
			}
		]
	}`)
	collabHeaders := http.Header{"X-Openai-Subagent": []string{"collab_spawn"}}

	t.Run("enabled but missing X-Openai-Subagent header does not rewrite", func(t *testing.T) {
		cfg := &config.Config{
			Codex: config.CodexConfig{
				OrphanDelegationCompatibility: true,
			},
		}
		got := TranslateRequestWithCodexMultiAgentV2(context.Background(), http.Header{}, cfg, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatOpenAIResponse, "test-model", payload, false)
		parsed := gjson.ParseBytes(got)

		item0 := parsed.Get("input.0")
		if item0.Get("type").String() != "function_call_output" {
			t.Fatalf("input.0 = %s, want function_call_output when X-Openai-Subagent header is missing", item0.Raw)
		}
	})

	t.Run("enabled with wrong X-Openai-Subagent header value does not rewrite", func(t *testing.T) {
		cfg := &config.Config{
			Codex: config.CodexConfig{
				OrphanDelegationCompatibility: true,
			},
		}
		headers := http.Header{"X-Openai-Subagent": []string{"other_agent"}}
		got := TranslateRequestWithCodexMultiAgentV2(context.Background(), headers, cfg, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatOpenAIResponse, "test-model", payload, false)
		parsed := gjson.ParseBytes(got)

		item0 := parsed.Get("input.0")
		if item0.Get("type").String() != "function_call_output" {
			t.Fatalf("input.0 = %s, want function_call_output when X-Openai-Subagent header is wrong", item0.Raw)
		}
	})

	t.Run("enabled translates orphan delegation to user message for responses target", func(t *testing.T) {
		cfg := &config.Config{
			Codex: config.CodexConfig{
				OrphanDelegationCompatibility: true,
			},
		}
		got := TranslateRequestWithCodexMultiAgentV2(context.Background(), collabHeaders, cfg, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatOpenAIResponse, "test-model", payload, false)
		parsed := gjson.ParseBytes(got)

		item0 := parsed.Get("input.0")
		if item0.Get("type").String() != "message" || item0.Get("role").String() != "user" {
			t.Fatalf("input.0 = %s, want message/user", item0.Raw)
		}
		wantText := "Tool output from codex_app__create_thread:\n<codex_delegation><message>handoff</message></codex_delegation>"
		if text := item0.Get("content.0.text").String(); text != wantText {
			t.Fatalf("input.0.content.0.text = %q, want %q", text, wantText)
		}
	})

	t.Run("enabled translates orphan delegation to user message for chat target without empty tool_call_id", func(t *testing.T) {
		cfg := &config.Config{
			Codex: config.CodexConfig{
				OrphanDelegationCompatibility: true,
			},
		}
		got := TranslateRequestWithCodexMultiAgentV2(context.Background(), collabHeaders, cfg, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatOpenAI, "test-model", payload, false)
		parsed := gjson.ParseBytes(got)

		messages := parsed.Get("messages").Array()
		if len(messages) < 2 {
			t.Fatalf("expected at least 2 messages, got %d: %s", len(messages), string(got))
		}
		msg0 := messages[0]
		if msg0.Get("role").String() != "user" {
			t.Fatalf("messages.0.role = %q, want user (should not be tool message with empty id)", msg0.Get("role").String())
		}
		wantText := "Tool output from codex_app__create_thread:\n<codex_delegation><message>handoff</message></codex_delegation>"
		var text string
		if msg0.Get("content").IsArray() {
			text = msg0.Get("content.0.text").String()
		} else {
			text = msg0.Get("content").String()
		}
		if text != wantText {
			t.Fatalf("messages.0.content = %q, want %q", text, wantText)
		}
	})

	t.Run("disabled leaves orphan delegation untranslated for responses target", func(t *testing.T) {
		cfg := &config.Config{
			Codex: config.CodexConfig{
				OrphanDelegationCompatibility: false,
			},
		}
		got := TranslateRequestWithCodexMultiAgentV2(context.Background(), collabHeaders, cfg, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatOpenAIResponse, "test-model", payload, false)
		parsed := gjson.ParseBytes(got)

		item0 := parsed.Get("input.0")
		if item0.Get("type").String() != "function_call_output" {
			t.Fatalf("input.0 = %s, want function_call_output when disabled", item0.Raw)
		}
	})

	t.Run("interleaved valid tool pair and orphan delegation retains order and pairing", func(t *testing.T) {
		interleaved := []byte(`{
			"model": "test-model",
			"input": [
				{
					"type": "function_call",
					"call_id": "call_1",
					"name": "lookup",
					"arguments": "{\"q\":\"test\"}"
				},
				{
					"type": "function_call_output",
					"call_id": "call_1",
					"name": "lookup",
					"output": "result1"
				},
				{
					"type": "function_call_output",
					"name": "create_thread",
					"namespace": "codex_app",
					"output": "delegated"
				}
			]
		}`)
		cfg := &config.Config{
			Codex: config.CodexConfig{
				OrphanDelegationCompatibility: true,
			},
		}
		got := TranslateRequestWithCodexMultiAgentV2(context.Background(), collabHeaders, cfg, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatOpenAIResponse, "test-model", interleaved, false)
		parsed := gjson.ParseBytes(got)

		if parsed.Get("input.0.type").String() != "function_call" {
			t.Fatalf("input.0 should remain function_call")
		}
		if parsed.Get("input.1.type").String() != "function_call_output" || parsed.Get("input.1.call_id").String() != "call_1" {
			t.Fatalf("input.1 should remain paired function_call_output")
		}
		if parsed.Get("input.2.type").String() != "message" || parsed.Get("input.2.role").String() != "user" {
			t.Fatalf("input.2 should be downgraded to message/user: %s", parsed.Get("input.2").Raw)
		}
	})
}
