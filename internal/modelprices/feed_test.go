package modelprices

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

const sample = `{
  "sample_spec": {"input_cost_per_token": 0, "output_cost_per_token": 0},
  "gpt-6.1-sol": {"input_cost_per_token": 2e-06, "output_cost_per_token": 1e-05, "cache_read_input_token_cost": 1e-07, "litellm_provider": "openai", "mode": "responses"},
  "azure/gpt-6.1-sol": {"input_cost_per_token": 9e-06, "output_cost_per_token": 9e-05, "litellm_provider": "azure", "mode": "chat"},
  "bedrock/anthropic.claude-x": {"input_cost_per_token": 9e-06, "output_cost_per_token": 9e-05, "litellm_provider": "bedrock"},
  "openrouter/anthropic/claude-opus-5-5": {"input_cost_per_token": 9e-06, "output_cost_per_token": 9e-05, "litellm_provider": "openrouter", "mode": "chat"},
  "anthropic/claude-opus-5-5": {"input_cost_per_token": 4e-06, "output_cost_per_token": 2e-05, "cache_read_input_token_cost": 2e-07, "cache_creation_input_token_cost": 5e-06, "litellm_provider": "anthropic", "mode": "chat"},
  "text-embedding-x": {"input_cost_per_token": 1e-07, "output_cost_per_token": 0, "mode": "embedding"},
  "broken": {"input_cost_per_token": "n/a"}
}`

func near(a, b float64) bool { return a-b < 1e-9 && b-a < 1e-9 }

func TestParsePrefersUnprefixedAndFirstPartyEntries(t *testing.T) {
	prices, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	sol := prices["gpt-6.1-sol"]
	if !near(sol.Input, 2) || !near(sol.Output, 10) || !near(sol.CachedInput, 0.1) {
		t.Fatalf("gpt-6.1-sol = %+v", sol)
	}
	opus := prices["claude-opus-5-5"]
	if !near(opus.Input, 4) || !near(opus.CacheWrite, 5) || !near(opus.CachedInput, 0.2) {
		t.Fatalf("claude-opus-5-5 = %+v, want anthropic entry over openrouter", opus)
	}
	for _, skipped := range []string{"sample_spec", "text-embedding-x", "broken"} {
		if _, ok := prices[skipped]; ok {
			t.Fatalf("%s should be skipped", skipped)
		}
	}
	if _, err = Parse([]byte("not json")); err == nil {
		t.Fatal("invalid JSON must fail")
	}
}

func TestRefreshCachesAndLookups(t *testing.T) {
	cfg := &config.Config{}
	path := filepath.Join(t.TempDir(), "model-prices-cache.json")
	f := New()
	calls := 0
	f.fetch = func(context.Context, string, *config.Config) ([]byte, error) {
		calls++
		return []byte(sample), nil
	}
	// Set up without Init so no background refresh races with the test.
	f.cfgFn = func() *config.Config { return cfg }
	f.cachePath = path
	updates := 0
	f.OnUpdate(func() { updates++ })
	if err := f.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if updates != 1 {
		t.Fatalf("listener calls = %d", updates)
	}
	if p, ok := f.Lookup("unknown", "OpenAI/GPT-6.1-Sol"); !ok || !near(p.Input, 2) {
		t.Fatalf("lookup = %+v %v", p, ok)
	}
	if st := f.Status(); !st.Enabled || st.Models != 3 || st.URL != config.DefaultModelPriceFeedURL || st.UpdatedAt.IsZero() {
		t.Fatalf("status = %+v", st)
	}

	// A fresh feed loads the cached copy without downloading.
	g := New()
	g.fetch = func(context.Context, string, *config.Config) ([]byte, error) { return nil, errors.New("offline") }
	g.cfgFn = func() *config.Config { return cfg }
	g.cachePath = path
	g.loadCache()
	if _, ok := g.Lookup("claude-opus-5-5"); !ok {
		t.Fatal("cached prices not loaded")
	}
	// Not due yet: the cache is fresh.
	g.refreshIfDue(context.Background())
	if g.Status().LastError != "" {
		t.Fatal("fresh cache must not trigger a download")
	}
	// Due after the refresh interval; a failed download keeps the old prices.
	g.now = func() time.Time { return time.Now().Add(25 * time.Hour) }
	g.refreshIfDue(context.Background())
	if st := g.Status(); st.LastError == "" || st.Models != 3 {
		t.Fatalf("status after failed refresh = %+v", st)
	}

	cfg.ModelPriceFeed.Disabled = true
	if _, ok := g.Lookup("claude-opus-5-5"); ok {
		t.Fatal("disabled feed must not price models")
	}
	if calls != 1 {
		t.Fatalf("downloads = %d", calls)
	}
}
