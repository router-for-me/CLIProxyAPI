package auth

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/MoeForge/moego/json"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type issue7749SameIDHomeDispatcher struct {
	homeAuth *Auth
}

func (d *issue7749SameIDHomeDispatcher) RPopAuth(_ context.Context, _ string, _ string, _ http.Header, _ int) ([]byte, error) {
	return json.Marshal(homeAuthDispatchResponse{Auth: *d.homeAuth.Clone()})
}

func (d *issue7749SameIDHomeDispatcher) HeartbeatOK() bool { return true }

func (d *issue7749SameIDHomeDispatcher) AbortAmbiguousDispatch() {}

type issue7749HomeRefreshExecutor struct {
	mu           sync.Mutex
	seenTokens   []string
	refreshCalls atomic.Int32
}

func (e *issue7749HomeRefreshExecutor) Identifier() string { return "antigravity" }

func (e *issue7749HomeRefreshExecutor) refreshIfExpiring(ctx context.Context, auth *Auth) (*Auth, error) {
	updated, err := RefreshExecutionAuth(ctx, auth, func(_ context.Context, latest *Auth) (*Auth, error) {
		if latest != nil && testStringValue(latest.Metadata["access_token"]) == "local-valid-token" {
			// Local token looks fresh; returning (nil, nil) would adopt the local credential if Home leaked into m.auths.
			return nil, nil
		}
		e.refreshCalls.Add(1)
		out := latest.Clone()
		if out.Metadata == nil {
			out.Metadata = map[string]any{}
		}
		out.Metadata["access_token"] = "home-refreshed-token"
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	if updated != nil {
		return updated, nil
	}
	return auth, nil
}

func (e *issue7749HomeRefreshExecutor) recordToken(auth *Auth) {
	e.mu.Lock()
	e.seenTokens = append(e.seenTokens, testStringValue(auth.Metadata["access_token"]))
	e.mu.Unlock()
}

func (e *issue7749HomeRefreshExecutor) Execute(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	effective, err := e.refreshIfExpiring(ctx, auth)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	e.recordToken(effective)
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (e *issue7749HomeRefreshExecutor) ExecuteStream(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	effective, err := e.refreshIfExpiring(ctx, auth)
	if err != nil {
		return nil, err
	}
	e.recordToken(effective)
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte("ok")}
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}

func (e *issue7749HomeRefreshExecutor) CountTokens(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	effective, err := e.refreshIfExpiring(ctx, auth)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	e.recordToken(effective)
	return cliproxyexecutor.Response{Payload: []byte("1")}, nil
}

func (e *issue7749HomeRefreshExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *issue7749HomeRefreshExecutor) HttpRequest(_ context.Context, _ *Auth, _ *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestIssue7749HomeExecutionRefreshDoesNotUseSameIDLocalAuth(t *testing.T) {
	paths := []struct {
		name string
		run  func(*Manager, context.Context) error
	}{
		{
			name: "Execute",
			run: func(m *Manager, ctx context.Context) error {
				_, err := m.Execute(ctx, []string{"antigravity"}, cliproxyexecutor.Request{Model: "test-model"}, cliproxyexecutor.Options{})
				return err
			},
		},
		{
			name: "CountTokens",
			run: func(m *Manager, ctx context.Context) error {
				_, err := m.ExecuteCount(ctx, []string{"antigravity"}, cliproxyexecutor.Request{Model: "test-model"}, cliproxyexecutor.Options{})
				return err
			},
		},
		{
			name: "ExecuteStream",
			run: func(m *Manager, ctx context.Context) error {
				res, err := m.ExecuteStream(ctx, []string{"antigravity"}, cliproxyexecutor.Request{Model: "test-model"}, cliproxyexecutor.Options{Stream: true})
				if err != nil {
					return err
				}
				if res != nil && res.Chunks != nil {
					for range res.Chunks {
					}
				}
				return nil
			},
		},
	}

	for _, tc := range paths {
		t.Run(tc.name, func(t *testing.T) {
			exec := &issue7749HomeRefreshExecutor{}
			homeAuth := &Auth{
				ID:       "same-id-7749",
				Provider: "antigravity",
				Status:   StatusActive,
				Metadata: map[string]any{"access_token": "home-expiring-token", "source": "home"},
			}
			manager := NewManager(&requestPrepareStore{}, nil, nil)
			manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
			manager.PublishHomeDispatch(&issue7749SameIDHomeDispatcher{homeAuth: homeAuth}, executionregistry.New(), 1)
			manager.RegisterExecutor(exec)

			localAuth := &Auth{
				ID:       "same-id-7749",
				Provider: "antigravity",
				Status:   StatusActive,
				Metadata: map[string]any{"access_token": "local-valid-token", "source": "local"},
			}
			if _, errReg := manager.Register(WithSkipPersist(context.Background()), localAuth); errReg != nil {
				t.Fatalf("Register local auth: %v", errReg)
			}

			if errRun := tc.run(manager, context.Background()); errRun != nil {
				t.Fatalf("%s error = %v", tc.name, errRun)
			}

			exec.mu.Lock()
			tokens := append([]string(nil), exec.seenTokens...)
			exec.mu.Unlock()
			if len(tokens) != 1 || tokens[0] != "home-refreshed-token" {
				t.Fatalf("%s used tokens %v, want [home-refreshed-token]", tc.name, tokens)
			}
			if exec.refreshCalls.Load() != 1 {
				t.Fatalf("%s Home refresh calls = %d, want 1", tc.name, exec.refreshCalls.Load())
			}

			current, ok := manager.GetByID("same-id-7749")
			if !ok || current == nil {
				t.Fatal("local auth disappeared")
			}
			if got := testStringValue(current.Metadata["access_token"]); got != "local-valid-token" {
				t.Fatalf("local auth access_token = %q, want unchanged local-valid-token", got)
			}
		})
	}
}

type issue7749PrepAndHTTPExecutor struct {
	id          string
	hostToSet   string
	seenHTTPCtx context.Context
	seenReqCtx  context.Context
	prepareFn   func(req *http.Request, auth *Auth) error
}

func (e *issue7749PrepAndHTTPExecutor) Identifier() string { return e.id }
func (e *issue7749PrepAndHTTPExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e *issue7749PrepAndHTTPExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}
func (e *issue7749PrepAndHTTPExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e *issue7749PrepAndHTTPExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}
func (e *issue7749PrepAndHTTPExecutor) PrepareRequest(req *http.Request, auth *Auth) error {
	if e.prepareFn != nil {
		return e.prepareFn(req, auth)
	}
	if e.hostToSet != "" {
		req.Host = e.hostToSet
	}
	req.Header.Set("Authorization", "Bearer prepared-tok")
	return nil
}
func (e *issue7749PrepAndHTTPExecutor) HttpRequest(ctx context.Context, _ *Auth, req *http.Request) (*http.Response, error) {
	e.seenHTTPCtx = ctx
	e.seenReqCtx = req.Context()
	if ctx != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return &http.Response{StatusCode: http.StatusOK}, nil
}

func TestIssue7749InjectCredentialsConcurrentWithMarkResultNoRace(t *testing.T) {
	exec := &issue7749PrepAndHTTPExecutor{id: "issue7749-race"}
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(exec)

	auth := &Auth{
		ID:       "cred-race-7749",
		Provider: "issue7749-race",
		Status:   StatusActive,
		Metadata: map[string]any{"access_token": "tok-1"},
	}
	if _, errReg := manager.Register(context.Background(), auth); errReg != nil {
		t.Fatalf("Register auth: %v", errReg)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.test/api", nil)
			_ = manager.InjectCredentials(req, "cred-race-7749")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			manager.MarkResult(context.Background(), Result{
				AuthID:   "cred-race-7749",
				Provider: "issue7749-race",
				Model:    "model-" + string(rune('a'+(i%10))),
				Success:  i%2 == 0,
			})
		}
	}()
	wg.Wait()
}

func TestIssue7749ManagerHttpRequestNilContextPreservesRequestContext(t *testing.T) {
	type reqCtxKey struct{}

	exec := &issue7749PrepAndHTTPExecutor{id: "issue7749-httpctx"}
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(exec)

	auth := &Auth{
		ID:       "cred-httpctx-7749",
		Provider: "issue7749-httpctx",
		Status:   StatusActive,
	}

	reqCtx, cancel := context.WithCancel(context.WithValue(context.Background(), reqCtxKey{}, "req-marker-7749"))
	cancel()
	req, _ := http.NewRequestWithContext(reqCtx, http.MethodGet, "https://example.test/api", nil)

	_, errHTTP := manager.HttpRequest(nil, auth, req)
	if !errors.Is(errHTTP, context.Canceled) {
		t.Fatalf("Manager.HttpRequest(nil, auth, canceledReq) error = %v, want context.Canceled", errHTTP)
	}
	if exec.seenHTTPCtx == nil || exec.seenHTTPCtx.Value(reqCtxKey{}) != "req-marker-7749" {
		t.Fatalf("executor HttpRequest ctx value = %v, want req-marker-7749 from req.Context()", exec.seenHTTPCtx)
	}
}

func TestIssue7749PrepareRequestPreservesHostMutation(t *testing.T) {
	type origKey struct{}
	type preparerKey struct{}

	exec := &issue7749PrepAndHTTPExecutor{
		id:        "issue7749-host",
		hostToSet: "custom-host.example.test",
		prepareFn: func(req *http.Request, _ *Auth) error {
			req.Host = "custom-host.example.test"
			req.Header.Set("Authorization", "Bearer prepared-tok")
			*req = *req.WithContext(context.WithValue(req.Context(), preparerKey{}, "from-preparer"))
			return nil
		},
	}
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(exec)

	auth := &Auth{
		ID:       "cred-host-7749",
		Provider: "issue7749-host",
		Status:   StatusActive,
	}
	registered, errReg := manager.Register(context.Background(), auth)
	if errReg != nil {
		t.Fatalf("Register auth: %v", errReg)
	}

	t.Run("InjectCredentials", func(t *testing.T) {
		origCtx := context.WithValue(context.Background(), origKey{}, "keep-7749")
		req, _ := http.NewRequestWithContext(origCtx, http.MethodGet, "https://original.example.test/v1", nil)
		if errInj := manager.InjectCredentials(req, registered.ID); errInj != nil {
			t.Fatalf("InjectCredentials error = %v", errInj)
		}
		if req.Host != "custom-host.example.test" {
			t.Fatalf("req.Host = %q, want custom-host.example.test", req.Host)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer prepared-tok" {
			t.Fatalf("Authorization = %q, want Bearer prepared-tok", got)
		}
		if req.Context().Value(origKey{}) != "keep-7749" {
			t.Fatal("InjectCredentials lost original req.Context value")
		}
		if got := req.Context().Value(preparerKey{}); got != "from-preparer" {
			t.Fatalf("InjectCredentials dropped preparer context value: got %v, want from-preparer", got)
		}
		if fn, ok := req.Context().Value(executionAuthRefreshContextKey{}).(executionAuthRefreshHandler); ok && fn != nil {
			t.Fatal("InjectCredentials leaked executionAuthRefreshContextKey onto request context")
		}
	})

	t.Run("PrepareHttpRequest", func(t *testing.T) {
		origCtx := context.WithValue(context.Background(), origKey{}, "keep-prep-7749")
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://original.example.test/v1", nil)
		if errPrep := manager.PrepareHttpRequest(origCtx, registered.Clone(), req); errPrep != nil {
			t.Fatalf("PrepareHttpRequest error = %v", errPrep)
		}
		if req.Host != "custom-host.example.test" {
			t.Fatalf("req.Host = %q, want custom-host.example.test", req.Host)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer prepared-tok" {
			t.Fatalf("Authorization = %q, want Bearer prepared-tok", got)
		}
		if req.Context().Value(origKey{}) != "keep-prep-7749" {
			t.Fatal("PrepareHttpRequest lost caller ctx value")
		}
		if got := req.Context().Value(preparerKey{}); got != "from-preparer" {
			t.Fatalf("PrepareHttpRequest dropped preparer context value: got %v, want from-preparer", got)
		}
		if fn, ok := req.Context().Value(executionAuthRefreshContextKey{}).(executionAuthRefreshHandler); ok && fn != nil {
			t.Fatal("PrepareHttpRequest leaked executionAuthRefreshContextKey onto request context")
		}
	})
}
