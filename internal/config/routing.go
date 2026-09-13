package config

import "strings"

// NormalizeRoutingStrategy returns the canonical routing strategy name.
func NormalizeRoutingStrategy(strategy string) string {
	switch strings.ToLower(strings.TrimSpace(strategy)) {
	case "weighted-round-robin", "weightedroundrobin", "wrr":
		return "weighted-round-robin"
	case "fill-first", "fillfirst", "ff":
		return "fill-first"
	default:
		return "round-robin"
	}
}
