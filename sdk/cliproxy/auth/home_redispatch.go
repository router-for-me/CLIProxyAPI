package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// A credential that hit its limit while another one is eligible must not surface
// a Home bookkeeping error to the caller; the helpers below keep re-routes going.

// homeReleaseAckTimeoutMessage prefixes the error endHomeSelectionBeforeRedispatch
// returns when Home did not acknowledge a release in time. A pick that fails this
// way comes from a retained-selection target change, which keeps its wait.
const homeReleaseAckTimeoutMessage = "Home did not acknowledge credential release: "

// homeDispatcherHonoursExclusions reports whether the next pick sends
// excluded_auth_ids and Home applies them. Sending mirrors the dispatcher switch in
// pickHomeDispatchSelection; the plain RPopAuth and RPopAuthWithPolicy variants
// carry no exclusions. Home negotiates no capability for applying them, so a Home
// that has handed back an excluded credential is treated as one that ignores them.
func (m *Manager) homeDispatcherHonoursExclusions(ctx context.Context) bool {
	bundle := m.HomeDispatchBundle()
	if bundle == nil || bundle.client == nil || bundle.ignoresExclusions.Load() {
		return false
	}
	client := bundle.client
	if _, ok := client.(homeDispatchSessionHierarchyDispatcher); ok {
		return true
	}
	if credentialPolicyFromContext(ctx) == "" {
		if _, ok := client.(homeDispatchRetryRoundConstraintsDispatcher); ok {
			return true
		}
		_, ok := client.(homeDispatchConstraintsDispatcher)
		return ok
	}
	if _, ok := client.(homeCredentialPolicyRetryRoundConstraintsDispatcher); ok {
		return true
	}
	if _, ok := client.(homeCredentialPolicyDispatcher); ok {
		_, okConstraints := client.(homeCredentialPolicyConstraintsDispatcher)
		return okConstraints
	}
	return false
}

// endHomeSelectionBeforeExcludedRedispatch ends selection and waits for Home to
// acknowledge the release, like endHomeSelectionBeforeRedispatch. When the
// credential is excluded from the next pick and the dispatcher honours that, Home
// cannot hand it back, so a release acknowledgement that outlives CPACancelBound
// no longer ends the call. The release stays queued in the flusher as a cumulative
// sequence and Home applies it when it lands; nothing is resent or counted twice.
// A cancelled caller that has to wait still gets the wait's error.
func (m *Manager) endHomeSelectionBeforeExcludedRedispatch(ctx context.Context, selection *HomeDispatchSelection, reason string, excluded map[string]struct{}) error {
	if selection == nil {
		return nil
	}
	authID := ""
	if auth := selection.CloneAuth(); auth != nil {
		authID = strings.TrimSpace(auth.ID)
	}
	if _, isExcluded := excluded[authID]; isExcluded && authID != "" && selection.releaseWaited.Load() && m.homeDispatcherHonoursExclusions(ctx) {
		// A websocket session keeps its selection after the failure path ended it and
		// waited for the release. Waiting on the same ticket again would only turn a slow
		// acknowledgement into a 503 for a credential Home can't hand back. A selection
		// that was only ended, such as by the websocket lifecycle, has not been waited on
		// yet: it falls through, joins EndWithRelease and waits once.
		return nil
	}
	errEnd := m.endHomeSelectionBeforeRedispatch(ctx, selection, reason)
	if errEnd == nil || authID == "" || (ctx != nil && ctx.Err() != nil) {
		return errEnd
	}
	if _, isExcluded := excluded[authID]; !isExcluded || !m.homeDispatcherHonoursExclusions(ctx) {
		return errEnd
	}
	logEntryWithRequestID(ctx).Warnf("home: release of excluded credential %s not acknowledged within the cancel bound (%s); redispatching, release stays pending", authID, reason)
	return nil
}

// shouldRedispatchAfterHomeUnavailable reports whether a failed pick is a
// transient dispatch failure worth one more dispatch. An ambiguous dispatch
// fences the client and drops its heartbeat, so that retry fails fast with a
// non-retryable home_unavailable instead of reaching Home. A caller that is
// already gone gets no extra dispatch.
func shouldRedispatchAfterHomeUnavailable(ctx context.Context, errPick, lastErr error) bool {
	if errPick == nil || lastErr == nil || (ctx != nil && ctx.Err() != nil) {
		return false
	}
	var authErr *Error
	if !errors.As(errPick, &authErr) || authErr == nil || !authErr.Retryable {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(authErr.Code), "home_unavailable") {
		return false
	}
	return !strings.HasPrefix(authErr.Message, homeReleaseAckTimeoutMessage)
}

// redispatchAfterHomeUnavailable runs the one extra pick within the caller's
// remaining deadline, capped at one CPACancelBound. pickHomeDispatchSelection
// keeps no reference to the context it is given, so the bound ends with the call.
func (m *Manager) redispatchAfterHomeUnavailable(ctx context.Context, model string, opts cliproxyexecutor.Options) (*HomeDispatchSelection, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	pickCtx, cancelPick := context.WithTimeout(ctx, m.homeCPACancelBound())
	defer cancelPick()
	return m.pickHomeDispatchSelection(pickCtx, model, opts)
}

// homeRedispatchFailureError is what the caller gets when the extra pick also
// fails because Home is unavailable: the account's own 429 with its retry time,
// so a client backs off instead of seeing a 503 for a Home hiccup. It is nil
// when the extra pick failed any other way, such as Home saying no account is
// free yet, or for any other lastErr, which keeps the existing pick-failure handling
// and its retry rounds.
func homeRedispatchFailureError(ctx context.Context, errPick, lastErr, upstreamErr error, timing *homeRetryRoundTiming) error {
	if ctx != nil && ctx.Err() != nil {
		return nil
	}
	if !strings.EqualFold(errorCodeFromError(errPick), "home_unavailable") {
		return nil
	}
	if statusCodeFromError(lastErr) != http.StatusTooManyRequests {
		return nil
	}
	preferredErr := preferredExecutionAttemptError(lastErr, upstreamErr)
	// Like every other end of a round: a plugin's credential refusal is returned as is.
	if isCredentialTerminatedError(preferredErr) {
		return preferredErr
	}
	return markHomeRetryRoundExhausted(preferredErr, timing.RetryAfter(), false)
}
