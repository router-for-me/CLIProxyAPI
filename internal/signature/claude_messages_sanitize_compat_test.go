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

func TestSanitizeClaudeMessagesForClaudeUpstreamRestoresCommentaryMarkerAsText(t *testing.T) {
	input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"COMMENTARY_SENTINEL","signature":"` + CodexCommentarySignatureMarker + `"},{"type":"text","text":"FINAL_SENTINEL"}]}]}`)

	for _, compat := range []bool{false, true} {
		out, _ := SanitizeClaudeMessagesForClaudeUpstream(input, "deepseek-v4", compat)
		if gjson.GetBytes(out, "messages.0.content.#").Int() != 2 {
			t.Fatalf("compat=%v: commentary marker block was lost: %s", compat, out)
		}
		first := gjson.GetBytes(out, "messages.0.content.0")
		if first.Get("type").String() != "text" || first.Get("text").String() != "COMMENTARY_SENTINEL" || first.Get("signature").Exists() {
			t.Fatalf("compat=%v: commentary marker should become a plain text block: %s", compat, out)
		}
		if got := gjson.GetBytes(out, "messages.0.content.1.text").String(); got != "FINAL_SENTINEL" {
			t.Fatalf("compat=%v: final text = %q, want FINAL_SENTINEL: %s", compat, got, out)
		}
	}
}

func TestSanitizeClaudeMessagesSignaturesForModelRestoresCommentaryMarkerAsText(t *testing.T) {
	input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"COMMENTARY_SENTINEL","signature":"` + CodexCommentarySignatureMarker + `"}]}]}`)

	for _, model := range []string{"claude-sonnet-5", "gpt-5.4", "gemini-3.5-flash", "unknown-model"} {
		out, _ := SanitizeClaudeMessagesSignaturesForModel(input, model)
		part := gjson.GetBytes(out, "messages.0.content.0")
		if part.Get("type").String() != "text" || part.Get("text").String() != "COMMENTARY_SENTINEL" || part.Get("signature").Exists() {
			t.Fatalf("model=%s: commentary marker should become a plain text block: %s", model, out)
		}
	}
}

func TestRestoreCodexCommentaryMarkersRestoresTextOnAnyTarget(t *testing.T) {
	input := []byte(`{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"thinking","thinking":"COMMENTARY_SENTINEL","signature":"` + CodexCommentarySignatureMarker + `"},{"type":"text","text":"FINAL_SENTINEL"}]}]}`)

	out, restored := RestoreCodexCommentaryMarkers(input)
	if restored != 1 {
		t.Fatalf("restored = %d, want 1: %s", restored, out)
	}
	first := gjson.GetBytes(out, "messages.1.content.0")
	if first.Get("type").String() != "text" || first.Get("text").String() != "COMMENTARY_SENTINEL" || first.Get("signature").Exists() {
		t.Fatalf("commentary marker should become a plain text block: %s", out)
	}
	if got := gjson.GetBytes(out, "messages.1.content.1.text").String(); got != "FINAL_SENTINEL" {
		t.Fatalf("final text = %q, want FINAL_SENTINEL: %s", got, out)
	}

	untouched := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":"opaque-deepseek-id"}]}]}`)
	out, restored = RestoreCodexCommentaryMarkers(untouched)
	if restored != 0 || string(out) != string(untouched) {
		t.Fatalf("payload without the marker must pass through unchanged: restored=%d out=%s", restored, out)
	}
}

func TestSanitizeClaudeMessagesForClaudeUpstreamPreservesOpaqueThinkingSignatureInCompatMode(t *testing.T) {
	input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":"opaque-deepseek-id"}]}]}`)

	withoutCompat, _ := SanitizeClaudeMessagesForClaudeUpstream(input, "deepseek-v4")
	if gjson.GetBytes(withoutCompat, "messages.0.content.0.signature").String() != "" {
		t.Fatalf("default sanitizer preserved opaque signature: %s", withoutCompat)
	}

	withCompat, _ := SanitizeClaudeMessagesForClaudeUpstream(input, "deepseek-v4", true)
	part := gjson.GetBytes(withCompat, "messages.0.content.0")
	if part.Get("type").String() != "thinking" || part.Get("signature").String() != "opaque-deepseek-id" {
		t.Fatalf("compat sanitizer dropped opaque signature: %s", withCompat)
	}
}
