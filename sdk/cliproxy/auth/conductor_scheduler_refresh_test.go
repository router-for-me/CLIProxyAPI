package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type schedulerProviderTestExecutor struct {
	provider string
}

func (e schedulerProviderTestExecutor) Identifier() string { return e.provider }

func (e schedulerProviderTestExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e schedulerProviderTestExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e schedulerProviderTestExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e schedulerProviderTestExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e schedulerProviderTestExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

type unauthorizedRefreshTestExecutor struct {
	schedulerProviderTestExecutor
}

func (e unauthorizedRefreshTestExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return nil, errors.New("token refresh failed with status 401: invalid_grant")
}

func TestManager_RefreshAuthUnauthorizedFailureStopsAutoRefreshRetry(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(unauthorizedRefreshTestExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "codex"},
	})

	auth := &Auth{
		ID:       "unauthorized-refresh",
		Provider: "codex",
		Metadata: map[string]any{
			"email": "x@example.com",
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	manager.refreshAuth(ctx, auth.ID)

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("expected auth %q after refresh", auth.ID)
	}
	if updated.LastError == nil {
		t.Fatal("expected unauthorized refresh failure to be recorded")
	}
	if got := updated.LastError.StatusCode(); got != http.StatusUnauthorized {
		t.Fatalf("LastError.StatusCode() = %d, want %d", got, http.StatusUnauthorized)
	}
	if updated.LastError.Code != "unauthorized" {
		t.Fatalf("LastError.Code = %q, want unauthorized", updated.LastError.Code)
	}
	if !updated.NextRefreshAfter.IsZero() {
		t.Fatalf("NextRefreshAfter = %s, want zero for unauthorized refresh failure", updated.NextRefreshAfter)
	}
	now := time.Now()
	if manager.shouldRefresh(updated, now) {
		t.Fatal("expected unauthorized auth to stop refresh attempts")
	}
	if _, shouldSchedule := nextRefreshCheckAt(now, updated, time.Second); shouldSchedule {
		t.Fatal("expected unauthorized auth to be removed from the auto-refresh schedule")
	}
}

func TestManager_RefreshSchedulerEntry_RebuildsSupportedModelSetAfterModelRegistration(t *testing.T) {
	ctx := context.Background()

	testCases := []struct {
		name  string
		prime func(*Manager, *Auth) error
	}{
		{
			name: "register",
			prime: func(manager *Manager, auth *Auth) error {
				_, errRegister := manager.Register(ctx, auth)
				return errRegister
			},
		},
		{
			name: "update",
			prime: func(manager *Manager, auth *Auth) error {
				_, errRegister := manager.Register(ctx, auth)
				if errRegister != nil {
					return errRegister
				}
				updated := auth.Clone()
				updated.Metadata = map[string]any{"updated": true}
				_, errUpdate := manager.Update(ctx, updated)
				return errUpdate
			},
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			manager := NewManager(nil, &RoundRobinSelector{}, nil)
			auth := &Auth{
				ID:       "refresh-entry-" + testCase.name,
				Provider: "gemini",
			}
			if errPrime := testCase.prime(manager, auth); errPrime != nil {
				t.Fatalf("prime auth %s: %v", testCase.name, errPrime)
			}

			registerSchedulerModels(t, "gemini", "scheduler-refresh-model", auth.ID)

			got, errPick := manager.scheduler.pickSingle(ctx, "gemini", "scheduler-refresh-model", cliproxyexecutor.Options{}, nil)
			var authErr *Error
			if !errors.As(errPick, &authErr) || authErr == nil {
				t.Fatalf("pickSingle() before refresh error = %v, want auth_not_found", errPick)
			}
			if authErr.Code != "auth_not_found" {
				t.Fatalf("pickSingle() before refresh code = %q, want %q", authErr.Code, "auth_not_found")
			}
			if got != nil {
				t.Fatalf("pickSingle() before refresh auth = %v, want nil", got)
			}

			manager.RefreshSchedulerEntry(auth.ID)

			got, errPick = manager.scheduler.pickSingle(ctx, "gemini", "scheduler-refresh-model", cliproxyexecutor.Options{}, nil)
			if errPick != nil {
				t.Fatalf("pickSingle() after refresh error = %v", errPick)
			}
			if got == nil || got.ID != auth.ID {
				t.Fatalf("pickSingle() after refresh auth = %v, want %q", got, auth.ID)
			}
		})
	}
}

func TestManager_PickNext_RebuildsSchedulerAfterModelCooldownError(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "gemini"})

	registerSchedulerModels(t, "gemini", "scheduler-cooldown-rebuild-model", "cooldown-stale-old")

	oldAuth := &Auth{
		ID:       "cooldown-stale-old",
		Provider: "gemini",
	}
	if _, errRegister := manager.Register(ctx, oldAuth); errRegister != nil {
		t.Fatalf("register old auth: %v", errRegister)
	}

	manager.MarkResult(ctx, Result{
		AuthID:   oldAuth.ID,
		Provider: "gemini",
		Model:    "scheduler-cooldown-rebuild-model",
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"},
	})

	newAuth := &Auth{
		ID:       "cooldown-stale-new",
		Provider: "gemini",
	}
	if _, errRegister := manager.Register(ctx, newAuth); errRegister != nil {
		t.Fatalf("register new auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(newAuth.ID, "gemini", []*registry.ModelInfo{{ID: "scheduler-cooldown-rebuild-model"}})
	t.Cleanup(func() {
		reg.UnregisterClient(newAuth.ID)
	})

	got, errPick := manager.scheduler.pickSingle(ctx, "gemini", "scheduler-cooldown-rebuild-model", cliproxyexecutor.Options{}, nil)
	var cooldownErr *modelCooldownError
	if !errors.As(errPick, &cooldownErr) {
		t.Fatalf("pickSingle() before sync error = %v, want modelCooldownError", errPick)
	}
	if got != nil {
		t.Fatalf("pickSingle() before sync auth = %v, want nil", got)
	}

	got, executor, errPick := manager.pickNext(ctx, "gemini", "scheduler-cooldown-rebuild-model", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickNext() error = %v", errPick)
	}
	if executor == nil {
		t.Fatal("pickNext() executor = nil")
	}
	if got == nil || got.ID != newAuth.ID {
		t.Fatalf("pickNext() auth = %v, want %q", got, newAuth.ID)
	}
}

func TestManager_RefreshAuthUnauthorizedFailure_RetainsUnexpiredAccessToken(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(unauthorizedRefreshTestExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "codex"},
	})

	futureExpiry := time.Now().Add(48 * time.Hour).Format(time.RFC3339)
	auth := &Auth{
		ID:       "unauthorized-refresh-valid-token",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"email":        "active@example.com",
			"access_token": "valid-future-access-token",
			"expired":      futureExpiry,
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	manager.refreshAuth(ctx, auth.ID)

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("expected auth %q after refresh", auth.ID)
	}
	if updated.Unavailable {
		t.Fatal("expected auth with valid unexpired access_token NOT to be marked unavailable")
	}
	if updated.Status == StatusError {
		t.Fatalf("expected auth status not to be StatusError, got %s", updated.Status)
	}
	if updated.NextRefreshAfter.IsZero() {
		t.Fatal("expected NextRefreshAfter to be scheduled for retry backoff, got zero")
	}
}

func TestManager_RefreshAuthFailure_PreservesPreexistingUnavailableState(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(unauthorizedRefreshTestExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "codex"},
	})

	futureExpiry := time.Now().Add(48 * time.Hour).Format(time.RFC3339)
	auth := &Auth{
		ID:            "unauthorized-refresh-preexisting-unavailable",
		Provider:      "codex",
		Status:        StatusError,
		StatusMessage: "quota_exceeded",
		Unavailable:   true,
		Metadata: map[string]any{
			"email":        "quota@example.com",
			"access_token": "valid-future-access-token",
			"expired":      futureExpiry,
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	manager.refreshAuth(ctx, auth.ID)

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("expected auth %q after refresh", auth.ID)
	}
	if !updated.Unavailable {
		t.Fatal("expected preexisting unavailable state to be preserved on refresh failure")
	}
	if updated.StatusMessage != "quota_exceeded" {
		t.Fatalf("StatusMessage = %q, want quota_exceeded", updated.StatusMessage)
	}
	if updated.NextRefreshAfter.IsZero() {
		t.Fatal("expected NextRefreshAfter to have scheduled retry backoff")
	}
	if hasUnauthorizedAuthFailure(updated) {
		t.Fatal("hasUnauthorizedAuthFailure should be false when unexpired access token has scheduled NextRefreshAfter")
	}
	now := time.Now()
	if _, shouldSchedule := nextRefreshCheckAt(now, updated, time.Second); !shouldSchedule {
		t.Fatal("nextRefreshCheckAt should continue to schedule retry for unexpired access token with pending retry")
	}
}

type transientErrorRefreshTestExecutor struct {
	schedulerProviderTestExecutor
}

func (e transientErrorRefreshTestExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return nil, errors.New("upstream 503 service unavailable")
}

func TestManager_RefreshAuthTransientFailure_ExpiredTokenMarkedUnavailableWithRetry(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(transientErrorRefreshTestExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "codex"},
	})

	pastExpiry := time.Now().Add(-24 * time.Hour).Format(time.RFC3339)
	auth := &Auth{
		ID:       "transient-refresh-expired-token",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"email":        "expired@example.com",
			"access_token": "expired-access-token",
			"expired":      pastExpiry,
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	manager.refreshAuth(ctx, auth.ID)

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("expected auth %q after refresh", auth.ID)
	}
	if !updated.Unavailable {
		t.Fatal("expected expired token on transient refresh error to be marked unavailable")
	}
	if updated.Status != StatusError {
		t.Fatalf("expected StatusError, got %s", updated.Status)
	}
	if updated.NextRefreshAfter.IsZero() {
		t.Fatal("expected NextRefreshAfter to have retry backoff for transient error, got zero")
	}
}

func TestManager_RefreshAuth_ExpiredAccessTokenBlockedFromSelection(t *testing.T) {
	pastExpiry := time.Now().Add(-1 * time.Hour)
	auth := &Auth{
		ID:       "expired-token-blocked",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"email":        "user@example.com",
			"access_token": "expired-token",
			"expired":      pastExpiry.Format(time.RFC3339),
		},
	}

	blocked, reason, _ := effectiveBlock(auth, "gpt-5", time.Now())
	if !blocked {
		t.Fatal("effectiveBlock should return blocked=true for expired access token")
	}
	_ = reason
}

type blockingRefreshTestExecutor struct {
	schedulerProviderTestExecutor
	started chan struct{}
	release chan struct{}
}

func (e *blockingRefreshTestExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	close(e.started)
	<-e.release
	return nil, errors.New("token refresh failed with status 401: invalid_grant")
}

func TestManager_RefreshAuth_PreservesConcurrentCooldownMutationDuringRefresh(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	executor := &blockingRefreshTestExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "codex"},
		started:                       make(chan struct{}),
		release:                       make(chan struct{}),
	}
	manager.RegisterExecutor(executor)

	futureExpiry := time.Now().Add(48 * time.Hour).Format(time.RFC3339)
	auth := &Auth{
		ID:       "concurrent-refresh-auth",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"email":        "active@example.com",
			"access_token": "valid-future-access-token",
			"expired":      futureExpiry,
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	refreshDone := make(chan struct{})
	go func() {
		manager.refreshAuth(ctx, auth.ID)
		close(refreshDone)
	}()

	// Wait for refresh to start
	<-executor.started

	// Concurrently simulate a 503 / rate limit cooldown on the auth record
	manager.mu.Lock()
	if current := manager.auths[auth.ID]; current != nil {
		current.Unavailable = true
		current.Status = StatusError
		current.StatusMessage = "cooling_503"
	}
	manager.mu.Unlock()

	// Release refresh to complete with failure
	close(executor.release)
	<-refreshDone

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("expected auth %q after refresh", auth.ID)
	}
	if !updated.Unavailable {
		t.Fatal("expected concurrent cooldown (Unavailable=true) to be preserved, not overwritten by refresh")
	}
	if updated.StatusMessage != "cooling_503" {
		t.Fatalf("StatusMessage = %q, want cooling_503", updated.StatusMessage)
	}
}

func TestScheduler_ReadyAuthDemotedWhenTokenExpires(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(unauthorizedRefreshTestExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "codex"},
	})

	registerSchedulerModels(t, "codex", "gpt-5-short-test", "short-lived-auth")

	futureExpiry := time.Now().Add(10 * time.Minute).Format(time.RFC3339)
	auth := &Auth{
		ID:       "short-lived-auth",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"email":        "short@example.com",
			"access_token": "short-lived-access-token",
			"expired":      futureExpiry,
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	// First pick immediately: should succeed because token is unexpired
	got, errPick := manager.scheduler.pickSingle(ctx, "codex", "gpt-5-short-test", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickSingle() before expiry error = %v", errPick)
	}
	if got == nil || got.ID != auth.ID {
		t.Fatalf("pickSingle() got = %v, want %q", got, auth.ID)
	}

	// Explicitly mutate the internal auth metadata timestamp to the past without re-upserting
	pastExpiry := time.Now().Add(-10 * time.Minute).Format(time.RFC3339)
	manager.mu.Lock()
	if current := manager.auths[auth.ID]; current != nil {
		current.Metadata["expired"] = pastExpiry
	}
	manager.scheduler.mu.Lock()
	if p := manager.scheduler.providers["codex"]; p != nil {
		if meta := p.auths[auth.ID]; meta != nil && meta.auth != nil {
			meta.auth.Metadata["expired"] = pastExpiry
		}
		for _, shard := range p.modelShards {
			if entry := shard.entries[auth.ID]; entry != nil && entry.auth != nil {
				entry.auth.Metadata["expired"] = pastExpiry
			}
		}
	}
	manager.scheduler.mu.Unlock()
	manager.mu.Unlock()

	// Second pick: scheduler dynamic check must demote the expired token and reject pick
	gotAfter, errPickAfter := manager.scheduler.pickSingle(ctx, "codex", "gpt-5-short-test", cliproxyexecutor.Options{}, nil)
	if errPickAfter == nil && gotAfter != nil {
		t.Fatalf("pickSingle() after expiry should fail, but got auth %v", gotAfter.ID)
	}
}
