package auth

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"time"
)

// ErrQuotaReconcileChanged means provider evidence must not clear the current state.
var ErrQuotaReconcileChanged = errors.New("quota reconciliation state changed or evidence expired")

// CanReconcileQuota deliberately excludes mixed failures: ResetQuota is an
// account-wide operation, so a usage check must not erase an unrelated block.
func CanReconcileQuota(a *Auth, now time.Time) bool {
	if a == nil || strings.TrimSpace(a.Provider) == "" || a.Disabled || a.Status == StatusDisabled ||
		(a.Provider == "codex" && IsCodexUseSIWC(a)) ||
		a.RequiresSignIn || a.RefreshError != nil || hasUnauthorizedAuthFailure(a) ||
		a.RejectedAccessToken != "" || a.RejectedRefreshToken != "" ||
		a.RejectedSIWCAccessToken != "" || a.RejectedSIWCRefreshToken != "" {
		return false
	}
	token, _ := a.Metadata["access_token"].(string)
	exp, known := a.AccessTokenExpirationTime()
	if token == "" || !known || !exp.After(now.Add(time.Minute)) {
		return false
	}
	if a.nonQuotaCooldownUntil.After(now) || a.credentialRetryAfter.After(now) || !quotaOnlyError(a.LastError) {
		return false
	}
	latest := time.Time{}
	if a.Quota.Exceeded {
		if a.Quota.Reason != "quota" && a.Quota.Reason != "credential_quota" {
			return false
		}
		latest = a.Quota.NextRecoverAt
	}
	for _, state := range a.ModelStates {
		if !reconcilableModelState(state, now, 0) {
			return false
		}
		if state != nil && state.Quota.Exceeded && state.Quota.NextRecoverAt.After(latest) {
			latest = state.Quota.NextRecoverAt
		}
	}
	if !latest.After(now) || a.NextRetryAfter.After(latest) || a.forceCooldownUntil.After(latest) {
		return false
	}
	return true
}

func quotaOnlyError(err *Error) bool {
	if err == nil {
		return true
	}
	// Older persisted failures may lack the newer request-scoped classification.
	// Subscription usage cannot establish that a separate paid balance recovered.
	message := strings.ToLower(err.Message)
	for _, marker := range []string{"usage credits", "credit balance", "extra usage", "billing", "payment"} {
		if strings.Contains(message, marker) {
			return false
		}
	}
	// The stock Claude statusErr supplies HTTP status and CredentialScope but
	// can legitimately leave Error.Code empty. Retain core billing/auth checks.
	return isQuotaCooldownResultError(err) && !isRequestScopedResultError(err)
}

func reconcilableModelState(s *ModelState, now time.Time, depth int) bool {
	if s == nil {
		return true
	}
	if depth > 1 || s.Status == StatusDisabled || !quotaOnlyError(s.LastError) ||
		s.nonQuotaCooldownUntil.After(now) {
		return false
	}
	blocked := s.Unavailable || s.Quota.Exceeded || s.NextRetryAfter.After(now) || s.forceCooldownUntil.After(now)
	if blocked && (!s.Quota.Exceeded || (s.Quota.Reason != "quota" && s.Quota.Reason != "credential_quota") ||
		s.NextRetryAfter.After(s.Quota.NextRecoverAt) || s.forceCooldownUntil.After(s.Quota.NextRecoverAt)) {
		return false
	}
	return reconcilableModelState(s.credentialQuotaShadow, now, depth+1)
}

// ReconcileQuota performs no inference. verify must check the same
// credential's non-spending provider usage, not redeem a reset or refresh a grant.
// No manager/mutation lock is held during provider I/O.
func (m *Manager) ReconcileQuota(ctx context.Context, id string, verify func(context.Context, *Auth) (bool, error)) (*Auth, []string, error) {
	if m == nil || verify == nil || m.HomeEnabled() {
		return nil, nil, nil
	}
	before, ok := m.GetByID(id)
	started := time.Now()
	if !ok || !CanReconcileQuota(before, started) {
		return nil, nil, nil
	}
	// Do not give a callback the comparison snapshot itself.
	recovered, err := verify(ctx, before.Clone())
	if err != nil || !recovered {
		return nil, nil, err
	}
	if errCtx := ctx.Err(); errCtx != nil {
		return nil, nil, errCtx
	}
	return m.resetQuota(ctx, id, before, started)
}

func quotaReconcileMatches(current, expected *Auth, started, now time.Time) bool {
	if started.IsZero() || now.Before(started) || now.Sub(started) > time.Minute ||
		!CanReconcileQuota(current, now) {
		return false
	}
	// Includes registration epoch, credential version, generation, tokens,
	// hidden cooldown layers and errors. Reject even same-deadline new failures.
	// Runtime/Storage are opaque executors, not routing/credential snapshots.
	a, b := current.Clone(), expected.Clone()
	a.Runtime, b.Runtime, a.Storage, b.Storage = nil, nil, nil, nil
	return reflect.DeepEqual(a, b)
}
