package auth

import (
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
