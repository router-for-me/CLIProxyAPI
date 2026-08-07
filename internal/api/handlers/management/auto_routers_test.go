package management

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// TestValidateTierMappingsSingleModel keeps the legacy single-model validation
// behaviour: a mapped tier requires a non-empty model, and priorities must
// reference a pinned provider.
func TestValidateTierMappingsSingleModel(t *testing.T) {
	if msg := validateTierMappings([]store.TierMapping{
		{Tier: "simple", Model: "gpt-4o-mini"},
	}); msg != "" {
		t.Fatalf("valid single-model mapping rejected: %s", msg)
	}
	if msg := validateTierMappings([]store.TierMapping{
		{Tier: "complex", Model: ""},
	}); msg == "" {
		t.Fatal("expected empty-model mapping to be rejected")
	}
	if msg := validateTierMappings([]store.TierMapping{
		{Tier: "complex", Model: "claude-sonnet-4-5", Priorities: []store.ProviderPriority{{Provider: "openai", Priority: 5}}},
	}); msg == "" {
		t.Fatal("expected priority referencing an unpinned provider to be rejected")
	}
}

// TestValidateTierMappingsMultiTarget verifies the multi-target form: a tier
// with a non-empty Targets list resolves without a Model, every target must
// carry a model, and the target strategy must be supported.
func TestValidateTierMappingsMultiTarget(t *testing.T) {
	if msg := validateTierMappings([]store.TierMapping{
		{Tier: "complex", Targets: []store.TierTarget{{Model: "claude-sonnet-4-5", Weight: 7}, {Model: "gpt-4o", Weight: 3}}, TargetStrategy: "weighted"},
	}); msg != "" {
		t.Fatalf("valid multi-target mapping rejected: %s", msg)
	}
	// Default strategy (empty) is allowed.
	if msg := validateTierMappings([]store.TierMapping{
		{Tier: "complex", Targets: []store.TierTarget{{Model: "claude-sonnet-4-5"}}},
	}); msg != "" {
		t.Fatalf("default strategy rejected: %s", msg)
	}
	// A target with a blank model is rejected.
	if msg := validateTierMappings([]store.TierMapping{
		{Tier: "complex", Targets: []store.TierTarget{{Model: ""}}},
	}); msg == "" {
		t.Fatal("expected blank target model to be rejected")
	}
	// A tier with neither Model nor Targets is rejected.
	if msg := validateTierMappings([]store.TierMapping{
		{Tier: "simple"},
	}); msg == "" {
		t.Fatal("expected tier without any target to be rejected")
	}
	// Unknown target strategies are rejected.
	for _, s := range []string{"round-robin", "random", "bogus"} {
		if msg := validateTierMappings([]store.TierMapping{
			{Tier: "simple", Targets: []store.TierTarget{{Model: "gpt-4o-mini"}}, TargetStrategy: s},
		}); msg == "" {
			t.Fatalf("expected target strategy %q to be rejected", s)
		}
	}
}

// TestValidateTierMappingsTargetRouting verifies per-target routing rules: the
// target strategy must be a supported value, and a target priority must
// reference one of the target's pinned providers.
func TestValidateTierMappingsTargetRouting(t *testing.T) {
	// Valid per-target routing (providers + strategy + priorities).
	if msg := validateTierMappings([]store.TierMapping{
		{Tier: "complex", Targets: []store.TierTarget{{
			Model:      "claude-sonnet-4-5",
			Providers:  []string{"anthropic", "openai"},
			Strategy:   "failover",
			Priorities: []store.ProviderPriority{{Provider: "anthropic", Priority: 10}},
		}}},
	}); msg != "" {
		t.Fatalf("valid per-target routing rejected: %s", msg)
	}
	// Unknown per-target strategy is rejected.
	if msg := validateTierMappings([]store.TierMapping{
		{Tier: "complex", Targets: []store.TierTarget{{Model: "claude-sonnet-4-5", Strategy: "round-robin"}}},
	}); msg == "" {
		t.Fatal("expected unknown per-target strategy to be rejected")
	}
	// A target priority referencing an unpinned provider is rejected.
	if msg := validateTierMappings([]store.TierMapping{
		{Tier: "complex", Targets: []store.TierTarget{{
			Model:      "claude-sonnet-4-5",
			Providers:  []string{"anthropic"},
			Priorities: []store.ProviderPriority{{Provider: "openai", Priority: 5}},
		}}},
	}); msg == "" {
		t.Fatal("expected per-target priority on an unpinned provider to be rejected")
	}
}
