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

	blocked, reason, _ := isAuthBlockedForModel(auth, "gpt-5", time.Now())
	if !blocked {
		t.Fatal("isAuthBlockedForModel should return blocked=true for expired access token")
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

type blockingSuccessRefreshExecutor struct {
	schedulerProviderTestExecutor
	started chan struct{}
	release chan struct{}
}

func (e *blockingSuccessRefreshExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	close(e.started)
	<-e.release
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = "refreshed-access-token"
	auth.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339)
	return auth, nil
}

func TestManager_RefreshAuth_PreservesConcurrentProxyURLAndMetadata(t *testing.T) {
	ctx := context.Background()
	mockStore := newMemoryAuthTestStore()
	manager := NewManager(mockStore, &RoundRobinSelector{}, nil)
	executor := &blockingSuccessRefreshExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "antigravity"},
		started:                       make(chan struct{}),
		release:                       make(chan struct{}),
	}
	manager.RegisterExecutor(executor)

	auth := &Auth{
		ID:       "test-antigravity",
		Provider: "antigravity",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "old-access-token",
			"expired":      time.Now().Add(-time.Hour).Format(time.RFC3339),
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

	// Concurrently simulate a PATCH /v0/management/auth-files/fields or file watcher update:
	// injecting proxy_url and custom metadata while refresh is in flight.
	currentAuth, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("auth not found: %s", auth.ID)
	}
	currentAuth.ProxyURL = "http://127.0.0.1:7890"
	currentAuth.Metadata["proxy_url"] = "http://127.0.0.1:7890"
	currentAuth.Metadata["custom_field"] = "custom_value"
	if _, errUpdate := manager.Update(ctx, currentAuth); errUpdate != nil {
		t.Fatalf("concurrent update failed: %v", errUpdate)
	}

	// Release refresh to complete successfully
	close(executor.release)
	<-refreshDone

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("expected auth %q after refresh", auth.ID)
	}

	// Verify refreshed token fields exist
	if gotToken, _ := updated.Metadata["access_token"].(string); gotToken != "refreshed-access-token" {
		t.Fatalf("access_token = %q, want refreshed-access-token", gotToken)
	}

	// Verify concurrent proxy_url and metadata were preserved
	if updated.ProxyURL != "http://127.0.0.1:7890" {
		t.Fatalf("ProxyURL = %q, want http://127.0.0.1:7890", updated.ProxyURL)
	}
	if gotProxy, _ := updated.Metadata["proxy_url"].(string); gotProxy != "http://127.0.0.1:7890" {
		t.Fatalf("Metadata[proxy_url] = %q, want http://127.0.0.1:7890", gotProxy)
	}
	if gotCustom, _ := updated.Metadata["custom_field"].(string); gotCustom != "custom_value" {
		t.Fatalf("Metadata[custom_field] = %q, want custom_value", gotCustom)
	}

	// Verify persisted store also has proxy_url preserved
	mockStore.mu.Lock()
	persisted := mockStore.auths[auth.ID]
	mockStore.mu.Unlock()
	if persisted == nil {
		t.Fatalf("persisted auth not found in store")
	}
	if persisted.ProxyURL != "http://127.0.0.1:7890" {
		t.Fatalf("persisted ProxyURL = %q, want http://127.0.0.1:7890", persisted.ProxyURL)
	}
	if gotProxy, _ := persisted.Metadata["proxy_url"].(string); gotProxy != "http://127.0.0.1:7890" {
		t.Fatalf("persisted Metadata[proxy_url] = %q, want http://127.0.0.1:7890", gotProxy)
	}
}

type inPlaceMutatingRefreshExecutor struct {
	schedulerProviderTestExecutor
	started chan struct{}
	release chan struct{}
}

func (e *inPlaceMutatingRefreshExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	close(e.started)
	<-e.release
	// In-place mutation of the passed Auth object (common behavior in executors)
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = "new-access-token"
	auth.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339)
	auth.Metadata["account_id"] = "acc-456"
	auth.Metadata["project_id"] = "proj-789"
	return auth, nil
}

func TestManager_RefreshAuth_InPlaceMutatingExecutorNonTokenMetadataAndConcurrentProxy(t *testing.T) {
	ctx := context.Background()
	mockStore := newMemoryAuthTestStore()
	manager := NewManager(mockStore, &RoundRobinSelector{}, nil)
	executor := &inPlaceMutatingRefreshExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "antigravity"},
		started:                       make(chan struct{}),
		release:                       make(chan struct{}),
	}
	manager.RegisterExecutor(executor)

	auth := &Auth{
		ID:       "inplace-auth",
		Provider: "antigravity",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "old-token",
			"expired":      time.Now().Add(-time.Hour).Format(time.RFC3339),
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

	<-executor.started

	currentAuth, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("auth not found: %s", auth.ID)
	}
	currentAuth.ProxyURL = "http://10.0.0.1:8888"
	currentAuth.Metadata["proxy_url"] = "http://10.0.0.1:8888"
	if _, errUpdate := manager.Update(ctx, currentAuth); errUpdate != nil {
		t.Fatalf("concurrent update failed: %v", errUpdate)
	}

	close(executor.release)
	<-refreshDone

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("expected auth %q after refresh", auth.ID)
	}

	// Verify token updated
	if gotToken, _ := updated.Metadata["access_token"].(string); gotToken != "new-access-token" {
		t.Fatalf("access_token = %q, want new-access-token", gotToken)
	}
	// Verify non-token metadata added by executor is preserved
	if gotAccount, _ := updated.Metadata["account_id"].(string); gotAccount != "acc-456" {
		t.Fatalf("account_id = %q, want acc-456", gotAccount)
	}
	if gotProject, _ := updated.Metadata["project_id"].(string); gotProject != "proj-789" {
		t.Fatalf("project_id = %q, want proj-789", gotProject)
	}
	// Verify concurrent proxy_url is preserved
	if updated.ProxyURL != "http://10.0.0.1:8888" {
		t.Fatalf("ProxyURL = %q, want http://10.0.0.1:8888", updated.ProxyURL)
	}
	if gotProxy, _ := updated.Metadata["proxy_url"].(string); gotProxy != "http://10.0.0.1:8888" {
		t.Fatalf("Metadata[proxy_url] = %q, want http://10.0.0.1:8888", gotProxy)
	}
}

func TestManager_RefreshAuth_ConcurrentlyClearedProxyNotResurrected(t *testing.T) {
	ctx := context.Background()
	mockStore := newMemoryAuthTestStore()
	manager := NewManager(mockStore, &RoundRobinSelector{}, nil)
	executor := &blockingSuccessRefreshExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "antigravity"},
		started:                       make(chan struct{}),
		release:                       make(chan struct{}),
	}
	manager.RegisterExecutor(executor)

	auth := &Auth{
		ID:       "clear-proxy-auth",
		Provider: "antigravity",
		Status:   StatusActive,
		ProxyURL: "http://initial-proxy.local:8080",
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "old-token",
			"proxy_url":    "http://initial-proxy.local:8080",
			"expired":      time.Now().Add(-time.Hour).Format(time.RFC3339),
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

	<-executor.started

	// Concurrently CLEAR proxy_url
	currentAuth, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("auth not found: %s", auth.ID)
	}
	currentAuth.ProxyURL = ""
	delete(currentAuth.Metadata, "proxy_url")
	if _, errUpdate := manager.Update(ctx, currentAuth); errUpdate != nil {
		t.Fatalf("concurrent clear proxy failed: %v", errUpdate)
	}

	close(executor.release)
	<-refreshDone

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("expected auth %q after refresh", auth.ID)
	}

	// Verify proxy_url was NOT resurrected
	if updated.ProxyURL != "" {
		t.Fatalf("ProxyURL = %q, want empty string", updated.ProxyURL)
	}
	if _, exists := updated.Metadata["proxy_url"]; exists {
		t.Fatalf("Metadata[proxy_url] should not exist, got %v", updated.Metadata["proxy_url"])
	}
	// Token should still be updated
	if gotToken, _ := updated.Metadata["access_token"].(string); gotToken != "refreshed-access-token" {
		t.Fatalf("access_token = %q, want refreshed-access-token", gotToken)
	}
}

func TestManager_RefreshAuth_StaleRegistrationEpochDiscarded(t *testing.T) {
	ctx := context.Background()
	mockStore := newMemoryAuthTestStore()
	manager := NewManager(mockStore, &RoundRobinSelector{}, nil)
	executor := &blockingSuccessRefreshExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "antigravity"},
		started:                       make(chan struct{}),
		release:                       make(chan struct{}),
	}
	manager.RegisterExecutor(executor)

	auth := &Auth{
		ID:       "re-register-auth",
		Provider: "antigravity",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "original-token",
			"expired":      time.Now().Add(-time.Hour).Format(time.RFC3339),
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

	<-executor.started

	// Unregister and re-register with same ID, new credentials
	manager.Remove(ctx, auth.ID)
	newAuth := &Auth{
		ID:       "re-register-auth",
		Provider: "antigravity",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "freshly-registered-different-token",
			"expired":      time.Now().Add(48 * time.Hour).Format(time.RFC3339),
		},
	}
	if _, errRegister := manager.Register(ctx, newAuth); errRegister != nil {
		t.Fatalf("re-register auth failed: %v", errRegister)
	}

	close(executor.release)
	<-refreshDone

	current, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("auth not found: %s", auth.ID)
	}
	// The new registration token must remain intact and NOT overwritten by the old refresh
	if gotToken, _ := current.Metadata["access_token"].(string); gotToken != "freshly-registered-different-token" {
		t.Fatalf("access_token = %q, want freshly-registered-different-token (stale refresh should be discarded)", gotToken)
	}
}

func TestManager_RefreshAuth_RestoresStatusFromErrorToActive(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	executor := &blockingSuccessRefreshExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "antigravity"},
		started:                       make(chan struct{}),
		release:                       make(chan struct{}),
	}
	manager.RegisterExecutor(executor)

	// Auth is currently in StatusError and Unavailable due to a previous 401/failure
	auth := &Auth{
		ID:            "error-status-auth",
		Provider:      "antigravity",
		Status:        StatusError,
		StatusMessage: "token expired",
		Unavailable:   true,
		LastError:     &Error{Message: "token expired"},
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "expired-token",
			"expired":      time.Now().Add(-time.Hour).Format(time.RFC3339),
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

	<-executor.started
	close(executor.release)
	<-refreshDone

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("auth not found: %s", auth.ID)
	}
	if updated.Status != StatusActive {
		t.Fatalf("Status = %v, want StatusActive", updated.Status)
	}
	if updated.Unavailable {
		t.Fatal("Unavailable = true, want false after successful refresh")
	}
	if updated.StatusMessage != "" {
		t.Fatalf("StatusMessage = %q, want empty string", updated.StatusMessage)
	}
	if updated.LastError != nil {
		t.Fatalf("LastError = %v, want nil", updated.LastError)
	}
	if updated.LastRefreshedAt.IsZero() {
		t.Fatal("LastRefreshedAt is zero, want non-zero")
	}
}

type testPrepareExecutor struct {
	schedulerProviderTestExecutor
	started chan struct{}
	release chan struct{}
}

func (e *testPrepareExecutor) ShouldPrepareRequestAuth(auth *Auth) bool {
	return auth.Metadata["project_id"] == nil
}

func (e *testPrepareExecutor) PrepareRequestAuth(ctx context.Context, auth *Auth) (*Auth, error) {
	close(e.started)
	<-e.release
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["project_id"] = "discovered-project-id"
	return auth, nil
}

func TestManager_PrepareRequestAuth_DoesNotRollbackConcurrentlyRefreshedToken(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	executor := &testPrepareExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "antigravity"},
		started:                       make(chan struct{}),
		release:                       make(chan struct{}),
	}
	manager.RegisterExecutor(executor)

	auth := &Auth{
		ID:       "prepare-auth",
		Provider: "antigravity",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "original-token",
			"expired":      time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	prepareDone := make(chan struct{})
	go func() {
		_, _ = manager.prepareRequestAuth(ctx, executor, auth)
		close(prepareDone)
	}()

	<-executor.started

	// While prepare is in-flight, a background token refresh completes and sets a new token
	current, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("auth not found: %s", auth.ID)
	}
	current.Metadata["access_token"] = "brand-new-refreshed-token"
	if _, errUpdate := manager.Update(ctx, current); errUpdate != nil {
		t.Fatalf("update with new token failed: %v", errUpdate)
	}

	close(executor.release)
	<-prepareDone

	finalAuth, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("auth not found: %s", auth.ID)
	}
	// The brand-new-refreshed-token must NOT be rolled back to original-token
	if gotToken, _ := finalAuth.Metadata["access_token"].(string); gotToken != "brand-new-refreshed-token" {
		t.Fatalf("access_token = %q, want brand-new-refreshed-token (prepare should not roll back token)", gotToken)
	}
	// Project ID should be added
	if gotProj, _ := finalAuth.Metadata["project_id"].(string); gotProj != "discovered-project-id" {
		t.Fatalf("project_id = %q, want discovered-project-id", gotProj)
	}
}

func TestManager_Persist_SkipPersistAdvancesWatermarkAndBlocksOlderGeneration(t *testing.T) {
	ctx := context.Background()
	mockStore := newMemoryAuthTestStore()
	manager := NewManager(mockStore, &RoundRobinSelector{}, nil)

	auth := &Auth{
		ID:       "watermark-auth",
		Provider: "antigravity",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "gen1-token",
			"proxy_url":    "http://initial.proxy",
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	// Capture generation 1 snapshot and inject a unique sentinel that would only appear if persisted
	gen1Auth, _ := manager.GetByID(auth.ID)
	gen1Auth.Metadata["stale_sentinel"] = "must_not_persist"

	// Simulate file watcher updating auth from disk via WithSkipPersist (e.g. user edited JSON on disk)
	watcherAuth := gen1Auth.Clone()
	delete(watcherAuth.Metadata, "stale_sentinel")
	watcherAuth.ProxyURL = "http://user-disk-edit.proxy"
	watcherAuth.Metadata["proxy_url"] = "http://user-disk-edit.proxy"
	skipCtx := WithSkipPersist(ctx)
	if _, errUpdate := manager.Update(skipCtx, watcherAuth); errUpdate != nil {
		t.Fatalf("watcher update failed: %v", errUpdate)
	}

	// Now simulate the older generation 1 snapshot trying to persist afterwards
	if errPersist := manager.persist(ctx, gen1Auth); errPersist != nil {
		t.Fatalf("persist error: %v", errPersist)
	}

	// Immediately verify store was NOT overwritten with gen1's stale sentinel
	mockStore.mu.Lock()
	savedImmediately := mockStore.auths[auth.ID]
	mockStore.mu.Unlock()
	if savedImmediately == nil {
		t.Fatal("saved auth is nil")
	}
	if savedImmediately.Metadata["stale_sentinel"] != nil {
		t.Fatalf("stale generation-1 snapshot was persisted to store! sentinel = %v", savedImmediately.Metadata["stale_sentinel"])
	}
	// Let's verify by updating a generation 3 that DOES persist:
	gen3Auth := watcherAuth.Clone()
	gen3Auth.Metadata["access_token"] = "gen3-token"
	if _, errUpdate := manager.Update(ctx, gen3Auth); errUpdate != nil {
		t.Fatalf("gen3 update failed: %v", errUpdate)
	}

	mockStore.mu.Lock()
	savedFinal := mockStore.auths[auth.ID]
	mockStore.mu.Unlock()
	if savedFinal.Metadata["proxy_url"] != "http://user-disk-edit.proxy" {
		t.Fatalf("proxy_url in store = %v, want http://user-disk-edit.proxy", savedFinal.Metadata["proxy_url"])
	}
}

type proxyMutatingExecutor struct {
	schedulerProviderTestExecutor
	started chan struct{}
	release chan struct{}
}

func (e *proxyMutatingExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	close(e.started)
	<-e.release
	auth.ProxyURL = "http://executor-returned.proxy:8080"
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["proxy_url"] = "http://executor-returned.proxy:8080"
	auth.Metadata["access_token"] = "refreshed-token"
	auth.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339)
	auth.Status = "" // Executor returns unset status
	return auth, nil
}

func TestManager_RefreshAuth_BothModifyProxy_UserTakesPrecedenceAndConsistent(t *testing.T) {
	ctx := context.Background()
	mockStore := newMemoryAuthTestStore()
	manager := NewManager(mockStore, &RoundRobinSelector{}, nil)
	executor := &proxyMutatingExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "antigravity"},
		started:                       make(chan struct{}),
		release:                       make(chan struct{}),
	}
	manager.RegisterExecutor(executor)

	auth := &Auth{
		ID:       "conflict-proxy-auth",
		Provider: "antigravity",
		Status:   StatusError, // Starts in error
		ProxyURL: "http://base.proxy:8080",
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "old-token",
			"proxy_url":    "http://base.proxy:8080",
			"expired":      time.Now().Add(-time.Hour).Format(time.RFC3339),
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

	<-executor.started

	// Concurrently, user changes proxy to http://user-preferred.proxy:9090
	current, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("auth not found: %s", auth.ID)
	}
	current.ProxyURL = "http://user-preferred.proxy:9090"
	current.Metadata["proxy_url"] = "http://user-preferred.proxy:9090"
	if _, errUpdate := manager.Update(ctx, current); errUpdate != nil {
		t.Fatalf("concurrent update failed: %v", errUpdate)
	}

	close(executor.release)
	<-refreshDone

	finalAuth, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("expected auth %q after refresh", auth.ID)
	}

	// User's proxy change MUST win over executor's proxy change
	if finalAuth.ProxyURL != "http://user-preferred.proxy:9090" {
		t.Fatalf("ProxyURL = %q, want user value http://user-preferred.proxy:9090", finalAuth.ProxyURL)
	}
	if gotProxy, _ := finalAuth.Metadata["proxy_url"].(string); gotProxy != "http://user-preferred.proxy:9090" {
		t.Fatalf("Metadata[proxy_url] = %q, want user value http://user-preferred.proxy:9090", gotProxy)
	}
	// Token must still be updated
	if gotToken, _ := finalAuth.Metadata["access_token"].(string); gotToken != "refreshed-token" {
		t.Fatalf("access_token = %q, want refreshed-token", gotToken)
	}
	// Status should be restored to Active even if executor returned empty Status
	if finalAuth.Status != StatusActive {
		t.Fatalf("Status = %v, want StatusActive", finalAuth.Status)
	}
}

func TestManager_RefreshAuth_PreservesConcurrentModelCooldown(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	executor := &blockingSuccessRefreshExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "antigravity"},
		started:                       make(chan struct{}),
		release:                       make(chan struct{}),
	}
	manager.RegisterExecutor(executor)

	auth := &Auth{
		ID:       "model-cooldown-auth",
		Provider: "antigravity",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "old-token",
			"expired":      time.Now().Add(-time.Hour).Format(time.RFC3339),
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

	<-executor.started

	// While refresh is running, model "gemini-pro" gets put in 429 quota cooldown
	current, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("auth not found: %s", auth.ID)
	}
	if current.ModelStates == nil {
		current.ModelStates = make(map[string]*ModelState)
	}
	recoverTime := time.Now().Add(10 * time.Minute)
	current.ModelStates["gemini-pro"] = &ModelState{
		Status:         StatusError,
		Unavailable:    true,
		NextRetryAfter: recoverTime,
		StatusMessage:  "quota exceeded",
	}
	if _, errUpdate := manager.Update(ctx, current); errUpdate != nil {
		t.Fatalf("update with model cooldown failed: %v", errUpdate)
	}

	close(executor.release)
	<-refreshDone

	finalAuth, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("auth not found: %s", auth.ID)
	}
	// The model cooldown on "gemini-pro" MUST be preserved
	ms := finalAuth.ModelStates["gemini-pro"]
	if ms == nil {
		t.Fatal("ModelStates[gemini-pro] is nil, want preserved cooldown")
	}
	if !ms.Unavailable {
		t.Fatal("ModelStates[gemini-pro].Unavailable = false, want true")
	}
	if ms.StatusMessage != "quota exceeded" {
		t.Fatalf("ModelStates[gemini-pro].StatusMessage = %q, want quota exceeded", ms.StatusMessage)
	}
}
