package handlers

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/tidwall/gjson"
)

// A registered thinking model WITHOUT MaxCompletionTokens must fall back to the
// default thinking cap instead of leaving the client's small cap in place:
// reasoning and visible output share one budget, so a small cap yields empty
// visible content.
func TestInflateThinkingModelMaxTokensFallbackCap(t *testing.T) {
	const model = "thinking-no-cap-model"
	registry.GetGlobalRegistry().RegisterClient("thinking-no-cap-auth", "claude", []*registry.ModelInfo{{
		ID:       model,
		Thinking: &registry.ThinkingSupport{Min: 1024, Max: 64000},
		// MaxCompletionTokens deliberately 0.
	}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("thinking-no-cap-auth") })

	body := []byte(`{"model":"x","max_tokens":1024,"messages":[]}`)
	out := inflateThinkingModelMaxTokens(body, model)
	if got := gjson.GetBytes(out, "max_tokens").Int(); got != defaultThinkingCompletionCap {
		t.Fatalf("max_tokens = %d, want fallback cap %d", got, defaultThinkingCompletionCap)
	}
}

// A client cap above the fallback must never be lowered.
func TestInflateThinkingModelMaxTokensNeverLowers(t *testing.T) {
	const model = "thinking-no-cap-high"
	registry.GetGlobalRegistry().RegisterClient("thinking-no-cap-high-auth", "claude", []*registry.ModelInfo{{
		ID:       model,
		Thinking: &registry.ThinkingSupport{Min: 1024, Max: 128000},
	}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("thinking-no-cap-high-auth") })

	body := []byte(`{"model":"x","max_tokens":90000,"messages":[]}`)
	out := inflateThinkingModelMaxTokens(body, model)
	if got := gjson.GetBytes(out, "max_tokens").Int(); got != 90000 {
		t.Fatalf("max_tokens lowered to %d, want 90000 untouched", got)
	}
}

// finish_reason=max_tokens with no visible content (and no tool calls) becomes
// an explicit 502; anything else passes through as nil.
func TestAutoRouterEmptyCompletionError(t *testing.T) {
	empty := []byte(`{"choices":[{"message":{"content":""},"finish_reason":"max_tokens"}]}`)
	errMsg := autoRouterEmptyCompletionError(empty)
	if errMsg == nil {
		t.Fatal("expected an error for empty max_tokens completion")
	}
	if errMsg.StatusCode != 502 {
		t.Fatalf("status = %d, want 502", errMsg.StatusCode)
	}
	// Content present -> nil.
	okBody := []byte(`{"choices":[{"message":{"content":"hello"},"finish_reason":"max_tokens"}]}`)
	if autoRouterEmptyCompletionError(okBody) != nil {
		t.Fatal("non-empty content must not error")
	}
	// Tool calls present -> nil (a tool-call round-trip legitimately has no text).
	toolBody := []byte(`{"choices":[{"message":{"content":"","tool_calls":[{"id":"1"}]},"finish_reason":"tool_calls"}]}`)
	if autoRouterEmptyCompletionError(toolBody) != nil {
		t.Fatal("tool-call completion must not error")
	}
	// Different finish reason -> nil.
	stopBody := []byte(`{"choices":[{"message":{"content":""},"finish_reason":"stop"}]}`)
	if autoRouterEmptyCompletionError(stopBody) != nil {
		t.Fatal("non max_tokens finish reason must not error")
	}
	// Non-chat payload -> nil.
	if autoRouterEmptyCompletionError([]byte(`{}`)) != nil {
		t.Fatal("payload without choices must not error")
	}
}
