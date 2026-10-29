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
	available = preferCodexWebsocketAuths(ctx, provider, available)
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
