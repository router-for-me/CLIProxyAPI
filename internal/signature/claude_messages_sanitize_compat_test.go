package signature

import (
	"bytes"
	"encoding/base64"
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

func TestSanitizeClaudeMessagesForClaudeUpstreamNormalizesWhitespacePaddedShortSignatureInCompatMode(t *testing.T) {
	input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":"  EgI=  "}]}]}`)

	withCompat, _ := SanitizeClaudeMessagesForClaudeUpstream(input, "claude-sonnet-4", true)
	part := gjson.GetBytes(withCompat, "messages.0.content.0")
	if part.Get("type").String() != "thinking" || !part.Get("signature").Exists() {
		t.Fatalf("compat sanitizer dropped the thinking block: %s", withCompat)
	}
	if got := part.Get("signature").String(); got != "EgI=" {
		t.Fatalf("compat sanitizer forwarded whitespace-padded short signature %q, want %q", got, "EgI=")
	}
}

func TestSanitizeClaudeMessagesForClaudeUpstreamRejectsGrokOpaqueERInCompatMode(t *testing.T) {
	// Grok/xAI encrypted_content is uniformly distributed and can base64-encode
	// to a string starting with 'E' or 'R', but it is not a valid Claude
	// thinking signature and must not pass the short-signature fallback.
	grokLike := bytes.Repeat([]byte{0x12, 0xff, 0x88, 0x77, 0x66, 0x55, 0x44, 0x33}, 4)
	sig := base64.StdEncoding.EncodeToString(grokLike)
	input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":"` + sig + `"}]}]}`)

	withCompat, _ := SanitizeClaudeMessagesForClaudeUpstream(input, "claude-sonnet-4", true)
	part := gjson.GetBytes(withCompat, "messages.0.content.0")
	if part.Get("type").String() != "thinking" || !part.Get("signature").Exists() || part.Get("signature").String() != "" {
		t.Fatalf("compat sanitizer did not clear Grok-style E/R opaque signature: %s", withCompat)
	}
}

func TestSanitizeClaudeMessagesForClaudeUpstreamStripsClientReplayProvenanceMarkerInCompatMode(t *testing.T) {
	// Client-supplied _cliproxy_replay_provenance must not bypass signature
	// validation. The marker is stripped and the foreign signature is cleared.
	input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":"opaque-foreign-sig","_cliproxy_replay_provenance":true}]}]}`)

	withCompat, _ := SanitizeClaudeMessagesForClaudeUpstream(input, "claude-sonnet-4", true)
	part := gjson.GetBytes(withCompat, "messages.0.content.0")
	if part.Get("type").String() != "thinking" {
		t.Fatalf("compat sanitizer dropped the thinking block: %s", withCompat)
	}
	if part.Get("_cliproxy_replay_provenance").Exists() {
		t.Fatalf("compat sanitizer did not strip client-supplied replay provenance marker: %s", withCompat)
	}
	if part.Get("signature").String() != "" {
		t.Fatalf("compat sanitizer preserved foreign signature via client-supplied marker: %s", withCompat)
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
