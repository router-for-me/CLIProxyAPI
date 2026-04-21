package auth

import "strings"

const (
	// RoutingStrategyRoundRobin rotates across ready credentials.
	RoutingStrategyRoundRobin = "round-robin"
	// RoutingStrategyFillFirst burns the first ready credential before moving on.
	RoutingStrategyFillFirst = "fill-first"
	// RoutingStrategySequentialFill sticks to the current credential until it becomes unavailable.
	RoutingStrategySequentialFill = "sequential-fill"
)

// NormalizeRoutingStrategy canonicalizes supported routing strategy names and aliases.
func NormalizeRoutingStrategy(strategy string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(strategy)) {
	case "", RoutingStrategyRoundRobin, "roundrobin", "rr":
		return RoutingStrategyRoundRobin, true
	case RoutingStrategyFillFirst, "fillfirst", "ff":
		return RoutingStrategyFillFirst, true
	case RoutingStrategySequentialFill, "sequentialfill", "sf":
		return RoutingStrategySequentialFill, true
	default:
		return "", false
	}
}

// SelectorForRoutingStrategy returns the built-in selector for the supplied strategy.
// Unknown values fall back to round-robin so startup and reload behavior stay safe.
func SelectorForRoutingStrategy(strategy string) Selector {
	normalized, ok := NormalizeRoutingStrategy(strategy)
	if !ok {
		normalized = RoutingStrategyRoundRobin
	}
	switch normalized {
	case RoutingStrategyFillFirst:
		return &FillFirstSelector{}
	case RoutingStrategySequentialFill:
		return &SequentialFillSelector{}
	default:
		return &RoundRobinSelector{}
	}
}

func normalizeRoutingGroupKey(group string) string {
	return strings.ToLower(strings.TrimSpace(group))
}

// NormalizeRoutingGroupStrategies canonicalizes routing group strategy overrides.
// Empty group names and unsupported strategies are discarded.
func NormalizeRoutingGroupStrategies(overrides map[string]string) map[string]string {
	if len(overrides) == 0 {
		return nil
	}
	out := make(map[string]string, len(overrides))
	for group, strategy := range overrides {
		normalizedGroup := normalizeRoutingGroupKey(group)
		if normalizedGroup == "" {
			continue
		}
		normalizedStrategy, ok := NormalizeRoutingStrategy(strategy)
		if !ok {
			continue
		}
		out[normalizedGroup] = normalizedStrategy
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
