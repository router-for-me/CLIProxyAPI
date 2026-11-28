package signature

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestSanitizeClaudeMessagesForClaudeUpstreamPreservesEmptyThinkingInCompatMode(t *testing.T) {
	input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":""}]}]}`)

	withoutCompat, _ := SanitizeClaudeMessagesForClaudeUpstream(input, "deepseek-v4")
	if ojson.GetBytes(withoutCompat, "messages.0.content.#").Int() != 0 {
		t.Fatalf("default sanitizer preserved empty thinking: %s", withoutCompat)
	}

	withCompat, _ := SanitizeClaudeMessagesForClaudeUpstream(input, "deepseek-v4", true)
	part := ojson.GetBytes(withCompat, "messages.0.content.0")
	if part.Get("type").String() != "thinking" || part.Get("signature").String() != "" {
		t.Fatalf("compat sanitizer dropped empty thinking: %s", withCompat)
	}
}

func TestSanitizeClaudeMessagesForClaudeUpstreamRestoresCommentaryMarkerAsText(t *testing.T) {
	input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"COMMENTARY_SENTINEL","signature":"` + CodexCommentarySignatureMarker + `"},{"type":"text","text":"FINAL_SENTINEL"}]}]}`)

	for _, compat := range []bool{false, true} {
		out, _ := SanitizeClaudeMessagesForClaudeUpstream(input, "deepseek-v4", compat)
		if ojson.GetBytes(out, "messages.0.content.#").Int() != 2 {
			t.Fatalf("compat=%v: commentary marker block was lost: %s", compat, out)
		}
		first := ojson.GetBytes(out, "messages.0.content.0")
		if first.Get("type").String() != "text" || first.Get("text").String() != "COMMENTARY_SENTINEL" || first.Get("signature").Exists() {
			t.Fatalf("compat=%v: commentary marker should become a plain text block: %s", compat, out)
		}
		if got := ojson.GetBytes(out, "messages.0.content.1.text").String(); got != "FINAL_SENTINEL" {
			t.Fatalf("compat=%v: final text = %q, want FINAL_SENTINEL: %s", compat, got, out)
		}
	}
}

func TestSanitizeClaudeMessagesSignaturesForModelRestoresCommentaryMarkerAsText(t *testing.T) {
	input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"COMMENTARY_SENTINEL","signature":"` + CodexCommentarySignatureMarker + `"}]}]}`)

	for _, model := range []string{"claude-sonnet-5", "gpt-5.4", "gemini-3.5-flash", "unknown-model"} {
		out, _ := SanitizeClaudeMessagesSignaturesForModel(input, model)
		part := ojson.GetBytes(out, "messages.0.content.0")
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
	first := ojson.GetBytes(out, "messages.1.content.0")
	if first.Get("type").String() != "text" || first.Get("text").String() != "COMMENTARY_SENTINEL" || first.Get("signature").Exists() {
		t.Fatalf("commentary marker should become a plain text block: %s", out)
	}
	if got := ojson.GetBytes(out, "messages.1.content.1.text").String(); got != "FINAL_SENTINEL" {
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
	if ojson.GetBytes(withoutCompat, "messages.0.content.0.signature").String() != "" {
		t.Fatalf("default sanitizer preserved opaque signature: %s", withoutCompat)
	}

	withCompat, _ := SanitizeClaudeMessagesForClaudeUpstream(input, "deepseek-v4", true)
	part := ojson.GetBytes(withCompat, "messages.0.content.0")
	if part.Get("type").String() != "thinking" || part.Get("signature").String() != "opaque-deepseek-id" {
		t.Fatalf("compat sanitizer dropped opaque signature: %s", withCompat)
	}
}

func TestCodexCommentaryMirrorAppearsOnceInCrossProviderReplay(t *testing.T) {
	const marker = `{"type":"thinking","thinking":"progress","signature":"` + CodexCommentarySignatureMarker + `"}`
	const mirror = `{"type":"text","text":"progress"}`
	const final = `{"type":"text","text":"final"}`
	for _, tt := range []struct {
		name     string
		messages string
		want     string
	}{
		{"same message", `[{"role":"assistant","content":[` + marker + "," + mirror + "," + final + `]}]`, `[{"role":"assistant","content":[` + mirror + "," + final + `]}]`},
		{"split assistant message", `[{"role":"assistant","content":[` + marker + `]},{"role":"assistant","content":[` + mirror + "," + final + `]}]`, `[{"role":"assistant","content":[` + mirror + "," + final + `]}]`},
		{"split string mirror", `[{"role":"assistant","content":[` + marker + `]},{"role":"assistant","content":"progress"}]`, `[{"role":"assistant","content":"progress"}]`},
		{"different adjacent string", `[{"role":"assistant","content":[` + marker + `]},{"role":"assistant","content":"final"}]`, `[{"role":"assistant","content":[` + mirror + `]},{"role":"assistant","content":"final"}]`},
		{"different adjacent text", `[{"role":"assistant","content":[` + marker + "," + final + `]}]`, `[{"role":"assistant","content":[` + mirror + "," + final + `]}]`},
		{"nonadjacent repeated text", `[{"role":"assistant","content":[` + marker + "," + final + "," + mirror + `]}]`, `[{"role":"assistant","content":[` + mirror + "," + final + "," + mirror + `]}]`},
		{"user text is preserved", `[{"role":"assistant","content":[` + marker + `]},{"role":"user","content":[` + mirror + `]}]`, `[{"role":"assistant","content":[` + mirror + `]},{"role":"user","content":[` + mirror + `]}]`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			input := []byte(`{"messages":` + tt.messages + "}")
			restored, _ := RestoreCodexCommentaryMarkers(input)
			sanitized, _ := SanitizeClaudeMessagesForClaudeUpstream(input, "claude-sonnet-5")
			for _, result := range [][]byte{restored, sanitized} {
				if got := ojson.GetBytesNoCopy(result, "messages").Raw; got != tt.want {
					t.Errorf("messages = %s, want %s", got, tt.want)
				}
			}
		})
	}
}
