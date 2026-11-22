package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// pickNextMixed picks a credential for the request. When none is available
// only because every candidate's access token has expired, as after a laptop
// sleeps past the expiry and the background refresher hasn't run yet, it
// refreshes a candidate on demand and picks once more instead of failing with
// auth_unavailable.
//
// Other requests may be doing the same. The second pick also runs when one of
// them finished a refresh between this request's failed pick and its look at
// the candidates, which the structural epoch shows.
func (m *Manager) pickNextMixed(ctx context.Context, providers []string, model string, opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, ProviderExecutor, string, error) {
	epochBefore := m.structuralEpoch.Load()
	auth, executor, provider, err := m.pickNextMixedOnce(ctx, providers, model, opts, tried)
	if err == nil || m.HomeEnabled() || !shouldRetrySchedulerPick(err) {
		return auth, executor, provider, err
	}
	if !m.ensureFreshTokenForPick(ctx, providers, model, opts, tried) && m.structuralEpoch.Load() == epochBefore {
		return auth, executor, provider, err
	}
	return m.pickNextMixedOnce(ctx, providers, model, opts, tried)
}

type freshTokenForPickContextKey struct{}

var errFreshTokenForPickBackingOff = errors.New("credential refresh is backing off after a failure")

// withFreshTokenForPick marks ctx as an on-demand refresh made while picking a
// credential for a request with clientHeaders. Such a refresh only does work
// when, once it holds the credential's refresh lock, the access token is still
// expired and no refresh has just failed.
func withFreshTokenForPick(ctx context.Context, clientHeaders http.Header) context.Context {
	return context.WithValue(ctx, freshTokenForPickContextKey{}, clientHeaders)
}

func freshTokenForPickFromContext(ctx context.Context) (http.Header, bool) {
	if ctx == nil {
		return nil, false
	}
	clientHeaders, ok := ctx.Value(freshTokenForPickContextKey{}).(http.Header)
	return clientHeaders, ok
}

// refreshBackingOffLocked reports whether auth's last refresh failed and its
// retry is still in the future. A queued or running background job also sets
// NextRefreshAfter, as its pending marker; that is not a failure. Callers hold m.mu.
func (m *Manager) refreshBackingOffLocked(auth *Auth, now time.Time) bool {
	if auth == nil || !auth.NextRefreshAfter.After(now) {
		return false
	}
	job := m.refreshJobs[auth.ID]
	return job == nil || !auth.NextRefreshAfter.Equal(job.pendingUntil)
}

// freshTokenForPickSettled reports whether an on-demand refresh has nothing
// left to do: the access token is no longer expired because another caller
// refreshed it (nil error), or a refresh just failed and is backing off
// (errFreshTokenForPickBackingOff). The caller holds the credential's refresh lock.
func (m *Manager) freshTokenForPickSettled(ctx context.Context, id string, clientHeaders http.Header, now time.Time) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	current := m.auths[id]
	if current == nil {
		return false, nil
	}
	if !accessTokenExpiredForRequest(ctx, current, clientHeaders, now) {
		return true, nil
	}
	if m.refreshBackingOffLocked(current, now) {
		return true, errFreshTokenForPickBackingOff
	}
	return false, nil
}

func accessTokenExpiredForRequest(ctx context.Context, auth *Auth, clientHeaders http.Header, now time.Time) bool {
	exp, ok := auth.AccessTokenExpirationTimeForRequest(ctx, clientHeaders)
	return ok && !exp.IsZero() && !exp.After(now)
}

// ensureFreshTokenForPick gives one of the request's credentials an unexpired
// access token and reports whether it did. It refreshes credentials blocked
// only by an expired token, one at a time, and stops at the first success. A
// credential another caller is refreshing right now is waited for instead:
// the refresh lock is released only after the result has reached the scheduler.
func (m *Manager) ensureFreshTokenForPick(ctx context.Context, providers []string, model string, opts cliproxyexecutor.Options, tried map[string]struct{}) bool {
	if m == nil || ctx == nil || ctx.Err() != nil {
		return false
	}
	refreshCtx := withFreshTokenForPick(ctx, opts.Headers)
	for _, id := range m.freshTokenForPickCandidates(ctx, providers, model, opts, tried) {
		if ctx.Err() != nil {
			return false
		}
		refreshed, errRefresh := m.refreshAuthForRequest(refreshCtx, id, "")
		if errRefresh != nil || refreshed == nil {
			continue
		}
		if !accessTokenExpiredForRequest(ctx, refreshed, opts.Headers, time.Now()) {
			return true
		}
	}
	return false
}

// freshTokenForPickCandidates lists the request's credentials worth waiting
// for or refreshing: first those with a fresh token whose refresh is still
// being committed, then those blocked only by an expired token.
func (m *Manager) freshTokenForPickCandidates(ctx context.Context, providers []string, model string, opts cliproxyexecutor.Options, tried map[string]struct{}) []string {
	providerSet := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		if key := canonicalSchedulingProvider(provider); key != "" {
			providerSet[key] = struct{}{}
		}
	}
	if len(providerSet) == 0 {
		return nil
	}
	eligibility := authSelectionEligibilityForRequest(ctx, opts)
	pinnedAuthID := pinnedAuthIDFromMetadata(opts.Metadata)
	registryRef := registry.GetGlobalRegistry()
	now := time.Now()

	m.mu.RLock()
	defer m.mu.RUnlock()
	var committing, expired []string
	for _, auth := range m.auths {
		if auth == nil || isAuthDisabled(auth) {
			continue
		}
		if _, wasTried := tried[auth.ID]; wasTried {
			continue
		}
		if pinnedAuthID != "" && auth.ID != pinnedAuthID {
			continue
		}
		if _, ok := providerSet[canonicalSchedulingProvider(executorKeyFromAuth(auth))]; !ok {
			continue
		}
		if !eligibility.allows(auth) {
			continue
		}
		if strings.TrimSpace(model) != "" && !m.authSupportsRouteModel(registryRef, auth, model) {
			continue
		}
		if _, hasExecutor := m.executorLocked(executorKeyFromAuth(auth)); !hasExecutor {
			continue
		}
		if !accessTokenExpiredForRequest(ctx, auth, opts.Headers, now) {
			checkModel := model
			if strings.TrimSpace(model) != "" {
				checkModel = m.selectionModelForAuth(auth, model)
			}
			if blocked, _, _ := isAuthBlockedForModelForRequest(ctx, auth, checkModel, now, opts.Headers); !blocked && m.refreshLockHeld(auth.ID) {
				committing = append(committing, auth.ID)
			}
			continue
		}
		// Leave credentials that need sign-in, or whose last refresh failed
		// and is backing off, to the background refresher.
		if hasUnauthorizedAuthFailure(auth) || hasDisabledInvalidGrantFailure(auth) || authHasUnchangedRejectedRefreshToken(auth) {
			continue
		}
		if m.codexAutoRefreshUnavailable(auth, now) || m.refreshBackingOffLocked(auth, now) {
			continue
		}
		expired = append(expired, auth.ID)
	}
	return append(committing, expired...)
}

// refreshLockHeld reports whether a refresh of the credential is running now.
func (m *Manager) refreshLockHeld(id string) bool {
	lockValue, ok := m.refreshLocks.Load(id)
	if !ok {
		return false
	}
	lock, ok := lockValue.(*authRefreshLock)
	return ok && lock != nil && len(lock.gate) > 0
}
