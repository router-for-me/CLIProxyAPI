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
// a 429 with the mapped Retry-After; all others succeed. identifier overrides
// the default "claude" channel so multi-channel tests can register distinct
// executors.
type poolRateLimitExecutor struct {
	mu    sync.Mutex
	cool  map[string]time.Duration
	calls []string

	identifier string
}

func (e *poolRateLimitExecutor) Identifier() string {
	if e.identifier != "" {
		return e.identifier
	}
	return "claude"
}

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

// newPoolFailFastMultiChannelManager registers two executor channels
// ("alpha" and "beta"), two credentials each, all serving the same model. Two
// per channel because the aggregate threshold requires at least two cooling
// credentials before a channel can block at all. It exists to pin the ALL-of
// rule: the fail-fast must fire only when EVERY candidate channel is blocked.
func newPoolFailFastMultiChannelManager(t *testing.T) (*Manager, *poolRateLimitExecutor, *poolRateLimitExecutor, string) {
	t.Helper()
	model := "pool-failfast-multichannel-model-" + uuid.NewString()
	alphaExecutor := &poolRateLimitExecutor{cool: make(map[string]time.Duration), identifier: "alpha"}
	betaExecutor := &poolRateLimitExecutor{cool: make(map[string]time.Duration), identifier: "beta"}
	manager := NewManager(nil, nil, NoopHook{})
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(alphaExecutor)
	manager.RegisterExecutor(betaExecutor)

	type registration struct {
		id       string
		provider string
	}
	registrations := []registration{
		{id: "pool-multichannel-alpha-1", provider: "alpha"},
		{id: "pool-multichannel-alpha-2", provider: "alpha"},
		{id: "pool-multichannel-beta-1", provider: "beta"},
		{id: "pool-multichannel-beta-2", provider: "beta"},
	}
	reg := registry.GetGlobalRegistry()
	for _, r := range registrations {
		auth := &Auth{ID: r.id, Provider: r.provider, Status: StatusActive}
		reg.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", auth.ID, errRegister)
		}
	}
	t.Cleanup(func() {
		for _, r := range registrations {
			reg.UnregisterClient(r.id)
		}
	})
	return manager, alphaExecutor, betaExecutor, model
}

// requestMultiChannel drives one non-streaming Execute across both channels.
func requestMultiChannel(t *testing.T, manager *Manager, model string) (string, error) {
	t.Helper()
	resp, errExecute := manager.Execute(context.Background(), []string{"alpha", "beta"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		return "", errExecute
	}
	return string(resp.Payload), nil
}

func TestPoolFailFastMultiChannelOneOpenDoesNotBlock(t *testing.T) {
	for _, tc := range []struct {
		name   string
		legacy bool
	}{{"fast", false}, {"legacy", true}} {
		t.Run(tc.name, func(t *testing.T) {
			manager, alphaExecutor, betaExecutor, model := newPoolFailFastMultiChannelManager(t)
			if tc.legacy {
				// Force the legacy path by switching to a non-builtin
				// selector shape: disabling cooldown-wait no; the practical
				// legacy gate is hasPluginScheduler/useSchedulerFastPath.
				// Simplest deterministic switch: wrap the picker through the
				// compound-route path by pinning a compound provider key is
				// overkill; instead mark the manager's selector custom via
				// SetSelector with a stub that delegates.
				manager.SetSelector(&legacyDelegatingSelector{model: model})
			}

			// Only alpha's two credentials cooling (2 of 2 meets the
			// threshold); beta is healthy. The request must NOT fail fast:
			// a beta credential serves.
			ra := 5 * time.Minute
			markFour29(t, manager, "pool-multichannel-alpha-1", model, ra)
			markFour29(t, manager, "pool-multichannel-alpha-2", model, ra)

			served, errExecute := requestMultiChannel(t, manager, model)
			if errExecute != nil {
				t.Fatalf("Execute() error = %v, want the healthy beta channel to serve (ALL-of rule)", errExecute)
			}
			if served != "pool-multichannel-beta-1" && served != "pool-multichannel-beta-2" {
				t.Fatalf("served by %q, want a healthy beta credential", served)
			}
			if got := len(betaExecutor.ExecuteCalls()); got < 1 {
				t.Fatalf("beta executor calls = %d, want >= 1", got)
			}
			// Alpha's aggregate must not have poisoned the pick.
			if got := len(alphaExecutor.ExecuteCalls()); got != 0 {
				t.Fatalf("alpha executor calls = %v, want 0 (fail-fast must not fire with beta open)", alphaExecutor.ExecuteCalls())
			}
		})
	}
}

func TestPoolFailFastMultiChannelAllBlockedFailsFast(t *testing.T) {
	for _, tc := range []struct {
		name   string
		legacy bool
	}{{"fast", false}, {"legacy", true}} {
		t.Run(tc.name, func(t *testing.T) {
			manager, alphaExecutor, betaExecutor, model := newPoolFailFastMultiChannelManager(t)
			if tc.legacy {
				manager.SetSelector(&legacyDelegatingSelector{model: model})
			}

			// Both channels cooling at/above their thresholds (2 of 2 on
			// each): fail-fast engages and the error must name an ACTUALLY
			// blocked channel.
			ra := 5 * time.Minute
			markFour29(t, manager, "pool-multichannel-alpha-1", model, ra)
			markFour29(t, manager, "pool-multichannel-alpha-2", model, ra)
			markFour29(t, manager, "pool-multichannel-beta-1", model, ra)
			markFour29(t, manager, "pool-multichannel-beta-2", model, ra)

			_, errExecute := requestMultiChannel(t, manager, model)
			var cooldownErr *modelCooldownError
			if !errors.As(errExecute, &cooldownErr) {
				t.Fatalf("Execute() error = %v, want *modelCooldownError fail-fast", errExecute)
			}
			if cooldownErr.provider != "alpha" && cooldownErr.provider != "beta" {
				t.Fatalf("cooldown provider = %q, want a blocked channel (alpha or beta)", cooldownErr.provider)
			}
			if cooldownErr.resetIn <= 4*time.Minute || cooldownErr.resetIn > 5*time.Minute {
				t.Fatalf("resetIn = %v, want ~= 5m", cooldownErr.resetIn)
			}
			// Both candidate channels are blocked, so no upstream attempts
			// should have been made by THIS request (the MarkResults came
			// from seeding, before the Execute).
			if alphaCount, betaCount := len(alphaExecutor.ExecuteCalls()), len(betaExecutor.ExecuteCalls()); alphaCount != 0 || betaCount != 0 {
				t.Fatalf("executor calls alpha=%d beta=%d, want 0/0 (seeding used MarkResult, not Execute)", alphaCount, betaCount)
			}
		})
	}
}

// legacyDelegatingSelector forces pickNextMixed off the scheduler fast path
// (useSchedulerFastPath requires a builtin selector), routing picks through
// pickNextMixedLegacy while still handing picking to a round-robin selector.
type legacyDelegatingSelector struct {
	model string
}

func (s *legacyDelegatingSelector) Pick(ctx context.Context, provider, _ string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	delegate := &RoundRobinSelector{}
	return delegate.Pick(ctx, provider, s.model, opts, auths)
}
