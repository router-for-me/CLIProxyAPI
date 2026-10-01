package claudemaster

import (
	"context"
	"net/http"
	"sync"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

type backendAttemptKey struct{}

// Unrelated failures prevent paid retries of this request, not future requests.
// A stream result may arrive on a detached observer, so access is synchronized.
type backendAttempt struct {
	mu               sync.Mutex
	failures         map[string]map[string]bool
	route            *backendRouteAttempt
	count            bool
	cancelRegistered bool
}

type backendRouteAttempt struct {
	selector  *backendSeriesSelector
	sessionID string
	authID    string
	route     backendSessionRoute
	active    bool
	accepted  bool
}

func backendRequestAttempt(ctx context.Context) *backendAttempt {
	if ctx == nil {
		return nil
	}
	state, _ := ctx.Value(backendAttemptKey{}).(*backendAttempt)
	return state
}

func backendSetCountRequest(ctx context.Context) {
	if state := backendRequestAttempt(ctx); state != nil {
		state.mu.Lock()
		state.count = true
		state.mu.Unlock()
	}
}

func backendIsCountRequest(ctx context.Context) bool {
	state := backendRequestAttempt(ctx)
	if state == nil {
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.count
}

// Called while the selector lock is held. Request lifetime, not stream chunks,
// owns the active-generation guard; token counting never changes an origin.
func recordBackendRouteAttempt(ctx context.Context, selector *backendSeriesSelector, sessionID, authID string, route backendSessionRoute) {
	state := backendRequestAttempt(ctx)
	if state == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.count {
		return
	}
	if previous := state.route; previous != nil && previous.active {
		return
	}
	state.route = &backendRouteAttempt{selector: selector, sessionID: sessionID, authID: authID, route: route, active: true}
	if selector.activeRoutes == nil {
		selector.activeRoutes = make(map[string]map[string]int)
	}
	if selector.activeRoutes[sessionID] == nil {
		selector.activeRoutes[sessionID] = make(map[string]int)
	}
	selector.activeRoutes[sessionID][route.Origin]++
	if !state.cancelRegistered {
		state.cancelRegistered = true
		context.AfterFunc(ctx, func() { releaseBackendRouteAttempt(ctx) })
	}
}

func commitBackendRouteAttempt(ctx context.Context, authID string) {
	state := backendRequestAttempt(ctx)
	if state == nil {
		return
	}
	state.mu.Lock()
	var attempt backendRouteAttempt
	if state.route != nil && state.route.authID == authID {
		state.route.accepted = true
		attempt = *state.route
	}
	state.mu.Unlock()
	if attempt.selector == nil {
		return
	}
	s := attempt.selector
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.routes == nil {
		return
	}
	if err := s.routes.commit(attempt.sessionID, attempt.route); err != nil {
		// Never replace/replay an accepted native response because local durable
		// routing failed. Pending state remains conservative on the next turn.
		log.Warn("claude-master could not commit private conversation routing; opaque continuations may require a self-contained retry")
	}
}

func releaseBackendRouteAttempt(ctx context.Context) {
	releaseBackendRouteAttemptForAuth(ctx, "")
}

func releaseBackendRouteAttemptForAuth(ctx context.Context, authID string) {
	releaseBackendRouteAttemptMatching(ctx, authID, false)
}

func releaseFailedBackendRouteAttempt(ctx context.Context, authID string) {
	releaseBackendRouteAttemptMatching(ctx, authID, true)
}

func releaseBackendRouteAttemptMatching(ctx context.Context, authID string, onlyUnaccepted bool) {
	state := backendRequestAttempt(ctx)
	if state == nil {
		return
	}
	state.mu.Lock()
	var attempt backendRouteAttempt
	if state.route != nil && state.route.active && (authID == "" || state.route.authID == authID) && (!onlyUnaccepted || !state.route.accepted) {
		attempt = *state.route
		state.route.active = false
	}
	state.mu.Unlock()
	if attempt.selector == nil {
		return
	}
	s := attempt.selector
	s.mu.Lock()
	defer s.mu.Unlock()
	if active := s.activeRoutes[attempt.sessionID]; active != nil {
		active[attempt.route.Origin]--
		if active[attempt.route.Origin] <= 0 {
			delete(active, attempt.route.Origin)
		}
		if len(active) == 0 {
			delete(s.activeRoutes, attempt.sessionID)
		}
	}
}

func withBackendAttempt(ctx context.Context) context.Context {
	return context.WithValue(ctx, backendAttemptKey{}, &backendAttempt{})
}

func recordBackendAttempt(ctx context.Context, result coreauth.Result) {
	if ctx == nil {
		return
	}
	state, _ := ctx.Value(backendAttemptKey{}).(*backendAttempt)
	if state == nil || result.Success || result.Error == nil {
		return
	}
	credentialQuota := result.Error.HTTPStatus == http.StatusTooManyRequests && result.CredentialScope &&
		result.Error.Code != coreauth.ErrorCodeRequestScoped && result.Error.Code != coreauth.ErrorCodeForceCooldown
	if credentialQuota {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.failures == nil {
		state.failures = make(map[string]map[string]bool)
	}
	model := backendBlockedModelKey(result.Model)
	if state.failures[model] == nil {
		state.failures[model] = make(map[string]bool)
	}
	state.failures[model][result.AuthID] = true
}

func backendAttemptFailed(ctx context.Context, authID, model string) bool {
	if ctx == nil {
		return false
	}
	state, _ := ctx.Value(backendAttemptKey{}).(*backendAttempt)
	if state == nil {
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	for _, key := range []string{"", backendBlockedModelKey(model)} {
		for failedAuth := range state.failures[key] {
			if authID == "" || authID == failedAuth {
				return true
			}
		}
	}
	return false
}
