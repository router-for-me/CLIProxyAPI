package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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
	if !a.UsesLazyRefresh() {
		return false
	}
	expiry, known := a.AccessTokenExpirationTime()
	return authAccessToken(a) == "" || known && !expiry.After(now.Add(time.Duration(a.RefreshPolicy.ExpiryMarginSeconds)*time.Second))
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

func (e *lazyPersistenceError) Error() string { return fmt.Sprintf("persist lazy refresh: %v", e.err) }
func (e *lazyPersistenceError) Unwrap() error { return e.err }

type pendingLazyRefresh struct {
	base    *Auth
	updated *Auth
}

// EnsureAuthFresh refreshes opted-in credentials before an authenticated operation.
// It shares coordination and persistence with 401 recovery and explicit refresh.
func (m *Manager) EnsureAuthFresh(ctx context.Context, auth *Auth) (*Auth, error) {
	if auth == nil || !auth.UsesLazyRefresh() {
		return auth, nil
	}
	fresh, err := m.refreshAuthWithTrigger(ctx, auth.ID, "", auth.RegistrationEpoch, refreshTriggerDemand)
	if err != nil {
		return auth, &authFreshnessError{err}
	}
	if fresh == nil || fresh.RegistrationEpoch != auth.RegistrationEpoch || executorKeyFromAuth(fresh) != executorKeyFromAuth(auth) {
		return auth, &authFreshnessError{errors.New("credential changed during lazy refresh")}
	}
	if fresh.Disabled || fresh.Status == StatusDisabled || hasUnauthorizedAuthFailure(fresh) ||
		fresh.RequiresSignIn || fresh.UsesLazyRefresh() && !fresh.HasValidAccessToken(time.Now()) {
		return auth, &authFreshnessError{&Error{Code: "auth_unavailable", Message: "credential is not fresh", HTTPStatus: http.StatusServiceUnavailable}}
	}
	if blocked, _, _ := isAuthBlockedForModel(fresh, "", time.Now()); blocked {
		return auth, &authFreshnessError{&Error{Code: "auth_unavailable", Message: "credential is unavailable", HTTPStatus: http.StatusServiceUnavailable}}
	}
	return fresh, nil
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
	l.failure = err
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
		pending.base.CredentialVersion != current.CredentialVersion || CredentialsChanged(pending.base, current) {
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
