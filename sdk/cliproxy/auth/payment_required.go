package auth

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func paymentRequiredAction(cfg *internalconfig.Config) string {
	if cfg == nil {
		return "cooldown"
	}
	return cfg.QuotaExceeded.PaymentRequiredAction()
}

func paymentRequiredActionForAuth(cfg *internalconfig.Config, auth *Auth) string {
	action := paymentRequiredAction(cfg)
	if action == "disable" && IsPluginVirtualAuth(auth) {
		// Virtual children cannot be persisted or managed independently from their
		// plugin-owned source file, so retain the safe cooldown behavior.
		return "cooldown"
	}
	return action
}

func isPaymentRequiredDisabled(auth *Auth) bool {
	if auth == nil || (!auth.Disabled && auth.Status != StatusDisabled) || auth.Metadata == nil {
		return false
	}
	reason, _ := auth.Metadata["disabled_reason"].(string)
	return reason == "payment_required"
}

// IsPaymentRequiredDisabled reports whether an auth is disabled specifically
// because a request returned HTTP 402.
func IsPaymentRequiredDisabled(auth *Auth) bool {
	return isPaymentRequiredDisabled(auth)
}

type authMutationLock struct {
	mu   sync.Mutex
	refs int
}

func (m *Manager) lockAuthMutation(authID string) func() {
	if m == nil || strings.TrimSpace(authID) == "" {
		return func() {}
	}
	m.authMutationLocksMu.Lock()
	lockValue, _ := m.authMutationLocks.LoadOrStore(authID, &authMutationLock{})
	lock := lockValue.(*authMutationLock)
	lock.refs++
	m.authMutationLocksMu.Unlock()

	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		m.authMutationLocksMu.Lock()
		lock.refs--
		if lock.refs == 0 {
			m.authMutationLocks.CompareAndDelete(authID, lock)
		}
		m.authMutationLocksMu.Unlock()
	}
}

type credentialMaintenanceUpdateContextKey struct{}

type credentialMaintenanceUpdateState struct {
	sourcePaymentRequiredDisabled bool
	sourceRuntime                 *Auth
}

func withCredentialMaintenanceUpdate(ctx context.Context, source *Auth) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	var sourceRuntime *Auth
	if source != nil {
		sourceRuntime = source.Clone()
	}
	return context.WithValue(ctx, credentialMaintenanceUpdateContextKey{}, credentialMaintenanceUpdateState{
		sourcePaymentRequiredDisabled: isPaymentRequiredDisabled(source),
		sourceRuntime:                 sourceRuntime,
	})
}

func credentialMaintenanceStateFromContext(ctx context.Context) (credentialMaintenanceUpdateState, bool) {
	if ctx == nil {
		return credentialMaintenanceUpdateState{}, false
	}
	state, ok := ctx.Value(credentialMaintenanceUpdateContextKey{}).(credentialMaintenanceUpdateState)
	return state, ok
}

func isCredentialMaintenanceUpdate(ctx context.Context) bool {
	_, ok := credentialMaintenanceStateFromContext(ctx)
	return ok
}

func credentialMaintenanceModelStatesEqual(a, b map[string]*ModelState) bool {
	if len(a) != len(b) {
		return false
	}
	for model, aState := range a {
		bState, ok := b[model]
		if !ok {
			return false
		}
		if aState == nil || bState == nil {
			if aState != bState {
				return false
			}
			continue
		}
		if aState.Status != bState.Status ||
			aState.StatusMessage != bState.StatusMessage ||
			aState.Unavailable != bState.Unavailable ||
			!aState.NextRetryAfter.Equal(bState.NextRetryAfter) ||
			!aState.UpdatedAt.Equal(bState.UpdatedAt) ||
			!cooldownQuotaEqual(aState.Quota, bState.Quota) ||
			!cooldownErrorEqual(aState.LastError, bState.LastError) {
			return false
		}
	}
	return true
}

func credentialMaintenanceRuntimeEqual(source, current *Auth) bool {
	if source == nil || current == nil {
		return source == current
	}
	return source.Disabled == current.Disabled &&
		source.Unavailable == current.Unavailable &&
		source.Status == current.Status &&
		source.StatusMessage == current.StatusMessage &&
		source.NextRetryAfter.Equal(current.NextRetryAfter) &&
		cooldownQuotaEqual(source.Quota, current.Quota) &&
		cooldownErrorEqual(source.LastError, current.LastError) &&
		credentialMaintenanceModelStatesEqual(source.ModelStates, current.ModelStates)
}

func copyAuthoritativeRuntimeState(existing, incoming *Auth) {
	if existing == nil || incoming == nil {
		return
	}
	incoming.Disabled = existing.Disabled
	incoming.Unavailable = existing.Unavailable
	incoming.Status = existing.Status
	incoming.StatusMessage = existing.StatusMessage
	incoming.NextRetryAfter = existing.NextRetryAfter
	currentRuntime := existing.Clone()
	incoming.ModelStates = currentRuntime.ModelStates
	incoming.Quota = currentRuntime.Quota
	incoming.LastError = currentRuntime.LastError
}

func copyAuthoritativeDisabledMetadata(existing, incoming *Auth) {
	if incoming.Metadata == nil {
		incoming.Metadata = make(map[string]any)
	}
	incoming.Metadata["disabled"] = existing.Disabled
	if existing.Disabled || existing.Status == StatusDisabled {
		if existing.Metadata != nil {
			if reason, ok := existing.Metadata["disabled_reason"]; ok {
				incoming.Metadata["disabled_reason"] = reason
				return
			}
		}
	}
	delete(incoming.Metadata, "disabled_reason")
}

// preservePaymentRequiredDisableForMaintenance prevents stale credential
// maintenance from crossing an authoritative disable/re-enable transition.
// Config reloads and management updates remain authoritative because they do
// not carry the maintenance-update context marker.
func preservePaymentRequiredDisableForMaintenance(ctx context.Context, existing, incoming *Auth) {
	if incoming == nil {
		return
	}
	if isPaymentRequiredDisabled(existing) {
		copyAuthoritativeRuntimeState(existing, incoming)
		incoming.Disabled = true
		incoming.Status = StatusDisabled
		if incoming.Metadata == nil {
			incoming.Metadata = make(map[string]any)
		}
		incoming.Metadata["disabled"] = true
		incoming.Metadata["disabled_reason"] = "payment_required"
		return
	}
	state, ok := credentialMaintenanceStateFromContext(ctx)
	if !ok {
		return
	}
	if !state.sourcePaymentRequiredDisabled && credentialMaintenanceRuntimeEqual(state.sourceRuntime, existing) {
		return
	}
	// Maintenance started before an authoritative lifecycle or runtime-state
	// change. Preserve the current runtime fields while retaining refreshed
	// credential metadata from the maintenance result.
	copyAuthoritativeRuntimeState(existing, incoming)
	copyAuthoritativeDisabledMetadata(existing, incoming)
}

func clearPaymentRequiredModelCooldowns(auth *Auth, now time.Time) []string {
	if auth == nil || len(auth.ModelStates) == 0 {
		return nil
	}
	models := make([]string, 0, len(auth.ModelStates))
	for model, state := range auth.ModelStates {
		if state == nil || statusCodeFromResult(state.LastError) != http.StatusPaymentRequired {
			continue
		}
		resetModelState(state, now)
		models = append(models, model)
	}
	return models
}

func clearPaymentRequiredRuntimeState(auth *Auth, now time.Time) ([]string, bool) {
	if auth == nil {
		return nil, false
	}
	models := clearPaymentRequiredModelCooldowns(auth, now)
	changed := len(models) > 0
	if statusCodeFromResult(auth.LastError) == http.StatusPaymentRequired {
		auth.Unavailable = false
		auth.NextRetryAfter = time.Time{}
		auth.LastError = nil
		auth.UpdatedAt = now
		changed = true
	}
	if len(models) > 0 {
		updateAggregatedAvailability(auth, now)
	}
	return models, changed
}

// disableAuthForPaymentRequired marks an auth disabled after HTTP 402.
// File-backed auths persist the metadata through the auth store. Config-backed
// API keys remain disabled until re-enabled or recreated by a config reload.
func disableAuthForPaymentRequired(auth *Auth, now time.Time, resultErr *Error) {
	if auth == nil {
		return
	}
	auth.Disabled = true
	auth.Status = StatusDisabled
	auth.StatusMessage = "payment_required"
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

// applyPaymentRequiredModelFailure updates model and auth state for HTTP 402.
// The disable mode is auth-level because payment balance applies to the whole
// credential. The model state remains soft so management re-enable is enough to
// restore routing. The return value requests registry suspension for cooldown only.
func applyPaymentRequiredModelFailure(auth *Auth, state *ModelState, now time.Time, resultErr *Error, disableCooling bool, action string) (shouldSuspendModel bool) {
	if action == "disable" {
		// The disable is credential-wide. Keep any existing per-model runtime
		// state intact so an explicit credential re-enable does not erase an
		// independent model cooldown or quota state.
		disableAuthForPaymentRequired(auth, now, resultErr)
		return false
	}

	if state != nil {
		state.Unavailable = true
		state.Status = StatusError
		state.UpdatedAt = now
		if resultErr != nil {
			state.LastError = cloneError(resultErr)
			state.StatusMessage = resultErr.Message
		}
	}

	statusMessage := "payment_required"
	if resultErr != nil && resultErr.Message != "" {
		statusMessage = resultErr.Message
	}
	if state != nil {
		state.StatusMessage = statusMessage
		if disableCooling {
			state.NextRetryAfter = time.Time{}
		} else {
			state.NextRetryAfter = now.Add(30 * time.Minute)
		}
	}
	if auth != nil {
		auth.Status = StatusError
		auth.StatusMessage = statusMessage
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
