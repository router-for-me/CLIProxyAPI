package auth

import (
	"context"
	"errors"
	"reflect"
	"time"
)

// ErrQuotaReconcileChanged means provider evidence must not clear the current state.
var ErrQuotaReconcileChanged = errors.New("quota reconciliation state changed or evidence expired")

// CanReconcileClaudeQuota deliberately excludes mixed failures: ResetQuota is an
// account-wide operation, so a usage check must not erase an unrelated block.
func CanReconcileClaudeQuota(a *Auth, now time.Time) bool {
	if a == nil || a.Provider != "claude" || a.Disabled || a.Status == StatusDisabled ||
		a.RequiresSignIn || a.RefreshError != nil || hasUnauthorizedAuthFailure(a) ||
		a.RejectedAccessToken != "" || a.RejectedRefreshToken != "" ||
		a.RejectedSIWCAccessToken != "" || a.RejectedSIWCRefreshToken != "" {
		return false
	}
	token, _ := a.Metadata["access_token"].(string)
	exp, known := a.AccessTokenExpirationTime()
	if token == "" || !known || !exp.After(now.Add(time.Minute)) ||
		!a.Quota.Exceeded || a.Quota.Reason != "credential_quota" ||
		!a.Quota.NextRecoverAt.After(now) || a.nonQuotaCooldownUntil.After(now) ||
		a.credentialRetryAfter.After(now) || a.NextRetryAfter.After(a.Quota.NextRecoverAt) ||
		a.forceCooldownUntil.After(a.Quota.NextRecoverAt) || !quotaOnlyError(a.LastError) {
		return false
	}
	for _, state := range a.ModelStates {
		if !reconcilableModelState(state, now, 0) {
			return false
		}
	}
	return true
}

func quotaOnlyError(err *Error) bool {
	return err == nil || (err.HTTPStatus == 429 &&
		(err.Code == ErrorCodeCredentialQuota || err.Code == "rate_limit_error"))
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

// ReconcileClaudeQuota performs no inference. verify must check the same
// credential's non-spending provider usage, not redeem a reset or refresh a grant.
// No manager/mutation lock is held during provider I/O.
func (m *Manager) ReconcileClaudeQuota(ctx context.Context, id string, verify func(context.Context, *Auth) (bool, error)) (*Auth, []string, error) {
	if m == nil || verify == nil {
		return nil, nil, nil
	}
	before, ok := m.GetByID(id)
	started := time.Now()
	if !ok || !CanReconcileClaudeQuota(before, started) {
		return nil, nil, nil
	}
	// Do not give a callback the comparison snapshot itself.
	recovered, err := verify(ctx, before.Clone())
	if err != nil || !recovered {
		return nil, nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	return m.resetQuota(ctx, id, before, started)
}

func quotaReconcileMatches(current, expected *Auth, started, now time.Time) bool {
	if started.IsZero() || now.Before(started) || now.Sub(started) > time.Minute ||
		!CanReconcileClaudeQuota(current, now) {
		return false
	}
	// Includes registration epoch, credential version, generation, tokens,
	// hidden cooldown layers and errors. Reject even same-deadline new failures.
	// Runtime/Storage are opaque executors, not routing/credential snapshots.
	a, b := current.Clone(), expected.Clone()
	a.Runtime, b.Runtime, a.Storage, b.Storage = nil, nil, nil, nil
	return reflect.DeepEqual(a, b)
}
