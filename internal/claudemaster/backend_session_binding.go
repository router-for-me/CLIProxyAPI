package claudemaster

import (
	"context"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func (s *backendSeriesSelector) sessionBindingLocked(sessionID string, auths []*coreauth.Auth, opaque bool) (string, bool, error) {
	if s.routes == nil {
		id, found := s.sessions.GetAndRefresh(sessionID)
		return id, found, nil
	}
	route, found, _, ambiguous, errLookup := s.routes.lookupReplicas(sessionID)
	if errLookup != nil {
		return "", false, errLookup
	}
	if !found {
		return "", false, nil
	}
	if opaque && ambiguous {
		return "", false, backendRefuse(backendRefusalInvalid, "conversation routing replicas have an unfinished handoff; refusing to move opaque state")
	}
	for _, auth := range auths {
		if origin := s.routes.origin(auth); auth != nil && origin != "" {
			s.routes.authOrigins[auth.ID] = origin
		}
	}
	for authID, origin := range s.routes.authOrigins {
		if origin == route.Origin {
			s.sessions.Set(sessionID, authID)
			return authID, true, nil
		}
	}
	// Retain knowledge that there was an origin even when it isn't configured
	// now. Clean work can start on another account; opaque work cannot.
	return "missing:" + route.Origin, true, nil
}

func (s *backendSeriesSelector) bindSessionLocked(ctx context.Context, sessionID string, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return s.bindSessionOriginLocked(ctx, sessionID, auth, false)
}

func (s *backendSeriesSelector) bindInheritedSessionLocked(ctx context.Context, sessionID, parentSessionID string, auth *coreauth.Auth) (*coreauth.Auth, error) {
	proven := false
	if s.routes != nil {
		parent, found, errLookup := s.routes.lookup(parentSessionID)
		if errLookup != nil {
			return nil, errLookup
		}
		proven = found && parent.Committed && parent.Revision == 1 && parent.Origin == s.routes.origin(auth)
		if !proven {
			return nil, backendRefuse(backendRefusalInvalid, "cannot establish the first opaque child's unchanged parent origin")
		}
	}
	return s.bindSessionOriginLocked(ctx, sessionID, auth, proven)
}

func (s *backendSeriesSelector) bindSessionOriginLocked(ctx context.Context, sessionID string, auth *coreauth.Auth, originProven bool) (*coreauth.Auth, error) {
	if sessionID == "" || auth == nil {
		return auth, nil
	}
	if backendIsCountRequest(ctx) {
		return auth, nil
	}
	if s.routes != nil {
		if s.prepareIdentity != nil && s.routes.origin(auth) == "" {
			prepared, errPrepare := s.prepareIdentity(ctx, auth)
			if errPrepare != nil || prepared == nil {
				return nil, backendRefuse(backendRefusalInternal, "cannot acquire Claude conversation origin identity")
			}
			auth = prepared
		}
		for origin, count := range s.activeRoutes[sessionID] {
			if count > 0 && origin != s.routes.origin(auth) {
				return nil, backendRefuse(backendRefusalUnavailable, "the Claude conversation still has an active request on its current account; retry clean handoff after it finishes")
			}
		}
		if errBind := s.routes.bindWithProof(sessionID, auth, originProven); errBind != nil {
			return nil, errBind
		}
		route, found, errLookup := s.routes.lookup(sessionID)
		if errLookup != nil || !found {
			return nil, backendRefuse(backendRefusalInternal, "cannot confirm pending conversation routing")
		}
		recordBackendRouteAttempt(ctx, s, sessionID, auth.ID, route)
	}
	previousID, hadPrevious := "", false
	if s.sessions != nil {
		previousID, hadPrevious = s.sessions.Get(sessionID)
	}
	s.sessions.Set(sessionID, auth.ID)
	s.noteBoundLocked(sessionID, previousID, hadPrevious, auth)
	return auth, nil
}

func (s *backendSeriesSelector) parentOriginChangedLocked(sessionID string) (bool, error) {
	if s.routes == nil {
		return false, nil
	}
	route, found, errLookup := s.routes.lookup(sessionID)
	return found && route.Revision > 1, errLookup
}
