package payloadcompress

import (
	"strings"
	"testing"
)

func TestRelevanceDefaultsMatchUpstream(t *testing.T) {
	budget, threshold := relevanceConfig(StepOptions{})
	if budget != 0.5 || threshold != 0.1 {
		t.Fatalf("defaults = (%v, %v), want (0.5, 0.1)", budget, threshold)
	}
}

func TestRelevanceShrinksOlderUserTurn(t *testing.T) {
	old := "The weather is warm. " +
		"We shipped the database migration with a postgres index. " +
		"Please note this important thing."
	query := "database migration postgres index"

	got := relevanceEngine{}.rewrite(
		[]string{old, query},
		[]segmentKind{kindUser, kindUserLatest},
		[]bool{false, true},
	)
	replaced, ok := got[0]
	if !ok {
		t.Fatal("older user turn was not rewritten")
	}
	if len(replaced) >= len(old) {
		t.Fatalf("older turn did not shrink: %d >= %d", len(replaced), len(old))
	}
	if !strings.Contains(replaced, "postgres index") {
		t.Fatalf("relevant sentence dropped: %q", replaced)
	}
	if _, touched := got[1]; touched {
		t.Fatal("latest user turn must not be rewritten")
	}
}

func TestRelevanceLeavesLatestTurn(t *testing.T) {
	text := "First sentence. Second sentence with database migration postgres index."

	got := relevanceEngine{}.rewrite(
		[]string{text},
		[]segmentKind{kindUserLatest},
		[]bool{true},
	)
	if len(got) != 0 {
		t.Fatalf("latest turn rewritten: %v", got)
	}
}

func TestRelevanceSkipsSystemAndToolSegments(t *testing.T) {
	text := "First sentence. Second sentence about database migration postgres index."
	got := relevanceEngine{}.rewrite(
		[]string{text, text, "database migration postgres index"},
		[]segmentKind{kindSystem, kindToolOutput, kindUserLatest},
		[]bool{false, false, true},
	)
	if len(got) != 0 {
		t.Fatalf("system/tool segments rewritten: %v", got)
	}
}

func TestRelevanceForcePreservesSignals(t *testing.T) {
	old := "The weather is warm. " +
		"The build failed with TypeError: bad input. " +
		"Run ```go test ./...``` now. " +
		"The stale cache holds 42 entries. " +
		"See https://example.com/docs for details. " +
		"at main.go:12 in handler"
	query := "database migration postgres index"

	got := relevanceEngine{opts: StepOptions{RelevanceBudgetPercent: 0.05}}.rewrite(
		[]string{old, query},
		[]segmentKind{kindUser, kindUserLatest},
		[]bool{false, true},
	)
	replaced, ok := got[0]
	if !ok {
		t.Fatal("older user turn was not rewritten")
	}
	for _, keep := range []string{
		"TypeError: bad input",
		"```go test ./...```",
		"42 entries",
		"https://example.com/docs",
		"at main.go:12",
	} {
		if !strings.Contains(replaced, keep) {
			t.Errorf("force-preserved sentence dropped, missing %q in %q", keep, replaced)
		}
	}
	if strings.Contains(replaced, "weather") {
		t.Errorf("unrelated prose survived tiny budget: %q", replaced)
	}
}

func TestRelevanceRespectsBudget(t *testing.T) {
	old := "alpha x. beta y. gamma z. delta w."
	query := "alpha beta gamma delta"

	got := relevanceEngine{opts: StepOptions{RelevanceBudgetPercent: 0.2}}.rewrite(
		[]string{old, query},
		[]segmentKind{kindUser, kindUserLatest},
		[]bool{false, true},
	)
	replaced, ok := got[0]
	if !ok {
		t.Fatal("older user turn was not rewritten")
	}
	// Upstream attaches the sentence-final whitespace to the preceding sentence.
	if replaced != "alpha x. " {
		t.Fatalf("budget admitted unexpected sentences: %q", replaced)
	}
}

func TestRelevanceNoOpForSingleSentence(t *testing.T) {
	query := "database migration postgres index"
	old := "The weather is warm today."

	got := relevanceEngine{}.rewrite(
		[]string{old, query},
		[]segmentKind{kindUser, kindUserLatest},
		[]bool{false, true},
	)
	if len(got) != 0 {
		t.Fatalf("single-sentence text rewritten: %v", got)
	}
}
