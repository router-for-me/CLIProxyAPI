package auth

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/MoeForge/moego/json"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func discardStreamChunks(ch <-chan cliproxyexecutor.StreamChunk) {
	if ch == nil {
		return
	}
	go func() {
		for range ch {
		}
	}()
}

type streamBootstrapError struct {
	cause   error
	headers http.Header
	// attemptID identifies the upstream attempt marker this wrapper was
	// built from, so syncPreferredUpstreamError can tell a retry hint
	// refreshed for the same attempt apart from one raised by a different
	// credential attempt that surfaced an equal error value.
	attemptID uint64
}

func cloneHTTPHeader(headers http.Header) http.Header {
	if headers == nil {
		return nil
	}
	return headers.Clone()
}

func newStreamBootstrapError(err error, headers http.Header) error {
	if err == nil {
		return nil
	}
	upstreamAttempt := hasUpstreamExecutionAttempt(err)
	provider := upstreamAttemptProviderFromError(err)
	var marked *upstreamExecutionAttemptError
	attemptID := uint64(0)
	if errors.As(err, &marked) && marked != nil {
		attemptID = marked.attemptID
	}
	err = unwrapUpstreamExecutionAttempt(err)
	if existing, ok := err.(*streamBootstrapError); ok && existing != nil {
		// Already wrapped: refresh the captured headers when the caller
		// supplies a newer set, but never nest another wrapper.
		if headers != nil {
			existing.headers = cloneHTTPHeader(headers)
		}
		if upstreamAttempt {
			return markUpstreamExecutionAttemptWithProviderAndID(existing, provider, attemptID)
		}
		return existing
	}
	bootstrapErr := &streamBootstrapError{
		cause:     err,
		headers:   cloneHTTPHeader(headers),
		attemptID: attemptID,
	}
	if upstreamAttempt {
		return markUpstreamExecutionAttemptWithProviderAndID(bootstrapErr, provider, attemptID)
	}
	return bootstrapErr
}

func (e *streamBootstrapError) Error() string {
	if e == nil || e.cause == nil {
		return ""
	}
	return e.cause.Error()
}

func (e *streamBootstrapError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *streamBootstrapError) Headers() http.Header {
	if e == nil {
		return nil
	}
	return cloneHTTPHeader(e.headers)
}

// streamHeaderError attaches upstream response headers to an error that must be
// returned directly (for example a request-fault bootstrap failure) without
// becoming a streamBootstrapError. It exposes Headers() so the error path can
// forward upstream diagnostic headers, while Unwrap preserves the original
// error classification and status.
type streamHeaderError struct {
	cause   error
	headers http.Header
}

func (e *streamHeaderError) Error() string {
	if e == nil || e.cause == nil {
		return ""
	}
	return e.cause.Error()
}

func (e *streamHeaderError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *streamHeaderError) Headers() http.Header {
	if e == nil {
		return nil
	}
	return cloneHTTPHeader(e.headers)
}

func newStreamHeaderError(err error, headers http.Header) error {
	if err == nil {
		return nil
	}
	if headers == nil {
		return err
	}
	if existing, ok := err.(*streamHeaderError); ok && existing != nil {
		return &streamHeaderError{cause: existing.cause, headers: cloneHTTPHeader(headers)}
	}
	return &streamHeaderError{cause: err, headers: cloneHTTPHeader(headers)}
}

func streamErrorResult(headers http.Header, err error) *cliproxyexecutor.StreamResult {
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Err: err}
	close(ch)
	return &cliproxyexecutor.StreamResult{
		Headers: cloneHTTPHeader(headers),
		Chunks:  ch,
	}
}

func validateStreamResult(result *cliproxyexecutor.StreamResult, err error) (*cliproxyexecutor.StreamResult, error) {
	if err != nil {
		return result, err
	}
	if result == nil || result.Chunks == nil {
		return result, &Error{Code: "empty_stream", Message: "upstream stream has no source", Retryable: true}
	}
	return result, nil
}

func readStreamBootstrap(ctx context.Context, ch <-chan cliproxyexecutor.StreamChunk) ([]cliproxyexecutor.StreamChunk, bool, error) {
	if ch == nil {
		return nil, true, nil
	}
	sawIncomplete := false
	for {
		var (
			chunk cliproxyexecutor.StreamChunk
			ok    bool
		)
		if ctx != nil {
			// Prefer a chunk that is already available so an upstream error delivered together
			// with a context cancellation is not discarded.
			select {
			case chunk, ok = <-ch:
			default:
				select {
				case <-ctx.Done():
					return nil, false, ctx.Err()
				case chunk, ok = <-ch:
				}
			}
		} else {
			chunk, ok = <-ch
		}
		if !ok {
			return nil, true, nil
		}
		if chunk.Err != nil {
			return nil, false, chunk.Err
		}
		if len(chunk.Payload) == 0 {
			if chunk.Incomplete {
				sawIncomplete = true
			}
			continue
		}
		if sawIncomplete {
			chunk.Incomplete = true
		}
		return []cliproxyexecutor.StreamChunk{chunk}, false, nil
	}
}

// recordCanceledUpstreamCooldown keeps an upstream-confirmed cooldown (an error carrying
// RetryAfter) when the request context was canceled while the executor was returning, for
// example because the downstream connection was closed in response to the same upstream error.
// Errors without RetryAfter, such as plain client cancellations, are not recorded.
func (m *Manager) recordCanceledUpstreamCooldown(ctx context.Context, auth *Auth, provider, resultModel, routeModel string, opts cliproxyexecutor.Options, err error, ephemeral bool, executionSeq uint64) {
	retryAfter := retryAfterFromError(err)
	if m == nil || auth == nil || retryAfter == nil {
		return
	}
	action, okAction := matchRequestScopedErrorAction(auth, err, m.requestScopedErrorConfig(ctx))
	result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, RouteModel: routeModel, Success: false, Error: resultErrorFromError(err), Options: opts, RetryAfter: retryAfter, AccessToken: authAccessTokenForRequest(ctx, auth, opts.Headers), CredentialVersion: auth.CredentialVersion, RegistrationEpoch: auth.RegistrationEpoch, ExecutionSeq: executionSeq}
	result.CredentialScope = isCredentialScopedError(err)
	applyRequestScopedActionToResult(action, okAction, &result)
	m.recordExecutionResult(context.WithoutCancel(ctx), result, auth, ephemeral)
}

func (m *Manager) wrapStreamResult(ctx context.Context, auth *Auth, provider, resultModel, routeModel string, headers http.Header, buffered []cliproxyexecutor.StreamChunk, remaining <-chan cliproxyexecutor.StreamChunk, aliasResult OAuthModelAliasResult, ephemeralResult bool, opts cliproxyexecutor.Options, executionSeq uint64) *cliproxyexecutor.StreamResult {
	return m.wrapStreamResultWithCancel(ctx, auth, provider, resultModel, routeModel, headers, buffered, remaining, aliasResult, ephemeralResult, opts, executionSeq, nil)
}

func (m *Manager) wrapStreamResultWithCancel(ctx context.Context, auth *Auth, provider, resultModel, routeModel string, headers http.Header, buffered []cliproxyexecutor.StreamChunk, remaining <-chan cliproxyexecutor.StreamChunk, aliasResult OAuthModelAliasResult, ephemeralResult bool, opts cliproxyexecutor.Options, executionSeq uint64, cancelAttempt context.CancelFunc) *cliproxyexecutor.StreamResult {
	ctx = WithCodexClientRequest(ctx, isOpenAIImageExecutionOptions(opts) || IsCodexClientRequest(ctx, opts.Headers))
	accessToken := authAccessTokenForRequest(ctx, auth, opts.Headers)
	out := make(chan cliproxyexecutor.StreamChunk)
	streamStart := time.Now()
	go func() {
		defer close(out)
		if cancelAttempt != nil {
			defer cancelAttempt()
		}
		var failed bool
		var sawCompleted bool
		var sawIncomplete bool
		forward := true
		var rewriter *StreamRewriter
		if aliasResult.ForceMapping && strings.TrimSpace(aliasResult.OriginalAlias) != "" {
			rewriter = NewStreamRewriter(StreamRewriteOptions{RewriteModel: aliasResult.OriginalAlias})
		}
		emit := func(chunk cliproxyexecutor.StreamChunk) bool {
			if chunk.Err != nil && !failed {
				if ctxErr := ctx.Err(); ctxErr != nil {
					if retryAfterFromError(chunk.Err) != nil {
						m.recordCanceledUpstreamCooldown(ctx, auth, provider, resultModel, routeModel, opts, chunk.Err, ephemeralResult, executionSeq)
						failed = true
						return false
					}
					if !isRequestScopedError(chunk.Err) {
						failed = true
						return false
					}
				}
				failed = true
				entry := logEntryWithRequestID(ctx)
				warnLogUpstreamFailure(ctx, entry, provider, resultModel, auth, time.Since(streamStart), chunk.Err)
				rerr := resultErrorFromError(chunk.Err)
				action, okAction := matchRequestScopedErrorAction(auth, chunk.Err, m.requestScopedErrorConfig(ctx))
				result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, RouteModel: routeModel, Success: false, Error: rerr, Options: opts, AccessToken: accessToken, CredentialVersion: auth.CredentialVersion, RegistrationEpoch: auth.RegistrationEpoch, ExecutionSeq: executionSeq}
				result.RetryAfter = retryAfterFromError(chunk.Err)
				result.CredentialScope = isCredentialScopedError(chunk.Err)
				applyRequestScopedActionToResult(action, okAction, &result)
				m.recordExecutionResult(ctx, result, auth, ephemeralResult)
				if okAction && !isRequestTerminatedError(chunk.Err) && !isRequestStopError(chunk.Err) && !isTOSViolationError(chunk.Err) {
					if isRequestScopedStop(action, okAction) {
						chunk.Err = wrapRequestStopError(chunk.Err)
					} else {
						chunk.Err = wrapRequestScopedContinueError(chunk.Err)
					}
				}
			}
			if !forward {
				return false
			}
			if chunk.Err != nil {
				if session := continuitySessionFromContext(ctx); session != nil {
					session.setLastStreamProvider(provider)
				}
				if ctx == nil {
					out <- chunk
					if cancelAttempt != nil {
						cancelAttempt()
					}
					forward = false
					return true
				}
				select {
				case <-ctx.Done():
					forward = false
					return false
				case out <- chunk:
					if cancelAttempt != nil {
						cancelAttempt()
					}
					forward = false
					return true
				}
			}
			if chunk.Incomplete {
				sawIncomplete = true
			}
			if len(chunk.Payload) == 0 {
				return true
			}
			completedChunk, incompleteChunk := streamPayloadCompletionFlags(chunk.Payload)
			if completedChunk {
				sawCompleted = true
			}
			if incompleteChunk {
				sawIncomplete = true
			}
			payload := rewriteForceMappedStreamChunk(rewriter, chunk.Payload)
			if len(payload) == 0 {
				return true
			}
			chunk.Payload = payload
			if ctx == nil {
				out <- chunk
				return true
			}
			select {
			case <-ctx.Done():
				forward = false
				return false
			case out <- chunk:
				return true
			}
		}
		for _, chunk := range buffered {
			if !forward {
				break
			}
			if ok := emit(chunk); !ok {
				if cancelAttempt != nil {
					cancelAttempt()
				}
				discardStreamChunks(remaining)
				return
			}
		}
		for {
			var (
				chunk cliproxyexecutor.StreamChunk
				open  bool
			)
			if ctx != nil {
				// Prefer a chunk that is already available so an upstream error delivered
				// together with cancellation can still record its RetryAfter cooldown.
				select {
				case chunk, open = <-remaining:
				default:
					select {
					case <-ctx.Done():
						if cancelAttempt != nil {
							cancelAttempt()
						}
						discardStreamChunks(remaining)
						return
					case chunk, open = <-remaining:
					}
				}
			} else {
				chunk, open = <-remaining
			}
			if !open {
				break
			}
			if !forward {
				continue
			}
			if ok := emit(chunk); !ok {
				if cancelAttempt != nil {
					cancelAttempt()
				}
				discardStreamChunks(remaining)
				return
			}
		}
		if forward {
			tail := finishForceMappedStreamChunks(rewriter)
			rewriter = nil
			if len(tail) > 0 {
				tailChunk := cliproxyexecutor.StreamChunk{Payload: tail}
				if !emit(tailChunk) {
					return
				}
			}
		}
		if !failed && (ctx == nil || ctx.Err() == nil) {
			m.recordExecutionResult(ctx, Result{AuthID: auth.ID, Provider: provider, Model: resultModel, RouteModel: routeModel, Success: true, CompletedResponse: sawCompleted && !sawIncomplete, Options: opts, AccessToken: accessToken, CredentialVersion: auth.CredentialVersion, RegistrationEpoch: auth.RegistrationEpoch, ExecutionSeq: executionSeq}, auth, ephemeralResult)
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: headers, Chunks: out}
}

func streamPayloadCompletionFlags(payload []byte) (completed, incomplete bool) {
	if len(payload) == 0 {
		return false, false
	}
	for _, rawLine := range bytes.Split(payload, []byte("\n")) {
		line := bytes.TrimSpace(rawLine)
		if len(line) == 0 || bytes.HasPrefix(line, []byte(":")) || bytes.HasPrefix(line, []byte("event:")) || bytes.HasPrefix(line, []byte("id:")) || bytes.HasPrefix(line, []byte("retry:")) {
			continue
		}
		data := line
		if bytes.HasPrefix(line, []byte("data:")) {
			data = bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		}
		if bytes.Equal(data, []byte("[DONE]")) {
			continue
		}
		if len(data) < 2 || data[0] != '{' || data[len(data)-1] != '}' {
			continue
		}
		comp, incomp := jsonObjectCompletionFlags(data)
		if comp {
			completed = true
		}
		if incomp {
			incomplete = true
		}
	}
	return completed, incomplete
}

// anthropicStopReasonFlags classifies an Anthropic stop_reason value with the
// same allow/deny semantics for both streaming message_delta events and
// non-streaming message payloads.
func anthropicStopReasonFlags(stopReason string) (completed, incomplete bool) {
	switch strings.ToLower(strings.TrimSpace(stopReason)) {
	case "max_tokens", "stopsequencemaxtokens", "refusal", "model_context_window_exceeded", "pause_turn":
		return false, true
	case "end_turn", "stop_sequence", "tool_use":
		return true, false
	}
	return false, false
}

// geminiCandidatesCompletionFlags inspects a Gemini/Antigravity candidates slice
// and classifies terminal finishReason values as completed or incomplete.
func geminiCandidatesCompletionFlags(candidates []any) (completed, incomplete bool) {
	for _, item := range candidates {
		cand, okCand := item.(map[string]any)
		if !okCand {
			continue
		}
		fr, okFR := cand["finishReason"].(string)
		if !okFR {
			continue
		}
		switch strings.ToUpper(strings.TrimSpace(fr)) {
		case "MAX_TOKENS",
			"SAFETY",
			"RECITATION",
			"OTHER",
			"MALFORMED_FUNCTION_CALL",
			"UNEXPECTED_TOOL_CALL",
			"MALFORMED_RESPONSE",
			"PROHIBITED_CONTENT",
			"BLOCKLIST",
			"SPII",
			"IMAGE_SAFETY",
			"LANGUAGE",
			"NO_IMAGE":
			incomplete = true
		case "STOP":
			completed = true
		}
	}
	return completed, incomplete
}

func jsonObjectCompletionFlags(rawJSON []byte) (completed, incomplete bool) {
	var obj map[string]any
	if err := json.Unmarshal(rawJSON, &obj); err != nil || len(obj) == 0 {
		return false, false
	}
	if obj["error"] != nil {
		return false, true
	}
	eventType, _ := obj["type"].(string)
	eventType = strings.TrimSpace(eventType)
	status, _ := obj["status"].(string)
	status = strings.ToLower(strings.TrimSpace(status))
	respObj, hasRespObj := obj["response"].(map[string]any)
	if hasRespObj {
		if respObj["error"] != nil || respObj["incomplete_details"] != nil {
			incomplete = true
		}
		if respStatus, _ := respObj["status"].(string); strings.TrimSpace(respStatus) != "" {
			status = strings.ToLower(strings.TrimSpace(respStatus))
		}
	}
	switch status {
	case "incomplete", "failed", "cancelled", "canceled":
		return false, true
	}
	switch eventType {
	case "response.incomplete", "response.failed", "response.cancelled", "response.canceled", "error":
		return false, true
	case "response.completed", "response.done", "message_stop":
		if status == "" || status == "completed" {
			return true, incomplete
		}
		return false, true
	case "message_delta":
		if deltaObj, ok := obj["delta"].(map[string]any); ok {
			if sr, okSR := deltaObj["stop_reason"].(string); okSR {
				deltaCompleted, deltaIncomplete := anthropicStopReasonFlags(sr)
				if deltaIncomplete {
					incomplete = true
				}
				if deltaCompleted {
					completed = true
				}
			}
		}
	}
	if choices, ok := obj["choices"].([]any); ok {
		for _, item := range choices {
			choice, okChoice := item.(map[string]any)
			if !okChoice {
				continue
			}
			fr, okFR := choice["finish_reason"].(string)
			if !okFR {
				continue
			}
			switch strings.ToLower(strings.TrimSpace(fr)) {
			case "length", "content_filter":
				incomplete = true
			case "stop", "tool_calls", "function_call", "end_turn":
				completed = true
			}
		}
	}
	if candidates, ok := obj["candidates"].([]any); ok {
		candCompleted, candIncomplete := geminiCandidatesCompletionFlags(candidates)
		if candCompleted {
			completed = true
		}
		if candIncomplete {
			incomplete = true
		}
	}
	if hasRespObj {
		if candidates, ok := respObj["candidates"].([]any); ok {
			candCompleted, candIncomplete := geminiCandidatesCompletionFlags(candidates)
			if candCompleted {
				completed = true
			}
			if candIncomplete {
				incomplete = true
			}
		}
	}
	return completed, incomplete
}

func isCompletedExecutionPayload(payload []byte) bool {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return false
	}
	var obj map[string]any
	if err := json.Unmarshal(trimmed, &obj); err != nil || len(obj) == 0 {
		return false
	}
	if obj["error"] != nil || obj["incomplete_details"] != nil {
		return false
	}
	comp, incomp := jsonObjectCompletionFlags(trimmed)
	if incomp {
		return false
	}
	if comp {
		return true
	}
	status, _ := obj["status"].(string)
	status = strings.ToLower(strings.TrimSpace(status))
	if respObj, ok := obj["response"].(map[string]any); ok {
		if respObj["error"] != nil || respObj["incomplete_details"] != nil {
			return false
		}
		if respStatus, _ := respObj["status"].(string); strings.TrimSpace(respStatus) != "" {
			status = strings.ToLower(strings.TrimSpace(respStatus))
		}
	}
	switch status {
	case "incomplete", "failed", "cancelled", "canceled", "in_progress", "queued":
		return false
	case "completed":
		return true
	}
	if objType, _ := obj["type"].(string); strings.TrimSpace(objType) == "message" {
		if stopReason, ok := obj["stop_reason"].(string); ok {
			msgCompleted, msgIncomplete := anthropicStopReasonFlags(stopReason)
			if msgIncomplete {
				return false
			}
			if msgCompleted {
				return true
			}
		}
	}
	if output, ok := obj["output"].([]any); ok && len(output) > 0 && status == "" {
		return true
	}
	return false
}

func (m *Manager) executeStreamWithModelPool(ctx context.Context, executor ProviderExecutor, auth *Auth, provider string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, routeModel, executionModel string, execModels []string, pooled bool, aliasResult OAuthModelAliasResult, routing *apiKeyModelRoutingSnapshot, allowRetry bool, ephemeralResult bool) (*cliproxyexecutor.StreamResult, error) {
	if executor == nil {
		return nil, &Error{Code: "executor_not_found", Message: "executor not registered"}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	executor = executorForAuth(executor, auth)
	baseCtx := contextWithRequestedModelAlias(ctx, opts, routeModel)
	var lastErr error
	var upstreamErr error
	didRefreshOnUnauthorized := false
	for idx, execModel := range execModels {
		execCtx := withUpstreamAttemptProvider(m.newUpstreamAttemptContext(baseCtx), provider)
		resultModel := m.stateModelForExecution(auth, routeModel, execModel, pooled)
		execReq := req
		execReq.Model = execModel
		if executionModel != "" {
			execReq.Model = executionModel
		}
		execOpts := opts
		payload := execOpts.OriginalRequest
		if len(payload) == 0 {
			payload = execReq.Payload
		}
		execOpts.Metadata = ensureCanonicalSessionMetadata(execOpts.Metadata, execOpts.Headers, payload)
		var errIntercept error
		execReq, execOpts, errIntercept = prepareRequestAfterAuth(execCtx, executor, auth, provider, execReq, execOpts, requestedModelAliasFromOptions(execOpts, routeModel))
		if errIntercept != nil {
			if isCredentialTerminatedError(errIntercept) {
				if auth != nil {
					recordRequestRefusedAuth(execCtx, auth.ID)
				}
				if upstreamErr != nil {
					return nil, preferredExecutionAttemptError(lastErr, upstreamErr)
				}
			}
			return nil, errIntercept
		}
		execReq = attachResolvedExecutionModelInfo(routing, execReq, auth, routeModel, execModel, executionModel != "")
		if errCtx := execCtx.Err(); errCtx != nil {
			return nil, errCtx
		}
		entry := logEntryWithRequestID(execCtx)
		execCtx = syncMetadataSessionToContext(execCtx, execOpts.Metadata)
		executionSeq := m.nextExecutionSeq()
		accessToken := authAccessTokenForRequest(execCtx, auth, execOpts.Headers)
		var activeAuth atomic.Pointer[Auth]
		activeAuth.Store(auth)
		syncActiveAuth := func() {
			if currentAuth := activeAuth.Load(); currentAuth != nil && currentAuth != auth {
				auth = currentAuth
				accessToken = authAccessTokenForRequest(execCtx, auth, execOpts.Headers)
			}
		}

		var cancelAttempt context.CancelFunc
		var attemptCleaned bool
		startAttempt := func(parent context.Context) context.Context {
			if cancelAttempt != nil {
				cancelAttempt()
			}
			var attemptCtx context.Context
			attemptCtx, cancelAttempt = context.WithCancel(parent)
			attemptCtx = m.withExecutionAuthRefresh(attemptCtx, executionAuthRefreshOptions{
				ephemeral: ephemeralResult,
				onAuthUpdated: func(updated *Auth) {
					if updated != nil {
						activeAuth.Store(updated.Clone())
					}
				},
			})
			attemptCleaned = false
			return attemptCtx
		}
		cancelCurrentAttempt := func() {
			if cancelAttempt != nil {
				cancelAttempt()
				cancelAttempt = nil
			}
		}
		abandonAttempt := func(res *cliproxyexecutor.StreamResult) {
			cancelCurrentAttempt()
			if attemptCleaned {
				return
			}
			attemptCleaned = true
			if res != nil && res.Chunks != nil {
				discardStreamChunks(res.Chunks)
			}
		}

		var preRefreshStreamErr error
		activeAuth.Store(auth)
		attemptCtx := startAttempt(execCtx)
		startStream := time.Now()
		streamResult, errStream := executor.ExecuteStream(attemptCtx, auth, execReq, execOpts)
		syncActiveAuth()
		errStream = markUpstreamExecutionAttemptFromContext(attemptCtx, errStream)
		durationStream := time.Since(startStream)
		if errStream != nil {
			abandonAttempt(streamResult)
			if isHomeExecutorPluginUnavailableError(errStream) {
				return nil, errStream
			}
			if errCtx := execCtx.Err(); errCtx != nil {
				m.recordCanceledUpstreamCooldown(execCtx, auth, provider, resultModel, routeModel, execOpts, errStream, ephemeralResult, executionSeq)
				return nil, errCtx
			}
			if allowRetry && !ephemeralResult {
				alreadyTried := didRefreshOnUnauthorized
				refreshed, okRefresh := m.tryRefreshAfterUnauthorized(newUpstreamAttemptContext(execCtx), auth, errStream, alreadyTried, execOpts.Headers)
				if okRefresh {
					preRefreshStreamErr = errStream
					auth = refreshed
					publishSelectedAuthMetadata(execOpts.Metadata, auth)
					didRefreshOnUnauthorized = true
					execCtx = m.newUpstreamAttemptContext(execCtx)
					execCtx = syncMetadataSessionToContext(execCtx, execOpts.Metadata)
					executionSeq = m.nextExecutionSeq()
					accessToken = authAccessTokenForRequest(execCtx, auth, execOpts.Headers)
					activeAuth.Store(auth)
					attemptCtx = startAttempt(execCtx)
					startRetry := time.Now()
					streamResult, errStream = executor.ExecuteStream(attemptCtx, auth, execReq, execOpts)
					syncActiveAuth()
					errStream = markUpstreamExecutionAttemptFromContext(attemptCtx, errStream)
					durationRetry := time.Since(startRetry)
					if errStream != nil {
						abandonAttempt(streamResult)
						warnLogUpstreamFailure(execCtx, entry, provider, execModel, auth, durationRetry, errStream)
						if errCtx := execCtx.Err(); errCtx != nil {
							m.recordCanceledUpstreamCooldown(execCtx, auth, provider, resultModel, routeModel, execOpts, errStream, ephemeralResult, executionSeq)
							return nil, errCtx
						}
					}
				} else {
					warnLogUpstreamFailure(execCtx, entry, provider, execModel, auth, durationStream, errStream)
				}
			} else {
				warnLogUpstreamFailure(execCtx, entry, provider, execModel, auth, durationStream, errStream)
			}
		}
		if !ephemeralResult {
			if errCancel := claudeOAuthRequestCancellation(execCtx, auth, errStream); errCancel != nil {
				abandonAttempt(streamResult)
				return nil, errCancel
			}
		}
		streamResult, errStream = validateStreamResult(streamResult, errStream)
		errStream = markUpstreamExecutionAttemptFromContext(attemptCtx, errStream)
		if errStream != nil {
			abandonAttempt(streamResult)
			rerr := resultErrorFromError(errStream)
			action, okAction := matchRequestScopedErrorAction(auth, errStream, m.requestScopedErrorConfig(execCtx))
			result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, RouteModel: routeModel, Success: false, Error: rerr, Options: execOpts, AccessToken: accessToken, CredentialVersion: auth.CredentialVersion, RegistrationEpoch: auth.RegistrationEpoch, ExecutionSeq: executionSeq}
			result.RetryAfter = retryAfterFromError(errStream)
			if isCredentialScopedError(errStream) {
				result.CredentialScope = true
			}
			applyRequestScopedActionToResult(action, okAction, &result)
			m.recordExecutionResult(execCtx, result, auth, ephemeralResult)
			errStream = annotateTrustedRetryAfterByHint(errStream)
			if okAction && !isRequestStopError(errStream) && !isTOSViolationError(errStream) && !isRequestScopedStop(action, okAction) {
				errStream = wrapRequestScopedContinueError(errStream)
			}
			if hasUpstreamExecutionAttempt(errStream) {
				syncPreferredUpstreamError(&upstreamErr, errStream)
			} else if preRefreshStreamErr != nil {
				syncPreferredUpstreamError(&upstreamErr, preRefreshStreamErr)
			}
			if isRequestStopError(errStream) {
				return nil, errStream
			}
			if isTOSViolationError(errStream) {
				if isRequestScopedStop(action, okAction) {
					return nil, wrapRequestStopError(errStream)
				}
				return nil, errStream
			}
			if okAction {
				if isRequestScopedStop(action, okAction) {
					return nil, wrapRequestStopError(errStream)
				}
				if isRequestTerminatedError(errStream) {
					return nil, errStream
				}
				lastErr = errStream
				if result.CredentialScope {
					return nil, preferredExecutionAttemptError(errStream, upstreamErr)
				}
				continue
			}
			if isTerminalRequestStopError(errStream) || isRequestInvalidError(errStream) || cliproxyexecutor.IsUpstreamWebsocketTurnError(errStream) {
				return nil, errStream
			}
			lastErr = errStream
			if result.CredentialScope {
				return nil, preferredExecutionAttemptError(errStream, upstreamErr)
			}
			continue
		}

		var preRefreshBootstrapErr error
		var preRefreshBootstrapHeaders http.Header
		buffered, closed, bootstrapErr := readStreamBootstrap(attemptCtx, streamResult.Chunks)
		bootstrapErr = markUpstreamExecutionAttemptFromContext(attemptCtx, bootstrapErr)
		if bootstrapErr != nil {
			abandonAttempt(streamResult)
			if errCtx := execCtx.Err(); errCtx != nil {
				m.recordCanceledUpstreamCooldown(execCtx, auth, provider, resultModel, routeModel, execOpts, bootstrapErr, ephemeralResult, executionSeq)
				return nil, errCtx
			}
			if allowRetry && !ephemeralResult {
				alreadyTried := didRefreshOnUnauthorized
				refreshed, okRefresh := m.tryRefreshAfterUnauthorized(newUpstreamAttemptContext(execCtx), auth, bootstrapErr, alreadyTried, execOpts.Headers)
				if okRefresh {
					preRefreshBootstrapErr = bootstrapErr
					preRefreshBootstrapHeaders = streamResult.Headers
					auth = refreshed
					publishSelectedAuthMetadata(execOpts.Metadata, auth)
					didRefreshOnUnauthorized = true
					execCtx = m.newUpstreamAttemptContext(execCtx)
					execCtx = syncMetadataSessionToContext(execCtx, execOpts.Metadata)
					executionSeq = m.nextExecutionSeq()
					accessToken = authAccessTokenForRequest(execCtx, auth, execOpts.Headers)
					activeAuth.Store(auth)
					attemptCtx = startAttempt(execCtx)
					startRetry := time.Now()
					retryStream, retryErr := executor.ExecuteStream(attemptCtx, auth, execReq, execOpts)
					syncActiveAuth()
					retryErr = markUpstreamExecutionAttemptFromContext(attemptCtx, retryErr)
					retryStream, retryErr = validateStreamResult(retryStream, retryErr)
					retryErr = markUpstreamExecutionAttemptFromContext(attemptCtx, retryErr)
					if retryErr != nil {
						abandonAttempt(retryStream)
						if errCtx := execCtx.Err(); errCtx != nil {
							m.recordCanceledUpstreamCooldown(execCtx, auth, provider, resultModel, routeModel, execOpts, retryErr, ephemeralResult, executionSeq)
							return nil, errCtx
						}
						bootstrapErr = retryErr
						warnLogUpstreamFailure(execCtx, entry, provider, execModel, auth, time.Since(startRetry), bootstrapErr)
						streamResult = &cliproxyexecutor.StreamResult{}
					} else {
						streamResult = retryStream
						buffered, closed, bootstrapErr = readStreamBootstrap(attemptCtx, streamResult.Chunks)
						bootstrapErr = markUpstreamExecutionAttemptFromContext(attemptCtx, bootstrapErr)
						if bootstrapErr != nil {
							abandonAttempt(streamResult)
						}
						if errCtx := execCtx.Err(); errCtx != nil {
							m.recordCanceledUpstreamCooldown(execCtx, auth, provider, resultModel, routeModel, execOpts, bootstrapErr, ephemeralResult, executionSeq)
							abandonAttempt(streamResult)
							return nil, errCtx
						}
						if bootstrapErr != nil {
							warnLogUpstreamFailure(execCtx, entry, provider, execModel, auth, time.Since(startRetry), bootstrapErr)
						}
					}
				} else {
					warnLogUpstreamFailure(execCtx, entry, provider, execModel, auth, time.Since(startStream), bootstrapErr)
				}
			} else {
				warnLogUpstreamFailure(execCtx, entry, provider, execModel, auth, time.Since(startStream), bootstrapErr)
			}
		}
		if !ephemeralResult {
			if errCancel := claudeOAuthRequestCancellation(execCtx, auth, bootstrapErr); errCancel != nil {
				abandonAttempt(streamResult)
				return nil, errCancel
			}
		}
		if bootstrapErr != nil {
			abandonAttempt(streamResult)
			if !hasUpstreamExecutionAttempt(bootstrapErr) {
				if preRefreshBootstrapErr != nil {
					syncPreferredUpstreamError(&upstreamErr, newStreamBootstrapError(preRefreshBootstrapErr, preRefreshBootstrapHeaders))
				} else if preRefreshStreamErr != nil {
					syncPreferredUpstreamError(&upstreamErr, preRefreshStreamErr)
				}
			}
			action, okAction := matchRequestScopedErrorAction(auth, bootstrapErr, m.requestScopedErrorConfig(execCtx))
			if okAction {
				rerr := resultErrorFromError(bootstrapErr)
				result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, RouteModel: routeModel, Success: false, Error: rerr, Options: execOpts, AccessToken: accessToken, CredentialVersion: auth.CredentialVersion, RegistrationEpoch: auth.RegistrationEpoch, ExecutionSeq: executionSeq}
				result.RetryAfter = retryAfterFromError(bootstrapErr)
				if isCredentialScopedError(bootstrapErr) {
					result.CredentialScope = true
				}
				applyRequestScopedActionToResult(action, okAction, &result)
				m.recordExecutionResult(execCtx, result, auth, ephemeralResult)
				bootstrapErr = annotateTrustedRetryAfterByHint(bootstrapErr)
				if isRequestStopError(bootstrapErr) {
					headerErr := newStreamHeaderError(bootstrapErr, streamResult.Headers)
					syncPreferredUpstreamError(&upstreamErr, headerErr)
					return nil, headerErr
				}
				if isTOSViolationError(bootstrapErr) {
					headerErr := newStreamHeaderError(bootstrapErr, streamResult.Headers)
					syncPreferredUpstreamError(&upstreamErr, headerErr)
					if isRequestScopedStop(action, okAction) {
						return nil, wrapRequestStopError(headerErr)
					}
					return nil, headerErr
				}
				if isRequestScopedStop(action, okAction) {
					headerErr := newStreamHeaderError(bootstrapErr, streamResult.Headers)
					syncPreferredUpstreamError(&upstreamErr, headerErr)
					return nil, wrapRequestStopError(headerErr)
				}
				bootstrapErr = wrapRequestScopedContinueError(bootstrapErr)
				bootstrapErr = newStreamBootstrapError(bootstrapErr, streamResult.Headers)
				syncPreferredUpstreamError(&upstreamErr, bootstrapErr)
				if isRequestTerminatedError(bootstrapErr) {
					return nil, bootstrapErr
				}
				lastErr = bootstrapErr
				if result.CredentialScope {
					currentErr := bootstrapErr
					return nil, preferredExecutionAttemptError(currentErr, upstreamErr)
				}
				continue
			}
			if isTerminalRequestStopError(bootstrapErr) || isTOSViolationError(bootstrapErr) || isRequestInvalidError(bootstrapErr) || cliproxyexecutor.IsUpstreamWebsocketTurnError(bootstrapErr) {
				rerr := resultErrorFromError(bootstrapErr)
				result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, RouteModel: routeModel, Success: false, Error: rerr, Options: execOpts, AccessToken: accessToken, CredentialVersion: auth.CredentialVersion, RegistrationEpoch: auth.RegistrationEpoch, ExecutionSeq: executionSeq}
				result.RetryAfter = retryAfterFromError(bootstrapErr)
				if isCredentialScopedError(bootstrapErr) {
					result.CredentialScope = true
				}
				m.recordExecutionResult(execCtx, result, auth, ephemeralResult)
				bootstrapErr = annotateTrustedRetryAfterByHint(bootstrapErr)
				syncPreferredUpstreamError(&upstreamErr, bootstrapErr)
				return nil, newStreamHeaderError(bootstrapErr, streamResult.Headers)
			}
			if idx < len(execModels)-1 {
				rerr := resultErrorFromError(bootstrapErr)
				result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, RouteModel: routeModel, Success: false, Error: rerr, Options: execOpts, AccessToken: accessToken, CredentialVersion: auth.CredentialVersion, RegistrationEpoch: auth.RegistrationEpoch, ExecutionSeq: executionSeq}
				result.RetryAfter = retryAfterFromError(bootstrapErr)
				if isCredentialScopedError(bootstrapErr) {
					result.CredentialScope = true
				}
				m.recordExecutionResult(execCtx, result, auth, ephemeralResult)
				bootstrapErr = annotateTrustedRetryAfterByHint(bootstrapErr)
				bootstrapErr = newStreamBootstrapError(bootstrapErr, streamResult.Headers)
				syncPreferredUpstreamError(&upstreamErr, bootstrapErr)
				lastErr = bootstrapErr
				if result.CredentialScope {
					currentErr := bootstrapErr
					return nil, preferredExecutionAttemptError(currentErr, upstreamErr)
				}
				continue
			}
			rerr := resultErrorFromError(bootstrapErr)
			result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, RouteModel: routeModel, Success: false, Error: rerr, Options: execOpts, AccessToken: accessToken, CredentialVersion: auth.CredentialVersion, RegistrationEpoch: auth.RegistrationEpoch, ExecutionSeq: executionSeq}
			result.RetryAfter = retryAfterFromError(bootstrapErr)
			if isCredentialScopedError(bootstrapErr) {
				result.CredentialScope = true
			}
			m.recordExecutionResult(execCtx, result, auth, ephemeralResult)
			bootstrapErr = annotateTrustedRetryAfterByHint(bootstrapErr)
			bootstrapErr = newStreamBootstrapError(bootstrapErr, streamResult.Headers)
			syncPreferredUpstreamError(&upstreamErr, bootstrapErr)
			currentErr := bootstrapErr
			return nil, preferredExecutionAttemptError(currentErr, upstreamErr)
		}

		if closed || len(buffered) == 0 {
			cancelCurrentAttempt()
			if !closed && !attemptCleaned {
				attemptCleaned = true
				discardStreamChunks(streamResult.Chunks)
			}
			emptyErr := markUpstreamExecutionAttemptFromContext(attemptCtx, &Error{Code: "empty_stream", Message: "upstream stream closed before first payload", Retryable: true})
			currentErr := newStreamBootstrapError(emptyErr, streamResult.Headers)
			if hasUpstreamExecutionAttempt(emptyErr) {
				syncPreferredUpstreamError(&upstreamErr, currentErr)
			} else if preRefreshBootstrapErr != nil {
				syncPreferredUpstreamError(&upstreamErr, newStreamBootstrapError(preRefreshBootstrapErr, preRefreshBootstrapHeaders))
			} else if preRefreshStreamErr != nil {
				syncPreferredUpstreamError(&upstreamErr, preRefreshStreamErr)
			}
			warnLogUpstreamFailure(execCtx, entry, provider, execModel, auth, time.Since(startStream), emptyErr)
			result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, RouteModel: routeModel, Success: false, Error: resultErrorFromError(emptyErr), Options: execOpts, AccessToken: accessToken, CredentialVersion: auth.CredentialVersion, RegistrationEpoch: auth.RegistrationEpoch, ExecutionSeq: executionSeq}
			m.recordExecutionResult(execCtx, result, auth, ephemeralResult)
			if idx < len(execModels)-1 {
				lastErr = emptyErr
				continue
			}
			return nil, preferredExecutionAttemptError(currentErr, upstreamErr)
		}

		attemptAliasResult := resolveAttemptAliasResult(routing, auth, routeModel, execModel, aliasResult)
		handoffCancel := cancelAttempt
		cancelAttempt = nil
		return m.wrapStreamResultWithCancel(execCtx, auth.Clone(), provider, resultModel, routeModel, streamResult.Headers, buffered, streamResult.Chunks, attemptAliasResult, ephemeralResult, execOpts, executionSeq, handoffCancel), nil
	}
	if lastErr == nil {
		lastErr = &Error{Code: "auth_not_found", Message: "no upstream model available"}
	}
	return nil, preferredExecutionAttemptError(lastErr, upstreamErr)
}
