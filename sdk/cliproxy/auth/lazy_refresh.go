package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// RefreshPolicy is the plugin credential's host-managed refresh policy.
type RefreshPolicy = pluginapi.AuthRefreshPolicy

func (a *Auth) UsesLazyRefresh() bool {
	return a != nil && a.RefreshPolicy != nil && a.RefreshPolicy.Mode == pluginapi.AuthRefreshLazy
}

func lazyRefreshEligible(a *Auth) bool {
	return a.UsesLazyRefresh() && !a.Disabled && a.Status != StatusDisabled &&
		!hasUnauthorizedAuthFailure(a) && !a.RequiresSignIn &&
		!authHasAnyRejectedRefreshPartition(a)
}

func canCallLazyRefresh(executor ProviderExecutor, auth *Auth) bool {
	type pluginRefresher interface{ CanRefreshPluginAuth(*Auth) bool }
	if plugin, ok := executor.(pluginRefresher); ok {
		return plugin.CanRefreshPluginAuth(auth)
	}
	return authHasRefreshCredential(auth)
}

func accessExpiryBlocksSelection(ctx context.Context, auth *Auth, now time.Time, headers ...http.Header) bool {
	expiry, known := auth.AccessTokenExpirationTimeForRequest(ctx, headers...)
	return known && !expiry.IsZero() && !expiry.After(now) && !lazyRefreshEligible(auth)
}

func lazyRefreshDueForRequest(ctx context.Context, a *Auth, now time.Time) bool {
	return a.UsesLazyRefresh() && lazyAccessTokenDue(ctx, a, now, a.RefreshPolicy.ExpiryMarginSeconds)
}

func homeLazyRefreshDue(ctx context.Context, auth *Auth, headers ...http.Header) bool {
	if auth == nil {
		return false
	}
	var margin int64
	if auth.RefreshPolicy != nil {
		if !auth.UsesLazyRefresh() {
			return false
		}
		margin = auth.RefreshPolicy.ExpiryMarginSeconds
	} else {
		stored, _ := auth.Metadata["host_refresh_policy"].(map[string]any)
		if stored["mode"] != pluginapi.AuthRefreshLazy {
			return false
		}
		seconds, _ := parseIntAny(stored["expiry_margin_seconds"])
		margin = int64(seconds)
	}
	if margin < 0 || margin > 3600 {
		margin = 0
	}
	ctx = WithCodexClientRequest(ctx, IsCodexClientRequest(ctx, headers...))
	return lazyAccessTokenDue(ctx, auth, time.Now(), margin)
}

func lazyAccessTokenDue(ctx context.Context, auth *Auth, now time.Time, marginSeconds int64) bool {
	expiry, known := auth.AccessTokenExpirationTimeForRequest(ctx)
	return authAccessTokenForRequest(ctx, auth) == "" || known && !expiry.After(now.Add(time.Duration(marginSeconds)*time.Second))
}

func hasValidAccessTokenForRequest(ctx context.Context, auth *Auth, now time.Time) bool {
	if authAccessTokenForRequest(ctx, auth) == "" {
		return false
	}
	expiry, known := auth.AccessTokenExpirationTimeForRequest(ctx)
	return !known || expiry.After(now)
}

func refreshIdentityMatches(base, updated *Auth) bool {
	return base.ID == updated.ID && refreshBindingMatches(base, updated)
}

func refreshBindingMatches(base, updated *Auth) bool {
	return strings.EqualFold(strings.TrimSpace(base.Provider), strings.TrimSpace(updated.Provider)) &&
		executorKeyFromAuth(base) == executorKeyFromAuth(updated)
}

func refreshRevalidatesUnchangedToken(base, updated *Auth) bool {
	return !base.UsesLazyRefresh() && !updated.UsesLazyRefresh() || updated.CodexRefreshPatch != nil
}

type authFreshnessError struct{ error }

func (e *authFreshnessError) Unwrap() error { return e.error }

func isAuthFreshnessError(err error) bool {
	var freshness *authFreshnessError
	return errors.As(err, &freshness)
}

type lazyPersistenceError struct {
	auth *Auth
	err  error
}

func (e *lazyPersistenceError) Error() string {
	return "failed to persist refreshed credential"
}
func (e *lazyPersistenceError) Unwrap() error { return e.err }

type pendingLazyRefresh struct {
	base    *Auth
	updated *Auth
}

// EnsureAuthFresh refreshes opted-in credentials before an authenticated operation.
// It shares coordination and persistence with 401 recovery and explicit refresh.
func (m *Manager) EnsureAuthFresh(ctx context.Context, auth *Auth, clientHeaders ...http.Header) (*Auth, error) {
	fresh, _, err := m.EnsureAuthFreshForOperation(ctx, auth, clientHeaders...)
	return fresh, err
}

// HasLazyRefreshWork reports an active lazy refresh or a retained unsaved rotation.
func (m *Manager) HasLazyRefreshWork(id string) bool {
	if m == nil {
		return false
	}
	value, ok := m.refreshLocks.Load(id)
	if !ok {
		return false
	}
	lock, ok := value.(*authRefreshLock)
	return ok && lock != nil && lock.lazyWork.Load()
}

// EnsureAuthFreshForOperation also reports whether freshness work required validation.
func (m *Manager) EnsureAuthFreshForOperation(ctx context.Context, auth *Auth, clientHeaders ...http.Header) (*Auth, bool, error) {
	return m.ensureAuthFresh(ctx, auth, "", clientHeaders...)
}

func (m *Manager) ensureAuthFresh(ctx context.Context, auth *Auth, model string, clientHeaders ...http.Header) (*Auth, bool, error) {
	if m == nil || auth == nil {
		return auth, false, nil
	}
	if ctx != nil && ctx.Value(homeAttemptContextKey{}) != nil {
		if homeLazyRefreshDue(ctx, auth, clientHeaders...) {
			if errCtx := ctx.Err(); errCtx != nil {
				return auth, true, errCtx
			}
			return auth, true, &authFreshnessError{&Error{Code: "auth_unavailable", Message: "Home credential is not fresh", HTTPStatus: http.StatusServiceUnavailable}}
		}
		return auth, false, nil
	}
	m.mu.RLock()
	current := m.auths[auth.ID]
	hasWork := m.HasLazyRefreshWork(auth.ID)
	if current == nil {
		m.mu.RUnlock()
		if auth.RegistrationEpoch != 0 || auth.UsesLazyRefresh() || hasWork {
			return auth, true, &authFreshnessError{errors.New("credential changed before lazy refresh")}
		}
		return auth, false, nil
	}
	if !auth.UsesLazyRefresh() && !current.UsesLazyRefresh() && !hasWork {
		m.mu.RUnlock()
		return auth, false, nil
	}
	current = current.Clone()
	m.mu.RUnlock()
	if auth.RegistrationEpoch != 0 && current.RegistrationEpoch != auth.RegistrationEpoch ||
		auth.Provider != "" && !refreshBindingMatches(auth, current) {
		return auth, true, &authFreshnessError{errors.New("credential changed before lazy refresh")}
	}
	auth = current
	ctx = WithCodexClientRequest(ctx, IsCodexClientRequest(ctx, clientHeaders...))
	fresh, err := m.refreshAuthWithTrigger(ctx, auth.ID, "", auth.RegistrationEpoch, refreshTrigger{selected: auth, model: model})
	if err != nil {
		return auth, true, &authFreshnessError{err}
	}
	if fresh == nil || fresh.RegistrationEpoch != auth.RegistrationEpoch || !refreshIdentityMatches(auth, fresh) {
		return auth, true, &authFreshnessError{errors.New("credential changed during lazy refresh")}
	}
	if fresh.Disabled || fresh.Status == StatusDisabled || hasUnauthorizedAuthFailure(fresh) ||
		fresh.RequiresSignIn || !hasValidAccessTokenForRequest(ctx, fresh, time.Now()) {
		return auth, true, &authFreshnessError{&Error{Code: "auth_unavailable", Message: "credential is not fresh", HTTPStatus: http.StatusServiceUnavailable}}
	}
	if model != "" {
		if blocked, _, _ := isAuthBlockedForModelForRequest(ctx, fresh, m.selectionModelKeyForAuth(fresh, model), time.Now()); blocked {
			return auth, true, &authFreshnessError{&Error{Code: "auth_unavailable", Message: "credential became unavailable", HTTPStatus: http.StatusServiceUnavailable}}
		}
	}
	return fresh, true, nil
}

func lazyPersistenceUnsupported(auth *Auth) bool {
	return IsPluginVirtualAuth(auth) || IsConfigAPIKeyAuth(auth) ||
		strings.EqualFold(strings.TrimSpace(auth.Attributes["runtime_only"]), "true")
}

var errLazyRefreshStoreRequired = errors.New("lazy refresh requires a persistent store and persistable physical credential")

type safeLazyError struct {
	public     *Error
	retryAfter *time.Duration
}

func (e *safeLazyError) Error() string              { return e.public.Error() }
func (e *safeLazyError) Unwrap() error              { return e.public }
func (e *safeLazyError) StatusCode() int            { return e.public.StatusCode() }
func (e *safeLazyError) ErrorCode() string          { return e.public.Code }
func (e *safeLazyError) RetryAfter() *time.Duration { return e.retryAfter }
func (e *safeLazyError) SafeResponseHeaders() http.Header {
	if e.retryAfter == nil {
		return nil
	}
	return safeRetryAfterHeader(*e.retryAfter)
}

// SanitizeLazyRefreshError retains typed failure information without exposing plugin diagnostics.
func SanitizeLazyRefreshError(auth *Auth, err error) error {
	if err == nil {
		return nil
	}
	var persist *lazyPersistenceError
	if errors.As(err, &persist) {
		return &Error{Code: "refresh_persistence_failed", Message: "failed to persist refreshed credential", HTTPStatus: http.StatusServiceUnavailable}
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	var retry *time.Duration
	if hint := retryAfterFromError(err); hint != nil {
		value := *hint
		retry = &value
	}
	return &safeLazyError{public: renewalError(auth, err, true), retryAfter: retry}
}

type refreshTrigger struct {
	background bool
	selected   *Auth
	model      string
}

func (l *authRefreshLock) clearLazyState() {
	l.pending = nil
	l.failure = nil
	l.retryAt = time.Time{}
	l.failureEpoch = 0
	l.failureVersion = 0
}

func (l *authRefreshLock) recordLazyFailure(auth *Auth, err error) {
	l.failure = SanitizeLazyRefreshError(auth, err)
	delay := refreshFailureBackoff
	if hint := retryAfterFromError(l.failure); hint != nil {
		delay = max(delay, *hint)
	}
	retryAt := time.Now().Add(delay)
	if l.failureEpoch != auth.RegistrationEpoch || l.failureVersion != auth.CredentialVersion || l.retryAt.Before(retryAt) {
		l.retryAt = retryAt
	}
	l.failureEpoch = auth.RegistrationEpoch
	l.failureVersion = auth.CredentialVersion
}

func syncRefreshPolicyMetadata(auth *Auth) {
	if auth == nil || auth.RefreshPolicy == nil {
		return
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["host_refresh_policy"] = map[string]any{
		"mode":                  auth.RefreshPolicy.Mode,
		"expiry_margin_seconds": auth.RefreshPolicy.ExpiryMarginSeconds,
	}
}

func (m *Manager) retryLazyPersistence(ctx context.Context, lock *authRefreshLock, current *Auth) (*Auth, error) {
	pending := lock.pending
	if pending == nil {
		return current, nil
	}
	if pending.base.RegistrationEpoch != current.RegistrationEpoch ||
		pending.base.CredentialVersion != current.CredentialVersion || CredentialsChanged(pending.base, current) ||
		!refreshIdentityMatches(pending.base, current) {
		lock.clearLazyState()
		return current, nil
	}
	saved, err := m.UpdateRefreshedAuth(context.WithoutCancel(ctx), pending.base, pending.updated)
	if err != nil {
		var persist *lazyPersistenceError
		if errors.As(err, &persist) {
			lock.pending.updated = persist.auth
		}
		return nil, err
	}
	lock.clearLazyState()
	if saved == nil {
		return nil, errors.New("credential removed during lazy refresh persistence")
	}
	return saved, nil
}
