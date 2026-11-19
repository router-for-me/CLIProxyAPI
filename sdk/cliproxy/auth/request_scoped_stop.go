package auth

import (
	"context"
	"strings"
)

// StoppedByOAuthRequestScopedRule reports whether err, returned for a request
// served by an OAuth auth of provider, matches a stop rule of the effective
// OAuth request-scoped errors. It is false when the provider has no such rule,
// for example after an explicit empty rule list.
func (m *Manager) StoppedByOAuthRequestScopedRule(ctx context.Context, provider string, err error) bool {
	if m == nil || err == nil {
		return false
	}
	cfg := m.requestScopedErrorConfig(ctx)
	if cfg == nil {
		return false
	}
	rules := cfg.OAuthRequestScopedErrors[strings.ToLower(strings.TrimSpace(provider))]
	action, matched := MatchRequestScopedErrorRules(rules, err)
	return matched && (action == RequestScopedActionStop || action == RequestScopedActionStopAndCooldown)
}
