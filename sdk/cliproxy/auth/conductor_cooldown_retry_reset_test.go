package auth

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
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

// idRecordingRateLimitedExecutor behaves like rateLimitedExecutor and records
// which auth IDs were executed, so a test can prove a caller-excluded
// credential is never picked after a cooldown retry.
type idRecordingRateLimitedExecutor struct {
	mu         sync.Mutex
	identifier string
	calls      map[string]int
	err        error
}

func (e *idRecordingRateLimitedExecutor) Identifier() string {
	if e.identifier != "" {
		return e.identifier
	}
	return "gemini"
}

func (e *idRecordingRateLimitedExecutor) record(id string) {
	e.mu.Lock()
	e.calls[id]++
	e.mu.Unlock()
}

func (e *idRecordingRateLimitedExecutor) Execute(_ context.Context, a *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.record(a.ID)
	return cliproxyexecutor.Response{}, e.err
}

func (e *idRecordingRateLimitedExecutor) ExecuteStream(_ context.Context, a *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.record(a.ID)
	return nil, e.err
}

func (e *idRecordingRateLimitedExecutor) CountTokens(_ context.Context, a *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.record(a.ID)
	return cliproxyexecutor.Response{}, e.err
}

func (e *idRecordingRateLimitedExecutor) Refresh(_ context.Context, a *Auth) (*Auth, error) {
	return a, nil
}

func (e *idRecordingRateLimitedExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *idRecordingRateLimitedExecutor) count(id string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls[id]
}

// TestCooldownRetryPreservesCallerExclusions is a regression guard for the
// codex P2 follow-up on PR #4881: resetRecoveredExclusions must prune only
// rotation-added exclusions. Caller-provided exclusions from request metadata
// must survive the cooldown retry, otherwise a credential the caller already
// ruled out can be executed once the wait completes.
func TestCooldownRetryPreservesCallerExclusions(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(1, 5*time.Second, 0)
	authRateLimited := &Auth{ID: "auth-429", Provider: "gemini", Status: StatusActive}
	authCallerExcluded := &Auth{ID: "auth-caller", Provider: "gemini", Status: StatusActive}
	for _, a := range []*Auth{authRateLimited, authCallerExcluded} {
		if _, err := manager.Register(context.Background(), a); err != nil {
			t.Fatalf("register auth %s: %v", a.ID, err)
		}
	}
	reg := registry.GetGlobalRegistry()
	for _, a := range []*Auth{authRateLimited, authCallerExcluded} {
		reg.RegisterClient(a.ID, "gemini", []*registry.ModelInfo{{ID: "test-model"}})
	}
	t.Cleanup(func() {
		reg.UnregisterClient(authRateLimited.ID)
		reg.UnregisterClient(authCallerExcluded.ID)
	})
	manager.RefreshSchedulerEntry(authRateLimited.ID)
	manager.RefreshSchedulerEntry(authCallerExcluded.ID)
	exec := &idRecordingRateLimitedExecutor{
		calls: make(map[string]int),
		err:   &retryableRateLimitError{status: http.StatusTooManyRequests, retryAfter: 50 * time.Millisecond},
	}
	manager.RegisterExecutor(exec)

	opts := cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.ExcludedAuthIDsMetadataKey: []string{"auth-caller"},
		},
	}
	_, err := manager.Execute(context.Background(), []string{"gemini"}, cliproxyexecutor.Request{Model: "test-model"}, opts)
	if err == nil {
		t.Fatal("expected rate-limit error")
	}
	if got := exec.count("auth-caller"); got != 0 {
		t.Fatalf("caller-excluded auth executed %d times across cooldown retry", got)
	}
	if got := exec.count("auth-429"); got != 2 {
		t.Fatalf("expected rotation auth to run twice (initial + post-cooldown retry), got %d", got)
	}
}

func TestCooldownRetryPreservesConfigDisabledCoolingExclusions(t *testing.T) {
	t.Run("global config disable cooling retains exclusion on retry", func(t *testing.T) {
		manager := NewManager(nil, nil, nil)
		manager.SetConfigSnapshot(&internalconfig.Config{DisableCooling: true})
		manager.SetRetryConfig(1, 5*time.Second, 0)
		auth := &Auth{ID: "auth-global-disabled", Provider: "gemini", Status: StatusActive}
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("register auth: %v", err)
		}
		reg := registry.GetGlobalRegistry()
		reg.RegisterClient(auth.ID, "gemini", []*registry.ModelInfo{{ID: "test-model"}})
		t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
		manager.RefreshSchedulerEntry(auth.ID)
		exec := &idRecordingRateLimitedExecutor{
			calls: make(map[string]int),
			err:   &retryableRateLimitError{status: http.StatusTooManyRequests, retryAfter: 50 * time.Millisecond},
		}
		manager.RegisterExecutor(exec)

		_, err := manager.Execute(context.Background(), []string{"gemini"}, cliproxyexecutor.Request{Model: "test-model"}, cliproxyexecutor.Options{})
		if err == nil {
			t.Fatal("expected rate-limit error")
		}
		if got := exec.count("auth-global-disabled"); got != 1 {
			t.Fatalf("expected config-disabled cooling auth to run once (exclusion retained on retry), got %d", got)
		}
	})

	t.Run("provider compat config disable cooling retains exclusion on retry", func(t *testing.T) {
		manager := NewManager(nil, nil, nil)
		manager.SetConfigSnapshot(&internalconfig.Config{
			OpenAICompatibility: []internalconfig.OpenAICompatibility{
				{
					Name:           "custom-openai",
					DisableCooling: &[]bool{true}[0],
				},
			},
		})
		manager.SetRetryConfig(1, 5*time.Second, 0)
		auth := &Auth{
			ID:       "auth-compat-disabled",
			Provider: "openai-compatibility",
			Status:   StatusActive,
			Attributes: map[string]string{
				"provider_key": "custom-openai",
			},
		}
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("register auth: %v", err)
		}
		reg := registry.GetGlobalRegistry()
		reg.RegisterClient(auth.ID, "openai-compatibility", []*registry.ModelInfo{{ID: "test-model"}})
		t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
		manager.RefreshSchedulerEntry(auth.ID)
		exec := &idRecordingRateLimitedExecutor{
			identifier: "openai-compatibility",
			calls:      make(map[string]int),
			err:        &retryableRateLimitError{status: http.StatusTooManyRequests, retryAfter: 50 * time.Millisecond},
		}
		manager.RegisterExecutor(exec)

		_, err := manager.Execute(context.Background(), []string{"openai-compatibility"}, cliproxyexecutor.Request{Model: "test-model"}, cliproxyexecutor.Options{})
		if err == nil {
			t.Fatal("expected rate-limit error")
		}
		if got := exec.count("auth-compat-disabled"); got != 1 {
			t.Fatalf("expected provider-config-disabled cooling auth to run once (exclusion retained on retry), got %d", got)
		}
	})

	t.Run("control cooling enabled normally resets exclusion on retry", func(t *testing.T) {
		manager := NewManager(nil, nil, nil)
		manager.SetConfigSnapshot(&internalconfig.Config{DisableCooling: false})
		manager.SetRetryConfig(1, 5*time.Second, 0)
		auth := &Auth{ID: "auth-cooling-enabled", Provider: "gemini", Status: StatusActive}
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("register auth: %v", err)
		}
		reg := registry.GetGlobalRegistry()
		reg.RegisterClient(auth.ID, "gemini", []*registry.ModelInfo{{ID: "test-model"}})
		t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
		manager.RefreshSchedulerEntry(auth.ID)
		exec := &idRecordingRateLimitedExecutor{
			calls: make(map[string]int),
			err:   &retryableRateLimitError{status: http.StatusTooManyRequests, retryAfter: 50 * time.Millisecond},
		}
		manager.RegisterExecutor(exec)

		_, err := manager.Execute(context.Background(), []string{"gemini"}, cliproxyexecutor.Request{Model: "test-model"}, cliproxyexecutor.Options{})
		if err == nil {
			t.Fatal("expected rate-limit error")
		}
		if got := exec.count("auth-cooling-enabled"); got != 2 {
			t.Fatalf("expected cooling-enabled auth to run twice (initial + post-cooldown retry), got %d", got)
		}
	})
}
