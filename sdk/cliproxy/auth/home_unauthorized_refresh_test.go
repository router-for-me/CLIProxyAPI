package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

const homeUnauthorizedRefreshProvider = "home-unauthorized-refresh"

type homeUnauthorizedRefreshDispatcher struct {
	calls atomic.Int32
}

func (*homeUnauthorizedRefreshDispatcher) HeartbeatOK() bool { return true }

func (d *homeUnauthorizedRefreshDispatcher) RPopAuth(context.Context, string, string, http.Header, int) ([]byte, error) {
	d.calls.Add(1)
	return json.Marshal(homeAuthDispatchResponse{Auth: Auth{
		ID:       "home-refresh-auth",
		Provider: homeUnauthorizedRefreshProvider,
		Status:   StatusActive,
		Attributes: map[string]string{
			AttributeAuthKind: AuthKindOAuth,
			"websockets":      "true",
		},
		Metadata: map[string]any{
			"access_token": "stale-access-token",
		},
	}})
}

func (*homeUnauthorizedRefreshDispatcher) AbortAmbiguousDispatch() {}

type homeUnauthorizedRefreshExecutor struct {
	streamMode      string
	refreshErr      error
	retainSelection bool
	executeCalls    atomic.Int32
	countCalls      atomic.Int32
	streamCalls     atomic.Int32
	refreshCalls    atomic.Int32
}

func (*homeUnauthorizedRefreshExecutor) Identifier() string { return homeUnauthorizedRefreshProvider }

func (e *homeUnauthorizedRefreshExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.executeCalls.Add(1)
	if e.retainSelection {
		if lifecycle, ok := opts.ExecutionLifecycle.(interface{ Retain() }); ok {
			lifecycle.Retain()
		}
	}
	if authAccessToken(auth) == "stale-access-token" {
		return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusUnauthorized, Message: "expired access token"}
	}
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (e *homeUnauthorizedRefreshExecutor) ExecuteStream(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.streamCalls.Add(1)
	if authAccessToken(auth) == "stale-access-token" {
		switch e.streamMode {
		case "bootstrap":
			chunks := make(chan cliproxyexecutor.StreamChunk, 1)
			chunks <- cliproxyexecutor.StreamChunk{Err: &Error{HTTPStatus: http.StatusUnauthorized, Message: "expired access token"}}
			close(chunks)
			return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
		case "started":
			chunks := make(chan cliproxyexecutor.StreamChunk, 2)
			chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("started")}
			chunks <- cliproxyexecutor.StreamChunk{Err: &Error{HTTPStatus: http.StatusUnauthorized, Message: "expired access token"}}
			close(chunks)
			return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
		default:
			return nil, &Error{HTTPStatus: http.StatusUnauthorized, Message: "expired access token"}
		}
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("ok")}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *homeUnauthorizedRefreshExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	e.refreshCalls.Add(1)
	if e.refreshErr != nil {
		return nil, e.refreshErr
	}
	updated := auth.Clone()
	if updated.Metadata == nil {
		updated.Metadata = make(map[string]any)
	}
	updated.Metadata["access_token"] = "fresh-access-token"
	return updated, nil
}

func (e *homeUnauthorizedRefreshExecutor) CountTokens(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.countCalls.Add(1)
	if authAccessToken(auth) == "stale-access-token" {
		return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusUnauthorized, Message: "expired access token"}
	}
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (*homeUnauthorizedRefreshExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func newHomeUnauthorizedRefreshManager(dispatcher *homeUnauthorizedRefreshDispatcher, executor *homeUnauthorizedRefreshExecutor) *Manager {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
	manager.RegisterExecutor(executor)
	return manager
}

func TestHomeUnauthorizedRefreshesSameSelectionBeforeRedispatch(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*Manager) error
	}{
		{
			name: "execute",
			run: func(manager *Manager) error {
				_, errExecute := manager.Execute(context.Background(), []string{homeUnauthorizedRefreshProvider}, cliproxyexecutor.Request{Model: "model-a"}, cliproxyexecutor.Options{})
				return errExecute
			},
		},
		{
			name: "count_tokens",
			run: func(manager *Manager) error {
				_, errCount := manager.ExecuteCount(context.Background(), []string{homeUnauthorizedRefreshProvider}, cliproxyexecutor.Request{Model: "model-a"}, cliproxyexecutor.Options{})
				return errCount
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			dispatcher := &homeUnauthorizedRefreshDispatcher{}
			executor := &homeUnauthorizedRefreshExecutor{}
			manager := newHomeUnauthorizedRefreshManager(dispatcher, executor)

			if errRun := test.run(manager); errRun != nil {
				t.Fatalf("execution error = %v", errRun)
			}
			if got := dispatcher.calls.Load(); got != 1 {
				t.Fatalf("Home dispatch calls = %d, want 1", got)
			}
			if got := executor.refreshCalls.Load(); got != 1 {
				t.Fatalf("refresh calls = %d, want 1", got)
			}
			if test.name == "execute" && executor.executeCalls.Load() != 2 {
				t.Fatalf("execute calls = %d, want 2", executor.executeCalls.Load())
			}
			if test.name == "count_tokens" && executor.countCalls.Load() != 2 {
				t.Fatalf("count calls = %d, want 2", executor.countCalls.Load())
			}
		})
	}
}

func TestHomeUnauthorizedRefreshUpdatesRetainedSelection(t *testing.T) {
	dispatcher := &homeUnauthorizedRefreshDispatcher{}
	executor := &homeUnauthorizedRefreshExecutor{retainSelection: true}
	manager := newHomeUnauthorizedRefreshManager(dispatcher, executor)
	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.ExecutionSessionMetadataKey: "refresh-session",
		cliproxyexecutor.PinnedAuthMetadataKey:       "home-refresh-auth",
	}}

	for range 2 {
		if _, errExecute := manager.Execute(ctx, []string{homeUnauthorizedRefreshProvider}, cliproxyexecutor.Request{Model: "model-a"}, opts); errExecute != nil {
			t.Fatalf("Execute() error = %v", errExecute)
		}
	}
	if got := dispatcher.calls.Load(); got != 1 {
		t.Fatalf("Home dispatch calls = %d, want one retained selection", got)
	}
	if got := executor.refreshCalls.Load(); got != 1 {
		t.Fatalf("refresh calls = %d, want refreshed token reused by retained selection", got)
	}
	if got := executor.executeCalls.Load(); got != 3 {
		t.Fatalf("execute calls = %d, want stale attempt, retry, and retained reuse", got)
	}
}

func TestHomeUnauthorizedTransientRefreshFailureIsReturned(t *testing.T) {
	dispatcher := &homeUnauthorizedRefreshDispatcher{}
	executor := &homeUnauthorizedRefreshExecutor{
		refreshErr: &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "Home refresh temporarily unavailable"},
	}
	manager := newHomeUnauthorizedRefreshManager(dispatcher, executor)

	_, errExecute := manager.Execute(context.Background(), []string{homeUnauthorizedRefreshProvider}, cliproxyexecutor.Request{Model: "model-a"}, cliproxyexecutor.Options{})
	if statusCodeFromError(errExecute) != http.StatusServiceUnavailable {
		t.Fatalf("Execute() error = %v, want transient 503", errExecute)
	}
	if got := executor.executeCalls.Load(); got != 1 {
		t.Fatalf("execute calls = %d, want 1", got)
	}
	if got := executor.refreshCalls.Load(); got != 1 {
		t.Fatalf("refresh calls = %d, want 1", got)
	}
}

func TestHomeUnauthorizedStartedStreamDoesNotReplay(t *testing.T) {
	dispatcher := &homeUnauthorizedRefreshDispatcher{}
	executor := &homeUnauthorizedRefreshExecutor{streamMode: "started"}
	manager := newHomeUnauthorizedRefreshManager(dispatcher, executor)

	result, errStream := manager.ExecuteStream(context.Background(), []string{homeUnauthorizedRefreshProvider}, cliproxyexecutor.Request{Model: "model-a"}, cliproxyexecutor.Options{Stream: true})
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	sawPayload := false
	sawUnauthorized := false
	for chunk := range result.Chunks {
		if string(chunk.Payload) == "started" {
			sawPayload = true
		}
		if statusCodeFromError(chunk.Err) == http.StatusUnauthorized {
			sawUnauthorized = true
		}
	}
	if !sawPayload || !sawUnauthorized {
		t.Fatalf("stream results = payload %v unauthorized %v, want both", sawPayload, sawUnauthorized)
	}
	if got := executor.refreshCalls.Load(); got != 0 {
		t.Fatalf("refresh calls = %d, want 0 after stream started", got)
	}
	if got := executor.streamCalls.Load(); got != 1 {
		t.Fatalf("stream calls = %d, want 1", got)
	}
}

func TestHomeUnauthorizedStreamRefreshesBeforeRedispatch(t *testing.T) {
	for _, mode := range []string{"synchronous", "bootstrap"} {
		t.Run(mode, func(t *testing.T) {
			dispatcher := &homeUnauthorizedRefreshDispatcher{}
			executor := &homeUnauthorizedRefreshExecutor{streamMode: mode}
			manager := newHomeUnauthorizedRefreshManager(dispatcher, executor)

			result, errStream := manager.ExecuteStream(context.Background(), []string{homeUnauthorizedRefreshProvider}, cliproxyexecutor.Request{Model: "model-a"}, cliproxyexecutor.Options{Stream: true})
			if errStream != nil {
				t.Fatalf("ExecuteStream() error = %v", errStream)
			}
			var payload string
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					t.Fatalf("stream chunk error = %v", chunk.Err)
				}
				payload += string(chunk.Payload)
			}
			if payload != "ok" {
				t.Fatalf("stream payload = %q, want ok", payload)
			}
			if got := dispatcher.calls.Load(); got != 1 {
				t.Fatalf("Home dispatch calls = %d, want 1", got)
			}
			if got := executor.refreshCalls.Load(); got != 1 {
				t.Fatalf("refresh calls = %d, want 1", got)
			}
			if got := executor.streamCalls.Load(); got != 2 {
				t.Fatalf("stream calls = %d, want 2", got)
			}
		})
	}
}
