package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type unauthorizedRefreshExecutor struct {
	id string

	mu            sync.Mutex
	executeCalls  []string
	streamCalls   []string
	refreshCalls  int
	tokenInvalid  map[string]struct{}
	refreshFail   bool
	refreshTokens map[string]string
}

func (e *unauthorizedRefreshExecutor) Identifier() string { return e.id }

func (e *unauthorizedRefreshExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.executeCalls = append(e.executeCalls, auth.ID)
	token := authAccessToken(auth)
	_, invalid := e.tokenInvalid[token]
	e.mu.Unlock()
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
	e.mu.Unlock()
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
	defer e.mu.Unlock()
	e.refreshCalls++
	if e.refreshFail {
		return nil, &Error{HTTPStatus: http.StatusUnauthorized, Message: "refresh token invalid"}
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	next := e.refreshTokens[auth.ID]
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
