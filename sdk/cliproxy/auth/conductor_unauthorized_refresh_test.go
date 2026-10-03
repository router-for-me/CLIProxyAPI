package auth

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type unauthorizedRefreshExecutor struct {
	id string

	mu            sync.Mutex
	executeCalls  []string
	streamCalls   []string
	refreshCalls  int
	tokenInvalid  map[string]struct{}
	refreshFail   bool
	refreshErr    error
	refreshTokens map[string]string
	onExecute     func(authID string)
	onRefresh     func()
}

func (e *unauthorizedRefreshExecutor) Identifier() string { return e.id }

func (e *unauthorizedRefreshExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.executeCalls = append(e.executeCalls, auth.ID)
	token := authAccessToken(auth)
	_, invalid := e.tokenInvalid[token]
	onExec := e.onExecute
	e.mu.Unlock()
	if onExec != nil {
		onExec(auth.ID)
	}
	if invalid {
		return cliproxyexecutor.Response{}, &Error{
			HTTPStatus: http.StatusUnauthorized,
			Message:    "Your authentication token has been invalidated. Please try signing in again.",
		}
	}
	return cliproxyexecutor.Response{Payload: []byte(auth.ID + ":" + token)}, nil
}

func (e *unauthorizedRefreshExecutor) ExecuteStream(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.mu.Lock()
	e.streamCalls = append(e.streamCalls, auth.ID)
	token := authAccessToken(auth)
	_, invalid := e.tokenInvalid[token]
	onExec := e.onExecute
	e.mu.Unlock()
	if onExec != nil {
		onExec(auth.ID)
	}
	if invalid {
		return nil, &Error{
			HTTPStatus: http.StatusUnauthorized,
			Message:    "Your authentication token has been invalidated. Please try signing in again.",
		}
	}
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte(auth.ID + ":" + token)}
	close(ch)
	return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Auth": {auth.ID}}, Chunks: ch}, nil
}

func (e *unauthorizedRefreshExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	e.mu.Lock()
	e.refreshCalls++
	onRef := e.onRefresh
	err := e.refreshErr
	fail := e.refreshFail
	next := e.refreshTokens[auth.ID]
	e.mu.Unlock()
	if onRef != nil {
		onRef()
	}
	if err != nil {
		return nil, err
	}
	if fail {
		return nil, &Error{HTTPStatus: http.StatusUnauthorized, Message: "refresh token invalid"}
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	if next == "" {
		next = "refreshed-access-token"
	}
	auth.Metadata["access_token"] = next
	return auth, nil
}

func (e *unauthorizedRefreshExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func (e *unauthorizedRefreshExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *unauthorizedRefreshExecutor) ExecuteCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.executeCalls))
	copy(out, e.executeCalls)
	return out
}

func (e *unauthorizedRefreshExecutor) StreamCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.streamCalls))
	copy(out, e.streamCalls)
	return out
}

func (e *unauthorizedRefreshExecutor) RefreshCalls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.refreshCalls
}

func newUnauthorizedRefreshFixture(t *testing.T, refreshFail bool) (*Manager, *unauthorizedRefreshExecutor, *Auth, *Auth, string) {
	t.Helper()

	model := "gpt-5.5"
	primary := &Auth{
		ID:       "aa-primary",
		Provider: "codex",
		Metadata: map[string]any{
			"access_token":  "stale-access-token",
			"refresh_token": "primary-refresh-token",
		},
	}
	backup := &Auth{
		ID:       "bb-backup",
		Provider: "codex",
		Metadata: map[string]any{
			"access_token":  "backup-access-token",
			"refresh_token": "backup-refresh-token",
		},
	}

	executor := &unauthorizedRefreshExecutor{
		id: "codex",
		tokenInvalid: map[string]struct{}{
			"stale-access-token": {},
		},
		refreshFail: refreshFail,
		refreshTokens: map[string]string{
			primary.ID: "fresh-access-token",
		},
	}

	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(primary.ID, "codex", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(backup.ID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(primary.ID)
		reg.UnregisterClient(backup.ID)
	})

	if _, errRegister := m.Register(context.Background(), primary); errRegister != nil {
		t.Fatalf("register primary: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), backup); errRegister != nil {
		t.Fatalf("register backup: %v", errRegister)
	}

	return m, executor, primary, backup, model
}

func TestManager_Execute_UnauthorizedRefreshesCurrentAuthBeforeFallback(t *testing.T) {
	m, executor, primary, backup, model := newUnauthorizedRefreshFixture(t, false)

	resp, errExecute := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("Execute error = %v, want success on refreshed primary", errExecute)
	}
	if got := string(resp.Payload); got != primary.ID+":fresh-access-token" {
		t.Fatalf("payload = %q, want refreshed primary response", got)
	}

	if got := executor.RefreshCalls(); got != 1 {
		t.Fatalf("Refresh calls = %d, want 1", got)
	}
	if got := executor.ExecuteCalls(); len(got) != 2 || got[0] != primary.ID || got[1] != primary.ID {
		t.Fatalf("Execute calls = %v, want [primary, primary]", got)
	}
	for _, id := range executor.ExecuteCalls() {
		if id == backup.ID {
			t.Fatalf("backup auth should not be used when refresh recovers primary")
		}
	}

	updated, ok := m.GetByID(primary.ID)
	if !ok || updated == nil {
		t.Fatalf("primary auth missing after refresh")
	}
	if got := authAccessToken(updated); got != "fresh-access-token" {
		t.Fatalf("primary access_token = %q, want fresh-access-token", got)
	}
	if state := updated.ModelStates[model]; state != nil && state.Unavailable {
		t.Fatalf("primary model should not remain suspended after successful refresh retry")
	}
}

func TestManager_ExecuteStream_UnauthorizedRefreshesCurrentAuthBeforeFallback(t *testing.T) {
	m, executor, primary, backup, model := newUnauthorizedRefreshFixture(t, false)

	stream, errStream := m.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errStream != nil {
		t.Fatalf("ExecuteStream error = %v, want success on refreshed primary", errStream)
	}
	if stream == nil || stream.Chunks == nil {
		t.Fatalf("expected stream result")
	}
	chunk, ok := <-stream.Chunks
	if !ok {
		t.Fatalf("expected stream chunk")
	}
	if chunk.Err != nil {
		t.Fatalf("stream chunk error = %v", chunk.Err)
	}
	if got := string(chunk.Payload); got != primary.ID+":fresh-access-token" {
		t.Fatalf("stream payload = %q, want refreshed primary response", got)
	}

	if got := executor.RefreshCalls(); got != 1 {
		t.Fatalf("Refresh calls = %d, want 1", got)
	}
	if got := executor.StreamCalls(); len(got) != 2 || got[0] != primary.ID || got[1] != primary.ID {
		t.Fatalf("Stream calls = %v, want [primary, primary]", got)
	}
	for _, id := range executor.StreamCalls() {
		if id == backup.ID {
			t.Fatalf("backup auth should not be used when refresh recovers primary")
		}
	}
}

func TestManager_Execute_RejectedTokenWithInvalidGrantStopsSelectingAuth(t *testing.T) {
	m, executor, primary, backup, model := newUnauthorizedRefreshFixture(t, false)
	executor.mu.Lock()
	executor.refreshErr = errors.New(`token refresh failed with status 400: {"error": "invalid_grant", "error_description": "Refresh token not found or invalid"}`)
	executor.mu.Unlock()
	// The revoked access token still has a future expiry, as in production.
	updated, ok := m.GetByID(primary.ID)
	if !ok || updated == nil {
		t.Fatal("primary auth missing")
	}
	updated.Metadata["expired"] = time.Now().Add(6 * time.Hour).Format(time.RFC3339)
	if _, errUpdate := m.Update(context.Background(), updated); errUpdate != nil {
		t.Fatalf("update primary: %v", errUpdate)
	}

	for i := 0; i < 2; i++ {
		resp, errExecute := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
		if errExecute != nil {
			t.Fatalf("Execute %d error = %v, want success via backup", i, errExecute)
		}
		if got := string(resp.Payload); got != backup.ID+":backup-access-token" {
			t.Fatalf("Execute %d payload = %q, want backup response", i, got)
		}
	}

	primaryCalls := 0
	for _, id := range executor.ExecuteCalls() {
		if id == primary.ID {
			primaryCalls++
		}
	}
	if primaryCalls != 1 {
		t.Fatalf("primary executions = %d, want 1; calls = %v", primaryCalls, executor.ExecuteCalls())
	}
	if got := executor.RefreshCalls(); got != 1 {
		t.Fatalf("Refresh calls = %d, want 1", got)
	}
	final, ok := m.GetByID(primary.ID)
	if !ok || final == nil {
		t.Fatal("primary auth missing after refresh failure")
	}
	if !hasUnauthorizedAuthFailure(final) {
		t.Fatalf("expected terminal unauthorized state, got unavailable=%v status=%s next_refresh=%v last_error=%+v", final.Unavailable, final.Status, final.NextRefreshAfter, final.LastError)
	}
}

func TestManager_MarkResult_InFlightResultDoesNotReviveTerminalUnauthorizedAuth(t *testing.T) {
	m, executor, primary, backup, model := newUnauthorizedRefreshFixture(t, false)
	executor.mu.Lock()
	executor.refreshErr = errors.New(`token refresh failed with status 400: {"error": "invalid_grant", "error_description": "Refresh token not found or invalid"}`)
	executor.mu.Unlock()

	// Initial request triggers 401 + invalid_grant -> terminal unauthorized.
	_, errExecute := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("Execute error = %v, want success via backup", errExecute)
	}

	authAfterRefresh, ok := m.GetByID(primary.ID)
	if !ok || authAfterRefresh == nil {
		t.Fatal("primary auth missing")
	}
	if !hasUnauthorizedAuthFailure(authAfterRefresh) {
		t.Fatalf("expected terminal unauthorized state initially")
	}

	// Simulate an in-flight request on primary finishing with 500 Internal Server Error.
	m.MarkResult(context.Background(), Result{
		AuthID:   primary.ID,
		Provider: "codex",
		Model:    model,
		Success:  false,
		Error: &Error{
			HTTPStatus: http.StatusInternalServerError,
			Code:       "internal_error",
			Message:    "internal server error",
		},
	})

	authAfter500, ok := m.GetByID(primary.ID)
	if !ok || authAfter500 == nil {
		t.Fatal("primary auth missing")
	}
	if !hasUnauthorizedAuthFailure(authAfter500) {
		t.Fatalf("expected terminal unauthorized state preserved after in-flight 500, got status=%s unavailable=%v last_error=%+v", authAfter500.Status, authAfter500.Unavailable, authAfter500.LastError)
	}

	// Simulate another in-flight request finishing with Success.
	m.MarkResult(context.Background(), Result{
		AuthID:   primary.ID,
		Provider: "codex",
		Model:    model,
		Success:  true,
	})

	authAfterSuccess, ok := m.GetByID(primary.ID)
	if !ok || authAfterSuccess == nil {
		t.Fatal("primary auth missing")
	}
	if !hasUnauthorizedAuthFailure(authAfterSuccess) {
		t.Fatalf("expected terminal unauthorized state preserved after in-flight success, got status=%s unavailable=%v last_error=%+v", authAfterSuccess.Status, authAfterSuccess.Unavailable, authAfterSuccess.LastError)
	}

	// Reset any model cooldowns / advance time.
	_, _, _ = m.ResetQuota(context.Background(), primary.ID)

	// Execute again: primary MUST NOT be selected.
	callsBefore := len(executor.ExecuteCalls())
	resp, errSecond := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errSecond != nil {
		t.Fatalf("second execute error: %v", errSecond)
	}
	if got := string(resp.Payload); got != backup.ID+":backup-access-token" {
		t.Fatalf("got payload %q, want backup", got)
	}
	for _, id := range executor.ExecuteCalls()[callsBefore:] {
		if id == primary.ID {
			t.Fatalf("primary was selected again after in-flight result!")
		}
	}
}

func TestManager_ConcurrentUnauthorized_BarrierExecutionPreservesTerminalState(t *testing.T) {
	model := "gpt-5.5"
	primary := &Auth{
		ID:       "aa-primary",
		Provider: "codex",
		Metadata: map[string]any{
			"access_token":  "stale-access-token",
			"refresh_token": "primary-refresh-token",
			"expired":       time.Now().Add(6 * time.Hour).Format(time.RFC3339),
		},
	}
	backup := &Auth{
		ID:       "bb-backup",
		Provider: "codex",
		Metadata: map[string]any{
			"access_token":  "backup-access-token",
			"refresh_token": "backup-refresh-token",
		},
	}

	executor := &unauthorizedRefreshExecutor{
		id: "codex",
		tokenInvalid: map[string]struct{}{
			"stale-access-token": {},
		},
		refreshErr: errors.New(`token refresh failed with status 400: {"error": "invalid_grant", "error_description": "Refresh token not found or invalid"}`),
	}

	m := NewManager(nil, &FillFirstSelector{}, nil)
	m.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(primary.ID, "codex", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(backup.ID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(primary.ID)
		reg.UnregisterClient(backup.ID)
	})

	if _, err := m.Register(context.Background(), primary); err != nil {
		t.Fatalf("register primary: %v", err)
	}
	if _, err := m.Register(context.Background(), backup); err != nil {
		t.Fatalf("register backup: %v", err)
	}

	// Channel-based synchronization barrier:
	// Hold both requests when executing primary until both have entered.
	primaryEntered := make(chan struct{}, 2)
	releasePrimary := make(chan struct{})
	executor.mu.Lock()
	executor.onExecute = func(authID string) {
		if authID == primary.ID {
			primaryEntered <- struct{}{}
			<-releasePrimary
		}
	}
	executor.mu.Unlock()

	var wg sync.WaitGroup
	wg.Add(2)
	results := make([]string, 2)
	errs := make([]error, 2)

	for i := 0; i < 2; i++ {
		idx := i
		go func() {
			defer wg.Done()
			resp, err := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
			errs[idx] = err
			if err == nil {
				results[idx] = string(resp.Payload)
			}
		}()
	}

	// Wait until both concurrent goroutines have arrived at executing primary with timeout protection.
	timeout := time.After(5 * time.Second)
	for i := 0; i < 2; i++ {
		select {
		case <-primaryEntered:
		case <-timeout:
			t.Fatal("timed out waiting for concurrent executions to enter primary")
		}
	}
	// Release both goroutines to receive 401 concurrently.
	close(releasePrimary)

	waitDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for concurrent executions to complete")
	}

	for i := 0; i < 2; i++ {
		if errs[i] != nil {
			t.Fatalf("goroutine %d error = %v, want success via backup", i, errs[i])
		}
		if results[i] != backup.ID+":backup-access-token" {
			t.Fatalf("goroutine %d payload = %q, want backup response", i, results[i])
		}
	}

	// Only 1 refresh attempt should have occurred for the invalidated primary token.
	if got := executor.RefreshCalls(); got != 1 {
		t.Fatalf("expected 1 refresh call during concurrent execution, got %d", got)
	}

	final, ok := m.GetByID(primary.ID)
	if !ok || final == nil {
		t.Fatal("primary auth missing")
	}
	if !hasUnauthorizedAuthFailure(final) {
		t.Fatalf("expected terminal unauthorized state after concurrent execution, got unavailable=%v status=%s next_refresh=%v last_error=%+v",
			final.Unavailable, final.Status, final.NextRefreshAfter, final.LastError)
	}

	// Explicitly verify background and request-triggered refresh do NOT call executor.Refresh on terminal unauthorized auth.
	callsBefore := executor.RefreshCalls()
	m.refreshAuth(context.Background(), primary.ID)
	if got := executor.RefreshCalls(); got != callsBefore {
		t.Fatalf("background refreshAuth called executor.Refresh on terminal unauthorized auth (%d -> %d)", callsBefore, got)
	}

	_, refreshed := m.tryRefreshAfterUnauthorized(context.Background(), final, &Error{HTTPStatus: http.StatusUnauthorized, Message: "401"}, false)
	if refreshed {
		t.Fatal("tryRefreshAfterUnauthorized should not report refreshed for terminal unauthorized auth")
	}
	if got := executor.RefreshCalls(); got != callsBefore {
		t.Fatalf("tryRefreshAfterUnauthorized called executor.Refresh on terminal unauthorized auth (%d -> %d)", callsBefore, got)
	}

	// Verify that manual ForceRefreshAuth DOES call executor.Refresh.
	_, _ = m.ForceRefreshAuth(context.Background(), primary.ID)
	if got := executor.RefreshCalls(); got != callsBefore+1 {
		t.Fatalf("ForceRefreshAuth should call executor.Refresh, got %d calls, want %d", got, callsBefore+1)
	}

	// Verify that a subsequent request still does not touch primary.
	execCallsBefore := len(executor.ExecuteCalls())
	resp3, err3 := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err3 != nil {
		t.Fatalf("Execute 3 error: %v", err3)
	}
	if string(resp3.Payload) != backup.ID+":backup-access-token" {
		t.Fatalf("Execute 3 want backup, got %s", resp3.Payload)
	}
	for _, id := range executor.ExecuteCalls()[execCallsBefore:] {
		if id == primary.ID {
			t.Fatalf("primary was selected again after concurrent execution!")
		}
	}
}

func TestManager_ForceRefreshAuth_FailurePreservesTerminalUnauthorizedState(t *testing.T) {
	m, executor, primary, backup, model := newUnauthorizedRefreshFixture(t, false)

	// Primary has future nominal expiry.
	updated, ok := m.GetByID(primary.ID)
	if !ok || updated == nil {
		t.Fatal("primary auth missing")
	}
	updated.Metadata["expired"] = time.Now().Add(6 * time.Hour).Format(time.RFC3339)
	if _, errUpdate := m.Update(context.Background(), updated); errUpdate != nil {
		t.Fatalf("update primary: %v", errUpdate)
	}

	executor.mu.Lock()
	executor.refreshErr = errors.New(`token refresh failed with status 400: {"error": "invalid_grant", "error_description": "Refresh token not found or invalid"}`)
	executor.mu.Unlock()

	// Initial request triggers 401 + invalid_grant -> terminal unauthorized.
	_, errExecute := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("Execute error: %v", errExecute)
	}

	authAfterInit, _ := m.GetByID(primary.ID)
	if !hasUnauthorizedAuthFailure(authAfterInit) {
		t.Fatalf("expected terminal unauthorized state initially")
	}

	// 1. Manually force refresh, but the refresh fails with 503 Service Unavailable.
	executor.mu.Lock()
	executor.refreshErr = errors.New("upstream 503 service unavailable")
	executor.mu.Unlock()

	_, errForce := m.ForceRefreshAuth(context.Background(), primary.ID)
	if errForce == nil {
		t.Fatal("expected ForceRefreshAuth to fail")
	}

	authAfterFailedForce, _ := m.GetByID(primary.ID)
	if !hasUnauthorizedAuthFailure(authAfterFailedForce) {
		t.Fatalf("expected terminal unauthorized state to be preserved after failed force refresh, got unavailable=%v status=%s next_refresh=%v last_error=%+v",
			authAfterFailedForce.Unavailable, authAfterFailedForce.Status, authAfterFailedForce.NextRefreshAfter, authAfterFailedForce.LastError)
	}
	if !authAfterFailedForce.NextRefreshAfter.IsZero() {
		t.Fatalf("expected NextRefreshAfter to remain zero after failed force refresh, got %v", authAfterFailedForce.NextRefreshAfter)
	}

	// Selection still skips primary.
	resp, errAfterFailedForce := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errAfterFailedForce != nil {
		t.Fatalf("Execute error: %v", errAfterFailedForce)
	}
	if string(resp.Payload) != backup.ID+":backup-access-token" {
		t.Fatalf("expected backup selection, got %s", resp.Payload)
	}

	// 2. Now force refresh succeeds with a valid token.
	executor.mu.Lock()
	executor.refreshErr = nil
	executor.refreshTokens[primary.ID] = "newly-minted-token"
	executor.mu.Unlock()

	refreshed, errSuccess := m.ForceRefreshAuth(context.Background(), primary.ID)
	if errSuccess != nil {
		t.Fatalf("ForceRefreshAuth should succeed, got: %v", errSuccess)
	}
	if refreshed.Status != StatusActive {
		t.Fatalf("expected StatusActive after successful force refresh, got %s", refreshed.Status)
	}
	if refreshed.Unavailable {
		t.Fatal("expected Unavailable=false after successful force refresh")
	}
	if hasUnauthorizedAuthFailure(refreshed) {
		t.Fatal("hasUnauthorizedAuthFailure should be false after successful force refresh")
	}
}

func TestManager_Execute_UnauthorizedRefreshFailureFallsBackToNextAuth(t *testing.T) {
	m, executor, primary, backup, model := newUnauthorizedRefreshFixture(t, true)

	resp, errExecute := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("Execute error = %v, want success via backup", errExecute)
	}
	if got := string(resp.Payload); got != backup.ID+":backup-access-token" {
		t.Fatalf("payload = %q, want backup response", got)
	}

	if got := executor.RefreshCalls(); got != 1 {
		t.Fatalf("Refresh calls = %d, want 1", got)
	}
	if got := executor.ExecuteCalls(); len(got) != 2 || got[0] != primary.ID || got[1] != backup.ID {
		t.Fatalf("Execute calls = %v, want [primary, backup]", got)
	}

	updated, ok := m.GetByID(primary.ID)
	if !ok || updated == nil {
		t.Fatalf("primary auth missing after failed refresh")
	}
	state := updated.ModelStates[model]
	if state == nil || !state.Unavailable {
		t.Fatalf("expected primary model to be suspended after refresh failure")
	}
	if state.StatusMessage != "unauthorized" && (state.LastError == nil || state.LastError.StatusCode() != http.StatusUnauthorized) {
		t.Fatalf("expected unauthorized suspension, got state=%+v", state)
	}
}

func TestManager_Execute_UnauthorizedWithoutRefreshTokenDoesNotCallRefresh(t *testing.T) {
	model := "gpt-5.5"
	primary := &Auth{
		ID:       "aa-primary-api-key",
		Provider: "codex",
		Metadata: map[string]any{
			"access_token": "stale-access-token",
		},
	}
	backup := &Auth{
		ID:       "bb-backup-api-key",
		Provider: "codex",
		Metadata: map[string]any{
			"access_token": "backup-access-token",
		},
	}
	executor := &unauthorizedRefreshExecutor{
		id: "codex",
		tokenInvalid: map[string]struct{}{
			"stale-access-token": {},
		},
	}
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(primary.ID, "codex", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(backup.ID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(primary.ID)
		reg.UnregisterClient(backup.ID)
	})
	if _, errRegister := m.Register(context.Background(), primary); errRegister != nil {
		t.Fatalf("register primary: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), backup); errRegister != nil {
		t.Fatalf("register backup: %v", errRegister)
	}

	resp, errExecute := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("Execute error = %v, want success via backup", errExecute)
	}
	if got := string(resp.Payload); got != backup.ID+":backup-access-token" {
		t.Fatalf("payload = %q, want backup response", got)
	}
	if got := executor.RefreshCalls(); got != 0 {
		t.Fatalf("Refresh calls = %d, want 0 when no refresh_token is present", got)
	}
	if got := executor.ExecuteCalls(); len(got) != 2 || got[0] != primary.ID || got[1] != backup.ID {
		t.Fatalf("Execute calls = %v, want [primary, backup]", got)
	}
}

func TestManager_Execute_UnauthorizedRefreshThenRetryStillFailsFallsBackOnce(t *testing.T) {
	m, executor, primary, backup, model := newUnauthorizedRefreshFixture(t, false)
	// Refresh "succeeds" but hands back another invalidated token.
	executor.refreshTokens[primary.ID] = "still-invalid-token"
	executor.mu.Lock()
	executor.tokenInvalid["still-invalid-token"] = struct{}{}
	executor.mu.Unlock()

	resp, errExecute := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("Execute error = %v, want success via backup", errExecute)
	}
	if got := string(resp.Payload); got != backup.ID+":backup-access-token" {
		t.Fatalf("payload = %q, want backup response", got)
	}
	if got := executor.RefreshCalls(); got != 1 {
		t.Fatalf("Refresh calls = %d, want 1 (no refresh loop)", got)
	}
	if got := executor.ExecuteCalls(); len(got) != 3 || got[0] != primary.ID || got[1] != primary.ID || got[2] != backup.ID {
		t.Fatalf("Execute calls = %v, want [primary, primary, backup]", got)
	}
}
