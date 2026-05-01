package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
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

type refreshFailureStore struct {
	mu    sync.Mutex
	saved []*Auth
}

func (s *refreshFailureStore) List(context.Context) ([]*Auth, error) { return nil, nil }

func (s *refreshFailureStore) Save(_ context.Context, auth *Auth) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saved = append(s.saved, auth.Clone())
	return auth.ID, nil
}

func (s *refreshFailureStore) Delete(context.Context, string) error { return nil }

func (s *refreshFailureStore) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saved = nil
}

func (s *refreshFailureStore) lastSaved() *Auth {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.saved) == 0 {
		return nil
	}
	return s.saved[len(s.saved)-1]
}

type refreshFailureExecutor struct {
	provider string
	err      error
}

func (e refreshFailureExecutor) Identifier() string { return e.provider }

func (e refreshFailureExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e refreshFailureExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e refreshFailureExecutor) Refresh(context.Context, *Auth) (*Auth, error) {
	return nil, e.err
}

func (e refreshFailureExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e refreshFailureExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

type refreshSuccessExecutor struct {
	provider  string
	refreshed *Auth
}

func (e refreshSuccessExecutor) Identifier() string { return e.provider }

func (e refreshSuccessExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e refreshSuccessExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e refreshSuccessExecutor) Refresh(context.Context, *Auth) (*Auth, error) {
	if e.refreshed == nil {
		return nil, nil
	}
	return e.refreshed.Clone(), nil
}

func (e refreshSuccessExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e refreshSuccessExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
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

func TestManager_RefreshAuth_PersistsFailureState(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := &refreshFailureStore{}
	manager := NewManager(store, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(refreshFailureExecutor{
		provider: "codex",
		err:      errors.New("refresh_token_reused: refresh token already rotated"),
	})

	auth := &Auth{
		ID:       "refresh-failure-auth",
		Provider: "codex",
		Metadata: map[string]any{"email": "user@example.com"},
	}
	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	store.reset()

	before := time.Now()
	manager.refreshAuth(ctx, auth.ID)
	after := time.Now()

	saved := store.lastSaved()
	if saved == nil {
		t.Fatal("expected refresh failure to be persisted, got no Save() call")
	}
	if !saved.Unavailable {
		t.Fatalf("saved.Unavailable = false, want true")
	}
	if saved.Status != StatusError {
		t.Fatalf("saved.Status = %q, want %q", saved.Status, StatusError)
	}
	if !strings.Contains(saved.StatusMessage, "refresh_token_reused") {
		t.Fatalf("saved.StatusMessage = %q, want refresh_token_reused marker", saved.StatusMessage)
	}
	if saved.LastError == nil || !strings.Contains(saved.LastError.Message, "refresh_token_reused") {
		t.Fatalf("saved.LastError = %+v, want refresh_token_reused marker", saved.LastError)
	}
	if saved.NextRetryAfter.IsZero() || saved.NextRefreshAfter.IsZero() {
		t.Fatalf("saved retry gates not set: next_retry_after=%v next_refresh_after=%v", saved.NextRetryAfter, saved.NextRefreshAfter)
	}
	if !saved.NextRetryAfter.Equal(saved.NextRefreshAfter) {
		t.Fatalf("saved.NextRetryAfter = %v, want equal to NextRefreshAfter %v", saved.NextRetryAfter, saved.NextRefreshAfter)
	}

	lowerBound := before.Add(12*time.Hour - 5*time.Second)
	upperBound := after.Add(12*time.Hour + 5*time.Second)
	if saved.NextRetryAfter.Before(lowerBound) || saved.NextRetryAfter.After(upperBound) {
		t.Fatalf("saved.NextRetryAfter = %v, want between %v and %v", saved.NextRetryAfter, lowerBound, upperBound)
	}
}

func TestManager_RefreshAuth_PreservesCodexQuotaCooldownAcrossUpdate(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := &refreshFailureStore{}
	manager := NewManager(store, &RoundRobinSelector{}, nil)

	const (
		authAID = "codex-refresh-freeze-auth-a"
		authBID = "codex-refresh-freeze-auth-b"
	)
	models := []*registry.ModelInfo{{ID: "gpt-5.4"}, {ID: "gpt-4.1"}}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authAID, "codex", models)
	reg.RegisterClient(authBID, "codex", models)
	t.Cleanup(func() {
		reg.UnregisterClient(authAID)
		reg.UnregisterClient(authBID)
	})

	nextRetry := time.Now().UTC().Add(95 * time.Minute)
	manager.RegisterExecutor(refreshSuccessExecutor{
		provider: "codex",
		refreshed: &Auth{
			ID:       authAID,
			Provider: "codex",
			Status:   StatusActive,
			Metadata: map[string]any{"email": "refresh-freeze@example.com"},
			ModelStates: map[string]*ModelState{
				"gpt-5.4": {Status: StatusActive},
				"gpt-4.1": {Status: StatusActive},
			},
		},
	})

	if _, err := manager.Register(ctx, &Auth{
		ID:       authAID,
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{"email": "refresh-freeze@example.com"},
		ModelStates: map[string]*ModelState{
			"gpt-5.4": {
				Status:         StatusError,
				StatusMessage:  "quota exhausted",
				Unavailable:    true,
				NextRetryAfter: nextRetry,
				LastError: &Error{
					HTTPStatus: http.StatusTooManyRequests,
					Message:    `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached"}}`,
				},
				Quota: QuotaState{
					Exceeded:      true,
					Reason:        "quota",
					NextRecoverAt: nextRetry,
				},
			},
		},
	}); err != nil {
		t.Fatalf("Register(auth-a) error = %v", err)
	}
	if _, err := manager.Register(ctx, &Auth{ID: authBID, Provider: "codex"}); err != nil {
		t.Fatalf("Register(auth-b) error = %v", err)
	}
	store.reset()

	manager.refreshAuth(ctx, authAID)

	updated, ok := manager.GetByID(authAID)
	if !ok || updated == nil {
		t.Fatalf("GetByID(auth-a) = %v, %v; want populated auth", updated, ok)
	}
	if !updated.Unavailable {
		t.Fatal("updated.Unavailable = false, want refresh/update flow to preserve Codex quota freeze")
	}
	if updated.Status != StatusError {
		t.Fatalf("updated.Status = %v, want %v", updated.Status, StatusError)
	}
	if !updated.Quota.Exceeded {
		t.Fatal("updated.Quota.Exceeded = false, want true")
	}
	if !updated.NextRetryAfter.Equal(nextRetry) {
		t.Fatalf("updated.NextRetryAfter = %v, want %v", updated.NextRetryAfter, nextRetry)
	}
	state := updated.ModelStates["gpt-5.4"]
	if state == nil {
		t.Fatal(`updated.ModelStates["gpt-5.4"] = nil, want preserved quota state`)
	}
	if !state.Unavailable || !state.Quota.Exceeded {
		t.Fatalf("updated gpt-5.4 state = %+v, want preserved unavailable quota cooldown", state)
	}
	if !state.NextRetryAfter.Equal(nextRetry) {
		t.Fatalf("updated gpt-5.4 NextRetryAfter = %v, want %v", state.NextRetryAfter, nextRetry)
	}

	saved := store.lastSaved()
	if saved == nil {
		t.Fatal("expected refresh success update to persist auth state, got no Save() call")
	}
	if !saved.Unavailable || saved.Status != StatusError || !saved.Quota.Exceeded {
		t.Fatalf("saved auth = %+v, want persisted account-wide quota freeze", saved)
	}
	if !saved.NextRetryAfter.Equal(nextRetry) {
		t.Fatalf("saved.NextRetryAfter = %v, want %v", saved.NextRetryAfter, nextRetry)
	}

	got, errPick := manager.scheduler.pickSingle(ctx, "codex", "gpt-4.1", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("scheduler.pickSingle() error = %v", errPick)
	}
	if got == nil || got.ID != authBID {
		t.Fatalf("scheduler.pickSingle() auth = %v, want auth-b", got)
	}
}
