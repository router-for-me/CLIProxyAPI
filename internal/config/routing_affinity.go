package config

import (
	"fmt"
	"strings"
)

// NormalizedSessionAffinityOverrides validates provider policy values without
// modifying a configuration snapshot held by another runtime component.
func (routing RoutingConfig) NormalizedSessionAffinityOverrides() (map[string]SessionAffinityOverride, error) {
	if len(routing.SessionAffinityOverrides) == 0 {
		return nil, nil
	}
	result := make(map[string]SessionAffinityOverride, len(routing.SessionAffinityOverrides))
	for provider, policy := range routing.SessionAffinityOverrides {
		key := strings.ToLower(strings.TrimSpace(provider))
		if key == "anthropic" {
			key = "claude"
		}
		if key == "" || key == "mixed" {
			return nil, fmt.Errorf("session-affinity-overrides requires an individual provider name")
		}
		if _, duplicate := result[key]; duplicate {
			return nil, fmt.Errorf("duplicate session-affinity override for provider %s", key)
		}
		switch strings.ToLower(strings.TrimSpace(policy.Strategy)) {
		case "":
			policy.Strategy = ""
		case "round-robin", "roundrobin", "rr":
			policy.Strategy = "round-robin"
		case "fill-first", "fillfirst", "ff":
			policy.Strategy = "fill-first"
		case "weighted-round-robin", "weightedroundrobin", "wrr":
			policy.Strategy = "weighted-round-robin"
		default:
			return nil, fmt.Errorf("invalid session-affinity override strategy for provider %s", key)
		}
		switch strings.ToLower(strings.TrimSpace(policy.Precedence)) {
		case "", "session":
			policy.Precedence = "session"
		case "priority":
			policy.Precedence = "priority"
		default:
			return nil, fmt.Errorf("invalid session-affinity override precedence for provider %s", key)
		}
		result[key] = policy
	}
	return result, nil
}
