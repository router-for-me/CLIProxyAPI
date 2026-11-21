package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// issue7731Hook collects every Result delivered to OnResult so tests can
// assert the access token snapshot recorded on each failure path.
type issue7731Hook struct {
	NoopHook
	mu      sync.Mutex
	results []Result
	notify  chan struct{}
}

func (h *issue7731Hook) OnResult(_ context.Context, result Result) {
	h.mu.Lock()
	h.results = append(h.results, result)
	h.mu.Unlock()
	if h.notify != nil {
		select {
		case h.notify <- struct{}{}:
		default:
		}
	}
}

func (h *issue7731Hook) Results() []Result {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Result, len(h.results))
	copy(out, h.results)
	return out
}

// waitForResults blocks until at least n results were recorded or the timeout
// elapses, then returns a snapshot of everything captured so far.
func (h *issue7731Hook) waitForResults(t *testing.T, n int, timeout time.Duration) []Result {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if got := h.Results(); len(got) >= n {
			return got
		}
		if h.notify == nil {
			time.Sleep(5 * time.Millisecond)
			continue
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		select {
		case <-h.notify:
		case <-time.After(remaining):
		}
	}
	return h.Results()
}

// issue7731Executor drives streams that fail in configurable ways. Each
// ExecuteStream call records the access token visible through
// authAccessTokenForRequest at the moment the attempt ran, then hands control
// to streamFn which may mutate the per-attempt auth clone before returning.
type issue7731Executor struct {
	identifier string

	mu         sync.Mutex
	seenTokens []string
	streamFn   func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, call int) (*cliproxyexecutor.StreamResult, error)
	refreshFn  func(ctx context.Context, auth *Auth) (*Auth, error)
}

func (e *issue7731Executor) Identifier() string { return e.identifier }

func (e *issue7731Executor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.mu.Lock()
	call := len(e.seenTokens)
	e.seenTokens = append(e.seenTokens, authAccessTokenForRequest(ctx, auth, opts.Headers))
	e.mu.Unlock()
	if e.streamFn != nil {
		return e.streamFn(ctx, auth, req, opts, call)
	}
	return nil, customStatusError{code: http.StatusTooManyRequests, msg: "stream unavailable"}
}

func (e *issue7731Executor) SeenTokens() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.seenTokens))
	copy(out, e.seenTokens)
	return out
}

func (e *issue7731Executor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, customStatusError{code: http.StatusTooManyRequests, msg: "unavailable"}
}

func (e *issue7731Executor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	if e.refreshFn != nil {
		return e.refreshFn(ctx, auth)
	}
	return auth, nil
}

func (e *issue7731Executor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "CountTokens not implemented"}
}

func (e *issue7731Executor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "HttpRequest not implemented"}
}

// registerIssue7731Auth registers an OAuth-style auth holding the supplied
// access token and the provided registry model list so
// executeStreamWithModelPool can iterate over the resolved candidates.
func registerIssue7731Auth(t *testing.T, m *Manager, authID, provider, accessToken string, models []string) *Auth {
	t.Helper()
	reg := registry.GetGlobalRegistry()
	infos := make([]*registry.ModelInfo, 0, len(models))
	for _, model := range models {
		infos = append(infos, &registry.ModelInfo{ID: model})
	}
	reg.RegisterClient(authID, provider, infos)
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	auth := &Auth{
		ID:       authID,
		Provider: provider,
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":          "codex",
			"access_token":  accessToken,
			"refresh_token": "issue7731-refresh",
		},
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	return auth
}

// drainIssue7731Stream consumes a stream result and reports the first chunk
// error it observes. Bootstrap and empty-stream failures are delivered through
// the chunk channel rather than the ExecuteStream return value, so callers must
// drain the stream to observe them.
func drainIssue7731Stream(streamResult *cliproxyexecutor.StreamResult) error {
	if streamResult == nil || streamResult.Chunks == nil {
		return nil
	}
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			return chunk.Err
		}
	}
	return nil
}

// assertFailuresHaveToken asserts that every recorded failure Result carries
// the exact token the attempt used, not an empty value and not a token that a
// later mutation wrote into the same auth object.
func assertFailuresHaveToken(t *testing.T, results []Result, wantTokens ...string) {
	t.Helper()
	if len(results) == 0 {
		t.Fatal("expected at least one recorded failure Result, got none")
	}
	allowed := make(map[string]struct{}, len(wantTokens))
	for _, tok := range wantTokens {
		if tok != "" {
			allowed[tok] = struct{}{}
		}
	}
	for i, result := range results {
		if result.Success {
			t.Fatalf("results[%d].Success = true, want failure result", i)
		}
		if result.AccessToken == "" {
			t.Fatalf("results[%d].AccessToken is empty, want one of %v", i, wantTokens)
		}
		if _, ok := allowed[result.AccessToken]; !ok {
			t.Fatalf("results[%d].AccessToken = %q, want one of the per-attempt token snapshots %v", i, result.AccessToken, wantTokens)
		}
	}
}

// TestExecuteStreamWithModelPool_AccessTokenOnFailurePaths exercises the
// Result{...} literals inside executeStreamWithModelPool. To prove the recorded
// AccessToken is the snapshot taken at attempt time (and not a value resolved
// again when the result is recorded), each sub-case mutates the per-attempt
// auth clone after the snapshot point but before the failure result is
// recorded, so a record-time token lookup would observe a different value.
func TestExecuteStreamWithModelPool_AccessTokenOnFailurePaths(t *testing.T) {
	const (
		provider   = "codex"
		tokenT1    = "issue7731-token-t1"
		tokenT2    = "issue7731-token-t2"
		tampered   = "issue7731-tampered-token"
		routeModel = "gpt-5.5"
	)

	newManager := func() (*Manager, *issue7731Hook) {
		hook := &issue7731Hook{notify: make(chan struct{}, 8)}
		return NewManager(nil, nil, hook), hook
	}

	// tamperAuth rewrites the access token on the per-attempt auth clone. The
	// clone is the same object executeStreamWithModelPool passes to
	// recordExecutionResult, so a record-time token lookup would observe the
	// tampered value while the attempt-time snapshot must not.
	tamperAuth := func(auth *Auth) {
		if auth != nil && auth.Metadata != nil {
			auth.Metadata["access_token"] = tampered
		}
	}

	t.Run("stream start failure", func(t *testing.T) {
		m, hook := newManager()
		registerIssue7731Auth(t, m, "auth-7731-start", provider, tokenT1, []string{routeModel})

		var snapToken string
		exec := &issue7731Executor{identifier: provider}
		exec.streamFn = func(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, opts cliproxyexecutor.Options, _ int) (*cliproxyexecutor.StreamResult, error) {
			snapToken = authAccessTokenForRequest(ctx, auth, opts.Headers)
			tamperAuth(auth)
			return nil, customStatusError{code: http.StatusTooManyRequests, msg: "rate limited"}
		}
		m.RegisterExecutor(exec)

		_, errStream := m.ExecuteStream(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: routeModel}, cliproxyexecutor.Options{})
		if errStream == nil {
			t.Fatal("expected stream start error, got nil")
		}
		results := hook.waitForResults(t, 1, 3*time.Second)
		assertFailuresHaveToken(t, results, snapToken)
	})

	t.Run("bootstrap failure terminal single model", func(t *testing.T) {
		m, hook := newManager()
		registerIssue7731Auth(t, m, "auth-7731-bootstrap", provider, tokenT1, []string{routeModel})

		var snapToken string
		exec := &issue7731Executor{identifier: provider}
		exec.streamFn = func(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, opts cliproxyexecutor.Options, _ int) (*cliproxyexecutor.StreamResult, error) {
			snapToken = authAccessTokenForRequest(ctx, auth, opts.Headers)
			tamperAuth(auth)
			ch := make(chan cliproxyexecutor.StreamChunk, 1)
			ch <- cliproxyexecutor.StreamChunk{Err: customStatusError{code: http.StatusInternalServerError, msg: "bootstrap exploded"}}
			close(ch)
			return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
		}
		m.RegisterExecutor(exec)

		streamResult, errStream := m.ExecuteStream(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: routeModel}, cliproxyexecutor.Options{})
		if errStream == nil {
			errStream = drainIssue7731Stream(streamResult)
		}
		if errStream == nil {
			t.Fatal("expected bootstrap error, got nil")
		}
		results := hook.waitForResults(t, 1, 3*time.Second)
		assertFailuresHaveToken(t, results, snapToken)
	})

	t.Run("empty stream failure", func(t *testing.T) {
		m, hook := newManager()
		registerIssue7731Auth(t, m, "auth-7731-empty", provider, tokenT1, []string{routeModel})

		var snapToken string
		exec := &issue7731Executor{identifier: provider}
		exec.streamFn = func(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, opts cliproxyexecutor.Options, _ int) (*cliproxyexecutor.StreamResult, error) {
			snapToken = authAccessTokenForRequest(ctx, auth, opts.Headers)
			tamperAuth(auth)
			ch := make(chan cliproxyexecutor.StreamChunk)
			close(ch)
			return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
		}
		m.RegisterExecutor(exec)

		streamResult, errStream := m.ExecuteStream(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: routeModel}, cliproxyexecutor.Options{})
		if errStream == nil {
			errStream = drainIssue7731Stream(streamResult)
		}
		if errStream == nil {
			t.Fatal("expected empty stream error, got nil")
		}
		results := hook.waitForResults(t, 1, 3*time.Second)
		assertFailuresHaveToken(t, results, snapToken)
	})

	// A 401 drives tryRefreshAfterUnauthorized, which swaps auth for a
	// refreshed credential (T2) and re-captures the snapshot before the retry.
	// The recorded failure must report T2, the token the retry actually used.
	t.Run("stream start unauthorized then refreshed retry fails", func(t *testing.T) {
		m, hook := newManager()
		registerIssue7731Auth(t, m, "auth-7731-401", provider, tokenT1, []string{routeModel})

		exec := &issue7731Executor{identifier: provider}
		exec.refreshFn = func(_ context.Context, auth *Auth) (*Auth, error) {
			refreshed := auth.Clone()
			refreshed.Metadata["access_token"] = tokenT2
			return refreshed, nil
		}
		exec.streamFn = func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options, _ int) (*cliproxyexecutor.StreamResult, error) {
			return nil, &Error{HTTPStatus: http.StatusUnauthorized, Message: "token invalidated"}
		}
		m.RegisterExecutor(exec)

		_, errStream := m.ExecuteStream(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: routeModel}, cliproxyexecutor.Options{})
		if errStream == nil {
			t.Fatal("expected stream error after refreshed retry failure, got nil")
		}
		results := hook.waitForResults(t, 1, 3*time.Second)
		assertFailuresHaveToken(t, results, tokenT2)
	})

	// Bootstrap-phase 401 (error arrives on the first chunk) must also rotate
	// the snapshot so the recorded failure carries the refreshed retry token.
	t.Run("bootstrap unauthorized then refreshed retry fails", func(t *testing.T) {
		m, hook := newManager()
		registerIssue7731Auth(t, m, "auth-7731-401b", provider, tokenT1, []string{routeModel})

		exec := &issue7731Executor{identifier: provider}
		exec.refreshFn = func(_ context.Context, auth *Auth) (*Auth, error) {
			refreshed := auth.Clone()
			refreshed.Metadata["access_token"] = tokenT2
			return refreshed, nil
		}
		exec.streamFn = func(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options, _ int) (*cliproxyexecutor.StreamResult, error) {
			ch := make(chan cliproxyexecutor.StreamChunk, 1)
			ch <- cliproxyexecutor.StreamChunk{Err: &Error{HTTPStatus: http.StatusUnauthorized, Message: "token invalidated"}}
			close(ch)
			return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
		}
		m.RegisterExecutor(exec)

		streamResult, errStream := m.ExecuteStream(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: routeModel}, cliproxyexecutor.Options{})
		if errStream == nil {
			errStream = drainIssue7731Stream(streamResult)
		}
		if errStream == nil {
			t.Fatal("expected stream error after refreshed retry failure, got nil")
		}
		results := hook.waitForResults(t, 1, 3*time.Second)
		assertFailuresHaveToken(t, results, tokenT2)
	})
}
