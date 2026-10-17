package auth

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type mockOAuthErrorExecutor struct {
	id           string
	refreshCalls atomic.Int32
	errToReturn  error
}

func (e *mockOAuthErrorExecutor) Identifier() string { return e.id }

func (e *mockOAuthErrorExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *mockOAuthErrorExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *mockOAuthErrorExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	e.refreshCalls.Add(1)
	if e.errToReturn != nil {
		return nil, e.errToReturn
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = "new-valid-token"
	return auth, nil
}

func (e *mockOAuthErrorExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *mockOAuthErrorExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

type oauthStatusError struct {
	code int
	msg  string
}

func (e oauthStatusError) Error() string   { return fmt.Sprintf("status %d: %s", e.code, e.msg) }
func (e oauthStatusError) StatusCode() int { return e.code }

func TestRefreshAuthForRequest_NormalDisabledAuth_RefreshesTokenSuccessfully(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	executor := &mockOAuthErrorExecutor{id: "test-provider"}
	manager.RegisterExecutor(executor)

	// Normal disabled auth without invalid_grant should refresh tokens normally (expected behavior)
	auth := &Auth{
		ID:       "normal-disabled-auth",
		Provider: "test-provider",
		Disabled: true,
		Status:   StatusDisabled,
		Metadata: map[string]any{
			"access_token":  "expired-token",
			"refresh_token": "refresh-1",
		},
	}
	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatalf("Register error: %v", err)
	}

	refreshed, errRefresh := manager.refreshAuthForRequest(ctx, auth.ID, "")
	if errRefresh != nil {
		t.Fatalf("expected successful refresh for normal disabled auth, got error: %v", errRefresh)
	}
	if executor.refreshCalls.Load() != 1 {
		t.Fatalf("executor.Refresh called %d times, want 1 for normal disabled auth", executor.refreshCalls.Load())
	}
	if refreshed.Metadata["access_token"] != "new-valid-token" {
		t.Fatalf("refreshed token = %v, want new-valid-token", refreshed.Metadata["access_token"])
	}
}

func TestRefreshAuthForRequest_DisabledAuth_TerminalFailure_NeverRetries(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{
			name: "invalid_grant",
			err: oauthStatusError{
				code: http.StatusBadRequest,
				msg:  `{"error": "invalid_grant", "error_description": "Bad Request"}`,
			},
		},
		{
			name: "wrapped_refresh_rejection",
			err: fmt.Errorf("token refresh failed after 3 attempts: %w", fmt.Errorf(`token refresh failed with status 401: {
"error": {
"message": "Could not validate your refresh token. Please try signing in again.",
"type": "invalid_request_error",
"param": null,
"code": "invalid_refresh_token"
}
}`)),
		},
		{
			name: "invalid_refresh_token",
			err:  &Error{HTTPStatus: http.StatusUnauthorized, Code: "invalid_refresh_token"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			manager := NewManager(nil, &RoundRobinSelector{}, nil)
			executor := &mockOAuthErrorExecutor{id: "test-provider", errToReturn: tc.err}
			manager.RegisterExecutor(executor)

			auth := &Auth{
				ID:       "disabled-" + tc.name,
				Provider: "test-provider",
				Disabled: true,
				Status:   StatusDisabled,
				Metadata: map[string]any{
					"access_token":  "expired-token",
					"refresh_token": "refresh-1",
				},
			}
			if _, err := manager.Register(ctx, auth); err != nil {
				t.Fatalf("Register error: %v", err)
			}

			if _, errRefresh := manager.refreshAuthForRequest(ctx, auth.ID, ""); errRefresh == nil {
				t.Fatal("expected refresh error, got nil")
			}

			current, ok := manager.GetByID(auth.ID)
			if !ok {
				t.Fatalf("auth %q not found in manager", auth.ID)
			}
			if current.Status != StatusDisabled {
				t.Fatalf("auth status = %v, want StatusDisabled", current.Status)
			}
			if !current.NextRefreshAfter.IsZero() {
				t.Fatalf("NextRefreshAfter = %v, want zero time (never refresh)", current.NextRefreshAfter)
			}
			if _, scheduled := nextRefreshCheckAt(time.Now(), current, time.Second); scheduled {
				t.Fatal("terminal refresh failure remained scheduled")
			}
			if tc.name != "invalid_grant" {
				if current.LastError.HTTPStatus != 401 || current.LastError.Code != "invalid_refresh_token" {
					t.Fatalf("normalization lost the refresh rejection: %+v", current.LastError)
				}
				if HasDisabledInvalidGrantFailure(current) {
					t.Fatal("invalid refresh token broadened the existing public grant matcher")
				}
			}
			if manager.shouldRefresh(current, time.Now().Add(24*time.Hour)) {
				t.Fatal("terminal failure became eligible after backoff")
			}
			if job := manager.markRefreshPending(nil, current.ID, current.RegistrationEpoch, time.Now().Add(24*time.Hour)); job != nil {
				t.Fatal("terminal failure queued a refresh job")
			}

			callsBefore := executor.refreshCalls.Load()
			if _, errSecond := manager.refreshAuthForRequest(ctx, auth.ID, ""); errSecond == nil {
				t.Fatal("expected second call to fail")
			}
			if executor.refreshCalls.Load() != callsBefore {
				t.Fatalf("executor was called again after terminal %s failure, calls=%d", tc.name, executor.refreshCalls.Load())
			}
		})
	}
}

func TestRefreshAuthForRequest_EnabledAuth_InvalidGrant_ExponentialBackoff(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	invalidGrantErr := oauthStatusError{
		code: http.StatusBadRequest,
		msg:  `{"error": "invalid_grant", "error_description": "Bad Request"}`,
	}
	executor := &mockOAuthErrorExecutor{
		id:          "test-provider",
		errToReturn: invalidGrantErr,
	}
	manager.RegisterExecutor(executor)

	now := time.Now()
	expiredAt := now.Add(-time.Hour).Format(time.RFC3339)
	auth := &Auth{
		ID:       "enabled-auth-invalid-grant-exp",
		Provider: "test-provider",
		Disabled: false,
		Status:   StatusActive,
		Metadata: map[string]any{
			"access_token":  "expired-token",
			"refresh_token": "refresh-1",
			"expires_at":    expiredAt,
		},
	}
	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatalf("Register error: %v", err)
	}

	// 1st failure: backoff 1m
	t1 := time.Now()
	_, _ = manager.refreshAuthForRequest(ctx, auth.ID, "")
	manager.mu.RLock()
	current := manager.auths[auth.ID]
	manager.mu.RUnlock()
	if current.Status != StatusError {
		t.Fatalf("attempt 1: status = %v, want StatusError", current.Status)
	}
	if current.RefreshFailures != 1 {
		t.Fatalf("attempt 1: RefreshFailures = %d, want 1", current.RefreshFailures)
	}
	diff1 := current.NextRefreshAfter.Sub(t1)
	if diff1 < 50*time.Second || diff1 > 70*time.Second {
		t.Fatalf("attempt 1: backoff diff = %v, want ~1m", diff1)
	}

	// 2nd failure: backoff 2m
	t2 := time.Now()
	_, _ = manager.refreshAuthForRequest(ctx, auth.ID, "")
	manager.mu.RLock()
	current = manager.auths[auth.ID]
	manager.mu.RUnlock()
	if current.RefreshFailures != 2 {
		t.Fatalf("attempt 2: RefreshFailures = %d, want 2", current.RefreshFailures)
	}
	diff2 := current.NextRefreshAfter.Sub(t2)
	if diff2 < 110*time.Second || diff2 > 130*time.Second {
		t.Fatalf("attempt 2: backoff diff = %v, want ~2m", diff2)
	}

	// 3rd failure: backoff 4m
	t3 := time.Now()
	_, _ = manager.refreshAuthForRequest(ctx, auth.ID, "")
	manager.mu.RLock()
	current = manager.auths[auth.ID]
	manager.mu.RUnlock()
	if current.RefreshFailures != 3 {
		t.Fatalf("attempt 3: RefreshFailures = %d, want 3", current.RefreshFailures)
	}
	diff3 := current.NextRefreshAfter.Sub(t3)
	if diff3 < 230*time.Second || diff3 > 250*time.Second {
		t.Fatalf("attempt 3: backoff diff = %v, want ~4m", diff3)
	}
}

func TestManager_AutoRefreshLoop_DisabledAuthWithInvalidGrantNeverRefreshes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	executor := &mockOAuthErrorExecutor{id: "test-provider"}
	manager.RegisterExecutor(executor)

	now := time.Now()
	expiredAt := now.Add(-time.Hour).Format(time.RFC3339)
	auth := &Auth{
		ID:       "disabled-invalid-grant-loop",
		Provider: "test-provider",
		Disabled: true,
		Status:   StatusDisabled,
		LastError: &Error{
			HTTPStatus: 400,
			Message:    `{"error": "invalid_grant", "error_description": "Bad Request"}`,
		},
		Metadata: map[string]any{
			"access_token":  "expired-token",
			"refresh_token": "refresh-1",
			"expires_at":    expiredAt,
		},
	}
	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatalf("Register error: %v", err)
	}

	manager.StartAutoRefresh(ctx, 10*time.Millisecond)
	defer manager.StopAutoRefresh()

	// Wait 100ms to allow refresh loop cycles
	time.Sleep(100 * time.Millisecond)

	if calls := executor.refreshCalls.Load(); calls != 0 {
		t.Fatalf("executor.Refresh called %d times for disabled+invalid_grant auth, want 0", calls)
	}
}

func TestRefreshAuthForRequest_RawFmtError_RecognizesInvalidGrant(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	// Error created via fmt.Errorf without implementing StatusCode()
	rawErr := fmt.Errorf("oauth token refresh failed: invalid_grant: account checkpoint required")
	executor := &mockOAuthErrorExecutor{
		id:          "test-provider",
		errToReturn: rawErr,
	}
	manager.RegisterExecutor(executor)

	now := time.Now()
	expiredAt := now.Add(-time.Hour).Format(time.RFC3339)
	auth := &Auth{
		ID:       "raw-fmt-invalid-grant",
		Provider: "test-provider",
		Disabled: false,
		Status:   StatusActive,
		Metadata: map[string]any{
			"access_token":  "expired-token",
			"refresh_token": "refresh-1",
			"expires_at":    expiredAt,
		},
	}
	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatalf("Register error: %v", err)
	}

	_, _ = manager.refreshAuthForRequest(ctx, auth.ID, "")
	manager.mu.RLock()
	current := manager.auths[auth.ID]
	manager.mu.RUnlock()

	if current.RefreshFailures != 1 {
		t.Fatalf("RefreshFailures = %d, want 1 (should recognize raw fmt.Errorf invalid_grant)", current.RefreshFailures)
	}
	diff := current.NextRefreshAfter.Sub(now)
	if diff < 50*time.Second || diff > 70*time.Second {
		t.Fatalf("backoff diff = %v, want ~1m", diff)
	}
}
