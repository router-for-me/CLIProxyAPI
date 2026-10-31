package auth

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const requestRefreshMargin = 60 * time.Second

// PluginRefreshPolicyStorageKey is reserved for policies restored by plugin parsers.
const PluginRefreshPolicyStorageKey = "_cliproxy_refresh_policy"

func syncPluginRefreshPolicy(auth *Auth) {
	if auth.RefreshPolicy == "" || auth.Metadata == nil {
		return
	}
	if auth.RefreshPolicy == pluginapi.RefreshOnDemand {
		auth.Metadata[PluginRefreshPolicyStorageKey] = string(pluginapi.RefreshOnDemand)
	} else {
		delete(auth.Metadata, PluginRefreshPolicyStorageKey)
	}
}

type managedRefreshAttempt struct {
	done chan struct{}
	base *Auth
	err  error
}

type pendingAuthRotation struct {
	base    *Auth
	updated *Auth
	err     error
}

// InvalidRefreshResultError marks an unusable result after a successful provider rotation.
// The previous refresh grant may have been consumed and must not be retried.
type InvalidRefreshResultError struct {
	Err error
}

func (e *InvalidRefreshResultError) Error() string { return e.Err.Error() }
func (e *InvalidRefreshResultError) Unwrap() error { return e.Err }

type managedRefreshError struct{ cause error }

func (e *managedRefreshError) Error() string   { return ExtractUpstreamErrorSummary(e.cause.Error()) }
func (e *managedRefreshError) Unwrap() error   { return e.cause }
func (e *managedRefreshError) StatusCode() int { return statusCodeFromError(e.cause) }

func safeManagedRefreshError(err error) error {
	if err == nil {
		return nil
	}
	var safe *managedRefreshError
	if errors.As(err, &safe) {
		return err
	}
	return &managedRefreshError{cause: err}
}

func (p *pendingAuthRotation) replacedBy(current *Auth) bool {
	if current == nil || authAccessToken(current) == "" || authRefreshToken(current) == "" {
		return false
	}
	return authRefreshToken(current) != authRefreshToken(p.base) &&
		(p.updated == nil || authRefreshToken(current) != authRefreshToken(p.updated))
}

type managedRefreshOutcome struct {
	base      *Auth
	err       error
	retryAt   time.Time
	voluntary bool
}

type strictRefreshPersistKey struct{}
type managedRefreshContextKey struct{}

// IsManagedRefreshContext identifies host-owned rotations whose physical result must be retained.
func IsManagedRefreshContext(ctx context.Context) bool {
	return ctx != nil && ctx.Value(managedRefreshContextKey{}) == true
}

func strictRefreshContext(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = WithoutSkipPersist(context.WithoutCancel(ctx))
	ctx = context.WithValue(ctx, managedRefreshContextKey{}, true)
	return context.WithValue(ctx, strictRefreshPersistKey{}, true)
}

func requiresStrictRefreshSave(ctx context.Context) bool {
	return ctx != nil && ctx.Value(strictRefreshPersistKey{}) == true
}

func requestRefreshDue(auth *Auth, now time.Time) bool {
	exp, known := auth.AccessTokenExpirationTime()
	return !known || exp.IsZero() || !exp.After(now.Add(requestRefreshMargin))
}

func requestRefreshable(auth *Auth) bool {
	if auth == nil || auth.RefreshPolicy != pluginapi.RefreshOnDemand || authAccessToken(auth) == "" || authRefreshToken(auth) == "" {
		return false
	}
	exp, known := auth.AccessTokenExpirationTime()
	return known && !exp.IsZero() && !hasUnauthorizedAuthFailure(auth) && !hasRejectedRequestToken(auth) && !authHasUnchangedRejectedRefreshToken(auth)
}

func hasRejectedRequestToken(auth *Auth) bool {
	return auth != nil && auth.RejectedAccessToken != "" && auth.RejectedAccessToken == authAccessToken(auth)
}

func sameRotationSource(base, current *Auth) bool {
	return base != nil && current != nil && base.ID == current.ID && base.Provider == current.Provider &&
		base.RegistrationEpoch == current.RegistrationEpoch && base.CredentialVersion == current.CredentialVersion &&
		base.FileName == current.FileName && base.Attributes[AttributePath] == current.Attributes[AttributePath] &&
		sameRotationCredentials(base, current)
}

func sameRotationCredentials(a, b *Auth) bool {
	if a == nil || b == nil || CredentialsChanged(a, b) {
		return false
	}
	aToken, bToken := *a, *b
	aToken.RejectedAccessToken, aToken.RejectedSIWCAccessToken = "", ""
	bToken.RejectedAccessToken, bToken.RejectedSIWCAccessToken = "", ""
	aExpiry, aKnown := aToken.AccessTokenExpirationTime()
	bExpiry, bKnown := bToken.AccessTokenExpirationTime()
	return aKnown == bKnown && aExpiry.Equal(bExpiry)
}

// Reloaded bytes are not a Save receipt for a consumed refresh token.
func (m *Manager) preserveCommittedAuthOnFileObservation(observed, previous *Auth) (*Auth, bool) {
	value, found := m.refreshLocks.Load(observed.ID)
	if !found || previous == nil {
		return observed, false
	}
	state := value.(*authRefreshLock)
	state.stateMu.Lock()
	defer state.stateMu.Unlock()
	var base, rotated *Auth
	if state.pending != nil {
		base, rotated = state.pending.base, state.pending.updated
	} else if state.active != nil {
		base = state.active.base
	}
	if base == nil {
		return observed, false
	}
	sameSource := observed.Provider == base.Provider && observed.FileName == base.FileName && observed.Attributes[AttributePath] == base.Attributes[AttributePath]
	replaced := !sameSource || !sameRotationCredentials(observed, base) && !sameRotationCredentials(observed, rotated)
	if replaced {
		if state.pending == nil || state.pending.replacedBy(observed) {
			state.pending, state.outcome = nil, nil
		}
		return observed, false
	}
	retained := previous.Clone()
	retained.RefreshPolicy = observed.RefreshPolicy
	if retained.Disabled || retained.Status == StatusDisabled || observed.Disabled || observed.Status == StatusDisabled {
		retained.Disabled, retained.Status = true, StatusDisabled
	}
	if observed.Quota.Exceeded && (observed.Quota.NextRecoverAt.IsZero() || observed.Quota.NextRecoverAt.After(time.Now())) {
		retained.Unavailable = true
		if !retained.Quota.Exceeded || observed.Quota.NextRecoverAt.IsZero() || observed.Quota.NextRecoverAt.After(retained.Quota.NextRecoverAt) {
			retained.Quota = observed.Quota.Clone()
		}
	}
	retained.ProxyURL, retained.Prefix, retained.Label = observed.ProxyURL, observed.Prefix, observed.Label
	return retained, true
}

func (m *Manager) authRefreshState(id string) *authRefreshLock {
	value, _ := m.refreshLocks.LoadOrStore(id, &authRefreshLock{})
	return value.(*authRefreshLock)
}

func (m *Manager) hasUnsettledRotation(id string) bool {
	value, found := m.refreshLocks.Load(id)
	return found && value.(*authRefreshLock).unsettled()
}

func (s *authRefreshLock) unsettled() bool {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.active != nil || s.pending != nil
}

func (s *authRefreshLock) hasPendingRotation() bool {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.pending != nil
}

func (s *authRefreshLock) cachedError(current *Auth) error {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.outcome != nil && sameRotationSource(s.outcome.base, current) && time.Now().Before(s.outcome.retryAt) {
		if s.outcome.voluntary && s.pending == nil && !requestRefreshDue(current, time.Now()) && !hasRejectedRequestToken(current) && !hasUnauthorizedAuthFailure(current) {
			return nil
		}
		return s.outcome.err
	}
	return nil
}

func (s *authRefreshLock) retireReplacedRotation(current *Auth) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.pending != nil && s.pending.replacedBy(current) {
		s.pending = nil
	}
	if s.outcome != nil && !sameRotationSource(s.outcome.base, current) {
		s.outcome = nil
	}
}

func (m *Manager) durableRequestRefresh(auth *Auth) error {
	if m.HomeEnabled() || auth == nil || auth.Metadata == nil || auth.Storage == nil || IsConfigAPIKeyAuth(auth) || IsPluginVirtualAuth(auth) || strings.EqualFold(auth.Attributes["runtime_only"], "true") {
		return errors.New("on-demand refresh requires a durable file-backed plugin credential")
	}
	m.mu.RLock()
	store := m.store
	m.mu.RUnlock()
	fileStore, ok := store.(interface{ OnDemandRefreshPath(*Auth) (string, error) })
	if !ok || fileStore == nil {
		return errors.New("on-demand refresh requires a file token store")
	}
	path, err := fileStore.OnDemandRefreshPath(auth)
	if err != nil {
		return err
	}
	if path == "" {
		return errors.New("on-demand refresh has no durable auth file")
	}
	return nil
}

// EnsureRequestAuth returns the current committed credential before authenticated dispatch.
// Opt-out credentials retain their existing lifecycle. Managed failures stop dispatch.
func (m *Manager) EnsureRequestAuth(ctx context.Context, selected *Auth, model string) (*Auth, error) {
	if selected == nil || m == nil {
		return selected, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	current, found := m.auths[selected.ID]
	value, hasState := m.refreshLocks.Load(selected.ID)
	managed := selected.RefreshPolicy == pluginapi.RefreshOnDemand || found && current.RefreshPolicy == pluginapi.RefreshOnDemand || hasState && value.(*authRefreshLock).unsettled()
	if !managed {
		m.mu.RUnlock()
		return selected, nil
	}
	state := m.authRefreshState(selected.ID)
	if found {
		state.retireReplacedRotation(current)
	}
	if selected.RefreshPolicy != pluginapi.RefreshOnDemand && found && current.RefreshPolicy != pluginapi.RefreshOnDemand && !state.unsettled() {
		current = current.Clone()
		m.mu.RUnlock()
		return current, nil
	}
	current = current.Clone()
	m.mu.RUnlock()

	if !found || current.Provider != selected.Provider || selected.RegistrationEpoch != 0 && current.RegistrationEpoch != selected.RegistrationEpoch {
		return nil, errors.New("request auth registration changed")
	}
	if !current.Disabled && current.Status != StatusDisabled {
		if err := state.cachedError(current); err != nil {
			return nil, err
		}
	}
	checkModel := m.selectionModelForAuth(current, model)
	if blocked, _, _ := isAuthBlockedWithoutExpiry(current, checkModel, time.Now()); blocked || hasLiveQuotaCooldown(current, checkModel, time.Now()) || hasRejectedRequestToken(current) {
		return nil, &Error{Code: "auth_unavailable", Message: "request auth is unavailable"}
	}
	if state.unsettled() || current.RefreshPolicy == pluginapi.RefreshOnDemand && requestRefreshDue(current, time.Now()) {
		fresh, err := m.managedRefresh(ctx, current, false, "")
		if err != nil {
			return nil, err
		}
		current = fresh
	}
	checkModel = m.selectionModelForAuth(current, model)
	if blocked, _, _ := isAuthBlockedForModel(current, checkModel, time.Now()); blocked || hasLiveQuotaCooldown(current, checkModel, time.Now()) || hasRejectedRequestToken(current) {
		return nil, &Error{Code: "auth_unavailable", Message: "request auth became unavailable"}
	}
	if current.RefreshPolicy == pluginapi.RefreshOnDemand && (authAccessToken(current) == "" || requestRefreshDue(current, time.Now())) {
		return nil, errors.New("plugin refresh did not produce usable access credentials")
	}
	if model != "" && !m.authSupportsRouteModel(registry.GetGlobalRegistry(), current, model) {
		return nil, &Error{Code: "auth_unavailable", Message: "request auth no longer supports the model"}
	}
	return current, nil
}

func (m *Manager) ensureExecutionAuth(ctx context.Context, selected *Auth, model, provider string) (*Auth, error) {
	managed := selected.RefreshPolicy == pluginapi.RefreshOnDemand || m.hasUnsettledRotation(selected.ID)
	current, err := m.EnsureRequestAuth(ctx, selected, model)
	if err != nil {
		return nil, err
	}
	if !m.HomeEnabled() && (managed || current.RefreshPolicy == pluginapi.RefreshOnDemand) && canonicalSchedulingProvider(executorKeyFromAuth(current)) != canonicalSchedulingProvider(provider) {
		return nil, &Error{Code: "auth_route_changed", Message: "request auth executor route changed before dispatch"}
	}
	return current, nil
}

func (m *Manager) managedRefresh(ctx context.Context, base *Auth, force bool, failedToken string) (*Auth, error) {
	state := m.authRefreshState(base.ID)
	state.stateMu.Lock()
	attempt := state.active
	if attempt != nil && !sameRotationSource(attempt.base, base) && !force && failedToken == "" && authAccessToken(base) != "" && authRefreshToken(base) != "" && authRefreshToken(base) != authRefreshToken(attempt.base) && !requestRefreshDue(base, time.Now()) {
		state.stateMu.Unlock()
		current, found := m.GetByID(base.ID)
		if found && sameRotationSource(base, current) {
			return current, nil
		}
		return nil, errors.New("request auth changed while checking replacement")
	}
	if attempt == nil {
		if state.outcome != nil && sameRotationSource(state.outcome.base, base) && time.Now().Before(state.outcome.retryAt) && !force {
			err := state.outcome.err
			state.stateMu.Unlock()
			return nil, err
		}
		attempt = &managedRefreshAttempt{done: make(chan struct{}), base: base.Clone()}
		state.active = attempt
		go func() {
			state.mu.Lock()
			_, err := m.performManagedRefresh(strictRefreshContext(ctx), state, attempt.base, force, failedToken)
			state.mu.Unlock()
			cause := err
			err = safeManagedRefreshError(err)
			state.stateMu.Lock()
			attempt.err = err
			if err != nil {
				voluntary := force && failedToken == "" && !requestRefreshDue(attempt.base, time.Now()) && !isTerminalRefreshError(attempt.base, cause) && !isInvalidGrantError(cause)
				state.outcome = &managedRefreshOutcome{base: attempt.base, err: err, retryAt: time.Now().Add(refreshFailureBackoff), voluntary: voluntary}
			} else {
				state.outcome = nil
			}
			state.active = nil
			close(attempt.done)
			state.stateMu.Unlock()
		}()
	}
	sameAttempt := sameRotationSource(attempt.base, base)
	state.stateMu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-attempt.done:
		if !sameAttempt {
			current, found := m.GetByID(base.ID)
			if !found || current.Provider != base.Provider || current.RegistrationEpoch != base.RegistrationEpoch {
				return nil, errors.New("request auth registration changed during refresh")
			}
			return m.managedRefresh(ctx, current, force, failedToken)
		}
		if attempt.err != nil {
			return nil, attempt.err
		}
		current, found := m.GetByID(base.ID)
		if !found || current.Provider != base.Provider || current.RegistrationEpoch != base.RegistrationEpoch {
			return nil, errors.New("request auth registration changed during refresh")
		}
		return current, nil
	}
}

func (m *Manager) performManagedRefresh(ctx context.Context, state *authRefreshLock, base *Auth, force bool, failedToken string) (*Auth, error) {
	current, found := m.GetByID(base.ID)
	if !found || current.Provider != base.Provider || current.RegistrationEpoch != base.RegistrationEpoch || current.FileName != base.FileName || current.Attributes[AttributePath] != base.Attributes[AttributePath] {
		return nil, errors.New("credential changed before managed refresh")
	}
	state.retireReplacedRotation(current)
	state.stateMu.Lock()
	pending := state.pending
	state.stateMu.Unlock()
	if pending != nil && pending.err != nil {
		return nil, pending.err
	}
	if !sameRotationSource(base, current) {
		if !force && !state.hasPendingRotation() && !requestRefreshDue(current, time.Now()) && !hasRejectedRequestToken(current) {
			return current, nil
		}
		return nil, errors.New("credential changed before managed refresh")
	}
	if pending != nil {
		if !sameRotationSource(pending.base, current) {
			return nil, errors.New("credential changed before rotation persistence")
		}
		return m.commitManagedRotation(ctx, state, pending)
	}
	if authHasUnchangedRejectedRefreshToken(current) {
		if current.RefreshError != nil {
			return nil, current.RefreshError
		}
		return nil, errors.New("refresh token requires sign-in")
	}
	if failedToken != "" && authAccessToken(current) != failedToken && !requestRefreshDue(current, time.Now()) {
		return current, nil
	}
	if !force && failedToken == "" && !requestRefreshDue(current, time.Now()) {
		return current, nil
	}
	if !force && (current.Disabled || current.Status == StatusDisabled || hasUnauthorizedAuthFailure(current)) {
		return nil, errors.New("managed refresh credential is unavailable")
	}
	if !force && failedToken == "" && !requestRefreshable(current) {
		return nil, errors.New("managed request refresh requires known expiry and usable refresh credentials")
	}
	if err := m.durableRequestRefresh(current); err != nil {
		return nil, err
	}
	if authAccessToken(current) == "" || authRefreshToken(current) == "" {
		return nil, errors.New("managed refresh requires access and refresh tokens")
	}
	exec := m.executorFor(executorKeyFromAuth(current))
	if exec == nil {
		return nil, errors.New("managed refresh executor not found")
	}
	updated, err := exec.Refresh(ctx, current.Clone())
	if err != nil {
		var invalid *InvalidRefreshResultError
		if errors.As(err, &invalid) {
			err = safeManagedRefreshError(err)
			state.stateMu.Lock()
			state.pending = &pendingAuthRotation{base: current.Clone(), err: err}
			state.stateMu.Unlock()
			return nil, err
		}
		m.recordManagedRefreshFailure(current, err)
		return nil, err
	}
	if updated == nil || updated.ID != current.ID || updated.Provider != current.Provider || authAccessToken(updated) == "" || authRefreshToken(updated) == "" || requestRefreshDue(updated, time.Now()) || hasRejectedRequestToken(updated) || contradictoryRequestBearer(updated) {
		err = &InvalidRefreshResultError{Err: errors.New("plugin refresh did not produce usable access credentials")}
		state.stateMu.Lock()
		state.pending = &pendingAuthRotation{base: current.Clone(), updated: updated.Clone(), err: err}
		state.stateMu.Unlock()
		return nil, err
	}
	updated.LastRefreshedAt = time.Now()
	updated.NextRefreshAfter = time.Time{}
	pending = &pendingAuthRotation{base: current.Clone(), updated: updated.Clone()}
	state.stateMu.Lock()
	state.pending = pending
	state.stateMu.Unlock()
	latest, exists := m.GetByID(current.ID)
	if !exists || !sameRotationSource(current, latest) {
		return nil, errors.New("credential replaced during managed refresh")
	}
	return m.commitManagedRotation(ctx, state, pending)
}

func contradictoryRequestBearer(auth *Auth) bool {
	if bearer, supplied := auth.Attributes["api_key"]; supplied && bearer != authAccessToken(auth) {
		return true
	}
	if bearer, supplied := auth.Metadata["api_key"]; supplied && bearer != authAccessToken(auth) {
		return true
	}
	return false
}

func (m *Manager) commitManagedRotation(ctx context.Context, state *authRefreshLock, pending *pendingAuthRotation) (*Auth, error) {
	saved, err := m.UpdateRefreshedAuth(ctx, pending.base.Clone(), pending.updated.Clone())
	if err != nil {
		return nil, err
	}
	if saved == nil {
		return nil, errors.New("credential removed during rotation persistence")
	}
	if saved.RegistrationEpoch != pending.base.RegistrationEpoch || !sameRotationCredentials(pending.updated, saved) {
		state.stateMu.Lock()
		if pending.replacedBy(saved) {
			state.pending = nil
		}
		state.stateMu.Unlock()
		return nil, errors.New("credential changed during rotation persistence")
	}
	state.stateMu.Lock()
	state.pending = nil
	state.stateMu.Unlock()
	return saved, nil
}

func (m *Manager) recordManagedRefreshFailure(base *Auth, err error) {
	release := m.lockAuthMutation(base.ID)
	defer release()
	m.mu.Lock()
	if current := m.auths[base.ID]; sameRotationSource(base, current) {
		now := time.Now()
		terminal := isTerminalRefreshError(current, err) || isInvalidGrantError(err)
		diagnostic := sanitizeRenewalError(current, err)
		diagnostic.Retryable = !terminal
		if terminal || !authHasAnyRejectedRefreshPartition(current) {
			current.RefreshError = diagnostic
		}
		if terminal {
			current.RequiresSignIn = true
			current.RenewalVerified = false
			current.LastRenewalVerifiedAt = time.Time{}
			recordRejectedRefreshTokens(current, err)
			if current.RenewalEventAt.IsZero() {
				current.RenewalEventAt = now
			}
			current.LastError = &Error{Code: "unauthorized", Message: diagnostic.Message, HTTPStatus: 401}
			current.Unavailable = true
			if current.Disabled || current.Status == StatusDisabled {
				current.Status = StatusDisabled
			} else {
				current.Status = StatusError
			}
			current.StatusMessage = "unauthorized (refresh token invalid)"
			current.NextRefreshAfter = time.Time{}
			current.NextRetryAfter = time.Time{}
			current.RefreshFailures = 0
		} else {
			current.NextRefreshAfter = now.Add(refreshFailureBackoff)
		}
		current.UpdatedAt = now
		current.Generation++
		if m.scheduler != nil {
			m.scheduler.upsertAuth(current.Clone())
		}
	}
	m.mu.Unlock()
}

func (m *Manager) isAuthBlockedForRequest(auth *Auth, model string, now time.Time) (bool, blockReason, time.Time) {
	if auth != nil && (auth.RefreshPolicy == pluginapi.RefreshOnDemand || m.hasUnsettledRotation(auth.ID)) && hasLiveQuotaCooldown(auth, model, now) {
		return true, blockReasonCooldown, auth.Quota.NextRecoverAt
	}
	if auth != nil && m.hasUnsettledRotation(auth.ID) && authAccessToken(auth) != "" && authRefreshToken(auth) != "" && !hasRejectedRequestToken(auth) {
		return isAuthBlockedWithoutExpiry(auth, model, now)
	}
	return isAuthBlockedForDemand(auth, model, now)
}

func (m *Manager) managedRecoveryError(auth *Auth) error {
	state, found := m.refreshLocks.Load(auth.ID)
	if !found {
		return nil
	}
	current, found := m.GetByID(auth.ID)
	if !found {
		return nil
	}
	return state.(*authRefreshLock).cachedError(current)
}

type requestDemandSelectionKey struct{}

func requestDemandSelection(ctx context.Context) bool {
	return ctx != nil && ctx.Value(requestDemandSelectionKey{}) == true
}

func (m *Manager) pickNextMixedForExecution(ctx context.Context, providers []string, model string, opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, ProviderExecutor, string, error) {
	if m.HomeEnabled() {
		return m.pickNextMixed(ctx, providers, model, opts, tried)
	}
	providerSet := make(map[string]bool, len(providers))
	for _, provider := range providers {
		providerSet[canonicalSchedulingProvider(provider)] = true
	}
	m.mu.RLock()
	managed := false
	for _, candidate := range m.auths {
		if candidate != nil && providerSet[canonicalSchedulingProvider(executorKeyFromAuth(candidate))] && (candidate.RefreshPolicy == pluginapi.RefreshOnDemand || m.hasUnsettledRotation(candidate.ID)) {
			managed = true
			break
		}
	}
	m.mu.RUnlock()
	if !managed {
		return m.pickNextMixed(ctx, providers, model, opts, tried)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return m.pickNextMixedLegacy(context.WithValue(ctx, requestDemandSelectionKey{}, true), providers, model, opts, tried)
}
