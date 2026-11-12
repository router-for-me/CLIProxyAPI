package helps

import (
	"strings"
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

func TestBuildAntigravitySensitiveWordMatcherSubwordsAndPunctuation(t *testing.T) {
	matcher := BuildAntigravitySensitiveWordMatcher([]string{
		"Codex",
		"GPT-5",
		"Claude Code",
		"<system-conventions>",
		"claude-code",
		"Agent X",
	}, nil)
	if matcher == nil {
		t.Fatal("expected non-nil matcher")
	}

	input := "You are Codex, an agent based on GPT-5. Follow <system-conventions> in Claude Code / claude-code with Agent X."
	got := matcher.ObfuscateText(input)
	want := "You are C\u034Fodex, an agent based on G\u034FPT-5. Follow <s\u034Fystem-c\u034Fonventions> in C\u034Flaude C\u034Fode / c\u034Flaude-c\u034Fode with A\u034Fgent X."
	if got != want {
		t.Fatalf("ObfuscateText() = %q, want %q", got, want)
	}
	if strings.Contains(got, "\u200B") {
		t.Fatalf("ObfuscateText() = %q, must not contain U+200B", got)
	}
	if secondPass := matcher.ObfuscateText(got); secondPass != got {
		t.Fatalf("second obfuscation pass = %q, want idempotent %q", secondPass, got)
	}
}

func TestSensitiveWordMatcherReplacementsPrecedenceAndEmptyDeletion(t *testing.T) {
	matcher := BuildAntigravitySensitiveWordMatcher(
		[]string{"Claude", "Codex", "GPT-5"},
		map[string]string{
			"You are Codex, an agent based on GPT-5.": "You are a helpful coding assistant.",
			"Claude Code": "Claude",
			"Codex":       "Assistant",
			"RemoveMe ":   "",
		},
	)
	if matcher == nil {
		t.Fatal("expected non-nil matcher")
	}

	input := "you are codex, an agent based on gpt-5. Built for Claude Code, standalone Claude, Codex, and RemoveMe ."
	got := matcher.ObfuscateText(input)
	want := "You are a helpful coding assistant. Built for Claude, standalone C\u034Flaude, Assistant, and  ."
	if got != want {
		t.Fatalf("ObfuscateText() = %q, want %q", got, want)
	}
}

func TestObfuscateSensitiveWordsDocReplacementsOnlyAndBillingHeaderSkip(t *testing.T) {
	matcher := BuildSensitiveWordMatcherWithReplacements(nil, map[string]string{
		"anthropic": "provider",
		"secret":    "",
	})
	if matcher == nil {
		t.Fatal("expected non-nil matcher when only replacements are configured")
	}

	doc := ojson.NewDocString(`{
		"system": [
			{"type": "text", "text": "x-anthropic-billing-header: cc_version=2.1.321; cc_entrypoint=cli;"},
			{"type": "text", "text": "Keep anthropic rules and drop secret"}
		],
		"messages": [
			{"role": "user", "content": "Ask anthropic about secret"}
		]
	}`)
	if !ObfuscateSensitiveWordsDoc(doc, matcher) {
		t.Fatal("expected ObfuscateSensitiveWordsDoc to modify document")
	}
	if gotBilling := doc.Get("system.0.text").String(); gotBilling != "x-anthropic-billing-header: cc_version=2.1.321; cc_entrypoint=cli;" {
		t.Fatalf("billing header = %q, want untouched", gotBilling)
	}
	if gotSys := doc.Get("system.1.text").String(); gotSys != "Keep provider rules and drop " {
		t.Fatalf("system.1.text = %q, want %q", gotSys, "Keep provider rules and drop ")
	}
	if gotMsg := doc.Get("messages.0.content").String(); gotMsg != "Ask provider about " {
		t.Fatalf("messages.0.content = %q, want %q", gotMsg, "Ask provider about ")
	}
}

func TestSensitiveWordConfigCacheKeyCollisionFree(t *testing.T) {
	k1 := SensitiveWordConfigCacheKey([]string{"a", "b"}, map[string]string{"x": "1=2"})
	k2 := SensitiveWordConfigCacheKey([]string{"a;1:b"}, map[string]string{"x": "1=2"})
	k3 := SensitiveWordConfigCacheKey([]string{"a", "b"}, map[string]string{"x=2:1": "2"})
	if k1 == k2 || k1 == k3 || k2 == k3 {
		t.Fatalf("expected distinct cache keys, got %q, %q, %q", k1, k2, k3)
	}
}
