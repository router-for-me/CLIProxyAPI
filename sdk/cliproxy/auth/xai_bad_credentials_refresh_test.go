package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// xaiBadCredentialsRefreshExecutor mirrors unauthorizedRefreshExecutor but returns
// an xAI 403 bad-credentials response (instead of a 401) for an invalid token.
type xaiBadCredentialsRefreshExecutor struct {
	id string

	mu            sync.Mutex
	executeCalls  []string
	streamCalls   []string
	refreshCalls  int
	tokenInvalid  map[string]struct{}
	refreshFail   bool
	refreshTokens map[string]string
}

func (e *xaiBadCredentialsRefreshExecutor) Identifier() string { return e.id }

func (e *xaiBadCredentialsRefreshExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.executeCalls = append(e.executeCalls, auth.ID)
	token := authAccessToken(auth)
	_, invalid := e.tokenInvalid[token]
	e.mu.Unlock()
	if invalid {
		return cliproxyexecutor.Response{}, &Error{
			HTTPStatus: http.StatusForbidden,
			Message:    "unauthenticated:bad-credentials",
		}
	}
	return cliproxyexecutor.Response{Payload: []byte(auth.ID + ":" + token)}, nil
}

func (e *xaiBadCredentialsRefreshExecutor) ExecuteStream(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.mu.Lock()
	e.streamCalls = append(e.streamCalls, auth.ID)
	token := authAccessToken(auth)
	_, invalid := e.tokenInvalid[token]
	e.mu.Unlock()
	if invalid {
		return nil, &Error{
			HTTPStatus: http.StatusForbidden,
			Message:    "unauthenticated:bad-credentials",
		}
	}
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte(auth.ID + ":" + token)}
	close(ch)
	return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Auth": {auth.ID}}, Chunks: ch}, nil
}

func (e *xaiBadCredentialsRefreshExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
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

func (e *xaiBadCredentialsRefreshExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func (e *xaiBadCredentialsRefreshExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *xaiBadCredentialsRefreshExecutor) ExecuteCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.executeCalls))
	copy(out, e.executeCalls)
	return out
}

func (e *xaiBadCredentialsRefreshExecutor) RefreshCalls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.refreshCalls
}

func newXaiBadCredentialsRefreshFixture(t *testing.T, refreshFail bool) (*Manager, *xaiBadCredentialsRefreshExecutor, *Auth, *Auth, string) {
	t.Helper()

	prev := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })

	model := "grok-4"
	primary := &Auth{
		ID:       "aa-primary-xai",
		Provider: "xai",
		Metadata: map[string]any{
			"access_token":  "stale-access-token",
			"refresh_token": "primary-refresh-token",
		},
	}
	backup := &Auth{
		ID:       "bb-backup-xai",
		Provider: "xai",
		Metadata: map[string]any{
			"access_token":  "backup-access-token",
			"refresh_token": "backup-refresh-token",
		},
	}

	executor := &xaiBadCredentialsRefreshExecutor{
		id: "xai",
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
	reg.RegisterClient(primary.ID, "xai", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(backup.ID, "xai", []*registry.ModelInfo{{ID: model}})
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

func TestManager_Execute_XaiBadCredentialsRefreshesCurrentAuthBeforeFallback(t *testing.T) {
	m, executor, primary, backup, model := newXaiBadCredentialsRefreshFixture(t, false)

	resp, errExecute := m.Execute(context.Background(), []string{"xai"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
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

func TestManager_Execute_XaiBadCredentialsRefreshFailureFallsBackToNextAuth(t *testing.T) {
	m, executor, primary, backup, model := newXaiBadCredentialsRefreshFixture(t, true)

	resp, errExecute := m.Execute(context.Background(), []string{"xai"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
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
	// Key assertion: the xAI bad-credentials branch must tag the model LastError as
	// "unauthorized" even though the underlying status is a 403. This is what lets a
	// later successful refresh resume the model early (see the resume test below).
	if state.LastError == nil || state.LastError.Code != "unauthorized" {
		t.Fatalf("expected suspended model LastError.Code = unauthorized, got %+v", state.LastError)
	}
}

func TestManager_Execute_XaiBadCredentialsResumesAfterLaterRefreshSucceeds(t *testing.T) {
	m, executor, primary, _, model := newXaiBadCredentialsRefreshFixture(t, true)

	// First request: refresh fails, primary gets suspended, request falls back to backup.
	if _, errExecute := m.Execute(context.Background(), []string{"xai"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); errExecute != nil {
		t.Fatalf("Execute error = %v, want success via backup", errExecute)
	}

	reg := registry.GetGlobalRegistry()
	suspendedCount := reg.GetModelCount(model)
	if suspended, ok := m.GetByID(primary.ID); !ok || suspended == nil {
		t.Fatalf("primary auth missing after failed refresh")
	} else if state := suspended.ModelStates[model]; state == nil || !state.Unavailable {
		t.Fatalf("expected primary model to be suspended before resume, got %+v", state)
	}

	// Second refresh succeeds (valid token, refresh no longer failing). The
	// bad-credentials LastError.Code = "unauthorized" tag is what makes
	// clearUnauthorizedModelStates recognize and resume the suspended model.
	executor.mu.Lock()
	executor.refreshFail = false
	executor.refreshTokens[primary.ID] = "fresh-access-token"
	executor.mu.Unlock()

	if _, errRefresh := m.refreshAuthForRequest(context.Background(), primary.ID, ""); errRefresh != nil {
		t.Fatalf("refreshAuthForRequest error = %v, want success", errRefresh)
	}

	// (a) registry count restored: the suspended primary client is back in rotation.
	if got := reg.GetModelCount(model); got <= suspendedCount {
		t.Fatalf("expected model count to increase after resume (was %d), got %d", suspendedCount, got)
	}
	if got := reg.GetModelCount(model); got == 0 {
		t.Fatalf("expected non-zero model count after resume")
	}

	// (b) primary's model state no longer marked unavailable.
	updated, ok := m.GetByID(primary.ID)
	if !ok || updated == nil {
		t.Fatalf("primary auth missing after resume refresh")
	}
	if state := updated.ModelStates[model]; state != nil && state.Unavailable {
		t.Fatalf("expected primary model to be resumed (not unavailable) after successful refresh, got %+v", state)
	}
}
