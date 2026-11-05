package auth

import (
	"context"
	"strings"
)

// SessionAffinityProviderOverride changes selection within one provider only.
// PriorityPrecedence keeps affinity within the highest eligible priority tier.
type SessionAffinityProviderOverride struct {
	Fallback           Selector
	PriorityPrecedence bool
}

func affinityProviderKey(provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "anthropic" {
		return "claude"
	}
	return provider
}

type sessionAffinityRequestedProviderKey struct{}

// Resolve the requested pool before availability, retry and model filtering.
// Keep this separate from the affinity namespace, which remains "mixed".
func withSessionAffinityRequestedProviders(ctx context.Context, providers map[string]struct{}) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	key := "mixed"
	if len(providers) == 1 {
		for provider := range providers {
			key = affinityProviderKey(provider)
		}
	}
	return context.WithValue(ctx, sessionAffinityRequestedProviderKey{}, key)
}

func (s *SessionAffinitySelector) providerPolicy(ctx context.Context, provider string, candidates []*Auth) SessionAffinityProviderOverride {
	key := affinityProviderKey(provider)
	requested := ""
	if ctx != nil {
		requested, _ = ctx.Value(sessionAffinityRequestedProviderKey{}).(string)
	}
	if key == "mixed" && requested != "" {
		key = requested
	} else if key == "mixed" {
		// Execution uses the mixed entrypoint even for one provider. Keep its
		// affinity namespace, but use the real provider's policy when unambiguous.
		key = ""
		for _, candidate := range candidates {
			if candidate == nil {
				continue
			}
			candidateKey := affinityProviderKey(executorKeyFromAuth(candidate))
			if candidateKey == "" || (key != "" && key != candidateKey) {
				key = "mixed"
				break
			}
			key = candidateKey
		}
	}
	policy, ok := s.providerOverrides[key]
	if !ok || policy.Fallback == nil {
		policy.Fallback = s.fallback
	}
	return policy
}

type sessionAffinityAuthPrioritiesKey struct{}

func withSessionAffinityAuthPriorities(ctx context.Context, auths []*Auth) context.Context {
	if len(auths) == 0 {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	priorities := make(map[string]int, len(auths))
	for _, auth := range auths {
		if auth != nil {
			priorities[auth.ID] = authPriority(auth)
		}
	}
	return context.WithValue(ctx, sessionAffinityAuthPrioritiesKey{}, priorities)
}

// A retry can exclude a bound auth from candidates. Its latest priority still
// comes from the manager's snapshot, rather than a stale cached binding.
func bindingInHighestPriority(ctx context.Context, candidates, available []*Auth, authID string) bool {
	if len(available) == 0 {
		return false
	}
	priority, known := 0, false
	for _, candidate := range candidates {
		if candidate != nil && candidate.ID == authID {
			priority, known = authPriority(candidate), true
			break
		}
	}
	if !known && ctx != nil {
		if priorities, ok := ctx.Value(sessionAffinityAuthPrioritiesKey{}).(map[string]int); ok {
			priority, known = priorities[authID]
		}
	}
	if !known {
		return false
	}
	for _, candidate := range available {
		if candidate != nil && authPriority(candidate) > priority {
			return false
		}
	}
	return true
}

// LocalBuiltinSelector reports whether local built-in routing owns selection.
// Home and plugin schedulers can select before session-affinity policies run.
func (m *Manager) LocalBuiltinSelector() bool {
	if m == nil || m.HomeEnabled() || m.hasPluginScheduler() {
		return false
	}
	selector := m.Selector()
	if affinity, ok := selector.(*SessionAffinitySelector); ok {
		if !isBuiltInSelector(affinity.fallback) {
			return false
		}
		for _, policy := range affinity.providerOverrides {
			if !isBuiltInSelector(policy.Fallback) {
				return false
			}
		}
		return true
	}
	return isBuiltInSelector(selector)
}
