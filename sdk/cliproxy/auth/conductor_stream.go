package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func newTTFTTimeoutError(timeout time.Duration) error {
	return &Error{
		Code:       "stream_first_chunk_timeout",
		Message:    fmt.Sprintf("time to first chunk timeout after %v", timeout),
		HTTPStatus: 504,
		Retryable:  true,
	}
}

func (m *Manager) streamFirstChunkTimeout(opts cliproxyexecutor.Options) time.Duration {
	if opts.Metadata != nil {
		if ms, ok := opts.Metadata["stream_connect_timeout_ms"].(int); ok {
			if ms <= 0 {
				return 0
			}
			return time.Duration(ms) * time.Millisecond
		}
		if ms, ok := opts.Metadata["stream_first_chunk_timeout_ms"].(int); ok {
			if ms <= 0 {
				return 0
			}
			return time.Duration(ms) * time.Millisecond
		}
	}
	if m == nil {
		return 0
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil {
		return 0
	}
	if cfg.Streaming.StreamConnectTimeoutSeconds > 0 {
		return time.Duration(cfg.Streaming.StreamConnectTimeoutSeconds) * time.Second
	}
	if cfg.Streaming.StreamFirstChunkTimeoutSeconds > 0 {
		return time.Duration(cfg.Streaming.StreamFirstChunkTimeoutSeconds) * time.Second
	}
	return 0
}

var streamDrainTimeout = 5 * time.Second

func discardStreamChunks(ctx context.Context, ch <-chan cliproxyexecutor.StreamChunk) <-chan struct{} {
	done := make(chan struct{})
	if ch == nil {
		close(done)
		return done
	}
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		defer close(done)
		timer := time.NewTimer(streamDrainTimeout)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				return
			case _, ok := <-ch:
				if !ok {
					return
				}
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(streamDrainTimeout)
			}
		}
	}()
	return done
}

type streamBootstrapError struct {
	cause   error
	headers http.Header
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
	return &streamBootstrapError{
		cause:   err,
		headers: cloneHTTPHeader(headers),
	}
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

func streamErrorResult(headers http.Header, err error) *cliproxyexecutor.StreamResult {
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Err: sanitizeErrorTextFields(err)}
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

func readStreamBootstrap(ctx context.Context, ch <-chan cliproxyexecutor.StreamChunk, requestPayloads ...[]byte) ([]cliproxyexecutor.StreamChunk, bool, error) {
	if ch == nil {
		return nil, true, nil
	}
	buffered := make([]cliproxyexecutor.StreamChunk, 0, 1)
	var bootstrap streamBootstrapState
	for _, p := range requestPayloads {
		if n := ExtractExpectedChoices(p); n > 1 {
			bootstrap.setExpectedChoices(n)
			break
		}
	}
	for {
		var (
			chunk cliproxyexecutor.StreamChunk
			ok    bool
		)
		if ctx != nil {
			select {
			case <-ctx.Done():
				return nil, false, ctx.Err()
			case chunk, ok = <-ch:
			}
		} else {
			chunk, ok = <-ch
		}
		if !ok {
			// A final frame without its blank-line delimiter is only parsed by
			// finish(): without it a provider error carried by that last frame stays
			// pending and the stream looks like a clean close.
			bootstrap.finish()
			if err := bootstrap.streamError(); err != nil && !bootstrap.hasMeaningfulOutput() {
				return nil, false, err
			}
			return buffered, true, nil
		}
		if chunk.Err != nil {
			if bootstrap.hasMeaningfulOutput() {
				buffered = append(buffered, chunk)
				return buffered, false, nil
			}
			return nil, false, chunk.Err
		}
		buffered = append(buffered, chunk)
		if bootstrap.observe(chunk.Payload) {
			return buffered, false, nil
		}
		if err := bootstrap.streamError(); err != nil {
			if bootstrap.hasMeaningfulOutput() {
				return buffered, false, nil
			}
			return nil, false, err
		}
		if bootstrap.isTerminalEmpty() {
			return buffered, true, nil
		}
	}
}

func (m *Manager) wrapStreamResult(ctx context.Context, auth *Auth, provider, resultModel string, headers http.Header, buffered []cliproxyexecutor.StreamChunk, remaining <-chan cliproxyexecutor.StreamChunk, aliasResult OAuthModelAliasResult, ephemeralResult bool, opts cliproxyexecutor.Options, cleanups ...func()) *cliproxyexecutor.StreamResult {
	out := make(chan cliproxyexecutor.StreamChunk)
	streamStart := time.Now()
	go func() {
		defer close(out)
		for _, cleanup := range cleanups {
			if cleanup != nil {
				defer cleanup()
			}
		}
		var failed bool
		var errorDetector streamPayloadErrorDetector
		forward := true
		var rewriter *StreamRewriter
		if aliasResult.ForceMapping && strings.TrimSpace(aliasResult.OriginalAlias) != "" {
			rewriter = NewStreamRewriter(StreamRewriteOptions{RewriteModel: aliasResult.OriginalAlias})
		}
		emit := func(chunk cliproxyexecutor.StreamChunk) bool {
			if chunk.Err != nil && !failed {
				failed = true
				chunk.Err = sanitizeErrorTextFields(chunk.Err)
				entry := logEntryWithRequestID(ctx)
				warnLogUpstreamFailure(ctx, entry, provider, resultModel, auth, time.Since(streamStart), chunk.Err)
				rerr := resultErrorFromError(chunk.Err)
				action, okAction := matchRequestScopedErrorAction(auth, chunk.Err, m.runtimeConfigSnapshot())
				result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, Success: false, Error: rerr, Options: opts}
				applyRequestScopedActionToResult(action, okAction, &result)
				m.recordExecutionResult(ctx, result, auth, ephemeralResult)
			}
			if len(chunk.Payload) > 0 {
				if !failed {
					if streamErr := errorDetector.Observe(chunk.Payload); streamErr != nil {
						failed = true
						streamErr = sanitizeErrorTextFields(streamErr).(*Error)
						entry := logEntryWithRequestID(ctx)
						warnLogUpstreamFailure(ctx, entry, provider, resultModel, auth, time.Since(streamStart), streamErr)
						rerr := resultErrorFromError(streamErr)
						action, okAction := matchRequestScopedErrorAction(auth, streamErr, m.runtimeConfigSnapshot())
						result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, Success: false, Error: rerr, Options: opts}
						applyRequestScopedActionToResult(action, okAction, &result)
						m.recordExecutionResult(ctx, result, auth, ephemeralResult)
					}
				}
			}
			if !forward {
				return false
			}
			if chunk.Err != nil {
				if failed {
					chunk.Payload = redactStreamPayload(chunk.Payload)
				}
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
			if len(chunk.Payload) == 0 {
				return true
			}
			payload := rewriteForceMappedStreamChunk(rewriter, chunk.Payload)
			if len(payload) == 0 {
				return true
			}
			if failed {
				payload = redactStreamPayload(payload)
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
			if ok := emit(chunk); !ok {
				discardStreamChunks(ctx, remaining)
				return
			}
		}
		for chunk := range remaining {
			if ok := emit(chunk); !ok {
				discardStreamChunks(ctx, remaining)
				return
			}
		}
		if tail := finishForceMappedStreamChunks(rewriter); len(tail) > 0 {
			tailChunk := cliproxyexecutor.StreamChunk{Payload: tail}
			if !emit(tailChunk) {
				return
			}
		}
		if !failed {
			if streamErr := errorDetector.Finish(); streamErr != nil {
				failed = true
				streamErr = sanitizeErrorTextFields(streamErr).(*Error)
				entry := logEntryWithRequestID(ctx)
				warnLogUpstreamFailure(ctx, entry, provider, resultModel, auth, time.Since(streamStart), streamErr)
				rerr := resultErrorFromError(streamErr)
				action, okAction := matchRequestScopedErrorAction(auth, streamErr, m.runtimeConfigSnapshot())
				result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, Success: false, Error: rerr, Options: opts}
				applyRequestScopedActionToResult(action, okAction, &result)
				m.recordExecutionResult(ctx, result, auth, ephemeralResult)
			}
		}
		if !failed && (ephemeralResult || claudeOAuthRequestCancellation(ctx, auth, nil) == nil) {
			m.recordExecutionResult(ctx, Result{AuthID: auth.ID, Provider: provider, Model: resultModel, Success: true, Options: opts}, auth, ephemeralResult)
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: headers, Chunks: out}
}

func (m *Manager) replaceHomeExecutionLifecycleAuth(lifecycle cliproxyexecutor.ExecutionLifecycle, auth *Auth) {
	selection, ok := lifecycle.(*HomeDispatchSelection)
	if !ok || selection == nil {
		return
	}
	m.replaceHomeSelectionAuth(selection, auth)
}

func (m *Manager) executeStreamWithModelPool(ctx context.Context, executor ProviderExecutor, auth *Auth, provider string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, routeModel, executionModel string, execModels []string, pooled bool, aliasResult OAuthModelAliasResult, routing *apiKeyModelRoutingSnapshot, allowRetry bool, ephemeralResult bool, unauthorizedRefreshTried map[string]struct{}) (*cliproxyexecutor.StreamResult, error) {
	if executor == nil {
		return nil, &Error{Code: "executor_not_found", Message: "executor not registered"}
	}
	ctx = contextWithRequestedModelAlias(ctx, opts, routeModel)
	var lastErr error
	didRefreshOnUnauthorized := false
	if auth != nil && unauthorizedRefreshTried != nil {
		_, didRefreshOnUnauthorized = unauthorizedRefreshTried[auth.ID]
	}
	for idx, execModel := range execModels {
		ctx = newUpstreamAttemptContext(ctx)
		ttftTimeout := m.streamFirstChunkTimeout(opts)
		attemptCtx, cancelAttempt := context.WithCancel(ctx)
		var timer *time.Timer
		var timedOut atomic.Bool
		var attemptMu sync.Mutex
		var attemptSeq uint64
		var stopTTFT func()

		checkTTFTErr := func(err error) error {
			if timedOut.Load() {
				return newTTFTTimeoutError(ttftTimeout)
			}
			return err
		}

		// Arm the TTFT timer only after local interception and request
		// preparation: the budget measures connection establishment, so a slow
		// after-auth interceptor must not cancel the attempt before any
		// upstream request was even made. AGENTS.md:58 permits timeouts only
		// until the upstream connection is established, including CONNECT/TLS
		// setup for HTTPS proxying, so the timer stops at GotConn.
		armTTFT := func() {
			attemptMu.Lock()
			if timer != nil {
				timer.Stop()
				timer = nil
			}
			var once sync.Once
			stopTTFT = func() {
				once.Do(func() {
					attemptMu.Lock()
					if timer != nil {
						timer.Stop()
						timer = nil
					}
					attemptSeq++
					attemptMu.Unlock()
				})
			}
			if ttftTimeout > 0 {
				currentSeq := attemptSeq
				currentCancel := cancelAttempt
				timer = time.AfterFunc(ttftTimeout, func() {
					attemptMu.Lock()
					defer attemptMu.Unlock()
					if currentSeq != attemptSeq {
						return
					}
					timedOut.Store(true)
					currentCancel()
				})
				trace := &httptrace.ClientTrace{
					GotConn: func(connInfo httptrace.GotConnInfo) {
						stopTTFT()
					},
				}
				attemptCtx = httptrace.WithClientTrace(attemptCtx, trace)
			}
			attemptMu.Unlock()
		}
		stopTTFT = func() {}

		resultModel := m.stateModelForExecution(auth, routeModel, execModel, pooled)
		execReq := req
		execReq.Model = execModel
		if executionModel != "" {
			execReq.Model = executionModel
		}
		execOpts := opts
		var errIntercept error
		execReq, execOpts, errIntercept = applyRequestAfterAuthInterceptor(ctx, executor, provider, execReq, execOpts, requestedModelAliasFromOptions(execOpts, routeModel))
		if errIntercept != nil {
			stopTTFT()
			cancelAttempt()
			return nil, errIntercept
		}
		if executionModel == "" {
			execReq = attachResolvedAPIKeyModelInfo(routing, execReq, auth, routeModel, execModel)
		}
		if errCtx := ctx.Err(); errCtx != nil {
			stopTTFT()
			cancelAttempt()
			return nil, errCtx
		}
		armTTFT()
		// The unauthorized-refresh retries below re-execute behind a credential
		// refresh, which may consume the whole TTFT budget (or fire the timer
		// and cancel attemptCtx). Restart the first-chunk window on a fresh
		// attempt context so the refreshed upstream request gets a full budget.
		restartAttempt := func() {
			stopTTFT()
			attemptMu.Lock()
			cancelAttempt()
			attemptCtx, cancelAttempt = context.WithCancel(ctx)
			timedOut.Store(false)
			attemptMu.Unlock()
			armTTFT()
		}
		entry := logEntryWithRequestID(ctx)
		startStream := time.Now()
		streamResult, errStream := executor.ExecuteStream(attemptCtx, auth, execReq, execOpts)
		durationStream := time.Since(startStream)
		if errStream != nil {
			if errCtx := ctx.Err(); errCtx != nil {
				stopTTFT()
				cancelAttempt()
				return nil, errCtx
			}
			errStream = checkTTFTErr(errStream)
			if allowRetry {
				stopTTFT()
				alreadyTried := didRefreshOnUnauthorized
				willAttemptHomeRefresh := ephemeralResult && !alreadyTried && auth != nil && auth.AuthKind() == AuthKindOAuth && isUnauthorizedError(errStream)
				refreshCtx := newUpstreamAttemptContext(ctx)
				refreshed, okRefresh, errRefresh := m.tryRefreshExecutionAuthAfterUnauthorized(refreshCtx, executor, auth, errStream, alreadyTried, ephemeralResult)
				if willAttemptHomeRefresh {
					didRefreshOnUnauthorized = true
					if unauthorizedRefreshTried != nil {
						unauthorizedRefreshTried[auth.ID] = struct{}{}
					}
				}
				if errRefresh != nil {
					errStream = errRefresh
					warnLogUpstreamFailure(ctx, entry, provider, execModel, auth, durationStream, errStream)
				} else if okRefresh {
					auth = refreshed
					m.replaceHomeExecutionLifecycleAuth(execOpts.ExecutionLifecycle, auth)
					publishSelectedAuthMetadata(execOpts.Metadata, auth)
					didRefreshOnUnauthorized = true
					ctx = newUpstreamAttemptContext(ctx)
					restartAttempt()
					if streamResult != nil {
						discardStreamChunks(ctx, streamResult.Chunks)
					}
					startRetry := time.Now()
					streamResult, errStream = executor.ExecuteStream(attemptCtx, auth, execReq, execOpts)
					durationRetry := time.Since(startRetry)
					errStream = checkTTFTErr(errStream)
					if errStream != nil {
						warnLogUpstreamFailure(ctx, entry, provider, execModel, auth, durationRetry, errStream)
						if errCtx := ctx.Err(); errCtx != nil {
							stopTTFT()
							cancelAttempt()
							if streamResult != nil {
								discardStreamChunks(ctx, streamResult.Chunks)
							}
							return nil, errCtx
						}
					}
				} else {
					warnLogUpstreamFailure(ctx, entry, provider, execModel, auth, durationStream, errStream)
				}
			} else {
				warnLogUpstreamFailure(ctx, entry, provider, execModel, auth, durationStream, errStream)
			}
		}
		if !ephemeralResult {
			if errCancel := claudeOAuthRequestCancellation(ctx, auth, errStream); errCancel != nil {
				stopTTFT()
				cancelAttempt()
				if streamResult != nil {
					discardStreamChunks(ctx, streamResult.Chunks)
				}
				return nil, errCancel
			}
		}
		streamResult, errStream = validateStreamResult(streamResult, errStream)
		if errStream != nil {
			stopTTFT()
			cancelAttempt()
			if streamResult != nil {
				discardStreamChunks(ctx, streamResult.Chunks)
			}
			errStream = checkTTFTErr(errStream)
			errStream = sanitizeErrorTextFields(errStream)
			rerr := resultErrorFromError(errStream)
			action, okAction := matchRequestScopedErrorAction(auth, errStream, m.runtimeConfigSnapshot())
			result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, Success: false, Error: rerr, Options: execOpts}
			result.RetryAfter = retryAfterFromError(errStream)
			if isCredentialScopedError(errStream) {
				result.CredentialScope = true
			}
			applyRequestScopedActionToResult(action, okAction, &result)
			m.recordExecutionResult(ctx, result, auth, ephemeralResult)
			if okAction {
				if isRequestScopedStop(action, okAction) {
					return nil, wrapRequestStopError(errStream)
				}
				lastErr = errStream
				if result.CredentialScope {
					return nil, errStream
				}
				continue
			}
			if isRequestInvalidError(errStream) {
				return nil, errStream
			}
			lastErr = errStream
			if result.CredentialScope {
				return nil, errStream
			}
			continue
		}
		stopTTFT()

		buffered, closed, bootstrapErr := readStreamBootstrap(attemptCtx, streamResult.Chunks, execReq.Payload, execOpts.OriginalRequest)
		if bootstrapErr != nil {
			if errCtx := ctx.Err(); errCtx != nil {
				stopTTFT()
				cancelAttempt()
				discardStreamChunks(ctx, streamResult.Chunks)
				return nil, errCtx
			}
			bootstrapErr = checkTTFTErr(bootstrapErr)
			if allowRetry {
				stopTTFT()
				alreadyTried := didRefreshOnUnauthorized
				willAttemptHomeRefresh := ephemeralResult && !alreadyTried && auth != nil && auth.AuthKind() == AuthKindOAuth && isUnauthorizedError(bootstrapErr)
				refreshCtx := newUpstreamAttemptContext(ctx)
				refreshed, okRefresh, errRefresh := m.tryRefreshExecutionAuthAfterUnauthorized(refreshCtx, executor, auth, bootstrapErr, alreadyTried, ephemeralResult)
				if willAttemptHomeRefresh {
					didRefreshOnUnauthorized = true
					if unauthorizedRefreshTried != nil {
						unauthorizedRefreshTried[auth.ID] = struct{}{}
					}
				}
				if errRefresh != nil {
					discardStreamChunks(ctx, streamResult.Chunks)
					bootstrapErr = errRefresh
					warnLogUpstreamFailure(ctx, entry, provider, execModel, auth, time.Since(startStream), bootstrapErr)
					streamResult = &cliproxyexecutor.StreamResult{}
				} else if okRefresh {
					discardStreamChunks(ctx, streamResult.Chunks)
					auth = refreshed
					m.replaceHomeExecutionLifecycleAuth(execOpts.ExecutionLifecycle, auth)
					publishSelectedAuthMetadata(execOpts.Metadata, auth)
					didRefreshOnUnauthorized = true
					ctx = newUpstreamAttemptContext(ctx)
					restartAttempt()
					startRetry := time.Now()
					retryStream, retryErr := executor.ExecuteStream(attemptCtx, auth, execReq, execOpts)
					retryStream, retryErr = validateStreamResult(retryStream, retryErr)
					stopTTFT()
					retryErr = checkTTFTErr(retryErr)
					if retryErr != nil {
						if retryStream != nil {
							discardStreamChunks(ctx, retryStream.Chunks)
						}
						if errCtx := ctx.Err(); errCtx != nil {
							stopTTFT()
							cancelAttempt()
							return nil, errCtx
						}
						bootstrapErr = retryErr
						warnLogUpstreamFailure(ctx, entry, provider, execModel, auth, time.Since(startRetry), bootstrapErr)
						streamResult = &cliproxyexecutor.StreamResult{}
					} else {
						streamResult = retryStream
						buffered, closed, bootstrapErr = readStreamBootstrap(attemptCtx, streamResult.Chunks, execReq.Payload, execOpts.OriginalRequest)
						bootstrapErr = checkTTFTErr(bootstrapErr)
						if bootstrapErr != nil {
							warnLogUpstreamFailure(ctx, entry, provider, execModel, auth, time.Since(startRetry), bootstrapErr)
						}
					}
				} else {
					warnLogUpstreamFailure(ctx, entry, provider, execModel, auth, time.Since(startStream), bootstrapErr)
				}
			} else {
				warnLogUpstreamFailure(ctx, entry, provider, execModel, auth, time.Since(startStream), bootstrapErr)
			}
		}
		if !ephemeralResult {
			if errCancel := claudeOAuthRequestCancellation(ctx, auth, bootstrapErr); errCancel != nil {
				stopTTFT()
				cancelAttempt()
				discardStreamChunks(ctx, streamResult.Chunks)
				return nil, errCancel
			}
		}
		if bootstrapErr != nil {
			stopTTFT()
			cancelAttempt()
			bootstrapErr = checkTTFTErr(bootstrapErr)
			bootstrapErr = sanitizeErrorTextFields(bootstrapErr)
			action, okAction := matchRequestScopedErrorAction(auth, bootstrapErr, m.runtimeConfigSnapshot())
			if okAction {
				rerr := resultErrorFromError(bootstrapErr)
				result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, Success: false, Error: rerr, Options: execOpts}
				result.RetryAfter = retryAfterFromError(bootstrapErr)
				if isCredentialScopedError(bootstrapErr) {
					result.CredentialScope = true
				}
				applyRequestScopedActionToResult(action, okAction, &result)
				m.recordExecutionResult(ctx, result, auth, ephemeralResult)
				discardStreamChunks(ctx, streamResult.Chunks)
				if isRequestScopedStop(action, okAction) {
					return nil, wrapRequestStopError(bootstrapErr)
				}
				lastErr = bootstrapErr
				if result.CredentialScope {
					return nil, newStreamBootstrapError(bootstrapErr, streamResult.Headers)
				}
				continue
			}
			if isRequestInvalidError(bootstrapErr) {
				rerr := resultErrorFromError(bootstrapErr)
				result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, Success: false, Error: rerr, Options: execOpts}
				result.RetryAfter = retryAfterFromError(bootstrapErr)
				if isCredentialScopedError(bootstrapErr) {
					result.CredentialScope = true
				}
				m.recordExecutionResult(ctx, result, auth, ephemeralResult)
				discardStreamChunks(ctx, streamResult.Chunks)
				return nil, bootstrapErr
			}
			if idx < len(execModels)-1 {
				rerr := resultErrorFromError(bootstrapErr)
				result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, Success: false, Error: rerr, Options: execOpts}
				result.RetryAfter = retryAfterFromError(bootstrapErr)
				if isCredentialScopedError(bootstrapErr) {
					result.CredentialScope = true
				}
				m.recordExecutionResult(ctx, result, auth, ephemeralResult)
				discardStreamChunks(ctx, streamResult.Chunks)
				lastErr = bootstrapErr
				if result.CredentialScope {
					return nil, newStreamBootstrapError(bootstrapErr, streamResult.Headers)
				}
				continue
			}
			rerr := resultErrorFromError(bootstrapErr)
			result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, Success: false, Error: rerr, Options: execOpts}
			result.RetryAfter = retryAfterFromError(bootstrapErr)
			if isCredentialScopedError(bootstrapErr) {
				result.CredentialScope = true
			}
			m.recordExecutionResult(ctx, result, auth, ephemeralResult)
			discardStreamChunks(ctx, streamResult.Chunks)
			return nil, newStreamBootstrapError(bootstrapErr, streamResult.Headers)
		}

		payloadBytes := 0
		for _, chunk := range buffered {
			payloadBytes += len(chunk.Payload)
		}
		// Determine emptiness by buffered payload bytes, not chunk count:
		// zero-payload chunks are dropped downstream by wrapStreamResult, so a
		// stream of only such chunks would surface as a successful empty
		// completion without failover.
		if closed && (payloadBytes == 0 || isEmptyCompletion(buffered)) {
			stopTTFT()
			cancelAttempt()
			emptyErr := errEmptyCompletion
			if payloadBytes == 0 {
				emptyErr = &Error{Code: "empty_stream", Message: "upstream stream closed before first payload", Retryable: true}
			}
			warnLogUpstreamFailure(ctx, entry, provider, execModel, auth, time.Since(startStream), emptyErr)
			result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, Success: false, Error: emptyErr, Options: execOpts}
			m.recordExecutionResult(ctx, result, auth, ephemeralResult)
			discardStreamChunks(ctx, streamResult.Chunks)
			if idx < len(execModels)-1 {
				lastErr = emptyErr
				continue
			}
			return nil, newStreamBootstrapError(emptyErr, streamResult.Headers)
		}

		stopTTFT()

		remaining := streamResult.Chunks
		if closed {
			discardStreamChunks(ctx, streamResult.Chunks)
			closedCh := make(chan cliproxyexecutor.StreamChunk)
			close(closedCh)
			remaining = closedCh
		}
		attemptAliasResult := resolveAttemptAliasResult(routing, auth, routeModel, execModel, aliasResult)
		return m.wrapStreamResult(ctx, auth.Clone(), provider, resultModel, streamResult.Headers, buffered, remaining, attemptAliasResult, ephemeralResult, execOpts, cancelAttempt), nil
	}
	if lastErr == nil {
		lastErr = &Error{Code: "auth_not_found", Message: "no upstream model available"}
	}
	return nil, lastErr
}
