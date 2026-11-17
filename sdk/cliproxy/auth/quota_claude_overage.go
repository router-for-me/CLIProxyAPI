package auth

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// QuotaRecoveryRequiresClaudeExtraUsage reports whether a still-live Claude
// overage rejection needs capacity evidence from the separate paid usage pool.
func QuotaRecoveryRequiresClaudeExtraUsage(a *Auth) bool {
	if a == nil || !strings.EqualFold(strings.TrimSpace(a.Provider), "claude") {
		return false
	}
	now := time.Now()
	if a.claudeOverageCooldownUntil.After(now) {
		return true
	}
	for _, state := range a.ModelStates {
		if state != nil && (state.claudeOverageCooldownUntil.After(now) || state.credentialQuotaShadow != nil && state.credentialQuotaShadow.claudeOverageCooldownUntil.After(now)) {
			return true
		}
	}
	return false
}

// QuotaRecoveryRequiresClaudeFablePool identifies a live standalone 7d_oi
// rejection. No canonical usage field is proven to cover that opaque pool.
func QuotaRecoveryRequiresClaudeFablePool(a *Auth) bool {
	if a == nil || !strings.EqualFold(strings.TrimSpace(a.Provider), "claude") {
		return false
	}
	now := time.Now()
	if a.claudeFablePoolCooldownUntil.After(now) {
		return true
	}
	for _, state := range a.ModelStates {
		if state != nil && (state.claudeFablePoolCooldownUntil.After(now) || state.credentialQuotaShadow != nil && state.credentialQuotaShadow.claudeFablePoolCooldownUntil.After(now)) {
			return true
		}
	}
	return false
}

func claudeOverageHeaders(headers http.Header) bool {
	signals := collectQuotaSignals("claude", headers)
	get := func(name string) string {
		return strings.ToLower(strings.TrimSpace(signals[http.CanonicalHeaderKey(name)]))
	}
	// Explicit paid-pool rejection is independent of shared-window capacity.
	rejected := get("Anthropic-Ratelimit-Unified-Overage-Status") == "rejected" || get("Anthropic-Ratelimit-Unified-Overage-Disabled-Reason") != ""
	if rejected {
		return true
	}
	five, seven := get("Anthropic-Ratelimit-Unified-5h-Status"), get("Anthropic-Ratelimit-Unified-7d-Status")
	if five == "rejected" || seven == "rejected" {
		return false
	}
	claim := strings.Contains(get("Anthropic-Ratelimit-Unified-Representative-Claim"), "overage")
	if !(claim && get("Anthropic-Ratelimit-Unified-Status") == "rejected") {
		return false
	}
	allowed := func(value string) bool { return value == "allowed" || value == "allowed_warning" }
	healthy := func(name string, absent bool) bool {
		value := get(name)
		if value == "" {
			return absent
		}
		// Strict comparisons also reject NaN and infinite parsed header values.
		return claudeOverageUtilizationHealthy(value)
	}
	if allowed(five) && allowed(seven) {
		return true
	}
	if allowed(seven) && five == "" && healthy("Anthropic-Ratelimit-Unified-5h-Utilization", true) {
		return true
	}
	if allowed(five) && seven == "" && healthy("Anthropic-Ratelimit-Unified-7d-Utilization", false) {
		return true
	}
	return five == "" && seven == "" && claim && get("Anthropic-Ratelimit-Unified-Status") == "rejected" && healthy("Anthropic-Ratelimit-Unified-5h-Utilization", true) && healthy("Anthropic-Ratelimit-Unified-7d-Utilization", true)
}

func claudeOverageOwnDeadline(quota QuotaState, retry *time.Duration, now time.Time) time.Time {
	if retry != nil {
		duration := *retry
		if duration < minQuotaCooldownFloor {
			duration = minQuotaCooldownFloor
		}
		return now.Add(duration).Round(0)
	}
	// An unrelated open quota window may be longer than this overage failure.
	// Keep the scheduler's reuse policy, but never attribute its deadline here.
	quota.NextRecoverAt = time.Time{}
	next, _ := quotaCooldownAfterFailure(quota, now)
	return next
}

func (m *Manager) recordClaudeOverage(ctx context.Context, a *Auth, result Result, model string, headers http.Header, now time.Time) {
	m.recordClaudeFablePool(ctx, a, result, model, headers, now)
	if result.Success || a == nil || !strings.EqualFold(strings.TrimSpace(a.Provider), "claude") || statusCodeFromResult(result.Error) != 429 || m.cooldownDisabledForAuth(a) || hasUnauthorizedAuthFailure(a) || shouldSkipCredentialCooldown(result.Error) || (result.Error != nil && result.Error.Code == ErrorCodeForceCooldown) || isTransientRateLimitResultError(result.Error) || !claudeOverageIdentityMatches(ctx, a) || !claudeOverageHeaders(headers) {
		return
	}
	if model != "" {
		state := ensureModelState(a, model)
		next := claudeOverageOwnDeadline(state.Quota, result.RetryAfter, now)
		if next.After(state.claudeOverageCooldownUntil) {
			state.claudeOverageCooldownUntil = next
		}
	} else {
		next := claudeOverageOwnDeadline(a.Quota, result.RetryAfter, now)
		if next.After(a.claudeOverageCooldownUntil) {
			a.claudeOverageCooldownUntil = next
		}
	}
}

func (m *Manager) recordClaudeFablePool(ctx context.Context, a *Auth, result Result, model string, headers http.Header, now time.Time) {
	if result.Success || a == nil || !strings.EqualFold(strings.TrimSpace(a.Provider), "claude") || statusCodeFromResult(result.Error) != 429 || m.cooldownDisabledForAuth(a) || hasUnauthorizedAuthFailure(a) || shouldSkipCredentialCooldown(result.Error) || (result.Error != nil && result.Error.Code == ErrorCodeForceCooldown) || isTransientRateLimitResultError(result.Error) || !claudeOverageIdentityMatches(ctx, a) || !claudeFablePoolHeaders(headers) {
		return
	}
	if model != "" {
		state := ensureModelState(a, model)
		next := claudeOverageOwnDeadline(state.Quota, result.RetryAfter, now)
		if next.After(state.claudeFablePoolCooldownUntil) {
			state.claudeFablePoolCooldownUntil = next
		}
	} else {
		next := claudeOverageOwnDeadline(a.Quota, result.RetryAfter, now)
		if next.After(a.claudeFablePoolCooldownUntil) {
			a.claudeFablePoolCooldownUntil = next
		}
	}
}

func sameClaudeOverageAccount(a, b *Auth) bool {
	if a == nil || b == nil || !strings.EqualFold(strings.TrimSpace(a.Provider), "claude") || !strings.EqualFold(a.Provider, b.Provider) {
		return false
	}
	known := false
	for _, key := range []string{"account_id", "email", "sub", "account_uuid", "organization_uuid"} {
		left, _ := a.Metadata[key].(string)
		right, _ := b.Metadata[key].(string)
		if left != right {
			return false
		}
		known = known || strings.TrimSpace(left) != ""
	}
	return known || !CredentialsChanged(a, b)
}

// Preserve only the account-owned overage marker. Other update semantics remain
// with the existing credential lifecycle and cooldown merge paths.
func retainClaudeOverageUpdate(incoming, current *Auth) bool {
	same := sameClaudeOverageAccount(incoming, current)
	if incoming == nil || current == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(incoming.Provider), "claude") && !strings.EqualFold(strings.TrimSpace(current.Provider), "claude") {
		return false
	}
	changed := retainClaudeFablePoolUpdate(incoming, current)
	before := incoming.claudeOverageCooldownUntil
	if !same {
		incoming.claudeOverageCooldownUntil = time.Time{}
	} else if current.claudeOverageCooldownUntil.After(before) {
		incoming.claudeOverageCooldownUntil = current.claudeOverageCooldownUntil
	}
	changed = changed || !before.Equal(incoming.claudeOverageCooldownUntil)
	incoming.ModelStates = incoming.Clone().ModelStates
	for model, state := range incoming.ModelStates {
		if state == nil {
			continue
		}
		old := current.ModelStates[model]
		before := state.claudeOverageCooldownUntil
		if !same {
			state.claudeOverageCooldownUntil = time.Time{}
		} else if old != nil && old.claudeOverageCooldownUntil.After(before) {
			state.claudeOverageCooldownUntil = old.claudeOverageCooldownUntil
		}
		changed = changed || !before.Equal(state.claudeOverageCooldownUntil)
		if state.credentialQuotaShadow != nil {
			shadow := state.credentialQuotaShadow
			before = shadow.claudeOverageCooldownUntil
			if !same {
				shadow.claudeOverageCooldownUntil = time.Time{}
			} else if old != nil && old.credentialQuotaShadow != nil && old.credentialQuotaShadow.claudeOverageCooldownUntil.After(before) {
				shadow.claudeOverageCooldownUntil = old.credentialQuotaShadow.claudeOverageCooldownUntil
			}
			changed = changed || !before.Equal(shadow.claudeOverageCooldownUntil)
		}
	}
	return changed
}

func retainClaudeFablePoolUpdate(incoming, current *Auth) bool {
	same := sameClaudeOverageAccount(incoming, current)
	if incoming == nil || current == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(incoming.Provider), "claude") && !strings.EqualFold(strings.TrimSpace(current.Provider), "claude") {
		return false
	}
	changed := false
	before := incoming.claudeFablePoolCooldownUntil
	if !same {
		incoming.claudeFablePoolCooldownUntil = time.Time{}
	} else if current.claudeFablePoolCooldownUntil.After(before) {
		incoming.claudeFablePoolCooldownUntil = current.claudeFablePoolCooldownUntil
	}
	changed = changed || !before.Equal(incoming.claudeFablePoolCooldownUntil)
	incoming.ModelStates = incoming.Clone().ModelStates
	for model, state := range incoming.ModelStates {
		if state == nil {
			continue
		}
		old := current.ModelStates[model]
		before := state.claudeFablePoolCooldownUntil
		if !same {
			state.claudeFablePoolCooldownUntil = time.Time{}
		} else if old != nil && old.claudeFablePoolCooldownUntil.After(before) {
			state.claudeFablePoolCooldownUntil = old.claudeFablePoolCooldownUntil
		}
		changed = changed || !before.Equal(state.claudeFablePoolCooldownUntil)
		if state.credentialQuotaShadow != nil {
			shadow := state.credentialQuotaShadow
			before = shadow.claudeFablePoolCooldownUntil
			if !same {
				shadow.claudeFablePoolCooldownUntil = time.Time{}
			} else if old != nil && old.credentialQuotaShadow != nil && old.credentialQuotaShadow.claudeFablePoolCooldownUntil.After(before) {
				shadow.claudeFablePoolCooldownUntil = old.credentialQuotaShadow.claudeFablePoolCooldownUntil
			}
			changed = changed || !before.Equal(shadow.claudeFablePoolCooldownUntil)
		}
	}
	return changed
}

func claudeOverageUtilizationHealthy(raw string) bool {
	value, err := strconv.ParseFloat(raw, 64)
	return err == nil && value >= 0 && value < 1
}

type claudeOverageIdentityKey struct{}
type claudeOverageIdentity struct {
	account [5]string
	known   bool
	version uint64
}

func claudeOverageIdentityFor(a *Auth) claudeOverageIdentity {
	identity := claudeOverageIdentity{version: a.CredentialVersion}
	for i, key := range []string{"account_id", "email", "sub", "account_uuid", "organization_uuid"} {
		identity.account[i], _ = a.Metadata[key].(string)
		identity.known = identity.known || strings.TrimSpace(identity.account[i]) != ""
	}
	return identity
}
func withClaudeOverageIdentity(ctx context.Context, a *Auth) context.Context {
	if a == nil || !strings.EqualFold(strings.TrimSpace(a.Provider), "claude") {
		return ctx
	}
	return context.WithValue(ctx, claudeOverageIdentityKey{}, claudeOverageIdentityFor(a))
}
func claudeOverageIdentityMatches(ctx context.Context, a *Auth) bool {
	identity, known := ctx.Value(claudeOverageIdentityKey{}).(claudeOverageIdentity)
	if !known {
		return true
	}
	current := claudeOverageIdentityFor(a)
	return identity.account == current.account && (identity.known || identity.version == current.version)
}

func claudeOverageSignals(signals map[string]string) bool {
	headers := make(http.Header, len(signals))
	for key, value := range signals {
		headers.Set(key, value)
	}
	return claudeOverageHeaders(headers)
}

// A standalone pool rejection has no paid-overage attribution. Explicit paid
// overage keeps its existing, independently validated extra_usage contract.
func claudeFablePoolHeaders(headers http.Header) bool {
	signals := collectQuotaSignals("claude", headers)
	return strings.EqualFold(strings.TrimSpace(signals[http.CanonicalHeaderKey("Anthropic-Ratelimit-Unified-7d_oi-Status")]), "rejected") && !claudeOverageHeaders(headers)
}
func claudeFablePoolSignals(signals map[string]string) bool {
	headers := make(http.Header, len(signals))
	for key, value := range signals {
		headers.Set(key, value)
	}
	return claudeFablePoolHeaders(headers)
}
