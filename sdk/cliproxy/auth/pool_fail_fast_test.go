package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// poolRateLimitError models an upstream 429 carrying a Retry-After hint. The
// conductor surfaces Retry-After onto Result.RetryAfter through
// retryAfterFromError, which pins the model state's cooling-until directly.
type poolRateLimitError struct {
	retryAfter time.Duration
}

func (e *poolRateLimitError) Error() string {
	return fmt.Sprintf(`{"error":{"code":"rate_limit_error","message":"too many requests","retry_after_ms":%d}}`, e.retryAfter.Milliseconds())
}

func (e *poolRateLimitError) StatusCode() int { return http.StatusTooManyRequests }

// RetryAfter implements the retryAfterProvider seam MarkResult reads.
func (e *poolRateLimitError) RetryAfter() *time.Duration { return &e.retryAfter }

// poolRateLimitExecutor is a claude executor stub: auths named in cool map get
// a 429 with the mapped Retry-After; all others succeed.
type poolRateLimitExecutor struct {
	mu    sync.Mutex
	cool  map[string]time.Duration
	calls []string
}

func (e *poolRateLimitExecutor) Identifier() string { return "claude" }

func (e *poolRateLimitExecutor) setCool(authID string, d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if d <= 0 {
		delete(e.cool, authID)
		return
	}
	e.cool[authID] = d
}

func (e *poolRateLimitExecutor) record(authID string) {
	e.mu.Lock()
	e.calls = append(e.calls, authID)
	e.mu.Unlock()
}

func (e *poolRateLimitExecutor) ExecuteCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.calls))
	copy(out, e.calls)
	return out
}

func (e *poolRateLimitExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.record(auth.ID)
	e.mu.Lock()
	d := e.cool[auth.ID]
	e.mu.Unlock()
	if d > 0 {
		return cliproxyexecutor.Response{}, &poolRateLimitError{retryAfter: d}
	}
	return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
}

func (*poolRateLimitExecutor) CountTokens(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
}

func (e *poolRateLimitExecutor) ExecuteStream(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.record(auth.ID)
	e.mu.Lock()
	d := e.cool[auth.ID]
	e.mu.Unlock()
	if d > 0 {
		return nil, &poolRateLimitError{retryAfter: d}
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(auth.ID)}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (*poolRateLimitExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (*poolRateLimitExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

// newPoolFailFastTestManager registers three plain claude auths (no compound
// provider_key: the pool aggregate groups by executor channel) backed by one
// shared poolRateLimitExecutor.
func newPoolFailFastTestManager(t *testing.T) (*Manager, *poolRateLimitExecutor, string) {
	t.Helper()
	model := "pool-failfast-model-" + uuid.NewString()
	executor := &poolRateLimitExecutor{cool: make(map[string]time.Duration)}
	manager := NewManager(nil, nil, NoopHook{})
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(executor)

	ids := []string{"pool-failfast-auth-a", "pool-failfast-auth-b", "pool-failfast-auth-c"}
	reg := registry.GetGlobalRegistry()
	for _, id := range ids {
		auth := &Auth{ID: id, Provider: "claude", Status: StatusActive}
		reg.RegisterClient(id, auth.Provider, []*registry.ModelInfo{{ID: model}})
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", id, errRegister)
		}
	}
	t.Cleanup(func() {
		for _, id := range ids {
			reg.UnregisterClient(id)
		}
	})
	return manager, executor, model
}

// markFour29 drives a MarkResult carrying a 429 + Retry-After for one auth
// through the manager, as the conductor would after a failed attempt.
func markFour29(t *testing.T, manager *Manager, authID, model string, retryAfter time.Duration) {
	t.Helper()
	err := &Error{Code: "rate_limit_error", Message: "too many requests", HTTPStatus: http.StatusTooManyRequests}
	manager.MarkResult(context.Background(), Result{
		AuthID:     authID,
		Provider:   "claude",
		Model:      model,
		Success:    false,
		RetryAfter: &retryAfter,
		Error:      err,
	})
}

func TestPoolFailFastBlocksThirdCredential(t *testing.T) {
	manager, executor, model := newPoolFailFastTestManager(t)

	// Two of three credentials get a 5-minute 429 on model M: the aggregate
	// threshold (cooling >= 2 AND cooling*2 >= total = 2*2 >= 3) is met.
	ra := 5 * time.Minute
	markFour29(t, manager, "pool-failfast-auth-a", model, ra)
	markFour29(t, manager, "pool-failfast-auth-b", model, ra)

	_, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	var cooldownErr *modelCooldownError
	if !errors.As(errExecute, &cooldownErr) {
		t.Fatalf("Execute() error = %v, want *modelCooldownError fail-fast", errExecute)
	}
	if cooldownErr.model != model {
		t.Fatalf("cooldown model = %q, want %q", cooldownErr.model, model)
	}
	if cooldownErr.resetIn <= 4*time.Minute || cooldownErr.resetIn > 5*time.Minute {
		t.Fatalf("resetIn = %v, want ~= 5m", cooldownErr.resetIn)
	}
	// The only eligible credential (c) must never have been invoked.
	for _, call := range executor.ExecuteCalls() {
		if call == "pool-failfast-auth-c" {
			t.Fatalf("credential c was invoked %v, want 0 calls (fail-fast)", executor.ExecuteCalls())
		}
	}
}

func TestPoolFailFastDisabledRestoresRotation(t *testing.T) {
	manager, _, model := newPoolFailFastTestManager(t)

	ra := 5 * time.Minute
	markFour29(t, manager, "pool-failfast-auth-a", model, ra)
	markFour29(t, manager, "pool-failfast-auth-b", model, ra)

	SetPoolModelCooldownEnabled(false)
	t.Cleanup(func() { SetPoolModelCooldownEnabled(true) })

	resp, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v, want rotation to serve via credential c", errExecute)
	}
	if got := string(resp.Payload); got != "pool-failfast-auth-c" {
		t.Fatalf("served by %q, want pool-failfast-auth-c (rotation preserved)", got)
	}
}

func TestPoolFailFastOneCoolingNotBlocked(t *testing.T) {
	manager, executor, model := newPoolFailFastTestManager(t)

	// A single cooling credential sits below the two-cooling minimum, so the
	// third credential still serves.
	ra := 5 * time.Minute
	markFour29(t, manager, "pool-failfast-auth-a", model, ra)

	resp, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v, want rotation to serve via a non-cooling credential", errExecute)
	}
	if got := string(resp.Payload); got == "pool-failfast-auth-a" {
		t.Fatalf("served by the cooling credential, want b or c")
	}
	if got := callsForAuth(executor.ExecuteCalls(), "pool-failfast-auth-c"); got > 1 {
		t.Fatalf("credential c invoked %d times, want at most 1", got)
	}
}

func TestPoolFailFastExpiresAndRecovers(t *testing.T) {
	manager, _, model := newPoolFailFastTestManager(t)

	// Tiny Retry-After: fail-fast engages, then the deadline passes and the
	// blocked credential becomes eligible again.
	ra := 25 * time.Millisecond
	markFour29(t, manager, "pool-failfast-auth-a", model, ra)
	markFour29(t, manager, "pool-failfast-auth-b", model, ra)

	mustModelCooldown := func() {
		t.Helper()
		_, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
		var cooldownErr *modelCooldownError
		if !errors.As(errExecute, &cooldownErr) {
			t.Fatalf("Execute() error = %v, want fail-fast while the pool deadline is live", errExecute)
		}
	}
	mustModelCooldown()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("pool fail-fast never lifted after the retry deadline")
		}
		time.Sleep(10 * time.Millisecond)
		_, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
		if errExecute == nil {
			// Any credential may serve after expiry: stale exceeded states no
			// longer block selection. The point is the block lifted.
			return
		}
		var cooldownErr *modelCooldownError
		if errors.As(errExecute, &cooldownErr) {
			continue
		}
		t.Fatalf("Execute() error = %v, want model_cooldown (still blocking) or success", errExecute)
	}
}

func TestPoolFailFastSnapshotRecords(t *testing.T) {
	manager, _, model := newPoolFailFastTestManager(t)

	ra := 5 * time.Minute
	markFour29(t, manager, "pool-failfast-auth-a", model, ra)
	markFour29(t, manager, "pool-failfast-auth-b", model, ra)

	snapshot := manager.CooldownStateSnapshot()
	found := false
	for _, record := range snapshot {
		if record.AuthID != "" || record.Provider != "claude" || record.Model != canonicalModelKey(model) {
			continue
		}
		found = true
		if record.Reason != "pool_quota" {
			t.Fatalf("pool record reason = %q, want pool_quota", record.Reason)
		}
		if record.NextRetryAfter.IsZero() {
			t.Fatal("pool record has a zero deadline")
		}
	}
	if !found {
		t.Fatalf("snapshot %v has no AuthID-less pool record for model %s", snapshot, model)
	}
}

func TestPoolFailFastToggleOffDisablesRecording(t *testing.T) {
	manager, _, model := newPoolFailFastTestManager(t)

	SetPoolModelCooldownEnabled(false)
	t.Cleanup(func() { SetPoolModelCooldownEnabled(true) })

	ra := 5 * time.Minute
	markFour29(t, manager, "pool-failfast-auth-a", model, ra)
	markFour29(t, manager, "pool-failfast-auth-b", model, ra)

	// Recording is gated by the same toggle: no entries may exist even after
	// cooling-looking MarkResult calls. Re-enabling the toggle must not
	// resurrect a ledger that was never written — block() must stay false.
	SetPoolModelCooldownEnabled(true)
	if _, blocked := manager.modelPoolCooldowns.block([]string{"claude"}, model, time.Now()); blocked {
		t.Fatal("pool aggregate recorded entries while the toggle was off")
	}
}
