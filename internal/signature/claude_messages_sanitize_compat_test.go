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
	if part.Get("type").String() != "thinking" || !part.Get("signature").Exists() {
		t.Fatalf("compat sanitizer dropped empty thinking or its signature member: %s", withCompat)
	}
}

func TestSanitizeClaudeMessagesForClaudeUpstreamRetainsEmptySignatureOnGeminiPrefixInCompatMode(t *testing.T) {
	input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":"gemini#EgI="}]}]}`)

	withCompat, _ := SanitizeClaudeMessagesForClaudeUpstream(input, "claude-sonnet-4", true)
	part := gjson.GetBytes(withCompat, "messages.0.content.0")
	if part.Get("type").String() != "thinking" || !part.Get("signature").Exists() || part.Get("signature").String() != "" {
		t.Fatalf("compat sanitizer did not retain empty signature member on foreign-prefixed thinking block: %s", withCompat)
	}
}

func TestSanitizeClaudeMessagesForClaudeUpstreamRetainsEmptySignatureOnMislabeledClaudePrefixInCompatMode(t *testing.T) {
	geminiSig := testGemini3ThoughtSignature([]byte{0x01, 0x0c, 0x39, 0xd6, 0xc7, 0x34})
	input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":"claude#` + geminiSig + `"}]}]}`)

	withCompat, _ := SanitizeClaudeMessagesForClaudeUpstream(input, "claude-sonnet-4", true)
	part := gjson.GetBytes(withCompat, "messages.0.content.0")
	if part.Get("type").String() != "thinking" || !part.Get("signature").Exists() || part.Get("signature").String() != "" {
		t.Fatalf("compat sanitizer did not retain empty signature member on mislabeled claude# block: %s", withCompat)
	}
}

func TestSanitizeClaudeMessagesForClaudeUpstreamRetainsEmptySignatureOnNestedClaudePrefixInCompatMode(t *testing.T) {
	input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":"claude#vendor#EgI="}]}]}`)

	withCompat, _ := SanitizeClaudeMessagesForClaudeUpstream(input, "claude-sonnet-4", true)
	part := gjson.GetBytes(withCompat, "messages.0.content.0")
	if part.Get("type").String() != "thinking" || !part.Get("signature").Exists() || part.Get("signature").String() != "" {
		t.Fatalf("compat sanitizer did not retain empty signature member on nested claude# block: %s", withCompat)
	}
}

func TestSanitizeClaudeMessagesForClaudeUpstreamRetainsEmptySignatureOnUnknownVendorPrefixInCompatMode(t *testing.T) {
	input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":"vendor#EgI="}]}]}`)

	withCompat, _ := SanitizeClaudeMessagesForClaudeUpstream(input, "claude-sonnet-4", true)
	part := gjson.GetBytes(withCompat, "messages.0.content.0")
	if part.Get("type").String() != "thinking" || !part.Get("signature").Exists() || part.Get("signature").String() != "" {
		t.Fatalf("compat sanitizer did not retain empty signature member on unknown-vendor block: %s", withCompat)
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
	if part.Get("type").String() != "thinking" || !part.Get("signature").Exists() || part.Get("signature").String() != "" {
		t.Fatalf("compat sanitizer did not retain empty signature member on opaque-signature block: %s", withCompat)
	}
}
