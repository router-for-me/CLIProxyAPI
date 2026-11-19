package auth

import "context"

// StoppedByRequestScopedRule reports whether err, returned for a request
// served by auth, matches a stop rule of that auth's effective request-scoped
// errors: per-credential rules first, then the config. It is the evaluation
// the manager uses to end a request on that error.
func (m *Manager) StoppedByRequestScopedRule(ctx context.Context, auth *Auth, err error) bool {
	if m == nil || auth == nil {
		return false
	}
	action, matched := matchRequestScopedErrorAction(auth, err, m.requestScopedErrorConfig(ctx))
	return isRequestScopedStop(action, matched)
}
