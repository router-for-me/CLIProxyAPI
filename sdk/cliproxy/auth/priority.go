package auth

import (
	"encoding/json"
	"strconv"
	"strings"
)

const (
	PriorityFallback  = 1
	PriorityDefault   = 2
	PriorityPreferred = 3
)

// NormalizePriority clamps priority into [1,3] and treats 0 as the default (2).
func NormalizePriority(priority int) int {
	if priority == 0 {
		return PriorityDefault
	}
	if priority < PriorityFallback {
		return PriorityFallback
	}
	if priority > PriorityPreferred {
		return PriorityPreferred
	}
	return priority
}

// ParsePriority parses a string priority and returns a normalized value.
// Empty/invalid values are treated as the default priority (2).
func ParsePriority(raw string) int {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return PriorityDefault
	}
	parsed, err := strconv.Atoi(trimmed)
	if err != nil {
		return PriorityDefault
	}
	return NormalizePriority(parsed)
}

// ParsePriorityFromInterface parses priority values from decoded JSON objects.
// It returns the raw integer value and whether parsing succeeded.
//
// Callers should typically pass the result through NormalizePriority.
func ParsePriorityFromInterface(v any) (int, bool) {
	switch raw := v.(type) {
	case float64:
		return int(raw), true
	case int:
		return raw, true
	case int64:
		return int(raw), true
	case json.Number:
		if n, err := raw.Int64(); err == nil {
			return int(n), true
		}
		return 0, false
	case string:
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			return 0, false
		}
		n, err := strconv.Atoi(trimmed)
		if err != nil {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}
