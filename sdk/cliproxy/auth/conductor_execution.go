package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	cliproxysession "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
)

func (m *Manager) newUpstreamAttemptContext(ctx context.Context) context.Context {
	ctx = logging.WithFreshResponseHeadersHolder(ctx)
	if RequestScopedErrorConfigFromContext(ctx) == nil {
		ctx = WithRequestScopedErrorConfig(ctx, m.runtimeConfigSnapshot())
	}
	return cliproxyexecutor.WithUpstreamAttemptTracker(ctx)
}

func newUpstreamAttemptContext(ctx context.Context) context.Context {
	ctx = logging.WithFreshResponseHeadersHolder(ctx)
	return cliproxyexecutor.WithUpstreamAttemptTracker(ctx)
}

func claudeOAuthRequestCancellation(ctx context.Context, auth *Auth, err error) error {
	if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "claude") || !strings.EqualFold(strings.TrimSpace(auth.Attributes["auth_kind"]), "oauth") {
		return nil
	}
	if ctx != nil && errors.Is(ctx.Err(), context.Canceled) {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

type upstreamAttemptProviderContextKey struct{}

func withUpstreamAttemptProvider(ctx context.Context, provider string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return ctx
	}
	return context.WithValue(ctx, upstreamAttemptProviderContextKey{}, provider)
}

func upstreamAttemptProviderFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	provider, _ := ctx.Value(upstreamAttemptProviderContextKey{}).(string)
	return strings.TrimSpace(provider)
}

// upstreamAttemptSeq allocates a process-unique identifier for every
// upstream execution attempt marker so error wrappers rebuilt during
// annotation can still be traced back to the same physical attempt.
var upstreamAttemptSeq atomic.Uint64

type upstreamExecutionAttemptError struct {
	cause    error
	provider string
	// attemptID identifies the physical upstream attempt this error was
	// raised for; it is preserved when bookkeeping wrappers are rebuilt.
	attemptID uint64
}

func (e *upstreamExecutionAttemptError) Error() string {
	if e == nil || e.cause == nil {
		return ""
	}
	return e.cause.Error()
}

func (e *upstreamExecutionAttemptError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *upstreamExecutionAttemptError) Headers() http.Header {
	if e == nil || e.cause == nil {
		return nil
	}
	var he interface{ Headers() http.Header }
	if errors.As(e.cause, &he) && he != nil {
		return he.Headers()
	}
	return nil
}

func markUpstreamExecutionAttempt(err error) error {
	return markUpstreamExecutionAttemptWithProvider(err, "")
}

func markUpstreamExecutionAttemptWithProvider(err error, provider string) error {
	if err == nil {
		return nil
	}
	provider = strings.TrimSpace(provider)
	if existingProvider := upstreamAttemptProviderFromError(err); existingProvider != "" || hasUpstreamExecutionAttempt(err) {
		if provider != "" && existingProvider == "" {
			var marked *upstreamExecutionAttemptError
			if errors.As(err, &marked) && marked != nil {
				marked.provider = provider
			}
		}
		return err
	}
	return markUpstreamExecutionAttemptWithProviderAndID(err, provider, upstreamAttemptSeq.Add(1))
}

// markUpstreamExecutionAttemptWithProviderAndID tags err with the caller's
// attempt identity, reusing id when the error already carries a marker.
func markUpstreamExecutionAttemptWithProviderAndID(err error, provider string, id uint64) error {
	if err == nil {
		return nil
	}
	provider = strings.TrimSpace(provider)
	if existingProvider := upstreamAttemptProviderFromError(err); existingProvider != "" || hasUpstreamExecutionAttempt(err) {
		if provider != "" && existingProvider == "" {
			var marked *upstreamExecutionAttemptError
			if errors.As(err, &marked) && marked != nil {
				marked.provider = provider
			}
		}
		return err
	}
	return &upstreamExecutionAttemptError{cause: err, provider: provider, attemptID: id}
}

func markUpstreamExecutionAttemptFromContext(ctx context.Context, err error) error {
	if err == nil || !cliproxyexecutor.UpstreamAttempted(ctx) {
		return err
	}
	return markUpstreamExecutionAttemptWithProvider(err, upstreamAttemptProviderFromContext(ctx))
}

func upstreamAttemptProviderFromError(err error) string {
	var marked *upstreamExecutionAttemptError
	if errors.As(err, &marked) && marked != nil {
		return strings.TrimSpace(marked.provider)
	}
	return ""
}

func hasUpstreamExecutionAttempt(err error) bool {
	var marked *upstreamExecutionAttemptError
	return errors.As(err, &marked) && marked != nil
}

func unwrapUpstreamExecutionAttempt(err error) error {
	marked, ok := err.(*upstreamExecutionAttemptError)
	if !ok || marked == nil || marked.cause == nil {
		return err
	}
	return marked.cause
}

func unwrapExecutionBoundaryError(err error) error {
	unwrapped, _ := unwrapExecutionBoundaryErrorChanged(err)
	return unwrapped
}

func unwrapExecutionBoundaryErrorChanged(err error) (error, bool) {
	changed := false
	for err != nil {
		switch wrapped := err.(type) {
		case requestStopError:
			err = wrapped.error
			changed = true
		case schedulerRejectError:
			err = wrapped.error
			changed = true
		case *roundEndedBySchedulerRejectError:
			if wrapped == nil || wrapped.cause == nil {
				return err, changed
			}
			err = wrapped.cause
			changed = true
		case *upstreamExecutionAttemptError:
			if wrapped == nil || wrapped.cause == nil {
				return err, changed
			}
			err = wrapped.cause
			changed = true
		case *credentialTerminatedError:
			if wrapped == nil || wrapped.terminal == nil {
				return err, changed
			}
			err = wrapped.terminal
			changed = true
		case requestScopedContinueError:
			inner, innerChanged := unwrapExecutionBoundaryErrorChanged(wrapped.error)
			if !innerChanged {
				return err, changed
			}
			return requestScopedContinueError{error: inner}, true
		case *streamHeaderError:
			if wrapped == nil {
				return nil, changed
			}
			inner, innerChanged := unwrapExecutionBoundaryErrorChanged(wrapped.cause)
			if !innerChanged {
				return err, changed
			}
			return newStreamHeaderError(inner, wrapped.headers), true
		case *streamBootstrapError:
			if wrapped == nil {
				return nil, changed
			}
			inner, innerChanged := unwrapExecutionBoundaryErrorChanged(wrapped.cause)
			if !innerChanged {
				return err, changed
			}
			copied := *wrapped
			copied.cause = inner
			return &copied, true
		default:
			return err, changed
		}
	}
	return nil, changed
}

func unwrapStoppedStreamError(err error) error {
	unwrapped := unwrapExecutionBoundaryError(err)
	if bootstrap, ok := unwrapped.(*streamBootstrapError); ok && bootstrap != nil {
		return newStreamHeaderError(bootstrap.cause, bootstrap.headers)
	}
	return unwrapped
}

func roundPickFailureError(lastErr, upstreamErr, errPick error) error {
	preferred := preferredExecutionAttemptError(lastErr, upstreamErr)
	if isSchedulerRejectError(errPick) {
		return markRoundEndedBySchedulerReject(preferred)
	}
	return preferred
}

func preferredExecutionAttemptError(fallback, upstream error) error {
	if errors.Is(fallback, context.Canceled) || errors.Is(fallback, context.DeadlineExceeded) || isSessionAffinityRetryError(fallback) {
		return fallback
	}
	if upstream == nil {
		return fallback
	}
	var exhausted *homeRetryRoundExhaustedError
	if errors.As(fallback, &exhausted) && exhausted != nil {
		upstreamProvider := upstreamAttemptProviderFromError(upstream)
		upstream = unwrapUpstreamExecutionAttempt(upstream)
		var previousRound *homeRetryRoundExhaustedError
		if errors.As(upstream, &previousRound) && previousRound != nil && previousRound.cause != nil {
			upstream = previousRound.cause
		}
		// Keep the current Home round marker because it owns authoritative retry timing.
		preferred := *exhausted
		preferred.cause = upstream
		return markUpstreamExecutionAttemptWithProvider(&preferred, upstreamProvider)
	}
	return markUpstreamExecutionAttempt(upstream)
}

func (m *Manager) normalizeSessionAffinityNeverTerminalError(err error, providers []string, model string, opts cliproxyexecutor.Options) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if isRequestTerminatedError(err) || isCredentialTerminatedError(err) || isRequestStopError(err) || isSchedulerRejectError(err) || isRequestInvalidError(err) || isSessionAffinityRetryError(err) {
		return err
	}
	var authErr *Error
	if errors.As(err, &authErr) && authErr != nil {
		switch authErr.Code {
		case "session_auth_unavailable", "auth_not_found", "auth_unavailable", "model_cooldown", "provider_not_found", "executor_not_found":
			return err
		}
	}
	var cooldownErr *modelCooldownError
	if errors.As(err, &cooldownErr) && cooldownErr != nil {
		return err
	}
	affinity, ok := m.Selector().(*SessionAffinitySelector)
	if !ok || affinity == nil || !affinity.rebindOnNever {
		return err
	}
	if _, okBound := affinity.boundAuthForRequest(providers, model, opts); !okBound {
		return err
	}
	rerr := resultErrorFromError(err)
	if shouldRebindSessionAffinityOnError(rerr) {
		return err
	}
	if shouldSkipCredentialCooldown(rerr) && !isTransientTransportResultError(rerr) && !isConnectionLifecycleResultError(rerr) {
		return err
	}
	return &Error{
		Code:       "session_auth_retry",
		Message:    "bound session credential is transiently unavailable",
		Retryable:  true,
		HTTPStatus: http.StatusServiceUnavailable,
	}
}

// Execute performs a non-streaming execution using the configured selector and executor.
// It supports multiple providers for the same model and round-robins the starting provider per model.
func isOpenAIImageExecutionOptions(opts cliproxyexecutor.Options) bool {
	return strings.EqualFold(strings.TrimSpace(opts.SourceFormat.String()), "openai-image")
}

func (m *Manager) Execute(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	scope := m.beginInFlight("")
	defer func() { scope.end() }()
	ctx = cliproxyexecutor.WithRequestProxyURL(ctx, opts.ProxyURL)
	req, opts = cliproxysession.Enrich(req, opts)
	ctx = WithCodexClientRequest(ctx, isOpenAIImageExecutionOptions(opts) || IsCodexClientRequest(ctx, opts.Headers))
	ctx = coreusage.CaptureIdentity(ctx)
	ctx = contextWithInFlightScope(ctx, scope)
	ctx = withRequestRefusedAuths(ctx)
	ctx = withAntigravityAttemptedModels(ctx)
	normalized := m.normalizeProviders(providers)
	if len(normalized) == 0 {
		return cliproxyexecutor.Response{}, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}
	opts, pinnedThread, errThread := withClaudeThreadAuth(ctx, normalized, req, opts)
	if errThread != nil {
		return cliproxyexecutor.Response{}, errThread
	}
	if m.HomeEnabled() {
		resp, errHome := m.executeHome(ctx, normalized, req, opts, false)
		if pinnedThread && errHome != nil && !isRequestStopError(errHome) {
			return resp, claudeThreadReplayError(unwrapExecutionBoundaryError(errHome))
		}
		return resp, unwrapExecutionBoundaryError(errHome)
	}

	defaultRequestRetry, maxRetryCredentials, maxWait := m.retrySettings()

	var lastErr error
	var preferredUpstreamErr error
	var selectionCooldownDeadline time.Time
	var terminalRoundRejectedByScheduler bool
	retryModel := authSelectionModelFromOptions(opts, req.Model)
	continuitySession := ensureContinuitySession(ctx, &opts)
	if continuitySession.terminated {
		termErr := continuitySession.terminalErr
		if termErr == nil {
			termErr = &Error{Code: "continuity_terminated", Message: "continuity terminated"}
		}
		return cliproxyexecutor.Response{}, markContinuityTerminatedError(termErr)
	}
	for attempt := 0; ; attempt++ {
		if m.continuityDeadlineExceeded(continuitySession) {
			continuitySession.budgetExhausted = true
			continuitySession.terminated = true
			break
		}
		roundAttempted := make(map[string]struct{})
		roundOpts := withAttemptedAuthTracker(opts, roundAttempted)
		resp, errExec := m.executeMixedOnce(ctx, normalized, req, roundOpts, maxRetryCredentials, attempt, defaultRequestRetry)
		if len(roundAttempted) > 0 {
			continuitySession.priorAttemptSeen = true
		}
		if errExec == nil {
			continuitySession.priorAttemptSeen = true
			return resp, nil
		}
		if isTerminalRequestStopError(errExec) {
			return cliproxyexecutor.Response{}, unwrapExecutionBoundaryError(errExec)
		}
		if isCredentialTerminatedError(errExec) {
			if preferredUpstreamErr == nil {
				return cliproxyexecutor.Response{}, unwrapExecutionBoundaryError(errExec)
			}
			break
		}
		upstreamAttempted := hasUpstreamExecutionAttempt(errExec)
		if !continuitySession.priorAttemptSeen && lastErr == nil && preferredUpstreamErr == nil && !upstreamAttempted && isPreAdmissionContinuityError(errExec) {
			outcome, errContinuity := m.runCodexContinuityGate(ctx, continuitySession, "pre_admission", normalized, retryModel, opts, attempt, false, defaultRequestRetry)
			if errContinuity != nil {
				return cliproxyexecutor.Response{}, errContinuity
			}
			if outcome == continuityOutcomeReady {
				if continuitySession.waitedLastGate {
					continuitySession.immediateReadyRetries = 0
					attempt--
					continue
				}
				continuitySession.immediateReadyRetries++
				if continuitySession.immediateReadyRetries <= 1 {
					attempt--
					continue
				}
			}
		}
		if isSchedulerRejectError(errExec) {
			if lastErr == nil && preferredUpstreamErr == nil {
				return cliproxyexecutor.Response{}, unwrapExecutionBoundaryError(errExec)
			}
			terminalRoundRejectedByScheduler = true
			break
		}
		terminalRoundRejectedByScheduler = hasRoundEndedBySchedulerReject(errExec)
		observeSelectionCooldownDeadline(&selectionCooldownDeadline, errExec, time.Now())
		continuitySession.priorAttemptSeen = true
		if upstreamAttempted {
			preferredUpstreamErr = errExec
		}
		lastErr = keepPreferredCandidateError(lastErr, errExec)
		if upstreamAttempted && m.isCodexAttemptFailure(errExec, roundAttempted) {
			eligibility := authSelectionEligibilityForRequest(ctx, opts)
			pinnedAuthID := pinnedAuthIDFromMetadata(opts.Metadata)
			if _, okContinuity := m.canRunCodexContinuity(ctx, normalized); okContinuity &&
				isPositiveQuotaRejectionError(errExec) {
				if !m.retryAllowedForRequest(ctx, attempt, normalized, retryModel, eligibility, pinnedAuthID, defaultRequestRetry, opts.Headers) {
					continuitySession.terminated = true
					break
				}
				outcome, errContinuity := m.runCodexContinuityGate(ctx, continuitySession, "pre_output_429", normalized, retryModel, opts, attempt+1, true, defaultRequestRetry)
				if errContinuity != nil {
					return cliproxyexecutor.Response{}, errContinuity
				}
				if outcome == continuityOutcomeReady {
					continuitySession.immediateReadyRetries = 0
					continue
				}
				break
			}
		}
		if continuitySession.terminated || continuitySession.budgetExhausted {
			break
		}
		wait, shouldRetry := m.shouldRetryAfterErrorWithAttempted(ctx, opts, errExec, attempt, normalized, retryModel, maxWait, -1, defaultRequestRetry, roundAttempted)
		if !shouldRetry {
			break
		}
		exceeded, errWait := m.waitForContinuityBoundedCooldown(ctx, continuitySession, wait, maxWait)
		if errWait != nil {
			return cliproxyexecutor.Response{}, errWait
		}
		if exceeded {
			break
		}
	}
	if lastErr == nil && continuitySession.terminated {
		termErr := continuitySession.terminalErr
		if termErr == nil {
			termErr = &Error{Code: "continuity_terminated", Message: "continuity terminated"}
		}
		return cliproxyexecutor.Response{}, markContinuityTerminatedError(termErr)
	}
	if lastErr != nil {
		if ctx != nil {
			if errCtx := ctx.Err(); errCtx != nil {
				return cliproxyexecutor.Response{}, errCtx
			}
		}
		lastErr = preferredExecutionAttemptError(lastErr, preferredUpstreamErr)
		lastErr = unwrapExecutionBoundaryError(lastErr)
		lastErr = m.normalizeSessionAffinityNeverTerminalError(lastErr, normalized, retryModel, opts)
		if !terminalRoundRejectedByScheduler && !continuitySession.terminated && hasAntigravityProvider(normalized) && shouldAttemptAntigravityCreditsFallback(m, lastErr, normalized) {
			if resp, ok, errCredits := m.tryAntigravityCreditsExecute(ctx, req, opts); errCredits != nil {
				if isTerminalRequestStopError(errCredits) {
					return cliproxyexecutor.Response{}, unwrapExecutionBoundaryError(errCredits)
				}
				errCredits = unwrapExecutionBoundaryError(errCredits)
				if pinnedThread {
					errCredits = claudeThreadReplayError(errCredits)
				}
				return cliproxyexecutor.Response{}, errCredits
			} else if ok {
				return resp, nil
			}
		}
		lastErr = m.annotatePoolCooldownTerminalError(ctx, normalized, retryModel, opts, lastErr, selectionCooldownDeadline)
		if pinnedThread {
			lastErr = claudeThreadReplayError(lastErr)
		}
		if continuitySession.terminated {
			continuitySession.terminalErr = lastErr
			lastErr = markContinuityTerminatedError(lastErr)
		}
		return cliproxyexecutor.Response{}, lastErr
	}
	errFinal := error(&Error{Code: "auth_not_found", Message: "no auth available"})
	if pinnedThread {
		errFinal = claudeThreadReplayError(errFinal)
	}
	return cliproxyexecutor.Response{}, errFinal
}

// It supports multiple providers for the same model and round-robins the starting provider per model.
func (m *Manager) ExecuteCount(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	scope := m.beginInFlight("")
	defer func() { scope.end() }()
	ctx = cliproxyexecutor.WithRequestProxyURL(ctx, opts.ProxyURL)
	req, opts = cliproxysession.Enrich(req, opts)
	ctx = WithCodexClientRequest(ctx, IsCodexClientRequest(ctx, opts.Headers))
	ctx = coreusage.CaptureIdentity(ctx)
	ctx = contextWithInFlightScope(ctx, scope)
	ctx = withRequestRefusedAuths(ctx)
	ctx = withAntigravityAttemptedModels(ctx)
	normalized := m.normalizeProviders(providers)
	if len(normalized) == 0 {
		return cliproxyexecutor.Response{}, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}
	if m.HomeEnabled() {
		resp, errHome := m.executeHome(ctx, normalized, req, opts, true)
		return resp, unwrapExecutionBoundaryError(errHome)
	}

	defaultRequestRetry, maxRetryCredentials, maxWait := m.retrySettings()

	var lastErr error
	var preferredUpstreamErr error
	var selectionCooldownDeadline time.Time
	retryModel := authSelectionModelFromOptions(opts, req.Model)
	for attempt := 0; ; attempt++ {
		roundAttempted := make(map[string]struct{})
		roundOpts := withAttemptedAuthTracker(opts, roundAttempted)
		resp, errExec := m.executeCountMixedOnce(ctx, normalized, req, roundOpts, maxRetryCredentials, attempt, defaultRequestRetry)
		if errExec == nil {
			return resp, nil
		}
		if isTerminalRequestStopError(errExec) {
			return cliproxyexecutor.Response{}, unwrapExecutionBoundaryError(errExec)
		}
		if isCredentialTerminatedError(errExec) {
			if preferredUpstreamErr == nil {
				return cliproxyexecutor.Response{}, unwrapExecutionBoundaryError(errExec)
			}
			break
		}
		if isSchedulerRejectError(errExec) {
			if lastErr == nil && preferredUpstreamErr == nil {
				return cliproxyexecutor.Response{}, unwrapExecutionBoundaryError(errExec)
			}
			break
		}
		observeSelectionCooldownDeadline(&selectionCooldownDeadline, errExec, time.Now())
		if hasUpstreamExecutionAttempt(errExec) {
			preferredUpstreamErr = errExec
		}
		lastErr = keepPreferredCandidateError(lastErr, errExec)
		wait, shouldRetry := m.shouldRetryAfterErrorWithAttempted(ctx, opts, errExec, attempt, normalized, retryModel, maxWait, -1, defaultRequestRetry, roundAttempted)
		if !shouldRetry {
			break
		}
		if errWait := waitForCooldown(ctx, wait, maxWait); errWait != nil {
			return cliproxyexecutor.Response{}, errWait
		}
	}
	if lastErr != nil {
		if ctx != nil {
			if errCtx := ctx.Err(); errCtx != nil {
				return cliproxyexecutor.Response{}, errCtx
			}
		}
		lastErr = preferredExecutionAttemptError(lastErr, preferredUpstreamErr)
		lastErr = unwrapExecutionBoundaryError(lastErr)
		lastErr = m.normalizeSessionAffinityNeverTerminalError(lastErr, normalized, retryModel, opts)
		lastErr = m.annotatePoolCooldownTerminalError(ctx, normalized, retryModel, opts, lastErr, selectionCooldownDeadline)
		return cliproxyexecutor.Response{}, lastErr
	}
	return cliproxyexecutor.Response{}, &Error{Code: "auth_not_found", Message: "no auth available"}
}

// ExecuteStream performs a streaming execution using the configured selector and executor.
// It supports multiple providers for the same model and round-robins the starting provider per model.
func (m *Manager) ExecuteStream(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (result *cliproxyexecutor.StreamResult, err error) {
	scope := m.beginInFlight("")
	defer func() {
		if scope != nil {
			result = wrapInFlightStreamResult(result, scope, ctx)
			scope = nil
		}
	}()
	ctx = cliproxyexecutor.WithRequestProxyURL(ctx, opts.ProxyURL)
	req, opts = cliproxysession.Enrich(req, opts)
	ctx = WithCodexClientRequest(ctx, isOpenAIImageExecutionOptions(opts) || IsCodexClientRequest(ctx, opts.Headers))
	ctx = coreusage.CaptureIdentity(ctx)
	ctx = contextWithInFlightScope(ctx, scope)
	ctx = withRequestRefusedAuths(ctx)
	ctx = withAntigravityAttemptedModels(ctx)
	if m.HomeEnabled() {
		if unlockSession := m.lockHomeWebsocketSession(ctx, opts); unlockSession != nil {
			defer unlockSession()
		}
	}
	normalized := m.normalizeProviders(providers)
	if len(normalized) == 0 {
		return nil, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}
	opts, pinnedThread, errThread := withClaudeThreadAuth(ctx, normalized, req, opts)
	if errThread != nil {
		return nil, errThread
	}

	defaultRequestRetry, maxRetryCredentials, maxWait := m.retrySettings()

	var lastErr error
	var preferredUpstreamErr error
	var selectionCooldownDeadline time.Time
	var terminalRoundRejectedByScheduler bool
	homeRetryLimit := -1
	retryModel := authSelectionModelFromOptions(opts, req.Model)
	continuitySession := ensureContinuitySession(ctx, &opts)
	if continuitySession.terminated {
		termErr := continuitySession.terminalErr
		if termErr == nil {
			termErr = &Error{Code: "continuity_terminated", Message: "continuity terminated"}
		}
		termErr = markContinuityTerminatedError(termErr)
		var bootstrapErr *streamBootstrapError
		if errors.As(termErr, &bootstrapErr) && bootstrapErr != nil {
			return streamErrorResult(bootstrapErr.Headers(), termErr), nil
		}
		return nil, termErr
	}
	_, shareStreamAttempts := m.canRunCodexContinuity(ctx, normalized)
	attempt := 0
	if shareStreamAttempts {
		attempt = continuitySession.codexAttemptsUsed
	}
	retryRoundPending := false
	retryRoundWaited := false
	for {
		if m.continuityDeadlineExceeded(continuitySession) {
			continuitySession.budgetExhausted = true
			continuitySession.terminated = true
			break
		}
		roundAttempted := make(map[string]struct{})
		roundOpts := withAttemptedAuthTracker(opts, roundAttempted)
		continuitySession.setLastStreamContext(normalized, req.Model, opts)
		continuitySession.setLastStreamProvider("")
		result, errStream := m.executeStreamMixedOnce(ctx, normalized, req, roundOpts, maxRetryCredentials, &homeRetryLimit, attempt, defaultRequestRetry)
		if len(roundAttempted) > 0 {
			continuitySession.priorAttemptSeen = true
		}
		if errStream == nil {
			continuitySession.priorAttemptSeen = true
			return result, nil
		}
		if isTerminalRequestStopError(errStream) {
			// Keep stopped/terminated bootstrap failures as direct errors carrying
			// upstream headers via streamHeaderError rather than streamErrorResult so
			// outer bootstrap retries cannot replay an explicitly stopped request.
			return nil, unwrapStoppedStreamError(errStream)
		}
		if !m.HomeEnabled() {
			observeSelectionCooldownDeadline(&selectionCooldownDeadline, errStream, time.Now())
		}
		upstreamAttempted := hasUpstreamExecutionAttempt(errStream)
		if upstreamAttempted {
			if isDirectStreamTerminalError(errStream) {
				preferredUpstreamErr = errStream
			} else {
				syncPreferredUpstreamError(&preferredUpstreamErr, errStream)
			}
		}
		if m.HomeEnabled() && retryRoundPending {
			if wait, okWait := pendingHomeRetryRoundDelay(errStream, maxWait, &homeRetryLimit, pinnedAuthIDFromMetadata(opts.Metadata) == ""); okWait && m.homeRetryAllowed(attempt-1, homeRetryLimit) {
				if retryRoundWaited {
					errOut := unwrapExecutionBoundaryError(errStream)
					if pinnedThread {
						errOut = claudeThreadReplayError(errOut)
					}
					return nil, errOut
				}
				exceeded, errWait := m.waitForContinuityBoundedCooldown(ctx, continuitySession, wait, maxWait)
				if errWait != nil {
					return nil, errWait
				}
				if exceeded {
					lastErr = errStream
					break
				}
				retryRoundWaited = true
				continue
			}
		}
		retryRoundPending = false
		retryRoundWaited = false
		if isCredentialTerminatedError(errStream) {
			if preferredUpstreamErr == nil {
				return nil, unwrapExecutionBoundaryError(errStream)
			}
			break
		}
		if !continuitySession.priorAttemptSeen && lastErr == nil && preferredUpstreamErr == nil && !upstreamAttempted && isPreAdmissionContinuityError(errStream) {
			outcome, errContinuity := m.runCodexContinuityGate(ctx, continuitySession, "pre_admission", normalized, retryModel, opts, attempt, false, defaultRequestRetry)
			if errContinuity != nil {
				return nil, errContinuity
			}
			if outcome == continuityOutcomeReady {
				if continuitySession.waitedLastGate {
					continuitySession.immediateReadyRetries = 0
					continue
				}
				continuitySession.immediateReadyRetries++
				if continuitySession.immediateReadyRetries <= 1 {
					continue
				}
			}
		}
		if isSchedulerRejectError(errStream) {
			if lastErr == nil && preferredUpstreamErr == nil {
				return nil, unwrapExecutionBoundaryError(errStream)
			}
			terminalRoundRejectedByScheduler = true
			break
		}
		continuitySession.priorAttemptSeen = true
		terminalRoundRejectedByScheduler = hasRoundEndedBySchedulerReject(errStream)
		lastErr = keepPreferredCandidateError(lastErr, errStream)
		if upstreamAttempted && m.isCodexAttemptFailure(errStream, roundAttempted) {
			eligibility := authSelectionEligibilityForRequest(ctx, opts)
			pinnedAuthID := pinnedAuthIDFromMetadata(opts.Metadata)
			if _, okContinuity := m.canRunCodexContinuity(ctx, normalized); okContinuity &&
				isPositiveQuotaRejectionError(errStream) {
				var retryAllowed bool
				if m.HomeEnabled() {
					retryAllowed = m.homeRetryAllowed(attempt, homeRetryLimit)
				} else {
					retryAllowed = m.retryAllowedForRequest(ctx, attempt, normalized, retryModel, eligibility, pinnedAuthID, defaultRequestRetry, opts.Headers)
				}
				if !retryAllowed {
					continuitySession.terminated = true
					break
				}
				outcome, errContinuity := m.runCodexContinuityGate(ctx, continuitySession, "pre_output_429", normalized, retryModel, opts, attempt+1, true, defaultRequestRetry)
				if errContinuity != nil {
					return nil, errContinuity
				}
				if outcome == continuityOutcomeReady {
					continuitySession.immediateReadyRetries = 0
					attempt++
					continuitySession.codexAttemptsUsed++
					continue
				}
				break
			}
		}
		if continuitySession.terminated || continuitySession.budgetExhausted {
			break
		}
		wait, shouldRetry := m.shouldRetryAfterErrorWithAttempted(ctx, opts, errStream, attempt, normalized, retryModel, maxWait, homeRetryLimit, defaultRequestRetry, roundAttempted)
		if !shouldRetry {
			break
		}
		exceeded, errWait := m.waitForContinuityBoundedCooldown(ctx, continuitySession, wait, maxWait)
		if errWait != nil {
			return nil, errWait
		}
		if exceeded {
			break
		}
		attempt++
		if shareStreamAttempts && m.isCodexAttemptFailure(errStream, roundAttempted) {
			continuitySession.codexAttemptsUsed++
		}
		retryRoundPending = m.HomeEnabled()
		retryRoundWaited = false
	}
	if lastErr == nil && continuitySession.terminated {
		termErr := continuitySession.terminalErr
		if termErr == nil {
			termErr = &Error{Code: "continuity_terminated", Message: "continuity terminated"}
		}
		termErr = markContinuityTerminatedError(termErr)
		var bootstrapErr *streamBootstrapError
		if errors.As(termErr, &bootstrapErr) && bootstrapErr != nil {
			return streamErrorResult(bootstrapErr.Headers(), termErr), nil
		}
		return nil, termErr
	}
	if lastErr != nil {
		if ctx != nil {
			if errCtx := ctx.Err(); errCtx != nil {
				return nil, errCtx
			}
		}
		if preferredUpstreamErr != nil && !isDirectStreamTerminalError(lastErr) && (!m.HomeEnabled() || isHomeRetryRoundExhausted(lastErr)) {
			lastErr = preferredExecutionAttemptError(lastErr, preferredUpstreamErr)
		}
		lastErr = unwrapExecutionBoundaryError(lastErr)
		lastErr = m.normalizeSessionAffinityNeverTerminalError(lastErr, normalized, retryModel, opts)
		if !terminalRoundRejectedByScheduler && !continuitySession.terminated && hasAntigravityProvider(normalized) && shouldAttemptAntigravityCreditsFallback(m, lastErr, normalized) {
			if result, ok, errCredits := m.tryAntigravityCreditsExecuteStream(ctx, req, opts); errCredits != nil {
				if isTerminalRequestStopError(errCredits) ||
					isTOSViolationError(errCredits) || isAntigravityValidationRequiredError(errCredits) {
					return nil, unwrapStoppedStreamError(errCredits)
				}
				errCredits = unwrapExecutionBoundaryError(errCredits)
				if pinnedThread {
					errCredits = claudeThreadReplayError(errCredits)
				}
				var bootstrapErr *streamBootstrapError
				if errors.As(errCredits, &bootstrapErr) && bootstrapErr != nil {
					return streamErrorResult(bootstrapErr.Headers(), errCredits), nil
				}
				return nil, errCredits
			} else if ok {
				return result, nil
			}
		}
		if !m.HomeEnabled() {
			lastErr = m.annotatePoolCooldownTerminalError(ctx, normalized, retryModel, opts, lastErr, selectionCooldownDeadline)
		}
		if pinnedThread {
			lastErr = claudeThreadReplayError(lastErr)
		}
		var bootstrapErr *streamBootstrapError
		if errors.As(lastErr, &bootstrapErr) && bootstrapErr != nil {
			streamErr := lastErr
			if continuitySession.terminated {
				continuitySession.terminalErr = lastErr
				streamErr = markContinuityTerminatedError(streamErr)
			}
			return streamErrorResult(bootstrapErr.Headers(), streamErr), nil
		}
		if continuitySession.terminated {
			continuitySession.terminalErr = lastErr
			lastErr = markContinuityTerminatedError(lastErr)
		}
		return nil, lastErr
	}
	errFinal := error(&Error{Code: "auth_not_found", Message: "no auth available"})
	if pinnedThread {
		errFinal = claudeThreadReplayError(errFinal)
	}
	return nil, errFinal
}

type requestToFormatResolver interface {
	RequestToFormat(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) sdktranslator.Format
}

type credentialTerminatedError struct {
	terminal *cliproxyexecutor.RequestTerminatedError
}

func (e *credentialTerminatedError) Error() string {
	if e == nil || e.terminal == nil {
		return "credential terminated by plugin"
	}
	return e.terminal.Error()
}

func isCredentialTerminatedError(err error) bool {
	var credErr *credentialTerminatedError
	return errors.As(err, &credErr) && credErr != nil
}

type requestRefusedAuthsContextKey struct{}

func withRequestRefusedAuths(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Value(requestRefusedAuthsContextKey{}).(map[string]struct{}); ok {
		return ctx
	}
	return context.WithValue(ctx, requestRefusedAuthsContextKey{}, make(map[string]struct{}))
}

func requestRefusedAuthsFromContext(ctx context.Context) map[string]struct{} {
	if ctx == nil {
		return nil
	}
	refused, _ := ctx.Value(requestRefusedAuthsContextKey{}).(map[string]struct{})
	return refused
}

func recordRequestRefusedAuth(ctx context.Context, authID string) {
	if authID == "" {
		return
	}
	if refused := requestRefusedAuthsFromContext(ctx); refused != nil {
		refused[authID] = struct{}{}
	}
}

func isCredentialTerminateScope(scope string) bool {
	return scope == "credential"
}

func keepPreferredCandidateError(current, next error) error {
	if next == nil {
		return current
	}
	if isCredentialTerminatedError(current) && !isCredentialTerminatedError(next) && !hasUpstreamExecutionAttempt(next) {
		return current
	}
	return next
}

func isRequestTerminatedError(err error) bool {
	var terminated *cliproxyexecutor.RequestTerminatedError
	return (errors.As(err, &terminated) && terminated != nil) || cliproxyexecutor.IsUpstreamWebsocketTurnError(err)
}

// prepareRequestAfterAuth plans the upstream route before passing its format to the optional interceptor.
func prepareRequestAfterAuth(ctx context.Context, executor ProviderExecutor, auth *Auth, provider string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, requestedModel string) (cliproxyexecutor.Request, cliproxyexecutor.Options, error) {
	if planner, ok := executor.(interface {
		PlanRequestRoute(*Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) cliproxyexecutor.Options
	}); ok {
		opts = planner.PlanRequestRoute(auth, req, opts)
	}
	if opts.RequestAfterAuthInterceptor == nil {
		return req, opts, nil
	}
	toFormat := requestToFormat(provider, executor, req, opts)
	resp := opts.RequestAfterAuthInterceptor(ctx, cliproxyexecutor.RequestAfterAuthInterceptRequest{
		SourceFormat:   opts.SourceFormat,
		ToFormat:       toFormat,
		Model:          req.Model,
		RequestedModel: requestedModel,
		Stream:         opts.Stream,
		Headers:        cloneRequestHeaders(opts.Headers),
		Body:           req.Payload,
		Metadata:       maps.Clone(opts.Metadata),
	})
	prevHeaders := opts.Headers
	opts.Headers = mergeRequestHeaders(opts.Headers, resp.Headers, resp.ClearHeaders)
	if len(resp.Body) > 0 {
		req.Payload = resp.Body
		opts.OriginalRequest = resp.Body
	}
	metadataCloned := false
	ensureWritableMetadata := func(allocate bool, hint int) {
		if metadataCloned {
			if opts.Metadata == nil && allocate {
				opts.Metadata = make(map[string]any, hint)
			}
			return
		}
		if opts.Metadata == nil {
			if allocate {
				opts.Metadata = make(map[string]any, hint)
				metadataCloned = true
			}
			return
		}
		opts.Metadata = maps.Clone(opts.Metadata)
		metadataCloned = true
	}
	if path := strings.TrimSpace(resp.Path); path != "" {
		ensureWritableMetadata(true, 1)
		opts.Metadata[cliproxyexecutor.RequestPathMetadataKey] = path
	}
	if resp.Terminate {
		terminated := &cliproxyexecutor.RequestTerminatedError{
			HTTPStatus: resp.StatusCode,
			Header:     cloneRequestHeaders(resp.ResponseHeaders),
			Body:       bytes.Clone(resp.ResponseBody),
		}
		if isCredentialTerminateScope(resp.TerminateScope) {
			return req, opts, &credentialTerminatedError{terminal: terminated}
		}
		return req, opts, terminated
	}
	if len(resp.ClearHeaders) > 0 || len(resp.Body) > 0 {
		evalPayload := opts.OriginalRequest
		if len(evalPayload) == 0 {
			evalPayload = req.Payload
		}
		info, ok := cliproxysession.ExtractSessionInfo(opts.Headers, evalPayload, opts.Metadata)
		if ok && info.SessionID != "" {
			ensureWritableMetadata(true, 2)
			opts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey] = cliproxysession.BoundSessionIdentity(info.SessionID)
			if info.ParentSessionID != "" && info.ParentSessionID != info.SessionID {
				opts.Metadata[cliproxyexecutor.ParentSessionIDMetadataKey] = cliproxysession.BoundSessionIdentity(info.ParentSessionID)
			} else {
				delete(opts.Metadata, cliproxyexecutor.ParentSessionIDMetadataKey)
			}
		} else {
			ensureWritableMetadata(false, 0)
			delete(opts.Metadata, cliproxyexecutor.CanonicalSessionIDMetadataKey)
			delete(opts.Metadata, cliproxyexecutor.ParentSessionIDMetadataKey)
			delete(opts.Metadata, cliproxyexecutor.LCPAffinitySessionIDMetadataKey)
		}
	} else if len(resp.Headers) > 0 {
		prevInfo, prevOK := cliproxysession.ExtractSessionInfo(prevHeaders, nil, nil)
		if info, ok := cliproxysession.ExtractSessionInfo(opts.Headers, nil, nil); ok && info.SessionID != "" {
			if !prevOK || prevInfo.SessionID != info.SessionID || prevInfo.ParentSessionID != info.ParentSessionID {
				ensureWritableMetadata(true, 2)
				opts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey] = cliproxysession.BoundSessionIdentity(info.SessionID)
				if info.ParentSessionID != "" && info.ParentSessionID != info.SessionID {
					opts.Metadata[cliproxyexecutor.ParentSessionIDMetadataKey] = cliproxysession.BoundSessionIdentity(info.ParentSessionID)
				} else {
					delete(opts.Metadata, cliproxyexecutor.ParentSessionIDMetadataKey)
				}
			}
		}
	}
	return req, opts, nil
}

func requestToFormat(provider string, executor ProviderExecutor, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) sdktranslator.Format {
	resolver, ok := executor.(requestToFormatResolver)
	if ok && resolver != nil {
		formatRequestTo := resolver.RequestToFormat(req, opts)
		if formatRequestTo != "" {
			return formatRequestTo
		}
	}
	source := opts.SourceFormat.String()
	if source == "openai-image" || source == "openai-video" || source == "openai-speech" {
		return opts.SourceFormat
	}
	if opts.Alt == "responses/compact" && !opts.Stream {
		return sdktranslator.FormatOpenAIResponse
	}
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "codex":
		return sdktranslator.FormatCodex
	case "xai":
		return sdktranslator.FormatCodex
	case "claude":
		return sdktranslator.FormatClaude
	case "gemini", "vertex", "aistudio":
		return sdktranslator.FormatGemini
	case "kimi", "kimi-ai", "kimi.ai", "kimi.com":
		return sdktranslator.FormatOpenAI
	case "meta":
		return sdktranslator.FormatCodex
	case "antigravity":
		return sdktranslator.FormatAntigravity
	case "devin":
		return sdktranslator.FormatInteractions
	default:
		return sdktranslator.FormatOpenAI
	}
}

func cloneRequestHeaders(src http.Header) http.Header {
	if src == nil {
		return nil
	}
	dst := make(http.Header, len(src))
	for key, values := range src {
		dst[key] = append([]string(nil), values...)
	}
	return dst
}

func mergeRequestHeaders(current, updates http.Header, clear []string) http.Header {
	if updates == nil && len(clear) == 0 {
		return current
	}
	out := cloneRequestHeaders(current)
	if out == nil && (len(updates) > 0 || len(clear) > 0) {
		out = make(http.Header)
	}
	for _, key := range clear {
		deleteRequestHeaderFold(out, key)
	}
	for key, values := range updates {
		deleteRequestHeaderFold(out, key)
		for _, value := range values {
			out.Add(key, value)
		}
	}
	return out
}

func deleteRequestHeaderFold(headers http.Header, key string) {
	if len(headers) == 0 {
		return
	}
	headers.Del(key)
	for existingKey := range headers {
		if strings.EqualFold(existingKey, key) {
			delete(headers, existingKey)
		}
	}
}

func executorForAuth(executor ProviderExecutor, auth *Auth) ProviderExecutor {
	if auth.AuthKind() == AuthKindAPIKey {
		if scoped, ok := executor.(APIKeyConfigExecutor); ok {
			return scoped.ForAPIKey()
		}
	}
	return executor
}

// syncPreferredUpstreamError refreshes the error that terminal selection
// will prefer over the last local failure when the attempt error gained a
// trusted retry annotation after its result was recorded.
func syncPreferredUpstreamError(upstreamErr *error, errExec error) {
	if upstreamErr == nil || errExec == nil || !hasUpstreamExecutionAttempt(errExec) {
		return
	}
	// A stream bootstrap wrapper built from an earlier attempt already
	// carries the response headers needed to hand that failure back as an
	// error stream; keep that earlier failure preferred. Only a hint
	// raised for the very same upstream attempt may annotate that wrapper
	// in place; a hint from a different credential attempt must not mutate
	// an earlier bootstrap, even when both attempts surfaced an equal
	// error value.
	var bootstrap *streamBootstrapError
	if errors.As(*upstreamErr, &bootstrap) && bootstrap != nil {
		if upstreamErrorMatchesBootstrap(errExec, bootstrap) {
			if hint := trustedRetryAfterHint(errExec); hint != nil && *hint > 0 {
				bootstrap.cause = withTrustedRetryAfter(bootstrap.cause, hint)
			}
			if IsRequestScopedContinueError(errExec) {
				bootstrap.cause = wrapRequestScopedContinueError(bootstrap.cause)
			}
			return
		}
		// A synthetic empty_stream wrapper must not mask a later real upstream
		// failure, and a headerless bootstrap wrapper can advance to a later
		// real bootstrap failure while still staying preferred over headerless
		// stream-start errors or empty_stream.
		if isEmptyStreamError(bootstrap.cause) && !isEmptyStreamError(errExec) {
			*upstreamErr = errExec
			return
		}
		var execBootstrap *streamBootstrapError
		if len(bootstrap.headers) == 0 && errors.As(errExec, &execBootstrap) && execBootstrap != nil && !isEmptyStreamError(execBootstrap.cause) {
			*upstreamErr = errExec
		}
		return
	}
	*upstreamErr = errExec
}

func isEmptyStreamError(err error) bool {
	var authErr *Error
	return errors.As(err, &authErr) && authErr != nil && authErr.Code == "empty_stream"
}

func isDirectStreamTerminalError(err error) bool {
	if err == nil {
		return false
	}
	var bootstrap *streamBootstrapError
	if errors.As(err, &bootstrap) && bootstrap != nil {
		return false
	}
	var headerErr *streamHeaderError
	if errors.As(err, &headerErr) && headerErr != nil {
		return true
	}
	if isTerminalRequestStopError(err) || isTOSViolationError(err) {
		return true
	}
	return !IsRequestScopedContinueError(err) && isRequestInvalidError(err)
}

// upstreamErrorMatchesBootstrap reports whether err belongs to the same
// upstream attempt that produced bootstrap. Attempt markers carry a
// process-unique id that survives annotation rebuilds, so a hint refreshed
// for the same attempt matches while a different credential attempt is
// rejected even when both surfaced the very same error value. The id is
// read from the innermost source the attempt owns: a nested bootstrap
// wrapper's recorded id first, then the outer attempt marker. Only when
// neither side carries an attempt id (for example tests that bypass the
// marking helpers) does the check fall back to cause identity.
func upstreamErrorMatchesBootstrap(err error, bootstrap *streamBootstrapError) bool {
	if err == nil || bootstrap == nil {
		return false
	}
	execAttemptID := uint64(0)
	var execBootstrap *streamBootstrapError
	if errors.As(err, &execBootstrap) && execBootstrap != nil {
		execAttemptID = execBootstrap.attemptID
	}
	if execAttemptID == 0 {
		var marked *upstreamExecutionAttemptError
		if errors.As(err, &marked) && marked != nil {
			execAttemptID = marked.attemptID
		}
	}
	if execAttemptID != 0 && bootstrap.attemptID != 0 {
		return execAttemptID == bootstrap.attemptID
	}
	return sameInterfaceHeader(unwrapBootstrapSyncError(err), bootstrap.cause)
}

// unwrapBootstrapSyncError peels the wrappers syncPreferredUpstreamError
// deals with so the underlying failure can be compared by identity.
func unwrapBootstrapSyncError(err error) error {
	for err != nil {
		switch wrapped := err.(type) {
		case *upstreamExecutionAttemptError:
			err = wrapped.cause
		case *trustedRetryAfterError:
			err = wrapped.cause
		case *streamBootstrapError:
			err = wrapped.cause
		case requestScopedContinueError:
			err = wrapped.error
		default:
			return err
		}
	}
	return nil
}

func (m *Manager) executeMixedOnce(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, maxRetryCredentials int, retryRound int, defaultRequestRetry int) (cliproxyexecutor.Response, error) {
	if len(providers) == 0 {
		return cliproxyexecutor.Response{}, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}
	routeModel := authSelectionModelFromOptions(opts, req.Model)
	executionModel, restoreExecutionModel := executionModelForAuthSelection(opts, req.Model)
	opts = ensureRequestedModelMetadata(opts, routeModel)
	clearSchedulerSelectionID(opts.Metadata)
	baseMetadata := opts.Metadata
	homeMode := m.HomeEnabled()
	homeAuthCount := 1
	tried := make(map[string]struct{})
	for authID := range requestRefusedAuthsFromContext(ctx) {
		tried[authID] = struct{}{}
	}
	if !homeMode {
		for authID := range m.requestRetryRoundExclusions(retryRound, defaultRequestRetry) {
			tried[authID] = struct{}{}
		}
	}
	attempted := make(map[string]struct{})
	lastAttemptedAuthID := ""
	var lastErr error
	var upstreamErr error
	for {
		if maxRetryCredentials > 0 && len(attempted) >= maxRetryCredentials {
			if lastErr != nil {
				return cliproxyexecutor.Response{}, preferredExecutionAttemptError(lastErr, upstreamErr)
			}
			return cliproxyexecutor.Response{}, &Error{Code: "auth_not_found", Message: "no auth available"}
		}
		opts.Metadata = cloneRequestMetadata(baseMetadata)
		if !homeMode && lastAttemptedAuthID != "" {
			// Preserve the mid-round selected_auth_id signal for scheduler plugins (#7434)
			// as a one-shot signal after an executed failure without carrying over
			// affinity/LCP, selection_id, or index keys from prior attempts.
			opts.Metadata[cliproxyexecutor.SelectedAuthMetadataKey] = lastAttemptedAuthID
			lastAttemptedAuthID = ""
		}
		pickOpts := opts
		if homeMode {
			pickOpts = withHomeRetryRound(pickOpts, retryRound)
			pickOpts = withHomeAuthCount(pickOpts, homeAuthCount)
			pickOpts = withHomeExcludedAuthIDs(pickOpts, tried)
		}
		auth, executor, provider, errPick := m.pickNextMixed(ctx, providers, routeModel, pickOpts, tried)
		if errPick == nil {
			updateInFlightScopeProvider(ctx, provider)
		}
		if errPick != nil {
			if shouldReturnLastErrorOnPickFailure(homeMode, lastErr, errPick) {
				return cliproxyexecutor.Response{}, roundPickFailureError(lastErr, upstreamErr, errPick)
			}
			return cliproxyexecutor.Response{}, errPick
		}

		entry := logEntryWithRequestID(ctx)
		debugLogAuthSelection(entry, auth, provider, routeModel)
		tried[auth.ID] = struct{}{}
		execCtx := ctx
		execCtx = m.WithAuthTransport(execCtx, auth)
		execCtx = m.withAntigravityModelState(execCtx, auth)
		execCtx = contextWithRequestedModelAlias(execCtx, pickOpts, routeModel)
		execCtx = coreusage.WithSelectionID(execCtx, selectionIDFromOptions(pickOpts))
		execCtx = m.newUpstreamAttemptContext(execCtx)

		models, pooled, aliasResult, routing := m.preparedExecutionModelsWithAliasForRequest(execCtx, auth, routeModel, pickOpts.Headers)
		if len(models) == 0 {
			continue
		}
		attempted[auth.ID] = struct{}{}
		if authID := strings.TrimSpace(auth.ID); authID != "" {
			lastAttemptedAuthID = authID
		}
		publishSelectedAuthMetadata(opts.Metadata, auth)
		if homeMode {
			syncPublishedSelectedAuthMetadata(pickOpts.Metadata, opts.Metadata)
		}
		preparedAuth, errPrepare := m.prepareRequestAuth(execCtx, executor, auth)
		if errPrepare != nil {
			if preparedAuth != nil {
				auth = preparedAuth
			}
			errPrepare = markUpstreamExecutionAttemptFromContext(execCtx, errPrepare)
			if errCtx := execCtx.Err(); errCtx != nil {
				return cliproxyexecutor.Response{}, errCtx
			}
			if errCancel := claudeOAuthRequestCancellation(execCtx, auth, errPrepare); errCancel != nil {
				return cliproxyexecutor.Response{}, errCancel
			}
			stateModel := m.selectionModelKeyForAuth(auth, routeModel)
			if stateModel == "" {
				stateModel = canonicalModelKey(routeModel)
			}
			result := Result{AuthID: auth.ID, Provider: provider, Model: stateModel, RouteModel: routeModel, Success: false, Error: resultErrorFromError(errPrepare), Options: pickOpts, AccessToken: authAccessTokenForRequest(execCtx, auth, pickOpts.Headers), RefreshToken: authRefreshToken(auth), CredentialVersion: auth.CredentialVersion, RegistrationEpoch: auth.RegistrationEpoch}
			if ra := retryAfterFromError(errPrepare); ra != nil {
				result.RetryAfter = ra
			}
			if isCredentialScopedError(errPrepare) {
				result.CredentialScope = true
			}
			action, okAction := matchRequestScopedErrorAction(auth, errPrepare, m.requestScopedErrorConfig(execCtx))
			applyRequestScopedActionToResult(action, okAction, &result)
			m.MarkResult(execCtx, result)
			errPrepare = annotateTrustedRetryAfterByHint(errPrepare)
			syncPreferredUpstreamError(&upstreamErr, errPrepare)
			if isRequestScopedStop(action, okAction) {
				return cliproxyexecutor.Response{}, wrapRequestStopError(errPrepare)
			}
			if !okAction && isRequestInvalidError(errPrepare) {
				return cliproxyexecutor.Response{}, errPrepare
			}
			lastErr = keepPreferredCandidateError(lastErr, errPrepare)
			continue
		}
		if preparedAuth != nil {
			auth = preparedAuth
		}
		executor = executorForAuth(executor, auth)
		var authErr error
		didRefreshOnUnauthorized := false
		for _, upstreamModel := range models {
			execCtx = m.newUpstreamAttemptContext(execCtx)
			resultModel := m.stateModelForExecution(auth, routeModel, upstreamModel, pooled)
			execReq := req
			execReq.Model = upstreamModel
			if restoreExecutionModel {
				execReq.Model = executionModel
			}
			execOpts := opts
			if pickOpts.Metadata != nil {
				if canonicalID, ok := pickOpts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]; ok {
					meta := make(map[string]any, len(execOpts.Metadata)+2)
					for k, v := range execOpts.Metadata {
						meta[k] = v
					}
					meta[cliproxyexecutor.CanonicalSessionIDMetadataKey] = canonicalID
					if parentID, okParent := pickOpts.Metadata[cliproxyexecutor.ParentSessionIDMetadataKey]; okParent && parentID != canonicalID {
						meta[cliproxyexecutor.ParentSessionIDMetadataKey] = parentID
					} else {
						delete(meta, cliproxyexecutor.ParentSessionIDMetadataKey)
					}
					execOpts.Metadata = meta
				}
			}
			payload := execOpts.OriginalRequest
			if len(payload) == 0 {
				payload = execReq.Payload
			}
			execOpts.Metadata = ensureCanonicalSessionMetadata(execOpts.Metadata, execOpts.Headers, payload)
			var errIntercept error
			execReq, execOpts, errIntercept = prepareRequestAfterAuth(execCtx, executor, auth, provider, execReq, execOpts, requestedModelAliasFromOptions(execOpts, routeModel))
			if errIntercept != nil {
				if isCredentialTerminatedError(errIntercept) {
					recordRequestRefusedAuth(ctx, auth.ID)
					authErr = errIntercept
					break
				}
				return cliproxyexecutor.Response{}, errIntercept
			}
			execReq = attachResolvedExecutionModelInfo(routing, execReq, auth, routeModel, upstreamModel, restoreExecutionModel)
			var activeAuth atomic.Pointer[Auth]
			activeAuth.Store(auth)
			syncActiveAuth := func() {
				if currentAuth := activeAuth.Load(); currentAuth != nil && currentAuth != auth {
					auth = currentAuth
				}
			}
			refreshExecCtx := func(base context.Context) context.Context {
				return m.withExecutionAuthRefresh(base, executionAuthRefreshOptions{
					onAuthUpdated: func(updated *Auth) {
						if updated != nil {
							activeAuth.Store(updated.Clone())
						}
					},
				})
			}
			execCtx = refreshExecCtx(withUpstreamAttemptProvider(syncMetadataSessionToContext(execCtx, execOpts.Metadata), provider))
			executionSeq := m.nextExecutionSeq()
			startExec := time.Now()
			var preRefreshExecErr error
			resp, errExec := executor.Execute(execCtx, auth, execReq, execOpts)
			syncActiveAuth()
			errExec = markUpstreamExecutionAttemptFromContext(execCtx, errExec)
			durationExec := time.Since(startExec)
			if errExec != nil {
				if errCtx := execCtx.Err(); errCtx != nil {
					m.recordCanceledUpstreamCooldown(execCtx, auth, provider, resultModel, routeModel, execOpts, errExec, false, executionSeq)
					return cliproxyexecutor.Response{}, errCtx
				}
				refreshCtx := newUpstreamAttemptContext(execCtx)
				if refreshed, okRefresh := m.tryRefreshAfterUnauthorized(refreshCtx, auth, errExec, didRefreshOnUnauthorized, execOpts.Headers); okRefresh {
					preRefreshExecErr = errExec
					auth = refreshed
					activeAuth.Store(auth)
					publishSelectedAuthMetadata(execOpts.Metadata, auth)
					didRefreshOnUnauthorized = true
					execCtx = refreshExecCtx(withUpstreamAttemptProvider(m.newUpstreamAttemptContext(execCtx), provider))
					execCtx = syncMetadataSessionToContext(execCtx, execOpts.Metadata)
					executionSeq = m.nextExecutionSeq()
					startRetry := time.Now()
					resp, errExec = executor.Execute(execCtx, auth, execReq, execOpts)
					syncActiveAuth()
					errExec = markUpstreamExecutionAttemptFromContext(execCtx, errExec)
					durationRetry := time.Since(startRetry)
					if errExec != nil {
						warnLogUpstreamFailure(execCtx, entry, provider, upstreamModel, auth, durationRetry, errExec)
						if errCtx := execCtx.Err(); errCtx != nil {
							m.recordCanceledUpstreamCooldown(execCtx, auth, provider, resultModel, routeModel, execOpts, errExec, false, executionSeq)
							return cliproxyexecutor.Response{}, errCtx
						}
					}
				} else {
					warnLogUpstreamFailure(execCtx, entry, provider, upstreamModel, auth, durationExec, errExec)
				}
			}
			if errCancel := claudeOAuthRequestCancellation(execCtx, auth, errExec); errCancel != nil {
				return cliproxyexecutor.Response{}, errCancel
			}
			result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, RouteModel: routeModel, Success: errExec == nil, CompletedResponse: errExec == nil && !resp.Incomplete && isCompletedExecutionPayload(resp.Payload), Options: execOpts, AccessToken: authAccessTokenForRequest(execCtx, auth, execOpts.Headers), RefreshToken: authRefreshToken(auth), CredentialVersion: auth.CredentialVersion, RegistrationEpoch: auth.RegistrationEpoch, ExecutionSeq: executionSeq}
			if errExec != nil {
				recordAntigravityAttemptedModelFromAttempt(ctx, execCtx, provider, auth.ID, resultModel, errExec)
				result.Error = resultErrorFromError(errExec)
				if ra := retryAfterFromError(errExec); ra != nil {
					result.RetryAfter = ra
				}
				if isCredentialScopedError(errExec) {
					result.CredentialScope = true
				}
				action, okAction := matchRequestScopedErrorAction(auth, errExec, m.requestScopedErrorConfig(execCtx))
				applyRequestScopedActionToResult(action, okAction, &result)
				if okAction && !isRequestStopError(errExec) && !isRequestScopedStop(action, okAction) {
					errExec = wrapRequestScopedContinueError(errExec)
				}
				if isResponsesCompactAvailabilityNeutralError(execOpts, errExec, result.Error) {
					m.recordAvailabilityNeutralResult(execCtx, result)
				} else {
					m.MarkResult(execCtx, result)
					errExec = annotateTrustedRetryAfterByHint(errExec)
				}
				if hasUpstreamExecutionAttempt(errExec) {
					syncPreferredUpstreamError(&upstreamErr, errExec)
				} else if preRefreshExecErr != nil {
					syncPreferredUpstreamError(&upstreamErr, preRefreshExecErr)
				}
				if isRequestStopError(errExec) {
					return cliproxyexecutor.Response{}, errExec
				}
				if okAction {
					if isRequestScopedStop(action, okAction) {
						return cliproxyexecutor.Response{}, wrapRequestStopError(errExec)
					}
					authErr = errExec
					if result.CredentialScope {
						break
					}
					continue
				}
				if isResponsesCompactRequestFaultError(execOpts, errExec) || isRequestInvalidError(errExec) {
					return cliproxyexecutor.Response{}, errExec
				}
				authErr = errExec
				if result.CredentialScope {
					break
				}
				continue
			}
			m.MarkResult(execCtx, result)
			attemptAliasResult := resolveAttemptAliasResult(routing, auth, routeModel, upstreamModel, aliasResult)
			rewriteForceMappedResponse(&resp, attemptAliasResult)
			return resp, nil
		}
		if authErr != nil {
			if isCredentialTerminatedError(authErr) {
				lastErr = authErr
				if homeMode {
					homeAuthCount++
				}
				continue
			}
			action, okAction := matchRequestScopedErrorAction(auth, authErr, m.requestScopedErrorConfig(execCtx))
			if okAction {
				if isRequestScopedStop(action, okAction) {
					return cliproxyexecutor.Response{}, wrapRequestStopError(authErr)
				}
				lastErr = keepPreferredCandidateError(lastErr, authErr)
				if homeMode {
					homeAuthCount++
				}
				continue
			}
			if isResponsesCompactRequestFaultError(opts, authErr) || isRequestInvalidError(authErr) {
				return cliproxyexecutor.Response{}, authErr
			}
			lastErr = keepPreferredCandidateError(lastErr, authErr)
			if homeMode {
				homeAuthCount++
			}
			continue
		}
	}
}

func (m *Manager) executeCountMixedOnce(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, maxRetryCredentials int, retryRound int, defaultRequestRetry int) (cliproxyexecutor.Response, error) {
	if len(providers) == 0 {
		return cliproxyexecutor.Response{}, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}
	routeModel := authSelectionModelFromOptions(opts, req.Model)
	executionModel, restoreExecutionModel := executionModelForAuthSelection(opts, req.Model)
	opts = ensureRequestedModelMetadata(opts, routeModel)
	clearSchedulerSelectionID(opts.Metadata)
	baseMetadata := opts.Metadata
	homeMode := m.HomeEnabled()
	homeAuthCount := 1
	tried := make(map[string]struct{})
	for authID := range requestRefusedAuthsFromContext(ctx) {
		tried[authID] = struct{}{}
	}
	if !homeMode {
		for authID := range m.requestRetryRoundExclusions(retryRound, defaultRequestRetry) {
			tried[authID] = struct{}{}
		}
	}
	attempted := make(map[string]struct{})
	lastAttemptedAuthID := ""
	var lastErr error
	var upstreamErr error
	for {
		if maxRetryCredentials > 0 && len(attempted) >= maxRetryCredentials {
			if lastErr != nil {
				return cliproxyexecutor.Response{}, preferredExecutionAttemptError(lastErr, upstreamErr)
			}
			return cliproxyexecutor.Response{}, &Error{Code: "auth_not_found", Message: "no auth available"}
		}
		opts.Metadata = cloneRequestMetadata(baseMetadata)
		if !homeMode && lastAttemptedAuthID != "" {
			// Preserve the mid-round selected_auth_id signal for scheduler plugins (#7434)
			// as a one-shot signal after an executed failure without carrying over
			// affinity/LCP, selection_id, or index keys from prior attempts.
			opts.Metadata[cliproxyexecutor.SelectedAuthMetadataKey] = lastAttemptedAuthID
			lastAttemptedAuthID = ""
		}
		pickOpts := opts
		if homeMode {
			pickOpts = withHomeRetryRound(pickOpts, retryRound)
			pickOpts = withHomeAuthCount(pickOpts, homeAuthCount)
			pickOpts = withHomeExcludedAuthIDs(pickOpts, tried)
		}
		auth, executor, provider, errPick := m.pickNextMixed(ctx, providers, routeModel, pickOpts, tried)
		if errPick == nil {
			updateInFlightScopeProvider(ctx, provider)
		}
		if errPick != nil {
			if shouldReturnLastErrorOnPickFailure(homeMode, lastErr, errPick) {
				return cliproxyexecutor.Response{}, roundPickFailureError(lastErr, upstreamErr, errPick)
			}
			return cliproxyexecutor.Response{}, errPick
		}

		entry := logEntryWithRequestID(ctx)
		debugLogAuthSelection(entry, auth, provider, routeModel)
		tried[auth.ID] = struct{}{}
		execCtx := ctx
		execCtx = m.WithAuthTransport(execCtx, auth)
		execCtx = m.withAntigravityModelState(execCtx, auth)
		execCtx = contextWithRequestedModelAlias(execCtx, pickOpts, routeModel)
		execCtx = coreusage.WithSelectionID(execCtx, selectionIDFromOptions(pickOpts))
		execCtx = m.newUpstreamAttemptContext(execCtx)

		models, pooled, aliasResult, routing := m.preparedExecutionModelsWithAliasForRequest(execCtx, auth, routeModel, pickOpts.Headers)
		if len(models) == 0 {
			continue
		}
		attempted[auth.ID] = struct{}{}
		if authID := strings.TrimSpace(auth.ID); authID != "" {
			lastAttemptedAuthID = authID
		}
		publishSelectedAuthMetadata(opts.Metadata, auth)
		if homeMode {
			syncPublishedSelectedAuthMetadata(pickOpts.Metadata, opts.Metadata)
		}
		preparedAuth, errPrepare := m.prepareRequestAuth(execCtx, executor, auth)
		if errPrepare != nil {
			if preparedAuth != nil {
				auth = preparedAuth
			}
			errPrepare = markUpstreamExecutionAttemptFromContext(execCtx, errPrepare)
			if errCtx := execCtx.Err(); errCtx != nil {
				return cliproxyexecutor.Response{}, errCtx
			}
			if errCancel := claudeOAuthRequestCancellation(execCtx, auth, errPrepare); errCancel != nil {
				return cliproxyexecutor.Response{}, errCancel
			}
			stateModel := m.selectionModelKeyForAuth(auth, routeModel)
			if stateModel == "" {
				stateModel = canonicalModelKey(routeModel)
			}
			result := Result{AuthID: auth.ID, Provider: provider, Model: stateModel, RouteModel: routeModel, Success: false, Error: resultErrorFromError(errPrepare), Options: pickOpts, SkipQuotaObservation: true, AccessToken: authAccessTokenForRequest(execCtx, auth, pickOpts.Headers), RefreshToken: authRefreshToken(auth), CredentialVersion: auth.CredentialVersion, RegistrationEpoch: auth.RegistrationEpoch}
			if ra := retryAfterFromError(errPrepare); ra != nil {
				result.RetryAfter = ra
			}
			if isCredentialScopedError(errPrepare) {
				result.CredentialScope = true
			}
			action, okAction := matchRequestScopedErrorAction(auth, errPrepare, m.requestScopedErrorConfig(execCtx))
			applyRequestScopedActionToResult(action, okAction, &result)
			m.MarkResult(execCtx, result)
			errPrepare = annotateTrustedRetryAfterByHint(errPrepare)
			syncPreferredUpstreamError(&upstreamErr, errPrepare)
			if isRequestScopedStop(action, okAction) {
				return cliproxyexecutor.Response{}, wrapRequestStopError(errPrepare)
			}
			if !okAction && isRequestInvalidError(errPrepare) {
				return cliproxyexecutor.Response{}, errPrepare
			}
			lastErr = keepPreferredCandidateError(lastErr, errPrepare)
			continue
		}
		if preparedAuth != nil {
			auth = preparedAuth
		}
		executor = executorForAuth(executor, auth)
		var authErr error
		didRefreshOnUnauthorized := false
		for _, upstreamModel := range models {
			execCtx = m.newUpstreamAttemptContext(execCtx)
			resultModel := m.stateModelForExecution(auth, routeModel, upstreamModel, pooled)
			execReq := req
			execReq.Model = upstreamModel
			if restoreExecutionModel {
				execReq.Model = executionModel
			}
			execOpts := opts
			if pickOpts.Metadata != nil {
				if canonicalID, ok := pickOpts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]; ok {
					meta := make(map[string]any, len(execOpts.Metadata)+2)
					for k, v := range execOpts.Metadata {
						meta[k] = v
					}
					meta[cliproxyexecutor.CanonicalSessionIDMetadataKey] = canonicalID
					if parentID, okParent := pickOpts.Metadata[cliproxyexecutor.ParentSessionIDMetadataKey]; okParent && parentID != canonicalID {
						meta[cliproxyexecutor.ParentSessionIDMetadataKey] = parentID
					} else {
						delete(meta, cliproxyexecutor.ParentSessionIDMetadataKey)
					}
					execOpts.Metadata = meta
				}
			}
			payload := execOpts.OriginalRequest
			if len(payload) == 0 {
				payload = execReq.Payload
			}
			execOpts.Metadata = ensureCanonicalSessionMetadata(execOpts.Metadata, execOpts.Headers, payload)
			var errIntercept error
			execReq, execOpts, errIntercept = prepareRequestAfterAuth(execCtx, executor, auth, provider, execReq, execOpts, requestedModelAliasFromOptions(execOpts, routeModel))
			if errIntercept != nil {
				if isCredentialTerminatedError(errIntercept) {
					recordRequestRefusedAuth(ctx, auth.ID)
					authErr = errIntercept
					break
				}
				return cliproxyexecutor.Response{}, errIntercept
			}
			execReq = attachResolvedExecutionModelInfo(routing, execReq, auth, routeModel, upstreamModel, restoreExecutionModel)
			var activeAuth atomic.Pointer[Auth]
			activeAuth.Store(auth)
			syncActiveAuth := func() {
				if currentAuth := activeAuth.Load(); currentAuth != nil && currentAuth != auth {
					auth = currentAuth
				}
			}
			refreshExecCtx := func(base context.Context) context.Context {
				return m.withExecutionAuthRefresh(base, executionAuthRefreshOptions{
					onAuthUpdated: func(updated *Auth) {
						if updated != nil {
							activeAuth.Store(updated.Clone())
						}
					},
				})
			}
			execCtx = refreshExecCtx(syncMetadataSessionToContext(execCtx, execOpts.Metadata))
			executionSeq := m.nextExecutionSeq()
			startExec := time.Now()
			var preRefreshExecErr error
			resp, errExec := executor.CountTokens(execCtx, auth, execReq, execOpts)
			syncActiveAuth()
			errExec = markUpstreamExecutionAttemptFromContext(execCtx, errExec)
			durationExec := time.Since(startExec)
			if errExec != nil {
				if errCtx := execCtx.Err(); errCtx != nil {
					m.recordCanceledUpstreamCooldown(execCtx, auth, provider, resultModel, routeModel, execOpts, errExec, false, executionSeq)
					return cliproxyexecutor.Response{}, errCtx
				}
				refreshCtx := newUpstreamAttemptContext(execCtx)
				if refreshed, okRefresh := m.tryRefreshAfterUnauthorized(refreshCtx, auth, errExec, didRefreshOnUnauthorized, execOpts.Headers); okRefresh {
					preRefreshExecErr = errExec
					auth = refreshed
					activeAuth.Store(auth)
					publishSelectedAuthMetadata(execOpts.Metadata, auth)
					didRefreshOnUnauthorized = true
					execCtx = refreshExecCtx(m.newUpstreamAttemptContext(execCtx))
					execCtx = syncMetadataSessionToContext(execCtx, execOpts.Metadata)
					executionSeq = m.nextExecutionSeq()
					startRetry := time.Now()
					resp, errExec = executor.CountTokens(execCtx, auth, execReq, execOpts)
					syncActiveAuth()
					errExec = markUpstreamExecutionAttemptFromContext(execCtx, errExec)
					durationRetry := time.Since(startRetry)
					if errExec != nil {
						warnLogUpstreamFailure(execCtx, entry, provider, upstreamModel, auth, durationRetry, errExec)
						if errCtx := execCtx.Err(); errCtx != nil {
							m.recordCanceledUpstreamCooldown(execCtx, auth, provider, resultModel, routeModel, execOpts, errExec, false, executionSeq)
							return cliproxyexecutor.Response{}, errCtx
						}
					}
				} else {
					warnLogUpstreamFailure(execCtx, entry, provider, upstreamModel, auth, durationExec, errExec)
				}
			}
			if errCancel := claudeOAuthRequestCancellation(execCtx, auth, errExec); errCancel != nil {
				return cliproxyexecutor.Response{}, errCancel
			}
			result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, RouteModel: routeModel, Success: errExec == nil, Options: execOpts, SkipQuotaObservation: true, AccessToken: authAccessTokenForRequest(execCtx, auth, execOpts.Headers), RefreshToken: authRefreshToken(auth), CredentialVersion: auth.CredentialVersion, RegistrationEpoch: auth.RegistrationEpoch, ExecutionSeq: executionSeq}
			if errExec != nil {
				recordAntigravityAttemptedModelFromAttempt(ctx, execCtx, provider, auth.ID, resultModel, errExec)
				result.Error = resultErrorFromError(errExec)
				if ra := retryAfterFromError(errExec); ra != nil {
					result.RetryAfter = ra
				}
				action, okAction := matchRequestScopedErrorAction(auth, errExec, m.requestScopedErrorConfig(execCtx))
				applyRequestScopedActionToResult(action, okAction, &result)
				// Some Anthropic-compatible upstreams and plugin executors do not
				// implement count_tokens and return a generic endpoint 404 or an
				// explicit 501 / unsupported-capability error. Record the failure
				// for hooks and metrics without suspending a model that remains
				// usable through the messages endpoint.
				if okAction && !isRequestStopError(errExec) && !isRequestScopedStop(action, okAction) {
					errExec = wrapRequestScopedContinueError(errExec)
				}
				if isCountTokensEndpointNotFoundError(errExec, execReq.Model) && (result.Error == nil || result.Error.Code != ErrorCodeForceCooldown) {
					m.recordAvailabilityNeutralResult(execCtx, result)
				} else {
					if isCredentialScopedError(errExec) {
						result.CredentialScope = true
					}
					m.MarkResult(execCtx, result)
					errExec = annotateTrustedRetryAfterByHint(errExec)
				}
				if hasUpstreamExecutionAttempt(errExec) {
					syncPreferredUpstreamError(&upstreamErr, errExec)
				} else if preRefreshExecErr != nil {
					syncPreferredUpstreamError(&upstreamErr, preRefreshExecErr)
				}
				if isRequestStopError(errExec) {
					return cliproxyexecutor.Response{}, errExec
				}
				if okAction {
					if isRequestScopedStop(action, okAction) {
						return cliproxyexecutor.Response{}, wrapRequestStopError(errExec)
					}
					authErr = errExec
					if result.CredentialScope {
						break
					}
					continue
				}
				if isRequestInvalidError(errExec) {
					return cliproxyexecutor.Response{}, errExec
				}
				authErr = errExec
				if result.CredentialScope {
					break
				}
				continue
			}
			m.MarkResult(execCtx, result)
			attemptAliasResult := resolveAttemptAliasResult(routing, auth, routeModel, upstreamModel, aliasResult)
			rewriteForceMappedResponse(&resp, attemptAliasResult)
			return resp, nil
		}
		if authErr != nil {
			if isCredentialTerminatedError(authErr) {
				lastErr = authErr
				if homeMode {
					homeAuthCount++
				}
				continue
			}
			action, okAction := matchRequestScopedErrorAction(auth, authErr, m.requestScopedErrorConfig(execCtx))
			if okAction {
				if isRequestScopedStop(action, okAction) {
					return cliproxyexecutor.Response{}, wrapRequestStopError(authErr)
				}
				lastErr = keepPreferredCandidateError(lastErr, authErr)
				if homeMode {
					homeAuthCount++
				}
				continue
			}
			if isRequestInvalidError(authErr) {
				return cliproxyexecutor.Response{}, authErr
			}
			lastErr = keepPreferredCandidateError(lastErr, authErr)
			if homeMode {
				homeAuthCount++
			}
			continue
		}
	}
}

func (m *Manager) executeStreamMixedOnce(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, maxRetryCredentials int, homeRetryLimit *int, retryRound int, defaultRequestRetry int) (*cliproxyexecutor.StreamResult, error) {
	if len(providers) == 0 {
		return nil, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}
	routeModel := authSelectionModelFromOptions(opts, req.Model)
	responseAlias := requestedModelAliasFromOptions(opts, routeModel)
	executionModel, restoreExecutionModel := executionModelForAuthSelection(opts, req.Model)
	opts = ensureRequestedModelMetadata(opts, routeModel)
	clearSchedulerSelectionID(opts.Metadata)
	baseMetadata := opts.Metadata
	homeMode := m.HomeEnabled()
	homeAuthCount := 1
	tried := make(map[string]struct{})
	homeExcludedAuthIDs := make(map[string]struct{})
	for authID := range requestRefusedAuthsFromContext(ctx) {
		tried[authID] = struct{}{}
		homeExcludedAuthIDs[authID] = struct{}{}
	}
	for _, authID := range homeExcludedAuthIDsFromMetadata(opts.Metadata) {
		tried[authID] = struct{}{}
		homeExcludedAuthIDs[authID] = struct{}{}
	}
	if !homeMode {
		for authID := range m.requestRetryRoundExclusions(retryRound, defaultRequestRetry) {
			tried[authID] = struct{}{}
		}
	}
	homeSameAuthRetries := make(map[string]int)
	lastHomeAuthID := ""
	homeSameAuthRetryPending := false
	attempted := make(map[string]struct{})
	lastAttemptedAuthID := ""
	var lastErr error
	var upstreamErr error
	var roundTiming homeRetryRoundTiming
	for {
		allowSameAuthRetry := homeMode && homeSameAuthRetryPending && lastHomeAuthID != "" && homeSameAuthRetries[lastHomeAuthID] == 0
		if maxRetryCredentials > 0 && len(attempted) >= maxRetryCredentials && !allowSameAuthRetry {
			if lastErr != nil {
				preferredErr := preferredExecutionAttemptError(lastErr, upstreamErr)
				if homeMode && !isCredentialTerminatedError(preferredErr) {
					return nil, markHomeRetryRoundExhausted(preferredErr, roundTiming.RetryAfter(), true)
				}
				return nil, preferredErr
			}
			return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
		}
		opts.Metadata = cloneRequestMetadata(baseMetadata)
		if !homeMode && lastAttemptedAuthID != "" {
			// Preserve the mid-round selected_auth_id signal for scheduler plugins (#7434)
			// as a one-shot signal after an executed failure without carrying over
			// affinity/LCP, selection_id, or index keys from prior attempts.
			opts.Metadata[cliproxyexecutor.SelectedAuthMetadataKey] = lastAttemptedAuthID
			lastAttemptedAuthID = ""
		}
		pickOpts := opts
		if homeMode {
			pickOpts = withHomeRetryRound(pickOpts, retryRound)
			pickOpts = withHomeAuthCount(pickOpts, homeAuthCount)
			pickOpts = withHomeExcludedAuthIDs(pickOpts, homeExcludedAuthIDs)
		}

		var selection *HomeDispatchSelection
		var auth *Auth
		var executor ProviderExecutor
		var provider string
		var errPick error
		if homeMode {
			selection, errPick = m.pickHomeDispatchSelection(ctx, routeModel, pickOpts)
			if selection != nil {
				auth = selection.CloneAuthForRoute(routeModel)
				executor = selection.Executor
				provider = selection.Provider
			}
		} else {
			auth, executor, provider, errPick = m.pickNextMixed(ctx, providers, routeModel, pickOpts, tried)
			if errPick == nil {
				updateInFlightScopeProvider(ctx, provider)
			}
		}
		if errPick != nil {
			preferredErr := preferredExecutionAttemptError(lastErr, upstreamErr)
			var homeCooldown *homeDispatchRetryAfterError
			if homeMode && lastErr != nil && errors.As(errPick, &homeCooldown) && homeCooldown != nil {
				observeHomeCooldownRetryLimit(homeCooldown, homeRetryLimit, pinnedAuthIDFromMetadata(opts.Metadata) == "")
				if isCredentialTerminatedError(preferredErr) {
					return nil, preferredErr
				}
				return nil, markHomeRetryRoundExhausted(preferredErr, homeCooldown.RetryAfter(), false)
			}
			if shouldReturnLastErrorOnPickFailure(homeMode, lastErr, errPick) {
				if homeMode {
					if isCredentialTerminatedError(preferredErr) {
						return nil, preferredErr
					}
					if isSchedulerRejectError(errPick) {
						preferredErr = roundPickFailureError(lastErr, upstreamErr, errPick)
					}
					return nil, markHomeRetryRoundExhausted(preferredErr, roundTiming.RetryAfter(), isHomeNextRoundImmediatelyAvailable(errPick))
				}
				return nil, roundPickFailureError(lastErr, upstreamErr, errPick)
			}
			return nil, errPick
		}
		if auth == nil || executor == nil {
			if selection != nil {
				selection.End("missing_execution_target")
			}
			return nil, &Error{Code: "executor_not_found", Message: "executor not registered"}
		}
		if homeMode {
			m.observeHomeRetryLimit(auth, selection, homeRetryLimit)
		}
		if selection != nil && allowSameAuthRetry && maxRetryCredentials > 0 && len(attempted) >= maxRetryCredentials && auth.ID != lastHomeAuthID {
			if errEnd := m.endHomeSelectionBeforeRedispatch(ctx, selection, "max_retry_credentials"); errEnd != nil {
				return nil, errEnd
			}
			if lastErr != nil {
				preferredErr := preferredExecutionAttemptError(lastErr, upstreamErr)
				if isCredentialTerminatedError(preferredErr) {
					return nil, preferredErr
				}
				return nil, markHomeRetryRoundExhausted(preferredErr, roundTiming.RetryAfter(), true)
			}
			return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
		}
		if homeMode && lastHomeAuthID != "" && auth.ID != lastHomeAuthID {
			homeSameAuthRetryPending = false
		}
		if selection != nil {
			// A legacy Home may ignore excluded_auth_ids and return the same
			// credential again. Reject credentials explicitly excluded from this
			// round while retaining the explicit same-auth retry path, which
			// intentionally leaves the credential out of homeExcludedAuthIDs.
			if _, alreadyTried := tried[auth.ID]; alreadyTried {
				if _, excluded := homeExcludedAuthIDs[auth.ID]; excluded {
					if errEnd := m.endHomeSelectionBeforeRedispatch(ctx, selection, "repeated_excluded_auth"); errEnd != nil {
						return nil, errEnd
					}
					if lastErr != nil {
						preferredErr := preferredExecutionAttemptError(lastErr, upstreamErr)
						if isCredentialTerminatedError(preferredErr) {
							return nil, preferredErr
						}
						return nil, markHomeRetryRoundExhausted(preferredErr, roundTiming.RetryAfter(), false)
					}
					return nil, repeatedHomeAuthError()
				} else {
					homeSameAuthRetries[auth.ID]++
					if homeSameAuthRetries[auth.ID] > 1 {
						// A fresh Home selection may retry the same auth once for
						// connection lifecycle or authorization recovery. Repeated
						// failures must still rotate away from this credential.
						homeExcludedAuthIDs[auth.ID] = struct{}{}
						if errEnd := m.endHomeSelectionBeforeRedispatch(ctx, selection, "repeated_same_auth"); errEnd != nil {
							return nil, errEnd
						}
						continue
					}
				}
			}
		}

		entry := logEntryWithRequestID(ctx)
		debugLogAuthSelection(entry, auth, provider, routeModel)
		if selection != nil {
			if errRuntimeAuth := m.bindHomeSelectionRuntimeAuth(ctx, opts, selection); errRuntimeAuth != nil {
				selection.End("runtime_auth_bind_failed")
				return nil, errRuntimeAuth
			}
		}
		tried[auth.ID] = struct{}{}
		execCtx := ctx
		releaseAttempt := func() {}
		if selection != nil {
			var errBind error
			execCtx, releaseAttempt, errBind = homeExecutionAttemptContext(ctx, selection)
			if errBind != nil {
				selection.End("attempt_bind_failed")
				return nil, errBind
			}
		}
		execCtx = m.WithAuthTransport(execCtx, auth)
		execCtx = m.withAntigravityModelState(execCtx, auth)
		// Enrich before auth preparation so prepare-stage usage records observe the client request.
		if homeMode {
			clearSchedulerSelectionID(opts.Metadata)
			clearSchedulerSelectionID(pickOpts.Metadata)
			if selection.SelectionID != "" {
				pickOpts.Metadata[cliproxyexecutor.SelectionIDMetadataKey] = selection.SelectionID
				opts.Metadata[cliproxyexecutor.SelectionIDMetadataKey] = selection.SelectionID
			}
		}
		execCtx = contextWithRequestedModelAlias(execCtx, pickOpts, routeModel)
		execCtx = coreusage.WithSelectionID(execCtx, selectionIDFromOptions(pickOpts))
		if selection != nil {
			execCtx = coreusage.WithHomeSelection(execCtx, coreusage.HomeSelection{
				Reused: selection.Retained(), Model: routeModel, CredentialPolicy: credentialPolicyFromContext(ctx),
			})
		}
		execCtx = m.newUpstreamAttemptContext(execCtx)
		models, pooled, aliasResult, routing := m.executionModelCandidatesWithAlias(auth, routeModel)
		// Home owns dispatched credential availability; only local selections need state filtering.
		if selection == nil {
			models = m.filterExecutionModelsForRequest(execCtx, auth, routeModel, models, pooled, pickOpts.Headers)
		}
		if selection != nil && aliasResult.ForceMapping && responseAlias != "" {
			aliasResult.OriginalAlias = responseAlias
		}
		if len(models) == 0 {
			if selection != nil {
				homeExcludedAuthIDs[auth.ID] = struct{}{}
				lastHomeAuthID = auth.ID
				homeSameAuthRetryPending = false
				releaseAttempt()
				if errEnd := m.endHomeSelectionBeforeRedispatch(ctx, selection, "no_execution_models"); errEnd != nil {
					return nil, errEnd
				}
			}
			continue
		}
		attempted[auth.ID] = struct{}{}
		if authID := strings.TrimSpace(auth.ID); authID != "" {
			lastAttemptedAuthID = authID
		}
		publishSelectedAuthMetadata(opts.Metadata, auth)
		if homeMode {
			syncPublishedSelectedAuthMetadata(pickOpts.Metadata, opts.Metadata)
		}
		var preparedAuth *Auth
		var errPrepare error
		if selection != nil {
			preparedAuth, errPrepare = m.prepareHomeRequestAuth(execCtx, selection, models[0])
		} else {
			preparedAuth, errPrepare = m.prepareRequestAuth(execCtx, executor, auth)
		}
		if errPrepare != nil {
			if preparedAuth != nil {
				auth = preparedAuth
			}
			errPrepare = markUpstreamExecutionAttemptFromContext(execCtx, errPrepare)
			if errCtx := execCtx.Err(); errCtx != nil {
				if selection != nil {
					releaseAttempt()
					selection.End("attempt_canceled")
				}
				return nil, errCtx
			}
			if selection != nil {
				excludeAuth := shouldExcludeHomeAuthAfterStreamError(execCtx, auth, errPrepare)
				if homeSameAuthRetries[auth.ID] > 0 {
					excludeAuth = true
				}
				if excludeAuth {
					homeExcludedAuthIDs[auth.ID] = struct{}{}
				}
				lastHomeAuthID = auth.ID
				homeSameAuthRetryPending = !excludeAuth
			}
			if selection == nil {
				if errCancel := claudeOAuthRequestCancellation(execCtx, auth, errPrepare); errCancel != nil {
					return nil, errCancel
				}
			}
			stateModel := m.selectionModelKeyForAuth(auth, routeModel)
			if selection != nil && selection.ExecutorPlugin != "" {
				stateModel = models[0]
			} else if stateModel == "" {
				stateModel = canonicalModelKey(routeModel)
			}
			result := Result{AuthID: auth.ID, Provider: provider, Model: stateModel, RouteModel: routeModel, Success: false, Error: resultErrorFromError(errPrepare), Options: pickOpts, AccessToken: authAccessTokenForRequest(execCtx, auth, pickOpts.Headers), RefreshToken: authRefreshToken(auth), CredentialVersion: auth.CredentialVersion, RegistrationEpoch: auth.RegistrationEpoch}
			if ra := retryAfterFromError(errPrepare); ra != nil {
				result.RetryAfter = ra
			}
			if isCredentialScopedError(errPrepare) {
				result.CredentialScope = true
			}
			action, okAction := matchRequestScopedErrorAction(auth, errPrepare, m.requestScopedErrorConfig(execCtx))
			applyRequestScopedActionToResult(action, okAction, &result)
			if selection != nil {
				m.reportHomeResult(execCtx, result, auth)
				releaseAttempt()
			} else {
				m.MarkResult(execCtx, result)
			}
			errPrepare = annotateTrustedRetryAfterByHint(errPrepare)
			if okAction && !isRequestStopError(errPrepare) && !isRequestScopedStop(action, okAction) {
				errPrepare = wrapRequestScopedContinueError(errPrepare)
			}
			syncPreferredUpstreamError(&upstreamErr, errPrepare)
			if isRequestStopError(errPrepare) {
				if selection != nil {
					selection.End("request_stopped")
				}
				return nil, errPrepare
			}
			if isRequestScopedStop(action, okAction) {
				if selection != nil {
					selection.End("request_stopped")
				}
				return nil, wrapRequestStopError(errPrepare)
			}
			if isTerminalRequestStopError(errPrepare) || (!okAction && isRequestInvalidError(errPrepare)) {
				if selection != nil {
					selection.End("request_invalid")
				}
				return nil, errPrepare
			}
			lastErr = keepPreferredCandidateError(lastErr, errPrepare)
			if homeMode {
				roundTiming.Observe(errPrepare)
			}
			if selection != nil {
				if errEnd := m.endHomeSelectionBeforeRedispatch(ctx, selection, "prepare_failed"); errEnd != nil {
					return nil, errEnd
				}
			}
			continue
		}
		if preparedAuth != nil {
			auth = preparedAuth
		}
		execReq := sanitizeDownstreamWebsocketFallbackRequest(execCtx, auth, req)
		restoreModel := restoreExecutionModel && (selection == nil || selection.ExecutorPlugin == "")
		if selection != nil && !restoreModel {
			execReq = attachResolvedHomeModelInfo(execReq, auth, routeModel, selection.modelInfo, selection.configurationUpdateSupport)
		}
		streamExecutionModel := ""
		if restoreModel {
			streamExecutionModel = executionModel
		}
		execOpts := opts
		if selection != nil {
			execOpts.ExecutionLifecycle = selection
			if selection.CanonicalSessionID != "" || selection.lcpAffinitySessionID != "" {
				execOpts.Metadata = applyHomeSelectionSessionMetadata(execOpts.Metadata, selection)
			}
		} else if pickOpts.Metadata != nil {
			if canonicalID, ok := pickOpts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]; ok {
				meta := make(map[string]any, len(execOpts.Metadata)+2)
				for k, v := range execOpts.Metadata {
					meta[k] = v
				}
				meta[cliproxyexecutor.CanonicalSessionIDMetadataKey] = canonicalID
				if parentID, okParent := pickOpts.Metadata[cliproxyexecutor.ParentSessionIDMetadataKey]; okParent && parentID != canonicalID {
					meta[cliproxyexecutor.ParentSessionIDMetadataKey] = parentID
				} else {
					delete(meta, cliproxyexecutor.ParentSessionIDMetadataKey)
				}
				execOpts.Metadata = meta
			}
		}
		payload := execOpts.OriginalRequest
		if len(payload) == 0 {
			payload = execReq.Payload
		}
		execOpts.Metadata = ensureCanonicalSessionMetadata(execOpts.Metadata, execOpts.Headers, payload)
		execCtx = syncMetadataSessionToContext(execCtx, execOpts.Metadata)
		if homeMode && len(models) > 1 {
			models = models[:1]
			pooled = false
		}
		streamResult, errStream := m.executeStreamWithModelPool(execCtx, executor, auth, provider, execReq, execOpts, routeModel, streamExecutionModel, models, pooled, aliasResult, routing, !homeMode || selection != nil, selection != nil)
		if errStream != nil {
			if isCredentialTerminatedError(errStream) {
				recordRequestRefusedAuth(ctx, auth.ID)
				if selection != nil {
					homeExcludedAuthIDs[auth.ID] = struct{}{}
					lastHomeAuthID = auth.ID
					homeSameAuthRetryPending = false
					releaseAttempt()
					if errEnd := m.endHomeSelectionBeforeRedispatch(ctx, selection, "credential_refused"); errEnd != nil {
						return nil, errEnd
					}
				}
				lastErr = errStream
				if homeMode {
					homeAuthCount++
				}
				continue
			}
			syncPreferredUpstreamError(&upstreamErr, errStream)
			if isTerminalRequestStopError(errStream) || isTOSViolationError(errStream) || (!IsRequestScopedContinueError(errStream) && (isRequestInvalidError(errStream) || cliproxyexecutor.IsUpstreamWebsocketTurnError(errStream))) {
				// Only redispatch needs a release acknowledgement. Preserve the final
				// response even when Home cannot acknowledge the released admission.
				if selection != nil {
					releaseAttempt()
					selection.End("request_stopped")
				}
				return nil, errStream
			}
			if selection != nil {
				excludeAuth := shouldExcludeHomeAuthAfterStreamError(execCtx, auth, errStream)
				if homeSameAuthRetries[auth.ID] > 0 {
					excludeAuth = true
				}
				if excludeAuth {
					homeExcludedAuthIDs[auth.ID] = struct{}{}
				}
				lastHomeAuthID = auth.ID
				homeSameAuthRetryPending = !excludeAuth
			}
			if selection != nil {
				releaseAttempt()
				if errEnd := m.endHomeSelectionBeforeRedispatch(ctx, selection, "stream_start_failed"); errEnd != nil {
					return nil, errEnd
				}
			}
			if errCtx := execCtx.Err(); errCtx != nil && ctx != nil && ctx.Err() != nil {
				return nil, errCtx
			}
			lastErr = keepPreferredCandidateError(lastErr, errStream)
			if homeMode {
				roundTiming.Observe(errStream)
			}
			if homeMode {
				homeAuthCount++
			}
			continue
		}
		if selection != nil {
			if m.retainHomeWebsocketSelection(ctx, opts, routeModel, selection) {
				return wrapHomeStream(ctx, streamResult, nil, releaseAttempt), nil
			}
			return wrapHomeStream(ctx, streamResult, selection, releaseAttempt), nil
		}
		return streamResult, nil
	}
}

func shouldExcludeHomeAuthAfterStreamError(ctx context.Context, _ *Auth, err error) bool {
	if err == nil || isConnectionLifecycleError(err) {
		return false
	}
	// A 426 during a downstream websocket attempt is a transport fallback
	// signal and may retry the same credential once.
	if cliproxyexecutor.DownstreamWebsocket(ctx) && statusCodeFromError(err) == http.StatusUpgradeRequired {
		return false
	}
	return true
}

func withAttemptedAuthTracker(opts cliproxyexecutor.Options, attempted map[string]struct{}) cliproxyexecutor.Options {
	if attempted == nil {
		return opts
	}
	meta := cloneRequestMetadata(opts.Metadata)
	prevCallback, _ := meta[cliproxyexecutor.SelectedAuthCallbackMetadataKey].(func(string))
	meta[cliproxyexecutor.SelectedAuthCallbackMetadataKey] = func(authID string) {
		if strings.TrimSpace(authID) != "" {
			attempted[authID] = struct{}{}
		}
		if prevCallback != nil {
			prevCallback(authID)
		}
	}
	opts.Metadata = meta
	return opts
}

type metadataCloneVisitKey struct {
	ptr      uintptr
	capacity int
	isSlice  bool
	typ      reflect.Type
}

var metadataMapAnyType = reflect.TypeFor[map[string]any]()

func cloneRequestMetadata(src map[string]any) map[string]any {
	if len(src) == 0 {
		return make(map[string]any, 4)
	}
	dst := make(map[string]any, len(src)+4)
	var visited map[metadataCloneVisitKey]any
	rootKey := metadataCloneVisitKey{ptr: reflect.ValueOf(src).Pointer()}
	for k, v := range src {
		dst[k] = cloneRequestMetadataValue(v, rootKey, dst, &visited)
	}
	return dst
}

func cloneRequestMetadataValue(v any, rootKey metadataCloneVisitKey, rootDst map[string]any, visited *map[metadataCloneVisitKey]any) any {
	switch v.(type) {
	case nil, string, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, uintptr, float32, float64, func(string):
		return v
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Map:
		if rv.IsNil() {
			return v
		}
		key := metadataCloneVisitKey{ptr: rv.Pointer()}
		if key == rootKey {
			if rv.Type() == metadataMapAnyType {
				return rootDst
			}
			return reflect.ValueOf(rootDst).Convert(rv.Type()).Interface()
		}
		if *visited == nil {
			*visited = make(map[metadataCloneVisitKey]any)
		} else if existing, ok := (*visited)[key]; ok {
			ev := reflect.ValueOf(existing)
			if ev.Type() == rv.Type() {
				return existing
			}
			return ev.Convert(rv.Type()).Interface()
		}
		cloned := reflect.MakeMapWithSize(rv.Type(), rv.Len())
		(*visited)[key] = cloned.Interface()
		elemType := rv.Type().Elem()
		iter := rv.MapRange()
		for iter.Next() {
			cloned.SetMapIndex(iter.Key(), cloneReflectMetadataElement(iter.Value(), elemType, rootKey, rootDst, visited))
		}
		return cloned.Interface()
	case reflect.Slice:
		if rv.IsNil() {
			return v
		}
		if rv.Cap() == 0 {
			return reflect.MakeSlice(rv.Type(), 0, 0).Interface()
		}
		elemType := rv.Type().Elem()
		if elemType.Size() == 0 {
			return reflect.MakeSlice(rv.Type(), rv.Len(), rv.Cap()).Interface()
		}
		key := metadataCloneVisitKey{ptr: rv.Pointer(), capacity: rv.Cap(), isSlice: true, typ: elemType}
		if *visited == nil {
			*visited = make(map[metadataCloneVisitKey]any)
		} else if existing, ok := (*visited)[key]; ok {
			ev := reflect.ValueOf(existing)
			if ev.Type() != rv.Type() {
				ev = ev.Convert(rv.Type())
			}
			return ev.Slice(0, rv.Len()).Interface()
		}
		clonedFull := reflect.MakeSlice(rv.Type(), rv.Cap(), rv.Cap())
		(*visited)[key] = clonedFull.Interface()
		fullSrc := rv.Slice(0, rv.Cap())
		switch elemType.Kind() {
		case reflect.Map, reflect.Slice, reflect.Array, reflect.Struct, reflect.Interface:
			for i := 0; i < rv.Cap(); i++ {
				clonedFull.Index(i).Set(cloneReflectMetadataElement(fullSrc.Index(i), elemType, rootKey, rootDst, visited))
			}
		default:
			reflect.Copy(clonedFull, fullSrc)
		}
		return clonedFull.Slice(0, rv.Len()).Interface()
	case reflect.Array:
		cloned := reflect.New(rv.Type()).Elem()
		elemType := rv.Type().Elem()
		for i := 0; i < rv.Len(); i++ {
			cloned.Index(i).Set(cloneReflectMetadataElement(rv.Index(i), elemType, rootKey, rootDst, visited))
		}
		return cloned.Interface()
	case reflect.Struct:
		rt := rv.Type()
		var cloned reflect.Value
		for i := 0; i < rt.NumField(); i++ {
			sf := rt.Field(i)
			if !sf.IsExported() {
				continue
			}
			fv := rv.Field(i)
			switch fv.Kind() {
			case reflect.Map, reflect.Slice, reflect.Array, reflect.Struct, reflect.Interface:
				if !cloned.IsValid() {
					cloned = reflect.New(rt).Elem()
					cloned.Set(rv)
				}
				cloned.Field(i).Set(cloneReflectMetadataElement(fv, sf.Type, rootKey, rootDst, visited))
			}
		}
		if !cloned.IsValid() {
			return v
		}
		return cloned.Interface()
	default:
		return v
	}
}

func cloneReflectMetadataElement(elem reflect.Value, elemType reflect.Type, rootKey metadataCloneVisitKey, rootDst map[string]any, visited *map[metadataCloneVisitKey]any) reflect.Value {
	switch elem.Kind() {
	case reflect.Interface:
		if elem.IsNil() {
			return reflect.Zero(elemType)
		}
		clonedVal := cloneRequestMetadataValue(elem.Interface(), rootKey, rootDst, visited)
		if clonedVal == nil {
			return reflect.Zero(elemType)
		}
		return reflect.ValueOf(clonedVal)
	case reflect.Map, reflect.Slice:
		if elem.IsNil() {
			return elem
		}
		return reflect.ValueOf(cloneRequestMetadataValue(elem.Interface(), rootKey, rootDst, visited))
	case reflect.Array, reflect.Struct:
		return reflect.ValueOf(cloneRequestMetadataValue(elem.Interface(), rootKey, rootDst, visited))
	default:
		return elem
	}
}

func ensureRequestedModelMetadata(opts cliproxyexecutor.Options, requestedModel string) cliproxyexecutor.Options {
	opts.Metadata = cloneRequestMetadata(opts.Metadata)
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return opts
	}
	if hasRequestedModelMetadata(opts.Metadata) {
		return opts
	}
	opts.Metadata[cliproxyexecutor.RequestedModelMetadataKey] = requestedModel
	return opts
}

func authSelectionModelFromOptions(opts cliproxyexecutor.Options, fallback string) string {
	fallback = strings.TrimSpace(fallback)
	if len(opts.Metadata) == 0 {
		return fallback
	}
	raw, ok := opts.Metadata[cliproxyexecutor.AuthSelectionModelMetadataKey]
	if !ok || raw == nil {
		return fallback
	}
	switch value := raw.(type) {
	case string:
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	case []byte:
		if strings.TrimSpace(string(value)) != "" {
			return strings.TrimSpace(string(value))
		}
	}
	return fallback
}

func executionModelForAuthSelection(opts cliproxyexecutor.Options, model string) (string, bool) {
	model = strings.TrimSpace(model)
	if model == "" {
		return "", false
	}
	selectionModel := authSelectionModelFromOptions(opts, model)
	if selectionModel == model {
		return "", false
	}
	return model, true
}

func withHomeAuthCount(opts cliproxyexecutor.Options, count int) cliproxyexecutor.Options {
	if count <= 0 {
		count = 1
	}
	meta := make(map[string]any, len(opts.Metadata)+1)
	for k, v := range opts.Metadata {
		meta[k] = v
	}
	meta[homeAuthCountMetadataKey] = count
	opts.Metadata = meta
	return opts
}

func withHomeRetryRound(opts cliproxyexecutor.Options, retryRound int) cliproxyexecutor.Options {
	meta := make(map[string]any, len(opts.Metadata)+1)
	for key, value := range opts.Metadata {
		meta[key] = value
	}
	if retryRound > 0 {
		meta[homeRetryRoundMetadataKey] = retryRound
	} else {
		delete(meta, homeRetryRoundMetadataKey)
	}
	opts.Metadata = meta
	return opts
}

func withHomeExcludedAuthIDs(opts cliproxyexecutor.Options, tried map[string]struct{}) cliproxyexecutor.Options {
	meta := make(map[string]any, len(opts.Metadata)+1)
	for key, value := range opts.Metadata {
		meta[key] = value
	}
	excluded := make(map[string]struct{})
	for _, authID := range homeExcludedAuthIDsFromMetadata(meta) {
		excluded[authID] = struct{}{}
	}
	for authID := range tried {
		if authID = strings.TrimSpace(authID); authID != "" {
			excluded[authID] = struct{}{}
		}
	}
	if len(excluded) == 0 {
		delete(meta, ExcludedAuthIDsMetadataKey)
	} else {
		ids := make([]string, 0, len(excluded))
		for authID := range excluded {
			ids = append(ids, authID)
		}
		sort.Strings(ids)
		meta[ExcludedAuthIDsMetadataKey] = ids
	}
	opts.Metadata = meta
	return opts
}

func homeAuthCountFromMetadata(meta map[string]any) int {
	if len(meta) == 0 {
		return 1
	}
	switch value := meta[homeAuthCountMetadataKey].(type) {
	case int:
		if value > 0 {
			return value
		}
	case int64:
		if value > 0 {
			return int(value)
		}
	case float64:
		if value > 0 {
			return int(value)
		}
	}
	return 1
}

func homeExcludedAuthIDsFromMetadata(meta map[string]any) []string {
	if len(meta) == 0 {
		return nil
	}
	raw, ok := meta[ExcludedAuthIDsMetadataKey]
	if !ok {
		return nil
	}
	seen := make(map[string]struct{})
	ids := make([]string, 0)
	appendID := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, exists := seen[value]; exists {
			return
		}
		seen[value] = struct{}{}
		ids = append(ids, value)
	}
	switch values := raw.(type) {
	case []string:
		for _, value := range values {
			appendID(value)
		}
	case []any:
		for _, value := range values {
			if text, okText := value.(string); okText {
				appendID(text)
			}
		}
	case map[string]struct{}:
		for value := range values {
			appendID(value)
		}
	case map[string]bool:
		for value, enabled := range values {
			if enabled {
				appendID(value)
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	sort.Strings(ids)
	return ids
}

func hasRequestedModelMetadata(meta map[string]any) bool {
	if len(meta) == 0 {
		return false
	}
	raw, ok := meta[cliproxyexecutor.RequestedModelMetadataKey]
	if !ok || raw == nil {
		return false
	}
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v) != ""
	case []byte:
		return strings.TrimSpace(string(v)) != ""
	default:
		return false
	}
}

type homeAuthPrepareFlight struct {
	done              chan struct{}
	cancel            context.CancelFunc
	waiters           int
	base              *Auth
	result            *Auth
	err               error
	upstreamAttempted bool
}

func homeAuthPrepareKey(ctx context.Context, auth *Auth) string {
	if auth == nil {
		return ""
	}
	id := strings.TrimSpace(auth.ID)
	if id == "" {
		return ""
	}
	payload := struct {
		ID                  string                       `json:"id"`
		Index               string                       `json:"index,omitempty"`
		RegistrationEpoch   uint64                       `json:"registration_epoch,omitempty"`
		CredentialVersion   uint64                       `json:"credential_version,omitempty"`
		Provider            string                       `json:"provider"`
		Prefix              string                       `json:"prefix,omitempty"`
		Label               string                       `json:"label,omitempty"`
		FileName            string                       `json:"file_name,omitempty"`
		ProxyURL            string                       `json:"proxy_url,omitempty"`
		ProxyConnectionPool *ProxyConnectionPoolOverride `json:"proxy_connection_pool,omitempty"`
		RequestProxyURL     string                       `json:"request_proxy_url,omitempty"`
		Attributes          map[string]string            `json:"attributes,omitempty"`
		Metadata            map[string]any               `json:"metadata,omitempty"`
	}{
		ID:                  id,
		Index:               strings.TrimSpace(auth.Index),
		RegistrationEpoch:   auth.RegistrationEpoch,
		CredentialVersion:   auth.CredentialVersion,
		Provider:            strings.ToLower(strings.TrimSpace(auth.Provider)),
		Prefix:              strings.TrimSpace(auth.Prefix),
		Label:               strings.TrimSpace(auth.Label),
		FileName:            strings.TrimSpace(auth.FileName),
		ProxyURL:            strings.TrimSpace(auth.ProxyURL),
		ProxyConnectionPool: auth.ProxyConnectionPool,
		RequestProxyURL:     strings.TrimSpace(cliproxyexecutor.RequestProxyURL(ctx)),
		Attributes:          auth.Attributes,
		Metadata:            auth.Metadata,
	}
	encoded, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return id + "|" + hex.EncodeToString(sum[:])
}

func prepareProviderLabel(provider string) string {
	label := strings.ToLower(strings.TrimSpace(provider))
	if label == "" {
		return "request"
	}
	return label
}

func sameOptionalInterface(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if sameInterfaceHeader(a, b) {
		return true
	}
	return sameReflectValue(addressableValue(reflect.ValueOf(a)), addressableValue(reflect.ValueOf(b)))
}

func sameInterfaceHeader(a, b any) bool {
	// An empty interface value stores a two-word (type, data) header; matching headers
	// guarantee the interface value was preserved verbatim (for example via Auth.Clone).
	ha := *(*[2]uintptr)(unsafe.Pointer(&a))
	hb := *(*[2]uintptr)(unsafe.Pointer(&b))
	runtime.KeepAlive(a)
	runtime.KeepAlive(b)
	return ha == hb
}

func addressableValue(v reflect.Value) reflect.Value {
	if !v.IsValid() || v.CanAddr() || !v.CanInterface() {
		return v
	}
	copyVal := reflect.New(v.Type()).Elem()
	copyVal.Set(v)
	return copyVal
}

func funcInstancePointer(v reflect.Value) (uintptr, bool) {
	if v.Kind() != reflect.Func {
		return 0, false
	}
	if v.IsNil() {
		return 0, true
	}
	if v.CanAddr() {
		return *(*uintptr)(v.Addr().UnsafePointer()), true
	}
	if v.CanInterface() {
		copyVal := reflect.New(v.Type()).Elem()
		copyVal.Set(v)
		return *(*uintptr)(copyVal.Addr().UnsafePointer()), true
	}
	return 0, false
}

func sameReflectValue(va, vb reflect.Value) bool {
	if !va.IsValid() || !vb.IsValid() {
		return !va.IsValid() && !vb.IsValid()
	}
	if va.Type() != vb.Type() {
		return false
	}
	switch va.Kind() {
	case reflect.Bool:
		return va.Bool() == vb.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return va.Int() == vb.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return va.Uint() == vb.Uint()
	case reflect.Float32, reflect.Float64:
		return va.Float() == vb.Float()
	case reflect.Complex64, reflect.Complex128:
		return va.Complex() == vb.Complex()
	case reflect.String:
		return va.String() == vb.String()
	case reflect.Pointer, reflect.Chan, reflect.UnsafePointer:
		return va.Pointer() == vb.Pointer()
	case reflect.Func:
		if va.IsNil() || vb.IsNil() {
			return va.IsNil() && vb.IsNil()
		}
		pa, okA := funcInstancePointer(va)
		pb, okB := funcInstancePointer(vb)
		return okA && okB && pa == pb
	case reflect.Map:
		if va.IsNil() || vb.IsNil() {
			return va.IsNil() && vb.IsNil()
		}
		return va.Pointer() == vb.Pointer()
	case reflect.Slice:
		if va.IsNil() || vb.IsNil() {
			return va.IsNil() && vb.IsNil()
		}
		return va.Pointer() == vb.Pointer() && va.Len() == vb.Len() && va.Cap() == vb.Cap()
	case reflect.Interface:
		if va.IsNil() || vb.IsNil() {
			return va.IsNil() && vb.IsNil()
		}
		if va.CanAddr() && vb.CanAddr() {
			ha := *(*[2]uintptr)(va.Addr().UnsafePointer())
			hb := *(*[2]uintptr)(vb.Addr().UnsafePointer())
			if ha == hb {
				return true
			}
		}
		return sameReflectValue(addressableValue(va.Elem()), addressableValue(vb.Elem()))
	case reflect.Struct:
		for i := 0; i < va.NumField(); i++ {
			if !sameReflectValue(va.Field(i), vb.Field(i)) {
				return false
			}
		}
		return true
	case reflect.Array:
		for i := 0; i < va.Len(); i++ {
			if !sameReflectValue(va.Index(i), vb.Index(i)) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func mergeHomePreparedSnapshot(base, current, prepared *Auth) *Auth {
	if current == nil {
		return prepared.Clone()
	}
	if prepared == nil {
		return current.Clone()
	}
	merged := MergePreparedAuth(base, current.Clone(), prepared.Clone())
	if merged == nil {
		return nil
	}
	// Preserve caller-owned Storage/Runtime unless preparation actually replaced them.
	if base != nil && sameOptionalInterface(prepared.Storage, base.Storage) {
		merged.Storage = current.Storage
	}
	if base != nil && sameOptionalInterface(prepared.Runtime, base.Runtime) {
		merged.Runtime = current.Runtime
	}
	return merged
}

// prepareHomeRequestAuth prepares a dispatch auth without reading or updating local auth state.
func (m *Manager) prepareHomeRequestAuth(ctx context.Context, selection *HomeDispatchSelection, upstreamModel string) (*Auth, error) {
	if selection == nil {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = context.WithValue(ctx, homeAuthPreparationModelKey{}, canonicalModelKey(upstreamModel))
	auth := selection.CloneAuth()
	credentialExecutor := selection.Executor
	if selection.ExecutorPlugin != "" {
		credentialExecutor = selection.authExecutor
	}
	prepared, errPrepare := m.prepareHomeAuthSnapshot(ctx, credentialExecutor, auth, selection.ExecutorPlugin != "")
	if errPrepare != nil {
		warnLogHomeCredentialFailure(ctx, "request_auth_preparation", selection.Provider, auth, errPrepare)
	} else if selection.ExecutorPlugin != "" && prepared != auth {
		preserveHomeRoutingAttributes(prepared, auth)
	}
	return prepared, errPrepare
}

func (m *Manager) prepareHomeAuthSnapshot(ctx context.Context, executor ProviderExecutor, auth *Auth, delegated bool) (*Auth, error) {
	if m == nil || executor == nil || auth == nil {
		return auth, nil
	}
	preparer, ok := executor.(RequestAuthPreparer)
	if !ok || preparer == nil {
		return auth, nil
	}
	shouldPrepare := preparer.ShouldPrepareRequestAuth
	if providerPreparer, okProvider := preparer.(HomePluginRequestAuthPreparer); delegated && okProvider {
		shouldPrepare = providerPreparer.ShouldPrepareHomePluginRequestAuth
	}
	if !shouldPrepare(auth) {
		return auth, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if errCtx := ctx.Err(); errCtx != nil {
		return auth, errCtx
	}

	prepareDirect := func(callCtx context.Context) (*Auth, error) {
		target := auth.Clone()
		if !shouldPrepare(target) {
			return target, nil
		}
		updated, errPrepare := preparer.PrepareRequestAuth(callCtx, target)
		if errPrepare != nil {
			return auth, errPrepare
		}
		if updated == nil {
			return target, nil
		}
		return updated, nil
	}

	key := homeAuthPrepareKey(ctx, auth)
	if key == "" {
		return prepareDirect(ctx)
	}

	m.homePrepareMu.Lock()
	if m.homePrepareFlights == nil {
		m.homePrepareFlights = make(map[string]*homeAuthPrepareFlight)
	}
	flight, exists := m.homePrepareFlights[key]
	if exists && flight != nil {
		flight.waiters++
		m.homePrepareMu.Unlock()
	} else {
		flightCtx, cancelFlight := context.WithCancel(context.WithoutCancel(ctx))
		flightCtx = cliproxyexecutor.WithUpstreamAttemptTracker(flightCtx)
		// Clone once before starting the goroutine so the caller's auth cannot race.
		baseSnapshot := auth.Clone()
		snapshot := baseSnapshot.Clone()
		flight = &homeAuthPrepareFlight{
			done:    make(chan struct{}),
			cancel:  cancelFlight,
			waiters: 1,
			base:    baseSnapshot,
		}
		m.homePrepareFlights[key] = flight
		m.homePrepareMu.Unlock()

		authID := strings.TrimSpace(snapshot.ID)
		providerLabel := prepareProviderLabel(snapshot.Provider)
		go func() {
			defer func() {
				if r := recover(); r != nil {
					// Avoid logging or returning panic values that may embed credential material.
					log.Errorf("home request auth preparation panicked for provider=%s auth_id=%s", providerLabel, authID)
					flight.result = nil
					flight.err = fmt.Errorf("prepare %s auth: unexpected panic during home preparation", providerLabel)
				}
				cancelFlight()
				m.homePrepareMu.Lock()
				if m.homePrepareFlights[key] == flight {
					delete(m.homePrepareFlights, key)
				}
				m.homePrepareMu.Unlock()
				close(flight.done)
			}()

			if !shouldPrepare(snapshot) {
				flight.result = snapshot
				return
			}
			updated, errPrepare := preparer.PrepareRequestAuth(flightCtx, snapshot)
			flight.upstreamAttempted = cliproxyexecutor.UpstreamAttempted(flightCtx)
			if errPrepare != nil {
				flight.err = errPrepare
				return
			}
			if updated == nil {
				flight.result = snapshot
				return
			}
			flight.result = updated
		}()
	}

	select {
	case <-flight.done:
		if flight.upstreamAttempted {
			cliproxyexecutor.MarkUpstreamAttempt(ctx)
		}
		if flight.err != nil {
			return auth, flight.err
		}
		if flight.result == nil {
			return auth.Clone(), nil
		}
		return mergeHomePreparedSnapshot(flight.base, auth, flight.result), nil
	case <-ctx.Done():
		m.homePrepareMu.Lock()
		flight.waiters--
		shouldCancel := flight.waiters == 0
		if shouldCancel && m.homePrepareFlights[key] == flight {
			delete(m.homePrepareFlights, key)
		}
		m.homePrepareMu.Unlock()
		if shouldCancel {
			flight.cancel()
		}
		return auth, ctx.Err()
	}
}

func (m *Manager) prepareRequestAuth(ctx context.Context, executor ProviderExecutor, auth *Auth) (*Auth, error) {
	if m == nil || executor == nil || auth == nil {
		return auth, nil
	}
	preparer, ok := executor.(RequestAuthPreparer)
	if !ok {
		return auth, nil
	}

	return m.PrepareRequestAuth(ctx, preparer, auth)
}

func (m *Manager) prepareRequestAuthAtVersion(ctx context.Context, executor ProviderExecutor, auth *Auth, expectedEpoch, expectedVersion uint64) (*Auth, error) {
	if m == nil || executor == nil || auth == nil {
		return auth, nil
	}
	preparer, ok := executor.(RequestAuthPreparer)
	if !ok {
		return auth, nil
	}

	return m.prepareRequestAuthInternal(ctx, preparer, auth, expectedEpoch, expectedVersion)
}

// PrepareRequestAuth prepares a registered credential using the same serialization
// and lifecycle checks as normal request execution. Management tools use this path too.
func (m *Manager) PrepareRequestAuth(ctx context.Context, preparer RequestAuthPreparer, auth *Auth) (*Auth, error) {
	return m.prepareRequestAuthInternal(ctx, preparer, auth, 0, 0)
}

func (m *Manager) prepareRequestAuthInternal(ctx context.Context, preparer RequestAuthPreparer, auth *Auth, expectedEpoch, expectedVersion uint64) (*Auth, error) {
	if m == nil || preparer == nil || auth == nil {
		return auth, nil
	}
	id := strings.TrimSpace(auth.ID)
	if !preparer.ShouldPrepareRequestAuth(auth) {
		if expectedVersion != 0 && id != "" {
			current, okCurrent := m.GetByID(id)
			if !okCurrent || current == nil {
				return nil, errVerifyCredentialRemoved
			}
			if (expectedEpoch != 0 && current.RegistrationEpoch != expectedEpoch) || current.CredentialVersion != expectedVersion {
				return nil, errVerifyCredentialSuperseded
			}
		}
		return auth, nil
	}

	if id == "" {
		return preparer.PrepareRequestAuth(ctx, auth.Clone())
	}

	releaseRefresh, errLock := m.lockRefreshContext(ctx, id)
	if errLock != nil {
		return nil, errLock
	}
	defer releaseRefresh()

	m.mu.RLock()
	current := m.auths[id]
	var target *Auth
	if current != nil {
		target = current.Clone()
	}
	m.mu.RUnlock()
	if target == nil {
		if expectedVersion != 0 {
			return nil, errVerifyCredentialRemoved
		}
		return nil, fmt.Errorf("prepare %s auth: credential no longer registered", prepareProviderLabel(auth.Provider))
	}
	if expectedVersion != 0 && ((expectedEpoch != 0 && target.RegistrationEpoch != expectedEpoch) || target.CredentialVersion != expectedVersion) {
		return nil, errVerifyCredentialSuperseded
	}

	if !preparer.ShouldPrepareRequestAuth(target) {
		return target, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	base := target.Clone()
	currentExpectedVersion := expectedVersion
	var latestCommitted *Auth
	prepOpts := executionAuthRefreshOptions{
		holdRefreshLock: true,
		expectedEpoch:   expectedEpoch,
		onAuthUpdated: func(saved *Auth) {
			if saved != nil {
				base = saved.Clone()
				latestCommitted = saved.Clone()
			}
		},
	}
	if expectedVersion != 0 {
		prepOpts.expectedVersion = &currentExpectedVersion
	}
	prepCtx := m.withExecutionAuthRefresh(ctx, prepOpts)
	updated, errPrepare := preparer.PrepareRequestAuth(prepCtx, base.Clone())
	if expectedVersion != 0 {
		currentAfter, okAfter := m.GetByID(id)
		if !okAfter || currentAfter == nil {
			return nil, errVerifyCredentialRemoved
		}
		if (expectedEpoch != 0 && currentAfter.RegistrationEpoch != expectedEpoch) || currentAfter.CredentialVersion != currentExpectedVersion {
			return nil, errVerifyCredentialSuperseded
		}
	}
	if errPrepare != nil {
		if latestCommitted != nil {
			return latestCommitted, errPrepare
		}
		return auth, errPrepare
	}
	if updated == nil {
		if latestCommitted != nil {
			if errCtx := ctx.Err(); errCtx != nil {
				return latestCommitted, errCtx
			}
			return latestCommitted, nil
		}
		return target, nil
	}
	if latestCommitted != nil && preparedAuthMatchesBase(base, updated) {
		if errCtx := ctx.Err(); errCtx != nil {
			return latestCommitted, errCtx
		}
		return latestCommitted, nil
	}

	commitCtx := context.WithoutCancel(ctx)
	if expectedVersion != 0 {
		saved, committed, baseSuperseded, errUpdate := m.updatePreparedAuthStrict(commitCtx, base, updated)
		if errUpdate != nil {
			if strings.Contains(errUpdate.Error(), "stale registration epoch") {
				return nil, errVerifyCredentialSuperseded
			}
			return nil, errUpdate
		}
		if saved == nil {
			return nil, errVerifyCredentialRemoved
		}
		if !committed || baseSuperseded {
			return nil, errVerifyCredentialSuperseded
		}
		if errCtx := ctx.Err(); errCtx != nil {
			return saved, errCtx
		}
		return saved, nil
	}

	saved, errUpdate := m.UpdatePreparedAuth(commitCtx, base, updated)
	if errUpdate != nil {
		return nil, errUpdate
	}
	if saved != nil {
		if errCtx := ctx.Err(); errCtx != nil {
			return saved, errCtx
		}
		return saved, nil
	}
	if strings.EqualFold(strings.TrimSpace(auth.Provider), "meta") {
		return nil, fmt.Errorf("prepare meta auth: credential removed during mint")
	}
	return nil, fmt.Errorf("prepare %s auth: credential removed during preparation", prepareProviderLabel(auth.Provider))
}

func contextWithRequestedModelAlias(ctx context.Context, opts cliproxyexecutor.Options, fallback string) context.Context {
	alias := requestedModelAliasFromOptions(opts, fallback)
	ctx = coreusage.WithRequestedModelAlias(ctx, alias)
	effort := reasoningEffortFromOptions(opts)
	if effort != "" {
		ctx = coreusage.WithReasoningEffort(ctx, effort)
	}
	serviceTier := serviceTierFromOptions(opts)
	if serviceTier != "" {
		ctx = coreusage.WithServiceTier(ctx, serviceTier)
	}
	if generate, ok := generateFromOptions(opts); ok {
		ctx = coreusage.WithGenerate(ctx, generate)
	}
	ctx = coreusage.WithStream(ctx, opts.Stream)
	if selectionID := selectionIDFromOptions(opts); selectionID != "" {
		ctx = coreusage.WithSelectionID(ctx, selectionID)
	} else if _, exists := opts.Metadata[cliproxyexecutor.SelectionIDMetadataKey]; exists {
		ctx = coreusage.WithSelectionID(ctx, "")
	}
	return ctx
}

func selectionIDFromOptions(opts cliproxyexecutor.Options) string {
	return stringMetadataValue(opts.Metadata, cliproxyexecutor.SelectionIDMetadataKey)
}

func requestedModelAliasFromOptions(opts cliproxyexecutor.Options, fallback string) string {
	fallback = strings.TrimSpace(fallback)
	if len(opts.Metadata) == 0 {
		return fallback
	}
	raw, ok := opts.Metadata[cliproxyexecutor.RequestedModelMetadataKey]
	if !ok || raw == nil {
		return fallback
	}
	switch value := raw.(type) {
	case string:
		if strings.TrimSpace(value) == "" {
			return fallback
		}
		return strings.TrimSpace(value)
	case []byte:
		if len(value) == 0 {
			return fallback
		}
		return strings.TrimSpace(string(value))
	default:
		return fallback
	}
}

func reasoningEffortFromOptions(opts cliproxyexecutor.Options) string {
	if len(opts.Metadata) == 0 {
		return ""
	}
	raw, ok := opts.Metadata[cliproxyexecutor.ReasoningEffortMetadataKey]
	if !ok || raw == nil {
		return ""
	}
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case []byte:
		return strings.TrimSpace(string(value))
	default:
		return ""
	}
}

func serviceTierFromOptions(opts cliproxyexecutor.Options) string {
	return stringMetadataValue(opts.Metadata, cliproxyexecutor.ServiceTierMetadataKey)
}

func generateFromOptions(opts cliproxyexecutor.Options) (bool, bool) {
	if len(opts.Metadata) == 0 {
		return false, false
	}
	raw, ok := opts.Metadata[cliproxyexecutor.GenerateMetadataKey]
	if !ok || raw == nil {
		return false, false
	}
	switch value := raw.(type) {
	case bool:
		return value, true
	default:
		return false, false
	}
}

func stringMetadataValue(metadata map[string]any, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	raw, ok := metadata[key]
	if !ok || raw == nil {
		return ""
	}
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case []byte:
		return strings.TrimSpace(string(value))
	default:
		return ""
	}
}

func pinnedAuthIDFromMetadata(meta map[string]any) string {
	if len(meta) == 0 {
		return ""
	}
	raw, ok := meta[cliproxyexecutor.PinnedAuthMetadataKey]
	if !ok || raw == nil {
		return ""
	}
	switch val := raw.(type) {
	case string:
		return strings.TrimSpace(val)
	case []byte:
		return strings.TrimSpace(string(val))
	default:
		return ""
	}
}

func disallowFreeAuthFromMetadata(meta map[string]any) bool {
	if len(meta) == 0 {
		return false
	}
	raw, ok := meta[cliproxyexecutor.DisallowFreeAuthMetadataKey]
	if !ok || raw == nil {
		return false
	}
	switch val := raw.(type) {
	case bool:
		return val
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(val))
		return err == nil && parsed
	case []byte:
		parsed, err := strconv.ParseBool(strings.TrimSpace(string(val)))
		return err == nil && parsed
	default:
		return false
	}
}

func isFreeCodexAuth(auth *Auth) bool {
	if auth == nil || auth.Attributes == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(auth.Attributes["plan_type"]), "free")
}

func publishSelectedAuthMetadata(meta map[string]any, auth *Auth) {
	if len(meta) == 0 || auth == nil {
		return
	}
	if authID := strings.TrimSpace(auth.ID); authID != "" {
		meta[cliproxyexecutor.SelectedAuthMetadataKey] = authID
		if callback, ok := meta[cliproxyexecutor.SelectedAuthCallbackMetadataKey].(func(string)); ok && callback != nil {
			callback(authID)
		}
	}
	if authIndex := strings.TrimSpace(auth.EnsureIndex()); authIndex != "" {
		meta[cliproxyexecutor.SelectedAuthIndexMetadataKey] = authIndex
		if callback, ok := meta[cliproxyexecutor.SelectedAuthIndexCallbackMetadataKey].(func(string)); ok && callback != nil {
			callback(authIndex)
		}
	}
}

func syncPublishedSelectedAuthMetadata(dst, src map[string]any) {
	if dst == nil || src == nil {
		return
	}
	if authID, ok := src[cliproxyexecutor.SelectedAuthMetadataKey]; ok {
		dst[cliproxyexecutor.SelectedAuthMetadataKey] = authID
	} else {
		delete(dst, cliproxyexecutor.SelectedAuthMetadataKey)
	}
	if authIndex, ok := src[cliproxyexecutor.SelectedAuthIndexMetadataKey]; ok {
		dst[cliproxyexecutor.SelectedAuthIndexMetadataKey] = authIndex
	} else {
		delete(dst, cliproxyexecutor.SelectedAuthIndexMetadataKey)
	}
}

func (m *Manager) executorFor(provider string) ProviderExecutor {
	m.mu.RLock()
	defer m.mu.RUnlock()
	exec, _ := m.executorLocked(provider)
	return exec
}

// roundTripperContextKey is an unexported context key type to avoid collisions.
type roundTripperContextKey struct{}

// WithAuthTransport applies the same per-account transport to inference and model discovery.
func (m *Manager) WithAuthTransport(ctx context.Context, auth *Auth) context.Context {
	if rt := m.roundTripperFor(auth); rt != nil {
		ctx = context.WithValue(ctx, roundTripperContextKey{}, rt)
		return context.WithValue(ctx, "cliproxy.roundtripper", rt)
	}
	return ctx
}

// roundTripperFor retrieves an HTTP RoundTripper for the given auth if a provider is registered.
func (m *Manager) roundTripperFor(auth *Auth) http.RoundTripper {
	m.mu.RLock()
	p := m.rtProvider
	m.mu.RUnlock()
	if p == nil || auth == nil {
		return nil
	}
	return p.RoundTripperFor(auth)
}

// RoundTripperProvider defines a minimal provider of per-auth HTTP transports.
type RoundTripperProvider interface {
	RoundTripperFor(auth *Auth) http.RoundTripper
}

// RequestPreparer is an optional interface that provider executors can implement
// to mutate outbound HTTP requests with provider credentials.
type RequestPreparer interface {
	PrepareRequest(req *http.Request, auth *Auth) error
}

func executorKeyFromAuth(auth *Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		providerKey := strings.TrimSpace(auth.Attributes["provider_key"])
		compatName := strings.TrimSpace(auth.Attributes["compat_name"])
		if compatName != "" {
			if providerKey == "" {
				providerKey = compatName
			}
			return util.OpenAICompatibleProviderKey(providerKey)
		}
	}
	if strings.EqualFold(strings.TrimSpace(auth.Provider), "openai-compatibility") {
		providerKey := strings.TrimSpace(auth.Label)
		if providerKey == "" {
			providerKey = "openai-compatibility"
		}
		return util.OpenAICompatibleProviderKey(providerKey)
	}
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	switch provider {
	case "kimi.com":
		return "kimi"
	case "kimi.ai":
		return "kimi-ai"
	default:
		return provider
	}
}

// logEntryWithRequestID returns a logrus entry with request_id field if available in context.
func logEntryWithRequestID(ctx context.Context) *log.Entry {
	if ctx == nil {
		return log.NewEntry(log.StandardLogger())
	}
	if reqID := logging.GetRequestID(ctx); reqID != "" {
		return log.WithField("request_id", reqID)
	}
	return log.NewEntry(log.StandardLogger())
}

func debugLogAuthSelection(entry *log.Entry, auth *Auth, provider string, model string) {
	if !log.IsLevelEnabled(log.DebugLevel) {
		return
	}
	if entry == nil || auth == nil {
		return
	}
	accountType, accountInfo := auth.AccountInfo()
	proxyInfo := auth.ProxyInfo()
	suffix := ""
	if proxyInfo != "" {
		suffix = " " + proxyInfo
	}
	switch accountType {
	case "api_key":
		entry.Debugf("Use API key %s for model %s%s", util.HideAPIKey(accountInfo), model, suffix)
	case "oauth":
		ident := formatOauthIdentity(auth, provider, accountInfo)
		entry.Debugf("Use OAuth %s for model %s%s", ident, model, suffix)
	}
}

func formatOauthIdentity(auth *Auth, provider string, accountInfo string) string {
	if auth == nil {
		return ""
	}
	// Prefer the auth's provider when available.
	providerName := strings.TrimSpace(auth.Provider)
	if providerName == "" {
		providerName = strings.TrimSpace(provider)
	}
	// Only log the basename to avoid leaking host paths.
	// FileName may be unset for some auth backends; fall back to ID.
	authFile := strings.TrimSpace(auth.FileName)
	if authFile == "" {
		authFile = strings.TrimSpace(auth.ID)
	}
	if authFile != "" {
		authFile = filepath.Base(authFile)
	}
	parts := make([]string, 0, 3)
	if providerName != "" {
		parts = append(parts, "provider="+providerName)
	}
	if authFile != "" {
		parts = append(parts, "auth_file="+authFile)
	}
	if len(parts) == 0 {
		return accountInfo
	}
	return strings.Join(parts, " ")
}

func formatAuthIdentity(auth *Auth, provider string) string {
	if auth == nil {
		return "auth=nil"
	}
	accountType, accountInfo := auth.AccountInfo()
	switch accountType {
	case "api_key":
		return fmt.Sprintf("api_key=%s", util.HideAPIKey(accountInfo))
	case "oauth":
		return formatOauthIdentity(auth, provider, accountInfo)
	default:
		if auth.FileName != "" {
			return fmt.Sprintf("auth_file=%s", filepath.Base(auth.FileName))
		}
		if auth.ID != "" {
			return fmt.Sprintf("auth_id=%s", auth.ID)
		}
		if accountInfo != "" {
			return accountInfo
		}
		return "unknown"
	}
}

func warnLogHomeCredentialFailure(ctx context.Context, operation, provider string, auth *Auth, err error) {
	if err == nil {
		return
	}
	provider = strings.TrimSpace(provider)
	if provider == "" && auth != nil {
		provider = strings.TrimSpace(auth.Provider)
	}
	fields := log.Fields{
		"auth":      formatAuthIdentity(auth, provider),
		"operation": operation,
		"provider":  provider,
	}
	if statusCode := statusCodeFromError(err); statusCode != 0 {
		fields["status"] = statusCode
	}
	logEntryWithRequestID(ctx).WithFields(fields).Warnf("Home credential operation failed: err=%s", safeErrorDiagnosticForLog(err))
}

func safeErrorDiagnosticForLog(err error) string {
	if err == nil {
		return ""
	}
	diagnostic := err.Error()
	type logDiagnosticError interface {
		LogDiagnostic() string
	}
	var diagnosticErr logDiagnosticError
	if errors.As(err, &diagnosticErr) && diagnosticErr != nil {
		if markedDiagnostic := strings.TrimSpace(diagnosticErr.LogDiagnostic()); markedDiagnostic != "" {
			diagnostic = markedDiagnostic
		}
	}
	return logging.SafeDiagnosticForLog(diagnostic)
}

func warnLogUpstreamFailure(ctx context.Context, entry *log.Entry, provider, model string, auth *Auth, duration time.Duration, err error) {
	if err == nil {
		return
	}
	if ctx != nil && errors.Is(ctx.Err(), context.Canceled) {
		return
	}
	if errors.Is(err, context.Canceled) {
		return
	}
	if isRequestInvalidError(err) {
		return
	}
	if entry == nil {
		if ctx != nil {
			entry = logEntryWithRequestID(ctx)
		} else {
			entry = log.NewEntry(log.StandardLogger())
		}
	}
	authIdent := formatAuthIdentity(auth, provider)
	errSummary := safeErrorDiagnosticForLog(err)
	duration = duration.Round(time.Millisecond)
	if statusCode := statusCodeFromError(err); statusCode != 0 {
		entry.Warnf("%3d | %13v | upstream execution failed: provider=%s model=%s auth=%s err=%s", statusCode, duration, provider, model, authIdent, errSummary)
		return
	}
	entry.Warnf("upstream execution failed: provider=%s model=%s auth=%s duration=%s err=%s", provider, model, authIdent, duration, errSummary)
}

// InjectCredentials delegates per-provider HTTP request preparation when supported.
// If the registered executor for the auth provider implements RequestPreparer,
// it will be invoked to modify the request (e.g., add headers).
func (m *Manager) InjectCredentials(req *http.Request, authID string) error {
	if req == nil || authID == "" {
		return nil
	}
	m.mu.RLock()
	a := m.auths[authID]
	var authClone *Auth
	var exec ProviderExecutor
	if a != nil {
		authClone = a.Clone()
		exec, _ = m.executorLocked(executorKeyFromAuth(a))
	}
	m.mu.RUnlock()
	if authClone == nil || exec == nil {
		return nil
	}
	if p, ok := exec.(RequestPreparer); ok && p != nil {
		return m.prepareHTTPRequestWithExecutionAuthRefresh(req, authClone, p)
	}
	return nil
}

func (m *Manager) prepareHTTPRequestWithExecutionAuthRefresh(req *http.Request, auth *Auth, preparer RequestPreparer) error {
	origCtx := req.Context()
	prepCtx := m.withExecutionAuthRefresh(origCtx, executionAuthRefreshOptions{})
	prepReq := req.WithContext(prepCtx)
	errPrep := preparer.PrepareRequest(prepReq, auth)
	finalCtx := origCtx
	if updatedCtx := prepReq.Context(); updatedCtx != prepCtx {
		finalCtx = context.WithValue(updatedCtx, executionAuthRefreshContextKey{}, nil)
	}
	*req = *prepReq.WithContext(finalCtx)
	return errPrep
}

// PrepareHttpRequest injects provider credentials into the supplied HTTP request.
func (m *Manager) PrepareHttpRequest(ctx context.Context, auth *Auth, req *http.Request) error {
	if m == nil {
		return &Error{Code: "provider_not_found", Message: "manager is nil"}
	}
	if auth == nil {
		return &Error{Code: "auth_not_found", Message: "auth is nil"}
	}
	if req == nil {
		return &Error{Code: "invalid_request", Message: "http request is nil"}
	}
	if ctx != nil {
		*req = *req.WithContext(ctx)
	}
	providerKey := executorKeyFromAuth(auth)
	if providerKey == "" {
		return &Error{Code: "provider_not_found", Message: "auth provider is empty"}
	}
	exec := m.executorFor(providerKey)
	if exec == nil {
		return &Error{Code: "provider_not_found", Message: "executor not registered for provider: " + providerKey}
	}
	preparer, ok := exec.(RequestPreparer)
	if !ok || preparer == nil {
		return &Error{Code: "not_supported", Message: "executor does not support http request preparation"}
	}
	return m.prepareHTTPRequestWithExecutionAuthRefresh(req, auth, preparer)
}

// NewHttpRequest constructs a new HTTP request and injects provider credentials into it.
func (m *Manager) NewHttpRequest(ctx context.Context, auth *Auth, method, targetURL string, body []byte, headers http.Header) (*http.Request, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	method = strings.TrimSpace(method)
	if method == "" {
		method = http.MethodGet
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, targetURL, reader)
	if err != nil {
		return nil, err
	}
	if headers != nil {
		httpReq.Header = headers.Clone()
	}
	if errPrepare := m.PrepareHttpRequest(ctx, auth, httpReq); errPrepare != nil {
		return nil, errPrepare
	}
	return httpReq, nil
}

// HttpRequest injects provider credentials into the supplied HTTP request and executes it.
func (m *Manager) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	if m == nil {
		return nil, &Error{Code: "provider_not_found", Message: "manager is nil"}
	}
	if auth == nil {
		return nil, &Error{Code: "auth_not_found", Message: "auth is nil"}
	}
	if req == nil {
		return nil, &Error{Code: "invalid_request", Message: "http request is nil"}
	}
	providerKey := executorKeyFromAuth(auth)
	if providerKey == "" {
		return nil, &Error{Code: "provider_not_found", Message: "auth provider is empty"}
	}
	exec := m.executorFor(providerKey)
	if exec == nil {
		return nil, &Error{Code: "provider_not_found", Message: "executor not registered for provider: " + providerKey}
	}
	if ctx == nil {
		ctx = req.Context()
	}
	return exec.HttpRequest(m.withExecutionAuthRefresh(ctx, executionAuthRefreshOptions{}), auth, req)
}

func ensureCanonicalSessionMetadata(metadata map[string]any, headers http.Header, payload []byte) map[string]any {
	if metadata != nil {
		if canonicalID, ok := metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey].(string); ok && strings.TrimSpace(canonicalID) != "" {
			return metadata
		}
	}
	canonicalID := CanonicalSessionID(headers, payload, metadata)
	if canonicalID == "" {
		return metadata
	}
	out := make(map[string]any, len(metadata)+1)
	for k, v := range metadata {
		out[k] = v
	}
	out[cliproxyexecutor.CanonicalSessionIDMetadataKey] = canonicalID
	return out
}

func syncMetadataSessionToContext(ctx context.Context, metadata map[string]any) context.Context {
	if ctx == nil {
		return nil
	}
	canonicalID := ""
	if len(metadata) > 0 {
		canonicalID, _ = metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey].(string)
		if canonicalID == "" {
			canonicalID, _ = metadata[cliproxyexecutor.LCPAffinitySessionIDMetadataKey].(string)
		}
		if canonicalID == "" {
			if execID, _ := metadata[cliproxyexecutor.ExecutionSessionMetadataKey].(string); execID != "" {
				execID = strings.TrimSpace(execID)
				if !strings.HasPrefix(execID, "execution:") {
					canonicalID = "execution:" + execID
				} else {
					canonicalID = execID
				}
			}
		}
		if canonicalID == "" {
			if derivedID, _ := metadata[cliproxyexecutor.DerivedSessionIDMetadataKey].(string); derivedID != "" {
				derivedID = strings.TrimSpace(derivedID)
				if !strings.HasPrefix(derivedID, "derived:") {
					canonicalID = "derived:" + derivedID
				} else {
					canonicalID = derivedID
				}
			}
		}
	}
	canonicalID = strings.TrimSpace(canonicalID)
	if canonicalID == "" {
		clientMeta := logging.GetClientRequestMetadata(ctx)
		if clientMeta.SessionID != "" || clientMeta.ParentSessionID != "" || clientMeta.NodeKind != "" || clientMeta.IsFork || clientMeta.IsCompaction {
			clientMeta.SessionID = ""
			clientMeta.ParentSessionID = ""
			clientMeta.NodeKind = ""
			clientMeta.IsFork = false
			clientMeta.IsCompaction = false
			ctx = logging.WithClientRequestMetadata(ctx, clientMeta)
		}
		return util.WithSessionID(ctx, "")
	}
	clientMeta := logging.GetClientRequestMetadata(ctx)
	clientMeta.SessionID = cliproxysession.BoundSessionIdentity(canonicalID)
	if parentID, ok := metadata[cliproxyexecutor.ParentSessionIDMetadataKey].(string); ok && strings.TrimSpace(parentID) != "" {
		clientMeta.ParentSessionID = cliproxysession.BoundSessionIdentity(strings.TrimSpace(parentID))
	} else {
		clientMeta.ParentSessionID = ""
	}
	if clientMeta.SessionID == clientMeta.ParentSessionID {
		clientMeta.ParentSessionID = ""
	}
	if nodeKind, ok := metadata[cliproxyexecutor.NodeKindMetadataKey].(string); ok && strings.TrimSpace(nodeKind) != "" {
		clientMeta.NodeKind = strings.TrimSpace(nodeKind)
	} else {
		clientMeta.NodeKind = ""
	}
	if isFork, ok := metadata[cliproxyexecutor.IsForkMetadataKey].(bool); ok {
		clientMeta.IsFork = isFork
	} else {
		clientMeta.IsFork = false
	}
	if isCompaction, ok := metadata[cliproxyexecutor.IsCompactionMetadataKey].(bool); ok {
		clientMeta.IsCompaction = isCompaction
	} else {
		clientMeta.IsCompaction = false
	}
	ctx = logging.WithClientRequestMetadata(ctx, clientMeta)
	return util.WithSessionID(ctx, clientMeta.SessionID)
}

func modelModalitiesContain(modalities []string, target string) bool {
	for _, modality := range modalities {
		if strings.EqualFold(strings.TrimSpace(modality), target) {
			return true
		}
	}
	return false
}

func modelSupportsTextProbeModalities(info *registry.ModelInfo) bool {
	if info == nil {
		return false
	}
	if len(info.SupportedInputModalities) > 0 && !modelModalitiesContain(info.SupportedInputModalities, "text") {
		return false
	}
	if len(info.SupportedOutputModalities) > 0 && !modelModalitiesContain(info.SupportedOutputModalities, "text") {
		return false
	}
	return true
}

func selectVerifyInferenceModel(models []*registry.ModelInfo) string {
	var nonThinking, zeroAllowed, firstText, textModalityFallback, fallback string
	for _, info := range models {
		if info == nil {
			continue
		}
		id := strings.TrimSpace(info.ID)
		if id == "" {
			continue
		}
		if fallback == "" {
			fallback = id
		}
		if !modelSupportsTextProbeModalities(info) {
			continue
		}
		lowerID := strings.ToLower(id)
		if strings.Contains(lowerID, "image") || strings.Contains(lowerID, "video") {
			if textModalityFallback == "" {
				textModalityFallback = id
			}
			continue
		}
		if firstText == "" {
			firstText = id
		}
		if info.Thinking == nil {
			if nonThinking == "" {
				nonThinking = id
			}
			continue
		}
		if info.Thinking.ZeroAllowed && zeroAllowed == "" {
			zeroAllowed = id
		}
	}
	switch {
	case nonThinking != "":
		return nonThinking
	case zeroAllowed != "":
		return zeroAllowed
	case firstText != "":
		return firstText
	case textModalityFallback != "":
		return textModalityFallback
	default:
		return fallback
	}
}

func resolveVerifyInferenceRouteModel(auth *Auth, provider, model string) (string, error) {
	if routeModel := strings.TrimSpace(model); routeModel != "" {
		return routeModel, nil
	}
	if auth != nil {
		if selected := selectVerifyInferenceModel(registry.GetGlobalRegistry().GetModelsForClient(auth.ID)); selected != "" {
			return selected, nil
		}
	}
	switch provider {
	case "codex", "openai":
		return "gpt-5", nil
	default:
		return "", &Error{
			Code:       "model_required",
			Message:    fmt.Sprintf("no registered models available for %s credential; specify a model or wait for model discovery", provider),
			HTTPStatus: http.StatusBadRequest,
		}
	}
}

func buildVerifyInferencePayload(provider, verifyModel string) []byte {
	doc := ojson.NewDocString(`{"model":"","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"ping"}]}],"store":false}`)
	_ = doc.Set("model", verifyModel)
	if provider == "codex" {
		// Codex upstreams reject integer max_output_tokens below 16, so the minimal
		// verification budget must sit at that floor. Other providers omit
		// max_output_tokens to avoid rejecting or truncating thinking models.
		_ = doc.Set("max_output_tokens", 16)
	}
	return doc.Bytes()
}

func verifyCancellationError(execCtx context.Context, auth *Auth, err error) error {
	if execCtx != nil {
		if errCtx := execCtx.Err(); errCtx != nil {
			return errCtx
		}
	}
	return claudeOAuthRequestCancellation(execCtx, auth, err)
}

func verifyCredentialSupersededError() *Error {
	return &Error{
		Code:       "credential_superseded",
		Message:    "credential was replaced during verification; re-run verification for the new credential",
		HTTPStatus: http.StatusConflict,
	}
}

func verifyCredentialRemovedError() *Error {
	return &Error{
		Code:       "auth_not_found",
		Message:    "credential was removed during verification",
		HTTPStatus: http.StatusConflict,
	}
}

// tryRefreshVerifyAuthAfterUnauthorized refreshes an account-pinned credential
// once after a 401 while fencing on both RegistrationEpoch and CredentialVersion
// so an external replacement with the same ID fails with 409 credential_superseded
// instead of retrying or verifying the replacement credential.
func (m *Manager) tryRefreshVerifyAuthAfterUnauthorized(ctx context.Context, auth *Auth, execErr error, alreadyTried bool, clientHeaders http.Header, credVersion, regEpoch uint64) (*Auth, bool, error) {
	if m == nil || auth == nil || execErr == nil {
		return auth, false, nil
	}
	if isRequestScopedError(execErr) || isRequestStopError(execErr) || isRequestTerminatedError(execErr) || isTOSViolationError(execErr) || isInvalidGrantError(execErr) || !isUnauthorizedError(execErr) {
		return auth, false, nil
	}
	if _, ruleMatched := matchRequestScopedErrorAction(auth, execErr, m.requestScopedErrorConfig(ctx)); ruleMatched {
		return auth, false, nil
	}
	current, okCurrent := m.GetByID(auth.ID)
	if !okCurrent || current == nil {
		return nil, false, verifyCredentialRemovedError()
	}
	if current.RegistrationEpoch != regEpoch || current.CredentialVersion != credVersion {
		return current, false, verifyCredentialSupersededError()
	}
	failedToken := authAccessTokenForRequest(ctx, auth, clientHeaders)
	if alreadyTried || !authHasRefreshCredential(auth) || hasUnauthorizedAuthFailure(auth) {
		if failedToken != "" {
			m.markRejectedAccessToken(auth.ID, failedToken)
		}
		return auth, false, nil
	}
	log.Debugf("unauthorized response for %s (%s) during verification, refreshing credentials before retry", auth.Provider, auth.ID)
	refreshed, errRefresh := m.refreshAuthForRequestAtVersion(ctx, auth.ID, failedToken, regEpoch, credVersion)
	if errors.Is(errRefresh, errVerifyCredentialRemoved) {
		return nil, false, verifyCredentialRemovedError()
	}
	if errors.Is(errRefresh, errVerifyCredentialSuperseded) {
		latest, _ := m.GetByID(auth.ID)
		return latest, false, verifyCredentialSupersededError()
	}
	if errRefresh != nil || refreshed == nil {
		log.Debugf("credential refresh during verification failed for %s (%s): %v", auth.Provider, auth.ID, errRefresh)
		latest, okLatest := m.GetByID(auth.ID)
		if !okLatest || latest == nil {
			return nil, false, verifyCredentialRemovedError()
		}
		if latest.RegistrationEpoch != regEpoch || latest.CredentialVersion != credVersion {
			return latest, false, verifyCredentialSupersededError()
		}
		return auth, false, nil
	}
	if failedToken != "" && authAccessTokenForRequest(ctx, refreshed, clientHeaders) == failedToken && (refreshed.RejectedAccessToken == failedToken || refreshed.RejectedSIWCAccessToken == failedToken) {
		return auth, false, nil
	}
	return refreshed, true, nil
}

func (m *Manager) recordVerifyFailure(execCtx context.Context, auth *Auth, provider, stateModel, routeModel string, opts cliproxyexecutor.Options, accessToken string, credVersion, regEpoch, executionSeq uint64, err error) {
	rerr := resultErrorFromError(err)
	if rerr != nil {
		rerr.Message = sanitizeRenewalMessage(rerr.Message)
	}
	result := Result{
		AuthID:            auth.ID,
		Provider:          provider,
		Model:             stateModel,
		RouteModel:        routeModel,
		Success:           false,
		Error:             rerr,
		RetryAfter:        retryAfterFromError(err),
		CredentialScope:   isCredentialScopedError(err),
		Options:           opts,
		AccessToken:       accessToken,
		RefreshToken:      authRefreshToken(auth),
		CredentialVersion: credVersion,
		RegistrationEpoch: regEpoch,
		ExecutionSeq:      executionSeq,
	}
	m.ApplyRequestScopedErrorResult(execCtx, auth, err, &result)
	m.MarkResult(execCtx, result)
}

func (m *Manager) completeVerifyInferenceSuccess(execCtx context.Context, auth *Auth, provider, stateModel, routeModel string, opts cliproxyexecutor.Options, accessToken string, credVersion, regEpoch, executionSeq uint64) (*Auth, error) {
	m.MarkResult(execCtx, Result{
		AuthID:            auth.ID,
		Provider:          provider,
		Model:             stateModel,
		RouteModel:        routeModel,
		Success:           true,
		CompletedResponse: true,
		Options:           opts,
		AccessToken:       accessToken,
		RefreshToken:      authRefreshToken(auth),
		CredentialVersion: credVersion,
		RegistrationEpoch: regEpoch,
		ExecutionSeq:      executionSeq,
	})
	updated, okUpdated := m.GetByID(auth.ID)
	if !okUpdated || updated == nil {
		return nil, verifyCredentialRemovedError()
	}
	if updated.CredentialVersion != credVersion || updated.RegistrationEpoch != regEpoch || !updated.InferenceVerified {
		return updated, verifyCredentialSupersededError()
	}
	return updated, nil
}

// VerifyInference performs an operator-triggered, account-pinned inference verification
// using an isolated session and minimal non-user prompt/token budget. It never falls back
// to another credential and requires a completed response before marking InferenceVerified.
func (m *Manager) VerifyInference(ctx context.Context, id, model string) (*Auth, error) {
	if m == nil {
		return nil, &Error{Code: "manager_unavailable", Message: "auth manager is not initialized", HTTPStatus: http.StatusServiceUnavailable}
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, &Error{Code: "invalid_request", Message: "auth id is required", HTTPStatus: http.StatusBadRequest}
	}
	auth, ok := m.GetByID(id)
	if !ok || auth == nil {
		return nil, &Error{Code: "auth_not_found", Message: "credential not found", HTTPStatus: http.StatusNotFound}
	}
	if auth.Disabled || auth.Status == StatusDisabled {
		return auth, &Error{Code: "auth_disabled", Message: "credential is disabled", HTTPStatus: http.StatusConflict}
	}
	if auth.RequiresSignIn {
		return auth, &Error{Code: "sign_in_required", Message: "credential requires sign-in before inference verification", HTTPStatus: http.StatusConflict}
	}
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	executorKey := executorKeyFromAuth(auth)
	if executorKey == "" {
		executorKey = provider
	}
	m.mu.RLock()
	executor, okExec := m.executorLocked(executorKey)
	m.mu.RUnlock()
	if !okExec || executor == nil {
		return auth, &Error{Code: "executor_not_found", Message: "executor not registered for provider", HTTPStatus: http.StatusBadRequest}
	}

	routeModel, errModel := resolveVerifyInferenceRouteModel(auth, provider, model)
	if errModel != nil {
		return auth, errModel
	}
	verifyModel := routeModel
	if candidates, _, _, _ := m.executionModelCandidatesWithAlias(auth, routeModel); len(candidates) > 0 && strings.TrimSpace(candidates[0]) != "" {
		verifyModel = strings.TrimSpace(candidates[0])
	}
	stateModel := m.selectionModelKeyForAuth(auth, routeModel)
	if stateModel == "" {
		stateModel = canonicalModelKey(routeModel)
	}

	sessionID := fmt.Sprintf("verify-%s-%d", auth.ID, time.Now().UTC().UnixNano())
	payload := buildVerifyInferencePayload(provider, verifyModel)
	req := cliproxyexecutor.Request{
		Model:   verifyModel,
		Payload: payload,
	}
	opts := cliproxyexecutor.Options{
		Stream:          true,
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		OriginalRequest: payload,
		Headers:         http.Header{"X-Session-Id": {sessionID}},
		Metadata:        map[string]any{cliproxyexecutor.CanonicalSessionIDMetadataKey: sessionID},
	}

	baseCtx := ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	if errCtx := baseCtx.Err(); errCtx != nil {
		return auth, errCtx
	}
	baseCtx = m.WithAuthTransport(baseCtx, auth)
	execCtx := syncMetadataSessionToContext(m.newUpstreamAttemptContext(baseCtx), opts.Metadata)
	executionSeq := m.nextExecutionSeq()

	baseExecutor := executor
	accessToken := authAccessTokenForRequest(execCtx, auth, opts.Headers)
	credVersion := auth.CredentialVersion
	regEpoch := auth.RegistrationEpoch
	didRefreshOnUnauthorized := false

	for {
		preparedAuth, errPrepare := m.prepareRequestAuthAtVersion(execCtx, baseExecutor, auth, regEpoch, credVersion)
		if errPrepare == nil {
			if preparedAuth != nil {
				if preparedAuth.RegistrationEpoch != regEpoch {
					return preparedAuth, verifyCredentialSupersededError()
				}
				auth = preparedAuth
				accessToken = authAccessTokenForRequest(execCtx, auth, opts.Headers)
				credVersion = auth.CredentialVersion
				regEpoch = auth.RegistrationEpoch
			}
			break
		}
		if errors.Is(errPrepare, errVerifyCredentialRemoved) {
			return nil, verifyCredentialRemovedError()
		}
		if errors.Is(errPrepare, errVerifyCredentialSuperseded) {
			latest, _ := m.GetByID(auth.ID)
			return latest, verifyCredentialSupersededError()
		}
		if preparedAuth != nil && preparedAuth.RegistrationEpoch == regEpoch {
			auth = preparedAuth
			accessToken = authAccessTokenForRequest(execCtx, auth, opts.Headers)
			credVersion = auth.CredentialVersion
			regEpoch = auth.RegistrationEpoch
		}
		if errCancel := verifyCancellationError(execCtx, auth, errPrepare); errCancel != nil {
			updated, _ := m.GetByID(auth.ID)
			return updated, errCancel
		}
		if accessToken != "" {
			refreshed, okRefresh, errSuperseded := m.tryRefreshVerifyAuthAfterUnauthorized(newUpstreamAttemptContext(execCtx), auth, errPrepare, didRefreshOnUnauthorized, opts.Headers, credVersion, regEpoch)
			if errSuperseded != nil {
				return refreshed, errSuperseded
			}
			if okRefresh {
				auth = refreshed
				didRefreshOnUnauthorized = true
				execCtx = syncMetadataSessionToContext(m.newUpstreamAttemptContext(baseCtx), opts.Metadata)
				executionSeq = m.nextExecutionSeq()
				accessToken = authAccessTokenForRequest(execCtx, auth, opts.Headers)
				credVersion = auth.CredentialVersion
				regEpoch = auth.RegistrationEpoch
				continue
			}
		}
		if errCancel := verifyCancellationError(execCtx, auth, errPrepare); errCancel != nil {
			updated, _ := m.GetByID(auth.ID)
			return updated, errCancel
		}
		m.recordVerifyFailure(execCtx, auth, provider, stateModel, routeModel, opts, accessToken, credVersion, regEpoch, executionSeq, errPrepare)
		updated, _ := m.GetByID(auth.ID)
		return updated, errPrepare
	}
	executor = executorForAuth(baseExecutor, auth)

	var activeVerifyAuth atomic.Pointer[Auth]
	activeVerifyAuth.Store(auth)
	syncVerifyAuth := func() {
		if currentAuth := activeVerifyAuth.Load(); currentAuth != nil && currentAuth != auth {
			auth = currentAuth
			accessToken = authAccessTokenForRequest(execCtx, auth, opts.Headers)
			credVersion = auth.CredentialVersion
			regEpoch = auth.RegistrationEpoch
		}
	}
	verifyRefreshCtx := func(base context.Context) context.Context {
		return m.withExecutionAuthRefresh(base, executionAuthRefreshOptions{
			expectedEpoch:   regEpoch,
			expectedVersion: &credVersion,
			onAuthUpdated: func(updated *Auth) {
				if updated != nil {
					activeVerifyAuth.Store(updated.Clone())
				}
			},
		})
	}

	for {
		activeVerifyAuth.Store(auth)
		streamRes, errStream := executor.ExecuteStream(verifyRefreshCtx(execCtx), auth, req, opts)
		syncVerifyAuth()
		if errStream != nil {
			if errors.Is(errStream, errVerifyCredentialRemoved) {
				return nil, verifyCredentialRemovedError()
			}
			if errors.Is(errStream, errVerifyCredentialSuperseded) {
				latest, _ := m.GetByID(auth.ID)
				return latest, verifyCredentialSupersededError()
			}
			if errCancel := verifyCancellationError(execCtx, auth, errStream); errCancel != nil {
				updated, _ := m.GetByID(auth.ID)
				return updated, errCancel
			}
			rerr := resultErrorFromError(errStream)
			if rerr != nil && rerr.HTTPStatus == http.StatusNotImplemented {
				opts.Stream = false
				for {
					activeVerifyAuth.Store(auth)
					resp, errExec := executor.Execute(verifyRefreshCtx(execCtx), auth, req, opts)
					syncVerifyAuth()
					if errExec != nil {
						if errors.Is(errExec, errVerifyCredentialRemoved) {
							return nil, verifyCredentialRemovedError()
						}
						if errors.Is(errExec, errVerifyCredentialSuperseded) {
							latest, _ := m.GetByID(auth.ID)
							return latest, verifyCredentialSupersededError()
						}
						if errCancel := verifyCancellationError(execCtx, auth, errExec); errCancel != nil {
							updated, _ := m.GetByID(auth.ID)
							return updated, errCancel
						}
						refreshed, okRefresh, errSuperseded := m.tryRefreshVerifyAuthAfterUnauthorized(newUpstreamAttemptContext(execCtx), auth, errExec, didRefreshOnUnauthorized, opts.Headers, credVersion, regEpoch)
						if errSuperseded != nil {
							return refreshed, errSuperseded
						}
						if okRefresh {
							auth = refreshed
							executor = executorForAuth(baseExecutor, auth)
							didRefreshOnUnauthorized = true
							execCtx = syncMetadataSessionToContext(m.newUpstreamAttemptContext(baseCtx), opts.Metadata)
							executionSeq = m.nextExecutionSeq()
							accessToken = authAccessTokenForRequest(execCtx, auth, opts.Headers)
							credVersion = auth.CredentialVersion
							regEpoch = auth.RegistrationEpoch
							continue
						}
						if errCancel := verifyCancellationError(execCtx, auth, errExec); errCancel != nil {
							updated, _ := m.GetByID(auth.ID)
							return updated, errCancel
						}
						m.recordVerifyFailure(execCtx, auth, provider, stateModel, routeModel, opts, accessToken, credVersion, regEpoch, executionSeq, errExec)
						updated, _ := m.GetByID(auth.ID)
						return updated, errExec
					}
					if errCancel := verifyCancellationError(execCtx, auth, nil); errCancel != nil {
						updated, _ := m.GetByID(auth.ID)
						return updated, errCancel
					}
					if resp.Incomplete || !isCompletedExecutionPayload(resp.Payload) {
						incompleteErr := &Error{Code: "incomplete_response", Message: "verification response did not complete", HTTPStatus: http.StatusBadGateway}
						updated, _ := m.GetByID(auth.ID)
						return updated, incompleteErr
					}
					return m.completeVerifyInferenceSuccess(execCtx, auth, provider, stateModel, routeModel, opts, accessToken, credVersion, regEpoch, executionSeq)
				}
			}
			refreshed, okRefresh, errSuperseded := m.tryRefreshVerifyAuthAfterUnauthorized(newUpstreamAttemptContext(execCtx), auth, errStream, didRefreshOnUnauthorized, opts.Headers, credVersion, regEpoch)
			if errSuperseded != nil {
				return refreshed, errSuperseded
			}
			if okRefresh {
				auth = refreshed
				executor = executorForAuth(baseExecutor, auth)
				didRefreshOnUnauthorized = true
				execCtx = syncMetadataSessionToContext(m.newUpstreamAttemptContext(baseCtx), opts.Metadata)
				executionSeq = m.nextExecutionSeq()
				accessToken = authAccessTokenForRequest(execCtx, auth, opts.Headers)
				credVersion = auth.CredentialVersion
				regEpoch = auth.RegistrationEpoch
				continue
			}
			if errCancel := verifyCancellationError(execCtx, auth, errStream); errCancel != nil {
				updated, _ := m.GetByID(auth.ID)
				return updated, errCancel
			}
			m.recordVerifyFailure(execCtx, auth, provider, stateModel, routeModel, opts, accessToken, credVersion, regEpoch, executionSeq, errStream)
			updated, _ := m.GetByID(auth.ID)
			return updated, errStream
		}

		var sawCompleted bool
		var sawIncomplete bool
		var chunkErr error
		if streamRes != nil && streamRes.Chunks != nil {
		streamDrain:
			for {
				select {
				case <-execCtx.Done():
					discardStreamChunks(streamRes.Chunks)
					updated, _ := m.GetByID(auth.ID)
					return updated, execCtx.Err()
				case chunk, okChunk := <-streamRes.Chunks:
					if !okChunk {
						break streamDrain
					}
					if chunk.Err != nil && chunkErr == nil {
						chunkErr = chunk.Err
					}
					if chunk.Incomplete {
						sawIncomplete = true
					}
					if len(chunk.Payload) > 0 {
						comp, incomp := streamPayloadCompletionFlags(chunk.Payload)
						if comp {
							sawCompleted = true
						}
						if incomp {
							sawIncomplete = true
						}
					}
				}
			}
		}
		if errCancel := verifyCancellationError(execCtx, auth, chunkErr); errCancel != nil {
			updated, _ := m.GetByID(auth.ID)
			return updated, errCancel
		}
		if chunkErr != nil {
			if !sawCompleted {
				refreshed, okRefresh, errSuperseded := m.tryRefreshVerifyAuthAfterUnauthorized(newUpstreamAttemptContext(execCtx), auth, chunkErr, didRefreshOnUnauthorized, opts.Headers, credVersion, regEpoch)
				if errSuperseded != nil {
					return refreshed, errSuperseded
				}
				if okRefresh {
					auth = refreshed
					executor = executorForAuth(baseExecutor, auth)
					didRefreshOnUnauthorized = true
					execCtx = syncMetadataSessionToContext(m.newUpstreamAttemptContext(baseCtx), opts.Metadata)
					executionSeq = m.nextExecutionSeq()
					accessToken = authAccessTokenForRequest(execCtx, auth, opts.Headers)
					credVersion = auth.CredentialVersion
					regEpoch = auth.RegistrationEpoch
					continue
				}
				if errCancel := verifyCancellationError(execCtx, auth, chunkErr); errCancel != nil {
					updated, _ := m.GetByID(auth.ID)
					return updated, errCancel
				}
			}
			m.recordVerifyFailure(execCtx, auth, provider, stateModel, routeModel, opts, accessToken, credVersion, regEpoch, executionSeq, chunkErr)
			updated, _ := m.GetByID(auth.ID)
			return updated, chunkErr
		}
		if !sawCompleted || sawIncomplete {
			incompleteErr := &Error{Code: "incomplete_response", Message: "verification stream ended without a completed response", HTTPStatus: http.StatusBadGateway}
			updated, _ := m.GetByID(auth.ID)
			return updated, incompleteErr
		}
		return m.completeVerifyInferenceSuccess(execCtx, auth, provider, stateModel, routeModel, opts, accessToken, credVersion, regEpoch, executionSeq)
	}
}
