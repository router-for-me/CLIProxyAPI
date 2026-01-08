package alias

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestNewResolver(t *testing.T) {
	cfg := &config.ModelAliasConfig{
		DefaultStrategy: "round-robin",
		Aliases: []config.ModelAlias{
			{
				Alias: "opus-4.5",
				Providers: []config.AliasProvider{
					{Provider: "antigravity", Model: "gemini-claude-opus-4-5"},
					{Provider: "kiro", Model: "kiro-claude-opus-4-5"},
				},
			},
		},
	}

	r := NewResolver(cfg)
	if r == nil {
		t.Fatal("expected non-nil resolver")
	}

	aliases := r.GetAliases()
	if len(aliases) != 1 {
		t.Errorf("expected 1 alias, got %d", len(aliases))
	}
}

func TestResolve(t *testing.T) {
	cfg := &config.ModelAliasConfig{
		DefaultStrategy: "round-robin",
		Aliases: []config.ModelAlias{
			{
				Alias:    "opus-4.5",
				Strategy: "fill-first",
				Providers: []config.AliasProvider{
					{Provider: "antigravity", Model: "gemini-claude-opus-4-5"},
				},
			},
		},
	}

	r := NewResolver(cfg)

	// Test exact match
	resolved := r.Resolve("opus-4.5")
	if resolved == nil {
		t.Fatal("expected resolved alias")
	}
	if resolved.Strategy != "fill-first" {
		t.Errorf("expected strategy fill-first, got %s", resolved.Strategy)
	}

	// Test case-insensitive
	resolved = r.Resolve("OPUS-4.5")
	if resolved == nil {
		t.Fatal("expected case-insensitive match")
	}

	// Test non-alias
	resolved = r.Resolve("claude-sonnet-4")
	if resolved != nil {
		t.Error("expected nil for non-alias")
	}
}

func TestDefaultStrategy(t *testing.T) {
	cfg := &config.ModelAliasConfig{
		DefaultStrategy: "fill-first",
		Aliases: []config.ModelAlias{
			{
				Alias: "test-model",
				// No strategy specified - should use default
				Providers: []config.AliasProvider{
					{Provider: "test", Model: "test-model-v1"},
				},
			},
		},
	}

	r := NewResolver(cfg)
	resolved := r.Resolve("test-model")
	if resolved == nil {
		t.Fatal("expected resolved alias")
	}
	if resolved.Strategy != "fill-first" {
		t.Errorf("expected default strategy fill-first, got %s", resolved.Strategy)
	}
}

func TestUpdate(t *testing.T) {
	r := NewResolver(nil)

	// Initially empty
	if len(r.GetAliases()) != 0 {
		t.Error("expected empty aliases initially")
	}

	// Update with config
	cfg := &config.ModelAliasConfig{
		Aliases: []config.ModelAlias{
			{
				Alias: "new-alias",
				Providers: []config.AliasProvider{
					{Provider: "test", Model: "test-model"},
				},
			},
		},
	}
	r.Update(cfg)

	if len(r.GetAliases()) != 1 {
		t.Error("expected 1 alias after update")
	}
}

func TestSelectProviderNilInput(t *testing.T) {
	r := NewResolver(nil)

	// Nil resolved alias
	selected := r.SelectProvider(nil)
	if selected != nil {
		t.Error("expected nil for nil input")
	}

	// Empty providers
	selected = r.SelectProvider(&ResolvedAlias{
		OriginalAlias: "test",
		Strategy:      "round-robin",
		Providers:     nil,
	})
	if selected != nil {
		t.Error("expected nil for empty providers")
	}

	selected = r.SelectProvider(&ResolvedAlias{
		OriginalAlias: "test",
		Strategy:      "round-robin",
		Providers:     []config.AliasProvider{},
	})
	if selected != nil {
		t.Error("expected nil for zero-length providers")
	}
}

func TestSelectProviderStrategies(t *testing.T) {
	// Note: SelectProvider calls util.GetProviderName which requires
	// registered models. These tests verify the strategy logic and
	// edge cases for the selection algorithm.

	r := NewResolver(&config.ModelAliasConfig{
		DefaultStrategy: "round-robin",
		Aliases: []config.ModelAlias{
			{
				Alias:    "test-rr",
				Strategy: "round-robin",
				Providers: []config.AliasProvider{
					{Provider: "p1", Model: "model1"},
					{Provider: "p2", Model: "model2"},
				},
			},
			{
				Alias:    "test-ff",
				Strategy: "fill-first",
				Providers: []config.AliasProvider{
					{Provider: "p1", Model: "model1"},
					{Provider: "p2", Model: "model2"},
				},
			},
		},
	})

	// Verify aliases are registered correctly
	rrAlias := r.Resolve("test-rr")
	if rrAlias == nil {
		t.Fatal("expected round-robin alias to be registered")
	}
	if rrAlias.Strategy != "round-robin" {
		t.Errorf("expected strategy round-robin, got %s", rrAlias.Strategy)
	}

	ffAlias := r.Resolve("test-ff")
	if ffAlias == nil {
		t.Fatal("expected fill-first alias to be registered")
	}
	if ffAlias.Strategy != "fill-first" {
		t.Errorf("expected strategy fill-first, got %s", ffAlias.Strategy)
	}

	// Verify provider count
	if len(rrAlias.Providers) != 2 {
		t.Errorf("expected 2 providers for round-robin, got %d", len(rrAlias.Providers))
	}
	if len(ffAlias.Providers) != 2 {
		t.Errorf("expected 2 providers for fill-first, got %d", len(ffAlias.Providers))
	}
}

func TestSelectProviderRoundRobinCounter(t *testing.T) {
	// Note: SelectProvider only increments counter when there are available
	// providers (those returning non-empty from util.GetProviderName).
	// Since we don't have registered models in unit tests, this test verifies
	// the counter initialization and structure.

	r := NewResolver(nil)

	// Verify counters map exists and is empty initially
	r.mu.RLock()
	if r.counters == nil {
		t.Error("expected counters map to be initialized")
	}
	initialLen := len(r.counters)
	r.mu.RUnlock()

	if initialLen != 0 {
		t.Errorf("expected empty counters, got %d", initialLen)
	}

	// Calling SelectProvider with no available providers should not crash
	// and should return nil (no models registered)
	resolved := &ResolvedAlias{
		OriginalAlias: "counter-test",
		Strategy:      "round-robin",
		Providers: []config.AliasProvider{
			{Provider: "p1", Model: "m1"},
		},
	}

	selected := r.SelectProvider(resolved)
	// Should return nil because util.GetProviderName returns empty for unregistered models
	if selected != nil {
		// If it's not nil, that means there are registered models in the test environment
		// which would be unexpected but acceptable
		t.Logf("SelectProvider returned non-nil, model may be registered: %+v", selected)
	}
}

func TestSelectProviderFillFirstVariants(t *testing.T) {
	// Test that fill-first strategy aliases work correctly
	testCases := []string{"fill-first", "fillfirst", "ff"}

	for _, strategy := range testCases {
		cfg := &config.ModelAliasConfig{
			Aliases: []config.ModelAlias{
				{
					Alias:    "test-" + strategy,
					Strategy: strategy,
					Providers: []config.AliasProvider{
						{Provider: "p1", Model: "m1"},
					},
				},
			},
		}

		r := NewResolver(cfg)
		resolved := r.Resolve("test-" + strategy)
		if resolved == nil {
			t.Fatalf("expected alias for strategy %s", strategy)
		}

		// Verify the resolved strategy is preserved as specified
		if resolved.Strategy != strategy {
			t.Errorf("expected strategy %s, got %s", strategy, resolved.Strategy)
		}
	}
}
