package payloadcompress

import "testing"

// TestPackRegexFallsBackForRE2Rejects pins the two-engine contract: patterns RE2
// accepts keep using it, and only the ones it rejects reach the backtracking
// engine. A regression that silently dropped fallback patterns again would show
// up here as a nil engine.
func TestPackRegexFallsBackForRE2Rejects(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		wantAdv bool
	}{
		{name: "plain", pattern: `\bfoo\b`, wantAdv: false},
		{name: "lookbehind", pattern: `(?<=\.)\s+([А-ЯЁ])`, wantAdv: true},
		{name: "lookahead", pattern: `(?:吗|呢|吧)(?=[。，！？、\s]|$)`, wantAdv: true},
		{name: "backreference", pattern: `([^.!?]+[.!?])\s+\1`, wantAdv: true},
	}
	for _, tc := range cases {
		re, err := compilePackRegex(tc.pattern)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if gotAdv := re.adv != nil; gotAdv != tc.wantAdv {
			t.Fatalf("%s: backtracking engine = %v, want %v", tc.name, gotAdv, tc.wantAdv)
		}
	}
}

// TestPackRegexAdvSpansAreByteOffsets guards the rune/byte mismatch: regexp2
// reports rune indices, and the guarded rewrite path slices bytes. Multibyte
// text is where a wrong unit would cut a character in half.
func TestPackRegexAdvSpansAreByteOffsets(t *testing.T) {
	re, err := compilePackRegex(`(?<=проблема )ошибка`)
	if err != nil {
		t.Fatal(err)
	}
	spans := re.matchSpans("проблема ошибка")
	if len(spans) != 1 {
		t.Fatalf("spans = %v", spans)
	}
	start, end := spans[0][0], spans[0][1]
	if got := "проблема ошибка"[start:end]; got != "ошибка" {
		t.Fatalf("span [%d:%d] = %q, want %q", start, end, got, "ошибка")
	}
}

// TestPackRegexAdvReplacementExpandsCaptures covers $1 through the fallback
// engine, which is the only path that can produce a backreference match.
func TestPackRegexAdvReplacementExpandsCaptures(t *testing.T) {
	re, err := compilePackRegex(`([\p{L}]+)\s+\1`)
	if err != nil {
		t.Fatal(err)
	}
	if got := re.replaceAllString("abc abc def", "$1"); got != "abc def" {
		t.Fatalf("got %q", got)
	}
}

// TestPackRegexesDropsOnlyUnparseable keeps a single bad pattern from taking the
// rest of a pack with it.
func TestPackRegexesDropsOnlyUnparseable(t *testing.T) {
	out := compilePackRegexes("test", "list", []string{`ok`, `(unclosed`, `(?<=x)y`})
	if len(out) != 2 {
		t.Fatalf("compiled %d patterns, want 2", len(out))
	}
}
