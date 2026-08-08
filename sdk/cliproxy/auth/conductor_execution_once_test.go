package auth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// onceStatusError carries an HTTP status the way provider executors do.
type onceStatusError struct {
	status  int
	message string
}

func (e *onceStatusError) Error() string { return e.message }

func (e *onceStatusError) StatusCode() int { return e.status }

// onceTestExecutor counts every executor entry point the one-shot path may touch.
type onceTestExecutor struct {
	provider string

	executeCalls  atomic.Int32
	refreshCalls  atomic.Int32
	prepareCalls  atomic.Int32
	httpCalls     atomic.Int32
	countCalls    atomic.Int32
	executeErr    error
	refreshErr    error
	refreshFunc   func(ctx context.Context, auth *Auth) (*Auth, error)
	executeFunc   func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
	mu            sync.Mutex
	executedModel []string
}

func (e *onceTestExecutor) Identifier() string { return e.provider }

func (e *onceTestExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.executeCalls.Add(1)
	e.mu.Lock()
	e.executedModel = append(e.executedModel, req.Model)
	e.mu.Unlock()
	if e.executeFunc != nil {
		return e.executeFunc(ctx, auth, req, opts)
	}
	if e.executeErr != nil {
		return cliproxyexecutor.Response{}, e.executeErr
	}
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (e *onceTestExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, errors.New("stream not implemented")
}

func (e *onceTestExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	e.refreshCalls.Add(1)
	if e.refreshFunc != nil {
		return e.refreshFunc(ctx, auth)
	}
	if e.refreshErr != nil {
		return nil, e.refreshErr
	}
	return auth, nil
}

func (e *onceTestExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.countCalls.Add(1)
	return cliproxyexecutor.Response{}, nil
}

func (e *onceTestExecutor) HttpRequest(_ context.Context, _ *Auth, _ *http.Request) (*http.Response, error) {
	e.httpCalls.Add(1)
	return nil, errors.New("http request not implemented")
}

// PrepareRequest implements RequestPreparer so the one-shot HTTP path can inject
// credentials through the executor exactly as PrepareHttpRequest requires.
func (e *onceTestExecutor) PrepareRequest(req *http.Request, auth *Auth) error {
	e.prepareCalls.Add(1)
	if req == nil {
		return errors.New("nil request")
	}
	req.Header.Set("X-Api-Key", "secret-key")
	req.Header.Set("Authorization", "Bearer secret-token")
	return nil
}

func (e *onceTestExecutor) models() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.executedModel...)
}

// httpOnceWithAuth exercises the shared one-shot HTTP core against a caller
// resolved auth. DoHTTPOnce resolves the credential by ID, refreshes and prepares
// it, and then funnels into exactly this core, so the redirect, marking and
// attempt-fact contracts are pinned here once instead of at every entry point.
func httpOnceWithAuth(m *Manager, ctx context.Context, auth *Auth, req *http.Request, policy HTTPRedirectPolicy) (*http.Response, HTTPAttemptFacts, error) {
	return m.httpOnce(ctx, auth, req, "test-model", policy)
}

// onceAuthByID reads a registered auth snapshot directly from the manager, which
// is what the one-shot HTTP core receives after DoHTTPOnce resolves an auth ID.
func onceAuthByID(t *testing.T, manager *Manager, id string) *Auth {
	t.Helper()
	manager.mu.RLock()
	auth := manager.auths[id]
	manager.mu.RUnlock()
	if auth == nil {
		t.Fatalf("auth %q is not registered", id)
	}
	return auth.Clone()
}

// onceResultHook records every MarkResult the manager emits.
type onceResultHook struct {
	mu      sync.Mutex
	results []Result
}

func (h *onceResultHook) OnAuthRegistered(context.Context, *Auth) {}

func (h *onceResultHook) OnAuthUpdated(context.Context, *Auth) {}

func (h *onceResultHook) OnResult(_ context.Context, result Result) {
	h.mu.Lock()
	h.results = append(h.results, result)
	h.mu.Unlock()
}

func (h *onceResultHook) snapshot() []Result {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Result(nil), h.results...)
}

func newOnceTestManager(t *testing.T, executor *onceTestExecutor, auth *Auth) (*Manager, *onceResultHook) {
	t.Helper()
	hook := &onceResultHook{}
	manager := NewManager(nil, &RoundRobinSelector{}, hook)
	// A generous retry budget proves the one-shot path never consults it.
	manager.SetRetryConfig(5, time.Second, 5)
	manager.RegisterExecutor(executor)
	if auth != nil {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("register auth: %v", err)
		}
	}
	return manager, hook
}

func newOnceTestAuth(id string) *Auth {
	return &Auth{
		ID:       id,
		Provider: "codex",
		Status:   StatusActive,
		Attributes: map[string]string{
			"api_key": "test-key",
		},
	}
}

func onceRequest(authID string) ExecuteWithAuthOnceRequest {
	return ExecuteWithAuthOnceRequest{
		AuthID:  authID,
		Model:   "test-model",
		Request: cliproxyexecutor.Request{Model: "test-model", Payload: []byte(`{}`)},
		Options: cliproxyexecutor.Options{Metadata: map[string]any{}},
	}
}

func TestManagerExecuteWithAuthOnce_InvokesExecutorAndMarksExactlyOnce(t *testing.T) {
	tests := []struct {
		name        string
		executeErr  error
		wantSuccess bool
		wantStatus  int
	}{
		{name: "success", wantSuccess: true},
		{name: "unauthorized", executeErr: &onceStatusError{status: http.StatusUnauthorized, message: "unauthorized"}, wantStatus: http.StatusUnauthorized},
		{name: "request timeout", executeErr: &onceStatusError{status: http.StatusRequestTimeout, message: "timeout"}, wantStatus: http.StatusRequestTimeout},
		{name: "rate limited", executeErr: &onceStatusError{status: http.StatusTooManyRequests, message: "slow down"}, wantStatus: http.StatusTooManyRequests},
		{name: "server error", executeErr: &onceStatusError{status: http.StatusInternalServerError, message: "boom"}, wantStatus: http.StatusInternalServerError},
		{name: "transport error", executeErr: errors.New("connection reset by peer")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executor := &onceTestExecutor{provider: "codex", executeErr: tt.executeErr}
			manager, hook := newOnceTestManager(t, executor, newOnceTestAuth("once-auth"))

			_, facts, err := manager.ExecuteWithAuthOnce(context.Background(), onceRequest("once-auth"))
			if tt.wantSuccess && err != nil {
				t.Fatalf("ExecuteWithAuthOnce() error = %v, want nil", err)
			}
			if !tt.wantSuccess && err == nil {
				t.Fatalf("ExecuteWithAuthOnce() error = nil, want %v", tt.executeErr)
			}
			if got := executor.executeCalls.Load(); got != 1 {
				t.Fatalf("executor invocations = %d, want 1", got)
			}
			results := hook.snapshot()
			if len(results) != 1 {
				t.Fatalf("MarkResult calls = %d, want 1", len(results))
			}
			if results[0].Success != tt.wantSuccess {
				t.Fatalf("Result.Success = %v, want %v", results[0].Success, tt.wantSuccess)
			}
			if results[0].AuthID != "once-auth" {
				t.Fatalf("Result.AuthID = %q, want %q", results[0].AuthID, "once-auth")
			}
			if tt.wantStatus != 0 && facts.StatusCode != tt.wantStatus {
				t.Fatalf("facts.StatusCode = %d, want %d", facts.StatusCode, tt.wantStatus)
			}
			if got := executor.countCalls.Load(); got != 0 {
				t.Fatalf("CountTokens calls = %d, want 0", got)
			}
			if got := executor.models(); len(got) != 1 || got[0] != "test-model" {
				t.Fatalf("executed models = %v, want [test-model]", got)
			}
		})
	}
}

func TestManagerExecuteWithAuthOnce_UnauthorizedDoesNotReplay(t *testing.T) {
	executor := &onceTestExecutor{
		provider:   "codex",
		executeErr: &onceStatusError{status: http.StatusUnauthorized, message: "unauthorized"},
	}
	manager, hook := newOnceTestManager(t, executor, newOnceTestAuth("no-replay"))

	_, _, err := manager.ExecuteWithAuthOnce(context.Background(), onceRequest("no-replay"))
	if err == nil {
		t.Fatal("ExecuteWithAuthOnce() error = nil, want unauthorized")
	}
	if got := executor.executeCalls.Load(); got != 1 {
		t.Fatalf("executor invocations after 401 = %d, want 1", got)
	}
	if got := executor.refreshCalls.Load(); got != 0 {
		t.Fatalf("post-401 refresh calls = %d, want 0", got)
	}
	if got := len(hook.snapshot()); got != 1 {
		t.Fatalf("MarkResult calls = %d, want 1", got)
	}
}

func TestManagerExecuteWithAuthOnce_NeverHopsToAnotherCredential(t *testing.T) {
	executor := &onceTestExecutor{
		provider:   "codex",
		executeErr: &onceStatusError{status: http.StatusInternalServerError, message: "boom"},
	}
	manager, hook := newOnceTestManager(t, executor, newOnceTestAuth("pinned-auth"))
	if _, err := manager.Register(context.Background(), newOnceTestAuth("sibling-auth")); err != nil {
		t.Fatalf("register sibling auth: %v", err)
	}

	if _, _, err := manager.ExecuteWithAuthOnce(context.Background(), onceRequest("pinned-auth")); err == nil {
		t.Fatal("ExecuteWithAuthOnce() error = nil, want failure")
	}
	if got := executor.executeCalls.Load(); got != 1 {
		t.Fatalf("executor invocations = %d, want 1", got)
	}
	for _, result := range hook.snapshot() {
		if result.AuthID != "pinned-auth" {
			t.Fatalf("Result.AuthID = %q, want only the pinned auth", result.AuthID)
		}
	}
}

func TestManagerExecuteWithAuthOnce_PreflightFailuresNeverDispatch(t *testing.T) {
	tests := []struct {
		name     string
		authID   string
		provider string
		setup    func(t *testing.T, manager *Manager)
		wantCode string
	}{
		{name: "empty auth id", authID: "", wantCode: "auth_not_found"},
		{name: "unknown auth id", authID: "missing", wantCode: "auth_not_found"},
		{name: "unregistered executor", authID: "preflight", provider: "not-registered", wantCode: "executor_not_found"},
		{
			name:   "home mode",
			authID: "preflight",
			setup: func(_ *testing.T, manager *Manager) {
				manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
			},
			wantCode: "auth_not_durable",
		},
		{
			name:   "session scoped auth",
			authID: "preflight",
			setup: func(_ *testing.T, manager *Manager) {
				manager.mu.Lock()
				manager.homeRuntimeAuths["session-1"] = map[string]*Auth{"preflight": newOnceTestAuth("preflight")}
				manager.mu.Unlock()
			},
			wantCode: "auth_not_durable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executor := &onceTestExecutor{provider: "codex"}
			manager, hook := newOnceTestManager(t, executor, newOnceTestAuth("preflight"))
			if tt.setup != nil {
				tt.setup(t, manager)
			}

			in := onceRequest(tt.authID)
			in.Provider = tt.provider
			_, facts, err := manager.ExecuteWithAuthOnce(context.Background(), in)

			var authErr *Error
			if !errors.As(err, &authErr) || authErr == nil {
				t.Fatalf("ExecuteWithAuthOnce() error = %v, want *Error", err)
			}
			if authErr.Code != tt.wantCode {
				t.Fatalf("error code = %q, want %q", authErr.Code, tt.wantCode)
			}
			if facts.RequestWritten {
				t.Fatalf("facts.RequestWritten = true, want false for a pre-flight failure")
			}
			if facts.RequestCount != 0 {
				t.Fatalf("facts.RequestCount = %d, want 0", facts.RequestCount)
			}
			if got := executor.executeCalls.Load(); got != 0 {
				t.Fatalf("executor invocations = %d, want 0", got)
			}
			if got := len(hook.snapshot()); got != 0 {
				t.Fatalf("MarkResult calls = %d, want 0", got)
			}
		})
	}
}

func TestManagerExecuteWithAuthOnce_RefreshUsesEffectiveExecutorKey(t *testing.T) {
	executor := &onceTestExecutor{provider: "openai-compatible-custom"}
	auth := newOnceTestAuth("refresh-compatible")
	auth.Provider = "plugin-provider"
	auth.Attributes["compat_name"] = "custom"
	auth.Attributes["provider_key"] = "custom"
	auth.Attributes["refresh_interval_seconds"] = "1"
	auth.Metadata = map[string]any{
		"access_token":  "stale-token",
		"refresh_token": "refresh-token",
	}
	manager, _ := newOnceTestManager(t, executor, auth)

	request := onceRequest(auth.ID)
	request.Provider = "openai-compatible-custom"
	if _, _, err := manager.ExecuteWithAuthOnce(context.Background(), request); err != nil {
		t.Fatalf("ExecuteWithAuthOnce() error = %v", err)
	}
	if got := executor.refreshCalls.Load(); got != 1 {
		t.Fatalf("refresh calls = %d, want 1 through effective executor key", got)
	}
	if got := executor.executeCalls.Load(); got != 1 {
		t.Fatalf("executor calls = %d, want 1", got)
	}
}

func TestManagerExecuteWithAuthOnce_RefreshFailureFailsClosed(t *testing.T) {
	executor := &onceTestExecutor{provider: "codex", refreshErr: errors.New("refresh rejected")}
	auth := newOnceTestAuth("refresh-fail")
	auth.Attributes["refresh_interval_seconds"] = "1"
	manager, hook := newOnceTestManager(t, executor, auth)

	_, facts, err := manager.ExecuteWithAuthOnce(context.Background(), onceRequest("refresh-fail"))
	if err == nil {
		t.Fatal("ExecuteWithAuthOnce() error = nil, want refresh failure")
	}
	if got := executor.refreshCalls.Load(); got != 1 {
		t.Fatalf("refresh calls = %d, want 1", got)
	}
	if got := executor.executeCalls.Load(); got != 0 {
		t.Fatalf("executor invocations = %d, want 0", got)
	}
	if facts.RequestWritten {
		t.Fatal("facts.RequestWritten = true, want false when refresh failed before dispatch")
	}
	if got := len(hook.snapshot()); got != 0 {
		t.Fatalf("MarkResult calls = %d, want 0", got)
	}
}

func TestManagerRefreshAuthForOnce_ReloadsCurrentAuthWhenNoLongerDue(t *testing.T) {
	executor := &onceTestExecutor{provider: "codex"}
	auth := newOnceTestAuth("refresh-reload")
	auth.Metadata = map[string]any{"access_token": "stale-token"}
	manager, _ := newOnceTestManager(t, executor, auth)

	stale := onceAuthByID(t, manager, auth.ID)
	manager.mu.Lock()
	current := manager.auths[auth.ID].Clone()
	current.Metadata["access_token"] = "fresh-token"
	current.LastRefreshedAt = time.Now()
	manager.auths[auth.ID] = current
	manager.mu.Unlock()

	reloaded, errRefresh := manager.refreshAuthForOnce(context.Background(), stale)
	if errRefresh != nil {
		t.Fatalf("refreshAuthForOnce() error = %v", errRefresh)
	}
	if got := authAccessToken(reloaded); got != "fresh-token" {
		t.Fatalf("reloaded access token = %q, want fresh-token", got)
	}
	if got := executor.refreshCalls.Load(); got != 0 {
		t.Fatalf("refresh calls = %d, want 0", got)
	}
}

func TestManagerExecuteWithAuthOnce_ConcurrentRefreshWaitsForOwner(t *testing.T) {
	refreshStarted := make(chan struct{})
	releaseRefresh := make(chan struct{})
	executor := &onceTestExecutor{provider: "codex"}
	executor.refreshFunc = func(ctx context.Context, auth *Auth) (*Auth, error) {
		close(refreshStarted)
		select {
		case <-releaseRefresh:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		updated := auth.Clone()
		if updated.Metadata == nil {
			updated.Metadata = map[string]any{}
		}
		updated.Metadata["access_token"] = "fresh-token"
		return updated, nil
	}
	auth := newOnceTestAuth("refresh-concurrent")
	auth.Attributes["refresh_interval_seconds"] = "1"
	auth.Metadata = map[string]any{"access_token": "stale-token"}
	manager, _ := newOnceTestManager(t, executor, auth)

	const callers = 2
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		go func() {
			_, _, err := manager.ExecuteWithAuthOnce(context.Background(), onceRequest("refresh-concurrent"))
			errs <- err
		}()
	}

	<-refreshStarted
	time.Sleep(20 * time.Millisecond)
	if got := executor.executeCalls.Load(); got != 0 {
		t.Fatalf("executor invocations before refresh owner completed = %d, want 0", got)
	}
	close(releaseRefresh)
	for i := 0; i < callers; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("ExecuteWithAuthOnce() error = %v", err)
		}
	}
	if got := executor.refreshCalls.Load(); got != 1 {
		t.Fatalf("refresh calls = %d, want exactly one", got)
	}
	if got := executor.executeCalls.Load(); got != callers {
		t.Fatalf("executor invocations = %d, want %d", got, callers)
	}
}

func TestManagerExecuteWithAuthOnce_RefreshRunsBeforeDispatch(t *testing.T) {
	var order []string
	var mu sync.Mutex
	executor := &onceTestExecutor{provider: "codex"}
	executor.executeFunc = func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
		mu.Lock()
		order = append(order, "execute")
		mu.Unlock()
		return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
	}
	auth := newOnceTestAuth("refresh-order")
	auth.Attributes["refresh_interval_seconds"] = "1"
	manager, _ := newOnceTestManager(t, executor, auth)

	if _, _, err := manager.ExecuteWithAuthOnce(context.Background(), onceRequest("refresh-order")); err != nil {
		t.Fatalf("ExecuteWithAuthOnce() error = %v", err)
	}
	if got := executor.refreshCalls.Load(); got != 1 {
		t.Fatalf("refresh calls = %d, want 1", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 1 || order[0] != "execute" {
		t.Fatalf("execute order = %v, want a single execute after refresh", order)
	}
}

type oncePrepareFailExecutor struct {
	onceTestExecutor
	prepareAuthCalls atomic.Int32
}

func (e *oncePrepareFailExecutor) ShouldPrepareRequestAuth(*Auth) bool { return true }

func (e *oncePrepareFailExecutor) PrepareRequestAuth(context.Context, *Auth) (*Auth, error) {
	e.prepareAuthCalls.Add(1)
	return nil, errors.New("prepare failed")
}

func TestManagerExecuteWithAuthOnce_PrepareFailureMarksOnceWithoutDispatch(t *testing.T) {
	executor := &oncePrepareFailExecutor{onceTestExecutor: onceTestExecutor{provider: "codex"}}
	hook := &onceResultHook{}
	manager := NewManager(nil, &RoundRobinSelector{}, hook)
	manager.RegisterExecutor(executor)
	if _, err := manager.Register(context.Background(), newOnceTestAuth("prepare-fail")); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	_, facts, err := manager.ExecuteWithAuthOnce(context.Background(), onceRequest("prepare-fail"))
	if err == nil {
		t.Fatal("ExecuteWithAuthOnce() error = nil, want prepare failure")
	}
	if got := executor.prepareAuthCalls.Load(); got != 1 {
		t.Fatalf("PrepareRequestAuth calls = %d, want 1", got)
	}
	if got := executor.executeCalls.Load(); got != 0 {
		t.Fatalf("executor invocations = %d, want 0", got)
	}
	if facts.RequestWritten {
		t.Fatal("facts.RequestWritten = true, want false when preparation failed")
	}
	results := hook.snapshot()
	if len(results) != 1 {
		t.Fatalf("MarkResult calls = %d, want 1", len(results))
	}
	if results[0].Success {
		t.Fatal("Result.Success = true, want false for a preparation failure")
	}
}

func TestManagerExecuteWithAuthOnce_InterceptorTerminationSkipsExecutorAndMarking(t *testing.T) {
	executor := &onceTestExecutor{provider: "codex"}
	manager, hook := newOnceTestManager(t, executor, newOnceTestAuth("terminated"))

	in := onceRequest("terminated")
	in.Options.RequestAfterAuthInterceptor = func(context.Context, cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
		return cliproxyexecutor.RequestAfterAuthInterceptResponse{Terminate: true, StatusCode: http.StatusTeapot}
	}

	_, facts, err := manager.ExecuteWithAuthOnce(context.Background(), in)
	var terminated *cliproxyexecutor.RequestTerminatedError
	if !errors.As(err, &terminated) || terminated == nil {
		t.Fatalf("ExecuteWithAuthOnce() error = %v, want *RequestTerminatedError", err)
	}
	if terminated.HTTPStatus != http.StatusTeapot {
		t.Fatalf("terminated status = %d, want %d", terminated.HTTPStatus, http.StatusTeapot)
	}
	if got := executor.executeCalls.Load(); got != 0 {
		t.Fatalf("executor invocations = %d, want 0", got)
	}
	if facts.RequestWritten {
		t.Fatal("facts.RequestWritten = true, want false for an interceptor termination")
	}
	if got := len(hook.snapshot()); got != 0 {
		t.Fatalf("MarkResult calls = %d, want 0", got)
	}
}

func TestManagerExecuteWithAuthOnce_ModelPoolCollapsesToOneInvocation(t *testing.T) {
	alias := "pooled-alias"
	executor := &openAICompatPoolExecutor{
		id:            openAICompatPoolProviderKey,
		executeErrors: map[string]error{"pool-model-a": &onceStatusError{status: http.StatusInternalServerError, message: "boom"}},
	}
	manager := newOpenAICompatPoolTestManager(t, alias, []internalconfig.OpenAICompatibilityModel{
		{Name: "pool-model-a", Alias: alias},
		{Name: "pool-model-b", Alias: alias},
	}, executor)

	authID := "pool-auth-" + t.Name()
	_, _, err := manager.ExecuteWithAuthOnce(context.Background(), ExecuteWithAuthOnceRequest{
		AuthID:  authID,
		Model:   alias,
		Request: cliproxyexecutor.Request{Model: alias},
		Options: cliproxyexecutor.Options{Metadata: map[string]any{}},
	})
	if err == nil {
		t.Fatal("ExecuteWithAuthOnce() error = nil, want the first pooled model failure")
	}
	if got := executor.ExecuteModels(); len(got) != 1 {
		t.Fatalf("pooled executor invocations = %v, want exactly one", got)
	}
}

func TestManagerExecuteWithAuthOnce_ReportsAttemptFactsFromTransport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("upstream"))
	}))
	defer server.Close()

	executor := &onceTestExecutor{provider: "codex"}
	executor.executeFunc = func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
		transport, _ := ctx.Value("cliproxy.roundtripper").(http.RoundTripper)
		if transport == nil {
			return cliproxyexecutor.Response{}, errors.New("missing manager transport")
		}
		req, errNew := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
		if errNew != nil {
			return cliproxyexecutor.Response{}, errNew
		}
		resp, errDo := (&http.Client{Transport: transport}).Do(req)
		if errDo != nil {
			return cliproxyexecutor.Response{}, errDo
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		return cliproxyexecutor.Response{Payload: body}, nil
	}
	manager, _ := newOnceTestManager(t, executor, newOnceTestAuth("facts-auth"))

	resp, facts, err := manager.ExecuteWithAuthOnce(context.Background(), onceRequest("facts-auth"))
	if err != nil {
		t.Fatalf("ExecuteWithAuthOnce() error = %v", err)
	}
	if string(resp.Payload) != "upstream" {
		t.Fatalf("response payload = %q, want %q", resp.Payload, "upstream")
	}
	if facts.RequestCount != 1 {
		t.Fatalf("facts.RequestCount = %d, want 1", facts.RequestCount)
	}
	if !facts.RequestWritten || !facts.ResponseStarted {
		t.Fatalf("facts = %+v, want request written and response started", facts)
	}
	if facts.StatusCode != http.StatusOK {
		t.Fatalf("facts.StatusCode = %d, want %d", facts.StatusCode, http.StatusOK)
	}
}

func TestManagerExecuteWithAuthOnce_ConcurrentCallsMarkOncePerCall(t *testing.T) {
	executor := &onceTestExecutor{provider: "codex"}
	manager, hook := newOnceTestManager(t, executor, newOnceTestAuth("concurrent-auth"))

	const callers = 8
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			if _, _, err := manager.ExecuteWithAuthOnce(context.Background(), onceRequest("concurrent-auth")); err != nil {
				t.Errorf("ExecuteWithAuthOnce() error = %v", err)
			}
		}()
	}
	wg.Wait()

	if got := executor.executeCalls.Load(); got != callers {
		t.Fatalf("executor invocations = %d, want %d", got, callers)
	}
	if got := len(hook.snapshot()); got != callers {
		t.Fatalf("MarkResult calls = %d, want %d", got, callers)
	}
}

// onceRoundTripperFunc is a transport that ignores httptrace, standing in for a
// host supplied RoundTripper.
type onceRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f onceRoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// onceErrorCode returns the *Error code carried by err, or "" when err is nil.
func onceErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var authErr *Error
	if errors.As(err, &authErr) && authErr != nil {
		return authErr.Code
	}
	return err.Error()
}

func onceHTTPRequest(t *testing.T, ctx context.Context, method, target string, body io.Reader) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	return req
}

func TestManagerHTTPOnceCore_MarksOnceAndOnlySucceedsOn2xx(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		retryAfter  string
		wantSuccess bool
		wantRetry   bool
		wantErrCode string
	}{
		{name: "ok", status: http.StatusOK, wantSuccess: true},
		{name: "created", status: http.StatusCreated, wantSuccess: true},
		{name: "found", status: http.StatusFound, wantErrCode: "redirect_denied"},
		{name: "not found", status: http.StatusNotFound},
		{name: "unauthorized", status: http.StatusUnauthorized},
		{name: "request timeout", status: http.StatusRequestTimeout},
		{name: "rate limited", status: http.StatusTooManyRequests, retryAfter: "30", wantRetry: true},
		{name: "server error", status: http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tt.retryAfter != "" {
					w.Header().Set("Retry-After", tt.retryAfter)
				}
				if tt.status == http.StatusFound {
					w.Header().Set("Location", "/elsewhere")
				}
				w.WriteHeader(tt.status)
			}))
			defer server.Close()

			executor := &onceTestExecutor{provider: "codex"}
			manager, hook := newOnceTestManager(t, executor, newOnceTestAuth("http-once"))
			auth := onceAuthByID(t, manager, "http-once")

			resp, facts, errDo := httpOnceWithAuth(manager, context.Background(), auth, onceHTTPRequest(t, context.Background(), http.MethodPost, server.URL, strings.NewReader(`{}`)), HTTPRedirectDeny)
			if code := onceErrorCode(errDo); code != tt.wantErrCode {
				t.Fatalf("httpOnceWithAuth() error = %v, want code %q", errDo, tt.wantErrCode)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != tt.status {
				t.Fatalf("response status = %d, want %d", resp.StatusCode, tt.status)
			}
			if facts.StatusCode != tt.status {
				t.Fatalf("facts.StatusCode = %d, want %d", facts.StatusCode, tt.status)
			}
			if facts.RequestCount != 1 {
				t.Fatalf("facts.RequestCount = %d, want 1", facts.RequestCount)
			}
			if got := executor.prepareCalls.Load(); got != 1 {
				t.Fatalf("credential injections = %d, want 1", got)
			}
			if got := executor.httpCalls.Load(); got != 0 {
				t.Fatalf("executor HttpRequest calls = %d, want 0", got)
			}
			results := hook.snapshot()
			if len(results) != 1 {
				t.Fatalf("MarkResult calls = %d, want 1", len(results))
			}
			if results[0].Success != tt.wantSuccess {
				t.Fatalf("Result.Success = %v, want %v", results[0].Success, tt.wantSuccess)
			}
			if tt.wantRetry && results[0].RetryAfter == nil {
				t.Fatal("Result.RetryAfter = nil, want a parsed Retry-After hint")
			}
		})
	}
}

func TestManagerHTTPOnceCore_DenyNeverFollowsRedirect(t *testing.T) {
	tests := []struct {
		name   string
		status int
		method string
	}{
		{name: "moved permanently", status: http.StatusMovedPermanently, method: http.MethodGet},
		{name: "found", status: http.StatusFound, method: http.MethodGet},
		{name: "see other", status: http.StatusSeeOther, method: http.MethodPost},
		{name: "temporary redirect", status: http.StatusTemporaryRedirect, method: http.MethodPost},
		{name: "permanent redirect", status: http.StatusPermanentRedirect, method: http.MethodPost},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var followed atomic.Int32
			mux := http.NewServeMux()
			mux.HandleFunc("/start", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Location", "/followed")
				w.WriteHeader(tt.status)
			})
			mux.HandleFunc("/followed", func(w http.ResponseWriter, _ *http.Request) {
				followed.Add(1)
				w.WriteHeader(http.StatusOK)
			})
			server := httptest.NewServer(mux)
			defer server.Close()

			executor := &onceTestExecutor{provider: "codex"}
			manager, hook := newOnceTestManager(t, executor, newOnceTestAuth("redirect-deny"))
			auth := onceAuthByID(t, manager, "redirect-deny")

			var body io.Reader
			if tt.method != http.MethodGet {
				body = strings.NewReader(`{"paid":true}`)
			}
			resp, facts, errDo := httpOnceWithAuth(manager, context.Background(), auth, onceHTTPRequest(t, context.Background(), tt.method, server.URL+"/start", body), HTTPRedirectDeny)
			if code := onceErrorCode(errDo); code != "redirect_denied" {
				t.Fatalf("httpOnceWithAuth() error = %v, want redirect_denied", errDo)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != tt.status {
				t.Fatalf("response status = %d, want the unfollowed %d", resp.StatusCode, tt.status)
			}
			if got := followed.Load(); got != 0 {
				t.Fatalf("redirect target hits = %d, want 0", got)
			}
			if facts.RequestCount != 1 {
				t.Fatalf("facts.RequestCount = %d, want 1 for a paid request", facts.RequestCount)
			}
			results := hook.snapshot()
			if len(results) != 1 || results[0].Success {
				t.Fatalf("results = %+v, want exactly one unsuccessful result", results)
			}
			// Refusing a redirect is the policy working, not the credential
			// failing, so it must never cool the credential.
			stored := onceAuthByID(t, manager, "redirect-deny")
			if state := stored.ModelStates["test-model"]; state != nil && state.Unavailable {
				t.Fatalf("model state = %+v, want a policy refusal to leave the credential untouched", state)
			}
		})
	}
}

func TestManagerHTTPOnceCore_EmptyPolicyDefaultsToDeny(t *testing.T) {
	var followed atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "/followed")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/followed", func(w http.ResponseWriter, _ *http.Request) {
		followed.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	manager, _ := newOnceTestManager(t, &onceTestExecutor{provider: "codex"}, newOnceTestAuth("redirect-default"))
	auth := onceAuthByID(t, manager, "redirect-default")

	resp, facts, errDo := httpOnceWithAuth(manager, context.Background(), auth, onceHTTPRequest(t, context.Background(), http.MethodGet, server.URL+"/start", nil), "")
	if code := onceErrorCode(errDo); code != "redirect_denied" {
		t.Fatalf("httpOnceWithAuth() error = %v, want redirect_denied", errDo)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("response status = %d, want %d", resp.StatusCode, http.StatusFound)
	}
	if got := followed.Load(); got != 0 {
		t.Fatalf("redirect target hits = %d, want 0", got)
	}
	if facts.RequestCount != 1 {
		t.Fatalf("facts.RequestCount = %d, want 1", facts.RequestCount)
	}
}

func TestManagerHTTPOnceCore_InvalidRedirectPolicyIsRejected(t *testing.T) {
	manager, hook := newOnceTestManager(t, &onceTestExecutor{provider: "codex"}, newOnceTestAuth("bad-policy"))
	auth := onceAuthByID(t, manager, "bad-policy")

	_, facts, errDo := httpOnceWithAuth(manager, context.Background(), auth, onceHTTPRequest(t, context.Background(), http.MethodGet, "https://example.invalid/", nil), HTTPRedirectPolicy("follow-everything"))
	var authErr *Error
	if !errors.As(errDo, &authErr) || authErr == nil || authErr.Code != "invalid_redirect_policy" {
		t.Fatalf("httpOnceWithAuth() error = %v, want invalid_redirect_policy", errDo)
	}
	if facts.RequestWritten || facts.RequestCount != 0 {
		t.Fatalf("facts = %+v, want an undispatched attempt", facts)
	}
	if got := len(hook.snapshot()); got != 0 {
		t.Fatalf("MarkResult calls = %d, want 0", got)
	}
}

// newOnceTLSContext returns a context carrying the transport that trusts the
// supplied TLS test server, which is how the manager reaches an https origin in
// tests without weakening certificate verification.
func newOnceTLSContext(server *httptest.Server) context.Context {
	return context.WithValue(context.Background(), "cliproxy.roundtripper", server.Client().Transport)
}

func TestManagerHTTPOnceCore_SameOriginSafeFollowsOnlySafeRedirects(t *testing.T) {
	tests := []struct {
		name          string
		method        string
		withBody      bool
		location      func(server *httptest.Server) string
		wantStatus    int
		wantFollowed  int32
		wantRequests  uint32
		wantSucceeded bool
		wantErrCode   string
	}{
		{
			name:          "same origin get is followed",
			method:        http.MethodGet,
			location:      func(*httptest.Server) string { return "/followed" },
			wantStatus:    http.StatusOK,
			wantFollowed:  1,
			wantRequests:  2,
			wantSucceeded: true,
		},
		{
			name:          "same origin get with explicit zero-length body is followed",
			method:        http.MethodGet,
			withBody:      true,
			location:      func(*httptest.Server) string { return "/followed" },
			wantStatus:    http.StatusOK,
			wantFollowed:  1,
			wantRequests:  2,
			wantSucceeded: true,
		},
		{
			name:         "cross origin is refused",
			method:       http.MethodGet,
			location:     func(*httptest.Server) string { return "https://redirect-target.invalid/followed" },
			wantStatus:   http.StatusFound,
			wantFollowed: 0,
			wantRequests: 1,
			wantErrCode:  "redirect_denied",
		},
		{
			name:         "post is refused",
			method:       http.MethodPost,
			withBody:     true,
			location:     func(*httptest.Server) string { return "/followed" },
			wantStatus:   http.StatusTemporaryRedirect,
			wantFollowed: 0,
			wantRequests: 1,
			wantErrCode:  "redirect_denied",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var followed atomic.Int32
			var followedAuth atomic.Value
			followedAuth.Store("")
			mux := http.NewServeMux()
			var server *httptest.Server
			mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", tt.location(server))
				if r.Method == http.MethodGet {
					w.WriteHeader(http.StatusFound)
					return
				}
				w.WriteHeader(http.StatusTemporaryRedirect)
			})
			mux.HandleFunc("/followed", func(w http.ResponseWriter, r *http.Request) {
				followed.Add(1)
				followedAuth.Store(r.Header.Get("Authorization") + "|" + r.Header.Get("X-Api-Key"))
				w.WriteHeader(http.StatusOK)
			})
			server = httptest.NewTLSServer(mux)
			defer server.Close()

			manager, hook := newOnceTestManager(t, &onceTestExecutor{provider: "codex"}, newOnceTestAuth("same-origin"))
			auth := onceAuthByID(t, manager, "same-origin")

			ctx := newOnceTLSContext(server)
			var body io.Reader
			if tt.withBody {
				if strings.Contains(tt.name, "zero-length") {
					body = strings.NewReader("")
				} else {
					body = strings.NewReader(`{"paid":true}`)
				}
			}
			resp, facts, errDo := httpOnceWithAuth(manager, ctx, auth, onceHTTPRequest(t, ctx, tt.method, server.URL+"/start", body), HTTPRedirectSameOriginSafe)
			if code := onceErrorCode(errDo); code != tt.wantErrCode {
				t.Fatalf("httpOnceWithAuth() error = %v, want code %q", errDo, tt.wantErrCode)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("response status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			if got := followed.Load(); got != tt.wantFollowed {
				t.Fatalf("redirect target hits = %d, want %d", got, tt.wantFollowed)
			}
			if facts.RequestCount != tt.wantRequests {
				t.Fatalf("facts.RequestCount = %d, want %d", facts.RequestCount, tt.wantRequests)
			}
			if tt.wantFollowed > 0 {
				if got, _ := followedAuth.Load().(string); got != "|" {
					t.Fatalf("credential headers on the followed hop = %q, want them stripped", got)
				}
			}
			results := hook.snapshot()
			if len(results) != 1 {
				t.Fatalf("MarkResult calls = %d, want 1", len(results))
			}
			if results[0].Success != tt.wantSucceeded {
				t.Fatalf("Result.Success = %v, want %v", results[0].Success, tt.wantSucceeded)
			}
		})
	}
}

func TestManagerHTTPOnceCore_SameOriginSafeStopsAtThreeHops(t *testing.T) {
	var final atomic.Int32
	mux := http.NewServeMux()
	for _, hop := range []struct{ from, to string }{
		{"/hop1", "/hop2"},
		{"/hop2", "/hop3"},
		{"/hop3", "/hop4"},
		{"/hop4", "/final"},
	} {
		target := hop.to
		mux.HandleFunc(hop.from, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Location", target)
			w.WriteHeader(http.StatusFound)
		})
	}
	mux.HandleFunc("/final", func(w http.ResponseWriter, _ *http.Request) {
		final.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	server := httptest.NewTLSServer(mux)
	defer server.Close()

	manager, _ := newOnceTestManager(t, &onceTestExecutor{provider: "codex"}, newOnceTestAuth("hops"))
	auth := onceAuthByID(t, manager, "hops")

	ctx := newOnceTLSContext(server)
	resp, facts, errDo := httpOnceWithAuth(manager, ctx, auth, onceHTTPRequest(t, ctx, http.MethodGet, server.URL+"/hop1", nil), HTTPRedirectSameOriginSafe)
	if code := onceErrorCode(errDo); code != "redirect_denied" {
		t.Fatalf("httpOnceWithAuth() error = %v, want redirect_denied", errDo)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("response status = %d, want the unfollowed %d", resp.StatusCode, http.StatusFound)
	}
	if got := final.Load(); got != 0 {
		t.Fatalf("final target hits = %d, want 0", got)
	}
	if facts.RequestCount != uint32(maxSameOriginSafeRedirectHops+1) {
		t.Fatalf("facts.RequestCount = %d, want %d", facts.RequestCount, maxSameOriginSafeRedirectHops+1)
	}
}

func TestManagerHTTPOnceCore_AttemptFacts(t *testing.T) {
	t.Run("clean response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("done"))
		}))
		defer server.Close()

		manager, _ := newOnceTestManager(t, &onceTestExecutor{provider: "codex"}, newOnceTestAuth("facts-clean"))
		auth := onceAuthByID(t, manager, "facts-clean")

		resp, facts, err := httpOnceWithAuth(manager, context.Background(), auth, onceHTTPRequest(t, context.Background(), http.MethodGet, server.URL, nil), HTTPRedirectDeny)
		if err != nil {
			t.Fatalf("httpOnceWithAuth() error = %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if !facts.RequestWritten || !facts.ResponseStarted || facts.StatusCode != http.StatusOK || facts.RequestCount != 1 {
			t.Fatalf("facts = %+v, want a written request with a started 200 response", facts)
		}
	})

	t.Run("connection refused", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		target := server.URL
		server.Close()

		manager, hook := newOnceTestManager(t, &onceTestExecutor{provider: "codex"}, newOnceTestAuth("facts-refused"))
		auth := onceAuthByID(t, manager, "facts-refused")

		resp, facts, err := httpOnceWithAuth(manager, context.Background(), auth, onceHTTPRequest(t, context.Background(), http.MethodPost, target, strings.NewReader(`{}`)), HTTPRedirectDeny)
		if err == nil {
			if resp != nil {
				_ = resp.Body.Close()
			}
			t.Fatal("httpOnceWithAuth() error = nil, want a transport failure")
		}
		if facts.RequestWritten {
			t.Fatalf("facts.RequestWritten = true, want false when the connection was refused")
		}
		if facts.ResponseStarted {
			t.Fatalf("facts.ResponseStarted = true, want false when the connection was refused")
		}
		results := hook.snapshot()
		if len(results) != 1 || results[0].Success {
			t.Fatalf("results = %+v, want exactly one unsuccessful result", results)
		}
	})

	t.Run("truncated body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Errorf("response writer is not a hijacker")
				return
			}
			conn, buf, errHijack := hijacker.Hijack()
			if errHijack != nil {
				t.Errorf("hijack: %v", errHijack)
				return
			}
			_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 128\r\n\r\npartial")
			_ = buf.Flush()
			_ = conn.Close()
		}))
		defer server.Close()

		manager, _ := newOnceTestManager(t, &onceTestExecutor{provider: "codex"}, newOnceTestAuth("facts-truncated"))
		auth := onceAuthByID(t, manager, "facts-truncated")

		resp, facts, err := httpOnceWithAuth(manager, context.Background(), auth, onceHTTPRequest(t, context.Background(), http.MethodGet, server.URL, nil), HTTPRedirectDeny)
		if err != nil {
			t.Fatalf("httpOnceWithAuth() error = %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if _, errRead := io.ReadAll(resp.Body); errRead == nil {
			t.Fatal("body read error = nil, want a truncated body failure")
		}
		if !facts.RequestWritten || !facts.ResponseStarted {
			t.Fatalf("facts = %+v, want a written request with a started response", facts)
		}
		if facts.StatusCode != http.StatusOK {
			t.Fatalf("facts.StatusCode = %d, want %d", facts.StatusCode, http.StatusOK)
		}
	})

	t.Run("opaque transport reports ambiguity as written", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", onceRoundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("opaque transport failure")
		}))

		manager, _ := newOnceTestManager(t, &onceTestExecutor{provider: "codex"}, newOnceTestAuth("facts-opaque"))
		auth := onceAuthByID(t, manager, "facts-opaque")

		_, facts, err := httpOnceWithAuth(manager, ctx, auth, onceHTTPRequest(t, ctx, http.MethodPost, "https://example.invalid/create", strings.NewReader(`{}`)), HTTPRedirectDeny)
		if err == nil {
			t.Fatal("httpOnceWithAuth() error = nil, want the transport failure")
		}
		if !facts.RequestWritten {
			t.Fatal("facts.RequestWritten = false, want true when the transport reports nothing")
		}
		if facts.ResponseStarted {
			t.Fatal("facts.ResponseStarted = true, want false")
		}
	})
}

func TestManagerDoHTTPOnce_ResolvesDurableAuthAndPreparesBeforeDispatch(t *testing.T) {
	var seenKey atomic.Value
	seenKey.Store("")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenKey.Store(r.Header.Get("X-Api-Key"))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	executor := &onceTestExecutor{provider: "codex"}
	manager, hook := newOnceTestManager(t, executor, newOnceTestAuth("http-once-id"))

	resp, facts, err := manager.DoHTTPOnce(context.Background(), HTTPOnceRequest{
		AuthID:         "http-once-id",
		Model:          "test-model",
		Method:         http.MethodPost,
		URL:            server.URL,
		Header:         http.Header{"Content-Type": {"application/json"}},
		Body:           []byte(`{"paid":true}`),
		RedirectPolicy: HTTPRedirectDeny,
	})
	if err != nil {
		t.Fatalf("DoHTTPOnce() error = %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if got, _ := seenKey.Load().(string); got != "secret-key" {
		t.Fatalf("injected api key = %q, want %q", got, "secret-key")
	}
	if facts.RequestCount != 1 {
		t.Fatalf("facts.RequestCount = %d, want 1", facts.RequestCount)
	}
	results := hook.snapshot()
	if len(results) != 1 || !results[0].Success || results[0].Model != "test-model" {
		t.Fatalf("results = %+v, want one successful result for test-model", results)
	}
}

func TestManagerDoHTTPOnce_PreflightFailuresNeverDispatch(t *testing.T) {
	tests := []struct {
		name     string
		request  HTTPOnceRequest
		setup    func(manager *Manager)
		wantCode string
	}{
		{name: "unknown auth", request: HTTPOnceRequest{AuthID: "missing", URL: "https://example.invalid/"}, wantCode: "auth_not_found"},
		{name: "empty url", request: HTTPOnceRequest{AuthID: "http-once-preflight"}, wantCode: "invalid_request"},
		{name: "invalid policy", request: HTTPOnceRequest{AuthID: "http-once-preflight", URL: "https://example.invalid/", RedirectPolicy: "always"}, wantCode: "invalid_redirect_policy"},
		{
			name:    "home mode",
			request: HTTPOnceRequest{AuthID: "http-once-preflight", URL: "https://example.invalid/"},
			setup: func(manager *Manager) {
				manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
			},
			wantCode: "auth_not_durable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executor := &onceTestExecutor{provider: "codex"}
			manager, hook := newOnceTestManager(t, executor, newOnceTestAuth("http-once-preflight"))
			if tt.setup != nil {
				tt.setup(manager)
			}

			_, facts, err := manager.DoHTTPOnce(context.Background(), tt.request)
			var authErr *Error
			if !errors.As(err, &authErr) || authErr == nil {
				t.Fatalf("DoHTTPOnce() error = %v, want *Error", err)
			}
			if authErr.Code != tt.wantCode {
				t.Fatalf("error code = %q, want %q", authErr.Code, tt.wantCode)
			}
			if facts.RequestWritten || facts.RequestCount != 0 {
				t.Fatalf("facts = %+v, want an undispatched attempt", facts)
			}
			if got := executor.prepareCalls.Load(); got != 0 {
				t.Fatalf("credential injections = %d, want 0", got)
			}
			if got := len(hook.snapshot()); got != 0 {
				t.Fatalf("MarkResult calls = %d, want 0", got)
			}
		})
	}
}

func TestStripSensitiveRedirectHeaders(t *testing.T) {
	tests := []struct {
		name   string
		header http.Header
		want   []string
	}{
		{
			name:   "standard credential headers",
			header: http.Header{"Authorization": {"Bearer x"}, "Cookie": {"a=b"}, "Proxy-Authorization": {"Basic x"}, "Accept": {"application/json"}},
			want:   []string{"Accept"},
		},
		{
			name:   "provider key headers",
			header: http.Header{"X-Api-Key": {"k"}, "Api-Key": {"k"}, "X-Goog-Api-Key": {"k"}, "Content-Type": {"application/json"}},
			want:   []string{"Content-Type"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stripSensitiveRedirectHeaders(tt.header)
			if len(tt.header) != len(tt.want) {
				t.Fatalf("remaining headers = %v, want %v", tt.header, tt.want)
			}
			for _, name := range tt.want {
				if tt.header.Get(name) == "" {
					t.Fatalf("header %q was stripped, want it kept", name)
				}
			}
		})
	}
}

func TestSameOriginSafeRedirectAllowed(t *testing.T) {
	origin := func(method, raw string, hasBody bool) *redirectOrigin {
		req := onceHTTPRequest(t, context.Background(), method, raw, nil)
		snapshot := newRedirectOrigin(req)
		snapshot.hasBody = hasBody
		return snapshot
	}

	tests := []struct {
		name   string
		policy HTTPRedirectPolicy
		origin *redirectOrigin
		target string
		hop    int
		want   bool
	}{
		{name: "deny never follows", policy: HTTPRedirectDeny, origin: origin(http.MethodGet, "https://api.example.com/a", false), target: "https://api.example.com/b", hop: 1},
		{name: "same origin get", policy: HTTPRedirectSameOriginSafe, origin: origin(http.MethodGet, "https://api.example.com/a", false), target: "https://api.example.com/b", hop: 1, want: true},
		{name: "relative location", policy: HTTPRedirectSameOriginSafe, origin: origin(http.MethodHead, "https://api.example.com/a", false), target: "/b", hop: 1, want: true},
		{name: "cross host", policy: HTTPRedirectSameOriginSafe, origin: origin(http.MethodGet, "https://api.example.com/a", false), target: "https://cdn.example.com/b", hop: 1},
		{name: "downgrade to http", policy: HTTPRedirectSameOriginSafe, origin: origin(http.MethodGet, "https://api.example.com/a", false), target: "http://api.example.com/b", hop: 1},
		{name: "insecure origin", policy: HTTPRedirectSameOriginSafe, origin: origin(http.MethodGet, "http://api.example.com/a", false), target: "http://api.example.com/b", hop: 1},
		{name: "post", policy: HTTPRedirectSameOriginSafe, origin: origin(http.MethodPost, "https://api.example.com/a", false), target: "https://api.example.com/b", hop: 1},
		{name: "body bearing get", policy: HTTPRedirectSameOriginSafe, origin: origin(http.MethodGet, "https://api.example.com/a", true), target: "https://api.example.com/b", hop: 1},
		{name: "hop limit", policy: HTTPRedirectSameOriginSafe, origin: origin(http.MethodGet, "https://api.example.com/a", false), target: "https://api.example.com/b", hop: maxSameOriginSafeRedirectHops + 1},
		{name: "unknown policy", policy: HTTPRedirectPolicy("loose"), origin: origin(http.MethodGet, "https://api.example.com/a", false), target: "https://api.example.com/b", hop: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sameOriginSafeRedirectAllowed(tt.policy, tt.origin, tt.target, tt.hop); got != tt.want {
				t.Fatalf("sameOriginSafeRedirectAllowed() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOnceAttemptRecorderFacts(t *testing.T) {
	tests := []struct {
		name        string
		clientOwned bool
		prepare     func(recorder *onceAttemptRecorder)
		want        HTTPAttemptFacts
	}{
		{
			name:        "never dispatched",
			clientOwned: true,
			prepare:     func(*onceAttemptRecorder) {},
			want:        HTTPAttemptFacts{},
		},
		{
			name:        "dispatched without observation is ambiguous",
			clientOwned: true,
			prepare: func(recorder *onceAttemptRecorder) {
				recorder.markDispatched()
			},
			want: HTTPAttemptFacts{RequestWritten: true},
		},
		{
			name:        "owned client trace observed without write",
			clientOwned: true,
			prepare: func(recorder *onceAttemptRecorder) {
				recorder.markDispatched()
				recorder.clientTrace().ConnectStart("tcp", "host:443")
			},
			want: HTTPAttemptFacts{},
		},
		{
			name: "executor owned client never claims not written",
			prepare: func(recorder *onceAttemptRecorder) {
				recorder.markDispatched()
				recorder.clientTrace().ConnectStart("tcp", "host:443")
			},
			want: HTTPAttemptFacts{RequestWritten: true},
		},
		{
			name:        "trace observed with write",
			clientOwned: true,
			prepare: func(recorder *onceAttemptRecorder) {
				recorder.markDispatched()
				trace := recorder.clientTrace()
				trace.WroteHeaders()
				trace.GotFirstResponseByte()
			},
			want: HTTPAttemptFacts{RequestCount: 1, RequestWritten: true, RequestWrittenObserved: true, ResponseStarted: true},
		},
		{
			name:        "transport observed",
			clientOwned: true,
			prepare: func(recorder *onceAttemptRecorder) {
				recorder.markDispatched()
				recorder.observeRequest()
				recorder.observeResponse(http.StatusTooManyRequests)
			},
			want: HTTPAttemptFacts{RequestCount: 1, RequestWritten: true, RequestWrittenObserved: true, ResponseStarted: true, StatusCode: http.StatusTooManyRequests},
		},
		{
			name:        "transport resend below the wrapper is counted from the trace",
			clientOwned: true,
			prepare: func(recorder *onceAttemptRecorder) {
				recorder.markDispatched()
				recorder.observeRequest()
				trace := recorder.clientTrace()
				trace.WroteHeaders()
				trace.WroteHeaders()
				recorder.observeResponse(http.StatusOK)
			},
			want: HTTPAttemptFacts{RequestCount: 2, RequestWritten: true, RequestWrittenObserved: true, ResponseStarted: true, StatusCode: http.StatusOK},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := newOnceAttemptRecorder(tt.clientOwned)
			tt.prepare(recorder)
			if got := recorder.facts(); got != tt.want {
				t.Fatalf("facts() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// oncePrepareAuthExecutor fails the request-scoped credential preparation seam
// before any HTTP request can be constructed or dispatched.
type oncePrepareAuthExecutor struct {
	*onceTestExecutor
	prepareAuthErr error
}

func (e *oncePrepareAuthExecutor) ShouldPrepareRequestAuth(*Auth) bool { return true }

func (e *oncePrepareAuthExecutor) PrepareRequestAuth(context.Context, *Auth) (*Auth, error) {
	return nil, e.prepareAuthErr
}

func TestManagerDoHTTPOnce_RequestAuthPreparationFailureIsRecordedWithoutDispatch(t *testing.T) {
	executor := &oncePrepareAuthExecutor{
		onceTestExecutor: &onceTestExecutor{provider: "codex"},
		prepareAuthErr:   &onceStatusError{status: http.StatusUnauthorized, message: "credential acquisition failed"},
	}
	hook := &onceResultHook{}
	manager := NewManager(nil, &RoundRobinSelector{}, hook)
	manager.RegisterExecutor(executor)
	if _, err := manager.Register(context.Background(), newOnceTestAuth("prepare-auth-failure")); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	resp, facts, errDo := manager.DoHTTPOnce(context.Background(), HTTPOnceRequest{
		AuthID: "prepare-auth-failure",
		Model:  "test-model",
		URL:    "https://example.invalid/never-dispatched",
	})
	if resp != nil {
		t.Fatalf("response = %#v, want nil", resp)
	}
	if code := onceErrorCode(errDo); code != "credential acquisition failed" {
		t.Fatalf("DoHTTPOnce() error = %v, want preparation failure", errDo)
	}
	if facts.RequestCount != 0 || facts.RequestWritten || facts.RequestWrittenObserved || facts.ResponseStarted {
		t.Fatalf("facts = %+v, want undispatched attempt", facts)
	}
	if got := executor.prepareCalls.Load(); got != 0 {
		t.Fatalf("HTTP credential injections = %d, want 0", got)
	}
	results := hook.snapshot()
	if len(results) != 1 {
		t.Fatalf("recorded results = %d, want 1", len(results))
	}
	if results[0].Success || results[0].AuthID != "prepare-auth-failure" || results[0].Provider != "codex" || results[0].Model != "test-model" {
		t.Fatalf("recorded result = %+v, want the pinned credential preparation failure", results[0])
	}
}

func TestManagerHTTPOnceCore_FollowedUnauthenticatedRedirectIsAvailabilityNeutral(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "/protected")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/protected", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization") + "|" + r.Header.Get("X-Api-Key"); got != "|" {
			t.Errorf("credential headers on followed hop = %q, want stripped", got)
		}
		w.WriteHeader(http.StatusUnauthorized)
	})
	server := httptest.NewTLSServer(mux)
	defer server.Close()

	manager, hook := newOnceTestManager(t, &onceTestExecutor{provider: "codex"}, newOnceTestAuth("redirect-neutral"))
	auth := onceAuthByID(t, manager, "redirect-neutral")
	ctx := newOnceTLSContext(server)
	resp, facts, errDo := httpOnceWithAuth(manager, ctx, auth, onceHTTPRequest(t, ctx, http.MethodGet, server.URL+"/start", nil), HTTPRedirectSameOriginSafe)
	if errDo != nil {
		t.Fatalf("httpOnceWithAuth() error = %v", errDo)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized || facts.RequestCount != 2 {
		t.Fatalf("response status/facts = %d/%+v, want 401 after two requests", resp.StatusCode, facts)
	}
	results := hook.snapshot()
	if len(results) != 1 || results[0].Success {
		t.Fatalf("recorded results = %+v, want one observable failure", results)
	}
	stored := onceAuthByID(t, manager, "redirect-neutral")
	if state := stored.ModelStates["test-model"]; state != nil && state.Unavailable {
		t.Fatalf("model state = %+v, want stripped redirect-hop failure to remain availability-neutral", state)
	}
}
