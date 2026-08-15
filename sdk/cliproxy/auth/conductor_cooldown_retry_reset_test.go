package auth

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// retryableRateLimitError carries an explicit Retry-After so
// shouldRetryAfterError decides to wait and re-enter the rotation loop.
type retryableRateLimitError struct {
	status     int
	retryAfter time.Duration
}

func (e *retryableRateLimitError) Error() string { return "rate limited" }

func (e *retryableRateLimitError) StatusCode() int { return e.status }

func (e *retryableRateLimitError) RetryAfter() *time.Duration { return &e.retryAfter }

// rateLimitedExecutor fails every call with the same rate-limit error and
// counts invocations, so a test can prove the post-cooldown retry actually
// re-executed a recovered credential instead of dying on stale exclusions.
type rateLimitedExecutor struct {
	calls atomic.Int32
	err   error
}

func (e *rateLimitedExecutor) Identifier() string { return "gemini" }

func (e *rateLimitedExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.calls.Add(1)
	return cliproxyexecutor.Response{}, e.err
}

func (e *rateLimitedExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.calls.Add(1)
	return nil, e.err
}

func (e *rateLimitedExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.calls.Add(1)
	return cliproxyexecutor.Response{}, e.err
}

func (e *rateLimitedExecutor) Refresh(_ context.Context, a *Auth) (*Auth, error) { return a, nil }

func (e *rateLimitedExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

// TestCooldownRetryResetsExclusions is a regression guard for the codex P1
// finding on CLIProxyAPI PR #4881: when every credential fails 429 with a
// Retry-After shorter than max-retry-interval, the conductor waits for the
// cooldown and retries. The exclusions accumulated during the failed rotation
// pass used to leak into the post-cooldown attempt, so the pick returned
// auth_unavailable without executing anything and the configured
// request-retry never ran. After the fix each entry point must execute the
// credential twice: once in the initial pass and once after the cooldown wait.
func TestCooldownRetryResetsExclusions(t *testing.T) {
	newManager := func() (*Manager, *rateLimitedExecutor) {
		manager := NewManager(nil, nil, nil)
		manager.SetRetryConfig(1, 5*time.Second, 0)
		auth := &Auth{ID: "auth-429", Provider: "gemini", Status: StatusActive}
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("register auth: %v", err)
		}
		reg := registry.GetGlobalRegistry()
		reg.RegisterClient(auth.ID, "gemini", []*registry.ModelInfo{{ID: "test-model"}})
		t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
		manager.RefreshSchedulerEntry(auth.ID)
		exec := &rateLimitedExecutor{err: &retryableRateLimitError{status: http.StatusTooManyRequests, retryAfter: 50 * time.Millisecond}}
		manager.RegisterExecutor(exec)
		return manager, exec
	}

	req := cliproxyexecutor.Request{Model: "test-model"}
	opts := cliproxyexecutor.Options{}

	t.Run("Execute", func(t *testing.T) {
		manager, exec := newManager()
		if _, err := manager.Execute(context.Background(), []string{"gemini"}, req, opts); err == nil {
			t.Fatal("expected rate-limit error")
		}
		if got := exec.calls.Load(); got != 2 {
			t.Fatalf("expected 2 executor calls (initial + post-cooldown retry), got %d", got)
		}
	})

	t.Run("ExecuteCount", func(t *testing.T) {
		manager, exec := newManager()
		if _, err := manager.ExecuteCount(context.Background(), []string{"gemini"}, req, opts); err == nil {
			t.Fatal("expected rate-limit error")
		}
		if got := exec.calls.Load(); got != 2 {
			t.Fatalf("expected 2 executor calls (initial + post-cooldown retry), got %d", got)
		}
	})

	t.Run("ExecuteStream", func(t *testing.T) {
		manager, exec := newManager()
		if _, err := manager.ExecuteStream(context.Background(), []string{"gemini"}, req, opts); err == nil {
			t.Fatal("expected rate-limit error")
		}
		if got := exec.calls.Load(); got != 2 {
			t.Fatalf("expected 2 executor calls (initial + post-cooldown retry), got %d", got)
		}
	})
}
