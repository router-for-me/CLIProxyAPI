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
const (
	PoolStrategyRoundRobin         = "round-robin"
	PoolStrategyWeightedRoundRobin = "weighted-round-robin"
	PoolStrategyFillFirst          = "fill-first"
)

// NormalizePoolRoutingStrategy canonicalizes an operator-supplied pool
// strategy. Unknown or blank values return "" (unset).
func NormalizePoolRoutingStrategy(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "round-robin", "roundrobin", "rr", "failover":
		return PoolStrategyRoundRobin
	case "weighted-round-robin", "weightedroundrobin", "wrr":
		return PoolStrategyWeightedRoundRobin
	case "fill-first", "fillfirst", "ff", "priority":
		return PoolStrategyFillFirst
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
	case "", PoolStrategyRoundRobin, PoolStrategyWeightedRoundRobin, PoolStrategyFillFirst:
		return nil
	default:
		return fmt.Errorf("invalid routing strategy %q: want one of round-robin, weighted-round-robin, fill-first", s)
	}
}
