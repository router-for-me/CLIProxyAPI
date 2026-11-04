package auth

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// usageLimitMinResetIn separates cooldowns a client should wait out through
// Retry-After from exhausted quota it should stop on. A pool whose earliest
// reset is this close or closer keeps the retryable model_cooldown response.
const usageLimitMinResetIn = 5 * time.Minute

// codexWeeklyWindowMinutes is the shortest window Codex reports in its
// secondary rate-limit slot; shorter windows use the primary slot.
const codexWeeklyWindowMinutes = 24 * 60

// usageLimitRefusal describes a pool whose every eligible credential is out of
// quota: when the first one reopens and, when the upstream named it, the
// length of that credential's exhausted window.
type usageLimitRefusal struct {
	resetAt       time.Time
	windowMinutes int64
}

func usageLimitCooldown(err error) *modelCooldownError {
	var cooldown *modelCooldownError
	if errors.As(err, &cooldown) && cooldown.usageLimit != nil {
		return cooldown
	}
	return nil
}

// IsUsageLimitReachedError reports whether err carries a final usage-limit
// cooldown.
func IsUsageLimitReachedError(err error) bool { return usageLimitCooldown(err) != nil }

// UsageLimitResetAt reports when the first credential reopens if err carries a
// final usage-limit cooldown.
func UsageLimitResetAt(err error) (time.Time, bool) {
	if cooldown := usageLimitCooldown(err); cooldown != nil {
		return cooldown.usageLimit.resetAt, true
	}
	return time.Time{}, false
}

// UsageLimitResponseHeaders returns the client headers of the final
// usage-limit cooldown in err's chain: X-Should-Retry: false and, when the
// exhausted window is known, Codex's rate-limit headers. It never includes
// Retry-After.
func UsageLimitResponseHeaders(err error) (http.Header, bool) {
	cooldown := usageLimitCooldown(err)
	if cooldown == nil {
		return nil, false
	}
	return cooldown.usageLimit.headers(), true
}

// headers reports the usage limit as a response would: X-Should-Retry, which
// the official OpenAI and Anthropic SDKs honor ahead of the status code, and
// the exhausted window in the rate-limit headers Codex reads with a
// usage_limit_reached response (5-hour window in the primary slot, weekly in
// the secondary). An unknown window gets no Codex headers: the slot would be a
// guess.
func (r *usageLimitRefusal) headers() http.Header {
	headers := http.Header{"X-Should-Retry": {"false"}}
	if r.windowMinutes > 0 {
		prefix := "X-Codex-Primary-"
		if r.windowMinutes >= codexWeeklyWindowMinutes {
			prefix = "X-Codex-Secondary-"
		}
		headers.Set(prefix+"Used-Percent", "100")
		headers.Set(prefix+"Window-Minutes", strconv.FormatInt(r.windowMinutes, 10))
		headers.Set(prefix+"Reset-At", strconv.FormatInt(r.resetAt.Unix(), 10))
	}
	return headers
}

// usageLimitCandidate is one credential eligible for the request: the model
// selection checks it under, and the state models normal execution records its
// failures under (one per pooled backend).
type usageLimitCandidate struct {
	auth           *Auth
	selectionModel string
	stateModels    []string
}

// candidateQuotaBlock reports when a credential can serve the request again,
// and the exhausted window length when known, if what blocks it is explicit
// quota exhaustion. Selection blocks a credential under its selection model;
// otherwise it can still serve the request through any execution model whose
// state is not blocked, so every one of those must be blocked by evidence and
// the credential reopens with the first of them. A model disabled on the
// credential never serves the request and is left out; skip reports a
// credential with every model left out, which is no candidate at all.
func candidateQuotaBlock(candidate usageLimitCandidate, now time.Time) (resetAt time.Time, window int64, ok, skip bool) {
	models := candidate.stateModels
	if blocked, _, _ := isAuthBlockedForModel(candidate.auth, candidate.selectionModel, now); blocked || len(models) == 0 {
		models = []string{candidate.selectionModel}
	}
	for _, model := range models {
		blocked, reason, next := isAuthBlockedForModel(candidate.auth, model, now)
		if blocked && reason == blockReasonDisabled {
			continue
		}
		if !blocked || reason != blockReasonCooldown || !next.After(now) {
			return time.Time{}, 0, false, false
		}
		modelWindow, evidenced := quotaExhaustionEvidence(candidate.auth, model, now)
		if !evidenced {
			return time.Time{}, 0, false, false
		}
		if resetAt.IsZero() || next.Before(resetAt) {
			resetAt, window = next, modelWindow
		}
	}
	if resetAt.IsZero() {
		return time.Time{}, 0, false, true
	}
	return resetAt, window, true, false
}

// quotaExhaustionEvidence reports whether the cooldown that blocks auth for
// model is backed by live, explicit upstream quota-exhaustion evidence whose
// recovery time covers the blocking deadline, and the exhausted window length
// when known. It mirrors isAuthBlockedForModel: a live credential-wide quota
// decides alone; otherwise every blocking state of the model must carry the
// evidence.
func quotaExhaustionEvidence(auth *Auth, model string, now time.Time) (int64, bool) {
	if auth.Quota.Exceeded && auth.Quota.Reason == "credential_quota" && auth.Quota.NextRecoverAt.After(now) {
		return auth.Quota.LimitWindowMinutes, auth.Quota.Exhausted
	}
	// Only model-scoped and credential-wide cooldowns record evidence.
	modelKey := canonicalModelKey(model)
	evidenced := false
	var window int64
	var latest time.Time
	for stateModel, state := range auth.ModelStates {
		if state == nil || canonicalModelKey(stateModel) != modelKey {
			continue
		}
		blocked, _, deadline := availabilityBlock(state.Unavailable, state.Quota.Exceeded, state.NextRetryAfter, state.Quota.NextRecoverAt, now)
		if !blocked {
			continue
		}
		if !quotaExhaustionCovers(state.Quota, deadline, now) {
			return 0, false
		}
		if !evidenced || deadline.After(latest) {
			window, latest = state.Quota.LimitWindowMinutes, deadline
		}
		evidenced = true
	}
	return window, evidenced
}

// quotaExhaustionCovers reports whether quota holds live exhaustion evidence
// that lasts at least until deadline.
func quotaExhaustionCovers(quota QuotaState, deadline, now time.Time) bool {
	return quota.Exceeded && quota.Exhausted && quota.NextRecoverAt.After(now) && !quota.NextRecoverAt.Before(deadline)
}

// usageLimitRefusalForPool reports the usage limit that blocks every
// credential eligible for the request (the candidates pickNextMixedLegacy
// considers, ignoring which ones this request already attempted), or nil when
// there is none, any of them can still serve the request or is blocked for
// another reason, or the first reset is usageLimitMinResetIn away or closer.
// Only explicit quota-exhaustion evidence tied to the blocking deadline
// counts: a long or repeated generic 429 stays retryable.
func (m *Manager) usageLimitRefusalForPool(providers []string, model string, eligibility authSelectionEligibility, pinnedAuthID string, now time.Time) *usageLimitRefusal {
	providerSet := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		if key := canonicalSchedulingProvider(provider); key != "" {
			providerSet[key] = struct{}{}
		}
	}
	registryRef := registry.GetGlobalRegistry()
	hasModel := strings.TrimSpace(model) != ""
	m.mu.RLock()
	defer m.mu.RUnlock()
	requirePositiveWeight := selectorRequiresPositiveWeight(m.selector)
	var refusal *usageLimitRefusal
	for _, auth := range m.auths {
		if auth == nil || auth.Disabled || auth.Status == StatusDisabled || (pinnedAuthID != "" && auth.ID != pinnedAuthID) || !eligibility.allows(auth) {
			continue
		}
		if requirePositiveWeight && authWeight(auth) <= 0 {
			// Weighted selection never picks it, so it cannot serve the request.
			continue
		}
		providerKey := executorKeyFromAuth(auth)
		if _, ok := providerSet[providerKey]; !ok {
			continue
		}
		if _, ok := m.executorLocked(providerKey); !ok {
			continue
		}
		if hasModel && !m.authSupportsRouteModel(registryRef, auth, model) {
			continue
		}
		candidate := usageLimitCandidate{auth: auth, selectionModel: model}
		if hasModel {
			candidate.selectionModel = m.selectionModelForAuth(auth, model)
			upstreamModels, pooled, _, _ := m.executionModelCandidatesWithAliasRotation(auth, model, false)
			for _, upstreamModel := range upstreamModels {
				candidate.stateModels = append(candidate.stateModels, m.stateModelForExecution(auth, model, upstreamModel, pooled))
			}
		}
		resetAt, window, ok, skip := candidateQuotaBlock(candidate, now)
		if skip {
			continue
		}
		if !ok {
			return nil
		}
		if refusal == nil || resetAt.Before(refusal.resetAt) || (resetAt.Equal(refusal.resetAt) && refusal.windowMinutes == 0) {
			refusal = &usageLimitRefusal{resetAt: resetAt, windowMinutes: window}
		}
	}
	if refusal == nil || refusal.resetAt.Sub(now) <= usageLimitMinResetIn {
		return nil
	}
	return refusal
}

// selectorRequiresPositiveWeight reports whether selector, directly or as a
// session-affinity fallback, is weighted round-robin, which never picks a
// credential whose weight is zero.
func selectorRequiresPositiveWeight(selector Selector) bool {
	if affinity, ok := selector.(*SessionAffinitySelector); ok && affinity != nil {
		selector = affinity.fallback
	}
	_, weighted := selector.(*WeightedRoundRobinSelector)
	return weighted
}

// usageLimitResponseError applies the usage-limit response mode to the final
// error of a request. When the mode is active, the final error is a 429 (or no
// credential could run the request, as when a pooled alias has every backend
// cooling), and every eligible credential is out of quota with a reset more
// than usageLimitMinResetIn away, it returns a model cooldown reported as a
// final usage limit. Otherwise it returns nil and the caller keeps its error,
// so the retryable mode keeps today's behavior.
func (m *Manager) usageLimitResponseError(ctx context.Context, providers []string, model string, opts cliproxyexecutor.Options, err error) *modelCooldownError {
	if !QuotaCooldownUsageLimitModeEnabled() || m.HomeEnabled() {
		return nil
	}
	var authErr *Error
	if statusCodeFromError(err) != http.StatusTooManyRequests && !(errors.As(err, &authErr) && authErr.Code == "auth_not_found") {
		return nil
	}
	now := time.Now()
	refusal := m.usageLimitRefusalForPool(providers, model, authSelectionEligibilityForRequest(ctx, opts), pinnedAuthIDFromMetadata(opts.Metadata), now)
	if refusal == nil {
		return nil
	}
	modelName, provider, cause := model, "", err
	if len(providers) == 1 {
		provider = providers[0]
	}
	var cooldown *modelCooldownError
	if errors.As(err, &cooldown) {
		// All credentials were already cooling at selection: keep the
		// selection error's naming and last-error cause.
		if cooldown.model != "" {
			modelName = cooldown.model
		}
		provider, cause = cooldown.provider, cooldown.cause
	}
	usageLimit := newModelCooldownErrorWithCause(modelName, provider, refusal.resetAt.Sub(now), cause)
	usageLimit.usageLimit = refusal
	return usageLimit
}
