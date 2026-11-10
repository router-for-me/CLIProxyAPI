package helps

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestBuildSensitiveWordMatcherPreservesZeroWidthSpaceDefault(t *testing.T) {
	matcher := BuildSensitiveWordMatcher([]string{"proxy", "Claude Code"})
	if matcher == nil {
		t.Fatal("expected non-nil matcher")
	}

	got := matcher.ObfuscateText("Use proxy with Claude Code.")
	want := "Use p\u200Broxy with C\u200Blaude Code."
	if got != want {
		t.Fatalf("ObfuscateText() = %q, want %q", got, want)
	}
}

func TestBuildAntigravitySensitiveWordMatcherObfuscatesSubwordsWithCGJ(t *testing.T) {
	matcher := BuildAntigravitySensitiveWordMatcher([]string{
		"proxy",
		"Claude Code",
		"Nous Research",
		"GPT-5",
		"Agent X",
	})
	if matcher == nil {
		t.Fatal("expected non-nil matcher")
	}

	input := "Use proxy with Claude Code, Nous Research, GPT-5, and Agent X."
	got := matcher.ObfuscateText(input)
	want := "Use p\u034Froxy with C\u034Flaude C\u034Fode, N\u034Fous R\u034Fesearch, G\u034FPT-5, and A\u034Fgent X."
	if got != want {
		t.Fatalf("ObfuscateText() = %q, want %q", got, want)
	}
}

func TestBuildAntigravitySensitiveWordMatcherLiteralReplacement(t *testing.T) {
	matcher := BuildAntigravitySensitiveWordMatcher([]string{
		"Claude",
		"Claude Code => Claude",
		"You are Codex, an agent based on GPT-5. => You are a coding assistant.",
		"Codex",
	})
	if matcher == nil {
		t.Fatal("expected non-nil matcher")
	}

	input := "you are codex, an agent based on gpt-5. Prefer CLAUDE CODE or Claude Code over standalone Claude and Codex."
	got := matcher.ObfuscateText(input)
	want := "You are a coding assistant. Prefer Claude or Claude over standalone C\u034Flaude and C\u034Fodex."
	if got != want {
		t.Fatalf("ObfuscateText() = %q, want %q", got, want)
	}
}

func TestBuildAntigravitySensitiveWordMatcherEmptyReplacementAndArrowParsing(t *testing.T) {
	matcher := BuildAntigravitySensitiveWordMatcher([]string{
		"remove me => ",
		"trim me =>",
		"x=>y",
		"Claude Code",
		"Claude Code => Claude",
	})
	if matcher == nil {
		t.Fatal("expected non-nil matcher")
	}

	input := "Please remove me and trim me; keep x=>y and Claude Code."
	got := matcher.ObfuscateText(input)
	want := "Please  and ; keep x\u034F=>y and Claude."
	if got != want {
		t.Fatalf("ObfuscateText() = %q, want %q", got, want)
	}
}

func TestObfuscateSensitiveWordsInSystemInstructionDocIdempotent(t *testing.T) {
	matcher := BuildAntigravitySensitiveWordMatcher([]string{
		"Claude Code",
		"Code",
		"proxy",
		"secret token => API credential",
	})
	if matcher == nil {
		t.Fatal("expected non-nil matcher")
	}

	doc := ojson.NewDocString(`{
		"request": {
			"systemInstruction": {
				"parts": [
					{"text": "Use Claude Code with proxy, secret token, and legacy p\u200Broxy."}
				]
			},
			"contents": [
				{"role": "user", "parts": [{"text": "Claude Code and proxy stay untouched in contents"}]}
			]
		}
	}`)

	if !ObfuscateSensitiveWordsInSystemInstructionDoc(doc, matcher) {
		t.Fatal("first pass should report document modification")
	}
	wantSys := "Use C\u034Flaude C\u034Fode with p\u034Froxy, API credential, and legacy p\u200Broxy."
	if got := doc.Get("request.systemInstruction.parts.0.text").String(); got != wantSys {
		t.Fatalf("after first pass systemInstruction = %q, want %q", got, wantSys)
	}

	if ObfuscateSensitiveWordsInSystemInstructionDoc(doc, matcher) {
		t.Fatal("second pass should be a no-op and report false")
	}
	if got := doc.Get("request.systemInstruction.parts.0.text").String(); got != wantSys {
		t.Fatalf("after second pass systemInstruction = %q, want %q", got, wantSys)
	}
	if gotContent := doc.Get("request.contents.0.parts.0.text").String(); gotContent != "Claude Code and proxy stay untouched in contents" {
		t.Fatalf("contents = %q, want unchanged", gotContent)
	}
}
