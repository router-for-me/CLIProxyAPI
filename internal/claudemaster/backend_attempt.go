package claudemaster

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

type backendAttemptKey struct{}

// Unrelated failures prevent paid retries of this request, not future requests.
// A stream result may arrive on a detached observer, so access is synchronized.
type backendAttempt struct {
	mu               sync.Mutex
	failures         map[string]map[string]int // model key -> auth id -> upstream HTTP status (0 when none)
	refusal          error                     // the selector's refusal in this request, for the client's error
	refusalGeneric   bool                      // the refusal is a bare core error; a specific one replaces it
	upstream         *backendUpstreamError     // the latest upstream error response of this request, exactly
	notFoundRetried  bool                      // the one same-subscription retry of a 404 with no Anthropic error was spent
	route            *backendRouteAttempt
	pickedAt         time.Time // when an account was chosen: claude-master's own time ends here
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
	if state.pickedAt.IsZero() {
		state.pickedAt = time.Now()
	}
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
		state.failures = make(map[string]map[string]int)
	}
	model := backendBlockedModelKey(result.Model)
	if state.failures[model] == nil {
		state.failures[model] = make(map[string]int)
	}
	state.failures[model][result.AuthID] = result.Error.HTTPStatus
}

// backendAttemptStatus reports the upstream HTTP status of this request's failed attempt on
// authID for model (0 when the failure carried none), and whether there was one.
func backendAttemptStatus(ctx context.Context, authID, model string) (int, bool) {
	if ctx == nil {
		return 0, false
	}
	state, _ := ctx.Value(backendAttemptKey{}).(*backendAttempt)
	if state == nil {
		return 0, false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	for _, key := range []string{backendBlockedModelKey(model), ""} {
		if status, ok := state.failures[key][authID]; ok {
			return status, true
		}
	}
	return 0, false
}

// noteBackendRefusal keeps the reason the selector refused this request, for the client's error
// in place of the generic "configured inference failed". The reason is claude-master's own text
// (a profile name, a model, an HTTP status), never upstream text. A request can be refused more
// than once (the scheduler asks again after an attempt fails): the first SPECIFIC refusal wins,
// and a bare core error ("auth_unavailable: no auth available") only fills an empty slot and is
// replaced by a specific one. Observed 2026-10-09: a bound subscription in model cooldown made
// the core's generic error come first and the session saw that instead of the reason.
func noteBackendRefusal(ctx context.Context, err error) {
	state := backendRequestAttempt(ctx)
	if state == nil || err == nil || strings.TrimSpace(err.Error()) == "" {
		return
	}
	generic := backendGenericRefusal(err)
	state.mu.Lock()
	switch {
	case state.refusal == nil:
		state.refusal, state.refusalGeneric = err, generic
	case state.refusalGeneric && !generic:
		state.refusal, state.refusalGeneric = err, false
	}
	state.mu.Unlock()
}

func backendRefusalErrorFrom(ctx context.Context) error {
	state := backendRequestAttempt(ctx)
	if state == nil {
		return nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.refusal
}

// backendGenericRefusal reports a core auth error (auth_unavailable, auth_not_found, a model
// cooldown) as opposed to a reason the selector wrote itself.
func backendGenericRefusal(err error) bool {
	var refusal *backendRefusalError
	if errors.As(err, &refusal) {
		return false
	}
	var authErr *coreauth.Error
	return errors.As(err, &authErr)
}

func backendRefusal(ctx context.Context) string {
	if err := backendRefusalErrorFrom(ctx); err != nil {
		return err.Error()
	}
	return ""
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
