package auth

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type unauthorizedRefreshExecutor struct {
	id string

	mu             sync.Mutex
	executeCalls   []string
	streamCalls    []string
	refreshCalls   int
	tokenInvalid   map[string]struct{}
	refreshFail    bool
	refreshErr     error
	streamChunkErr error
	streamPayloads [][]byte
	lastStreamReq  cliproxyexecutor.Request
	lastStreamOpts cliproxyexecutor.Options
	refreshTokens  map[string]string
	refreshPatch   *codex.CredentialRefreshPatch
	onExecute      func(authID string)
	onRefresh      func()
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

func (e *unauthorizedRefreshExecutor) ExecuteStream(_ context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.mu.Lock()
	e.streamCalls = append(e.streamCalls, auth.ID)
	e.lastStreamReq = req
	e.lastStreamOpts = opts
	token := authAccessToken(auth)
	_, invalid := e.tokenInvalid[token]
	chunkErr := e.streamChunkErr
	customPayloads := e.streamPayloads
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
	if chunkErr != nil {
		ch := make(chan cliproxyexecutor.StreamChunk, 2)
		ch <- cliproxyexecutor.StreamChunk{Payload: []byte(`data: {"type":"response.created"}` + "\n\n")}
		ch <- cliproxyexecutor.StreamChunk{Err: chunkErr}
		close(ch)
		return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Auth": {auth.ID}}, Chunks: ch}, nil
	}
	if len(customPayloads) > 0 {
		ch := make(chan cliproxyexecutor.StreamChunk, len(customPayloads))
		for _, p := range customPayloads {
			ch <- cliproxyexecutor.StreamChunk{Payload: p}
		}
		close(ch)
		return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Auth": {auth.ID}}, Chunks: ch}, nil
	}
	ch := make(chan cliproxyexecutor.StreamChunk, 2)
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte(auth.ID + ":" + token)}
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte(`data: {"type":"response.completed"}` + "\n\n")}
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
	patch := e.refreshPatch
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
	if patch != nil {
		auth.CodexRefreshPatch = patch
	}
	return auth, nil
}

func (e *unauthorizedRefreshExecutor) CountTokens(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{Payload: []byte(`{"total_tokens":5}`)}, nil
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

func TestManagerReloginAfterRefreshFailureRestoresAccount(t *testing.T) {
	invalidGrant := errors.New(`token refresh failed with status 400: {"error": "invalid_grant", "error_description": "Refresh token not found or invalid"}`)
	outage := errors.New("token refresh failed with status 503: upstream connect error")
	for _, tc := range []struct {
		name        string
		refreshErr  error
		rateLimited bool
	}{
		{"invalid grant", invalidGrant, false},
		{"token endpoint 503", outage, false},
		{"active 429 cooldown", outage, true},
		{"active 429 cooldown with invalid grant", invalidGrant, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := WithSkipPersist(context.Background())
			path := filepath.Join(t.TempDir(), "claude-a.json")
			m := NewManager(nil, nil, nil)
			m.RegisterExecutor(&unauthorizedRefreshExecutor{id: "claude", refreshErr: tc.refreshErr})
			if _, errRegister := m.Register(ctx, reloginSnapshot(t, path, "old", time.Now().Add(-time.Hour))); errRegister != nil {
				t.Fatal(errRegister)
			}
			if tc.rateLimited {
				retryAfter := time.Hour
				m.MarkResult(ctx, Result{AuthID: "claude-a.json", Provider: "claude", RetryAfter: &retryAfter, Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "rate limited"}})
			}
			m.refreshAuth(ctx, "claude-a.json") // expired token, refresh fails
			if !tc.rateLimited {
				unchanged := reloginSnapshot(t, path, "old", time.Now().Add(-time.Hour))
				unchanged.Prefix = "kept"
				if _, errUpdate := m.Update(ctx, unchanged); errUpdate != nil {
					t.Fatal(errUpdate)
				}
				carried, _ := m.GetByID("claude-a.json")
				if !carried.Unavailable || carried.Status != StatusError || carried.LastError == nil {
					t.Fatalf("unchanged-credential snapshot dropped refresh failure state: status=%s unavailable=%v last_error=%v",
						carried.Status, carried.Unavailable, carried.LastError)
				}
			}
			if _, errUpdate := m.Update(ctx, reloginSnapshot(t, path, "new", time.Now().Add(8*time.Hour))); errUpdate != nil {
				t.Fatal(errUpdate)
			}
			after, _ := m.GetByID("claude-a.json")
			if tc.rateLimited {
				if !after.Unavailable || !after.NextRetryAfter.After(time.Now()) {
					t.Fatalf("re-login released a live 429 cooldown")
				}
				if after.RefreshFailures != 0 || !after.NextRefreshAfter.IsZero() {
					t.Fatalf("re-login kept stale refresh backoff: refresh_failures=%d next_refresh_after=%v", after.RefreshFailures, after.NextRefreshAfter)
				}
				return
			}
			if after.Unavailable || after.Status != StatusActive || after.LastError != nil || after.RefreshFailures != 0 || !after.NextRefreshAfter.IsZero() {
				t.Fatalf("re-logged account still out of rotation: status=%s msg=%q unavailable=%v refresh_failures=%d next_refresh_after=%v",
					after.Status, after.StatusMessage, after.Unavailable, after.RefreshFailures, after.NextRefreshAfter)
			}
		})
	}
}

// reloginSnapshot writes a Claude auth file and returns it as the watcher reads it.
func reloginSnapshot(t *testing.T, path, token string, expiry time.Time) *Auth {
	t.Helper()
	expired := expiry.Format(time.RFC3339)
	data := []byte(`{"type":"claude","access_token":"` + token + `-at","refresh_token":"` + token + `-rt","expired":"` + expired + `"}`)
	if errWrite := os.WriteFile(path, data, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	a := &Auth{
		ID:         filepath.Base(path),
		Provider:   "claude",
		FileName:   filepath.Base(path),
		Status:     StatusActive,
		Attributes: map[string]string{"path": path, "source": path},
		Metadata:   map[string]any{"type": "claude", "access_token": token + "-at", "refresh_token": token + "-rt", "expired": expired},
	}
	a.BindFileSnapshot(path, data)
	return a
}

func TestManager_CodexTerminalRenewalRecoveryLifecycle_Issue5783(t *testing.T) {
	ctx := WithSkipPersist(context.Background())
	model := "gpt-5.5"
	now := time.Now().UTC()
	futureExpiry := now.Add(24 * time.Hour).Truncate(time.Second)

	primary := &Auth{
		ID:       "aa-codex-primary.json",
		FileName: "aa-codex-primary.json",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":          "codex",
			"email":         "primary@example.com",
			"access_token":  "valid-access-token",
			"refresh_token": "reused-refresh-token",
			"expired":       futureExpiry.Format(time.RFC3339),
		},
	}
	backup := &Auth{
		ID:       "bb-codex-backup.json",
		FileName: "bb-codex-backup.json",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":          "codex",
			"email":         "backup@example.com",
			"access_token":  "backup-access-token",
			"refresh_token": "backup-refresh-token",
			"expired":       now.Add(48 * time.Hour).Format(time.RFC3339),
		},
	}

	executor := &unauthorizedRefreshExecutor{
		id: "codex",
		refreshErr: errors.New(
			`token refresh failed with status 401: {"error":{"message":"Your refresh token has already been used to generate a new access token. Please try signing in again.","type":"invalid_request_error","code":"refresh_token_reused"}}`,
		),
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

	if _, errReg := m.Register(ctx, primary); errReg != nil {
		t.Fatalf("register primary: %v", errReg)
	}
	if _, errReg := m.Register(ctx, backup); errReg != nil {
		t.Fatalf("register backup: %v", errReg)
	}

	// 1. Proactive refresh fails with terminal refresh_token_reused while access_token is still valid.
	m.refreshAuth(ctx, primary.ID)
	if got := executor.RefreshCalls(); got != 1 {
		t.Fatalf("RefreshCalls = %d, want 1", got)
	}

	afterFirstFailure, ok := m.GetByID(primary.ID)
	if !ok || afterFirstFailure == nil {
		t.Fatal("primary auth missing")
	}
	if afterFirstFailure.Unavailable || afterFirstFailure.Status != StatusActive {
		t.Fatalf("expected unexpired access_token to remain active, got status=%s unavailable=%v", afterFirstFailure.Status, afterFirstFailure.Unavailable)
	}
	if !afterFirstFailure.RequiresSignIn || afterFirstFailure.RenewalStateAt(now) != RenewalStateSignInRequired {
		t.Fatalf("RequiresSignIn=%v RenewalState=%q, want true/%q", afterFirstFailure.RequiresSignIn, afterFirstFailure.RenewalStateAt(now), RenewalStateSignInRequired)
	}
	if afterFirstFailure.RefreshError == nil || afterFirstFailure.RefreshError.Code != "refresh_token_reused" {
		t.Fatalf("RefreshError = %+v, want code refresh_token_reused", afterFirstFailure.RefreshError)
	}
	// NextRefreshAfter must be deferred to access_token expiry (~24h), not 5m retry storm.
	if afterFirstFailure.NextRefreshAfter.Before(futureExpiry.Add(-time.Minute)) {
		t.Fatalf("NextRefreshAfter = %v, want deferred until access_token expiry %v", afterFirstFailure.NextRefreshAfter, futureExpiry)
	}

	// 2. Repeated unchanged refresh attempts before expiry must NOT call upstream Refresh again.
	m.refreshAuth(ctx, primary.ID)
	if got := executor.RefreshCalls(); got != 1 {
		t.Fatalf("repeated refreshAuth with unchanged rejected grant called Refresh %d times, want 1", got)
	}

	// 3. Ordinary inference on primary remains usable while access_token is unexpired, and does not clear RequiresSignIn/RefreshError.
	resp, errExec := m.Execute(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExec != nil {
		t.Fatalf("Execute error = %v, want success on primary", errExec)
	}
	if got := string(resp.Payload); got != primary.ID+":valid-access-token" {
		t.Fatalf("Execute payload = %q, want primary response", got)
	}
	afterExec, _ := m.GetByID(primary.ID)
	if !afterExec.RequiresSignIn || afterExec.RefreshError == nil {
		t.Fatalf("successful inference cleared renewal failure state: RequiresSignIn=%v RefreshError=%+v", afterExec.RequiresSignIn, afterExec.RefreshError)
	}

	// 4. Transient refresh failure on backup follows bounded retry and does NOT require permanent re-authentication.
	executor.mu.Lock()
	executor.refreshErr = errors.New("token refresh failed with status 503: service unavailable")
	executor.mu.Unlock()
	m.refreshAuth(ctx, backup.ID)
	afterTransient, _ := m.GetByID(backup.ID)
	if afterTransient.RequiresSignIn || afterTransient.RenewalStateAt(now) != RenewalStateRetryScheduled {
		t.Fatalf("transient failure set RequiresSignIn=%v RenewalState=%q, want false/%q", afterTransient.RequiresSignIn, afterTransient.RenewalStateAt(now), RenewalStateRetryScheduled)
	}
	if afterTransient.NextRefreshAfter.After(now.Add(10 * time.Minute)) {
		t.Fatalf("transient NextRefreshAfter = %v, want ~5m backoff", afterTransient.NextRefreshAfter)
	}

	// 5. While access_token is still valid on primary, a genuine 429 quota cooldown is recorded before access expiry.
	retryAfter := 30 * time.Minute
	m.MarkResult(ctx, Result{
		AuthID:          primary.ID,
		Provider:        "codex",
		Success:         false,
		CredentialScope: true,
		RetryAfter:      &retryAfter,
		Error:           &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exceeded"},
	})

	// At access expiry, the unrenewable primary credential becomes unavailable without extra upstream refresh calls, and backup serves traffic.
	m.mu.Lock()
	if cur := m.auths[primary.ID]; cur != nil {
		cur.Metadata["expired"] = now.Add(-time.Minute).Format(time.RFC3339)
		m.scheduler.upsertAuth(cur.Clone())
	}
	m.mu.Unlock()
	callsBeforeExpiry := executor.RefreshCalls()
	m.refreshAuth(ctx, primary.ID)
	if got := executor.RefreshCalls(); got != callsBeforeExpiry {
		t.Fatalf("expiry transition called upstream Refresh %d -> %d for already-rejected grant", callsBeforeExpiry, got)
	}
	afterExpiry, _ := m.GetByID(primary.ID)
	if !afterExpiry.Unavailable || afterExpiry.Status != StatusError || !afterExpiry.RequiresSignIn || afterExpiry.RenewalStateAt(now) != RenewalStateExpiredSignInRequired {
		t.Fatalf("expected unavailable expired_sign_in_required at expiry, got unavailable=%v status=%s requires_sign_in=%v state=%q",
			afterExpiry.Unavailable, afterExpiry.Status, afterExpiry.RequiresSignIn, afterExpiry.RenewalStateAt(now))
	}
	respAtExpiry, errAtExpiry := m.Execute(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errAtExpiry != nil {
		t.Fatalf("Execute at primary expiry error = %v, want success on backup", errAtExpiry)
	}
	if got := string(respAtExpiry.Payload); got != backup.ID+":backup-access-token" {
		t.Fatalf("Execute at primary expiry payload = %q, want backup", got)
	}

	// 6. Fresh sign-in clears superseded auth failures while preserving active quota cooldown, and distinguishes awaiting_first_renewal from renewal_verified.
	relogged := afterExpiry.Clone()
	relogged.Metadata["access_token"] = "relogged-access-token"
	relogged.Metadata["refresh_token"] = "relogged-refresh-token"
	relogged.Metadata["expired"] = now.Add(48 * time.Hour).Format(time.RFC3339)
	if _, errUpdate := m.Update(ctx, relogged); errUpdate != nil {
		t.Fatalf("Update relogged primary: %v", errUpdate)
	}
	afterRelogin, _ := m.GetByID(primary.ID)
	if afterRelogin.RequiresSignIn || afterRelogin.RefreshError != nil {
		t.Fatalf("expected RequiresSignIn/RefreshError cleared on relogin, got %v / %+v", afterRelogin.RequiresSignIn, afterRelogin.RefreshError)
	}
	if afterRelogin.RenewalVerified || afterRelogin.RenewalStateAt(now) != RenewalStateAwaitingFirstRenewal {
		t.Fatalf("expected fresh grant to be awaiting_first_renewal, got RenewalVerified=%v state=%q", afterRelogin.RenewalVerified, afterRelogin.RenewalStateAt(now))
	}
	if !afterRelogin.Unavailable || afterRelogin.RecoveryStageAt(now) != RecoveryStageQuotaBlocked {
		t.Fatalf("expected active quota cooldown preserved after relogin, got unavailable=%v stage=%q", afterRelogin.Unavailable, afterRelogin.RecoveryStageAt(now))
	}

	// Release quota cooldown and verify streaming completion vs incomplete stream.
	_, _, _ = m.ResetQuota(ctx, primary.ID)
	afterReset, _ := m.GetByID(primary.ID)
	if afterReset.InferenceVerified || afterReset.RecoveryStageAt(now) != RecoveryStageLoaded {
		t.Fatalf("expected stage loaded before inference verification, got Verified=%v stage=%q", afterReset.InferenceVerified, afterReset.RecoveryStageAt(now))
	}

	// Another account's completed response does not verify primary.
	m.MarkResult(ctx, Result{AuthID: backup.ID, Provider: "codex", Model: model, Success: true, CompletedResponse: true})
	afterOtherSuccess, _ := m.GetByID(primary.ID)
	if afterOtherSuccess.InferenceVerified {
		t.Fatal("another account's inference success must not verify primary")
	}

	// Local ExecuteCount (CountTokens) on primary must NOT verify inference.
	if _, errCount := m.ExecuteCount(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); errCount != nil {
		t.Fatalf("ExecuteCount error: %v", errCount)
	}
	afterCountTokens, _ := m.GetByID(primary.ID)
	if afterCountTokens.InferenceVerified {
		t.Fatal("CountTokens success must not mark InferenceVerified=true")
	}

	// An HTTP 200 stream that terminates without a terminal completion event (chunk error mid-stream) must NOT verify inference.
	executor.mu.Lock()
	executor.streamChunkErr = &Error{HTTPStatus: http.StatusBadGateway, Message: "incomplete stream without response.completed", Retryable: true}
	executor.mu.Unlock()
	incompleteStream, errIncomplete := m.ExecuteStream(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errIncomplete != nil {
		t.Fatalf("ExecuteStream bootstrap error: %v", errIncomplete)
	}
	for range incompleteStream.Chunks {
	}
	afterIncomplete, _ := m.GetByID(primary.ID)
	if afterIncomplete.InferenceVerified {
		t.Fatal("HTTP 200 stream without terminal completion must not mark InferenceVerified=true")
	}
	_, _, _ = m.ResetQuota(ctx, primary.ID)

	// A stream that closes cleanly at EOF after response.created without a completion event must NOT verify inference.
	executor.mu.Lock()
	executor.streamChunkErr = nil
	executor.streamPayloads = [][]byte{[]byte(`data: {"type":"response.created"}` + "\n\n")}
	executor.mu.Unlock()
	eofStream, errEOFStream := m.ExecuteStream(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errEOFStream != nil {
		t.Fatalf("ExecuteStream (EOF without completed) error: %v", errEOFStream)
	}
	for range eofStream.Chunks {
	}
	afterEOFStream, _ := m.GetByID(primary.ID)
	if afterEOFStream.InferenceVerified {
		t.Fatal("clean EOF stream without terminal completion event must not mark InferenceVerified=true")
	}

	// A stream where delta text contains "response.completed" or finish_reason is null must NOT verify inference.
	for _, falsePositiveChunk := range [][]byte{
		[]byte(`data: {"choices":[{"delta":{"content":"hi"},"finish_reason":null}]}` + "\n\n"),
		[]byte(`data: {"type":"response.output_text.delta","delta":"response.completed [DONE] message_stop"}` + "\n\n"),
	} {
		if comp, incomp := streamPayloadCompletionFlags(falsePositiveChunk); comp || incomp {
			t.Fatalf("false-positive chunk %s returned completed=%v incomplete=%v, want false/false", string(falsePositiveChunk), comp, incomp)
		}
	}
	if isCompletedExecutionPayload([]byte(`{"object":"list","data":[{"id":"gpt-5"}]}`)) {
		t.Fatal("non-generation model list JSON must not be treated as a completed execution payload")
	}

	// A stream that finishes with response.incomplete must NOT verify inference.
	executor.mu.Lock()
	executor.streamPayloads = [][]byte{[]byte(`data: {"type":"response.incomplete","response":{"status":"incomplete"}}` + "\n\n")}
	executor.mu.Unlock()
	respIncompleteStream, errRespIncomplete := m.ExecuteStream(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errRespIncomplete != nil {
		t.Fatalf("ExecuteStream (response.incomplete) error: %v", errRespIncomplete)
	}
	for range respIncompleteStream.Chunks {
	}
	afterRespIncomplete, _ := m.GetByID(primary.ID)
	if afterRespIncomplete.InferenceVerified {
		t.Fatal("response.incomplete stream must not mark InferenceVerified=true")
	}
	if _, errVerifyIncomplete := m.VerifyInference(ctx, primary.ID, model); errVerifyIncomplete == nil {
		t.Fatal("VerifyInference must fail when stream ends with response.incomplete")
	}

	// Operator-triggered VerifyInference pins to primary, uses an isolated session and minimal 1-token prompt, and marks InferenceVerified = true upon response.completed.
	executor.mu.Lock()
	executor.streamPayloads = nil
	executor.streamCalls = nil
	executor.mu.Unlock()
	afterVerify, errVerify := m.VerifyInference(ctx, primary.ID, model)
	if errVerify != nil {
		t.Fatalf("VerifyInference error: %v", errVerify)
	}
	executor.mu.Lock()
	verifyCalls := append([]string(nil), executor.streamCalls...)
	verifyReq := executor.lastStreamReq
	verifyOpts := executor.lastStreamOpts
	executor.mu.Unlock()
	if len(verifyCalls) != 1 || verifyCalls[0] != primary.ID {
		t.Fatalf("VerifyInference streamCalls = %v, want [%s]", verifyCalls, primary.ID)
	}
	if gotSession := verifyOpts.Headers.Get("X-Session-Id"); gotSession == "" || len(gotSession) < 7 || gotSession[:7] != "verify-" {
		t.Fatalf("VerifyInference X-Session-Id = %q, want isolated verify-* session", gotSession)
	}
	if !containsSubstr(string(verifyReq.Payload), `"max_output_tokens":1`) || !containsSubstr(string(verifyReq.Payload), `"store":false`) {
		t.Fatalf("VerifyInference payload = %s, want minimal 1-token non-stored prompt", string(verifyReq.Payload))
	}
	if !afterVerify.InferenceVerified || afterVerify.RecoveryStageAt(now) != RecoveryStageInferenceVerified {
		t.Fatalf("expected InferenceVerified=true and stage=inference_verified after VerifyInference, got %v / %q", afterVerify.InferenceVerified, afterVerify.RecoveryStageAt(now))
	}
	if afterVerify.RenewalVerified {
		t.Fatal("RenewalVerified must remain false until first automatic refresh succeeds")
	}

	// Subsequent refresh cycle verifies renewal.
	executor.mu.Lock()
	executor.refreshErr = nil
	executor.refreshTokens = map[string]string{primary.ID: "rotated-access-token"}
	executor.mu.Unlock()
	if _, errForce := m.ForceRefreshAuth(ctx, primary.ID); errForce != nil {
		t.Fatalf("ForceRefreshAuth error: %v", errForce)
	}
	afterRenewal, _ := m.GetByID(primary.ID)
	if !afterRenewal.RenewalVerified || afterRenewal.RenewalStateAt(now) != RenewalStateRenewalVerified || afterRenewal.RecoveryStageAt(now) != RecoveryStageRenewalVerified {
		t.Fatalf("expected RenewalVerified=true after successful refresh, got verified=%v renewal_state=%q stage=%q",
			afterRenewal.RenewalVerified, afterRenewal.RenewalStateAt(now), afterRenewal.RecoveryStageAt(now))
	}
}

func TestManager_StaleRefreshFailureDoesNotPoisonReloggedCredential_Issue5783(t *testing.T) {
	ctx := WithSkipPersist(context.Background())
	now := time.Now().UTC()
	futureExpiry := now.Add(24 * time.Hour).Truncate(time.Second)

	auth := &Auth{
		ID:       "codex-stale-race.json",
		FileName: "codex-stale-race.json",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":          "codex",
			"email":         "race@example.com",
			"access_token":  "old-access-token",
			"refresh_token": "old-refresh-token",
			"expired":       futureExpiry.Format(time.RFC3339),
		},
	}

	refreshStarted := make(chan struct{})
	releaseRefresh := make(chan struct{})
	var once sync.Once
	executor := &unauthorizedRefreshExecutor{
		id: "codex",
		onRefresh: func() {
			once.Do(func() {
				close(refreshStarted)
				<-releaseRefresh
			})
		},
		refreshErr: errors.New(`token refresh failed with status 401: {"error":{"code":"refresh_token_reused","message":"Refresh token already used"}}`),
	}

	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(executor)
	if _, errReg := m.Register(ctx, auth); errReg != nil {
		t.Fatalf("register auth: %v", errReg)
	}

	refreshDone := make(chan struct{})
	go func() {
		defer close(refreshDone)
		m.refreshAuth(ctx, auth.ID)
	}()

	<-refreshStarted

	// While the old refresh is in flight, operator completes re-login with a new grant.
	relogged := auth.Clone()
	relogged.Metadata["access_token"] = "new-access-token"
	relogged.Metadata["refresh_token"] = "new-refresh-token"
	relogged.Metadata["expired"] = now.Add(48 * time.Hour).Format(time.RFC3339)
	if _, errUpdate := m.Update(ctx, relogged); errUpdate != nil {
		t.Fatalf("Update relogged auth: %v", errUpdate)
	}

	// Release the stale in-flight refresh failure.
	close(releaseRefresh)
	<-refreshDone

	after, ok := m.GetByID(auth.ID)
	if !ok || after == nil {
		t.Fatal("auth missing after stale refresh")
	}
	if after.RequiresSignIn || after.RefreshError != nil || after.RejectedRefreshToken != "" {
		t.Fatalf("stale refresh failure poisoned newly relogged credential: RequiresSignIn=%v RefreshError=%+v RejectedRefreshToken=%q",
			after.RequiresSignIn, after.RefreshError, after.RejectedRefreshToken)
	}
	if after.Unavailable || after.Status != StatusActive {
		t.Fatalf("expected active credential after stale refresh discard, got status=%s unavailable=%v", after.Status, after.Unavailable)
	}

	// Also verify that if a credential is replaced while VerifyInference is in flight, VerifyInference returns a superseded error and does NOT mark the new credential verified.
	verifyStarted := make(chan struct{})
	releaseVerify := make(chan struct{})
	var verifyOnce sync.Once
	executor.mu.Lock()
	executor.onExecute = func(string) {
		verifyOnce.Do(func() {
			close(verifyStarted)
			<-releaseVerify
		})
	}
	executor.mu.Unlock()

	verifyErrCh := make(chan error, 1)
	go func() {
		_, errV := m.VerifyInference(ctx, auth.ID, "gpt-5.5")
		verifyErrCh <- errV
	}()

	<-verifyStarted
	reloggedAgain := after.Clone()
	reloggedAgain.Metadata["access_token"] = "newer-access-token"
	reloggedAgain.Metadata["refresh_token"] = "newer-refresh-token"
	if _, errUpdate2 := m.Update(ctx, reloggedAgain); errUpdate2 != nil {
		t.Fatalf("Update reloggedAgain: %v", errUpdate2)
	}
	close(releaseVerify)

	errVerifyRace := <-verifyErrCh
	if errVerifyRace == nil || !containsSubstr(errVerifyRace.Error(), "replaced during verification") {
		t.Fatalf("expected credential_superseded error when credential is replaced during VerifyInference, got: %v", errVerifyRace)
	}
	afterVerifyRace, _ := m.GetByID(auth.ID)
	if afterVerifyRace.InferenceVerified {
		t.Fatal("new credential version must not inherit InferenceVerified from superseded in-flight VerifyInference")
	}

	// Also verify that if Store.Save blocks during a non-secret Update while a terminal refresh failure is recorded concurrently,
	// the terminal RequiresSignIn and RejectedRefreshToken state is preserved when Store.Save completes.
	saveStarted := make(chan struct{})
	releaseSave := make(chan struct{})
	var saveOnce sync.Once
	blockingStore := &blockingTestAuthStore{
		onSave: func() {
			saveOnce.Do(func() {
				close(saveStarted)
				<-releaseSave
			})
		},
	}
	mWithStore := NewManager(blockingStore, nil, nil)
	termExec := &unauthorizedRefreshExecutor{
		id:         "codex",
		refreshErr: errors.New(`token refresh failed with status 401: {"error":{"code":"refresh_token_reused","message":"reused"}}`),
	}
	mWithStore.RegisterExecutor(termExec)
	persistAuth := &Auth{
		ID:       "codex-persist-race.json",
		FileName: "codex-persist-race.json",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":          "codex",
			"email":         "persist@example.com",
			"access_token":  "persist-at",
			"refresh_token": "persist-rt",
			"expired":       futureExpiry.Format(time.RFC3339),
		},
	}
	if _, errRegPersist := mWithStore.Register(ctx, persistAuth); errRegPersist != nil {
		t.Fatalf("Register persistAuth: %v", errRegPersist)
	}

	updateDone := make(chan error, 1)
	go func() {
		nonSecretEdit := persistAuth.Clone()
		nonSecretEdit.Metadata["note"] = "updated-note"
		_, errU := mWithStore.Update(context.Background(), nonSecretEdit)
		updateDone <- errU
	}()

	<-saveStarted
	mWithStore.refreshAuth(ctx, persistAuth.ID)
	close(releaseSave)
	if errU := <-updateDone; errU != nil {
		t.Fatalf("concurrent non-secret Update error: %v", errU)
	}

	afterPersistRace, _ := mWithStore.GetByID(persistAuth.ID)
	if !afterPersistRace.RequiresSignIn || afterPersistRace.RefreshError == nil || afterPersistRace.RejectedRefreshToken != "persist-rt" {
		t.Fatalf("concurrent Store.Save overwrote terminal renewal failure: RequiresSignIn=%v RefreshError=%+v Rejected=%q",
			afterPersistRace.RequiresSignIn, afterPersistRace.RefreshError, afterPersistRace.RejectedRefreshToken)
	}
}

type blockingTestAuthStore struct {
	onSave func()
}

func (s *blockingTestAuthStore) List(context.Context) ([]*Auth, error) { return nil, nil }
func (s *blockingTestAuthStore) Save(_ context.Context, auth *Auth) (string, error) {
	if s.onSave != nil {
		s.onSave()
	}
	return auth.ID, nil
}
func (s *blockingTestAuthStore) Delete(context.Context, string) error { return nil }

func TestManager_AccessTokenOnlyUpdateAndDiskAdoption_Issue5783(t *testing.T) {
	ctx := WithSkipPersist(context.Background())
	now := time.Now().UTC()
	futureExpiry := now.Add(12 * time.Hour).Truncate(time.Second)

	auth := &Auth{
		ID:       "codex-partial.json",
		FileName: "codex-partial.json",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":          "codex",
			"email":         "partial@example.com",
			"access_token":  "at-1",
			"refresh_token": "dead-rt",
			"expired":       futureExpiry.Format(time.RFC3339),
		},
	}

	executor := &unauthorizedRefreshExecutor{
		id:         "codex",
		refreshErr: errors.New(`token refresh failed with status 401: {"error":{"code":"refresh_token_reused","message":"reused"}}`),
	}
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(executor)
	if _, errReg := m.Register(ctx, auth); errReg != nil {
		t.Fatalf("register auth: %v", errReg)
	}

	m.refreshAuth(ctx, auth.ID)
	broken, _ := m.GetByID(auth.ID)
	if !broken.RequiresSignIn {
		t.Fatal("expected RequiresSignIn=true after terminal refresh error")
	}
	// Mark old access_token as inference-verified before replacing only access_token.
	m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: "codex", Model: "gpt-5.5", Success: true, CompletedResponse: true})
	verifiedOld, _ := m.GetByID(auth.ID)
	if !verifiedOld.InferenceVerified {
		t.Fatal("expected InferenceVerified=true on old access_token")
	}

	// 1. Updating only access_token while keeping the same rejected refresh_token must keep RequiresSignIn=true AND reset InferenceVerified=false.
	newExp := now.Add(36 * time.Hour).Truncate(time.Second)
	accessOnly := verifiedOld.Clone()
	accessOnly.Metadata["access_token"] = "at-2"
	accessOnly.Metadata["refresh_token"] = "dead-rt"
	accessOnly.Metadata["expired"] = newExp.Format(time.RFC3339)
	if _, errUpdate := m.Update(ctx, accessOnly); errUpdate != nil {
		t.Fatalf("Update accessOnly: %v", errUpdate)
	}
	afterAccessOnly, _ := m.GetByID(auth.ID)
	if !afterAccessOnly.RequiresSignIn || afterAccessOnly.RefreshError == nil || afterAccessOnly.RejectedRefreshToken != "dead-rt" {
		t.Fatalf("updating only access_token cleared rejected refresh state: RequiresSignIn=%v RefreshError=%+v Rejected=%q",
			afterAccessOnly.RequiresSignIn, afterAccessOnly.RefreshError, afterAccessOnly.RejectedRefreshToken)
	}
	if afterAccessOnly.InferenceVerified {
		t.Fatal("replacing access_token must reset InferenceVerified=false so new access_token does not inherit old verification")
	}
	if afterAccessOnly.NextRefreshAfter.Before(newExp.Add(-time.Minute)) {
		t.Fatalf("NextRefreshAfter = %v, want deferred to new access_token expiry %v", afterAccessOnly.NextRefreshAfter, newExp)
	}

	// 2. A Refresh that only adopts disk-rotated credentials (UpstreamRefreshed=false) clears RequiresSignIn but keeps RenewalVerified=false.
	diskPatch := codex.NewCredentialRefreshPatch(
		afterAccessOnly.Metadata,
		map[string]any{"access_token": "disk-at", "refresh_token": "disk-rt", "expired": newExp.Format(time.RFC3339)},
		true,
		false,
	)
	diskPatch.SetUpstreamRefreshed(false)
	executor.mu.Lock()
	executor.refreshErr = nil
	executor.refreshTokens = map[string]string{auth.ID: "disk-at"}
	executor.refreshPatch = diskPatch
	executor.mu.Unlock()

	if _, errForce := m.ForceRefreshAuth(ctx, auth.ID); errForce != nil {
		t.Fatalf("ForceRefreshAuth (disk adoption) error: %v", errForce)
	}
	afterDiskAdopt, _ := m.GetByID(auth.ID)
	if afterDiskAdopt.RequiresSignIn || afterDiskAdopt.RefreshError != nil {
		t.Fatalf("disk adoption should clear RequiresSignIn, got %v / %+v", afterDiskAdopt.RequiresSignIn, afterDiskAdopt.RefreshError)
	}
	if afterDiskAdopt.RenewalVerified || afterDiskAdopt.RenewalStateAt(now) != RenewalStateAwaitingFirstRenewal {
		t.Fatalf("disk-only adoption must NOT mark RenewalVerified=true, got verified=%v state=%q",
			afterDiskAdopt.RenewalVerified, afterDiskAdopt.RenewalStateAt(now))
	}

	// 3. When a credential has both outer refresh_token and a healthy siwc.refresh_token, an outer terminal refresh failure
	// must NOT poison the healthy siwc.refresh_token or prevent recovery when only the outer refresh_token is re-authenticated.
	siwcAuth := &Auth{
		ID:       "codex-siwc-outer.json",
		FileName: "codex-siwc-outer.json",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":          "codex",
			"email":         "siwc@example.com",
			"access_token":  "outer-at-1",
			"refresh_token": "dead-outer-rt",
			"expired":       futureExpiry.Format(time.RFC3339),
			"siwc": map[string]any{
				"access_token":  "siwc-at-1",
				"refresh_token": "healthy-siwc-rt",
			},
		},
	}
	if _, errRegSIWC := m.Register(ctx, siwcAuth); errRegSIWC != nil {
		t.Fatalf("register siwcAuth: %v", errRegSIWC)
	}
	executor.mu.Lock()
	executor.refreshPatch = nil
	executor.refreshErr = errors.New(`token refresh failed with status 401: {"error":{"code":"refresh_token_reused"},"code":"authorization-secret-xyz","state":"callback-secret-123"} callback%3Fcode%3Descaped-code-secret%26state%3Descaped-state-secret`)
	executor.mu.Unlock()
	m.refreshAuth(ctx, siwcAuth.ID)
	siwcBroken, _ := m.GetByID(siwcAuth.ID)
	if !siwcBroken.RequiresSignIn || siwcBroken.RejectedRefreshToken != "dead-outer-rt" || siwcBroken.RejectedSIWCRefreshToken != "" {
		t.Fatalf("outer refresh failure should only mark outer refresh_token rejected, got RequiresSignIn=%v outer=%q siwc=%q",
			siwcBroken.RequiresSignIn, siwcBroken.RejectedRefreshToken, siwcBroken.RejectedSIWCRefreshToken)
	}
	for _, secret := range []string{"authorization-secret-xyz", "callback-secret-123", "escaped-code-secret", "escaped-state-secret"} {
		if containsSubstr(siwcBroken.RefreshError.Message, secret) {
			t.Fatalf("RefreshError leaked secret %q: %s", secret, siwcBroken.RefreshError.Message)
		}
	}
	authBearerSanitized := SanitizeRenewalMessage(`Authorization: Bearer opaque-secret-plain {"authorization": "Bearer opaque-secret-json"} AUTHORIZATION: BEARER opaque-secret-upper`)
	for _, secret := range []string{"opaque-secret-plain", "opaque-secret-json", "opaque-secret-upper"} {
		if containsSubstr(authBearerSanitized, secret) {
			t.Fatalf("SanitizeRenewalMessage leaked Authorization Bearer secret %q: %s", secret, authBearerSanitized)
		}
	}
	if !containsSubstr(siwcBroken.RefreshError.Message, "refresh_token_reused") {
		t.Fatalf("RefreshError should preserve safe error code refresh_token_reused, got: %s", siwcBroken.RefreshError.Message)
	}

	reloggedOuter := siwcBroken.Clone()
	reloggedOuter.Metadata["access_token"] = "outer-at-2"
	reloggedOuter.Metadata["refresh_token"] = "fresh-outer-rt"
	reloggedOuter.Metadata["siwc"] = map[string]any{
		"access_token":  "siwc-at-1",
		"refresh_token": "healthy-siwc-rt",
	}
	if _, errUpdateOuter := m.Update(ctx, reloggedOuter); errUpdateOuter != nil {
		t.Fatalf("Update reloggedOuter: %v", errUpdateOuter)
	}
	afterOuterRelogin, _ := m.GetByID(siwcAuth.ID)
	if afterOuterRelogin.RequiresSignIn || afterOuterRelogin.RefreshError != nil {
		t.Fatalf("re-authenticating outer refresh_token while healthy SIWC token is unchanged must clear RequiresSignIn, got %v / %+v",
			afterOuterRelogin.RequiresSignIn, afterOuterRelogin.RefreshError)
	}

	// 4. Conversely, a real SIWC HTTP 401 refresh error ("OpenAI OAuth token request failed (401): ...") must only mark
	// RejectedSIWCRefreshToken and allow recovery when only the SIWC refresh_token is re-bound.
	executor.mu.Lock()
	executor.refreshErr = codex.WrapSIWCRefreshError(errors.New(`OpenAI OAuth token request failed (401): {"error":{"code":"refresh_token_reused","message":"SIWC refresh token already used"}}`))
	executor.mu.Unlock()
	m.refreshAuth(ctx, siwcAuth.ID)
	siwcOnlyBroken, _ := m.GetByID(siwcAuth.ID)
	if !siwcOnlyBroken.RequiresSignIn || siwcOnlyBroken.RejectedSIWCRefreshToken != "healthy-siwc-rt" || siwcOnlyBroken.RejectedRefreshToken != "" {
		t.Fatalf("SIWC refresh failure should only mark SIWC refresh_token rejected, got RequiresSignIn=%v outer=%q siwc=%q",
			siwcOnlyBroken.RequiresSignIn, siwcOnlyBroken.RejectedRefreshToken, siwcOnlyBroken.RejectedSIWCRefreshToken)
	}
	reboundSIWC := siwcOnlyBroken.Clone()
	reboundSIWC.Metadata["siwc"] = map[string]any{
		"access_token":  "siwc-at-2",
		"refresh_token": "fresh-siwc-rt",
		"client_id":     "cid-siwc",
		"expired":       time.Now().UTC().Add(45 * time.Minute).Format(time.RFC3339),
	}
	if _, errUpdateSIWC := m.Update(ctx, reboundSIWC); errUpdateSIWC != nil {
		t.Fatalf("Update reboundSIWC: %v", errUpdateSIWC)
	}
	afterSIWCRebind, _ := m.GetByID(siwcAuth.ID)
	if afterSIWCRebind.RequiresSignIn || afterSIWCRebind.RefreshError != nil {
		t.Fatalf("re-binding SIWC refresh_token while healthy outer token is unchanged must clear RequiresSignIn, got %v / %+v",
			afterSIWCRebind.RequiresSignIn, afterSIWCRebind.RefreshError)
	}

	// 4b. A partial refresh where SIWC succeeds but outer refresh fails terminally (recorded on CodexRefreshPatch)
	// must NOT clear RequiresSignIn or mark RenewalVerified=true.
	partialPatch := codex.NewCredentialRefreshPatch(
		afterSIWCRebind.Metadata,
		afterSIWCRebind.Metadata,
		false,
		true,
	)
	partialPatch.SetUpstreamRefreshed(true)
	partialPatch.SetPartitionErrors(
		errors.New(`token refresh failed with status 401: {"error":{"code":"refresh_token_reused","message":"outer reused"}}`),
		nil,
	)
	executor.mu.Lock()
	executor.refreshErr = nil
	executor.refreshPatch = partialPatch
	executor.mu.Unlock()
	if _, errPartial := m.ForceRefreshAuth(ctx, siwcAuth.ID); errPartial != nil {
		t.Fatalf("ForceRefreshAuth (partial refresh) error: %v", errPartial)
	}
	afterPartial, _ := m.GetByID(siwcAuth.ID)
	if !afterPartial.RequiresSignIn || afterPartial.RenewalVerified || afterPartial.RejectedRefreshToken != "fresh-outer-rt" {
		t.Fatalf("partial refresh with terminal outer failure must set RequiresSignIn=true RenewalVerified=false RejectedRefreshToken=fresh-outer-rt, got RequiresSignIn=%v RenewalVerified=%v Rejected=%q",
			afterPartial.RequiresSignIn, afterPartial.RenewalVerified, afterPartial.RejectedRefreshToken)
	}
	if !afterPartial.NextRefreshAfter.IsZero() {
		t.Fatalf("partial refresh with healthy SIWC partition must not defer NextRefreshAfter to outer expiry without Update, got %v",
			afterPartial.NextRefreshAfter)
	}
	executor.mu.Lock()
	executor.refreshPatch = nil
	// A subsequent transient 503 failure on the healthy SIWC partition must NOT clear RequiresSignIn
	// or overwrite the terminal outer RefreshError.
	executor.refreshErr = errors.New(`OpenAI OAuth token request failed (503): service unavailable`)
	executor.mu.Unlock()
	m.refreshAuth(ctx, siwcAuth.ID)
	afterTransientOnHealthy, _ := m.GetByID(siwcAuth.ID)
	if !afterTransientOnHealthy.RequiresSignIn || afterTransientOnHealthy.RejectedRefreshToken != "fresh-outer-rt" {
		t.Fatalf("transient failure on healthy SIWC partition must preserve RequiresSignIn=true and RejectedRefreshToken=fresh-outer-rt, got RequiresSignIn=%v Rejected=%q",
			afterTransientOnHealthy.RequiresSignIn, afterTransientOnHealthy.RejectedRefreshToken)
	}
	if afterTransientOnHealthy.RefreshError == nil || !isTerminalRefreshError(afterTransientOnHealthy, afterTransientOnHealthy.RefreshError) {
		t.Fatalf("transient failure on healthy SIWC partition must preserve terminal RefreshError from rejected outer partition, got %+v",
			afterTransientOnHealthy.RefreshError)
	}
	executor.mu.Lock()
	executor.refreshErr = nil
	executor.mu.Unlock()

	// 4c. Bidirectional automatic refresh (m.shouldRefresh + m.refreshAuth) when one partition is rejected
	// and the other partition is healthy and due for refresh:
	// Direction 1: outer is rejected ("fresh-outer-rt"), while SIWC is healthy ("fresh-siwc-rt") and due for refresh.
	nowCheck := time.Now().UTC()
	siwcDueClone := afterPartial.Clone()
	siwcDueClone.Metadata["siwc"] = map[string]any{
		"access_token":  "siwc-at-due",
		"refresh_token": "fresh-siwc-rt",
		"client_id":     "cid-siwc",
		"expired":       nowCheck.Add(5 * time.Minute).Format(time.RFC3339),
	}
	if _, errUpdSIWCDue := m.Update(ctx, siwcDueClone); errUpdSIWCDue != nil {
		t.Fatalf("Update siwcDueClone: %v", errUpdSIWCDue)
	}
	currentSIWCDue, _ := m.GetByID(siwcAuth.ID)
	if !m.shouldRefresh(currentSIWCDue, nowCheck) {
		t.Fatal("shouldRefresh must be true when SIWC partition is healthy and due despite rejected outer partition")
	}
	callsBeforeAutoSIWC := executor.RefreshCalls()
	m.refreshAuth(ctx, siwcAuth.ID)
	if executor.RefreshCalls() != callsBeforeAutoSIWC+1 {
		t.Fatalf("m.refreshAuth must invoke executor for healthy SIWC partition, calls=%d want %d", executor.RefreshCalls(), callsBeforeAutoSIWC+1)
	}

	// Direction 2: SIWC is rejected, while outer is healthy and due for refresh.
	outerHealthyClone := currentSIWCDue.Clone()
	outerHealthyClone.Metadata["refresh_token"] = "brand-new-outer-rt"
	outerHealthyClone.Metadata["expired"] = nowCheck.Add(10 * time.Minute).Format(time.RFC3339)
	outerHealthyClone.Metadata["siwc"] = map[string]any{
		"access_token":  "siwc-at-valid",
		"refresh_token": "dead-siwc-rt",
		"client_id":     "cid-siwc",
		"expired":       nowCheck.Add(45 * time.Minute).Format(time.RFC3339),
	}
	if _, errUpdOuter := m.Update(ctx, outerHealthyClone); errUpdOuter != nil {
		t.Fatalf("Update outerHealthyClone: %v", errUpdOuter)
	}
	executor.mu.Lock()
	executor.refreshErr = errors.New(`OpenAI OAuth token request failed (401): {"error":{"code":"refresh_token_reused","message":"SIWC reused"}}`)
	executor.mu.Unlock()
	m.refreshAuth(ctx, siwcAuth.ID)
	afterSIWCDead, _ := m.GetByID(siwcAuth.ID)
	if afterSIWCDead.RejectedSIWCRefreshToken != "dead-siwc-rt" || afterSIWCDead.RejectedRefreshToken != "" {
		t.Fatalf("expected only RejectedSIWCRefreshToken=dead-siwc-rt, got outer=%q siwc=%q",
			afterSIWCDead.RejectedRefreshToken, afterSIWCDead.RejectedSIWCRefreshToken)
	}
	if !m.shouldRefresh(afterSIWCDead, nowCheck) {
		t.Fatal("shouldRefresh must be true when outer partition is healthy and due despite rejected SIWC partition")
	}
	executor.mu.Lock()
	executor.refreshErr = nil
	executor.mu.Unlock()
	callsBeforeAutoOuter := executor.RefreshCalls()
	m.refreshAuth(ctx, siwcAuth.ID)
	if executor.RefreshCalls() != callsBeforeAutoOuter+1 {
		t.Fatalf("m.refreshAuth must invoke executor for healthy outer partition, calls=%d want %d", executor.RefreshCalls(), callsBeforeAutoOuter+1)
	}

	// 5. Disable -> watcher file reload -> re-enable transition must preserve terminal renewal state
	// when the rejected refresh token is unchanged.
	disableAuth := &Auth{
		ID:       "codex-disable-reload.json",
		FileName: "codex-disable-reload.json",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":          "codex",
			"email":         "disable-reload@example.com",
			"access_token":  "valid-at",
			"refresh_token": "dead-rt- across-disable",
			"expired":       nowCheck.Add(40 * time.Minute).Format(time.RFC3339),
		},
	}
	if _, errRegDis := m.Register(ctx, disableAuth); errRegDis != nil {
		t.Fatalf("Register(disableAuth) error: %v", errRegDis)
	}
	executor.mu.Lock()
	executor.refreshErr = errors.New(`token refresh failed with status 401: {"error":{"code":"refresh_token_reused","message":"reused"}}`)
	executor.mu.Unlock()
	m.refreshAuth(ctx, disableAuth.ID)

	// Simulate watcher loading a disabled snapshot from disk (runtime fields are zeroed on fresh disk load).
	disabledFromDisk := &Auth{
		ID:       disableAuth.ID,
		FileName: disableAuth.FileName,
		Provider: "codex",
		Disabled: true,
		Status:   StatusDisabled,
		Metadata: map[string]any{
			"type":          "codex",
			"email":         "disable-reload@example.com",
			"access_token":  "valid-at",
			"refresh_token": "dead-rt- across-disable",
			"expired":       nowCheck.Add(40 * time.Minute).Format(time.RFC3339),
		},
	}
	if _, errUpdDis := m.Update(ctx, disabledFromDisk); errUpdDis != nil {
		t.Fatalf("Update(disabledFromDisk) error: %v", errUpdDis)
	}
	// Simulate watcher loading the re-enabled snapshot from disk with the same unchanged refresh token.
	reenabledFromDisk := &Auth{
		ID:       disableAuth.ID,
		FileName: disableAuth.FileName,
		Provider: "codex",
		Disabled: false,
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":          "codex",
			"email":         "disable-reload@example.com",
			"access_token":  "valid-at",
			"refresh_token": "dead-rt- across-disable",
			"expired":       nowCheck.Add(40 * time.Minute).Format(time.RFC3339),
		},
	}
	if _, errUpdRe := m.Update(ctx, reenabledFromDisk); errUpdRe != nil {
		t.Fatalf("Update(reenabledFromDisk) error: %v", errUpdRe)
	}
	afterReenable, _ := m.GetByID(disableAuth.ID)
	if !afterReenable.RequiresSignIn || afterReenable.RejectedRefreshToken != "dead-rt- across-disable" || afterReenable.RefreshError == nil {
		t.Fatalf("disable/re-enable with unchanged refresh_token must preserve RequiresSignIn and RejectedRefreshToken, got RequiresSignIn=%v Rejected=%q Err=%+v",
			afterReenable.RequiresSignIn, afterReenable.RejectedRefreshToken, afterReenable.RefreshError)
	}
	callsBeforeReenableRefresh := executor.RefreshCalls()
	m.refreshAuth(ctx, disableAuth.ID)
	if executor.RefreshCalls() != callsBeforeReenableRefresh {
		t.Fatalf("m.refreshAuth after re-enabling unchanged rejected token must short-circuit, calls=%d want %d",
			executor.RefreshCalls(), callsBeforeReenableRefresh)
	}

	// 6. Dual-partition refresh failure where outer fails with transient 503 and SIWC fails with terminal 401 refresh_token_reused
	// must still record SIWC's terminal rejection and RequiresSignIn=true.
	dualFailAuth := &Auth{
		ID:       "codex-dual-fail.json",
		FileName: "codex-dual-fail.json",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":          "codex",
			"email":         "dualfail@example.com",
			"access_token":  "outer-at-valid",
			"refresh_token": "outer-rt-transient",
			"expired":       nowCheck.Add(40 * time.Minute).Format(time.RFC3339),
			"use_siwc":      true,
			"siwc": map[string]any{
				"access_token":  "siwc-at-valid",
				"refresh_token": "siwc-rt-terminal",
				"client_id":     "cid-dual",
				"expired":       nowCheck.Add(40 * time.Minute).Format(time.RFC3339),
			},
		},
	}
	if _, errRegDual := m.Register(ctx, dualFailAuth); errRegDual != nil {
		t.Fatalf("Register(dualFailAuth) error: %v", errRegDual)
	}
	executor.mu.Lock()
	executor.refreshErr = codex.NewPartitionRefreshError(
		errors.New(`token refresh failed with status 503: service unavailable`),
		codex.WrapSIWCRefreshError(errors.New(`OpenAI OAuth token request failed (401): {"error":{"code":"refresh_token_reused","message":"SIWC reused"}}`)),
	)
	executor.mu.Unlock()
	m.refreshAuth(ctx, dualFailAuth.ID)
	afterDualFail, _ := m.GetByID(dualFailAuth.ID)
	if !afterDualFail.RequiresSignIn || afterDualFail.RejectedSIWCRefreshToken != "siwc-rt-terminal" || afterDualFail.RejectedRefreshToken != "" {
		t.Fatalf("dual-partition failure (outer 503 + SIWC 401) must set RequiresSignIn=true and RejectedSIWCRefreshToken=siwc-rt-terminal, got RequiresSignIn=%v outer=%q siwc=%q",
			afterDualFail.RequiresSignIn, afterDualFail.RejectedRefreshToken, afterDualFail.RejectedSIWCRefreshToken)
	}
	if afterDualFail.RefreshError == nil || afterDualFail.RefreshError.Code != "refresh_token_reused" {
		t.Fatalf("dual-partition failure must preserve terminal SIWC RefreshError code refresh_token_reused, got %+v", afterDualFail.RefreshError)
	}
	executor.mu.Lock()
	executor.refreshErr = nil
	executor.mu.Unlock()

	// 7. VerifyInference must resolve model prefix and OAuth model aliases before calling executor.
	m.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		"codex": {{Name: "gpt-5.5", Alias: "codex-verified"}},
	})
	aliasedAuth := &Auth{
		ID:       "codex-alias-verify.json",
		FileName: "codex-alias-verify.json",
		Provider: "codex",
		Prefix:   "team",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":          "codex",
			"email":         "alias@example.com",
			"access_token":  "alias-at",
			"refresh_token": "alias-rt",
			"expired":       nowCheck.Add(40 * time.Minute).Format(time.RFC3339),
		},
	}
	if _, errRegAlias := m.Register(ctx, aliasedAuth); errRegAlias != nil {
		t.Fatalf("Register(aliasedAuth) error: %v", errRegAlias)
	}
	executor.mu.Lock()
	executor.streamPayloads = [][]byte{[]byte(`data: {"type":"response.completed","response":{"status":"completed"}}` + "\n\n")}
	executor.mu.Unlock()
	verifiedAliasAuth, errVerifyAlias := m.VerifyInference(ctx, aliasedAuth.ID, "team/codex-verified")
	if errVerifyAlias != nil {
		t.Fatalf("VerifyInference with prefix and alias failed: %v", errVerifyAlias)
	}
	if !verifiedAliasAuth.InferenceVerified {
		t.Fatal("expected InferenceVerified=true after VerifyInference with prefix and alias")
	}
	executor.mu.Lock()
	gotModel := executor.lastStreamReq.Model
	executor.mu.Unlock()
	if gotModel != "gpt-5.5" {
		t.Fatalf("VerifyInference must resolve prefix+alias to upstream model gpt-5.5, got %q", gotModel)
	}

	// 8. Concurrent relogin of the failed partition before a partial refresh commits must NOT poison
	// the newly relogged partition in either direction (outer failed + SIWC refreshed, or SIWC failed + outer refreshed).
	// Direction A: outer failed on old token while SIWC refreshed; outer relogged before UpdateRefreshedAuth commits.
	raceBaseA, _ := m.GetByID(siwcAuth.ID)
	reloggedOuterA := raceBaseA.Clone()
	reloggedOuterA.Metadata["access_token"] = "relogged-outer-at-A"
	reloggedOuterA.Metadata["refresh_token"] = "relogged-outer-rt-A"
	if _, errUpdRaceA := m.Update(ctx, reloggedOuterA); errUpdRaceA != nil {
		t.Fatalf("Update reloggedOuterA: %v", errUpdRaceA)
	}
	stalePartialOuterFail := raceBaseA.Clone()
	stalePartialOuterFail.Metadata["siwc"] = map[string]any{
		"access_token":  "siwc-rotated-at-A",
		"refresh_token": "siwc-rotated-rt-A",
		"client_id":     "cid-siwc",
		"expired":       nowCheck.Add(50 * time.Minute).Format(time.RFC3339),
	}
	patchA := codex.NewCredentialRefreshPatch(raceBaseA.Metadata, stalePartialOuterFail.Metadata, false, true)
	patchA.SetUpstreamRefreshed(true)
	patchA.SetPartitionErrors(
		errors.New(`token refresh failed with status 401: {"error":{"code":"refresh_token_reused","message":"old outer reused"}}`),
		nil,
	)
	stalePartialOuterFail.CodexRefreshPatch = patchA
	mergedA, errMergeA := m.UpdateRefreshedAuth(ctx, raceBaseA, stalePartialOuterFail)
	if errMergeA != nil {
		t.Fatalf("UpdateRefreshedAuth (Direction A) error: %v", errMergeA)
	}
	if mergedA.RequiresSignIn || mergedA.RejectedRefreshToken != "" || !mergedA.RenewalVerified {
		t.Fatalf("stale outer failure in partial refresh must not poison relogged outer token: RequiresSignIn=%v RejectedOuter=%q RenewalVerified=%v",
			mergedA.RequiresSignIn, mergedA.RejectedRefreshToken, mergedA.RenewalVerified)
	}

	// Direction B: SIWC failed on old token while outer refreshed; SIWC rebound before UpdateRefreshedAuth commits.
	raceBaseB, _ := m.GetByID(siwcAuth.ID)
	reboundSIWCB := raceBaseB.Clone()
	reboundSIWCB.Metadata["siwc"] = map[string]any{
		"access_token":  "rebound-siwc-at-B",
		"refresh_token": "rebound-siwc-rt-B",
		"client_id":     "cid-siwc",
		"expired":       nowCheck.Add(50 * time.Minute).Format(time.RFC3339),
	}
	if _, errUpdRaceB := m.Update(ctx, reboundSIWCB); errUpdRaceB != nil {
		t.Fatalf("Update reboundSIWCB: %v", errUpdRaceB)
	}
	stalePartialSIWCFail := raceBaseB.Clone()
	stalePartialSIWCFail.Metadata["access_token"] = "outer-rotated-at-B"
	stalePartialSIWCFail.Metadata["refresh_token"] = "outer-rotated-rt-B"
	patchB := codex.NewCredentialRefreshPatch(raceBaseB.Metadata, stalePartialSIWCFail.Metadata, true, false)
	patchB.SetUpstreamRefreshed(true)
	patchB.SetPartitionErrors(
		nil,
		codex.WrapSIWCRefreshError(errors.New(`OpenAI OAuth token request failed (401): {"error":{"code":"refresh_token_reused","message":"old SIWC reused"}}`)),
	)
	stalePartialSIWCFail.CodexRefreshPatch = patchB
	mergedB, errMergeB := m.UpdateRefreshedAuth(ctx, raceBaseB, stalePartialSIWCFail)
	if errMergeB != nil {
		t.Fatalf("UpdateRefreshedAuth (Direction B) error: %v", errMergeB)
	}
	if mergedB.RequiresSignIn || mergedB.RejectedSIWCRefreshToken != "" || !mergedB.RenewalVerified {
		t.Fatalf("stale SIWC failure in partial refresh must not poison rebound SIWC token: RequiresSignIn=%v RejectedSIWC=%q RenewalVerified=%v",
			mergedB.RequiresSignIn, mergedB.RejectedSIWCRefreshToken, mergedB.RenewalVerified)
	}

	// 5. Secrets straddling the ~300-character truncation boundary must be redacted before truncation.
	padding := make([]byte, 285)
	for i := range padding {
		padding[i] = 'x'
	}
	boundaryRaw := string(padding) + ` {"code":"boundary-secret-code-1234567890","state":"boundary-secret-state-1234567890"}`
	sanitizedBoundary := SanitizeRenewalMessage(boundaryRaw)
	if containsSubstr(sanitizedBoundary, "boundary-secret-code") || containsSubstr(sanitizedBoundary, "boundary-secret-state") {
		t.Fatalf("SanitizeRenewalMessage leaked truncation-boundary secret: %s", sanitizedBoundary)
	}
}

func containsSubstr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
