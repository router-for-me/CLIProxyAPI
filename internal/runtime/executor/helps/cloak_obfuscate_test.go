package helps

import (
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

// TestObfuscateWordUsesCombiningGraphemeJoiner proves the cloak character is
// U+034F (combining grapheme joiner) and no longer the U+200B zero-width space
// that upstream filters strip before keyword matching (#7433).
func TestObfuscateWordUsesCombiningGraphemeJoiner(t *testing.T) {
	cases := map[string]string{
		"Claude Code": "C\u034Flaude Code",
		"Codex":       "C\u034Fodex",
		"GPT-5":       "G\u034FPT-5",
	}
	for word, want := range cases {
		got := obfuscateWord(word)
		if got != want {
			t.Fatalf("obfuscateWord(%q) = %q, want %q", word, got, want)
		}
		if strings.Contains(got, "\u200B") {
			t.Fatalf("obfuscateWord(%q) = %q, must not contain U+200B", word, got)
		}
		if !strings.Contains(got, "\u034F") {
			t.Fatalf("obfuscateWord(%q) = %q, must contain U+034F", word, got)
		}
	}
}

// TestObfuscateSystemInstructionCloaksBlockedPhrases covers the Antigravity
// systemInstruction path with the "Claude Code" phrase from #7433 and the
// Codex identity phrase from #7428.
func TestObfuscateSystemInstructionCloaksBlockedPhrases(t *testing.T) {
	matcher := BuildSensitiveWordMatcher([]string{"Claude Code", "Codex", "GPT-5"})
	payload := []byte(`{"request":{"systemInstruction":{"parts":[{"text":"or Claude Code. You are Codex, an agent based on GPT-5."}]}}}`)

	got := ObfuscateSensitiveWordsInSystemInstruction(payload, matcher)
	text := ojson.GetBytes(got, "request.systemInstruction.parts.0.text").String()
	want := "or C\u034Flaude Code. You are C\u034Fodex, an agent based on G\u034FPT-5."
	if text != want {
		t.Fatalf("system instruction = %q, want %q", text, want)
	}
	if strings.Contains(text, "\u200B") {
		t.Fatalf("system instruction = %q, must not rely on U+200B", text)
	}
	if StripCloakChars(text) != "or Claude Code. You are Codex, an agent based on GPT-5." {
		t.Fatalf("stripping the cloak joiner must recover the original text, got %q", text)
	}
}

// TestObfuscateWordIdempotent proves applying the cloak twice does not double-insert.
func TestObfuscateWordIdempotent(t *testing.T) {
	once := obfuscateWord("Claude Code")
	if again := obfuscateWord(once); again != once {
		t.Fatalf("obfuscateWord not idempotent: %q then %q", once, again)
	}

	matcher := BuildSensitiveWordMatcher([]string{"Claude Code"})
	payload := []byte(`{"request":{"systemInstruction":"Claude Code rules"}}`)
	onceBytes := ObfuscateSensitiveWordsInSystemInstruction(payload, matcher)
	twiceBytes := ObfuscateSensitiveWordsInSystemInstruction(onceBytes, matcher)
	if string(twiceBytes) != string(onceBytes) {
		t.Fatalf("second obfuscation pass changed the payload: %q -> %q", onceBytes, twiceBytes)
	}
}

// TestBuildSensitiveWordMatcherSkipsCloakedWords proves matcher construction
// filters out words that already contain the cloak character.
func TestBuildSensitiveWordMatcherSkipsCloakedWords(t *testing.T) {
	if BuildSensitiveWordMatcher([]string{"C\u034Flaude"}) != nil {
		t.Fatal("matcher built from an already-cloaked word should be nil")
	}

	matcher := BuildSensitiveWordMatcher([]string{"secret", "s\u034Fecret"})
	if matcher == nil {
		t.Fatal("matcher must still build from the clean word")
	}
	if matcher.Matches("hide the s\u034Fecret") {
		t.Fatal("cloaked word must be skipped by the matcher")
	}
	if !matcher.Matches("hide the secret") {
		t.Fatal("clean word must still match")
	}
}

// TestObfuscateTextRewritesPlainASCIIWord proves a visible ASCII word without
// the cloak character is still rewritten.
func TestObfuscateTextRewritesPlainASCIIWord(t *testing.T) {
	matcher := BuildSensitiveWordMatcher([]string{"secret"})
	got := matcher.ObfuscateText("share the secret now")
	want := "share the s\u034Fecret now"
	if got != want {
		t.Fatalf("ObfuscateText() = %q, want %q", got, want)
	}
	if strings.Contains(got, "\u200B") {
		t.Fatalf("ObfuscateText() = %q, must not contain U+200B", got)
	}
}

// TestStripCloakChars removes both the current joiner and the legacy U+200B.
func TestStripCloakChars(t *testing.T) {
	got := StripCloakChars("C\u034Flaude C\u200Bode")
	if got != "Claude Code" {
		t.Fatalf("StripCloakChars() = %q, want %q", got, "Claude Code")
	}
}
