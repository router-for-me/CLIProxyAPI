package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// proxyCooldownError identifies only a locally established all-candidate
// cooldown. Its deadline is the next reevaluation, not a provider quota window.
// Keep the original error as both the human detail and the error-chain cause.
type proxyCooldownError struct {
	cause            error
	nextReevaluation time.Time
}

func (e *proxyCooldownError) Error() string                            { return e.cause.Error() }
func (e *proxyCooldownError) Unwrap() error                            { return e.cause }
func (e *proxyCooldownError) StatusCode() int                          { return http.StatusTooManyRequests }
func (e *proxyCooldownError) ProxyCooldownNextReevaluation() time.Time { return e.nextReevaluation }

// Preserve the original downstream header contract through the local wrapper.
func (e *proxyCooldownError) Headers() http.Header {
	var provider interface{ Headers() http.Header }
	if errors.As(e.cause, &provider) && provider != nil {
		return provider.Headers().Clone()
	}
	return nil
}

// ProxyCooldownNextReevaluation accepts only the concrete trusted local error;
// upstream strings and interfaces cannot supply authenticated bridge metadata.
func ProxyCooldownNextReevaluation(err error) (time.Time, bool) {
	var cooldown *proxyCooldownError
	if !errors.As(err, &cooldown) || cooldown == nil {
		return time.Time{}, false
	}
	return cooldown.nextReevaluation, !cooldown.nextReevaluation.IsZero()
}

// withProxyCooldownSummary observes availability after final upstream error
// preference, without choosing an auth or changing scheduler/affinity state.
// All originally eligible candidates participate, including already tried ones.
func (m *Manager) withProxyCooldownSummary(ctx context.Context, providers []string, model string, opts cliproxyexecutor.Options, cause error, now time.Time) error {
	if cause == nil || statusCodeFromError(cause) != http.StatusTooManyRequests || m.HomeEnabled() || m.hasPluginScheduler() {
		return cause
	}
	eligibility := authSelectionEligibilityForRequest(ctx, opts)
	pinned := pinnedAuthIDFromMetadata(opts.Metadata)
	providerSet := make(map[string]struct{}, len(providers))
	for _, p := range providers {
		providerSet[canonicalSchedulingProvider(p)] = struct{}{}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	// Custom/plugin schedulers can impose additional eligibility rules. Do not
	// claim their state from the built-in local availability model.
	selector := m.selector
	if affinity, ok := selector.(*SessionAffinitySelector); ok {
		selector = affinity.fallback
	}
	if !isBuiltInSelector(selector) {
		return cause
	}
	_, requirePositiveWeight := selector.(*WeightedRoundRobinSelector)
	registryRef := registry.GetGlobalRegistry()
	var earliest time.Time
	count := 0
	for _, candidate := range m.auths {
		if candidate == nil || candidate.Disabled || (requirePositiveWeight && authWeight(candidate) <= 0) {
			continue
		}
		if pinned != "" && candidate.ID != pinned {
			continue
		}
		if !eligibility.allows(candidate) {
			continue
		}
		provider := canonicalSchedulingProvider(executorKeyFromAuth(candidate))
		if _, ok := providerSet[provider]; !ok {
			continue
		}
		if _, ok := m.executorLocked(provider); !ok {
			continue
		}
		if strings.TrimSpace(model) != "" && !m.authSupportsRouteModel(registryRef, candidate, model) {
			continue
		}
		count++
		blocked, reason, next := isAuthBlockedForModel(candidate, m.selectionModelForAuth(candidate, model), now)
		if !blocked || reason != blockReasonCooldown || !next.After(now) {
			return cause
		}
		if earliest.IsZero() || next.Before(earliest) {
			earliest = next
		}
	}
	if count == 0 || earliest.IsZero() {
		return cause
	}
	return &proxyCooldownError{cause: cause, nextReevaluation: earliest}
}
