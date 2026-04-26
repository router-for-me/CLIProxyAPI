package executor

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tidwall/gjson"
)

func TestIsKimiClaudeCompatBaseURL(t *testing.T) {
	t.Parallel()

	if !isKimiClaudeCompatBaseURL("https://api.kimi.com/coding") {
		t.Fatal("expected kimi base url to be recognized")
	}
	if isKimiClaudeCompatBaseURL("https://api.anthropic.com") {
		t.Fatal("did not expect anthropic base url to be recognized as kimi")
	}
}

func TestApplyKimiClaudeCompatibility_NormalizeAdaptiveThinking(t *testing.T) {
	t.Parallel()

	input := []byte(`{"thinking":{"type":"adaptive"},"output_config":{"effort":"high"},"context_management":{"edits":[{"type":"x"}]},"metadata":{"user_id":"{\"device_id\":\"abc\"}"}}`)
	out := applyKimiClaudeCompatibility(input, false)

	if got := gjson.GetBytes(out, "thinking.type").String(); got != "enabled" {
		t.Fatalf("thinking.type = %q, want enabled", got)
	}
	if got := gjson.GetBytes(out, "thinking.budget_tokens").Int(); got != 8192 {
		t.Fatalf("thinking.budget_tokens = %d, want 8192", got)
	}
	if gjson.GetBytes(out, "output_config.effort").Exists() {
		t.Fatal("output_config.effort should be removed")
	}
	if gjson.GetBytes(out, "context_management").Exists() {
		t.Fatal("context_management should be removed")
	}
	if gjson.GetBytes(out, "metadata").Exists() {
		t.Fatal("metadata should be removed")
	}
}

func TestApplyKimiClaudeBetaHeader(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPost, "https://api.kimi.com/coding/v1/messages?beta=true", nil)
	applyKimiClaudeBetaHeader(req, false)
	if got := req.Header.Get("Anthropic-Beta"); got != "claude-code-20250219,interleaved-thinking-2025-05-14" {
		t.Fatalf("beta header = %q", got)
	}
	applyKimiClaudeBetaHeader(req, true)
	if got := req.Header.Get("Anthropic-Beta"); got != "claude-code-20250219" {
		t.Fatalf("strict beta header = %q", got)
	}
}

func TestIsRetryableKimiInvalidRequest(t *testing.T) {
	t.Parallel()

	body := []byte(`{"error":{"type":"invalid_request_error","message":"Invalid request Error"}}`)
	if !isRetryableKimiInvalidRequest(http.StatusBadRequest, body) {
		t.Fatal("expected invalid_request_error to be retryable")
	}
	if isRetryableKimiInvalidRequest(http.StatusInternalServerError, body) {
		t.Fatal("status 500 should not be retryable")
	}
}

func TestApplyKimiClaudeCompatibility_StripToolReference(t *testing.T) {
	t.Parallel()

	input := []byte(`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"1","content":[{"type":"text","text":"result"},{"type":"tool_reference","tool_name":"WebSearch"}]}]}]}`)
	out := applyKimiClaudeCompatibility(input, false)

	arr := gjson.GetBytes(out, "messages.0.content.0.content")
	if !arr.IsArray() {
		t.Fatal("expected inner content to remain an array")
	}
	for _, v := range arr.Array() {
		if v.Get("type").String() == "tool_reference" {
			t.Fatalf("tool_reference should be removed, got %s", v.Raw)
		}
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.content.0.type").String(); got != "text" {
		t.Fatalf("first inner block type = %q, want text", got)
	}
	if gjson.GetBytes(out, "messages.0.content.0.content.1").Exists() {
		t.Fatal("expected only one inner block after stripping tool_reference")
	}
}

func TestApplyKimiClaudeCompatibility_NoToolReferenceNoOp(t *testing.T) {
	t.Parallel()

	input := []byte(`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"1","content":[{"type":"text","text":"ok"}]}]}]}`)
	out := applyKimiClaudeCompatibility(input, false)

	if got := gjson.GetBytes(out, "messages.0.content.0.content.0.text").String(); got != "ok" {
		t.Fatalf("inner text = %q, want ok", got)
	}
}

func TestApplyKimiClaudeCompatibility_MultipleToolReferences(t *testing.T) {
	t.Parallel()

	input := []byte(`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"1","content":[{"type":"tool_reference","tool_name":"A"},{"type":"text","text":"middle"},{"type":"tool_reference","tool_name":"B"}]}]}]}`)
	out := applyKimiClaudeCompatibility(input, false)

	arr := gjson.GetBytes(out, "messages.0.content.0.content")
	if len(arr.Array()) != 1 {
		t.Fatalf("expected 1 inner block, got %d", len(arr.Array()))
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.content.0.type").String(); got != "text" {
		t.Fatalf("expected text block, got %q", got)
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.content.0.text").String(); got != "middle" {
		t.Fatalf("expected middle text, got %q", got)
	}
}

func TestApplyKimiClaudeCompatibility_MultipleMessages(t *testing.T) {
	t.Parallel()

	input := []byte(`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"1","content":[{"type":"tool_reference","tool_name":"A"}]}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"2","content":[{"type":"tool_reference","tool_name":"B"},{"type":"text","text":"ok"}]}]}]}`)
	out := applyKimiClaudeCompatibility(input, false)

	if gjson.GetBytes(out, "messages.0.content.0.content.0.type").String() == "tool_reference" {
		t.Fatal("msg0 tool_reference should be removed")
	}
	if gjson.GetBytes(out, "messages.1.content.0.content.0.type").String() == "tool_reference" {
		t.Fatal("msg1 tool_reference should be removed")
	}
	if got := gjson.GetBytes(out, "messages.1.content.0.content.0.text").String(); got != "ok" {
		t.Fatalf("msg1 inner text = %q, want ok", got)
	}
}

func TestStripKimiIncompatibleFields_StrictMode(t *testing.T) {
	t.Parallel()

	input := []byte(`{"thinking":{"type":"enabled","budget_tokens":4096},"output_config":{"effort":"high"}}`)
	out := stripKimiIncompatibleFields(input, true)

	if gjson.GetBytes(out, "output_config.effort").Exists() {
		t.Fatal("output_config.effort should be removed in strict mode")
	}
	if !gjson.GetBytes(out, "thinking.budget_tokens").Exists() {
		t.Fatal("thinking.budget_tokens should be preserved in strict mode")
	}
}
