package auth

import (
	"context"
	"encoding/base64"
	"errors"
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

func TestRefreshAuthForRequest_DisabledAuth_InvalidGrant_NeverRetries(t *testing.T) {
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

	auth := &Auth{
		ID:       "disabled-invalid-grant",
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

	// 1st call encounters invalid_grant
	_, errRefresh := manager.refreshAuthForRequest(ctx, auth.ID, "")
	if errRefresh == nil {
		t.Fatalf("expected refresh error, got nil")
	}

	manager.mu.RLock()
	current := manager.auths[auth.ID]
	manager.mu.RUnlock()

	if current == nil {
		t.Fatalf("auth not found in manager")
	}
	// Disabled + invalid_grant must never schedule retry
	if current.Status != StatusDisabled {
		t.Fatalf("auth status = %v, want StatusDisabled", current.Status)
	}
	if !current.NextRefreshAfter.IsZero() {
		t.Fatalf("NextRefreshAfter = %v, want zero time (never refresh)", current.NextRefreshAfter)
	}

	// 2nd call should be blocked immediately without calling executor
	callsBefore := executor.refreshCalls.Load()
	_, errSecond := manager.refreshAuthForRequest(ctx, auth.ID, "")
	if errSecond == nil {
		t.Fatalf("expected second call to fail, got nil")
	}
	if executor.refreshCalls.Load() != callsBefore {
		t.Fatalf("executor was called again for disabled+invalid_grant, calls=%d", executor.refreshCalls.Load())
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

func TestDisabledCodexRejectedRefreshTokenDoesNotSpin(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	lead := CodexOuterRefreshLead
	setRefreshLeadFactory(t, "codex", func() *time.Duration { return &lead })

	// Access token still valid for 66h, i.e. inside the 96h CodexOuterRefreshLead window.
	exp := now.Add(66 * time.Hour)
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, exp.Unix())))
	accessToken := "eyJhbGciOiJub25lIn0." + payload + ".sig"

	a := &Auth{
		ID:       "codex-disabled.json",
		FileName: "codex-disabled.json",
		Provider: "codex",
		Status:   StatusActive,
		Disabled: true,
		Metadata: map[string]any{
			"type":          "codex",
			"access_token":  accessToken,
			"refresh_token": "rt",
			"disabled":      true,
		},
	}
	ex := &unauthorizedRefreshExecutor{
		id: "codex",
		refreshErr: errors.New(
			`token refresh failed with status 401: {"error":{"message":"Could not validate your refresh token. Please try signing in again.","type":"invalid_request_error","code":"invalid_refresh_token"}}`,
		),
	}
	m := NewManager(nil, &FillFirstSelector{}, nil)
	m.RegisterExecutor(ex)
	if _, errReg := m.Register(ctx, a); errReg != nil {
		t.Fatalf("Register error: %v", errReg)
	}

	// 1. First refresh attempt fails terminally; NextRefreshAfter must be deferred to exp (not 5m backoff).
	m.refreshAuth(ctx, a.ID)
	got, _ := m.GetByID(a.ID)
	if got == nil {
		t.Fatal("auth not found after refreshAuth")
	}
	if !got.Disabled || got.Status != StatusDisabled || !got.Unavailable {
		t.Fatalf("status = %s disabled=%v unavailable=%v, want disabled/unavailable", got.Status, got.Disabled, got.Unavailable)
	}
	if !got.RequiresSignIn || got.RejectedRefreshToken != "rt" {
		t.Fatalf("RequiresSignIn=%v RejectedRefreshToken=%q, want true / \"rt\"", got.RequiresSignIn, got.RejectedRefreshToken)
	}
	if !got.NextRefreshAfter.Equal(exp) {
		t.Fatalf("NextRefreshAfter = %v, want access token expiry %v", got.NextRefreshAfter, exp)
	}

	// 2. Even if NextRefreshAfter has elapsed while the access token is still valid inside the 96h lead window,
	// a refresh job must restore NextRefreshAfter=exp and the scheduler must not spin or re-trigger before exp.
	m.mu.Lock()
	m.auths[a.ID].NextRefreshAfter = now.Add(-time.Second)
	epoch := m.auths[a.ID].RegistrationEpoch
	m.mu.Unlock()

	job := m.markRefreshPending(nil, a.ID, epoch, now)
	if job == nil {
		t.Fatal("expected markRefreshPending to create job when NextRefreshAfter is in the past")
	}
	if !m.beginRefreshJob(ctx, job) {
		t.Fatal("expected beginRefreshJob to succeed")
	}
	_, _ = m.refreshAuthForRequestAtEpoch(ctx, a.ID, "", epoch)
	m.finishRefreshJob(job, time.Time{}, false)

	afterJob, _ := m.GetByID(a.ID)
	if !afterJob.NextRefreshAfter.Equal(exp) {
		t.Fatalf("after job completion NextRefreshAfter = %v, want %v (must not be zeroed by finishRefreshJob)", afterJob.NextRefreshAfter, exp)
	}
	if ex.RefreshCalls() != 1 {
		t.Fatalf("upstream RefreshCalls = %d, want 1", ex.RefreshCalls())
	}

	// 3. Even with NextRefreshAfter cleared, shouldRefresh must be false and nextRefreshCheckAt must defer to exp.
	m.mu.Lock()
	m.auths[a.ID].NextRefreshAfter = time.Time{}
	gen0 := m.auths[a.ID].Generation
	m.mu.Unlock()

	m.mu.RLock()
	cur := m.auths[a.ID]
	shouldRef := m.shouldRefresh(cur, now)
	nextAt, okNext := m.nextRefreshCheckAt(now, cur, refreshCheckInterval)
	m.mu.RUnlock()
	if shouldRef {
		t.Fatal("shouldRefresh at now = true, want false for unchanged rejected refresh token with unexpired access token")
	}
	if !okNext || !nextAt.Equal(exp) {
		t.Fatalf("nextRefreshCheckAt at now = (%v, %v), want (%v, true)", nextAt, okNext, exp)
	}

	loop := newAuthAutoRefreshLoop(m, refreshCheckInterval, 1)
	loop.rebuild(now)
	loop.handleDue(ctx, now)
	if len(loop.jobs) != 0 {
		t.Fatalf("loop queued %d jobs at now, want 0", len(loop.jobs))
	}
	afterLoop, _ := m.GetByID(a.ID)
	if afterLoop.Generation != gen0 {
		t.Fatalf("Generation bumped from %d to %d without due refresh", gen0, afterLoop.Generation)
	}

	// 4. Once the access token expires on the disabled credential, it must be unscheduled and retain StatusDisabled.
	afterExp := exp.Add(time.Second)
	m.mu.RLock()
	cur = m.auths[a.ID]
	shouldRefExpired := m.shouldRefresh(cur, afterExp)
	_, okExpired := m.nextRefreshCheckAt(afterExp, cur, refreshCheckInterval)
	m.mu.RUnlock()
	if shouldRefExpired || okExpired {
		t.Fatalf("at access token expiry for disabled credential: shouldRefresh=%v scheduled=%v, want false/false", shouldRefExpired, okExpired)
	}

	// 5. A disabled credential whose access token is already expired on the first terminal refresh failure
	// must unschedule immediately with NextRefreshAfter=zero and keep StatusDisabled.
	expPast := now.Add(-time.Hour)
	expiredPayload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, expPast.Unix())))
	expiredAccessToken := "eyJhbGciOiJub25lIn0." + expiredPayload + ".sig"
	expiredDisabled := &Auth{
		ID:       "codex-disabled-expired.json",
		FileName: "codex-disabled-expired.json",
		Provider: "codex",
		Status:   StatusActive,
		Disabled: true,
		Metadata: map[string]any{
			"type":          "codex",
			"access_token":  expiredAccessToken,
			"refresh_token": "rt-expired",
			"disabled":      true,
		},
	}
	if _, errRegExp := m.Register(ctx, expiredDisabled); errRegExp != nil {
		t.Fatalf("Register expiredDisabled error: %v", errRegExp)
	}
	m.refreshAuth(ctx, expiredDisabled.ID)
	gotExp, _ := m.GetByID(expiredDisabled.ID)
	if gotExp.Status != StatusDisabled || !gotExp.NextRefreshAfter.IsZero() {
		t.Fatalf("expired disabled terminal refresh: Status=%s NextRefreshAfter=%v, want StatusDisabled and zero time", gotExp.Status, gotExp.NextRefreshAfter)
	}
	if _, ok := m.nextRefreshCheckAt(now, gotExp, refreshCheckInterval); ok {
		t.Fatal("expired disabled credential with rejected refresh token must not be scheduled")
	}
}
