package engine

import (
	"context"
	"math"
	"testing"
	"time"

	proxusage "github.com/router-for-me/CLIProxyAPI/v8/internal/prox/usage"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestPlanWeight(t *testing.T) {
	cases := []struct {
		provider, plan string
		want           float64
	}{
		{"claude", "max_20x", 20}, {"claude", "Max 5x", 5}, {"claude", "pro", 1},
		{"codex", "pro", 10}, {"codex", "plus", 1}, {"codex", "free", 0.1}, {"kimi", "x", 1},
	}
	for _, c := range cases {
		if got := PlanWeight(c.provider, c.plan); got != c.want {
			t.Errorf("%s/%s: %v", c.provider, c.plan, got)
		}
	}
}

func TestUsageLedgerFromRecords(t *testing.T) {
	e := New()
	e.SetHost(fakeHost{})
	if err := e.Apply(true, []byte("state_dir: "+t.TempDir()+"\nusage:\n  pricing:\n    kimi-k3: {input: 1, output: 2}\n")); err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	at := time.Now()
	e.HandleUsage(context.Background(), coreusage.Record{Provider: "claude", AuthID: "a", Model: "claude-opus-5-5", SessionID: "claude:s1", RequestedAt: at,
		Detail: coreusage.Detail{InputTokens: 1_000_000, OutputTokens: 100_000}})
	e.HandleUsage(context.Background(), coreusage.Record{Provider: "kimi-ai", AuthID: "k", Model: "kimi-k3", RequestedAt: at,
		Detail: coreusage.Detail{InputTokens: 1_000_000}})
	f := false
	e.HandleUsage(context.Background(), coreusage.Record{Provider: "claude", AuthID: "a", Model: "claude-opus-5-5", RequestedAt: at, Generate: &f,
		Detail: coreusage.Detail{InputTokens: 9_000_000}})
	rows := e.Ledger().Run(proxusage.Query{GroupBy: []string{"provider", "client"}})
	if len(rows) != 2 {
		t.Fatalf("rows: %+v", rows)
	}
	for _, r := range rows {
		switch r.Provider {
		case "claude":
			if r.Client != "claude" || math.Abs(r.CostUSD-6) > 1e-9 || r.Requests != 1 {
				t.Fatalf("claude row: %+v", r)
			}
		case "kimi":
			if math.Abs(r.CostUSD-1) > 1e-9 {
				t.Fatalf("kimi row: %+v", r)
			}
		default:
			t.Fatalf("unexpected row %+v", r)
		}
	}
}
