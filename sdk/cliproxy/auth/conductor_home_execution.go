package auth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/sjson"
)

func (m *Manager) executeHome(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, countTokens bool) (cliproxyexecutor.Response, error) {
	if unlockSession := m.lockHomeWebsocketSession(ctx, opts); unlockSession != nil {
		defer unlockSession()
	}
	defaultRequestRetry, maxRetryCredentials, maxWait := m.retrySettings()
	retryModel := authSelectionModelFromOptions(opts, req.Model)
	homeRetryLimit := -1
	attempt := 0
	retryRoundPending := false
	retryRoundWaited := false
	var preferredUpstreamErr error
	for {
		response, errExecute := m.executeHomeOnce(ctx, providers, req, opts, countTokens, maxRetryCredentials, &homeRetryLimit, attempt)
		if errExecute == nil {
			return response, nil
		}
		if hasUpstreamExecutionAttempt(errExecute) {
			preferredUpstreamErr = errExecute
		}
		if retryRoundPending {
			if wait, okWait := pendingHomeRetryRoundDelay(errExecute, maxWait, &homeRetryLimit, pinnedAuthIDFromMetadata(opts.Metadata) == ""); okWait && m.homeRetryAllowed(attempt-1, homeRetryLimit) {
				if retryRoundWaited {
					return cliproxyexecutor.Response{}, errExecute
				}
				if errWait := waitForCooldown(ctx, wait, maxWait); errWait != nil {
					return cliproxyexecutor.Response{}, errWait
				}
				retryRoundWaited = true
				continue
			}
		}
		retryRoundPending = false
		retryRoundWaited = false
		if isRequestTerminatedError(errExecute) || isRequestStopError(errExecute) {
			return cliproxyexecutor.Response{}, unwrapExecutionBoundaryError(errExecute)
		}
		wait, shouldRetry := m.shouldRetryAfterErrorWithHomeRetryLimit(ctx, opts, errExecute, attempt, providers, retryModel, maxWait, homeRetryLimit, defaultRequestRetry)
		if !shouldRetry {
			if preferredUpstreamErr != nil && isHomeRetryRoundExhausted(errExecute) {
				errExecute = preferredExecutionAttemptError(errExecute, preferredUpstreamErr)
			}
			return cliproxyexecutor.Response{}, unwrapExecutionBoundaryError(errExecute)
		}
		if errWait := waitForCooldown(ctx, wait, maxWait); errWait != nil {
			return cliproxyexecutor.Response{}, errWait
		}
		attempt++
		retryRoundPending = true
		retryRoundWaited = false
	}
}

func (m *Manager) executeHomeOnce(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, countTokens bool, maxRetryCredentials int, homeRetryLimit *int, retryRounds ...int) (cliproxyexecutor.Response, error) {
	retryRound := 0
	if len(retryRounds) > 0 {
		retryRound = retryRounds[0]
	}
	routeModel := authSelectionModelFromOptions(opts, req.Model)
	responseAlias := requestedModelAliasFromOptions(opts, routeModel)
	executionModel, restoreExecutionModel := executionModelForAuthSelection(opts, req.Model)
	opts = ensureRequestedModelMetadata(opts, routeModel)
	tried := make(map[string]struct{})
	attempted := make(map[string]struct{})
	var lastErr error
	var upstreamErr error
	var roundTiming homeRetryRoundTiming
	for homeAuthCount := 1; ; homeAuthCount++ {
		if maxRetryCredentials > 0 && len(attempted) >= maxRetryCredentials {
			if lastErr != nil {
				return cliproxyexecutor.Response{}, markHomeRetryRoundExhausted(preferredExecutionAttemptError(lastErr, upstreamErr), roundTiming.RetryAfter(), true)
			}
			return cliproxyexecutor.Response{}, &Error{Code: "auth_not_found", Message: "no auth available"}
		}
		pickOpts := withHomeRetryRound(opts, retryRound)
		pickOpts = withHomeAuthCount(pickOpts, homeAuthCount)
		pickOpts = withHomeExcludedAuthIDs(pickOpts, tried)
		selection, errSelection := m.pickHomeDispatchSelection(ctx, routeModel, pickOpts)
		if errSelection != nil {
			preferredErr := preferredExecutionAttemptError(lastErr, upstreamErr)
			var homeCooldown *homeDispatchRetryAfterError
			if lastErr != nil && errors.As(errSelection, &homeCooldown) && homeCooldown != nil {
				observeHomeCooldownRetryLimit(homeCooldown, homeRetryLimit, pinnedAuthIDFromMetadata(opts.Metadata) == "")
				return cliproxyexecutor.Response{}, markHomeRetryRoundExhausted(preferredErr, homeCooldown.RetryAfter(), false)
			}
			if shouldReturnLastErrorOnPickFailure(true, lastErr, errSelection) {
				return cliproxyexecutor.Response{}, markHomeRetryRoundExhausted(preferredErr, roundTiming.RetryAfter(), isHomeNextRoundImmediatelyAvailable(errSelection))
			}
			return cliproxyexecutor.Response{}, errSelection
		}
		auth := selection.CloneAuthForRoute(routeModel)
		if auth == nil || selection.Executor == nil {
			selection.End("missing_execution_target")
			return cliproxyexecutor.Response{}, &Error{Code: "executor_not_found", Message: "executor not registered"}
		}
		m.observeHomeRetryLimit(auth, selection, homeRetryLimit)
		if _, seen := tried[auth.ID]; seen {
			if errEnd := m.endHomeSelectionBeforeRedispatch(ctx, selection, "repeated_auth"); errEnd != nil {
				return cliproxyexecutor.Response{}, errEnd
			}
			if lastErr != nil {
				return cliproxyexecutor.Response{}, markHomeRetryRoundExhausted(preferredExecutionAttemptError(lastErr, upstreamErr), roundTiming.RetryAfter(), false)
			}
			return cliproxyexecutor.Response{}, repeatedHomeAuthError()
		}
		tried[auth.ID] = struct{}{}
		attempted[auth.ID] = struct{}{}
		entry := logEntryWithRequestID(ctx)
		debugLogAuthSelection(entry, auth, selection.Provider, routeModel)
		if errRuntimeAuth := m.bindHomeSelectionRuntimeAuth(ctx, opts, selection); errRuntimeAuth != nil {
			selection.End("runtime_auth_bind_failed")
			return cliproxyexecutor.Response{}, errRuntimeAuth
		}
		publishSelectedAuthMetadata(opts.Metadata, auth)
		execCtx, releaseAttempt, errBind := homeExecutionAttemptContext(ctx, selection)
		if errBind != nil {
			selection.End("attempt_bind_failed")
			return cliproxyexecutor.Response{}, errBind
		}
		// Enrich before auth preparation so prepare-stage usage records observe the client request.
		execCtx = contextWithRequestedModelAlias(execCtx, opts, routeModel)
		execCtx = newUpstreamAttemptContext(execCtx)
		if rt := m.roundTripperFor(auth); rt != nil {
			execCtx = context.WithValue(execCtx, roundTripperContextKey{}, rt)
			execCtx = context.WithValue(execCtx, "cliproxy.roundtripper", rt)
		}
		models, pooled, aliasResult, routing := m.preparedExecutionModelsWithAlias(auth, routeModel)
		if aliasResult.ForceMapping && responseAlias != "" {
			aliasResult.OriginalAlias = responseAlias
		}
		if len(models) > 1 {
			models = models[:1]
			pooled = false
		}
		if len(models) == 0 {
			releaseAttempt()
			if errEnd := m.endHomeSelectionBeforeRedispatch(ctx, selection, "no_execution_models"); errEnd != nil {
				return cliproxyexecutor.Response{}, errEnd
			}
			lastErr = &Error{Code: "auth_not_found", Message: "no execution models available"}
			roundTiming.Observe(lastErr)
			continue
		}
		preparedAuth, errPrepare := m.prepareHomeRequestAuth(execCtx, selection.Executor, selection)
		if errPrepare != nil {
			stateModel := m.selectionModelKeyForAuth(auth, routeModel)
			if stateModel == "" {
				stateModel = canonicalModelKey(routeModel)
			}
			m.reportHomeResult(execCtx, Result{AuthID: auth.ID, Provider: selection.Provider, Model: stateModel, RouteModel: routeModel, Success: false, Error: resultErrorFromError(errPrepare), Options: opts}, auth)
			releaseAttempt()
			if errEnd := m.endHomeSelectionBeforeRedispatch(ctx, selection, "prepare_failed"); errEnd != nil {
				return cliproxyexecutor.Response{}, errEnd
			}
			lastErr = errPrepare
			roundTiming.Observe(lastErr)
			continue
		}
		for _, upstreamModel := range models {
			execCtx = newUpstreamAttemptContext(execCtx)
			resultModel := m.stateModelForExecution(preparedAuth, routeModel, upstreamModel, pooled)
			execReq := req
			execReq.Model = upstreamModel
			if restoreExecutionModel {
				execReq.Model = executionModel
			}
			execOpts := opts
			execOpts.ExecutionLifecycle = selection
			if selection != nil && selection.CanonicalSessionID != "" {
				meta := make(map[string]any, len(execOpts.Metadata)+2)
				for k, v := range execOpts.Metadata {
					meta[k] = v
				}
				meta[cliproxyexecutor.CanonicalSessionIDMetadataKey] = selection.CanonicalSessionID
				if selection.ParentSessionID != "" && selection.ParentSessionID != selection.CanonicalSessionID {
					meta[cliproxyexecutor.ParentSessionIDMetadataKey] = selection.ParentSessionID
				} else {
					delete(meta, cliproxyexecutor.ParentSessionIDMetadataKey)
				}
				execOpts.Metadata = meta
			}
			var errIntercept error
			execReq, execOpts, errIntercept = applyRequestAfterAuthInterceptor(execCtx, selection.Executor, selection.Provider, execReq, execOpts, requestedModelAliasFromOptions(execOpts, routeModel))
			if errIntercept != nil {
				releaseAttempt()
				selection.End("request_intercepted")
				return cliproxyexecutor.Response{}, errIntercept
			}
			if !restoreExecutionModel {
				execReq = attachResolvedAPIKeyModelInfo(routing, execReq, preparedAuth, routeModel, upstreamModel)
				execReq = attachResolvedHomeModelInfo(execReq, selection.modelInfo)
			}
			if errCtx := execCtx.Err(); errCtx != nil {
				releaseAttempt()
				selection.End("attempt_canceled")
				return cliproxyexecutor.Response{}, errCtx
			}
			var response cliproxyexecutor.Response
			var errExecute error
			var effectiveAuthMu sync.RWMutex
			effectiveAuth := preparedAuth.Clone()
			setEffectiveAuth := func(auth *Auth) {
				if auth == nil || AccessTokenSHA256(auth) == "" {
					return
				}
				effectiveAuthMu.Lock()
				effectiveAuth = auth.Clone()
				effectiveAuthMu.Unlock()
			}
			getEffectiveAuth := func() (*Auth, string) {
				effectiveAuthMu.RLock()
				defer effectiveAuthMu.RUnlock()
				if effectiveAuth == nil {
					return nil, ""
				}
				return effectiveAuth.Clone(), AccessTokenSHA256(effectiveAuth)
			}
			execCtx = syncMetadataSessionToContext(execCtx, execOpts.Metadata)
			executorCtx := execCtx
			if countTokens {
				executorCtx = withAccessTokenFingerprintObserver(execCtx, setEffectiveAuth)
			}
			execute := func() (cliproxyexecutor.Response, error) {
				if countTokens {
					return selection.Executor.CountTokens(executorCtx, preparedAuth, execReq, execOpts)
				}
				return selection.Executor.Execute(execCtx, preparedAuth, execReq, execOpts)
			}
			startHomeExec := time.Now()
			response, errExecute = execute()
			errExecute = markUpstreamExecutionAttemptFromContext(execCtx, errExecute)
			durationHomeExec := time.Since(startHomeExec)
			if countTokens {
				if _, fingerprint := getEffectiveAuth(); isUnauthorizedError(errExecute) {
					m.reportHomeUnauthorized(execCtx, preparedAuth, selection.Provider, resultModel, fingerprint, extractErrorBody(errExecute))
				}
			}
			if errExecute != nil {
				if hasUpstreamExecutionAttempt(errExecute) {
					upstreamErr = errExecute
				}
				warnLogUpstreamFailure(execCtx, entry, selection.Provider, upstreamModel, preparedAuth, durationHomeExec, errExecute)
			}
			result := Result{AuthID: preparedAuth.ID, Provider: selection.Provider, Model: resultModel, RouteModel: routeModel, Success: errExecute == nil, Options: execOpts}
			if errExecute == nil {
				m.reportHomeResult(execCtx, result, preparedAuth)
				releaseAttempt()
				attemptAliasResult := resolveAttemptAliasResult(routing, preparedAuth, routeModel, upstreamModel, aliasResult)
				rewriteForceMappedResponse(&response, attemptAliasResult)
				if !m.retainHomeWebsocketSelection(ctx, opts, routeModel, selection) {
					selection.End("completed")
				}
				return response, nil
			}
			result.Error = resultErrorFromError(errExecute)
			result.RetryAfter = retryAfterFromError(errExecute)
			if isCredentialScopedError(errExecute) {
				result.CredentialScope = true
			}
			action, okAction := matchRequestScopedErrorAction(preparedAuth, errExecute, m.runtimeConfigSnapshot())
			applyRequestScopedActionToResult(action, okAction, &result)
			m.reportHomeResult(execCtx, result, preparedAuth)
			lastErr = errExecute
			if okAction {
				if isRequestScopedStop(action, okAction) {
					releaseAttempt()
					selection.End("request_stopped")
					return cliproxyexecutor.Response{}, wrapRequestStopError(errExecute)
				}
				if result.CredentialScope {
					break
				}
				continue
			}
			if isRequestInvalidError(errExecute) {
				releaseAttempt()
				selection.End("request_invalid")
				return cliproxyexecutor.Response{}, errExecute
			}
			if result.CredentialScope {
				break
			}
		}
		roundTiming.Observe(lastErr)
		releaseAttempt()
		if errEnd := m.endHomeSelectionBeforeRedispatch(ctx, selection, "execution_failed"); errEnd != nil {
			return cliproxyexecutor.Response{}, errEnd
		}
		if errCtx := execCtx.Err(); errCtx != nil && ctx != nil && ctx.Err() != nil {
			return cliproxyexecutor.Response{}, errCtx
		}
	}
}

func homeExecutionAttemptContext(ctx context.Context, selection *HomeDispatchSelection) (context.Context, func(), error) {
	if selection == nil {
		return nil, func() {}, fmt.Errorf("Home dispatch selection is nil")
	}
	return selection.AttemptContext(ctx)
}

func wrapHomeStream(ctx context.Context, result *cliproxyexecutor.StreamResult, selection *HomeDispatchSelection, releaseAttempt func()) *cliproxyexecutor.StreamResult {
	if result == nil || result.Chunks == nil {
		if releaseAttempt != nil {
			releaseAttempt()
		}
		return result
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		if releaseAttempt != nil {
			defer releaseAttempt()
		}
		if selection != nil {
			defer selection.End("stream_closed")
		}
		forward := true
		for {
			select {
			case <-ctx.Done():
				return
			case chunk, ok := <-result.Chunks:
				if !ok {
					return
				}
				if !forward {
					continue
				}
				select {
				case <-ctx.Done():
					return
				case out <- chunk:
				}
				if chunk.Err != nil && selection != nil {
					forward = false
				}
			}
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: result.Headers, Chunks: out}
}

func sanitizeDownstreamWebsocketFallbackRequest(ctx context.Context, auth *Auth, req cliproxyexecutor.Request) cliproxyexecutor.Request {
	if !cliproxyexecutor.DownstreamWebsocket(ctx) || authWebsocketsEnabled(auth) || len(req.Payload) == 0 {
		return req
	}
	updated, errDelete := sjson.DeleteBytes(req.Payload, "generate")
	if errDelete != nil {
		return req
	}
	req.Payload = updated
	return req
}
