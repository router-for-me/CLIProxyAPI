package config

import (
	"fmt"
	"strings"
)

// Canonical pool routing strategy values for entry-bearing providers
// (claude-api-key, openai-compatibility). Empty means unset: entries follow
// the global routing.strategy and request-fault errors keep the historical
// hard-stop rotation behavior. "priority"/"failover" are accepted as Model
// Routes-compatible aliases at input boundaries (the management API request,
// canonicalized in the DTO; and the renderer's store read) so downstream
// layers see one spelling.
//
// power-of-two-choices and least-used (design G4) select by in-flight request
// counts in the scheduler; until that lands, as a global routing strategy
// they map to the fill-first-style deterministic pick (interim), and as
// pool-row values they still stamp pool_strategy (aggressive in-pool
// failover) while the picks themselves follow the global strategy.
const (
	PoolStrategyRoundRobin         = "round-robin"
	PoolStrategyWeightedRoundRobin = "weighted-round-robin"
	PoolStrategyFillFirst          = "fill-first"
	PoolStrategyPowerOfTwoChoices  = "power-of-two-choices"
	PoolStrategyLeastUsed          = "least-used"
)

// NormalizePoolRoutingStrategy canonicalizes an operator-supplied pool
// strategy. Unknown or blank values return "" (unset).
// Keep the alias sets in sync with the other two normalize sites:
// sdk/cliproxy/service_config.go (normalizedRoutingRuntimeState) and
// internal/api/handlers/management/config_basic.go (normalizeRoutingStrategy).
func NormalizePoolRoutingStrategy(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "round-robin", "roundrobin", "rr", "failover":
		return PoolStrategyRoundRobin
	case "weighted-round-robin", "weightedroundrobin", "wrr":
		return PoolStrategyWeightedRoundRobin
	case "fill-first", "fillfirst", "ff", "priority":
		return PoolStrategyFillFirst
	case "power-of-two-choices", "poweroftwochoices", "p2c", "two-random-choices":
		return PoolStrategyPowerOfTwoChoices
	case "least-used", "leastused", "least-busy":
		return PoolStrategyLeastUsed
	default:
		return ""
	}
}

// ValidatePoolRoutingStrategy accepts only canonical values (plus empty).
// Raw aliases must be rejected at strict input boundaries so operators see
// their typo; call NormalizePoolRoutingStrategy first when aliases are
// intended.
func ValidatePoolRoutingStrategy(s string) error {
	switch s {
	case "", PoolStrategyRoundRobin, PoolStrategyWeightedRoundRobin, PoolStrategyFillFirst, PoolStrategyPowerOfTwoChoices, PoolStrategyLeastUsed:
		return nil
	default:
		return fmt.Errorf("invalid routing strategy %q: want one of round-robin, weighted-round-robin, fill-first, power-of-two-choices, least-used", s)
	}
}

// Canonical global routing strategy values for the top-level selector that
// decides which auth to route an incoming request to when no per-pool rule
// applies. Distinct namespace from PoolStrategy*: the pool-row "fill-first"
// keeps deterministic-by-priority semantics on pool rows, while the global
// "fill-first" extends it with an in-flight-highest-below-cap rule.
const (
	GlobalStrategyRoundRobin = "round-robin"
	GlobalStrategyFillFirst  = "fill-first"
	GlobalStrategyWeighted   = "weighted"
	GlobalStrategyHeadroom   = "headroom"
)

// NormalizeGlobalRoutingStrategy canonicalizes an operator-supplied global
// routing strategy. Unknown or blank values return "" (unset).
func NormalizeGlobalRoutingStrategy(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "round-robin", "roundrobin", "rr":
		return GlobalStrategyRoundRobin
	case "fill-first", "fillfirst", "ff":
		return GlobalStrategyFillFirst
	case "weighted", "w":
		return GlobalStrategyWeighted
	case "headroom", "hr":
		return GlobalStrategyHeadroom
	default:
		return ""
	}
}

// ValidateGlobalRoutingStrategy accepts only canonical values (plus empty).
// Raw aliases must be rejected at strict input boundaries so operators see
// their typo; call NormalizeGlobalRoutingStrategy first when aliases are
// intended.
func ValidateGlobalRoutingStrategy(s string) error {
	switch s {
	case "", GlobalStrategyRoundRobin, GlobalStrategyFillFirst, GlobalStrategyWeighted, GlobalStrategyHeadroom:
		return nil
	default:
		return fmt.Errorf("invalid global routing strategy %q: want one of round-robin, fill-first, weighted, headroom", s)
	}
}
