package auth

import (
	"context"
	"fmt"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/sjson"
)

func (m *Manager) executeHome(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, countTokens bool) (cliproxyexecutor.Response, error) {
	if unlockSession := m.lockHomeWebsocketSession(ctx, opts); unlockSession != nil {
		defer unlockSession()
	}
	routeModel := authSelectionModelFromOptions(opts, req.Model)
	responseAlias := requestedModelAliasFromOptions(opts, routeModel)
	executionModel, restoreExecutionModel := executionModelForAuthSelection(opts, req.Model)
	opts = ensureRequestedModelMetadata(opts, routeModel)
	tried := make(map[string]struct{})
	var lastErr error
	for homeAuthCount := 1; ; homeAuthCount++ {
		selection, errSelection := m.pickHomeDispatchSelection(ctx, routeModel, withHomeAuthCount(opts, homeAuthCount))
		if errSelection != nil {
			if lastErr != nil && isHomeRequestRetryExceededError(errSelection) {
				return cliproxyexecutor.Response{}, lastErr
			}
			return cliproxyexecutor.Response{}, errSelection
		}
		auth := selection.CloneAuthForRoute(routeModel)
		if auth == nil || selection.Executor == nil {
			selection.End("missing_execution_target")
			return cliproxyexecutor.Response{}, &Error{Code: "executor_not_found", Message: "executor not registered"}
		}
		if _, seen := tried[auth.ID]; seen {
			selection.End("repeated_auth")
			if lastErr != nil {
				return cliproxyexecutor.Response{}, lastErr
			}
			return cliproxyexecutor.Response{}, repeatedHomeAuthError()
		}
		entry := logEntryWithRequestID(ctx)
		debugLogAuthSelection(entry, auth, selection.Provider, routeModel)
		if errRuntimeAuth := m.bindHomeSelectionRuntimeAuth(ctx, opts, selection); errRuntimeAuth != nil {
			selection.End("runtime_auth_bind_failed")
			return cliproxyexecutor.Response{}, errRuntimeAuth
		}
		publishSelectedAuthMetadata(opts.Metadata, auth)
		tried[auth.ID] = struct{}{}
		execCtx, releaseAttempt, errBind := homeExecutionAttemptContext(ctx, selection)
		if errBind != nil {
			selection.End("attempt_bind_failed")
			return cliproxyexecutor.Response{}, errBind
		}
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
			continue
		}
		preparedAuth, errPrepare := m.prepareHomeRequestAuth(execCtx, selection.Executor, selection)
		if errPrepare != nil {
			m.reportHomeResult(execCtx, Result{AuthID: auth.ID, Provider: selection.Provider, Model: routeModel, Success: false, Error: resultErrorFromError(errPrepare)}, auth)
			releaseAttempt()
			if errEnd := m.endHomeSelectionBeforeRedispatch(ctx, selection, "prepare_failed"); errEnd != nil {
				return cliproxyexecutor.Response{}, errEnd
			}
			lastErr = errPrepare
			continue
		}
		for _, upstreamModel := range models {
			resultModel := m.stateModelForExecution(preparedAuth, routeModel, upstreamModel, pooled)
			execReq := req
			execReq.Model = upstreamModel
			if restoreExecutionModel {
				execReq.Model = executionModel
			}
			execOpts := opts
			execOpts.ExecutionLifecycle = selection
			var errIntercept error
			execReq, execOpts, errIntercept = applyRequestAfterAuthInterceptor(execCtx, selection.Executor, selection.Provider, execReq, execOpts, requestedModelAliasFromOptions(execOpts, routeModel))
			if errIntercept != nil {
				releaseAttempt()
				selection.End("request_intercepted")
				return cliproxyexecutor.Response{}, errIntercept
			}
			if !restoreExecutionModel {
				execReq = attachResolvedAPIKeyModelInfo(routing, execReq, preparedAuth, routeModel, upstreamModel)
			}
			if errCtx := execCtx.Err(); errCtx != nil {
				releaseAttempt()
				selection.End("attempt_canceled")
				return cliproxyexecutor.Response{}, errCtx
			}
			var response cliproxyexecutor.Response
			var errExecute error
			if countTokens {
				response, errExecute = selection.Executor.CountTokens(execCtx, preparedAuth, execReq, execOpts)
			} else {
				response, errExecute = selection.Executor.Execute(execCtx, preparedAuth, execReq, execOpts)
			}
			result := Result{AuthID: preparedAuth.ID, Provider: selection.Provider, Model: resultModel, Success: errExecute == nil}
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
			m.reportHomeResult(execCtx, result, preparedAuth)
			lastErr = errExecute
			if isRequestInvalidError(errExecute) {
				releaseAttempt()
				selection.End("request_invalid")
				return cliproxyexecutor.Response{}, errExecute
			}
		}
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
