package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

// TestNextRefreshFailureBackoff_ExponentialGrowth pins the backoff curve so a
// future change that flattens or inverts the exponent is caught here. Curve:
//
//	failures=1 -> 5m   (base)
//	failures=2 -> 10m  (2×)
//	failures=3 -> 20m  (4×)
//	failures=4 -> 40m  (8×)
//	failures=5 -> 60m  (cap)
//	failures>=5 -> 60m (cap)
func TestNextRefreshFailureBackoff_ExponentialGrowth(t *testing.T) {
	cases := []struct {
		failures int
		want     time.Duration
	}{
		{0, refreshFailureBackoff},
		{1, refreshFailureBackoff},
		{2, 2 * refreshFailureBackoff},
		{3, 4 * refreshFailureBackoff},
		{4, 8 * refreshFailureBackoff},
		{5, refreshFailureBackoffMax},
		{10, refreshFailureBackoffMax},
		{1000, refreshFailureBackoffMax},
	}
	for _, tc := range cases {
		got := nextRefreshFailureBackoff(tc.failures)
		if got != tc.want {
			t.Fatalf("nextRefreshFailureBackoff(%d) = %s, want %s", tc.failures, got, tc.want)
		}
	}
}

// failingRefreshExecutor's Refresh always returns the same error so we can
// drive the failure counter deterministically without provider-specific
// scaffolding.
type failingRefreshExecutor struct {
	provider string
	err      error
	calls    int
}

func (e *failingRefreshExecutor) Identifier() string { return e.provider }
func (e *failingRefreshExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	e.calls++
	return nil, e.err
}
func (e *failingRefreshExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e *failingRefreshExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}
func (e *failingRefreshExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e *failingRefreshExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

// TestManager_RefreshAuth_FailureExponentialBackoff exercises refreshAuth's
// failure path multiple times and asserts NextRefreshAfter grows on each
// successive failure for the same credential.
func TestManager_RefreshAuth_FailureExponentialBackoff(t *testing.T) {
	m := NewManager(nil, nil, nil)
	exec := &failingRefreshExecutor{provider: "fake", err: errors.New("oauth: refresh failed")}
	m.RegisterExecutor(exec)

	a := &Auth{
		ID:       "fake-cred-1",
		Provider: "fake",
		Status:   StatusActive,
	}
	if _, err := m.Register(context.Background(), a); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Drive 4 sequential refresh attempts. Each one increments the failure
	// counter and pushes NextRefreshAfter further into the future.
	want := []time.Duration{
		refreshFailureBackoff,     // failures=1 -> 5m
		2 * refreshFailureBackoff, // failures=2 -> 10m
		4 * refreshFailureBackoff, // failures=3 -> 20m
		8 * refreshFailureBackoff, // failures=4 -> 40m
	}
	for i, expected := range want {
		before := time.Now()
		m.refreshAuth(context.Background(), a.ID)
		after := time.Now()

		m.mu.RLock()
		got := m.auths[a.ID]
		m.mu.RUnlock()
		if got == nil {
			t.Fatalf("attempt %d: auth disappeared from manager", i+1)
		}
		// NextRefreshAfter should be approximately now + expected. Allow a
		// generous tolerance (the refreshAuth internals call time.Now once,
		// which sits between `before` and `after`).
		if got.NextRefreshAfter.Before(before.Add(expected - time.Second)) {
			t.Fatalf("attempt %d: NextRefreshAfter=%s too early (want >= %s)",
				i+1, got.NextRefreshAfter, before.Add(expected))
		}
		if got.NextRefreshAfter.After(after.Add(expected + time.Second)) {
			t.Fatalf("attempt %d: NextRefreshAfter=%s too late (want <= %s)",
				i+1, got.NextRefreshAfter, after.Add(expected))
		}
		// Reset NextRefreshAfter so the next attempt isn't blocked by the
		// cooldown gate inside refreshAuth (we're deliberately driving the
		// failure path here, not the gate).
		m.mu.Lock()
		got.NextRefreshAfter = time.Time{}
		m.mu.Unlock()
	}
	if exec.calls != len(want) {
		t.Fatalf("expected %d Refresh calls, got %d", len(want), exec.calls)
	}
}

// successThenFailRefreshExecutor returns success once, then errors.
type successThenFailRefreshExecutor struct {
	provider string
	err      error
	calls    int
	mode     string // "success" or "fail"
}

func (e *successThenFailRefreshExecutor) Identifier() string { return e.provider }
func (e *successThenFailRefreshExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	e.calls++
	if e.mode == "success" {
		// Return the auth with refreshed timestamp; refreshAuth will fill in
		// LastRefreshedAt itself but the executor must return a non-error.
		return auth, nil
	}
	return nil, e.err
}
func (e *successThenFailRefreshExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e *successThenFailRefreshExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}
func (e *successThenFailRefreshExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e *successThenFailRefreshExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

// TestManager_RefreshAuth_SuccessResetsFailureCounter validates that a
// successful refresh clears the per-credential failure counter so a future
// failure restarts the backoff at the base value rather than continuing the
// previous exponential growth.
func TestManager_RefreshAuth_SuccessResetsFailureCounter(t *testing.T) {
	m := NewManager(nil, nil, nil)
	exec := &successThenFailRefreshExecutor{
		provider: "fake",
		err:      errors.New("oauth: refresh failed"),
		mode:     "fail",
	}
	m.RegisterExecutor(exec)

	a := &Auth{
		ID:       "fake-cred-2",
		Provider: "fake",
		Status:   StatusActive,
	}
	if _, err := m.Register(context.Background(), a); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Three failures push the failure counter to 3.
	for i := 0; i < 3; i++ {
		m.refreshAuth(context.Background(), a.ID)
		m.mu.Lock()
		m.auths[a.ID].NextRefreshAfter = time.Time{}
		m.mu.Unlock()
	}
	if got := m.refreshFailures[a.ID]; got != 3 {
		t.Fatalf("after 3 failures, counter=%d, want 3", got)
	}

	// One success — counter must reset to zero (delete from map).
	exec.mode = "success"
	m.refreshAuth(context.Background(), a.ID)
	if _, present := m.refreshFailures[a.ID]; present {
		t.Fatalf("after successful refresh, expected counter cleared; still present")
	}

	// The next failure must use the BASE backoff, not the exponential value
	// from before.
	exec.mode = "fail"
	// Reset NextRefreshAfter so the failure path runs (Update by success path
	// already cleared it but we make sure here in case shouldRefresh evaluated
	// it differently).
	m.mu.Lock()
	if cur := m.auths[a.ID]; cur != nil {
		cur.NextRefreshAfter = time.Time{}
	}
	m.mu.Unlock()

	before := time.Now()
	m.refreshAuth(context.Background(), a.ID)
	after := time.Now()

	m.mu.RLock()
	got := m.auths[a.ID]
	m.mu.RUnlock()
	if got == nil {
		t.Fatalf("auth disappeared from manager")
	}
	if got.NextRefreshAfter.Before(before.Add(refreshFailureBackoff - time.Second)) ||
		got.NextRefreshAfter.After(after.Add(refreshFailureBackoff+time.Second)) {
		t.Fatalf("first failure after success: NextRefreshAfter=%s, want ~%s in future",
			got.NextRefreshAfter, refreshFailureBackoff)
	}
}
