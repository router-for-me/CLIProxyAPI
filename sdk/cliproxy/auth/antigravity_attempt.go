package auth

import (
	"context"
	"net/http"
	"strconv"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type antigravityModelStateContextKey struct{}

func (m *Manager) withAntigravityModelState(ctx context.Context, auth *Auth) context.Context {
	if auth == nil || auth.Provider != "antigravity" || !m.localExecutionAllowed() {
		return ctx
	}
	return context.WithValue(ctx, antigravityModelStateContextKey{}, m)
}

// CheckAntigravityTargetModel checks model-level admission for a late variant
// without reinterpreting credential-level admission or reopening the catalog.
func CheckAntigravityTargetModel(ctx context.Context, auth *Auth, model string, now time.Time, useCredits bool) error {
	if ctx != nil && auth != nil {
		if m, ok := ctx.Value(antigravityModelStateContextKey{}).(*Manager); ok {
			m.mu.RLock()
			defer m.mu.RUnlock()
			// The source model's scheduler shard may have an older copy of the
			// target state. Use the live state only for the same credential.
			if current := m.auths[auth.ID]; current != nil && current.RegistrationEpoch == auth.RegistrationEpoch && current.CredentialVersion == auth.CredentialVersion {
				auth = current
			}
		}
	}
	if state := existingModelState(auth, model); state != nil {
		blocked, _, next := availabilityBlock(state.Unavailable, state.Quota.Exceeded, state.NextRetryAfter, state.Quota.NextRecoverAt, now)
		if state.Status == StatusDisabled || (blocked && next.IsZero()) {
			return &Error{Message: "selected Antigravity model is unavailable", HTTPStatus: http.StatusServiceUnavailable}
		}
		if blocked && !(useCredits && antigravityCreditsAllowsTargetQuota(auth, state, now)) {
			return &trustedRetryAfterError{cause: newModelCooldownError(model, "antigravity", next.Sub(now)), deadline: next}
		}
	}
	return nil
}

// An admitted credits retry can pay for model quota, but not bypass an
// independent, forced, or credential-level cooldown on the selected variant.
func antigravityCreditsAllowsTargetQuota(auth *Auth, state *ModelState, now time.Time) bool {
	if auth.Disabled || auth.Status == StatusDisabled || hasUnauthorizedAuthFailure(auth) ||
		!state.Quota.Exceeded || state.Quota.Reason != "quota" || !state.Quota.NextRecoverAt.After(now) ||
		state.NextRetryAfter.After(state.Quota.NextRecoverAt) || state.nonQuotaCooldownUntil.After(now) ||
		effectiveModelForceUntil(state).After(now) || !isQuotaCooldownResultError(state.LastError) {
		return false
	}
	var retryAfter *time.Duration
	if hint := state.LastError.Metadata[antigravityCreditsRetryHintKey]; hint != "" {
		if nanos, errParse := strconv.ParseInt(hint, 10, 64); errParse == nil {
			delay := time.Duration(nanos)
			retryAfter = &delay
		}
	}
	return isAntigravityCreditsQuota429(state.LastError, retryAfter)
}

const antigravityCreditsRetryHintKey = "antigravity_credits_retry_hint_ns"

// Keep the upstream retry hint for the original admission rules; the remaining
// cooldown is not the same as the retry hint received when it began.
func recordAntigravityCreditsRetryHint(stateErr *Error, result Result) {
	if result.Provider != "antigravity" || result.RetryAfter == nil || !isQuotaCooldownResultError(stateErr) {
		return
	}
	if stateErr.Metadata == nil {
		stateErr.Metadata = make(map[string]string)
	}
	stateErr.Metadata[antigravityCreditsRetryHintKey] = strconv.FormatInt(int64(*result.RetryAfter), 10)
}

const antigravityTargetRejectedCode = "antigravity_target_rejected"

type antigravityTargetRejectedError struct{ error }

func (e antigravityTargetRejectedError) Unwrap() error { return e.error }

// RejectAntigravityTarget marks an already-blocked target as a routing miss,
// preserving the status and retry information without generating new feedback.
func RejectAntigravityTarget(err error) error {
	return antigravityTargetRejectedError{error: err}
}

func antigravityResultModel(ctx context.Context, result Result) string {
	if result.Provider != "antigravity" {
		return ""
	}
	return canonicalModelKey(cliproxyexecutor.UpstreamAttemptModel(ctx))
}

func antigravityTargetRejected(result Result) bool {
	return result.Provider == "antigravity" && result.Error != nil && result.Error.Code == antigravityTargetRejectedCode
}
