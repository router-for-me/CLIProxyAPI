package auth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	cliproxysession "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/session"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
)

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

type upstreamExecutionAttemptError struct {
	cause error
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

func markUpstreamExecutionAttempt(err error) error {
	if err == nil {
		return nil
	}
	if hasUpstreamExecutionAttempt(err) {
		return err
	}
	return &upstreamExecutionAttemptError{cause: err}
}

func markUpstreamExecutionAttemptFromContext(ctx context.Context, err error) error {
	if err == nil || !cliproxyexecutor.UpstreamAttempted(ctx) {
		return err
	}
	return markUpstreamExecutionAttempt(err)
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
	err = unwrapRequestStopError(err)
	return unwrapUpstreamExecutionAttempt(err)
}

func preferredExecutionAttemptError(fallback, upstream error) error {
	if errors.Is(fallback, context.Canceled) || errors.Is(fallback, context.DeadlineExceeded) {
		return fallback
	}
	if upstream == nil {
		return fallback
	}
	var exhausted *homeRetryRoundExhaustedError
	if errors.As(fallback, &exhausted) && exhausted != nil {
		upstream = unwrapUpstreamExecutionAttempt(upstream)
		var previousRound *homeRetryRoundExhaustedError
		if errors.As(upstream, &previousRound) && previousRound != nil && previousRound.cause != nil {
			upstream = previousRound.cause
		}
		// Keep the current Home round marker because it owns authoritative retry timing.
		preferred := *exhausted
		preferred.cause = upstream
		return markUpstreamExecutionAttempt(&preferred)
	}
	return markUpstreamExecutionAttempt(upstream)
}

// Execute performs a non-streaming execution using the configured selector and executor.
// It supports multiple providers for the same model and round-robins the starting provider per model.
func (m *Manager) Execute(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	req, opts = cliproxysession.Enrich(req, opts)
	normalized := m.normalizeProviders(providers)
	if len(normalized) == 0 {
		return cliproxyexecutor.Response{}, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}
	if m.HomeEnabled() {
		resp, errHome := m.executeHome(ctx, normalized, req, opts, false)
		return resp, unwrapExecutionBoundaryError(errHome)
	}

	defaultRequestRetry, maxRetryCredentials, maxWait := m.retrySettings()

	var lastErr error
	var preferredUpstreamErr error
	retryModel := authSelectionModelFromOptions(opts, req.Model)
	for attempt := 0; ; attempt++ {
		resp, errExec := m.executeMixedOnce(ctx, normalized, req, opts, maxRetryCredentials, attempt, defaultRequestRetry)
		if errExec == nil {
			return resp, nil
		}
		if isRequestTerminatedError(errExec) || isRequestStopError(errExec) {
			return cliproxyexecutor.Response{}, unwrapExecutionBoundaryError(errExec)
		}
		if hasUpstreamExecutionAttempt(errExec) {
			preferredUpstreamErr = errExec
		}
		lastErr = errExec
		wait, shouldRetry := m.shouldRetryAfterErrorWithHomeRetryLimit(ctx, opts, errExec, attempt, normalized, retryModel, maxWait, -1, defaultRequestRetry)
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
		if hasAntigravityProvider(normalized) && shouldAttemptAntigravityCreditsFallback(m, lastErr, normalized) {
			if resp, ok, errCredits := m.tryAntigravityCreditsExecute(ctx, req, opts); errCredits != nil {
				return cliproxyexecutor.Response{}, errCredits
			} else if ok {
				return resp, nil
			}
		}
		return cliproxyexecutor.Response{}, lastErr
	}
	return cliproxyexecutor.Response{}, &Error{Code: "auth_not_found", Message: "no auth available"}
}

// It supports multiple providers for the same model and round-robins the starting provider per model.
func (m *Manager) ExecuteCount(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	req, opts = cliproxysession.Enrich(req, opts)
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
	retryModel := authSelectionModelFromOptions(opts, req.Model)
	for attempt := 0; ; attempt++ {
		resp, errExec := m.executeCountMixedOnce(ctx, normalized, req, opts, maxRetryCredentials, attempt, defaultRequestRetry)
		if errExec == nil {
			return resp, nil
		}
		if isRequestTerminatedError(errExec) || isRequestStopError(errExec) {
			return cliproxyexecutor.Response{}, unwrapExecutionBoundaryError(errExec)
		}
		if hasUpstreamExecutionAttempt(errExec) {
			preferredUpstreamErr = errExec
		}
		lastErr = errExec
		wait, shouldRetry := m.shouldRetryAfterErrorWithHomeRetryLimit(ctx, opts, errExec, attempt, normalized, retryModel, maxWait, -1, defaultRequestRetry)
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
		return cliproxyexecutor.Response{}, unwrapExecutionBoundaryError(lastErr)
	}
	return cliproxyexecutor.Response{}, &Error{Code: "auth_not_found", Message: "no auth available"}
}

// ExecuteStream performs a streaming execution using the configured selector and executor.
// It supports multiple providers for the same model and round-robins the starting provider per model.
func (m *Manager) ExecuteStream(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	req, opts = cliproxysession.Enrich(req, opts)
	if m.HomeEnabled() {
		if unlockSession := m.lockHomeWebsocketSession(ctx, opts); unlockSession != nil {
			defer unlockSession()
		}
	}
	normalized := m.normalizeProviders(providers)
	if len(normalized) == 0 {
		return nil, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}

	defaultRequestRetry, maxRetryCredentials, maxWait := m.retrySettings()

	var lastErr error
	var preferredUpstreamErr error
	homeRetryLimit := -1
	retryModel := authSelectionModelFromOptions(opts, req.Model)
	attempt := 0
	retryRoundPending := false
	retryRoundWaited := false
	for {
		result, errStream := m.executeStreamMixedOnce(ctx, normalized, req, opts, maxRetryCredentials, &homeRetryLimit, attempt, defaultRequestRetry)
		if errStream == nil {
			return result, nil
		}
		if hasUpstreamExecutionAttempt(errStream) {
			preferredUpstreamErr = errStream
		}
		if m.HomeEnabled() && retryRoundPending {
			if wait, okWait := pendingHomeRetryRoundDelay(errStream, maxWait, &homeRetryLimit, pinnedAuthIDFromMetadata(opts.Metadata) == ""); okWait && m.homeRetryAllowed(attempt-1, homeRetryLimit) {
				if retryRoundWaited {
					return nil, unwrapExecutionBoundaryError(errStream)
				}
				if errWait := waitForCooldown(ctx, wait, maxWait); errWait != nil {
					return nil, errWait
				}
				retryRoundWaited = true
				continue
			}
		}
		retryRoundPending = false
		retryRoundWaited = false
		if isRequestTerminatedError(errStream) || isRequestStopError(errStream) {
			return nil, unwrapExecutionBoundaryError(errStream)
		}
		lastErr = errStream
		wait, shouldRetry := m.shouldRetryAfterErrorWithHomeRetryLimit(ctx, opts, errStream, attempt, normalized, retryModel, maxWait, homeRetryLimit, defaultRequestRetry)
		if !shouldRetry {
			break
		}
		if errWait := waitForCooldown(ctx, wait, maxWait); errWait != nil {
			return nil, errWait
		}
		attempt++
		retryRoundPending = m.HomeEnabled()
		retryRoundWaited = false
	}
	if lastErr != nil {
		if ctx != nil {
			if errCtx := ctx.Err(); errCtx != nil {
				return nil, errCtx
			}
		}
		if preferredUpstreamErr != nil && (!m.HomeEnabled() || isHomeRetryRoundExhausted(lastErr)) {
			lastErr = preferredExecutionAttemptError(lastErr, preferredUpstreamErr)
		}
		lastErr = unwrapExecutionBoundaryError(lastErr)
		if hasAntigravityProvider(normalized) && shouldAttemptAntigravityCreditsFallback(m, lastErr, normalized) {
			if result, ok, errCredits := m.tryAntigravityCreditsExecuteStream(ctx, req, opts); errCredits != nil {
				return nil, errCredits
			} else if ok {
				return result, nil
			}
		}
		var bootstrapErr *streamBootstrapError
		if errors.As(lastErr, &bootstrapErr) && bootstrapErr != nil {
			return streamErrorResult(bootstrapErr.Headers(), lastErr), nil
		}
		return nil, lastErr
	}
	return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
}

type requestToFormatResolver interface {
	RequestToFormat(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) sdktranslator.Format
}

func isRequestTerminatedError(err error) bool {
	var terminated *cliproxyexecutor.RequestTerminatedError
	return errors.As(err, &terminated) && terminated != nil
}

func applyRequestAfterAuthInterceptor(ctx context.Context, executor ProviderExecutor, provider string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, requestedModel string) (cliproxyexecutor.Request, cliproxyexecutor.Options, error) {
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
		Body:           bytes.Clone(req.Payload),
		Metadata:       opts.Metadata,
	})
	opts.Headers = mergeRequestHeaders(opts.Headers, resp.Headers, resp.ClearHeaders)
	if len(resp.Body) > 0 {
		req.Payload = bytes.Clone(resp.Body)
		opts.OriginalRequest = bytes.Clone(resp.Body)
	}
	if resp.Terminate {
		return req, opts, &cliproxyexecutor.RequestTerminatedError{
			HTTPStatus: resp.StatusCode,
			Header:     cloneRequestHeaders(resp.ResponseHeaders),
			Body:       bytes.Clone(resp.ResponseBody),
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
	if source == "openai-image" || source == "openai-video" {
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
	case "kimi":
		return sdktranslator.FormatOpenAI
	case "antigravity":
		return sdktranslator.FormatAntigravity
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
		out.Del(key)
	}
	for key, values := range updates {
		out.Del(key)
		for _, value := range values {
			out.Add(key, value)
		}
	}
	return out
}

func (m *Manager) executeMixedOnce(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, maxRetryCredentials int, retryRound int, defaultRequestRetry int) (cliproxyexecutor.Response, error) {
	if len(providers) == 0 {
		return cliproxyexecutor.Response{}, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}
	routeModel := authSelectionModelFromOptions(opts, req.Model)
	executionModel, restoreExecutionModel := executionModelForAuthSelection(opts, req.Model)
	opts = ensureRequestedModelMetadata(opts, routeModel)
	homeMode := m.HomeEnabled()
	homeAuthCount := 1
	tried := make(map[string]struct{})
	if !homeMode {
		for authID := range m.requestRetryRoundExclusions(retryRound, defaultRequestRetry) {
			tried[authID] = struct{}{}
		}
	}
	attempted := make(map[string]struct{})
	var lastErr error
	var upstreamErr error
	for {
		if maxRetryCredentials > 0 && len(attempted) >= maxRetryCredentials {
			if lastErr != nil {
				return cliproxyexecutor.Response{}, preferredExecutionAttemptError(lastErr, upstreamErr)
			}
			return cliproxyexecutor.Response{}, &Error{Code: "auth_not_found", Message: "no auth available"}
		}
		pickOpts := opts
		if homeMode {
			pickOpts = withHomeRetryRound(pickOpts, retryRound)
			pickOpts = withHomeAuthCount(pickOpts, homeAuthCount)
			pickOpts = withHomeExcludedAuthIDs(pickOpts, tried)
		}
		auth, executor, provider, errPick := m.pickNextMixed(ctx, providers, routeModel, pickOpts, tried)
		if errPick != nil {
			if shouldReturnLastErrorOnPickFailure(homeMode, lastErr, errPick) {
				return cliproxyexecutor.Response{}, preferredExecutionAttemptError(lastErr, upstreamErr)
			}
			return cliproxyexecutor.Response{}, errPick
		}

		entry := logEntryWithRequestID(ctx)
		debugLogAuthSelection(entry, auth, provider, routeModel)
		publishSelectedAuthMetadata(opts.Metadata, auth)

		tried[auth.ID] = struct{}{}
		execCtx := ctx
		if rt := m.roundTripperFor(auth); rt != nil {
			execCtx = context.WithValue(execCtx, roundTripperContextKey{}, rt)
			execCtx = context.WithValue(execCtx, "cliproxy.roundtripper", rt)
		}
		execCtx = contextWithRequestedModelAlias(execCtx, opts, routeModel)
		execCtx = newUpstreamAttemptContext(execCtx)

		models, pooled, aliasResult, routing := m.preparedExecutionModelsWithAlias(auth, routeModel)
		if len(models) == 0 {
			continue
		}
		attempted[auth.ID] = struct{}{}
		var errPrepare error
		auth, errPrepare = m.prepareRequestAuth(execCtx, executor, auth)
		if errPrepare != nil {
			if errCancel := claudeOAuthRequestCancellation(execCtx, auth, errPrepare); errCancel != nil {
				return cliproxyexecutor.Response{}, errCancel
			}
			stateModel := m.selectionModelKeyForAuth(auth, routeModel)
			if stateModel == "" {
				stateModel = canonicalModelKey(routeModel)
			}
			result := Result{AuthID: auth.ID, Provider: provider, Model: stateModel, RouteModel: routeModel, Success: false, Error: resultErrorFromError(errPrepare), Options: pickOpts}
			m.MarkResult(execCtx, result)
			lastErr = errPrepare
			continue
		}
		var authErr error
		didRefreshOnUnauthorized := false
		for _, upstreamModel := range models {
			execCtx = newUpstreamAttemptContext(execCtx)
			resultModel := m.stateModelForExecution(auth, routeModel, upstreamModel, pooled)
			execReq := req
			execReq.Model = upstreamModel
			if restoreExecutionModel {
				execReq.Model = executionModel
			}
			execOpts := opts
			var errIntercept error
			execReq, execOpts, errIntercept = applyRequestAfterAuthInterceptor(execCtx, executor, provider, execReq, execOpts, requestedModelAliasFromOptions(execOpts, routeModel))
			if errIntercept != nil {
				return cliproxyexecutor.Response{}, errIntercept
			}
			if !restoreExecutionModel {
				execReq = attachResolvedAPIKeyModelInfo(routing, execReq, auth, routeModel, upstreamModel)
			}
			startExec := time.Now()
			resp, errExec := executor.Execute(execCtx, auth, execReq, execOpts)
			errExec = markUpstreamExecutionAttemptFromContext(execCtx, errExec)
			durationExec := time.Since(startExec)
			if errExec != nil {
				if hasUpstreamExecutionAttempt(errExec) {
					upstreamErr = errExec
				}
				if errCtx := execCtx.Err(); errCtx != nil {
					return cliproxyexecutor.Response{}, errCtx
				}
				refreshCtx := newUpstreamAttemptContext(execCtx)
				if refreshed, okRefresh := m.tryRefreshAfterUnauthorized(refreshCtx, auth, errExec, didRefreshOnUnauthorized); okRefresh {
					auth = refreshed
					didRefreshOnUnauthorized = true
					execCtx = newUpstreamAttemptContext(execCtx)
					startRetry := time.Now()
					resp, errExec = executor.Execute(execCtx, auth, execReq, execOpts)
					errExec = markUpstreamExecutionAttemptFromContext(execCtx, errExec)
					durationRetry := time.Since(startRetry)
					if errExec != nil {
						if hasUpstreamExecutionAttempt(errExec) {
							upstreamErr = errExec
						}
						warnLogUpstreamFailure(execCtx, entry, provider, upstreamModel, auth, durationRetry, errExec)
						if errCtx := execCtx.Err(); errCtx != nil {
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
			result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, RouteModel: routeModel, Success: errExec == nil, Options: execOpts}
			if errExec != nil {
				result.Error = resultErrorFromError(errExec)
				if ra := retryAfterFromError(errExec); ra != nil {
					result.RetryAfter = ra
				}
				if isCredentialScopedError(errExec) {
					result.CredentialScope = true
				}
				action, okAction := matchRequestScopedErrorAction(auth, errExec, m.runtimeConfigSnapshot())
				applyRequestScopedActionToResult(action, okAction, &result)
				if isResponsesCompactAvailabilityNeutralError(execOpts, errExec, result.Error) {
					m.recordAvailabilityNeutralResult(execCtx, result)
				} else {
					m.MarkResult(execCtx, result)
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
			action, okAction := matchRequestScopedErrorAction(auth, authErr, m.runtimeConfigSnapshot())
			if okAction {
				if isRequestScopedStop(action, okAction) {
					return cliproxyexecutor.Response{}, wrapRequestStopError(authErr)
				}
				lastErr = authErr
				if homeMode {
					homeAuthCount++
				}
				continue
			}
			if isResponsesCompactRequestFaultError(opts, authErr) || isRequestInvalidError(authErr) {
				return cliproxyexecutor.Response{}, authErr
			}
			lastErr = authErr
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
	homeMode := m.HomeEnabled()
	homeAuthCount := 1
	tried := make(map[string]struct{})
	if !homeMode {
		for authID := range m.requestRetryRoundExclusions(retryRound, defaultRequestRetry) {
			tried[authID] = struct{}{}
		}
	}
	attempted := make(map[string]struct{})
	var lastErr error
	var upstreamErr error
	for {
		if maxRetryCredentials > 0 && len(attempted) >= maxRetryCredentials {
			if lastErr != nil {
				return cliproxyexecutor.Response{}, preferredExecutionAttemptError(lastErr, upstreamErr)
			}
			return cliproxyexecutor.Response{}, &Error{Code: "auth_not_found", Message: "no auth available"}
		}
		pickOpts := opts
		if homeMode {
			pickOpts = withHomeRetryRound(pickOpts, retryRound)
			pickOpts = withHomeAuthCount(pickOpts, homeAuthCount)
			pickOpts = withHomeExcludedAuthIDs(pickOpts, tried)
		}
		auth, executor, provider, errPick := m.pickNextMixed(ctx, providers, routeModel, pickOpts, tried)
		if errPick != nil {
			if shouldReturnLastErrorOnPickFailure(homeMode, lastErr, errPick) {
				return cliproxyexecutor.Response{}, preferredExecutionAttemptError(lastErr, upstreamErr)
			}
			return cliproxyexecutor.Response{}, errPick
		}

		entry := logEntryWithRequestID(ctx)
		debugLogAuthSelection(entry, auth, provider, routeModel)
		publishSelectedAuthMetadata(opts.Metadata, auth)

		tried[auth.ID] = struct{}{}
		execCtx := ctx
		if rt := m.roundTripperFor(auth); rt != nil {
			execCtx = context.WithValue(execCtx, roundTripperContextKey{}, rt)
			execCtx = context.WithValue(execCtx, "cliproxy.roundtripper", rt)
		}
		execCtx = contextWithRequestedModelAlias(execCtx, opts, routeModel)
		execCtx = newUpstreamAttemptContext(execCtx)

		models, pooled, aliasResult, routing := m.preparedExecutionModelsWithAlias(auth, routeModel)
		if len(models) == 0 {
			continue
		}
		attempted[auth.ID] = struct{}{}
		var errPrepare error
		auth, errPrepare = m.prepareRequestAuth(execCtx, executor, auth)
		if errPrepare != nil {
			if errCancel := claudeOAuthRequestCancellation(execCtx, auth, errPrepare); errCancel != nil {
				return cliproxyexecutor.Response{}, errCancel
			}
			stateModel := m.selectionModelKeyForAuth(auth, routeModel)
			if stateModel == "" {
				stateModel = canonicalModelKey(routeModel)
			}
			result := Result{AuthID: auth.ID, Provider: provider, Model: stateModel, RouteModel: routeModel, Success: false, Error: resultErrorFromError(errPrepare), Options: pickOpts, SkipQuotaObservation: true}
			m.MarkResult(execCtx, result)
			lastErr = errPrepare
			continue
		}
		var authErr error
		didRefreshOnUnauthorized := false
		for _, upstreamModel := range models {
			execCtx = newUpstreamAttemptContext(execCtx)
			resultModel := m.stateModelForExecution(auth, routeModel, upstreamModel, pooled)
			execReq := req
			execReq.Model = upstreamModel
			if restoreExecutionModel {
				execReq.Model = executionModel
			}
			execOpts := opts
			var errIntercept error
			execReq, execOpts, errIntercept = applyRequestAfterAuthInterceptor(execCtx, executor, provider, execReq, execOpts, requestedModelAliasFromOptions(execOpts, routeModel))
			if errIntercept != nil {
				return cliproxyexecutor.Response{}, errIntercept
			}
			if !restoreExecutionModel {
				execReq = attachResolvedAPIKeyModelInfo(routing, execReq, auth, routeModel, upstreamModel)
			}
			startExec := time.Now()
			resp, errExec := executor.CountTokens(execCtx, auth, execReq, execOpts)
			errExec = markUpstreamExecutionAttemptFromContext(execCtx, errExec)
			durationExec := time.Since(startExec)
			if errExec != nil {
				if hasUpstreamExecutionAttempt(errExec) {
					upstreamErr = errExec
				}
				if errCtx := execCtx.Err(); errCtx != nil {
					return cliproxyexecutor.Response{}, errCtx
				}
				refreshCtx := newUpstreamAttemptContext(execCtx)
				if refreshed, okRefresh := m.tryRefreshAfterUnauthorized(refreshCtx, auth, errExec, didRefreshOnUnauthorized); okRefresh {
					auth = refreshed
					didRefreshOnUnauthorized = true
					execCtx = newUpstreamAttemptContext(execCtx)
					startRetry := time.Now()
					resp, errExec = executor.CountTokens(execCtx, auth, execReq, execOpts)
					errExec = markUpstreamExecutionAttemptFromContext(execCtx, errExec)
					durationRetry := time.Since(startRetry)
					if errExec != nil {
						if hasUpstreamExecutionAttempt(errExec) {
							upstreamErr = errExec
						}
						warnLogUpstreamFailure(execCtx, entry, provider, upstreamModel, auth, durationRetry, errExec)
						if errCtx := execCtx.Err(); errCtx != nil {
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
			result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, RouteModel: routeModel, Success: errExec == nil, Options: execOpts, SkipQuotaObservation: true}
			if errExec != nil {
				result.Error = resultErrorFromError(errExec)
				if ra := retryAfterFromError(errExec); ra != nil {
					result.RetryAfter = ra
				}
				action, okAction := matchRequestScopedErrorAction(auth, errExec, m.runtimeConfigSnapshot())
				applyRequestScopedActionToResult(action, okAction, &result)
				// Some Anthropic-compatible upstreams do not implement the
				// count_tokens route and return a generic endpoint 404. Record
				// the failure for hooks and metrics without suspending a model
				// that remains usable through the messages endpoint.
				if isCountTokensEndpointNotFoundError(errExec, execReq.Model) && (result.Error == nil || result.Error.Code != ErrorCodeForceCooldown) {
					m.recordAvailabilityNeutralResult(execCtx, result)
				} else {
					if isCredentialScopedError(errExec) {
						result.CredentialScope = true
					}
					m.MarkResult(execCtx, result)
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
			action, okAction := matchRequestScopedErrorAction(auth, authErr, m.runtimeConfigSnapshot())
			if okAction {
				if isRequestScopedStop(action, okAction) {
					return cliproxyexecutor.Response{}, wrapRequestStopError(authErr)
				}
				lastErr = authErr
				if homeMode {
					homeAuthCount++
				}
				continue
			}
			if isRequestInvalidError(authErr) {
				return cliproxyexecutor.Response{}, authErr
			}
			lastErr = authErr
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
	homeMode := m.HomeEnabled()
	homeAuthCount := 1
	tried := make(map[string]struct{})
	if !homeMode {
		for authID := range m.requestRetryRoundExclusions(retryRound, defaultRequestRetry) {
			tried[authID] = struct{}{}
		}
	}
	homeExcludedAuthIDs := make(map[string]struct{})
	homeSameAuthRetries := make(map[string]int)
	lastHomeAuthID := ""
	homeSameAuthRetryPending := false
	attempted := make(map[string]struct{})
	var lastErr error
	var upstreamErr error
	var roundTiming homeRetryRoundTiming
	for {
		allowSameAuthRetry := homeMode && homeSameAuthRetryPending && lastHomeAuthID != "" && homeSameAuthRetries[lastHomeAuthID] == 0
		if maxRetryCredentials > 0 && len(attempted) >= maxRetryCredentials && !allowSameAuthRetry {
			if lastErr != nil {
				preferredErr := preferredExecutionAttemptError(lastErr, upstreamErr)
				if homeMode {
					return nil, markHomeRetryRoundExhausted(preferredErr, roundTiming.RetryAfter(), true)
				}
				return nil, preferredErr
			}
			return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
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
		}
		if errPick != nil {
			preferredErr := preferredExecutionAttemptError(lastErr, upstreamErr)
			var homeCooldown *homeDispatchRetryAfterError
			if homeMode && lastErr != nil && errors.As(errPick, &homeCooldown) && homeCooldown != nil {
				observeHomeCooldownRetryLimit(homeCooldown, homeRetryLimit, pinnedAuthIDFromMetadata(opts.Metadata) == "")
				return nil, markHomeRetryRoundExhausted(preferredErr, homeCooldown.RetryAfter(), false)
			}
			if shouldReturnLastErrorOnPickFailure(homeMode, lastErr, errPick) {
				if homeMode {
					return nil, markHomeRetryRoundExhausted(preferredErr, roundTiming.RetryAfter(), isHomeNextRoundImmediatelyAvailable(errPick))
				}
				return nil, preferredErr
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
				return nil, markHomeRetryRoundExhausted(preferredExecutionAttemptError(lastErr, upstreamErr), roundTiming.RetryAfter(), true)
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
						return nil, markHomeRetryRoundExhausted(preferredExecutionAttemptError(lastErr, upstreamErr), roundTiming.RetryAfter(), false)
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
		publishSelectedAuthMetadata(opts.Metadata, auth)

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
		if rt := m.roundTripperFor(auth); rt != nil {
			execCtx = context.WithValue(execCtx, roundTripperContextKey{}, rt)
			execCtx = context.WithValue(execCtx, "cliproxy.roundtripper", rt)
		}
		// Enrich before auth preparation so prepare-stage usage records observe the client request.
		execCtx = contextWithRequestedModelAlias(execCtx, opts, routeModel)
		execCtx = newUpstreamAttemptContext(execCtx)
		models, pooled, aliasResult, routing := m.preparedExecutionModelsWithAlias(auth, routeModel)
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
		var errPrepare error
		if selection != nil {
			auth, errPrepare = m.prepareHomeRequestAuth(execCtx, executor, selection)
		} else {
			auth, errPrepare = m.prepareRequestAuth(execCtx, executor, auth)
		}
		if errPrepare != nil {
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
			if stateModel == "" {
				stateModel = canonicalModelKey(routeModel)
			}
			result := Result{AuthID: auth.ID, Provider: provider, Model: stateModel, RouteModel: routeModel, Success: false, Error: resultErrorFromError(errPrepare), Options: pickOpts}
			if selection != nil {
				m.reportHomeResult(execCtx, result, auth)
				releaseAttempt()
			} else {
				m.MarkResult(execCtx, result)
			}
			lastErr = errPrepare
			if homeMode {
				roundTiming.Observe(lastErr)
			}
			if selection != nil {
				if errEnd := m.endHomeSelectionBeforeRedispatch(ctx, selection, "prepare_failed"); errEnd != nil {
					return nil, errEnd
				}
			}
			continue
		}
		execReq := sanitizeDownstreamWebsocketFallbackRequest(execCtx, auth, req)
		streamExecutionModel := ""
		if restoreExecutionModel {
			streamExecutionModel = executionModel
		}
		execOpts := opts
		if selection != nil {
			execOpts.ExecutionLifecycle = selection
		}
		if homeMode && len(models) > 1 {
			models = models[:1]
			pooled = false
		}
		streamResult, errStream := m.executeStreamWithModelPool(execCtx, executor, auth, provider, execReq, execOpts, routeModel, streamExecutionModel, models, pooled, aliasResult, routing, !homeMode || selection != nil, selection != nil)
		if errStream != nil {
			if hasUpstreamExecutionAttempt(errStream) {
				upstreamErr = errStream
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
			action, okAction := matchRequestScopedErrorAction(auth, errStream, m.runtimeConfigSnapshot())
			if okAction {
				if isRequestScopedStop(action, okAction) {
					return nil, wrapRequestStopError(errStream)
				}
				lastErr = errStream
				if homeMode {
					roundTiming.Observe(lastErr)
				}
				if homeMode {
					homeAuthCount++
				}
				continue
			}
			if isRequestInvalidError(errStream) {
				return nil, errStream
			}
			lastErr = errStream
			if homeMode {
				roundTiming.Observe(lastErr)
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

func cloneRequestMetadata(src map[string]any) map[string]any {
	if len(src) == 0 {
		return make(map[string]any, 4)
	}
	dst := make(map[string]any, len(src)+4)
	for k, v := range src {
		dst[k] = v
	}
	return dst
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

type requestAuthPrepareLock struct {
	mu sync.Mutex
}

// prepareHomeRequestAuth prepares a dispatch auth without reading or updating local auth state.
func (m *Manager) prepareHomeRequestAuth(ctx context.Context, executor ProviderExecutor, selection *HomeDispatchSelection) (*Auth, error) {
	if selection == nil {
		return nil, nil
	}
	auth := selection.CloneAuth()
	prepared, errPrepare := m.prepareHomeAuthSnapshot(ctx, executor, auth)
	if errPrepare != nil {
		warnLogHomeCredentialFailure(ctx, "request_auth_preparation", selection.Provider, auth, errPrepare)
	}
	return prepared, errPrepare
}

func (m *Manager) prepareHomeAuthSnapshot(ctx context.Context, executor ProviderExecutor, auth *Auth) (*Auth, error) {
	if m == nil || executor == nil || auth == nil {
		return auth, nil
	}
	preparer, ok := executor.(RequestAuthPreparer)
	if !ok || preparer == nil || !preparer.ShouldPrepareRequestAuth(auth) {
		return auth, nil
	}

	prepare := func() (*Auth, error) {
		target := auth.Clone()
		if !preparer.ShouldPrepareRequestAuth(target) {
			return target, nil
		}
		updated, errPrepare := preparer.PrepareRequestAuth(ctx, target)
		if errPrepare != nil {
			return auth, errPrepare
		}
		if updated == nil {
			return target, nil
		}
		return updated, nil
	}

	id := strings.TrimSpace(auth.ID)
	if id == "" {
		return prepare()
	}
	lockValue, _ := m.requestPrepareLocks.LoadOrStore(id, &requestAuthPrepareLock{})
	lock, ok := lockValue.(*requestAuthPrepareLock)
	if !ok || lock == nil {
		return prepare()
	}
	lock.mu.Lock()
	defer lock.mu.Unlock()
	return prepare()
}

func (m *Manager) prepareRequestAuth(ctx context.Context, executor ProviderExecutor, auth *Auth) (*Auth, error) {
	if m == nil || executor == nil || auth == nil {
		return auth, nil
	}
	preparer, ok := executor.(RequestAuthPreparer)
	if !ok || preparer == nil || !preparer.ShouldPrepareRequestAuth(auth) {
		return auth, nil
	}

	id := strings.TrimSpace(auth.ID)
	if id == "" {
		return preparer.PrepareRequestAuth(ctx, auth.Clone())
	}

	lockValue, _ := m.requestPrepareLocks.LoadOrStore(id, &requestAuthPrepareLock{})
	lock, ok := lockValue.(*requestAuthPrepareLock)
	if !ok || lock == nil {
		return preparer.PrepareRequestAuth(ctx, auth.Clone())
	}

	lock.mu.Lock()
	defer lock.mu.Unlock()

	target := auth.Clone()
	m.mu.RLock()
	if current := m.auths[id]; current != nil {
		target = current.Clone()
	}
	m.mu.RUnlock()

	if !preparer.ShouldPrepareRequestAuth(target) {
		return target, nil
	}

	updated, errPrepare := preparer.PrepareRequestAuth(ctx, target)
	if errPrepare != nil {
		return auth, errPrepare
	}
	if updated == nil {
		return target, nil
	}

	saved, errUpdate := m.Update(ctx, updated)
	if errUpdate != nil {
		return updated, errUpdate
	}
	if saved != nil {
		return saved, nil
	}
	return updated, nil
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
	return ctx
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

func (m *Manager) executorFor(provider string) ProviderExecutor {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.executors[provider]
}

// roundTripperContextKey is an unexported context key type to avoid collisions.
type roundTripperContextKey struct{}

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
	return strings.ToLower(strings.TrimSpace(auth.Provider))
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
	var exec ProviderExecutor
	if a != nil {
		exec = m.executors[executorKeyFromAuth(a)]
	}
	m.mu.RUnlock()
	if a == nil || exec == nil {
		return nil
	}
	if p, ok := exec.(RequestPreparer); ok && p != nil {
		return p.PrepareRequest(req, a)
	}
	return nil
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
	return preparer.PrepareRequest(req, auth)
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
	return exec.HttpRequest(ctx, auth, req)
}
