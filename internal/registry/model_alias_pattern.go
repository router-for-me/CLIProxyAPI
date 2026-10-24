package registry

import "strings"

// ModelAliasPattern maps a wildcard client-visible model pattern to the upstream
// model that serves it.
//
// Patterns are routing hints only. They are deliberately never registered as
// models, because a pattern is not a model id and must not surface in the model
// catalog. Provider lookups fall back to them only after an exact model id misses.
type ModelAliasPattern struct {
	// Pattern is the client-visible alias pattern, for example "claude-haiku-4-5-*".
	Pattern string
	// Target is the upstream model id the pattern resolves to.
	Target string
}

// MatchModelPattern reports whether value matches pattern, where '*' matches zero
// or more characters. A pattern without '*' compares for equality.
//
// Matching is case-insensitive to line up with the model id comparisons used
// elsewhere in the registry and with OAuth model alias resolution.
func MatchModelPattern(pattern, value string) bool {
	pattern = strings.TrimSpace(pattern)
	value = strings.TrimSpace(value)
	if pattern == "" {
		return false
	}
	if !strings.Contains(pattern, "*") {
		return strings.EqualFold(pattern, value)
	}

	pattern = strings.ToLower(pattern)
	value = strings.ToLower(value)
	parts := strings.Split(pattern, "*")

	if prefix := parts[0]; prefix != "" {
		if !strings.HasPrefix(value, prefix) {
			return false
		}
		value = value[len(prefix):]
	}
	if suffix := parts[len(parts)-1]; suffix != "" {
		if !strings.HasSuffix(value, suffix) {
			return false
		}
		value = value[:len(value)-len(suffix)]
	}
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

// SetModelAliasPatterns replaces the routing-only alias patterns consulted when an
// exact model id lookup finds no provider. Entries without a pattern or a target
// are dropped. Order is preserved: the first matching pattern wins.
func (r *ModelRegistry) SetModelAliasPatterns(patterns []ModelAliasPattern) {
	if r == nil {
		return
	}

	clean := make([]ModelAliasPattern, 0, len(patterns))
	for _, entry := range patterns {
		pattern := strings.TrimSpace(entry.Pattern)
		target := strings.TrimSpace(entry.Target)
		if pattern == "" || target == "" {
			continue
		}
		if !strings.Contains(pattern, "*") {
			continue
		}
		clean = append(clean, ModelAliasPattern{Pattern: pattern, Target: target})
	}

	r.mutex.Lock()
	defer r.mutex.Unlock()
	if len(clean) == 0 {
		r.modelAliasPatterns = nil
		return
	}
	r.modelAliasPatterns = clean
}

// resolveModelAliasPatternLocked returns the target model for the first alias
// pattern matching modelID, or an empty string when nothing matches.
//
// Callers must hold at least a read lock on the registry mutex.
func (r *ModelRegistry) resolveModelAliasPatternLocked(modelID string) string {
	if r == nil || len(r.modelAliasPatterns) == 0 {
		return ""
	}
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return ""
	}
	for _, entry := range r.modelAliasPatterns {
		if MatchModelPattern(entry.Pattern, modelID) {
			return entry.Target
		}
	}
	return ""
}

// modelRegistrationForTargetLocked resolves an alias pattern target to its
// registration.
//
// Registered model ids keep the casing their client supplied, while a pattern
// target comes from configuration. An exact lookup is tried first and a
// case-insensitive scan only backs it up, so wildcard routing stays consistent
// with the case-insensitive matching used everywhere else on the alias path.
//
// Callers must hold at least a read lock on the registry mutex.
func (r *ModelRegistry) modelRegistrationForTargetLocked(target string) *ModelRegistration {
	target = strings.TrimSpace(target)
	if r == nil || target == "" {
		return nil
	}
	if registration, exists := r.models[target]; exists && registration != nil {
		return registration
	}
	for modelID, registration := range r.models {
		if registration == nil {
			continue
		}
		if strings.EqualFold(modelID, target) {
			return registration
		}
	}
	return nil
}
