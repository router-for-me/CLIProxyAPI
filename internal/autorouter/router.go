package autorouter

import "strings"

// Resolved is the outcome of resolving a classified tier to a concrete upstream
// target: the target model id plus its per-model routing (providers + strategy +
// priorities), mirroring a Model Route. The provider list may be empty, in which
// case the target model's default providers apply.
type Resolved struct {
	Model      string
	Providers  []string
	Strategy   string
	Priorities []ProviderPriority
}

// Resolve maps a classified tier to a concrete upstream target model for the
// given router config. When the chosen tier's mapping is unset (empty Model),
// it falls back to the next-lower tier, then the next, and finally the router's
// default (the first configured mapping). This mirrors the "fall back to a
// lower tier when the chosen tier is unavailable" behaviour.
//
// The returned Resolved carries the chosen mapping's full per-model routing
// (providers/strategy/priorities) so the caller can apply it at request time.
// The second return value reports whether any mapping resolved.
func Resolve(tier Tier, c *Config) (*Resolved, bool) {
	if c == nil {
		return nil, false
	}
	// Walk from the chosen tier downwards so an unmapped hard tier degrades to
	// the best available softer mapping.
	for _, t := range tierAndBelow(tier) {
		if m := c.mappingFor(t); m != nil && strings.TrimSpace(m.Model) != "" {
			return resolvedFromMapping(m), true
		}
	}
	// No deliberate mapping matched: fall back to the first non-empty mapping
	// (acts as the router default target).
	for i := range c.Mappings {
		if strings.TrimSpace(c.Mappings[i].Model) != "" {
			return resolvedFromMapping(&c.Mappings[i]), true
		}
	}
	return nil, false
}

// resolvedFromMapping copies a resolved mapping's model + routing into a
// Resolved value so callers never hold a reference into the config slice.
func resolvedFromMapping(m *TierMapping) *Resolved {
	if m == nil {
		return nil
	}
	r := &Resolved{
		Model:    strings.TrimSpace(m.Model),
		Strategy: strings.TrimSpace(m.Strategy),
	}
	if len(m.Providers) > 0 {
		r.Providers = append([]string(nil), m.Providers...)
	}
	if len(m.Priorities) > 0 {
		r.Priorities = make([]ProviderPriority, len(m.Priorities))
		copy(r.Priorities, m.Priorities)
	}
	return r
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
