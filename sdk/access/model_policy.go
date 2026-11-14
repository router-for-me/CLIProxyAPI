package access

import "strings"

// ModelPolicyContextKey is the gin context key under which the authenticated
// principal's *ModelPolicy is stored for request handlers.
const ModelPolicyContextKey = "accessModelPolicy"

// ModelPolicy restricts which models an authenticated principal may list and call.
//
// Patterns are case-insensitive and support the wildcards '*' (any run of characters, '/' included) and '?' (one character).
// A denied model is never allowed. When Allowed is empty every model that is not
// denied is allowed. A nil policy places no restriction.
type ModelPolicy struct {
	Allowed []string
	Denied  []string
}

// NewModelPolicy builds a policy from allow and deny patterns.
// It returns nil when no pattern is supplied, meaning "unrestricted".
func NewModelPolicy(allowed, denied []string) *ModelPolicy {
	policy := &ModelPolicy{Allowed: normalizePatterns(allowed), Denied: normalizePatterns(denied)}
	if len(policy.Allowed) == 0 && len(policy.Denied) == 0 {
		return nil
	}
	return policy
}

// Allows reports whether the policy permits the given model name.
func (p *ModelPolicy) Allows(model string) bool {
	if p == nil {
		return true
	}
	model = strings.ToLower(strings.TrimSpace(model))
	if matchesAny(p.Denied, model) {
		return false
	}
	return len(p.Allowed) == 0 || matchesAny(p.Allowed, model)
}

func matchesAny(patterns []string, model string) bool {
	for _, pattern := range patterns {
		if globMatch(pattern, model) {
			return true
		}
	}
	return false
}

func normalizePatterns(patterns []string) []string {
	out := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		if pattern = strings.ToLower(strings.TrimSpace(pattern)); pattern != "" {
			out = append(out, pattern)
		}
	}
	return out
}

// globMatch reports whether value matches pattern, where '*' matches any run of
// characters (including none) and '?' matches exactly one.
func globMatch(pattern, value string) bool {
	p, v := []rune(pattern), []rune(value)
	pi, vi, star, mark := 0, 0, -1, 0
	for vi < len(v) {
		switch {
		case pi < len(p) && (p[pi] == '?' || p[pi] == v[vi]):
			pi++
			vi++
		case pi < len(p) && p[pi] == '*':
			star, mark = pi, vi
			pi++
		case star >= 0:
			pi = star + 1
			mark++
			vi = mark
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}
