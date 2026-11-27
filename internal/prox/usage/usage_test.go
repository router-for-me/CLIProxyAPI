package usage

import (
	"math"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestPricerLookup(t *testing.T) {
	p := NewPricer(map[string]Price{"gpt-6-luna": {Input: 2, Output: 8}})
	cases := map[string]float64{
		"claude-opus-5-5":            4,
		"claude-sonnet-4-5-20250929": 3,
		"claude-haiku-5-5(high)":     0.10,
		"anthropic/claude-fable-5-2": 8,
		"gpt-5-codex":                1.25,
		"gpt-5-mini":                 0.25,
		"GPT-6-Luna":                 2,
		"claude-opus-5-5-thinking":   4,
	}
	for model, want := range cases {
		got, ok := p.Lookup(model)
		if !ok || got.Input != want {
			t.Errorf("%s: got %+v ok=%v want input %v", model, got, ok, want)
		}
	}
	if _, ok := p.Lookup("gpt-5.2"); ok {
		t.Error("unknown dotted version must not inherit gpt-5 price")
	}
	if _, ok := p.Lookup("kimi-k3"); ok {
		t.Error("kimi has no default price")
	}
}

func TestCost(t *testing.T) {
	p, _ := NewPricer(nil).Lookup("claude-opus-5-5")
	c := p.Cost(Tokens{Input: 1_000_000, Output: 1_000_000, CacheRead: 1_000_000, CacheWrite: 1_000_000})
	if !near(c, 4+20+0.20+5) {
		t.Fatalf("cost %v", c)
	}
}

func TestTokensFromDetail(t *testing.T) {
	codex := TokensFromDetail("codex", coreusage.Detail{InputTokens: 1000, CacheReadTokens: 800, OutputTokens: 50})
	if codex.Input != 200 || codex.CacheRead != 800 {
		t.Fatalf("codex %+v", codex)
	}
	claude := TokensFromDetail("claude", coreusage.Detail{InputTokens: 10, CacheReadTokens: 800, CacheCreationTokens: 100, OutputTokens: 50})
	if claude.Input != 10 || claude.CacheWrite != 100 {
		t.Fatalf("claude %+v", claude)
	}
	b := coreusage.NewSubsetTokenBreakdown(1000, 600, 0, 100, 40, 1100)
	got := TokensFromDetail("codex", coreusage.Detail{TokenBreakdown: b})
	if got.Input != 400 || got.CacheRead != 600 || got.Output != 100 || got.Reasoning != 40 {
		t.Fatalf("breakdown %+v (valid=%v)", got, b.Valid())
	}
}

func TestLedgerGroupRollupAndPersist(t *testing.T) {
	l := NewLedger(NewPricer(nil), 30*24*time.Hour)
	now := time.Date(2026, 11, 27, 12, 30, 0, 0, time.UTC)
	l.Add(now, "claude", "a", "claude-opus-5-5", "claude", false, Tokens{Input: 1_000_000})
	l.Add(now.Add(10*time.Minute), "claude", "a", "claude-opus-5-5", "claude", true, Tokens{Output: 1_000_000})
	l.Add(now, "codex", "b", "gpt-6-luna", "codex", false, Tokens{Input: 5})
	rows := l.Run(Query{GroupBy: []string{"provider"}})
	if len(rows) != 2 || rows[0].Provider != "claude" || !near(rows[0].CostUSD, 24) || rows[0].Requests != 2 || rows[0].Failures != 1 {
		t.Fatalf("rows %+v", rows)
	}
	if rows[1].Unpriced != 1 {
		t.Fatalf("expected unpriced codex row: %+v", rows[1])
	}
	// Old hourly rows roll into a day bucket; totals are kept.
	old := now.Add(-10 * 24 * time.Hour)
	l.Add(old, "claude", "a", "claude-opus-5-5", "claude", false, Tokens{Input: 1_000_000})
	l.Add(old.Add(3*time.Hour), "claude", "a", "claude-opus-5-5", "claude", false, Tokens{Input: 1_000_000})
	l.Prune(now)
	daily := l.Run(Query{Until: now.Add(-9 * 24 * time.Hour), GroupBy: []string{"model"}, Step: 24 * time.Hour})
	if len(daily) != 1 || daily[0].Requests != 2 || !near(daily[0].CostUSD, 8) {
		t.Fatalf("daily %+v", daily)
	}
	l2 := NewLedger(NewPricer(nil), 30*24*time.Hour)
	l2.Import(l.Export())
	if got := l2.Run(Query{}); len(got) != 1 || got[0].Requests != 5 {
		t.Fatalf("import %+v", got)
	}
}
