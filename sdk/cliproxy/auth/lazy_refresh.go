package auth

import (
	"context"
	"errors"
	"net/http"
	"sort"
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
		!authHasAnyRejectedRefreshPartition(a) && authHasRefreshCredential(a)
}

func lazyRefreshDue(a *Auth, now time.Time) bool {
	return lazyRefreshDueForRequest(nil, a, now)
}

func lazyRefreshDueForRequest(ctx context.Context, a *Auth, now time.Time) bool {
	if !a.UsesLazyRefresh() {
		return false
	}
	expiry, known := a.AccessTokenExpirationTimeForRequest(ctx)
	return authAccessTokenForRequest(ctx, a) == "" || known && !expiry.After(now.Add(time.Duration(a.RefreshPolicy.ExpiryMarginSeconds)*time.Second))
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
	if m == nil || auth == nil {
		return auth, false, nil
	}
	m.mu.RLock()
	current := m.auths[auth.ID].Clone()
	hasWork := m.HasLazyRefreshWork(auth.ID)
	m.mu.RUnlock()
	if !auth.UsesLazyRefresh() && !current.UsesLazyRefresh() && !hasWork {
		return auth, false, nil
	}
	if current == nil || auth.RegistrationEpoch != 0 && current.RegistrationEpoch != auth.RegistrationEpoch ||
		auth.Provider != "" && executorKeyFromAuth(current) != executorKeyFromAuth(auth) {
		return auth, true, &authFreshnessError{errors.New("credential changed before lazy refresh")}
	}
	auth = current
	ctx = WithCodexClientRequest(ctx, IsCodexClientRequest(ctx, clientHeaders...))
	fresh, err := m.refreshAuthWithTrigger(ctx, auth.ID, "", auth.RegistrationEpoch, refreshTriggerDemand)
	if err != nil {
		return auth, true, &authFreshnessError{err}
	}
	if fresh == nil || fresh.ID != auth.ID || fresh.RegistrationEpoch != auth.RegistrationEpoch || executorKeyFromAuth(fresh) != executorKeyFromAuth(auth) {
		return auth, true, &authFreshnessError{errors.New("credential changed during lazy refresh")}
	}
	if fresh.Disabled || fresh.Status == StatusDisabled || hasUnauthorizedAuthFailure(fresh) ||
		fresh.RequiresSignIn || !hasValidAccessTokenForRequest(ctx, fresh, time.Now()) {
		return auth, true, &authFreshnessError{&Error{Code: "auth_unavailable", Message: "credential is not fresh", HTTPStatus: http.StatusServiceUnavailable}}
	}
	if blocked, _, _ := isAuthBlockedForModelForRequest(ctx, fresh, "", time.Now()); blocked {
		return auth, true, &authFreshnessError{&Error{Code: "auth_unavailable", Message: "credential is unavailable", HTTPStatus: http.StatusServiceUnavailable}}
	}
	return fresh, true, nil
}

func lazyPersistenceUnsupported(auth *Auth) bool {
	return IsPluginVirtualAuth(auth) || IsConfigAPIKeyAuth(auth) ||
		strings.EqualFold(strings.TrimSpace(auth.Attributes["runtime_only"]), "true")
}

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

func safeLazyRefreshError(auth *Auth, err error) error {
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
	safe := resultErrorFromError(err)
	safe.Message = sanitizeRenewalMessage(redactLazyCredentialValues(auth, safe.Message))
	safe.Code = sanitizeRenewalMessage(redactLazyCredentialValues(auth, safe.Code))
	var retry *time.Duration
	if hint := retryAfterFromError(err); hint != nil {
		value := *hint
		retry = &value
	}
	return &safeLazyError{public: safe, retryAfter: retry}
}

func redactLazyCredentialValues(auth *Auth, text string) string {
	secrets := []string{authOuterAccessToken(auth), authRefreshToken(auth),
		authSIWCTokenField(auth, "access_token"), authSIWCTokenField(auth, "refresh_token"),
		authMetadataString(auth, "id_token"), authMetadataString(auth, "idToken")}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	for _, secret := range secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "[redacted]")
		}
	}
	return text
}

type refreshTrigger uint8

const (
	refreshTriggerDefault refreshTrigger = iota
	refreshTriggerDemand
	refreshTriggerBackground
)

func (l *authRefreshLock) clearLazyState() {
	l.pending = nil
	l.failure = nil
	l.retryAt = time.Time{}
	l.failureEpoch = 0
	l.failureVersion = 0
}

func (l *authRefreshLock) recordLazyFailure(auth *Auth, err error) {
	l.failure = safeLazyRefreshError(auth, err)
	l.retryAt = time.Now().Add(refreshFailureBackoff)
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
