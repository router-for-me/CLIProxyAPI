package auth

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func (m *Manager) sameUpstreamRetryCount() int {
	if cfg := m.runtimeConfigSnapshot(); cfg != nil && cfg.SameUpstreamRetry > 0 {
		return cfg.SameUpstreamRetry
	}
	return 0
}

func (m *Manager) consumeSameUpstreamRetry(ctx context.Context, auth *Auth, err error, remaining *atomic.Int64) bool {
	if remaining.Load() <= 0 || !m.isSameUpstreamRetryable(ctx, auth, err) {
		return false
	}
	remaining.Add(-1)
	return true
}

func (m *Manager) isSameUpstreamRetryable(ctx context.Context, auth *Auth, err error) bool {
	if err == nil || ctx.Err() != nil || isRequestTerminatedError(err) || isRequestStopError(err) || isRequestInvalidError(err) || isCloudflareChallengeError(err) {
		return false
	}
	// Internal handshake deadlines can be retried while the caller context is live.
	if (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) && !isConfiguredTLSHandshakeTimeoutError(err) {
		return false
	}
	if _, matched := matchRequestScopedErrorAction(auth, err, m.runtimeConfigSnapshot()); matched {
		return false
	}
	status := statusCodeFromError(err)
	switch status {
	case http.StatusRequestTimeout, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, 520, 521, 522, 523, 524, 525, 526:
	default:
		if status != 0 || (!isTransientTransportError(err) && !isConnectionLifecycleError(err)) {
			return false
		}
	}
	return true
}

type sameUpstreamRetryKey struct {
	authID string
	model  string
}

func (m *Manager) sameUpstreamRetryBudget(budgets map[sameUpstreamRetryKey]*atomic.Int64, authID, model string, enabled bool) *atomic.Int64 {
	key := sameUpstreamRetryKey{authID: authID, model: model}
	if remaining := budgets[key]; remaining != nil {
		return remaining
	}
	remaining := &atomic.Int64{}
	if enabled {
		remaining.Store(int64(m.sameUpstreamRetryCount()))
	}
	budgets[key] = remaining
	return remaining
}

func (m *Manager) deferUpstreamFailures(ctx context.Context, auth *Auth, opts *cliproxyexecutor.Options, remaining *atomic.Int64) func() {
	if remaining.Load() <= 0 {
		return func() {}
	}
	handler := &sameUpstreamFailureHandler{active: true, retryable: func(err error) bool {
		return remaining.Load() > 0 && m.isSameUpstreamRetryable(ctx, auth, err)
	}}
	opts.UpstreamFailureHandler = handler
	return handler.Commit
}

type sameUpstreamFailureHandler struct {
	mu        sync.Mutex
	active    bool
	retryable func(error) bool
	pending   func()
}

func (h *sameUpstreamFailureHandler) Defer(err error, finish func()) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.active || !h.retryable(err) {
		return false
	}
	h.pending = finish
	return true
}

func (h *sameUpstreamFailureHandler) Recovered() {
	h.mu.Lock()
	h.pending = nil
	h.mu.Unlock()
}

func (h *sameUpstreamFailureHandler) Commit() {
	h.mu.Lock()
	h.active = false
	finish := h.pending
	h.pending = nil
	h.mu.Unlock()
	if finish != nil {
		finish()
	}
}

// executeWithSameUpstreamRetry defers result recording until the selected
// credential/model has recovered or exhausted its additional attempts.
func executeWithSameUpstreamRetry[T any](ctx context.Context, m *Manager, auth *Auth, remaining *atomic.Int64, execute func(context.Context) (T, error)) (T, context.Context, error) {
	for {
		response, errExecute := execute(ctx)
		errExecute = markUpstreamExecutionAttemptFromContext(ctx, errExecute)
		if !m.consumeSameUpstreamRetry(ctx, auth, errExecute, remaining) {
			return response, ctx, errExecute
		}
		ctx = newUpstreamAttemptContext(ctx)
	}
}

// readBootstrapWithSameUpstreamRetry never replays a stream after its first payload.
func (m *Manager) readBootstrapWithSameUpstreamRetry(ctx context.Context, auth *Auth, remaining *atomic.Int64, stream *cliproxyexecutor.StreamResult, execute func(context.Context) (*cliproxyexecutor.StreamResult, error)) (*cliproxyexecutor.StreamResult, []cliproxyexecutor.StreamChunk, bool, context.Context, error) {
	for {
		buffered, closed, errBootstrap := readStreamBootstrap(ctx, stream.Chunks)
		errBootstrap = markUpstreamExecutionAttemptFromContext(ctx, errBootstrap)
		if !m.consumeSameUpstreamRetry(ctx, auth, errBootstrap, remaining) {
			return stream, buffered, closed, ctx, errBootstrap
		}
		discardStreamChunks(stream.Chunks)
		ctx = newUpstreamAttemptContext(ctx)
		retryStream, retryCtx, errStream := executeWithSameUpstreamRetry(ctx, m, auth, remaining, execute)
		ctx = retryCtx
		retryStream, errStream = validateStreamResult(retryStream, errStream)
		if errStream != nil {
			return &cliproxyexecutor.StreamResult{}, nil, false, ctx, errStream
		}
		stream = retryStream
	}
}
