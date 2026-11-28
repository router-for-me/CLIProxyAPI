package modelconfig

import "strings"

// MatchExcludedModelPattern matches normalized model IDs and patterns where '*' matches any substring.
func MatchExcludedModelPattern(pattern, value string) bool {
	if pattern == "" {
		return false
	}

	// Fast path for exact match (no wildcard present).
	if !strings.Contains(pattern, "*") {
		return pattern == value
	}

	parts := strings.Split(pattern, "*")
	// Handle prefix.
	if prefix := parts[0]; prefix != "" {
		if !strings.HasPrefix(value, prefix) {
			return false
		}
		value = value[len(prefix):]
	}

	// Handle suffix.
	if suffix := parts[len(parts)-1]; suffix != "" {
		if !strings.HasSuffix(value, suffix) {
			return false
		}
		value = value[:len(value)-len(suffix)]
	}

	// Handle middle segments in order.
	for i := 1; i < len(parts)-1; i++ {
		segment := parts[i]
		if segment == "" {
			continue
		}
		idx := strings.Index(value, segment)
		if idx < 0 {
			return false
		}
		value = value[idx+len(segment):]
	}

	return true
}

// NormalizeModelExclusionPrefix folds case and whitespace without changing the
// slashes that applyModelPrefixes preserves in published routes.
func NormalizeModelExclusionPrefix(prefix string) string {
	return strings.ToLower(strings.TrimSpace(prefix))
}

// IsModelExcluded matches canonical and published IDs. The slash-trimmed
// prefix form remains supported for credentials supplied without normalization.
func IsModelExcluded(modelID string, patterns []string, authPrefix string) bool {
	if len(patterns) == 0 {
		return false
	}
	modelID = strings.ToLower(strings.TrimSpace(modelID))
	prefix := NormalizeModelExclusionPrefix(authPrefix)
	normalizedPrefix := strings.Trim(prefix, "/")
	var prefixed, normalizedPrefixed string
	for _, raw := range patterns {
		pattern := strings.ToLower(strings.TrimSpace(raw))
		if pattern == "" {
			continue
		}
		if MatchExcludedModelPattern(pattern, modelID) {
			return true
		}
		if prefix == "" {
			continue
		}
		if prefixed == "" {
			prefixed = prefix + "/" + modelID
			if normalizedPrefix != "" && normalizedPrefix != prefix {
				normalizedPrefixed = normalizedPrefix + "/" + modelID
			}
		}
		if MatchExcludedModelPattern(pattern, prefixed) ||
			(normalizedPrefixed != "" && MatchExcludedModelPattern(pattern, normalizedPrefixed)) {
			return true
		}
	}
	return false
}
