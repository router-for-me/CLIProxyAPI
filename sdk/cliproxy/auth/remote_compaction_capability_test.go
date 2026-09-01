package auth

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type remoteCompactionCountingExecutor struct {
	executeCalls atomic.Int32
	countCalls   atomic.Int32
	streamCalls  atomic.Int32
}

func (e *remoteCompactionCountingExecutor) Identifier() string { return "codex" }

func (e *remoteCompactionCountingExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.executeCalls.Add(1)
	return cliproxyexecutor.Response{Payload: []byte("unexpected execute")}, nil
}

func (e *remoteCompactionCountingExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.streamCalls.Add(1)
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("unexpected stream")}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *remoteCompactionCountingExecutor) Refresh(context.Context, *Auth) (*Auth, error) {
	return nil, nil
}

func (e *remoteCompactionCountingExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.countCalls.Add(1)
	return cliproxyexecutor.Response{Payload: []byte("unexpected count")}, nil
}

func (e *remoteCompactionCountingExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func newRemoteCompactionInterceptorManager(t *testing.T, executor *remoteCompactionCountingExecutor) *Manager {
	t.Helper()
	const (
		authID = "remote-compaction-interceptor-auth"
		model  = "remote-compaction-interceptor-model"
	)
	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 1)
	manager.RegisterExecutor(executor)
	registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: authID, Provider: "codex", Status: StatusActive}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	return manager
}

func assertRemoteCompactionInterceptorError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("execution error = nil, want request-scoped trigger removal error")
	}
	var requestErr *Error
	if !errors.As(err, &requestErr) {
		t.Fatalf("execution error = %T (%v), want *Error", err, err)
	}
	if !requestErr.IsRequestScoped() {
		t.Fatalf("error code = %q, want request-scoped", requestErr.Code)
	}
	if requestErr.HTTPStatus != http.StatusBadRequest {
		t.Fatalf("error status = %d, want %d", requestErr.HTTPStatus, http.StatusBadRequest)
	}
}

func TestAuthSupportsRemoteCompactionV2(t *testing.T) {
	tests := []struct {
		name string
		auth *Auth
		want bool
	}{
		{name: "explicit compatibility opt in", auth: &Auth{Provider: "openai-compatible-provider", Attributes: map[string]string{AttributeRemoteCompactionV2: "true", "compat_name": "provider"}}, want: true},
		{name: "compatibility default off", auth: &Auth{Provider: "openai-compatible-provider", Attributes: map[string]string{"compat_name": "provider"}}, want: false},
		{name: "native codex default on", auth: &Auth{Provider: "codex"}, want: true},
		{name: "native codex remains enabled", auth: &Auth{Provider: "codex", Attributes: map[string]string{AttributeRemoteCompactionV2: "false"}}, want: true},
		{name: "xai is not v2", auth: &Auth{Provider: "xai"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := authSupportsRemoteCompactionV2(tt.auth); got != tt.want {
				t.Fatalf("authSupportsRemoteCompactionV2() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAuthHandlesRemoteCompactionTriggerIncludesXAI(t *testing.T) {
	if !authHandlesRemoteCompactionTrigger(&Auth{Provider: "xai"}) {
		t.Fatal("xAI must remain selectable for compaction_trigger via the origin v1 adapter")
	}
	if authHandlesRemoteCompactionTrigger(&Auth{Provider: "gemini"}) {
		t.Fatal("gemini must not handle compaction_trigger")
	}
}

func TestMarkRemoteCompactionRequirementChecksPayloadAndOriginalRequest(t *testing.T) {
	trigger := []byte(`{"input":[{"type":"compaction_trigger"}]}`)
	ordinary := []byte(`{"input":[{"type":"message"}]}`)

	for _, test := range []struct {
		name            string
		payload         []byte
		originalRequest []byte
		want            bool
	}{
		{name: "request payload", payload: trigger, originalRequest: ordinary, want: true},
		{name: "original request", payload: ordinary, originalRequest: trigger, want: true},
		{name: "no trigger", payload: ordinary, originalRequest: ordinary, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			opts := markRemoteCompactionRequirement(cliproxyexecutor.Options{OriginalRequest: test.originalRequest}, test.payload)
			if got := remoteCompactionRequired(opts); got != test.want {
				t.Fatalf("remoteCompactionRequired() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestRequestHasRemoteCompactionTriggerRequiresCanonicalCase(t *testing.T) {
	for _, triggerType := range []string{"Compaction_Trigger", "COMPACTION_TRIGGER", "compaction_Trigger"} {
		t.Run(triggerType, func(t *testing.T) {
			payload := []byte(`{"input":[{"type":"` + triggerType + `"}]}`)
			if requestHasRemoteCompactionTrigger(payload) {
				t.Fatalf("requestHasRemoteCompactionTrigger(%q) = true, want false", triggerType)
			}
		})
	}
}

func TestPreserveHomeRoutingAttributesKeepsRemoteCompactionCapability(t *testing.T) {
	previous := &Auth{Attributes: map[string]string{AttributeRemoteCompactionV2: "true"}}
	updated := &Auth{}

	preserveHomeRoutingAttributes(updated, previous)

	if got := updated.Attributes[AttributeRemoteCompactionV2]; got != "true" {
		t.Fatalf("remote compaction attribute = %q, want true", got)
	}
}

func TestRequestAfterAuthInterceptorTriggerRemovalIsRequestScoped(t *testing.T) {
	const model = "remote-compaction-interceptor-model"
	for _, test := range []struct {
		name string
		run  func(*Manager, cliproxyexecutor.Request, cliproxyexecutor.Options) error
		get  func(*remoteCompactionCountingExecutor) int32
	}{
		{
			name: "execute",
			run: func(manager *Manager, request cliproxyexecutor.Request, opts cliproxyexecutor.Options) error {
				_, errExecute := manager.Execute(context.Background(), []string{"codex"}, request, opts)
				return errExecute
			},
			get: func(executor *remoteCompactionCountingExecutor) int32 { return executor.executeCalls.Load() },
		},
		{
			name: "execute_count",
			run: func(manager *Manager, request cliproxyexecutor.Request, opts cliproxyexecutor.Options) error {
				_, errCount := manager.ExecuteCount(context.Background(), []string{"codex"}, request, opts)
				return errCount
			},
			get: func(executor *remoteCompactionCountingExecutor) int32 { return executor.countCalls.Load() },
		},
		{
			name: "execute_stream",
			run: func(manager *Manager, request cliproxyexecutor.Request, opts cliproxyexecutor.Options) error {
				_, errStream := manager.ExecuteStream(context.Background(), []string{"codex"}, request, opts)
				return errStream
			},
			get: func(executor *remoteCompactionCountingExecutor) int32 { return executor.streamCalls.Load() },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			executor := &remoteCompactionCountingExecutor{}
			manager := newRemoteCompactionInterceptorManager(t, executor)
			request := cliproxyexecutor.Request{Model: model, Payload: []byte(`{"input":[{"type":"message","content":"history"},{"type":"compaction_trigger"}]}`)}
			opts := cliproxyexecutor.Options{Stream: test.name == "execute_stream", RequestAfterAuthInterceptor: func(context.Context, cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
				return cliproxyexecutor.RequestAfterAuthInterceptResponse{Body: []byte(`{"input":[{"type":"message","content":"history"}]}`)}
			}}

			assertRemoteCompactionInterceptorError(t, test.run(manager, request, opts))
			if got := test.get(executor); got != 0 {
				t.Fatalf("executor calls = %d, want 0", got)
			}
		})
	}
}
