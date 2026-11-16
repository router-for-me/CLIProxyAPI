package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestHomeExecutionTrustsDispatchedAvailability(t *testing.T) {
	const model = "grok-4.7-build-fast"
	retryAt := time.Now().UTC().Add(time.Hour)
	cases := []struct {
		name  string
		auth  Auth
		state *ModelState
	}{
		{
			name: "client cancellation without recovery time",
			state: &ModelState{
				Status: StatusError, Unavailable: true,
				LastError: &Error{HTTPStatus: 499, Message: "context canceled"},
			},
		},
		{
			name: "model cooldown",
			state: &ModelState{
				Status: StatusError, Unavailable: true, NextRetryAfter: retryAt,
				LastError: &Error{HTTPStatus: http.StatusServiceUnavailable},
			},
		},
		{
			name: "model quota recovery",
			state: &ModelState{
				Status: StatusError,
				Quota:  QuotaState{Exceeded: true, NextRecoverAt: retryAt},
			},
		},
		{
			name:  "disabled model",
			state: &ModelState{Status: StatusDisabled},
		},
		{
			name: "disabled credential",
			auth: Auth{Disabled: true, Status: StatusDisabled},
		},
		{
			name: "credential cooldown without model states",
			auth: Auth{Status: StatusError, Unavailable: true, NextRetryAfter: retryAt},
		},
	}
	for _, tc := range cases {
		for _, path := range []string{"execute", "count_tokens", "stream", "websocket"} {
			t.Run(tc.name+"/"+path, func(t *testing.T) {
				auth := tc.auth
				auth.ID = "home-availability-auth"
				auth.Provider = "home-execution"
				auth.Attributes = map[string]string{"api_key": "test-key", "websockets": "true"}
				if tc.state != nil {
					auth.ModelStates = map[string]*ModelState{model: tc.state}
				}
				hook := &resultCaptureHook{}
				manager := NewManager(nil, nil, hook)
				manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
				executionRegistry := executionregistry.New()
				releases := make(chan executionregistry.ReleaseGroup, 2)
				executionRegistry.SetReleaseSink(func(group executionregistry.ReleaseGroup, _ int64) { releases <- group })
				dispatcher := &accountedHomeExecutionDispatcher{auths: []Auth{auth}}
				manager.PublishHomeDispatch(dispatcher, executionRegistry, 1)
				executor := &openAICompatPoolExecutor{id: auth.Provider}
				manager.RegisterExecutor(executor)

				ctx := context.Background()
				opts := cliproxyexecutor.Options{}
				if path == "websocket" {
					ctx = cliproxyexecutor.WithDownstreamWebsocket(ctx)
					opts.Metadata = map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "availability-session"}
					t.Cleanup(func() { manager.CloseExecutionSession("availability-session") })
				}
				req := cliproxyexecutor.Request{Model: model}
				var payload string
				switch path {
				case "execute":
					response, errExecute := manager.Execute(ctx, []string{auth.Provider}, req, opts)
					if errExecute != nil {
						t.Fatalf("Execute() error = %v", errExecute)
					}
					payload = string(response.Payload)
				case "count_tokens":
					response, errCount := manager.ExecuteCount(ctx, []string{auth.Provider}, req, opts)
					if errCount != nil {
						t.Fatalf("ExecuteCount() error = %v", errCount)
					}
					payload = string(response.Payload)
				default:
					opts.Stream = true
					response, errStream := manager.ExecuteStream(ctx, []string{auth.Provider}, req, opts)
					if errStream != nil {
						t.Fatalf("ExecuteStream() error = %v", errStream)
					}
					payload = readOpenAICompatStreamPayload(t, response)
				}
				if payload != model {
					t.Fatalf("executed model = %q, want Home-dispatched model %q", payload, model)
				}
				if calls := dispatcher.calls.Load(); calls != 1 {
					t.Fatalf("Home dispatch calls = %d, want 1", calls)
				}
				results := hook.Results()
				if len(results) != 1 || !results[0].Success || results[0].AuthID != auth.ID || results[0].Model != model {
					t.Fatalf("Home execution results = %#v, want one successful result for the dispatched credential", results)
				}
				if _, found := manager.GetByID(auth.ID); found {
					t.Fatal("Home-dispatched credential was registered in local auth state")
				}
				drainCtx, cancelDrain := context.WithTimeout(context.Background(), time.Second)
				defer cancelDrain()
				if errDrain := executionRegistry.Drain(drainCtx); errDrain != nil {
					t.Fatalf("Drain() error = %v", errDrain)
				}
				select {
				case group := <-releases:
					if group != (executionregistry.ReleaseGroup{CredentialID: auth.ID, Model: model}) {
						t.Fatalf("release group = %#v", group)
					}
				default:
					t.Fatal("Home selection did not release its concurrency admission")
				}
				select {
				case group := <-releases:
					t.Fatalf("duplicate concurrency release = %#v", group)
				default:
				}
			})
		}
	}
}

func TestStandaloneExecutionStillFiltersUnavailableModels(t *testing.T) {
	const model = "grok-4.7-build-fast"
	for _, path := range []string{"execute", "count_tokens", "stream"} {
		t.Run(path, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			manager.SetConfig(&internalconfig.Config{})
			auth := &Auth{
				ID: "standalone-availability-auth", Provider: "home-execution", Status: StatusActive,
				ModelStates: map[string]*ModelState{model: {
					Status: StatusError, Unavailable: true, NextRetryAfter: time.Now().UTC().Add(time.Hour),
					LastError: &Error{HTTPStatus: http.StatusServiceUnavailable},
				}},
			}
			if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
				t.Fatalf("Register() error = %v", errRegister)
			}
			modelRegistry := registry.GetGlobalRegistry()
			modelRegistry.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { modelRegistry.UnregisterClient(auth.ID) })
			executor := &openAICompatPoolExecutor{id: auth.Provider}
			manager.RegisterExecutor(executor)
			req := cliproxyexecutor.Request{Model: model}
			var errExecution error
			switch path {
			case "execute":
				_, errExecution = manager.Execute(context.Background(), []string{auth.Provider}, req, cliproxyexecutor.Options{})
			case "count_tokens":
				_, errExecution = manager.ExecuteCount(context.Background(), []string{auth.Provider}, req, cliproxyexecutor.Options{})
			case "stream":
				_, errExecution = manager.ExecuteStream(context.Background(), []string{auth.Provider}, req, cliproxyexecutor.Options{Stream: true})
			}
			if errExecution == nil {
				t.Fatal("standalone execution accepted a cooling model")
			}
			if calls := len(executor.ExecuteModels()) + len(executor.CountModels()) + len(executor.StreamModels()); calls != 0 {
				t.Fatalf("standalone executor calls = %d, want 0", calls)
			}
		})
	}
}

func TestHomeRetainedWebsocketSelectionPreservesAvailabilityState(t *testing.T) {
	const model = "grok-4.7-build-fast"
	auth := Auth{
		ID: "home-retained-availability-auth", Provider: "home-execution",
		Attributes: map[string]string{"websockets": "true"},
		ModelStates: map[string]*ModelState{model: {
			Status: StatusError, Unavailable: true,
			LastError: &Error{HTTPStatus: 499, Message: "context canceled"},
		}},
	}
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	dispatcher := &accountedHomeExecutionDispatcher{auths: []Auth{auth}}
	manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
	executor := &retainingHomeExecutionExecutor{}
	manager.RegisterExecutor(executor)
	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.ExecutionSessionMetadataKey: "retained-availability-session",
		cliproxyexecutor.PinnedAuthMetadataKey:       auth.ID,
	}}
	t.Cleanup(func() { manager.CloseExecutionSession("retained-availability-session") })
	for range 2 {
		if _, errExecute := manager.Execute(ctx, []string{auth.Provider}, cliproxyexecutor.Request{Model: model}, opts); errExecute != nil {
			t.Fatalf("Execute() error = %v", errExecute)
		}
	}
	if calls := dispatcher.calls.Load(); calls != 1 {
		t.Fatalf("Home dispatch calls = %d, want 1 for a retained selection", calls)
	}
	if calls := executor.calls.Load(); calls != 2 {
		t.Fatalf("executor calls = %d, want 2", calls)
	}
	retained, found := manager.GetExecutionSessionAuthByID("retained-availability-session", auth.ID)
	if !found || retained == nil || retained.ModelStates[model] == nil {
		t.Fatal("retained Home credential lost its model state")
	}
	state := retained.ModelStates[model]
	if !state.Unavailable || state.Status != StatusError || !state.NextRetryAfter.IsZero() || state.LastError == nil || state.LastError.HTTPStatus != 499 {
		t.Fatalf("CPA changed Home-owned availability state after successful execution: %#v", state)
	}
	if _, foundLocal := manager.GetByID(auth.ID); foundLocal {
		t.Fatal("retained Home credential was registered in local auth state")
	}
}
