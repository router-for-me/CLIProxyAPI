package thinking_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
)

// TestConvertUltraLevelToBudget guards the budget-only provider path for the
// "ultra" thinking level introduced by Codex GPT-6 Astra.
//
// Regression: ParseLevelSuffix accepts "ultra" and produces ModeLevel/LevelUltra.
// For budget-only models (legacy Claude, Gemini 2.5) ValidateConfig delegates to
// ConvertLevelToBudget. Without an "ultra" entry that lookup returns ok=false and
// the request fails with ErrUnknownLevel instead of clamping to a numeric budget
// the way "max" does.
func TestConvertUltraLevelToBudget(t *testing.T) {
	budget, ok := thinking.ConvertLevelToBudget(string(thinking.LevelUltra))
	if !ok {
		t.Fatalf("ConvertLevelToBudget(%q) returned ok=false; ultra must be convertible so budget-only providers do not fail with ErrUnknownLevel", thinking.LevelUltra)
	}
	if budget <= 0 {
		t.Fatalf("ConvertLevelToBudget(%q) = %d, want a positive budget", thinking.LevelUltra, budget)
	}

	maxBudget, ok := thinking.ConvertLevelToBudget(string(thinking.LevelMax))
	if !ok {
		t.Fatalf("ConvertLevelToBudget(%q) returned ok=false", thinking.LevelMax)
	}
	// Ultra is the highest effort level, so it must not resolve to a smaller
	// budget than max.
	if budget < maxBudget {
		t.Fatalf("ultra budget %d < max budget %d; ultra is the highest effort level", budget, maxBudget)
	}
}

// TestConvertUltraLevelToBudgetIsCaseInsensitive mirrors the case-insensitive
// contract documented on ConvertLevelToBudget.
func TestConvertUltraLevelToBudgetIsCaseInsensitive(t *testing.T) {
	for _, level := range []string{"ultra", "Ultra", "ULTRA"} {
		if _, ok := thinking.ConvertLevelToBudget(level); !ok {
			t.Fatalf("ConvertLevelToBudget(%q) returned ok=false, want case-insensitive match", level)
		}
	}
}

// TestParseUltraSuffixRoundTripsToBudget covers the full path the reviewer
// flagged: suffix parsing produces LevelUltra, which must then survive the
// level -> budget conversion used by budget-only providers.
func TestParseUltraSuffixRoundTripsToBudget(t *testing.T) {
	level, ok := thinking.ParseLevelSuffix("ultra")
	if !ok {
		t.Fatalf(`ParseLevelSuffix("ultra") returned ok=false, want LevelUltra`)
	}
	if level != thinking.LevelUltra {
		t.Fatalf("ParseLevelSuffix(\"ultra\") = %q, want %q", level, thinking.LevelUltra)
	}
	if _, ok := thinking.ConvertLevelToBudget(string(level)); !ok {
		t.Fatal("parsed ultra level cannot be converted to a budget; budget-only providers would fail with ErrUnknownLevel")
	}
}
