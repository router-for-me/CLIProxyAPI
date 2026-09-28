package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/autorouter"
)

// autorouterTier converts a test literal into an autorouter.Tier.
func autorouterTier(s string) autorouter.Tier { return autorouter.Tier(s) }

func TestTierDistanceIsSigned(t *testing.T) {
	cases := []struct {
		want, got string
		wantDist  int
	}{
		{"simple", "simple", 0},
		{"simple", "reasoning", 3},  // predictor went harder than needed
		{"reasoning", "simple", -3}, // predictor went cheaper than needed
		{"medium", "complex", 1},
		{"complex", "medium", -1},
	}
	for _, tc := range cases {
		got := tierDistance(autorouterTier(tc.want), autorouterTier(tc.got))
		if got != tc.wantDist {
			t.Errorf("tierDistance(%s, %s) = %d, want %d", tc.want, tc.got, got, tc.wantDist)
		}
	}
}

// A perfect predictor must report full accuracy, no over/under routing, and a
// zero mean error — the baseline that makes any other number interpretable.
func TestSummarizePerfectPredictor(t *testing.T) {
	outcomes := []outcome{
		{want: "simple", heuristic: "simple"},
		{want: "medium", heuristic: "medium"},
		{want: "complex", heuristic: "complex"},
	}
	heuristic, hybrid, classifier := summarize(outcomes)
	if heuristic.exact != 3 || heuristic.n != 3 {
		t.Errorf("heuristic = %+v, want 3/3 exact", heuristic)
	}
	if heuristic.over != 0 || heuristic.under != 0 {
		t.Errorf("perfect predictor must not over/under route: %+v", heuristic)
	}
	if heuristic.meanDistance() != 0 {
		t.Errorf("mean distance = %v, want 0", heuristic.meanDistance())
	}
	// With no classifier verdicts the hybrid collapses onto the heuristic.
	if hybrid.exact != heuristic.exact {
		t.Errorf("hybrid without verdicts must equal the heuristic")
	}
	if classifier.n != 0 {
		t.Errorf("classifier leg must ignore cases with no verdict, got n=%d", classifier.n)
	}
}

// An accepted verdict replaces the heuristic in the hybrid but leaves the
// heuristic leg's own numbers untouched, so the two remain comparable.
func TestSummarizeAcceptedVerdictMovesHybridOnly(t *testing.T) {
	outcomes := []outcome{
		{want: "reasoning", heuristic: "simple", classifier: "reasoning", accepted: true},
		{want: "simple", heuristic: "simple", classifier: "complex", accepted: true},
	}
	heuristic, hybrid, classifier := summarize(outcomes)

	if heuristic.exact != 1 {
		t.Errorf("heuristic exact = %d, want 1 (only the second case)", heuristic.exact)
	}
	if hybrid.exact != 1 {
		t.Errorf("hybrid exact = %d, want 1 (the verdict fixed the first, broke the second)", hybrid.exact)
	}
	if classifier.exact != 1 || classifier.n != 2 {
		t.Errorf("classifier = %+v, want 1/2", classifier)
	}
	// The first case: heuristic was 3 steps too cheap, the hybrid is exact.
	if heuristic.under != 1 {
		t.Errorf("heuristic under = %d, want 1", heuristic.under)
	}
}

// A rejected verdict must not reach the hybrid: the heuristic is what routed.
func TestSummarizeRejectedVerdictDoesNotMoveHybrid(t *testing.T) {
	outcomes := []outcome{
		{want: "simple", heuristic: "simple", classifier: "reasoning", accepted: false},
	}
	heuristic, hybrid, _ := summarize(outcomes)
	if hybrid.exact != heuristic.exact || hybrid.exact != 1 {
		t.Errorf("a rejected verdict must leave the hybrid on the heuristic: %+v", hybrid)
	}
}

// Errored cases are excluded from every leg: an API outage must not read as
// classifier inaccuracy.
func TestSummarizeSkipsErrors(t *testing.T) {
	outcomes := []outcome{
		{want: "simple", heuristic: "simple", err: "error"},
		{want: "simple", heuristic: "simple"},
	}
	heuristic, _, _ := summarize(outcomes)
	if heuristic.n != 1 {
		t.Errorf("errored case must be excluded, got n=%d", heuristic.n)
	}
}

func TestLoadCorpusSkipsCommentsAndBlanks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "corpus.jsonl")
	body := `# a comment

{"id":"a","tier":"simple","text":"hi"}
{"id":"b","tier":"reasoning","text":"prove it"}
`
	if errWrite := os.WriteFile(path, []byte(body), 0o600); errWrite != nil {
		t.Fatalf("write corpus: %v", errWrite)
	}
	cases, errLoad := loadCorpus(path)
	if errLoad != nil {
		t.Fatalf("loadCorpus: %v", errLoad)
	}
	if len(cases) != 2 {
		t.Fatalf("cases = %d, want 2", len(cases))
	}
	if cases[0].ID != "a" || cases[1].ID != "b" {
		t.Errorf("ids = %q, %q", cases[0].ID, cases[1].ID)
	}
}

func TestLoadCorpusRejectsUnknownTier(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "corpus.jsonl")
	if errWrite := os.WriteFile(path, []byte(`{"id":"a","tier":"turbo","text":"hi"}`), 0o600); errWrite != nil {
		t.Fatalf("write corpus: %v", errWrite)
	}
	if _, errLoad := loadCorpus(path); errLoad == nil {
		t.Fatal("an unknown tier must be rejected rather than silently mislabelled")
	}
}
