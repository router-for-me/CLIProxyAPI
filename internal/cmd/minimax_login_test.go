package cmd

import (
	"errors"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/minimax"
)

// TestPromptMinimaxRegionSkipsWhenNotTerminal proves a non-interactive run never
// blocks on stdin.
func TestPromptMinimaxRegionSkipsWhenNotTerminal(t *testing.T) {
	withStdinTerminal(t, false, func() {
		// A prompt function that would fail the test if it were ever called.
		region, ok := promptMinimaxRegion(func(string) (string, error) {
			t.Fatal("prompt must not run without a terminal")
			return "", nil
		})
		if ok {
			t.Fatalf("expected no prompt, got region %q", region)
		}
	})
}

// TestPromptMinimaxRegionPromptsWithTerminal proves an interactive run reaches
// the prompt.
func TestPromptMinimaxRegionPromptsWithTerminal(t *testing.T) {
	withStdinTerminal(t, true, func() {
		region, ok := promptMinimaxRegion(func(string) (string, error) { return "2", nil })
		if !ok {
			t.Fatal("expected the prompt to run")
		}
		if region != minimax.RegionCN {
			t.Fatalf("region = %q, want %q", region, minimax.RegionCN)
		}
	})
}

// withStdinTerminal substitutes the terminal check for the duration of fn.
func withStdinTerminal(t *testing.T, isTerminal bool, fn func()) {
	t.Helper()
	original := stdinIsTerminal
	stdinIsTerminal = func() bool { return isTerminal }
	t.Cleanup(func() { stdinIsTerminal = original })
	fn()
}

// TestPromptMinimaxRegionNilPrompt covers a caller that supplies no prompt at all.
func TestPromptMinimaxRegionNilPrompt(t *testing.T) {
	if _, ok := promptMinimaxRegion(nil); ok {
		t.Fatal("a nil prompt must not report a selection")
	}
}

// TestMinimaxRegionSelectionLogic exercises the accepted answers against the same
// matcher the prompt uses, without requiring a terminal.
func TestMinimaxRegionSelectionLogic(t *testing.T) {
	cases := []struct {
		answer string
		want   string
	}{
		{"", minimax.RegionGlobal},
		{"1", minimax.RegionGlobal},
		{" 1 ", minimax.RegionGlobal},
		{"global", minimax.RegionGlobal},
		{"GLOBAL", minimax.RegionGlobal},
		{"g", minimax.RegionGlobal},
		{"2", minimax.RegionCN},
		{"cn", minimax.RegionCN},
		{"CN", minimax.RegionCN},
		{"China", minimax.RegionCN},
		{"c", minimax.RegionCN},
	}
	for _, tc := range cases {
		got := matchMinimaxRegionAnswer(tc.answer)
		if got == "" {
			t.Fatalf("answer %q was not recognized", tc.answer)
		}
		if got != tc.want {
			t.Fatalf("answer %q = %q, want %q", tc.answer, got, tc.want)
		}
	}
	if got := matchMinimaxRegionAnswer("nope"); got != "" {
		t.Fatalf("invalid answer %q should not map to a region, got %q", "nope", got)
	}
}

// TestPromptMinimaxRegionRetriesOnInvalidAnswer drives the retry loop with a
// stub that does not require a real terminal.
func TestPromptMinimaxRegionRetriesOnInvalidAnswer(t *testing.T) {
	answers := []string{"nope", "also-bad", "2"}
	idx := 0
	prompt := func(string) (string, error) {
		if idx >= len(answers) {
			t.Fatal("prompt called more times than expected")
		}
		answer := answers[idx]
		idx++
		return answer, nil
	}

	region, ok := promptMinimaxRegionWithAnswers(prompt)
	if !ok {
		t.Fatal("expected a region to be selected")
	}
	if region != minimax.RegionCN {
		t.Fatalf("region = %q, want %q", region, minimax.RegionCN)
	}
	if idx != len(answers) {
		t.Fatalf("prompted %d times, want %d", idx, len(answers))
	}
}

// TestPromptMinimaxRegionFallsBackOnPromptError ensures a failing prompt does
// not abort the login.
func TestPromptMinimaxRegionFallsBackOnPromptError(t *testing.T) {
	region, ok := promptMinimaxRegionWithAnswers(func(string) (string, error) {
		return "", errors.New("stdin closed")
	})
	if ok {
		t.Fatalf("a prompt error must not report a selection, got %q", region)
	}
}
