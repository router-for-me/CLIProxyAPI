package auth

import (
	"context"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// SoonestResetFirstSelector prefers expiring quota within each provider and uses
// smooth weighted round-robin for equal resets or pools without a known reset.
type SoonestResetFirstSelector struct {
	ties WeightedRoundRobinSelector
}

func (s *SoonestResetFirstSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	now := time.Now()
	available, errAvailable := getSelectorAvailableAuths(ctx, positiveWeightAuths(auths), provider, model, now)
	if errAvailable != nil {
		return nil, errAvailable
	}
	available = preferWebsocketAuthsPerProvider(ctx, available)
	resets := make(map[*Auth]time.Time, len(available))
	earliest := make(map[string]time.Time)
	for _, candidate := range available {
		providerKey := canonicalSchedulingProvider(executorKeyFromAuth(candidate))
		resetAt, known := candidate.Quota.nextReset(providerKey, now)
		if !known {
			continue
		}
		resets[candidate] = resetAt
		if previous, exists := earliest[providerKey]; !exists || resetAt.Before(previous) {
			earliest[providerKey] = resetAt
		}
	}
	preferred := make([]*Auth, 0, len(available))
	for _, candidate := range available {
		providerKey := canonicalSchedulingProvider(executorKeyFromAuth(candidate))
		if resetAt, known := earliest[providerKey]; known {
			if candidateReset, exists := resets[candidate]; !exists || !candidateReset.Equal(resetAt) {
				continue
			}
		}
		preferred = append(preferred, candidate)
	}
	return s.ties.Pick(ctx, provider, model, opts, preferred)
}

func selectorUsesWeights(selector Selector) bool {
	switch selector.(type) {
	case *WeightedRoundRobinSelector, *SoonestResetFirstSelector:
		return true
	default:
		return false
	}
}

// preferWebsocketAuthsPerProvider applies the scheduler's websocket transport
// preference within each websocket-preferring provider of a mixed candidate set.
func preferWebsocketAuthsPerProvider(ctx context.Context, available []*Auth) []*Auth {
	groups := make(map[string][]*Auth)
	for _, candidate := range available {
		if providerKey := canonicalSchedulingProvider(executorKeyFromAuth(candidate)); providerPrefersWebsocketTransport(providerKey) {
			groups[providerKey] = append(groups[providerKey], candidate)
		}
	}
	excluded := make(map[*Auth]struct{})
	for _, group := range groups {
		preferred := preferCodexWebsocketAuths(ctx, "codex", group)
		if len(preferred) == len(group) {
			continue
		}
		kept := make(map[*Auth]struct{}, len(preferred))
		for _, candidate := range preferred {
			kept[candidate] = struct{}{}
		}
		for _, candidate := range group {
			if _, ok := kept[candidate]; !ok {
				excluded[candidate] = struct{}{}
			}
		}
	}
	if len(excluded) == 0 {
		return available
	}
	filtered := make([]*Auth, 0, len(available)-len(excluded))
	for _, candidate := range available {
		if _, ok := excluded[candidate]; !ok {
			filtered = append(filtered, candidate)
		}
	}
	return filtered
}
