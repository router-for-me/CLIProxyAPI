package auth

import (
	"context"
	"net/http"
	"strings"

	log "github.com/sirupsen/logrus"
)

// tryRefreshExecutionAuthAfterUnauthorized refreshes OAuth credentials once for
// either a local auth or an ephemeral Home dispatch auth.
func (m *Manager) tryRefreshExecutionAuthAfterUnauthorized(ctx context.Context, executor ProviderExecutor, auth *Auth, execErr error, alreadyTried bool, homeDispatch bool) (*Auth, bool, error) {
	if !homeDispatch {
		refreshed, ok := m.tryRefreshAfterUnauthorized(ctx, auth, execErr, alreadyTried)
		return refreshed, ok, nil
	}
	if m == nil || executor == nil || auth == nil || alreadyTried || execErr == nil {
		return auth, false, nil
	}
	if !isUnauthorizedError(execErr) || (auth.AuthKind() != AuthKindOAuth && strings.TrimSpace(authRefreshToken(auth)) == "") {
		return auth, false, nil
	}

	log.Debugf("unauthorized Home response for %s (%s), refreshing credentials before redispatch", auth.Provider, auth.ID)
	target := auth.Clone()
	updated, errRefresh := executor.Refresh(ctx, target)
	if errRefresh != nil {
		log.Debugf("Home credential refresh before redispatch failed for %s (%s)", auth.Provider, auth.ID)
		return auth, false, errRefresh
	}
	if updated == nil {
		updated = target
	}
	if updated.ID == "" {
		updated.ID = auth.ID
	}
	if updated.Index == "" {
		updated.Index = auth.Index
	}
	if updated.Provider == "" {
		updated.Provider = auth.Provider
	}
	if updated.Runtime == nil {
		updated.Runtime = auth.Runtime
	}
	preserveHomeRoutingAttributes(updated, auth)
	prepared, errPrepare := m.prepareHomeAuthSnapshot(ctx, executor, updated)
	if errPrepare != nil {
		return auth, false, errPrepare
	}
	preserveHomeRoutingAttributes(prepared, auth)
	return prepared, true, nil
}

// RefreshHomeSelectionAfterUnauthorized refreshes the credential snapshot that
// received a 401, or reuses a newer token already installed on the selection.
func (m *Manager) RefreshHomeSelectionAfterUnauthorized(ctx context.Context, selection *HomeDispatchSelection, failedAuth *Auth) (*Auth, bool, error) {
	if m == nil || selection == nil {
		return nil, false, nil
	}
	current := selection.CloneAuth()
	if failedAuth == nil {
		failedAuth = current
	}
	if current != nil && failedAuth != nil && current.ID == failedAuth.ID {
		currentToken := authAccessToken(current)
		failedToken := authAccessToken(failedAuth)
		if currentToken != "" && failedToken != "" && currentToken != failedToken {
			prepared, errPrepare := m.prepareHomeAuthSnapshot(ctx, selection.Executor, current)
			if errPrepare != nil {
				return current, false, errPrepare
			}
			preserveHomeRoutingAttributes(prepared, current)
			m.replaceHomeSelectionAuth(selection, prepared)
			return selection.CloneAuth(), true, nil
		}
	}
	refreshed, okRefresh, errRefresh := m.tryRefreshExecutionAuthAfterUnauthorized(ctx, selection.Executor, failedAuth, &Error{HTTPStatus: http.StatusUnauthorized, Message: "upstream unauthorized"}, false, true)
	if errRefresh != nil || !okRefresh {
		return current, false, errRefresh
	}
	m.replaceHomeSelectionAuth(selection, refreshed)
	updated := selection.CloneAuth()
	if updated == nil {
		return nil, false, &Error{Code: "auth_not_found", Message: "refreshed Home auth is unavailable", HTTPStatus: http.StatusServiceUnavailable}
	}
	return updated, true, nil
}
