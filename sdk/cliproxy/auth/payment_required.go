package auth

import (
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// paymentRequiredAction returns the normalized 402 handling mode from config.
func paymentRequiredAction(cfg *internalconfig.Config) string {
	if cfg == nil {
		return "cooldown"
	}
	return cfg.QuotaExceeded.PaymentRequiredAction()
}

// disableAuthForPaymentRequired marks an auth permanently disabled after HTTP 402.
// File-backed auths persist Disabled via the auth store. Config-synthesized API-key
// auths cannot be persisted the same way; they stay disabled for the process lifetime
// until re-enabled or config reload re-creates them as active.
func disableAuthForPaymentRequired(auth *Auth, now time.Time, resultErr *Error) {
	if auth == nil {
		return
	}
	auth.Disabled = true
	auth.Unavailable = true
	auth.Status = StatusDisabled
	auth.StatusMessage = "payment_required"
	auth.NextRetryAfter = time.Time{}
	auth.UpdatedAt = now
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["disabled"] = true
	auth.Metadata["disabled_reason"] = "payment_required"
	if resultErr != nil {
		auth.LastError = cloneError(resultErr)
		if resultErr.Message != "" {
			auth.StatusMessage = resultErr.Message
		}
	}
}

// applyPaymentRequiredModelFailure updates per-model failure state for HTTP 402.
// When action is "disable", the whole auth is disabled (key/balance is auth-level).
// Model state stays StatusError (not StatusDisabled) so management re-enable can
// restore routing without a ResetQuota. Returns whether the model should also be
// suspended in the registry (cooldown only).
func applyPaymentRequiredModelFailure(auth *Auth, state *ModelState, now time.Time, resultErr *Error, disableCooling bool, action string) (shouldSuspendModel bool) {
	if state != nil {
		state.Unavailable = true
		state.UpdatedAt = now
		if resultErr != nil {
			state.LastError = cloneError(resultErr)
			state.StatusMessage = resultErr.Message
		}
	}
	if action == "disable" {
		disableAuthForPaymentRequired(auth, now, resultErr)
		if state != nil {
			// Auth-level disable only: do not hard-disable the model state so
			// management "enable" can restore the credential without ResetQuota.
			state.Status = StatusError
			if resultErr == nil || resultErr.Message == "" {
				state.StatusMessage = "payment_required"
			}
			state.NextRetryAfter = time.Time{}
		}
		return false
	}
	// Default cooldown path.
	if state != nil {
		state.Status = StatusError
		if disableCooling {
			state.NextRetryAfter = time.Time{}
		} else {
			state.NextRetryAfter = now.Add(30 * time.Minute)
		}
		state.StatusMessage = "payment_required"
	}
	if auth != nil {
		auth.Status = StatusError
		auth.StatusMessage = "payment_required"
		auth.UpdatedAt = now
		if resultErr != nil {
			auth.LastError = cloneError(resultErr)
		}
		if disableCooling {
			auth.NextRetryAfter = time.Time{}
		} else {
			auth.NextRetryAfter = now.Add(30 * time.Minute)
		}
	}
	return !disableCooling
}
