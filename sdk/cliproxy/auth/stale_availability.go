package auth

import "time"

// clearStaleUnavailableState clears an "unavailable" flag that has no recovery
// deadline and no terminal condition. Without this, availabilityBlock would
// exclude the credential forever while cooldown snapshots stay empty.
func clearStaleUnavailableState(auth *Auth, now time.Time) bool {
	if auth == nil || !auth.Unavailable {
		return false
	}
	if auth.Disabled || auth.Status == StatusDisabled {
		return false
	}
	if hasUnauthorizedAuthFailure(auth) {
		return false
	}
	if auth.RequiresSignIn && !auth.HasValidAccessToken(now) {
		return false
	}
	if auth.NextRetryAfter.After(now) || auth.NextRefreshAfter.After(now) {
		return false
	}
	if auth.Quota.Exceeded {
		return false
	}
	if auth.credentialRetryAfter.After(now) || auth.nonAuthCredentialRetryAfter.After(now) {
		return false
	}
	if effectiveAuthForceUntil(auth).After(now) {
		return false
	}

	originalUnavailable := auth.Unavailable
	originalNextRetry := auth.NextRetryAfter
	originalQuota := cooldownFieldsOf(auth.Quota)
	modelStatesChanged := false

	auth.Unavailable = false
	auth.NextRetryAfter = time.Time{}

	for _, state := range auth.ModelStates {
		if state == nil || state.Status == StatusDisabled || !state.Unavailable {
			continue
		}
		if isUnauthorizedModelState(state) {
			continue
		}
		if effectiveModelForceUntil(state).After(now) {
			continue
		}
		if state.credentialQuotaShadow != nil && isModelStateActiveCooldown(state.credentialQuotaShadow, now) {
			continue
		}
		if state.NextRetryAfter.After(now) || state.Quota.NextRecoverAt.After(now) {
			continue
		}
		if state.Quota.Exceeded && state.Quota.NextRecoverAt.IsZero() {
			continue
		}
		resetModelState(state, now)
		modelStatesChanged = true
	}
	updateAggregatedAvailability(auth, now)

	// An aggregate re-flag driven by a remaining live model block is not a
	// change: only commit (and persist) a real net state transition.
	changed := modelStatesChanged ||
		auth.Unavailable != originalUnavailable ||
		!auth.NextRetryAfter.Equal(originalNextRetry) ||
		!cooldownQuotaEqual(cooldownFieldsOf(auth.Quota), originalQuota)
	if changed {
		auth.Generation++
		auth.UpdatedAt = now
	}
	return changed
}
