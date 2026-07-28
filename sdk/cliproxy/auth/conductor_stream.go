package auth

import (
	"context"
	"errors"
	"io"
	"math"
	"math/rand"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type streamRecoveryContextKey struct{}

type streamRecoveryState struct {
	policy         cliproxyexecutor.StreamRecoveryPolicy
	started        time.Time
	remaining      int
	releaseOnce    sync.Once
	release        func()
	holdUntilDrain bool
}

func (s *streamRecoveryState) releaseSlot() {
	if s == nil || s.release == nil {
		return
	}
	s.releaseOnce.Do(s.release)
}

func (m *Manager) acquireStreamRecovery(maxConcurrent int) bool {
	if m == nil || maxConcurrent <= 0 {
		return false
	}
	for {
		current := m.recoveryInFlight.Load()
		if current >= int32(maxConcurrent) {
			return false
		}
		if m.recoveryInFlight.CompareAndSwap(current, current+1) {
			return true
		}
	}
}

func streamRecoveryFromContext(ctx context.Context) *streamRecoveryState {
	if ctx == nil {
		return nil
	}
	state, _ := ctx.Value(streamRecoveryContextKey{}).(*streamRecoveryState)
	return state
}

func (s *streamRecoveryState) canRetry() bool {
	if s == nil || s.remaining <= 0 {
		return false
	}
	if s.policy.MaxRetryWindow > 0 && time.Since(s.started) >= s.policy.MaxRetryWindow {
		return false
	}
	s.remaining--
	return true
}

func streamRecoveryBackoff(ctx context.Context, policy cliproxyexecutor.StreamRecoveryPolicy, retry int) error {
	ceiling := policy.InitialBackoff
	for i := 1; i < retry && ceiling < policy.MaxBackoff; i++ {
		if ceiling > policy.MaxBackoff/2 {
			ceiling = policy.MaxBackoff
		} else {
			ceiling *= 2
		}
	}
	if ceiling <= 0 {
		return nil
	}
	var delay time.Duration
	if int64(ceiling) == math.MaxInt64 {
		delay = time.Duration(rand.Int63())
	} else {
		delay = time.Duration(rand.Int63n(int64(ceiling) + 1))
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func streamRecoveryReason(err error) string {
	if err == nil {
		return "none"
	}
	if statusCodeFromError(err) != 0 {
		return "http_status"
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return "unexpected_eof"
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return "transport"
	}
	return "stream_error"
}

func isEligibleStreamRecoveryError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	status := statusCodeFromError(err)
	switch status {
	case http.StatusRequestTimeout, http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout, 529:
		return true
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound,
		http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity, http.StatusTooManyRequests:
		return false
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Temporary()
}

func cloneStreamChunk(chunk cliproxyexecutor.StreamChunk) cliproxyexecutor.StreamChunk {
	chunk.Payload = append([]byte(nil), chunk.Payload...)
	return chunk
}

func closedStreamChunks() <-chan cliproxyexecutor.StreamChunk {
	ch := make(chan cliproxyexecutor.StreamChunk)
	close(ch)
	return ch
}

func prependStreamChunk(ctx context.Context, first cliproxyexecutor.StreamChunk, remaining <-chan cliproxyexecutor.StreamChunk) <-chan cliproxyexecutor.StreamChunk {
	out := make(chan cliproxyexecutor.StreamChunk, 1)
	out <- first
	go func() {
		defer close(out)
		for chunk := range remaining {
			select {
			case <-ctx.Done():
				discardStreamChunks(remaining)
				return
			case out <- chunk:
			}
		}
	}()
	return out
}

func bufferedStreamBytes(chunks []cliproxyexecutor.StreamChunk) int {
	total := 0
	for _, chunk := range chunks {
		total += len(chunk.Payload)
	}
	return total
}

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
	ch <- cliproxyexecutor.StreamChunk{Err: err}
	close(ch)
	return &cliproxyexecutor.StreamResult{
		Headers: cloneHTTPHeader(headers),
		Chunks:  ch,
	}
}

func readStreamBootstrap(ctx context.Context, ch <-chan cliproxyexecutor.StreamChunk) ([]cliproxyexecutor.StreamChunk, bool, error) {
	if ch == nil {
		return nil, true, nil
	}
	buffered := make([]cliproxyexecutor.StreamChunk, 0, 1)
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
			if len(buffered) > 0 {
				return nil, false, &Error{Code: "incomplete_stream", Message: "upstream stream closed after provisional framing", Retryable: true, HTTPStatus: http.StatusBadGateway}
			}
			return nil, true, nil
		}
		if chunk.Err != nil {
			return nil, false, chunk.Err
		}
		buffered = append(buffered, chunk)
		switch chunk.Commitment {
		case cliproxyexecutor.StreamCommitmentProvisional:
			continue
		case cliproxyexecutor.StreamCommitmentSemantic, cliproxyexecutor.StreamCommitmentTerminal:
			return buffered, false, nil
		default:
			if len(chunk.Payload) > 0 {
				return buffered, false, nil
			}
		}
	}
}

func collectRecoveryAttempt(ctx context.Context, streamResult *cliproxyexecutor.StreamResult, maxBufferBytes int) (buffered []cliproxyexecutor.StreamChunk, remaining <-chan cliproxyexecutor.StreamChunk, terminal bool, failOpenReason string, err error) {
	if streamResult == nil || streamResult.Chunks == nil {
		return nil, nil, false, "", &Error{Code: "empty_stream", Message: "upstream stream has no source", Retryable: true}
	}
	bufferedBytes := 0
	for {
		select {
		case <-ctx.Done():
			discardStreamChunks(streamResult.Chunks)
			return nil, nil, false, "", ctx.Err()
		case chunk, ok := <-streamResult.Chunks:
			if !ok {
				if terminal {
					return buffered, closedStreamChunks(), true, "", nil
				}
				return nil, nil, false, "", &Error{Code: requestScopedErrorCode, Message: "upstream stream closed before terminal completion", Retryable: true, HTTPStatus: http.StatusRequestTimeout}
			}
			if chunk.Err != nil {
				discardStreamChunks(streamResult.Chunks)
				return nil, nil, false, "", chunk.Err
			}
			if chunk.Commitment == cliproxyexecutor.StreamCommitmentUnknown && len(chunk.Payload) > 0 {
				return buffered, prependStreamChunk(ctx, chunk, streamResult.Chunks), false, "unknown_commitment", nil
			}
			if maxBufferBytes > 0 && len(chunk.Payload) > maxBufferBytes-bufferedBytes {
				return buffered, prependStreamChunk(ctx, chunk, streamResult.Chunks), false, "buffer_overflow", nil
			}
			cloned := cloneStreamChunk(chunk)
			buffered = append(buffered, cloned)
			bufferedBytes += len(cloned.Payload)
			if chunk.Commitment == cliproxyexecutor.StreamCommitmentTerminal {
				terminal = true
			}
		}
	}
}

func (m *Manager) executeStreamWithRecovery(ctx context.Context, executor ProviderExecutor, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, provider, model string) (*cliproxyexecutor.StreamResult, []cliproxyexecutor.StreamChunk, <-chan cliproxyexecutor.StreamChunk, error) {
	state := streamRecoveryFromContext(ctx)
	if state == nil {
		return nil, nil, nil, nil
	}
	attempt := 0
	log.WithFields(log.Fields{"provider": provider, "model": model, "reason": "enabled", "buffered_bytes": 0, "elapsed": time.Duration(0), "max_attempts": state.policy.Attempts + 1, "max_buffer_bytes": state.policy.MaxBufferBytes}).Debug("stream recovery started")
	for {
		attempt++
		streamResult, errStream := executor.ExecuteStream(ctx, auth, req, opts)
		if errStream == nil {
			var failOpenReason string
			var terminal bool
			var buffered []cliproxyexecutor.StreamChunk
			var remaining <-chan cliproxyexecutor.StreamChunk
			buffered, remaining, terminal, failOpenReason, errStream = collectRecoveryAttempt(ctx, streamResult, state.policy.MaxBufferBytes)
			if errStream == nil {
				if failOpenReason != "" {
					log.WithFields(log.Fields{"provider": provider, "model": model, "attempt": attempt, "reason": failOpenReason, "buffered_bytes": bufferedStreamBytes(buffered), "elapsed": time.Since(state.started)}).Debug("stream recovery failed open to ordinary streaming")
					return streamResult, buffered, remaining, nil
				}
				if terminal {
					state.holdUntilDrain = true
					log.WithFields(log.Fields{"provider": provider, "model": model, "attempt": attempt, "reason": "terminal_success", "buffered_bytes": bufferedStreamBytes(buffered), "elapsed": time.Since(state.started)}).Debug("stream recovery succeeded")
					return streamResult, buffered, remaining, nil
				}
			}
		}
		if errCtx := ctx.Err(); errCtx != nil {
			return nil, nil, nil, errCtx
		}
		if !isEligibleStreamRecoveryError(errStream) || !state.canRetry() {
			log.WithFields(log.Fields{"provider": provider, "model": model, "attempt": attempt, "reason": streamRecoveryReason(errStream), "status": statusCodeFromError(errStream), "buffered_bytes": 0, "elapsed": time.Since(state.started)}).Debug("stream recovery exhausted")
			return streamResult, nil, nil, errStream
		}
		log.WithFields(log.Fields{"provider": provider, "model": model, "attempt": attempt, "reason": streamRecoveryReason(errStream), "status": statusCodeFromError(errStream), "buffered_bytes": 0, "elapsed": time.Since(state.started)}).Debug("retrying recoverable stream")
		if errWait := streamRecoveryBackoff(ctx, state.policy, attempt); errWait != nil {
			return nil, nil, nil, errWait
		}
		if state.policy.MaxRetryWindow > 0 && time.Since(state.started) >= state.policy.MaxRetryWindow {
			log.WithFields(log.Fields{"provider": provider, "model": model, "attempt": attempt, "reason": "retry_window_exhausted", "status": statusCodeFromError(errStream), "buffered_bytes": 0, "elapsed": time.Since(state.started)}).Debug("stream recovery exhausted")
			return streamResult, nil, nil, errStream
		}
	}
}

func (m *Manager) wrapStreamResult(ctx context.Context, auth *Auth, provider, resultModel string, headers http.Header, buffered []cliproxyexecutor.StreamChunk, remaining <-chan cliproxyexecutor.StreamChunk, aliasResult OAuthModelAliasResult, ephemeralResult bool) *cliproxyexecutor.StreamResult {
	return m.wrapStreamResultWithDone(ctx, auth, provider, resultModel, headers, buffered, remaining, aliasResult, ephemeralResult, nil)
}

func (m *Manager) wrapStreamResultWithDone(ctx context.Context, auth *Auth, provider, resultModel string, headers http.Header, buffered []cliproxyexecutor.StreamChunk, remaining <-chan cliproxyexecutor.StreamChunk, aliasResult OAuthModelAliasResult, ephemeralResult bool, done func()) *cliproxyexecutor.StreamResult {
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		if done != nil {
			defer done()
		}
		var failed bool
		forward := true
		var rewriter *StreamRewriter
		if aliasResult.ForceMapping && strings.TrimSpace(aliasResult.OriginalAlias) != "" {
			rewriter = NewStreamRewriter(StreamRewriteOptions{RewriteModel: aliasResult.OriginalAlias})
		}
		emit := func(chunk cliproxyexecutor.StreamChunk) bool {
			if ctx != nil && ctx.Err() != nil {
				forward = false
				return false
			}
			if chunk.Err != nil && !failed {
				failed = true
				rerr := resultErrorFromError(chunk.Err)
				m.recordExecutionResult(ctx, Result{AuthID: auth.ID, Provider: provider, Model: resultModel, Success: false, Error: rerr}, auth, ephemeralResult)
			}
			if !forward {
				return false
			}
			if chunk.Err != nil {
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
				discardStreamChunks(remaining)
				return
			}
		}
		for chunk := range remaining {
			if ok := emit(chunk); !ok {
				discardStreamChunks(remaining)
				return
			}
		}
		if tail := finishForceMappedStreamChunks(rewriter); len(tail) > 0 {
			tailChunk := cliproxyexecutor.StreamChunk{Payload: tail}
			if !emit(tailChunk) {
				return
			}
		}
		if !failed && (ctx == nil || ctx.Err() == nil) {
			m.recordExecutionResult(ctx, Result{AuthID: auth.ID, Provider: provider, Model: resultModel, Success: true}, auth, ephemeralResult)
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: headers, Chunks: out}
}

func (m *Manager) executeStreamWithModelPool(ctx context.Context, executor ProviderExecutor, auth *Auth, provider string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, routeModel, executionModel string, execModels []string, pooled bool, aliasResult OAuthModelAliasResult, allowRetry bool, ephemeralResult bool) (*cliproxyexecutor.StreamResult, error) {
	if executor == nil {
		return nil, &Error{Code: "executor_not_found", Message: "executor not registered"}
	}
	ctx = contextWithRequestedModelAlias(ctx, opts, routeModel)
	var lastErr error
	didRefreshOnUnauthorized := false
	for idx, execModel := range execModels {
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
			return nil, errIntercept
		}
		if errCtx := ctx.Err(); errCtx != nil {
			return nil, errCtx
		}
		if recoveryState := streamRecoveryFromContext(ctx); recoveryState != nil {
			streamResult, buffered, remaining, errRecovery := m.executeStreamWithRecovery(ctx, executor, auth, execReq, execOpts, provider, resultModel)
			if errRecovery != nil && ctx.Err() == nil && allowRetry {
				if refreshed, okRefresh := m.tryRefreshAfterUnauthorized(ctx, auth, errRecovery, didRefreshOnUnauthorized); okRefresh {
					auth = refreshed
					didRefreshOnUnauthorized = true
					streamResult, buffered, remaining, errRecovery = m.executeStreamWithRecovery(ctx, executor, auth, execReq, execOpts, provider, resultModel)
				}
			}
			if errRecovery != nil {
				if errCtx := ctx.Err(); errCtx != nil {
					return nil, errCtx
				}
				rerr := resultErrorFromError(errRecovery)
				result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, Success: false, Error: rerr}
				result.RetryAfter = retryAfterFromError(errRecovery)
				m.recordExecutionResult(ctx, result, auth, ephemeralResult)
				if isRequestScopedError(errRecovery) || isRequestInvalidError(errRecovery) {
					return nil, errRecovery
				}
				lastErr = errRecovery
				continue
			}
			if recoveryState.holdUntilDrain {
				return m.wrapStreamResultWithDone(ctx, auth.Clone(), provider, resultModel, streamResult.Headers, buffered, remaining, aliasResult, ephemeralResult, recoveryState.releaseSlot), nil
			}
			return m.wrapStreamResult(ctx, auth.Clone(), provider, resultModel, streamResult.Headers, buffered, remaining, aliasResult, ephemeralResult), nil
		}
		streamResult, errStream := executor.ExecuteStream(ctx, auth, execReq, execOpts)
		if errStream != nil {
			if errCtx := ctx.Err(); errCtx != nil {
				return nil, errCtx
			}
			if allowRetry {
				if refreshed, okRefresh := m.tryRefreshAfterUnauthorized(ctx, auth, errStream, didRefreshOnUnauthorized); okRefresh {
					auth = refreshed
					didRefreshOnUnauthorized = true
					streamResult, errStream = executor.ExecuteStream(ctx, auth, execReq, execOpts)
					if errStream != nil {
						if errCtx := ctx.Err(); errCtx != nil {
							return nil, errCtx
						}
					}
				}
			}
		}
		if errStream == nil && (streamResult == nil || streamResult.Chunks == nil) {
			errStream = &Error{Code: "empty_stream", Message: "upstream stream has no source", Retryable: true}
		}
		if errStream != nil {
			rerr := resultErrorFromError(errStream)
			result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, Success: false, Error: rerr}
			result.RetryAfter = retryAfterFromError(errStream)
			m.recordExecutionResult(ctx, result, auth, ephemeralResult)
			if isRequestInvalidError(errStream) {
				return nil, errStream
			}
			lastErr = errStream
			continue
		}

		buffered, closed, bootstrapErr := readStreamBootstrap(ctx, streamResult.Chunks)
		if bootstrapErr != nil {
			if errCtx := ctx.Err(); errCtx != nil {
				discardStreamChunks(streamResult.Chunks)
				return nil, errCtx
			}
			if allowRetry {
				if refreshed, okRefresh := m.tryRefreshAfterUnauthorized(ctx, auth, bootstrapErr, didRefreshOnUnauthorized); okRefresh {
					discardStreamChunks(streamResult.Chunks)
					auth = refreshed
					didRefreshOnUnauthorized = true
					retryStream, retryErr := executor.ExecuteStream(ctx, auth, execReq, execOpts)
					if retryErr != nil {
						if errCtx := ctx.Err(); errCtx != nil {
							return nil, errCtx
						}
						bootstrapErr = retryErr
						streamResult = &cliproxyexecutor.StreamResult{}
					} else {
						streamResult = retryStream
						buffered, closed, bootstrapErr = readStreamBootstrap(ctx, streamResult.Chunks)
					}
				}
			}
		}
		if bootstrapErr != nil {
			if isRequestInvalidError(bootstrapErr) {
				rerr := resultErrorFromError(bootstrapErr)
				result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, Success: false, Error: rerr}
				result.RetryAfter = retryAfterFromError(bootstrapErr)
				m.recordExecutionResult(ctx, result, auth, ephemeralResult)
				discardStreamChunks(streamResult.Chunks)
				return nil, bootstrapErr
			}
			if idx < len(execModels)-1 {
				rerr := resultErrorFromError(bootstrapErr)
				result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, Success: false, Error: rerr}
				result.RetryAfter = retryAfterFromError(bootstrapErr)
				m.recordExecutionResult(ctx, result, auth, ephemeralResult)
				discardStreamChunks(streamResult.Chunks)
				lastErr = bootstrapErr
				continue
			}
			rerr := resultErrorFromError(bootstrapErr)
			result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, Success: false, Error: rerr}
			result.RetryAfter = retryAfterFromError(bootstrapErr)
			m.recordExecutionResult(ctx, result, auth, ephemeralResult)
			discardStreamChunks(streamResult.Chunks)
			return nil, newStreamBootstrapError(bootstrapErr, streamResult.Headers)
		}

		if closed && len(buffered) == 0 {
			emptyErr := &Error{Code: "empty_stream", Message: "upstream stream closed before first payload", Retryable: true}
			result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, Success: false, Error: emptyErr}
			m.recordExecutionResult(ctx, result, auth, ephemeralResult)
			if idx < len(execModels)-1 {
				lastErr = emptyErr
				continue
			}
			return nil, newStreamBootstrapError(emptyErr, streamResult.Headers)
		}

		remaining := streamResult.Chunks
		if closed {
			closedCh := make(chan cliproxyexecutor.StreamChunk)
			close(closedCh)
			remaining = closedCh
		}
		return m.wrapStreamResult(ctx, auth.Clone(), provider, resultModel, streamResult.Headers, buffered, remaining, aliasResult, ephemeralResult), nil
	}
	if lastErr == nil {
		lastErr = &Error{Code: "auth_not_found", Message: "no upstream model available"}
	}
	return nil, lastErr
}
