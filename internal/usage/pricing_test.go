package usage

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestPricingProviderTokenSemantics(t *testing.T) {
	table := compilePricing(config.UsagePricingConfig{
		Currency: "USD",
		Version:  "rates",
		Rules: []config.UsagePricingRule{{
			Provider: "*", Model: "*", ServiceTier: "*",
			InputPerMillion: "1.5", OutputPerMillion: "2",
			CacheReadPerMillion: "0.5", CacheWritePerMillion: "0.25",
		}},
	})

	tests := []struct {
		name     string
		provider string
		detail   coreusage.Detail
		want     int64
	}{
		{
			name:     "openai cached input and reasoning already included",
			provider: "openai",
			detail:   coreusage.Detail{InputTokens: 1100, OutputTokens: 200, ReasoningTokens: 50, CachedTokens: 100},
			want:     1950,
		},
		{
			name:     "claude cache counters are disjoint",
			provider: "claude",
			detail:   coreusage.Detail{InputTokens: 1000, OutputTokens: 200, CacheReadTokens: 100, CacheCreationTokens: 50},
			want:     1975,
		},
		{
			name:     "gemini thinking is added to output",
			provider: "gemini",
			detail:   coreusage.Detail{InputTokens: 1100, OutputTokens: 200, ReasoningTokens: 50, CachedTokens: 100},
			want:     2050,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, unpriced, unpricedAttempt := table.calculate(tt.provider, "team/model", "default", normalizeBillableTokens(tt.provider, tt.detail))
			if got != tt.want || unpriced != 0 || unpricedAttempt {
				t.Fatalf("calculate() = (%d, %d, %t), want (%d, 0, false)", got, unpriced, unpricedAttempt, tt.want)
			}
		})
	}
}

func TestPricingMergesCacheReadAndWriteAsCacheInput(t *testing.T) {
	table := compilePricing(config.UsagePricingConfig{Rules: []config.UsagePricingRule{{
		Provider: "*", Model: "*", ServiceTier: "*",
		InputPerMillion: "1", OutputPerMillion: "8",
		CacheReadPerMillion: "0.1", CacheWritePerMillion: "9",
	}}})
	cost, unpriced, unpricedAttempt := table.calculate("claude", "model", "default", billableTokens{
		input: 80, cacheRead: 20, cacheWrite: 10, output: 5,
	})
	if cost != 123 || unpriced != 0 || unpricedAttempt {
		t.Fatalf("merged cache-input calculation = (%d, %d, %t), want (123, 0, false)", cost, unpriced, unpricedAttempt)
	}
}

func TestPricingGlobMatchesSlashAndFirstRuleWins(t *testing.T) {
	table := compilePricing(config.UsagePricingConfig{Rules: []config.UsagePricingRule{
		{Provider: "open*", Model: "*", ServiceTier: "?efault", InputPerMillion: "1"},
		{Provider: "*", Model: "*", ServiceTier: "*", InputPerMillion: "9"},
	}})
	cost, unpriced, unpricedAttempt := table.calculate("OPENAI", "team/gpt-5", "default", billableTokens{input: 7})
	if cost != 7 || unpriced != 0 || unpricedAttempt {
		t.Fatalf("calculate() = (%d, %d, %t), want (7, 0, false)", cost, unpriced, unpricedAttempt)
	}
	if !globMatches("*", "team/gpt-5") {
		t.Fatal("glob '*' did not match a model containing slash")
	}
}

func TestPricingUnknownAndPartialRulesAreExplicitlyUnpriced(t *testing.T) {
	table := compilePricing(config.UsagePricingConfig{Rules: []config.UsagePricingRule{{
		Provider: "openai", Model: "gpt-*", ServiceTier: "*", InputPerMillion: "1",
	}}})

	_, unpriced, unpricedAttempt := table.calculate("claude", "claude-4", "default", billableTokens{})
	if unpriced != 0 || !unpricedAttempt {
		t.Fatalf("unknown zero-token attempt = (%d, %t), want (0, true)", unpriced, unpricedAttempt)
	}
	_, unpriced, unpricedAttempt = table.calculate("openai", "gpt-5", "default", billableTokens{input: 10, output: 4})
	if unpriced != 4 || !unpricedAttempt {
		t.Fatalf("partial rule = (%d, %t), want (4, true)", unpriced, unpricedAttempt)
	}
}

func TestPricingTotalOnlyAndPartialBreakdownRemainUnpriced(t *testing.T) {
	table := compilePricing(config.UsagePricingConfig{Rules: []config.UsagePricingRule{{
		Provider: "*", Model: "*", ServiceTier: "*", InputPerMillion: "1", OutputPerMillion: "1",
	}}})
	tests := []struct {
		name    string
		detail  coreusage.Detail
		cost    int64
		unknown int64
	}{
		{name: "total only", detail: coreusage.Detail{TotalTokens: 100}, cost: 0, unknown: 100},
		{name: "partial breakdown", detail: coreusage.Detail{InputTokens: 40, OutputTokens: 10, TotalTokens: 75}, cost: 50, unknown: 25},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tokens := normalizeBillableTokens("openai", tt.detail)
			cost, unpriced, unpricedAttempt := table.calculate("openai", "gpt-5", "default", tokens)
			if cost != tt.cost || unpriced != tt.unknown || !unpricedAttempt {
				t.Fatalf("calculate() = (%d, %d, %t), want (%d, %d, true)", cost, unpriced, unpricedAttempt, tt.cost, tt.unknown)
			}
		})
	}
}

func TestPricingRevisionIncludesNormalizedRules(t *testing.T) {
	left := compilePricing(config.UsagePricingConfig{Version: "default", Rules: []config.UsagePricingRule{{
		Provider: "*", Model: "*", ServiceTier: "*", InputPerMillion: "1",
	}}})
	right := compilePricing(config.UsagePricingConfig{Version: "default", Rules: []config.UsagePricingRule{{
		Provider: "*", Model: "*", ServiceTier: "*", InputPerMillion: "2",
	}}})
	if left.version == right.version {
		t.Fatalf("pricing revisions did not change: %q", left.version)
	}
	if !strings.HasPrefix(left.version, "default@") || len(strings.TrimPrefix(left.version, "default@")) != 8 {
		t.Fatalf("pricing version = %q, want default@<sha8>", left.version)
	}
	equivalent := compilePricing(config.UsagePricingConfig{Version: "default", Currency: " usd ", Rules: []config.UsagePricingRule{{
		Provider: " * ", Model: "*", ServiceTier: "*", InputPerMillion: "1.00",
	}}})
	if left.version != equivalent.version {
		t.Fatalf("equivalent normalized rules produced %q and %q", left.version, equivalent.version)
	}
}
