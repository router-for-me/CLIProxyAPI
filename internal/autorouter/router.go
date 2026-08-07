package autorouter

import (
	"math/rand/v2"
	"strings"
)

// Resolved is the outcome of resolving a classified tier to a concrete upstream
// target: the picked target model id plus its per-model routing (providers +
// strategy + priorities), mirroring a Model Route. The provider list may be
// empty, in which case the target model's default providers apply.
type Resolved struct {
	Model      string
	Providers  []string
	Strategy   string
	Priorities []ProviderPriority
	// TargetStrategy is the tier's target-selection strategy
	// (""/"weighted"/"priority"), kept for observability.
	TargetStrategy string
}

// Resolve maps a classified tier to a concrete upstream target model for the
// given router config. When the chosen tier's mapping is unset (no single Model
// and no Targets), it falls back to the next-lower tier, then the next, and
// finally the router's default (the first configured mapping). This mirrors the
// "fall back to a lower tier when the chosen tier is unavailable" behaviour.
//
// A tier mapping with multiple Targets selects one candidate per its
// TargetStrategy (weighted random by default) so request traffic is spread
// across the configured upstream models. The returned Resolved carries the
// picked model plus the chosen mapping's full per-model routing
// (providers/strategy/priorities) so the caller can apply it at request time.
// The second return value reports whether any mapping resolved.
func Resolve(tier Tier, c *Config) (*Resolved, bool) {
	return resolveWithRand(tier, c, nil)
}

// resolveWithRand is Resolve with an injectable random source (nil = the shared
// concurrency-safe source). Kept internal so tests can seed a deterministic
// sequence.
func resolveWithRand(tier Tier, c *Config, rng *rand.Rand) (*Resolved, bool) {
	if c == nil {
		return nil, false
	}
	// Walk from the chosen tier downwards so an unmapped hard tier degrades to
	// the best available softer mapping.
	for _, t := range tierAndBelow(tier) {
		if m := c.mappingFor(t); m != nil {
			if targets := mappingTargets(m); len(targets) > 0 {
				return resolvedFromMapping(m, targets, rng), true
			}
		}
	}
	// No deliberate mapping matched: fall back to the first resolvable mapping
	// (acts as the router default target).
	for i := range c.Mappings {
		if targets := mappingTargets(&c.Mappings[i]); len(targets) > 0 {
			return resolvedFromMapping(&c.Mappings[i], targets, rng), true
		}
	}
	return nil, false
}

// PickTargetModel selects one upstream model from a tier mapping's candidates
// (Targets when present, else the single Model) using the mapping's
// TargetStrategy. rng may be nil to use the shared concurrency-safe random
// source. Returns ok=false when the mapping has no usable target.
func PickTargetModel(m *TierMapping, rng *rand.Rand) (string, bool) {
	if m == nil {
		return "", false
	}
	targets := mappingTargets(m)
	if len(targets) == 0 {
		return "", false
	}
	idx := pickTargetIndex(targets, strings.ToLower(strings.TrimSpace(m.TargetStrategy)), rng)
	if idx < 0 {
		return "", false
	}
	return targets[idx].Model, true
}

// mappingTargets flattens a mapping into its selection candidates: the Targets
// list when non-empty (skipping blank entries), otherwise the single Model as a
// weight-1 candidate. Returns nil when the mapping has no usable target.
func mappingTargets(m *TierMapping) []TierTarget {
	if m == nil {
		return nil
	}
	if len(m.Targets) > 0 {
		out := make([]TierTarget, 0, len(m.Targets))
		for _, t := range m.Targets {
			if model := strings.TrimSpace(t.Model); model != "" {
				out = append(out, TierTarget{
					Model:      model,
					Weight:     t.Weight,
					Providers:  t.Providers,
					Strategy:   t.Strategy,
					Priorities: t.Priorities,
				})
			}
		}
		return out
	}
	if model := strings.TrimSpace(m.Model); model != "" {
		return []TierTarget{{Model: model, Weight: 1}}
	}
	return nil
}

// pickTargetIndex returns the index of the selected candidate among candidates
// per the given strategy:
//   - "priority": the highest weight wins; ties fall back to list order.
//   - anything else ("" or "weighted"): weighted random, weights <= 0 default
//     to 1 so an all-blank list picks uniformly.
//
// Returns -1 when the list is empty.
func pickTargetIndex(candidates []TierTarget, strategy string, rng *rand.Rand) int {
	if len(candidates) == 0 {
		return -1
	}
	if len(candidates) == 1 {
		return 0
	}
	if strategy == "priority" {
		best := 0
		for i := 1; i < len(candidates); i++ {
			if effectiveTargetWeight(candidates[i].Weight) > effectiveTargetWeight(candidates[best].Weight) {
				best = i
			}
		}
		return best
	}
	// Weighted random (default). Normalize non-positive weights to 1 so a
	// partially-blank list stays balanced instead of dropping candidates.
	total := 0
	for _, c := range candidates {
		total += effectiveTargetWeight(c.Weight)
	}
	roll := randFloat(rng) * float64(total)
	cumulative := 0.0
	for i, c := range candidates {
		cumulative += float64(effectiveTargetWeight(c.Weight))
		if roll < cumulative {
			return i
		}
	}
	return len(candidates) - 1
}

// effectiveTargetWeight maps a TierTarget weight into the selection weighting:
// non-positive (missing/zero/negative) values count as 1.
func effectiveTargetWeight(w int) int {
	if w <= 0 {
		return 1
	}
	return w
}

// randFloat returns a float64 in [0,1): from rng when provided, else from the
// shared concurrency-safe source.
func randFloat(rng *rand.Rand) float64 {
	if rng != nil {
		return rng.Float64()
	}
	return rand.Float64()
}

// resolvedFromMapping copies a resolved mapping's picked model + routing into a
// Resolved value so callers never hold a reference into the config slice.
//
// Routing precedence: when the picked target configures its own per-model
// routing (any of providers/strategy/priorities), that routing wins; otherwise
// the mapping's tier-level routing applies as the fallback.
func resolvedFromMapping(m *TierMapping, targets []TierTarget, rng *rand.Rand) *Resolved {
	if m == nil || len(targets) == 0 {
		return nil
	}
	idx := pickTargetIndex(targets, strings.ToLower(strings.TrimSpace(m.TargetStrategy)), rng)
	if idx < 0 {
		return nil
	}
	picked := targets[idx]
	providers := m.Providers
	strategy := strings.TrimSpace(m.Strategy)
	priorities := m.Priorities
	if hasTargetRouting(picked) {
		providers = picked.Providers
		strategy = strings.TrimSpace(picked.Strategy)
		priorities = picked.Priorities
	}
	r := &Resolved{
		Model:          picked.Model,
		Strategy:       strategy,
		TargetStrategy: strings.TrimSpace(m.TargetStrategy),
	}
	if len(providers) > 0 {
		r.Providers = append([]string(nil), providers...)
	}
	if len(priorities) > 0 {
		r.Priorities = make([]ProviderPriority, len(priorities))
		copy(r.Priorities, priorities)
	}
	return r
}

// hasTargetRouting reports whether a target configures its own per-model
// routing (any of providers/strategy/priorities). When false the target
// inherits the tier's routing.
func hasTargetRouting(t TierTarget) bool {
	return len(t.Providers) > 0 || strings.TrimSpace(t.Strategy) != "" || len(t.Priorities) > 0
}

// tierAndBelow returns the tier plus every lower tier, ordered from the given
// tier down to SIMPLE. Used to choose a fallback when a tier is unmapped.
func tierAndBelow(tier Tier) []Tier {
	start := 0
	for i, t := range TierOrder {
		if t == tier {
			start = i
			break
		}
	}
	out := make([]Tier, 0, start+1)
	for i := start; i >= 0; i-- {
		out = append(out, TierOrder[i])
	}
	return out
}
