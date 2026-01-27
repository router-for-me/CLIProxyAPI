package auth

import (
	"strings"

	appconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

const (
	RoutingStrategyRoundRobin      = "round-robin"
	RoutingStrategyFillFirst       = "fill-first"
	RoutingStrategyProviderFirst   = "provider-first"
	RoutingStrategyCredentialFirst = "credential-first"
	RoutingStrategyRandom          = "random"
)

// NormalizeRoutingStrategy normalizes user supplied routing strategy strings.
// It preserves the "random" value (which aliases to round-robin behavior).
func NormalizeRoutingStrategy(strategy string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(strategy))
	switch normalized {
	case "", "round-robin", "roundrobin", "rr":
		return RoutingStrategyRoundRobin, true
	case "fill-first", "fillfirst", "ff":
		return RoutingStrategyFillFirst, true
	case "provider-first", "providerfirst", "pf":
		return RoutingStrategyProviderFirst, true
	case "credential-first", "credentialfirst", "cf":
		return RoutingStrategyCredentialFirst, true
	case "random":
		return RoutingStrategyRandom, true
	default:
		return "", false
	}
}

func normalizeSelectorStrategy(strategy string) string {
	normalized, ok := NormalizeRoutingStrategy(strategy)
	if !ok {
		return RoutingStrategyRoundRobin
	}
	if normalized == RoutingStrategyRandom {
		return RoutingStrategyRoundRobin
	}
	return normalized
}

// NormalizeRoutingPreference normalizes a routing preference string.
// Supported values: "provider-first", "credential-first".
func NormalizeRoutingPreference(preference string) (string, bool) {
	normalized, ok := appconfig.NormalizeRoutingPreference(preference)
	if !ok {
		return "", false
	}
	switch normalized {
	case "provider-first":
		return RoutingStrategyProviderFirst, true
	case "credential-first":
		return RoutingStrategyCredentialFirst, true
	default:
		return "", false
	}
}

// NormalizeSameLevelRoutingStrategy normalizes the routing.strategy value used within the chosen group.
// Supported values: "round-robin", "fill-first". It also accepts "random" as an alias for round-robin.
func NormalizeSameLevelRoutingStrategy(strategy string) (string, bool) {
	normalized, ok := appconfig.NormalizeRoutingSameLevelStrategy(strategy)
	if !ok {
		return "", false
	}
	switch normalized {
	case "round-robin":
		return RoutingStrategyRoundRobin, true
	case "fill-first":
		return RoutingStrategyFillFirst, true
	default:
		return "", false
	}
}

func normalizeSameLevelStrategy(strategy string) string {
	normalized, ok := NormalizeSameLevelRoutingStrategy(strategy)
	if !ok {
		return RoutingStrategyRoundRobin
	}
	return normalized
}

// NormalizeEffectiveSelectorKey returns a stable key representing the effective selector behavior.
// - When preference is set, it includes both preference and same-level strategy.
// - Otherwise, it falls back to legacy strategy normalization.
func NormalizeEffectiveSelectorKey(preference, strategy string) string {
	if pref, ok := NormalizeRoutingPreference(preference); ok {
		return pref + ":" + normalizeSameLevelStrategy(strategy)
	}
	return normalizeSelectorStrategy(strategy)
}

// NormalizeSelectorStrategy returns the effective selector strategy used internally.
// It maps "random" to "round-robin".
func NormalizeSelectorStrategy(strategy string) string {
	return normalizeSelectorStrategy(strategy)
}

// NewSelector returns a selector implementation for the configured routing strategy.
// Unknown strategies default to round-robin.
func NewSelector(strategy string) Selector {
	switch normalizeSelectorStrategy(strategy) {
	case RoutingStrategyFillFirst:
		return &FillFirstSelector{}
	case RoutingStrategyProviderFirst:
		return &ProviderFirstSelector{}
	case RoutingStrategyCredentialFirst:
		return &CredentialFirstSelector{}
	default:
		return &RoundRobinSelector{}
	}
}

// NewSelectorWithRouting creates a selector using a split routing configuration:
// - preference controls which group is tried first in mixed routing (provider-first / credential-first)
// - strategy controls selection within the chosen group (round-robin / fill-first)
//
// If preference is empty/invalid, it falls back to legacy NewSelector(strategy).
func NewSelectorWithRouting(preference, strategy string) Selector {
	if pref, ok := NormalizeRoutingPreference(preference); ok {
		return &PreferenceSelector{
			preference: pref,
			strategy:   normalizeSameLevelStrategy(strategy),
		}
	}
	return NewSelector(strategy)
}
