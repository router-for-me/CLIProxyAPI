package auth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MoeForge/moego/ojson"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func (m *Manager) executeHome(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, countTokens bool) (cliproxyexecutor.Response, error) {
	ctx = withRequestRefusedAuths(ctx)
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
	var lastErr error
	continuitySession := &continuitySession{}
	if !countTokens {
		continuitySession = ensureContinuitySession(ctx, &opts)
	}
	waitSession := continuitySession
	if countTokens {
		waitSession = nil
	}
	for {
		if !countTokens && m.continuityDeadlineExceeded(continuitySession) {
			continuitySession.budgetExhausted = true
			continuitySession.terminated = true
			continuitySession.terminalErr = preferredExecutionAttemptError(lastErr, preferredUpstreamErr)
		}
		if !countTokens && continuitySession.terminated {
			if continuitySession.terminalErr != nil {
				return cliproxyexecutor.Response{}, markContinuityTerminatedError(continuitySession.terminalErr)
			}
			return cliproxyexecutor.Response{}, markContinuityTerminatedError(&Error{Code: "continuity_terminated", Message: "continuity terminated"})
		}
		response, errExecute := m.executeHomeOnce(ctx, providers, req, opts, countTokens, maxRetryCredentials, &homeRetryLimit, attempt)
		if errExecute == nil {
			return response, nil
		}
		if isTerminalRequestStopError(errExecute) {
			return cliproxyexecutor.Response{}, errExecute
		}
		upstreamAttempted := hasUpstreamExecutionAttempt(errExecute)
		if upstreamAttempted {
			preferredUpstreamErr = errExecute
		}
		if !countTokens && !continuitySession.priorAttemptSeen && !upstreamAttempted && isPreAdmissionContinuityError(errExecute) {
			outcome, errContinuity := m.runCodexContinuityGate(ctx, continuitySession, "pre_admission", providers, retryModel, opts, attempt, false, defaultRequestRetry)
			if errContinuity != nil {
				return cliproxyexecutor.Response{}, errContinuity
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
		if !countTokens && (continuitySession.terminated || continuitySession.budgetExhausted) {
			continuitySession.terminalErr = errExecute
			return cliproxyexecutor.Response{}, markContinuityTerminatedError(errExecute)
		}
		continuitySession.priorAttemptSeen = true
		if !countTokens && upstreamAttempted && m.isCodexAttemptFailure(errExecute, nil) && isPositiveQuotaRejectionError(errExecute) {
			if _, supported := m.canRunCodexContinuity(ctx, providers); supported {
				if !m.homeRetryAllowed(attempt, homeRetryLimit) {
					continuitySession.terminated = true
					continuitySession.terminalErr = errExecute
					return cliproxyexecutor.Response{}, markContinuityTerminatedError(errExecute)
				}
				outcome, errContinuity := m.runCodexContinuityGate(ctx, continuitySession, "pre_output_429", providers, retryModel, opts, attempt+1, true, defaultRequestRetry)
				if errContinuity != nil {
					return cliproxyexecutor.Response{}, errContinuity
				}
				if outcome == continuityOutcomeReady {
					attempt++
					continue
				}
				continuitySession.terminalErr = errExecute
				return cliproxyexecutor.Response{}, markContinuityTerminatedError(errExecute)
			}
		}
		if retryRoundPending {
			if wait, okWait := pendingHomeRetryRoundDelay(errExecute, maxWait, &homeRetryLimit, pinnedAuthIDFromMetadata(opts.Metadata) == ""); okWait && m.homeRetryAllowed(attempt-1, homeRetryLimit) {
				if retryRoundWaited {
					if preferredUpstreamErr != nil && isHomeRetryRoundExhausted(errExecute) {
						errExecute = preferredExecutionAttemptError(errExecute, preferredUpstreamErr)
					}
					return cliproxyexecutor.Response{}, unwrapExecutionBoundaryError(errExecute)
				}
				exceeded, errWait := m.waitForContinuityBoundedCooldown(ctx, waitSession, wait, maxWait)
				if errWait != nil {
					return cliproxyexecutor.Response{}, errWait
				}
				if exceeded {
					continuitySession.terminalErr = errExecute
					return cliproxyexecutor.Response{}, markContinuityTerminatedError(errExecute)
				}
				retryRoundWaited = true
				continue
			}
		}
		retryRoundPending = false
		retryRoundWaited = false
		if isCredentialTerminatedError(errExecute) {
			if preferredUpstreamErr != nil {
				return cliproxyexecutor.Response{}, unwrapExecutionBoundaryError(preferredExecutionAttemptError(lastErr, preferredUpstreamErr))
			}
			return cliproxyexecutor.Response{}, unwrapExecutionBoundaryError(errExecute)
		}
		if isSchedulerRejectError(errExecute) {
			if lastErr != nil || preferredUpstreamErr != nil {
				return cliproxyexecutor.Response{}, preferredExecutionAttemptError(lastErr, preferredUpstreamErr)
			}
			return cliproxyexecutor.Response{}, errExecute
		}
		lastErr = keepPreferredCandidateError(lastErr, errExecute)
		wait, shouldRetry := m.shouldRetryAfterErrorWithHomeRetryLimit(ctx, opts, errExecute, attempt, providers, retryModel, maxWait, homeRetryLimit, defaultRequestRetry)
		if !shouldRetry {
			if preferredUpstreamErr != nil && isHomeRetryRoundExhausted(errExecute) {
				errExecute = preferredExecutionAttemptError(errExecute, preferredUpstreamErr)
			}
			return cliproxyexecutor.Response{}, unwrapExecutionBoundaryError(errExecute)
		}
		exceeded, errWait := m.waitForContinuityBoundedCooldown(ctx, waitSession, wait, maxWait)
		if errWait != nil {
			return cliproxyexecutor.Response{}, errWait
		}
		if exceeded {
			continuitySession.terminalErr = errExecute
			return cliproxyexecutor.Response{}, markContinuityTerminatedError(errExecute)
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
	clearSchedulerSelectionID(opts.Metadata)
	baseMetadata := opts.Metadata
	tried := make(map[string]struct{})
	for authID := range requestRefusedAuthsFromContext(ctx) {
		tried[authID] = struct{}{}
	}
	attempted := make(map[string]struct{})
	var lastErr error
	var upstreamErr error
	var roundTiming homeRetryRoundTiming
	for homeAuthCount := 1; ; homeAuthCount++ {
		if maxRetryCredentials > 0 && len(attempted) >= maxRetryCredentials {
			if lastErr != nil {
				preferredErr := preferredExecutionAttemptError(lastErr, upstreamErr)
				if isCredentialTerminatedError(preferredErr) {
					return cliproxyexecutor.Response{}, preferredErr
				}
				return cliproxyexecutor.Response{}, markHomeRetryRoundExhausted(preferredErr, roundTiming.RetryAfter(), true)
			}
			return cliproxyexecutor.Response{}, &Error{Code: "auth_not_found", Message: "no auth available"}
		}
		opts.Metadata = cloneRequestMetadata(baseMetadata)
		pickOpts := withHomeRetryRound(opts, retryRound)
		pickOpts = withHomeAuthCount(pickOpts, homeAuthCount)
		pickOpts = withHomeExcludedAuthIDs(pickOpts, tried)
		selection, errSelection := m.pickHomeDispatchSelection(ctx, routeModel, pickOpts)
		if errSelection != nil {
			preferredErr := preferredExecutionAttemptError(lastErr, upstreamErr)
			var homeCooldown *homeDispatchRetryAfterError
			if lastErr != nil && errors.As(errSelection, &homeCooldown) && homeCooldown != nil {
				observeHomeCooldownRetryLimit(homeCooldown, homeRetryLimit, pinnedAuthIDFromMetadata(opts.Metadata) == "")
				if isCredentialTerminatedError(preferredErr) {
					return cliproxyexecutor.Response{}, preferredErr
				}
				return cliproxyexecutor.Response{}, markHomeRetryRoundExhausted(preferredErr, homeCooldown.RetryAfter(), false)
			}
			if shouldReturnLastErrorOnPickFailure(true, lastErr, errSelection) {
				if isCredentialTerminatedError(preferredErr) {
					return cliproxyexecutor.Response{}, preferredErr
				}
				if isSchedulerRejectError(errSelection) {
					preferredErr = roundPickFailureError(lastErr, upstreamErr, errSelection)
				}
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
				preferredErr := preferredExecutionAttemptError(lastErr, upstreamErr)
				if isCredentialTerminatedError(preferredErr) {
					return cliproxyexecutor.Response{}, preferredErr
				}
				return cliproxyexecutor.Response{}, markHomeRetryRoundExhausted(preferredErr, roundTiming.RetryAfter(), false)
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
		clearSchedulerSelectionID(opts.Metadata)
		if selection.SelectionID != "" {
			opts.Metadata[cliproxyexecutor.SelectionIDMetadataKey] = selection.SelectionID
		}
		execCtx = contextWithRequestedModelAlias(execCtx, opts, routeModel)
		execCtx = coreusage.WithSelectionID(execCtx, selection.SelectionID)
		execCtx = coreusage.WithHomeSelection(execCtx, coreusage.HomeSelection{
			Reused: selection.Retained(), Model: routeModel, CredentialPolicy: credentialPolicyFromContext(ctx),
		})
		execCtx = withUpstreamAttemptProvider(m.newUpstreamAttemptContext(execCtx), selection.Provider)
		execCtx = m.WithAuthTransport(execCtx, auth)
		// Home owns credential availability; execute its dispatch without local state filtering.
		models, pooled, aliasResult, routing := m.executionModelCandidatesWithAlias(auth, routeModel)
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
			noModelsErr := &Error{Code: "auth_not_found", Message: "no execution models available"}
			lastErr = keepPreferredCandidateError(lastErr, noModelsErr)
			roundTiming.Observe(noModelsErr)
			continue
		}
		preparedAuth, errPrepare := m.prepareHomeRequestAuth(execCtx, selection.Executor, selection)
		if errPrepare != nil {
			errPrepare = markUpstreamExecutionAttemptFromContext(execCtx, errPrepare)
			if errCtx := execCtx.Err(); errCtx != nil {
				releaseAttempt()
				selection.End("attempt_canceled")
				return cliproxyexecutor.Response{}, errCtx
			}
			stateModel := m.selectionModelKeyForAuth(auth, routeModel)
			if stateModel == "" {
				stateModel = canonicalModelKey(routeModel)
			}
			prepareOpts := opts
			prepareOpts.Metadata = applyHomeSelectionSessionMetadata(opts.Metadata, selection)
			result := Result{AuthID: auth.ID, Provider: selection.Provider, Model: stateModel, RouteModel: routeModel, Success: false, Error: resultErrorFromError(errPrepare), Options: prepareOpts}
			if ra := retryAfterFromError(errPrepare); ra != nil {
				result.RetryAfter = ra
			}
			if isCredentialScopedError(errPrepare) {
				result.CredentialScope = true
			}
			action, okAction := matchRequestScopedErrorAction(auth, errPrepare, m.requestScopedErrorConfig(execCtx))
			applyRequestScopedActionToResult(action, okAction, &result)
			m.reportHomeResult(execCtx, result, auth)
			releaseAttempt()
			errPrepare = annotateTrustedRetryAfterByHint(errPrepare)
			if okAction && !isRequestStopError(errPrepare) && !isRequestScopedStop(action, okAction) {
				errPrepare = wrapRequestScopedContinueError(errPrepare)
			}
			syncPreferredUpstreamError(&upstreamErr, errPrepare)
			if isRequestStopError(errPrepare) {
				selection.End("request_stopped")
				return cliproxyexecutor.Response{}, errPrepare
			}
			if isRequestScopedStop(action, okAction) {
				selection.End("request_stopped")
				return cliproxyexecutor.Response{}, wrapRequestStopError(errPrepare)
			}
			if isTerminalRequestStopError(errPrepare) || (!okAction && isRequestInvalidError(errPrepare)) {
				selection.End("request_invalid")
				return cliproxyexecutor.Response{}, errPrepare
			}
			if errEnd := m.endHomeSelectionBeforeRedispatch(ctx, selection, "prepare_failed"); errEnd != nil {
				return cliproxyexecutor.Response{}, errEnd
			}
			lastErr = keepPreferredCandidateError(lastErr, errPrepare)
			roundTiming.Observe(errPrepare)
			continue
		}
		var refusedErr error
		for _, upstreamModel := range models {
			execCtx = m.newUpstreamAttemptContext(execCtx)
			resultModel := m.stateModelForExecution(preparedAuth, routeModel, upstreamModel, pooled)
			execReq := req
			execReq.Model = upstreamModel
			if restoreExecutionModel {
				execReq.Model = executionModel
			}
			execOpts := opts
			execOpts.ExecutionLifecycle = selection
			if selection != nil && (selection.CanonicalSessionID != "" || selection.lcpAffinitySessionID != "") {
				execOpts.Metadata = applyHomeSelectionSessionMetadata(execOpts.Metadata, selection)
			} else {
				payload := execOpts.OriginalRequest
				if len(payload) == 0 {
					payload = execReq.Payload
				}
				execOpts.Metadata = ensureCanonicalSessionMetadata(execOpts.Metadata, execOpts.Headers, payload)
			}
			execReq = attachResolvedExecutionModelInfo(routing, execReq, preparedAuth, routeModel, upstreamModel, restoreExecutionModel)
			if !restoreExecutionModel {
				execReq = attachResolvedHomeModelInfo(execReq, preparedAuth, routeModel, selection.modelInfo, selection.configurationUpdateSupport)
			}
			var errIntercept error
			execReq, execOpts, errIntercept = prepareRequestAfterAuth(execCtx, selection.Executor, preparedAuth, selection.Provider, execReq, execOpts, requestedModelAliasFromOptions(execOpts, routeModel))
			if errIntercept != nil {
				if isCredentialTerminatedError(errIntercept) {
					refusedErr = errIntercept
					break
				}
				releaseAttempt()
				selection.End("request_intercepted")
				return cliproxyexecutor.Response{}, errIntercept
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
			var activeAuth atomic.Pointer[Auth]
			activeAuth.Store(preparedAuth)
			execCtx = m.withExecutionAuthRefresh(syncMetadataSessionToContext(execCtx, execOpts.Metadata), executionAuthRefreshOptions{
				ephemeral: true,
				onAuthUpdated: func(updated *Auth) {
					if updated != nil {
						activeAuth.Store(updated.Clone())
						setEffectiveAuth(updated)
					}
				},
			})
			executorCtx := execCtx
			if countTokens {
				executorCtx = withAccessTokenFingerprintObserver(execCtx, setEffectiveAuth)
			}
			executor := executorForAuth(selection.Executor, preparedAuth)
			execute := func() (cliproxyexecutor.Response, error) {
				if countTokens {
					return executor.CountTokens(executorCtx, preparedAuth, execReq, execOpts)
				}
				return executor.Execute(execCtx, preparedAuth, execReq, execOpts)
			}
			startHomeExec := time.Now()
			response, errExecute = execute()
			if currentAuth := activeAuth.Load(); currentAuth != nil && currentAuth != preparedAuth {
				preparedAuth = currentAuth
			}
			errExecute = markUpstreamExecutionAttemptFromContext(execCtx, errExecute)
			durationHomeExec := time.Since(startHomeExec)
			if errCtx := execCtx.Err(); errCtx != nil {
				releaseAttempt()
				selection.End("attempt_canceled")
				return cliproxyexecutor.Response{}, errCtx
			}
			if countTokens {
				if _, fingerprint := getEffectiveAuth(); isUnauthorizedError(errExecute) {
					m.reportHomeUnauthorized(execCtx, preparedAuth, selection.Provider, resultModel, fingerprint, extractErrorBody(errExecute))
				}
			}
			if errExecute != nil {
				syncPreferredUpstreamError(&upstreamErr, errExecute)
				warnLogUpstreamFailure(execCtx, entry, selection.Provider, upstreamModel, preparedAuth, durationHomeExec, errExecute)
			}
			result := Result{AuthID: preparedAuth.ID, Provider: selection.Provider, Model: resultModel, RouteModel: routeModel, Success: errExecute == nil, CompletedResponse: errExecute == nil && !countTokens && !response.Incomplete && isCompletedExecutionPayload(response.Payload), Options: execOpts, AccessToken: authAccessTokenForRequest(execCtx, preparedAuth, execOpts.Headers), RefreshToken: authRefreshToken(preparedAuth), CredentialVersion: preparedAuth.CredentialVersion, RegistrationEpoch: preparedAuth.RegistrationEpoch}
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
			action, okAction := matchRequestScopedErrorAction(preparedAuth, errExecute, m.requestScopedErrorConfig(execCtx))
			applyRequestScopedActionToResult(action, okAction, &result)
			if errCtx := execCtx.Err(); errCtx != nil {
				releaseAttempt()
				selection.End("attempt_canceled")
				return cliproxyexecutor.Response{}, errCtx
			}
			m.reportHomeResult(execCtx, result, preparedAuth)
			roundTiming.Observe(errExecute)
			if isRequestStopError(errExecute) {
				errExecute = annotateTrustedRetryAfterByHint(errExecute)
				syncPreferredUpstreamError(&upstreamErr, errExecute)
				lastErr = errExecute
				releaseAttempt()
				selection.End("request_stopped")
				return cliproxyexecutor.Response{}, errExecute
			}
			if okAction {
				if isRequestScopedStop(action, okAction) {
					errExecute = annotateTrustedRetryAfterByHint(errExecute)
					syncPreferredUpstreamError(&upstreamErr, errExecute)
					lastErr = errExecute
					releaseAttempt()
					selection.End("request_stopped")
					return cliproxyexecutor.Response{}, wrapRequestStopError(errExecute)
				}
				errExecute = wrapRequestScopedContinueError(errExecute)
				syncPreferredUpstreamError(&upstreamErr, errExecute)
				lastErr = keepPreferredCandidateError(lastErr, errExecute)
				if result.CredentialScope || isRequestTerminatedError(errExecute) {
					break
				}
				continue
			}
			lastErr = keepPreferredCandidateError(lastErr, errExecute)
			if isRequestInvalidError(errExecute) || cliproxyexecutor.IsUpstreamWebsocketTurnError(errExecute) {
				releaseAttempt()
				selection.End("request_invalid")
				return cliproxyexecutor.Response{}, errExecute
			}
			if result.CredentialScope {
				break
			}
		}
		if refusedErr != nil {
			recordRequestRefusedAuth(ctx, auth.ID)
			lastErr = refusedErr
			releaseAttempt()
			if errEnd := m.endHomeSelectionBeforeRedispatch(ctx, selection, "credential_refused"); errEnd != nil {
				return cliproxyexecutor.Response{}, errEnd
			}
			continue
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
		if selection != nil {
			selection.End("empty_stream")
		}
		return result
	}
	var done <-chan struct{}
	if ctx != nil {
		done = ctx.Done()
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
			case <-done:
				discardStreamChunks(result.Chunks)
				return
			case chunk, ok := <-result.Chunks:
				if !ok {
					return
				}
				if !forward {
					continue
				}
				select {
				case <-done:
					discardStreamChunks(result.Chunks)
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
	updated, errDelete := ojson.DeleteBytes(req.Payload, "generate")
	if errDelete != nil {
		return req
	}
	req.Payload = updated
	return req
}
