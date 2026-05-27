// Package common provides shared validation and sanitization utilities
// for tool calls across all translator implementations.
package common

import (
	"strconv"
	"strings"
)

// ToolNameShortener provides configurable tool name shortening with uniqueness guarantees.
type ToolNameShortener struct {
	// MaxLen is the maximum allowed tool name length (default: 64).
	MaxLen int
	// CustomMappings allows explicit name overrides before automatic shortening.
	// Key: original name, Value: desired shortened name.
	CustomMappings map[string]string
	// PreservePrefixes defines prefixes to preserve during shortening.
	// For MCP-style names, "mcp__" is preserved by default.
	PreservePrefixes []string
}

// DefaultToolNameShortener creates a shortener with sensible defaults.
func DefaultToolNameShortener() *ToolNameShortener {
	return &ToolNameShortener{
		MaxLen:           MaxToolNameLength,
		CustomMappings:   nil,
		PreservePrefixes: []string{"mcp__"},
	}
}

// ShortenName applies shortening rules to a single tool name.
// Returns the shortened name (may be unchanged if already within limits).
func (s *ToolNameShortener) ShortenName(name string) string {
	limit := s.MaxLen
	if limit <= 0 {
		limit = MaxToolNameLength
	}

	if len(name) <= limit {
		return name
	}

	// Check custom mappings first
	if s.CustomMappings != nil {
		if mapped, ok := s.CustomMappings[name]; ok {
			return mapped
		}
	}

	// Try preserving known prefixes
	for _, prefix := range s.PreservePrefixes {
		if strings.HasPrefix(name, prefix) {
			// Try to keep prefix + last segment
			idx := strings.LastIndex(name, "__")
			if idx > 0 && idx < len(name)-2 {
				candidate := prefix + name[idx+2:]
				if len(candidate) <= limit {
					return candidate
				}
				// If still too long, truncate the candidate
				return candidate[:limit]
			}
		}
	}

	// Fallback: simple truncation
	return name[:limit]
}

// ShortenNames ensures uniqueness of shortened names within a batch.
// Returns a mapping from original name to shortened (unique) name.
func (s *ToolNameShortener) ShortenNames(names []string) map[string]string {
	limit := s.MaxLen
	if limit <= 0 {
		limit = MaxToolNameLength
	}

	used := make(map[string]struct{}, len(names))
	result := make(map[string]string, len(names))

	for _, name := range names {
		candidate := s.ShortenName(name)
		unique := s.makeUnique(candidate, used, limit)
		used[unique] = struct{}{}
		result[name] = unique
	}

	return result
}

// makeUnique appends numeric suffixes to ensure uniqueness.
func (s *ToolNameShortener) makeUnique(candidate string, used map[string]struct{}, limit int) string {
	if _, exists := used[candidate]; !exists {
		return candidate
	}

	base := candidate
	for i := 1; ; i++ {
		suffix := "_" + strconv.Itoa(i)
		allowed := limit - len(suffix)
		if allowed < 0 {
			allowed = 0
		}
		tmp := base
		if len(tmp) > allowed {
			tmp = tmp[:allowed]
		}
		tmp = tmp + suffix
		if _, exists := used[tmp]; !exists {
			return tmp
		}
	}
}

// ShortenToolName is a convenience function for single-name shortening with defaults.
func ShortenToolName(name string) string {
	return DefaultToolNameShortener().ShortenName(name)
}

// ShortenToolNames is a convenience function for batch shortening with defaults.
func ShortenToolNames(names []string) map[string]string {
	return DefaultToolNameShortener().ShortenNames(names)
}
