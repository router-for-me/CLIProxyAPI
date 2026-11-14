package helps

import (
	"fmt"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
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
	p1 := SensitiveWordConfigCacheKeyWithPatterns([]string{"a"}, map[string]string{"x": "1"}, []config.SensitivePatternRule{
		{Pattern: "foo", Replacement: "bar"},
	})
	p2 := SensitiveWordConfigCacheKeyWithPatterns([]string{"a"}, map[string]string{"x": "1"}, []config.SensitivePatternRule{
		{Pattern: "foo", Replacement: "baz"},
	})
	p3 := SensitiveWordConfigCacheKeyWithPatterns([]string{"a"}, map[string]string{"x": "1"}, []config.SensitivePatternRule{
		{Pattern: "foo=bar;1:x", Replacement: "baz"},
	})
	if p1 == p2 || p1 == p3 || p2 == p3 {
		t.Fatalf("expected distinct pattern cache keys, got %q, %q, %q", p1, p2, p3)
	}
}

func TestSensitivePatternRulesOnlyNonCascadingAndDevin(t *testing.T) {
	matcher := BuildAntigravitySensitiveWordMatcherWithPatterns(
		[]string{"Claude", "omp"},
		map[string]string{"Codex": "Assistant"},
		[]config.SensitivePatternRule{
			{Pattern: `[invalid`, Replacement: "skip"},
			{Pattern: `(?m)^You are (omp|Codex)[^\n]*\n?`, Replacement: "You are Claude ($1).\n"},
			{Pattern: `(?s)<system[-_](conventions|directive)>.*?</system[-_](conventions|directive)>\n?`, Replacement: ""},
		},
	)
	if matcher == nil {
		t.Fatal("expected non-nil matcher")
	}
	if !matcher.Matches("<system-directive>hello</system-directive>") {
		t.Fatal("Matches() = false, want true for pattern-only match")
	}

	input := "You are omp's assistant.\n<system-directive>\nRFC 2119\n</system-directive>\nKeep Codex and omp."
	got := matcher.ObfuscateText(input)
	want := "You are Claude (omp).\nKeep Assistant and o\u034Fmp."
	if got != want {
		t.Fatalf("ObfuscateText() = %q, want %q", got, want)
	}

	// Pattern-only matcher (no words, no literal replacements) must still run on docs.
	patternOnly := BuildSensitiveWordMatcherWithPatterns(nil, nil, []config.SensitivePatternRule{
		{Pattern: `(?m)^RFC 2119[^\n]*\n?`, Replacement: ""},
		{Pattern: `secret-\d+`, Replacement: "[redacted]"},
	})
	if patternOnly == nil {
		t.Fatal("expected non-nil pattern-only matcher")
	}
	doc := ojson.NewDocString(`{
		"system": [
			{"type": "text", "text": "x-anthropic-billing-header: secret-123"},
			{"type": "text", "text": "RFC 2119 applies.\nKeep secret-42."}
		],
		"messages": [
			{"role": "user", "content": "Check secret-99"}
		]
	}`)
	if !ObfuscateSensitiveWordsDoc(doc, patternOnly) {
		t.Fatal("expected ObfuscateSensitiveWordsDoc to modify document")
	}
	if gotBilling := doc.Get("system.0.text").String(); gotBilling != "x-anthropic-billing-header: secret-123" {
		t.Fatalf("billing header = %q, want untouched", gotBilling)
	}
	if gotSys := doc.Get("system.1.text").String(); gotSys != "Keep [redacted]." {
		t.Fatalf("system.1.text = %q, want %q", gotSys, "Keep [redacted].")
	}
	if gotMsg := doc.Get("messages.0.content").String(); gotMsg != "Check [redacted]" {
		t.Fatalf("messages.0.content = %q, want %q", gotMsg, "Check [redacted]")
	}

	// Devin: pattern match emits replacement verbatim (even containing a sensitive word) without dropping the line.
	devinMatcher := BuildSensitiveWordMatcherWithPatterns(
		[]string{"Claude", "DropMe"},
		nil,
		[]config.SensitivePatternRule{
			{Pattern: `You are omp\.`, Replacement: "You are Claude."},
		},
	)
	devinInput := "You are omp.\nRemove DropMe line.\nKeep final line."
	if gotDevin := devinMatcher.SanitizeDevinText(devinInput); gotDevin != "You are Claude.\nKeep final line." {
		t.Fatalf("SanitizeDevinText() = %q, want %q", gotDevin, "You are Claude.\nKeep final line.")
	}

	// Multi-line literal and pattern replacements must not reset dropCurrentLine
	// when the same original line contains an unreplaced sensitive word.
	multilineLiteral := BuildSensitiveWordMatcherWithReplacements(
		[]string{"DROP_ME"},
		map[string]string{"alias": "safe\ncontinued"},
	)
	if got := multilineLiteral.SanitizeDevinText("DROP_ME alias\nKeep"); got != "Keep" {
		t.Fatalf("SanitizeDevinText(DROP_ME before multiline literal) = %q, want %q", got, "Keep")
	}
	if got := multilineLiteral.SanitizeDevinText("alias DROP_ME\nKeep"); got != "Keep" {
		t.Fatalf("SanitizeDevinText(DROP_ME after multiline literal) = %q, want %q", got, "Keep")
	}
	if got := multilineLiteral.SanitizeDevinText("alias\nKeep"); got != "safe\ncontinued\nKeep" {
		t.Fatalf("SanitizeDevinText(multiline literal kept) = %q, want %q", got, "safe\ncontinued\nKeep")
	}

	multilinePattern := BuildSensitiveWordMatcherWithPatterns(
		[]string{"DROP_ME"},
		nil,
		[]config.SensitivePatternRule{{Pattern: `alias-\d+`, Replacement: "safe\ncontinued"}},
	)
	if got := multilinePattern.SanitizeDevinText("DROP_ME alias-1\nKeep"); got != "Keep" {
		t.Fatalf("SanitizeDevinText(DROP_ME before multiline pattern) = %q, want %q", got, "Keep")
	}
	if got := multilinePattern.SanitizeDevinText("alias-1 DROP_ME\nKeep"); got != "Keep" {
		t.Fatalf("SanitizeDevinText(DROP_ME after multiline pattern) = %q, want %q", got, "Keep")
	}

	// Pattern match consuming an original newline must flush/drop the finished line
	// and reset dropCurrentLine so subsequent lines remain intact.
	newlineConsumingPattern := BuildSensitiveWordMatcherWithPatterns(
		[]string{"DROP_ME"},
		nil,
		[]config.SensitivePatternRule{
			{Pattern: `alias-\d+\n`, Replacement: ""},
			{Pattern: `(?s)<system-conventions>.*?</system-conventions>\n?`, Replacement: ""},
		},
	)
	if got := newlineConsumingPattern.SanitizeDevinText("DROP_ME alias-1\nKeep"); got != "Keep" {
		t.Fatalf("SanitizeDevinText(DROP_ME + alias-1\\n) = %q, want %q", got, "Keep")
	}
	if got := newlineConsumingPattern.SanitizeDevinText("DROP_ME <system-conventions>\nRFC\n</system-conventions>\nKeep"); got != "Keep" {
		t.Fatalf("SanitizeDevinText(DROP_ME + multiline block) = %q, want %q", got, "Keep")
	}
	if got := newlineConsumingPattern.SanitizeDevinText("KeepPrefix alias-1\nDROP_ME\nKeepAfter"); got != "KeepPrefix KeepAfter" {
		t.Fatalf("SanitizeDevinText(KeepPrefix + alias-1\\n + DROP_ME) = %q, want %q", got, "KeepPrefix KeepAfter")
	}

	// Multi-line match with non-empty replacement must not inject synthetic newlines
	// and must preserve original-line drop ownership on both DROP_ME directions.
	multilineMatchRepl := BuildSensitiveWordMatcherWithPatterns(
		[]string{"DROP_ME"},
		map[string]string{"L1\nL2": "M"},
		[]config.SensitivePatternRule{
			{Pattern: `A\nB`, Replacement: "X"},
			{Pattern: `alias\n`, Replacement: "safe\n"},
			{Pattern: `lead\n`, Replacement: "safe"},
		},
	)
	if got := multilineMatchRepl.SanitizeDevinText("prefix A\nB suffix"); got != "prefix X suffix" {
		t.Fatalf("SanitizeDevinText(prefix A\\nB suffix) = %q, want %q", got, "prefix X suffix")
	}
	if got := multilineMatchRepl.SanitizeDevinText("prefix L1\nL2 suffix"); got != "prefix M suffix" {
		t.Fatalf("SanitizeDevinText(prefix L1\\nL2 suffix) = %q, want %q", got, "prefix M suffix")
	}
	if got := multilineMatchRepl.SanitizeDevinText("DROP_ME alias\nKeep"); got != "Keep" {
		t.Fatalf("SanitizeDevinText(DROP_ME alias\\nKeep) = %q, want %q", got, "Keep")
	}
	if got := multilineMatchRepl.SanitizeDevinText("lead\nDROP_ME\nKeep"); got != "safeKeep" {
		t.Fatalf("SanitizeDevinText(lead\\nDROP_ME\\nKeep) = %q, want %q", got, "safeKeep")
	}
}

func TestSensitivePatternRulesPreserveOriginalAnchorsAndNonCascadingDeletion(t *testing.T) {
	// Original-text anchor preservation: earlier matches must not synthesize ^ or $
	// anchors for subsequent pattern rules.
	prefixAnchorMatcher := BuildSensitiveWordMatcherWithPatterns(nil, nil, []config.SensitivePatternRule{
		{Pattern: `prefix`, Replacement: "safe"},
		{Pattern: `(?m)^KEEP`, Replacement: ""},
	})
	if got := prefixAnchorMatcher.ObfuscateText("prefixKEEP"); got != "safeKEEP" {
		t.Fatalf("ObfuscateText(prefixKEEP) = %q, want %q", got, "safeKEEP")
	}
	suffixAnchorMatcher := BuildSensitiveWordMatcherWithPatterns(nil, nil, []config.SensitivePatternRule{
		{Pattern: `suffix`, Replacement: "safe"},
		{Pattern: `(?m)KEEP$`, Replacement: ""},
	})
	if got := suffixAnchorMatcher.ObfuscateText("KEEPsuffix"); got != "KEEPsafe" {
		t.Fatalf("ObfuscateText(KEEPsuffix) = %q, want %q", got, "KEEPsafe")
	}

	// Non-cascading across empty deletion: deleting "DELETE" from "fooDELETEbar"
	// must not let subsequent regex/literal/sensitive-word rules match "foobar".
	nonCascade := BuildSensitiveWordMatcherWithPatterns(
		[]string{"foobar"},
		map[string]string{"foobar": "LITERAL_CHANGED"},
		[]config.SensitivePatternRule{
			{Pattern: `DELETE`, Replacement: ""},
			{Pattern: `foobar`, Replacement: "REGEX_CHANGED"},
		},
	)
	if got := nonCascade.ObfuscateText("fooDELETEbar"); got != "foobar" {
		t.Fatalf("ObfuscateText(fooDELETEbar) = %q, want %q", got, "foobar")
	}
	if gotDevin := nonCascade.SanitizeDevinText("fooDELETEbar\nkeep"); gotDevin != "foobar\nkeep" {
		t.Fatalf("SanitizeDevinText(fooDELETEbar) = %q, want %q", gotDevin, "foobar\nkeep")
	}

	// Superset deletion subsumes enclosed empty deletions (e.g. inner line deleted
	// before outer tag rule) without overwriting non-empty replacements.
	nestedDeletion := BuildAntigravitySensitiveWordMatcherWithPatterns(
		[]string{"RFC 2119"},
		nil,
		[]config.SensitivePatternRule{
			{Pattern: `(?m)^RFC 2119[^\n]*\n?`, Replacement: ""},
			{Pattern: `(?s)<system[-_]conventions>.*?</system[-_]conventions>\n?`, Replacement: ""},
		},
	)
	nestedInput := "<system-conventions>\nRFC 2119 applies.\n</system-conventions>\nKeep me."
	if got := nestedDeletion.ObfuscateText(nestedInput); got != "Keep me." {
		t.Fatalf("ObfuscateText(nestedInput) = %q, want %q", got, "Keep me.")
	}
}

func TestSensitivePatternRulesHighDensityLinearScaling(t *testing.T) {
	matcher := BuildSensitiveWordMatcherWithPatterns(
		[]string{"omp"},
		nil,
		[]config.SensitivePatternRule{
			{Pattern: `secret-\d+`, Replacement: "[redacted]"},
			{Pattern: `token-[a-z]+`, Replacement: ""},
		},
	)
	if matcher == nil {
		t.Fatal("expected non-nil matcher")
	}

	const repeats = 25000
	var inBuilder strings.Builder
	var wantBuilder strings.Builder
	inBuilder.Grow(repeats * 24)
	wantBuilder.Grow(repeats * 24)
	for i := 0; i < repeats; i++ {
		inBuilder.WriteString("secret-1 token-abc omp ")
		wantBuilder.WriteString("[redacted]  o\u200Bmp ")
	}
	got := matcher.ObfuscateText(inBuilder.String())
	if got != wantBuilder.String() {
		t.Fatalf("high-density ObfuscateText mismatch (got len=%d, want len=%d)", len(got), wantBuilder.Len())
	}
}

func BenchmarkObfuscateTextPatternsScaling(b *testing.B) {
	matcher := BuildSensitiveWordMatcherWithPatterns(
		[]string{"omp"},
		nil,
		[]config.SensitivePatternRule{
			{Pattern: `(?m)^RFC 2119[^\n]*\n?`, Replacement: ""},
			{Pattern: `secret-\d+`, Replacement: "[redacted]"},
			{Pattern: `(?s)<system-conventions>.*?</system-conventions>\n?`, Replacement: ""},
		},
	)
	for _, count := range []int{100, 1000, 10000} {
		var sb strings.Builder
		for i := 0; i < count; i++ {
			sb.WriteString("<system-conventions>\nRFC 2119 applies.\n</system-conventions>\nsecret-42 omp ")
		}
		input := sb.String()
		b.Run(fmt.Sprintf("n=%d", count), func(b *testing.B) {
			b.SetBytes(int64(len(input)))
			for i := 0; i < b.N; i++ {
				_ = matcher.ObfuscateText(input)
			}
		})
	}
}
