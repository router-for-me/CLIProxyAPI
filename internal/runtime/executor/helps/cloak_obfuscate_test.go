package helps

import (
	"strings"
	"testing"
)

// TestBuildSensitiveWordMatcherMemoized verifies that identical word lists
// return the same compiled matcher pointer, confirming memoization replaces
// per-request regexp.Compile.
func TestBuildSensitiveWordMatcherMemoized(t *testing.T) {
	words := []string{"secret", "password", "token"}

	first := BuildSensitiveWordMatcher(words)
	second := BuildSensitiveWordMatcher([]string{"secret", "password", "token"})

	if first == nil || second == nil {
		t.Fatalf("expected non-nil matchers, got first=%v second=%v", first, second)
	}
	if first != second {
		t.Fatalf("expected same matcher pointer for identical word list, got %p and %p", first, second)
	}
}

// TestBuildSensitiveWordMatcherDistinctWords verifies distinct word lists yield
// distinct matchers.
func TestBuildSensitiveWordMatcherDistinctWords(t *testing.T) {
	a := BuildSensitiveWordMatcher([]string{"alpha", "beta"})
	b := BuildSensitiveWordMatcher([]string{"gamma", "delta"})

	if a == b {
		t.Fatalf("expected distinct matchers for distinct word lists")
	}
}

// TestBuildSensitiveWordMatcherEmpty verifies the no-word and no-valid-word
// cases return nil (and remain consistent across calls).
func TestBuildSensitiveWordMatcherEmpty(t *testing.T) {
	if m := BuildSensitiveWordMatcher(nil); m != nil {
		t.Fatalf("expected nil matcher for empty word list, got %v", m)
	}
	// All words too short (<2 runes) → no valid words → nil.
	if m := BuildSensitiveWordMatcher([]string{"a", "b"}); m != nil {
		t.Fatalf("expected nil matcher when no valid words, got %v", m)
	}
}

// TestObfuscateSensitiveWordsStillWorks confirms the memoized matcher still
// obfuscates system and message content as before.
func TestObfuscateSensitiveWordsStillWorks(t *testing.T) {
	matcher := BuildSensitiveWordMatcher([]string{"secret"})
	if matcher == nil {
		t.Fatalf("expected matcher")
	}

	payload := []byte(`{"system":"a secret value","messages":[{"role":"user","content":"another secret"}]}`)
	out := ObfuscateSensitiveWords(payload, matcher)

	if !strings.Contains(string(out), zeroWidthSpace) {
		t.Fatalf("expected obfuscated output to contain zero-width space, got %s", out)
	}
	if strings.Count(string(out), zeroWidthSpace) != 2 {
		t.Fatalf("expected two obfuscations (system + message), got %d in %s", strings.Count(string(out), zeroWidthSpace), out)
	}
}
