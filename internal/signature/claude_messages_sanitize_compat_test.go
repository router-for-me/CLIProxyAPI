package signature

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestSanitizeClaudeMessagesForClaudeUpstreamPreservesEmptyThinkingInCompatMode(t *testing.T) {
	input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":""}]}]}`)

	withoutCompat, _ := SanitizeClaudeMessagesForClaudeUpstream(input, "deepseek-v4")
	if gjson.GetBytes(withoutCompat, "messages.0.content.#").Int() != 0 {
		t.Fatalf("default sanitizer preserved empty thinking: %s", withoutCompat)
	}

	withCompat, _ := SanitizeClaudeMessagesForClaudeUpstream(input, "deepseek-v4", true)
	part := gjson.GetBytes(withCompat, "messages.0.content.0")
	if part.Get("type").String() != "thinking" || part.Get("signature").String() != "" {
		t.Fatalf("compat sanitizer dropped empty thinking: %s", withCompat)
	}
}

func TestSanitizeClaudeMessagesForClaudeUpstreamStripsGeminiPrefixInCompatMode(t *testing.T) {
	input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":"gemini#EgI="}]}]}`)

	withCompat, _ := SanitizeClaudeMessagesForClaudeUpstream(input, "claude-sonnet-4", true)
	part := gjson.GetBytes(withCompat, "messages.0.content.0")
	if part.Get("type").String() != "thinking" || part.Get("signature").String() != "" {
		t.Fatalf("compat sanitizer preserved gemini-prefixed signature: %s", withCompat)
	}
}

func TestSanitizeClaudeMessagesForClaudeUpstreamStripsOpaqueThinkingSignatureInCompatMode(t *testing.T) {
	input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":"opaque-deepseek-id"}]}]}`)

	withoutCompat, _ := SanitizeClaudeMessagesForClaudeUpstream(input, "deepseek-v4")
	if gjson.GetBytes(withoutCompat, "messages.0.content.0.signature").String() != "" {
		t.Fatalf("default sanitizer preserved opaque signature: %s", withoutCompat)
	}

	withCompat, _ := SanitizeClaudeMessagesForClaudeUpstream(input, "deepseek-v4", true)
	part := gjson.GetBytes(withCompat, "messages.0.content.0")
	if part.Get("type").String() != "thinking" || part.Get("signature").String() != "" {
		t.Fatalf("compat sanitizer dropped opaque thinking block: %s", withCompat)
	}
}
