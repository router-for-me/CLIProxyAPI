package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"golang.org/x/net/context"
)

const maxStreamingBootstrapTimeout = 10 * time.Minute

// StreamingBootstrapTimeout returns the configured maximum wait for the first
// upstream stream payload. Zero disables the timeout.
func StreamingBootstrapTimeout(cfg *config.SDKConfig) time.Duration {
	if cfg == nil || cfg.Streaming.BootstrapTimeoutSeconds <= 0 {
		return 0
	}
	seconds := cfg.Streaming.BootstrapTimeoutSeconds
	if seconds > int(maxStreamingBootstrapTimeout/time.Second) {
		return maxStreamingBootstrapTimeout
	}
	return time.Duration(seconds) * time.Second
}

type streamBootstrapTimeoutError struct {
	timeout time.Duration
}

func (e *streamBootstrapTimeoutError) Error() string {
	return fmt.Sprintf("upstream stream produced no payload within %s", e.timeout)
}

func (e *streamBootstrapTimeoutError) Unwrap() error   { return context.DeadlineExceeded }
func (e *streamBootstrapTimeoutError) StatusCode() int { return http.StatusGatewayTimeout }

// bootstrapAttemptContext applies a deadline only until disarm is called. Once
// the first upstream payload has been observed, the context keeps following the
// parent cancellation without imposing a deadline on the remainder of the stream.
type bootstrapAttemptContext struct {
	parent   context.Context
	deadline time.Time
	done     chan struct{}
	timer    *time.Timer

	mu       sync.RWMutex
	err      error
	disarmed bool
}

func newBootstrapAttemptContext(parent context.Context, timeout time.Duration) *bootstrapAttemptContext {
	if parent == nil {
		parent = context.Background()
	}
	ctx := &bootstrapAttemptContext{
		parent:   parent,
		deadline: time.Now().Add(timeout),
		done:     make(chan struct{}),
	}
	ctx.timer = time.AfterFunc(timeout, ctx.expire)
	go func() {
		select {
		case <-parent.Done():
			ctx.cancel(parent.Err())
		case <-ctx.done:
		}
	}()
	return ctx
}

func (c *bootstrapAttemptContext) Deadline() (time.Time, bool) {
	c.mu.RLock()
	disarmed := c.disarmed
	deadline := c.deadline
	c.mu.RUnlock()
	if disarmed {
		return c.parent.Deadline()
	}
	if parentDeadline, ok := c.parent.Deadline(); ok && parentDeadline.Before(deadline) {
		return parentDeadline, true
	}
	return deadline, true
}

func (c *bootstrapAttemptContext) Done() <-chan struct{} { return c.done }

func (c *bootstrapAttemptContext) Err() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.err
}

func (c *bootstrapAttemptContext) Value(key any) any { return c.parent.Value(key) }

func (c *bootstrapAttemptContext) expire() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.disarmed || c.err != nil {
		return
	}
	c.err = context.DeadlineExceeded
	close(c.done)
}

func (c *bootstrapAttemptContext) cancel(err error) {
	if err == nil {
		err = context.Canceled
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return
	}
	c.err = err
	if c.timer != nil {
		c.timer.Stop()
	}
	close(c.done)
}

// disarm removes the bootstrap deadline while preserving parent cancellation.
// It reports whether the deadline had already fired.
func (c *bootstrapAttemptContext) disarm() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if errors.Is(c.err, context.DeadlineExceeded) {
		return true
	}
	c.disarmed = true
	if c.timer != nil {
		c.timer.Stop()
	}
	return false
}

func (h *BaseAPIHandler) executeStreamBootstrapAttempt(ctx context.Context, providers []string, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	timeout := StreamingBootstrapTimeout(h.Cfg)
	if timeout <= 0 {
		return h.AuthManager.ExecuteStream(ctx, providers, req, opts)
	}

	attemptCtx := newBootstrapAttemptContext(ctx, timeout)
	result, err := h.AuthManager.ExecuteStream(attemptCtx, providers, req, opts)
	timedOut := attemptCtx.disarm()
	if timedOut {
		return nil, &streamBootstrapTimeoutError{timeout: timeout}
	}
	if err != nil {
		attemptCtx.cancel(context.Canceled)
		return nil, err
	}
	return releaseBootstrapAttemptContext(result, attemptCtx), nil
}

func releaseBootstrapAttemptContext(result *coreexecutor.StreamResult, attemptCtx *bootstrapAttemptContext) *coreexecutor.StreamResult {
	if result == nil || result.Chunks == nil {
		attemptCtx.cancel(context.Canceled)
		return result
	}
	remaining := result.Chunks
	out := make(chan coreexecutor.StreamChunk)
	result.Chunks = out
	go func() {
		defer close(out)
		defer attemptCtx.cancel(context.Canceled)
		for {
			select {
			case <-attemptCtx.Done():
				return
			case chunk, ok := <-remaining:
				if !ok {
					return
				}
				select {
				case <-attemptCtx.Done():
					return
				case out <- chunk:
				}
			}
		}
	}()
	return result
}

func (h *BaseAPIHandler) executeInitialStreamWithBootstrapTimeout(ctx context.Context, providers []string, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, int, error) {
	maxRetries := StreamingBootstrapRetries(h.Cfg)
	retriesUsed := 0
	for {
		result, err := h.executeStreamBootstrapAttempt(ctx, providers, req, opts)
		if err == nil {
			return result, retriesUsed, nil
		}
		var timeoutErr *streamBootstrapTimeoutError
		if !errors.As(err, &timeoutErr) || retriesUsed >= maxRetries {
			return nil, retriesUsed, err
		}
		retriesUsed++
	}
}
