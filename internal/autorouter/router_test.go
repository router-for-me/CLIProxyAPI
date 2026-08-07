package autorouter

import (
	"math/rand/v2"
	"testing"
)

// seededRand returns a deterministic rand.Rand for reproducible pick tests.
func seededRand() *rand.Rand {
	return rand.New(rand.NewPCG(1, 2))
}

// TestPickTargetSingleModel verifies the legacy single-Model form always picks
// that model regardless of strategy.
func TestPickTargetSingleModel(t *testing.T) {
	m := &TierMapping{Model: "gpt-4o-mini"}
	for _, strategy := range []string{"", "weighted", "priority"} {
		m.TargetStrategy = strategy
		got, ok := PickTargetModel(m, seededRand())
		if !ok || got != "gpt-4o-mini" {
			t.Fatalf("strategy %q: PickTargetModel = %q, %v; want gpt-4o-mini, true", strategy, got, ok)
		}
	}
}

// TestPickTargetUnmapped verifies an empty mapping (no Model, no Targets) never
// resolves.
func TestPickTargetUnmapped(t *testing.T) {
	if got, ok := PickTargetModel(&TierMapping{}, nil); ok {
		t.Fatalf("expected no pick for empty mapping, got %q", got)
	}
}

// TestPickTargetPriority verifies the priority strategy deterministically picks
// the highest-weight target, ties falling back to list order.
func TestPickTargetPriority(t *testing.T) {
	m := &TierMapping{
		TargetStrategy: "priority",
		Targets: []TierTarget{
			{Model: "gpt-4o", Weight: 30},
			{Model: "claude-sonnet-4-5", Weight: 70},
			{Model: "gpt-4o-mini", Weight: 70},
		},
	}
	for i := 0; i < 50; i++ {
		got, ok := PickTargetModel(m, seededRand())
		if !ok || got != "claude-sonnet-4-5" {
			t.Fatalf("pick %d = %q, %v; want claude-sonnet-4-5 (first of the tied highest)", i, got, ok)
		}
	}
}

// TestPickTargetWeighted verifies weighted selection only ever returns one of
// the configured targets and that a lopsided weighting picks the heavy target
// the large majority of the time.
func TestPickTargetWeighted(t *testing.T) {
	m := &TierMapping{
		Targets: []TierTarget{
			{Model: "gpt-4o-mini", Weight: 1},
			{Model: "claude-sonnet-4-5", Weight: 49},
		},
	}
	heavy := 0
	const trials = 2000
	rng := seededRand()
	for i := 0; i < trials; i++ {
		got, ok := PickTargetModel(m, rng)
		if !ok {
			t.Fatal("expected a pick")
		}
		switch got {
		case "gpt-4o-mini", "claude-sonnet-4-5":
		default:
			t.Fatalf("picked unknown target %q", got)
		}
		if got == "claude-sonnet-4-5" {
			heavy++
		}
	}
	// 49/50 expected; a seeded run must stay well clear of the 50% mark.
	if heavy < trials*90/100 {
		t.Fatalf("weighted pick drifted too far from 98%%: heavy=%d/%d", heavy, trials)
	}
}

// TestPickTargetWeightedBlankWeights verifies blank/zero weights degrade to an
// even split instead of dropping candidates.
func TestPickTargetWeightedBlankWeights(t *testing.T) {
	m := &TierMapping{
		Targets: []TierTarget{
			{Model: "a"},
			{Model: "b"},
			{Model: "c", Weight: 0},
		},
	}
	seen := map[string]int{}
	rng := seededRand()
	for i := 0; i < 600; i++ {
		got, ok := PickTargetModel(m, rng)
		if !ok {
			t.Fatal("expected a pick")
		}
		seen[got]++
	}
	for _, model := range []string{"a", "b", "c"} {
		if seen[model] == 0 {
			t.Fatalf("candidate %q never picked with blank weights: %v", model, seen)
		}
	}
}

// TestResolveMultiTargetFallback verifies a tier with multiple weighted targets
// resolves to one of them, and that an unmapped higher tier still falls back to
// a lower multi-target tier.
func TestResolveMultiTargetFallback(t *testing.T) {
	c := &Config{
		Mappings: []TierMapping{
			{Tier: TierSimple, Model: "gpt-4o-mini"},
			{
				Tier: TierComplex,
				Targets: []TierTarget{
					{Model: "claude-sonnet-4-5", Weight: 7},
					{Model: "gpt-4o", Weight: 3},
				},
			},
		},
	}
	// REASONING unmapped -> falls back to COMPLEX and picks one of its targets.
	seen := map[string]int{}
	rng := seededRand()
	for i := 0; i < 200; i++ {
		resolved, ok := resolveWithRand(TierReasoning, c, rng)
		if !ok || resolved == nil {
			t.Fatalf("expected fallback resolution, ok=%v", ok)
		}
		if resolved.Model != "claude-sonnet-4-5" && resolved.Model != "gpt-4o" {
			t.Fatalf("resolved to unexpected model %q", resolved.Model)
		}
		seen[resolved.Model]++
	}
	if seen["claude-sonnet-4-5"] == 0 || seen["gpt-4o"] == 0 {
		t.Fatalf("both targets should appear across picks, got %v", seen)
	}
	// COMPLEX resolves to itself (same candidates).
	resolved, ok := resolveWithRand(TierComplex, c, seededRand())
	if !ok || resolved == nil || resolved.Model != "claude-sonnet-4-5" && resolved.Model != "gpt-4o" {
		t.Fatalf("expected COMPLEX resolution to one of its targets, got %+v ok=%v", resolved, ok)
	}
}

// TestResolveCarriesMultiTargetRouting verifies a multi-target tier's routing
// (providers/strategy/priorities) rides along with the picked model.
func TestResolveCarriesMultiTargetRouting(t *testing.T) {
	c := &Config{
		Mappings: []TierMapping{
			{
				Tier: TierMedium,
				Targets: []TierTarget{
					{Model: "claude-sonnet-4-5", Weight: 1},
					{Model: "gpt-4o", Weight: 1},
				},
				TargetStrategy: "weighted",
				Providers:      []string{"anthropic", "openai"},
				Strategy:       "priority",
				Priorities: []ProviderPriority{
					{Provider: "anthropic", Priority: 10},
					{Provider: "openai", Priority: 5},
				},
			},
		},
	}
	resolved, ok := resolveWithRand(TierMedium, c, seededRand())
	if !ok || resolved == nil {
		t.Fatal("expected resolution")
	}
	if len(resolved.Providers) != 2 || resolved.Strategy != "priority" || len(resolved.Priorities) != 2 {
		t.Fatalf("routing not carried through, got %+v", resolved)
	}
	if resolved.TargetStrategy != "weighted" {
		t.Fatalf("TargetStrategy not carried through, got %q", resolved.TargetStrategy)
	}
}

// TestResolveTargetsTakePrecedenceOverModel verifies that when both Model and
// Targets are present, selection uses Targets (never the Model value).
func TestResolveTargetsTakePrecedenceOverModel(t *testing.T) {
	c := &Config{
		Mappings: []TierMapping{
			{
				Tier:  TierSimple,
				Model: "gpt-4o-mini",
				Targets: []TierTarget{
					{Model: "claude-haiku"},
				},
			},
		},
	}
	resolved, ok := resolveWithRand(TierSimple, c, seededRand())
	if !ok || resolved == nil || resolved.Model != "claude-haiku" {
		t.Fatalf("expected Targets to win over Model, got %+v ok=%v", resolved, ok)
	}
}

// TestResolveTargetRoutingWins verifies that a picked target's own per-model
// routing (providers/strategy/priorities) overrides the tier-level routing.
func TestResolveTargetRoutingWins(t *testing.T) {
	c := &Config{
		Mappings: []TierMapping{
			{
				Tier: TierComplex,
				Targets: []TierTarget{
					{
						Model:     "claude-sonnet-4-5",
						Weight:    1,
						Providers: []string{"anthropic"},
						Strategy:  "failover",
						Priorities: []ProviderPriority{
							{Provider: "anthropic", Priority: 10},
						},
					},
					{Model: "gpt-4o", Weight: 1},
				},
				// Tier-level default routing that the picked target overrides.
				Providers: []string{"anthropic", "openai"},
				Strategy:  "priority",
				Priorities: []ProviderPriority{
					{Provider: "anthropic", Priority: 10},
					{Provider: "openai", Priority: 5},
				},
			},
		},
	}
	// Force the seeded run to land on the first target (weighted tie, first
	// roll picks index 0 with the PCG seed used by seededRand).
	rng := seededRand()
	seen := map[string]int{}
	for i := 0; i < 20; i++ {
		resolved, ok := resolveWithRand(TierComplex, c, rng)
		if !ok || resolved == nil {
			t.Fatalf("expected resolution, ok=%v", ok)
		}
		seen[resolved.Model]++
		if resolved.Model == "claude-sonnet-4-5" {
			if resolved.Strategy != "failover" {
				t.Fatalf("target routing strategy not applied: got %q, want failover", resolved.Strategy)
			}
			if len(resolved.Providers) != 1 || resolved.Providers[0] != "anthropic" {
				t.Fatalf("target routing providers not applied: got %v", resolved.Providers)
			}
			if len(resolved.Priorities) != 1 || resolved.Priorities[0].Provider != "anthropic" {
				t.Fatalf("target routing priorities not applied: got %+v", resolved.Priorities)
			}
		}
	}
	if seen["claude-sonnet-4-5"] == 0 {
		t.Fatalf("seeded run never picked the first target: %v", seen)
	}
}

// TestResolveTargetRoutingInheritsTier verifies that a picked target with no
// per-target routing inherits the tier-level routing unchanged.
func TestResolveTargetRoutingInheritsTier(t *testing.T) {
	c := &Config{
		Mappings: []TierMapping{
			{
				Tier: TierComplex,
				Targets: []TierTarget{
					{Model: "claude-sonnet-4-5", Weight: 1},
					{Model: "gpt-4o", Weight: 1},
				},
				TargetStrategy: "priority",
				Providers:      []string{"anthropic", "openai"},
				Strategy:       "failover",
				Priorities: []ProviderPriority{
					{Provider: "anthropic", Priority: 10},
					{Provider: "openai", Priority: 5},
				},
			},
		},
	}
	// Priority strategy always picks the first (tied highest) target.
	resolved, ok := resolveWithRand(TierComplex, c, seededRand())
	if !ok || resolved == nil {
		t.Fatal("expected resolution")
	}
	if resolved.Model != "claude-sonnet-4-5" {
		t.Fatalf("priority strategy should pick the first tied target, got %q", resolved.Model)
	}
	if resolved.Strategy != "failover" || len(resolved.Providers) != 2 || len(resolved.Priorities) != 2 {
		t.Fatalf("tier routing not inherited, got %+v", resolved)
	}
}
