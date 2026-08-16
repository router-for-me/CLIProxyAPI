package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

type homeExecutionDispatcher struct{}

func (homeExecutionDispatcher) HeartbeatOK() bool { return true }

func (homeExecutionDispatcher) RPopAuth(context.Context, string, string, http.Header, int) ([]byte, error) {
	return json.Marshal(homeAuthDispatchResponse{Auth: Auth{ID: "home-auth", Provider: "home-execution", Status: StatusActive}})
}

func (homeExecutionDispatcher) AbortAmbiguousDispatch() {}

type homeExecutionStreamExecutor struct {
	chunks <-chan cliproxyexecutor.StreamChunk
}

type homeExecutionExecutor struct {
	ctx context.Context
}

func (*homeExecutionExecutor) Identifier() string { return "home-execution" }
func (e *homeExecutionExecutor) Execute(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.ctx = ctx
	if errCtx := ctx.Err(); errCtx != nil {
		return cliproxyexecutor.Response{}, errCtx
	}
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}
func (*homeExecutionExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}
func (*homeExecutionExecutor) Refresh(context.Context, *Auth) (*Auth, error) { return nil, nil }
func (*homeExecutionExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (*homeExecutionExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (*homeExecutionStreamExecutor) Identifier() string { return "home-execution" }
func (*homeExecutionStreamExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e *homeExecutionStreamExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return &cliproxyexecutor.StreamResult{Chunks: e.chunks}, nil
}
func (*homeExecutionStreamExecutor) Refresh(context.Context, *Auth) (*Auth, error) { return nil, nil }
func (*homeExecutionStreamExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (*homeExecutionStreamExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestHomeModeNeverAuthorizesLocalAuthFallback(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	cfg := &internalconfig.Config{}
	cfg.Home.Enabled = true
	manager.runtimeConfig.Store(cfg)
	manager.auths["local-antigravity"] = &Auth{ID: "local-antigravity", Provider: "antigravity", Status: StatusActive}

	if manager.localExecutionAllowed() {
		t.Fatal("local execution allowed in Home mode")
	}
	if selected := manager.localFallbackAuth("local-antigravity"); selected != nil {
		t.Fatalf("local fallback auth = %#v", selected)
	}
}

func TestHomeSelectionEndsAfterExecute(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.PublishHomeDispatch(homeExecutionDispatcher{}, executionregistry.New(), 1)
	executor := &homeExecutionExecutor{}
	manager.RegisterExecutor(executor)

	if _, errExecute := manager.Execute(context.Background(), []string{"home-execution"}, cliproxyexecutor.Request{Model: "test"}, cliproxyexecutor.Options{}); errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if executor.ctx == nil {
		t.Fatal("executor did not receive an attempt context")
	}
	if errCtx := executor.ctx.Err(); errCtx == nil {
		t.Fatal("attempt context was not canceled after execution")
	}
}

func TestHomeNonStreamingExecutionLogsSelectedOAuthAuth(t *testing.T) {
	previousLevel := log.GetLevel()
	log.SetLevel(log.DebugLevel)
	hook := logtest.NewLocal(log.StandardLogger())
	t.Cleanup(func() {
		hook.Reset()
		log.SetLevel(previousLevel)
	})

	tests := []struct {
		name string
		run  func(*Manager, context.Context) error
	}{
		{
			name: "execute",
			run: func(manager *Manager, ctx context.Context) error {
				_, errExecute := manager.Execute(ctx, []string{"home-execution"}, cliproxyexecutor.Request{Model: "model-a"}, cliproxyexecutor.Options{})
				return errExecute
			},
		},
		{
			name: "count_tokens",
			run: func(manager *Manager, ctx context.Context) error {
				_, errCount := manager.ExecuteCount(ctx, []string{"home-execution"}, cliproxyexecutor.Request{Model: "model-a"}, cliproxyexecutor.Options{})
				return errCount
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hook.Reset()
			manager := NewManager(nil, nil, nil)
			manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
			manager.PublishHomeDispatch(homeOAuthLoggingDispatcher{}, executionregistry.New(), 1)
			manager.RegisterExecutor(&homeExecutionExecutor{})

			ctx := internallogging.WithRequestID(context.Background(), "req-home-log")
			if errRun := tt.run(manager, ctx); errRun != nil {
				t.Fatalf("execution error = %v", errRun)
			}

			const expected = "Use OAuth provider=home-execution auth_file=home-auth for model model-a via socks5 proxy"
			for _, entry := range hook.AllEntries() {
				if entry.Level == log.DebugLevel && entry.Message == expected {
					if got := entry.Data["request_id"]; got != "req-home-log" {
						t.Fatalf("request_id = %v, want req-home-log", got)
					}
					return
				}
			}
			t.Fatalf("selected auth log %q not found", expected)
		})
	}
}

type homeOAuthLoggingDispatcher struct{}

func (homeOAuthLoggingDispatcher) HeartbeatOK() bool { return true }

func (homeOAuthLoggingDispatcher) RPopAuth(context.Context, string, string, http.Header, int) ([]byte, error) {
	return json.Marshal(homeAuthDispatchResponse{Auth: Auth{
		ID:       "home-auth",
		Provider: "home-execution",
		ProxyURL: "socks5://127.0.0.1:1080",
		Status:   StatusActive,
		Attributes: map[string]string{
			AttributeAuthKind: AuthKindOAuth,
		},
	}})
}

func (homeOAuthLoggingDispatcher) AbortAmbiguousDispatch() {}

func TestHomeSelectionEndsOnMissingExecutor(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	registry := executionregistry.New()
	manager.PublishHomeDispatch(homeExecutionDispatcher{}, registry, 1)

	if _, errExecute := manager.Execute(context.Background(), []string{"home-execution"}, cliproxyexecutor.Request{Model: "test"}, cliproxyexecutor.Options{}); errExecute == nil {
		t.Fatal("Execute() error = nil, want missing executor")
	}
	if errDrain := registry.Drain(context.Background()); errDrain != nil {
		t.Fatalf("Drain() error = %v", errDrain)
	}
}

func TestHomeSelectionClosesAttemptAndWebSocketResources(t *testing.T) {
	registry := executionregistry.New()
	pending, errBegin := registry.BeginDispatch()
	if errBegin != nil {
		t.Fatal(errBegin)
	}
	scope, errInstall := registry.Install(pending, executionregistry.ScopeSpec{})
	if errInstall != nil {
		t.Fatal(errInstall)
	}
	selection, errSelection := newHomeDispatchSelection(&Auth{ID: "home-auth"}, nil, "home-execution", scope)
	if errSelection != nil {
		t.Fatal(errSelection)
	}
	attemptCtx, releaseAttempt, errBind := homeExecutionAttemptContext(context.Background(), selection)
	if errBind != nil {
		t.Fatal(errBind)
	}
	var closeCalls atomic.Int32
	if errBind = selection.Bind(func() error {
		closeCalls.Add(1)
		return nil
	}); errBind != nil {
		t.Fatal(errBind)
	}
	selection.End("completed")
	releaseAttempt()
	if errCtx := attemptCtx.Err(); errCtx == nil {
		t.Fatal("attempt context was not canceled")
	}
	if got := closeCalls.Load(); got != 1 {
		t.Fatalf("resource close calls = %d, want 1", got)
	}
}

func TestHomeStreamConsumerCancelEndsSelection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	registry := executionregistry.New()
	manager.PublishHomeDispatch(homeExecutionDispatcher{}, registry, 1)
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("initial")}
	manager.RegisterExecutor(&homeExecutionStreamExecutor{chunks: chunks})

	result, errExecute := manager.ExecuteStream(ctx, []string{"home-execution"}, cliproxyexecutor.Request{Model: "test"}, cliproxyexecutor.Options{Stream: true})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}
	cancel()
	for range result.Chunks {
	}
	if errDrain := registry.Drain(context.Background()); errDrain != nil {
		t.Fatalf("Drain() error = %v", errDrain)
	}
}

type retainingHomeExecutionDispatcher struct {
	calls atomic.Int32
}

func (d *retainingHomeExecutionDispatcher) HeartbeatOK() bool { return true }

func (d *retainingHomeExecutionDispatcher) RPopAuth(context.Context, string, string, http.Header, int) ([]byte, error) {
	d.calls.Add(1)
	return json.Marshal(homeAuthDispatchResponse{Auth: Auth{
		ID:       "home-auth",
		Provider: "home-execution",
		Status:   StatusActive,
		Attributes: map[string]string{
			"websockets": "true",
		},
	}})
}

func (*retainingHomeExecutionDispatcher) AbortAmbiguousDispatch() {}

type retainingHomeExecutionExecutor struct {
	calls atomic.Int32
}

func (*retainingHomeExecutionExecutor) Identifier() string { return "home-execution" }

func (e *retainingHomeExecutionExecutor) Execute(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.calls.Add(1)
	if lifecycle, ok := opts.ExecutionLifecycle.(interface{ Retain() }); ok {
		lifecycle.Retain()
	}
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (*retainingHomeExecutionExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}
func (*retainingHomeExecutionExecutor) Refresh(context.Context, *Auth) (*Auth, error) {
	return nil, nil
}
func (*retainingHomeExecutionExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (*retainingHomeExecutionExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestHomeWebsocketSessionReusesRetainedSelection(t *testing.T) {
	dispatcher := &retainingHomeExecutionDispatcher{}
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
	executor := &retainingHomeExecutionExecutor{}
	manager.RegisterExecutor(executor)

	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.ExecutionSessionMetadataKey: "session-1",
		cliproxyexecutor.PinnedAuthMetadataKey:       "home-auth",
	}}
	for range 2 {
		if _, errExecute := manager.Execute(ctx, []string{"home-execution"}, cliproxyexecutor.Request{Model: "model-a"}, opts); errExecute != nil {
			t.Fatalf("Execute() error = %v", errExecute)
		}
	}
	if got := dispatcher.calls.Load(); got != 1 {
		t.Fatalf("Home RPOP calls = %d, want 1 for one retained session target", got)
	}
	if got := executor.calls.Load(); got != 2 {
		t.Fatalf("executor calls = %d, want 2", got)
	}
}

type changingHomeTargetDispatcher struct {
	calls              atomic.Int32
	firstSelection     *HomeDispatchSelection
	oldEndedBeforeRPop atomic.Bool
}

func (d *changingHomeTargetDispatcher) HeartbeatOK() bool { return true }
func (d *changingHomeTargetDispatcher) RPopAuth(context.Context, string, string, http.Header, int) ([]byte, error) {
	if d.calls.Add(1) == 2 && d.firstSelection != nil {
		d.oldEndedBeforeRPop.Store(!d.firstSelection.Active())
	}
	return json.Marshal(homeAuthDispatchResponse{Auth: Auth{ID: "home-auth", Provider: "home-execution", Status: StatusActive, Attributes: map[string]string{"websockets": "true"}}})
}
func (*changingHomeTargetDispatcher) AbortAmbiguousDispatch() {}

type selectionRecordingExecutor struct {
	first *HomeDispatchSelection
}

func (*selectionRecordingExecutor) Identifier() string { return "home-execution" }
func (e *selectionRecordingExecutor) Execute(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	selection, _ := opts.ExecutionLifecycle.(*HomeDispatchSelection)
	if e.first == nil {
		e.first = selection
	}
	if selection != nil {
		selection.Retain()
	}
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}
func (*selectionRecordingExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}
func (*selectionRecordingExecutor) Refresh(context.Context, *Auth) (*Auth, error) { return nil, nil }
func (*selectionRecordingExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (*selectionRecordingExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestHomeWebsocketTargetChangeEndsSelectionBeforeRedispatch(t *testing.T) {
	dispatcher := &changingHomeTargetDispatcher{}
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
	executor := &selectionRecordingExecutor{}
	manager.RegisterExecutor(executor)
	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.ExecutionSessionMetadataKey: "session-1",
		cliproxyexecutor.PinnedAuthMetadataKey:       "home-auth",
	}}

	if _, errExecute := manager.Execute(ctx, []string{"home-execution"}, cliproxyexecutor.Request{Model: "model-a"}, opts); errExecute != nil {
		t.Fatalf("first Execute() error = %v", errExecute)
	}
	dispatcher.firstSelection = executor.first
	if _, errExecute := manager.Execute(ctx, []string{"home-execution"}, cliproxyexecutor.Request{Model: "model-b"}, opts); errExecute != nil {
		t.Fatalf("second Execute() error = %v", errExecute)
	}
	if got := dispatcher.calls.Load(); got != 2 {
		t.Fatalf("Home RPOP calls = %d, want 2 after target change", got)
	}
	if !dispatcher.oldEndedBeforeRPop.Load() {
		t.Fatal("previous selection remained active when target-change RPOP started")
	}
}

type unpinnedTargetChangeDispatcher struct {
	calls                   atomic.Int32
	first                   *HomeDispatchSelection
	oldClosedBeforeDispatch atomic.Bool
	closeCalls              *atomic.Int32
}

func (d *unpinnedTargetChangeDispatcher) HeartbeatOK() bool { return true }
func (d *unpinnedTargetChangeDispatcher) RPopAuth(_ context.Context, _ string, _ string, _ http.Header, _ int) ([]byte, error) {
	call := d.calls.Add(1)
	if call == 2 && d.first != nil {
		d.oldClosedBeforeDispatch.Store(!d.first.Active() && d.closeCalls.Load() == 1)
	}
	return json.Marshal(homeAuthDispatchResponse{Auth: Auth{
		ID:       "home-auth-" + strconv.Itoa(int(call)),
		Provider: "home-execution",
		Status:   StatusActive,
		Attributes: map[string]string{
			"websockets": "true",
		},
	}})
}
func (*unpinnedTargetChangeDispatcher) AbortAmbiguousDispatch() {}

type bindingSelectionRecordingExecutor struct {
	first      *HomeDispatchSelection
	closeCalls *atomic.Int32
}

func (*bindingSelectionRecordingExecutor) Identifier() string { return "home-execution" }
func (e *bindingSelectionRecordingExecutor) Execute(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	selection, _ := opts.ExecutionLifecycle.(*HomeDispatchSelection)
	if e.first == nil {
		e.first = selection
	}
	if selection != nil {
		if errBind := selection.Bind(func() error {
			e.closeCalls.Add(1)
			return nil
		}); errBind != nil {
			return cliproxyexecutor.Response{}, errBind
		}
		selection.Retain()
	}
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}
func (*bindingSelectionRecordingExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}
func (*bindingSelectionRecordingExecutor) Refresh(context.Context, *Auth) (*Auth, error) {
	return nil, nil
}
func (*bindingSelectionRecordingExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (*bindingSelectionRecordingExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestHomeWebsocketUnpinnedModelChangeClosesSelectionBeforeRedispatch(t *testing.T) {
	var closeCalls atomic.Int32
	dispatcher := &unpinnedTargetChangeDispatcher{closeCalls: &closeCalls}
	registry := executionregistry.New()
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.PublishHomeDispatch(dispatcher, registry, 1)
	executor := &bindingSelectionRecordingExecutor{closeCalls: &closeCalls}
	manager.RegisterExecutor(executor)
	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.ExecutionSessionMetadataKey: "session-1",
	}}

	if _, errExecute := manager.Execute(ctx, []string{"home-execution"}, cliproxyexecutor.Request{Model: "model-a"}, opts); errExecute != nil {
		t.Fatalf("first Execute() error = %v", errExecute)
	}
	dispatcher.first = executor.first
	if _, errExecute := manager.Execute(ctx, []string{"home-execution"}, cliproxyexecutor.Request{Model: "model-b"}, opts); errExecute != nil {
		t.Fatalf("second Execute() error = %v", errExecute)
	}
	if got := dispatcher.calls.Load(); got != 2 {
		t.Fatalf("Home RPOP calls = %d, want 2", got)
	}
	if !dispatcher.oldClosedBeforeDispatch.Load() {
		t.Fatal("old unpinned selection was not ended and closed before the second RPOP")
	}
	manager.CloseExecutionSession("session-1")
	if errDrain := registry.Drain(context.Background()); errDrain != nil {
		t.Fatalf("Drain() error = %v", errDrain)
	}
}

type lifecycleRetryDispatcher struct {
	calls                      atomic.Int32
	executor                   *lifecycleRetryExecutor
	firstEndedBeforeRedispatch atomic.Bool
}

func (d *lifecycleRetryDispatcher) HeartbeatOK() bool { return true }
func (d *lifecycleRetryDispatcher) RPopAuth(_ context.Context, _ string, _ string, _ http.Header, _ int) ([]byte, error) {
	if d.calls.Add(1) == 2 && d.executor.first != nil {
		d.firstEndedBeforeRedispatch.Store(!d.executor.first.Active() && d.executor.firstCtx.Err() != nil)
	}
	return json.Marshal(homeAuthDispatchResponse{Auth: Auth{ID: "home-auth", Provider: "home-execution", Status: StatusActive, Attributes: map[string]string{"websockets": "true"}}})
}
func (*lifecycleRetryDispatcher) AbortAmbiguousDispatch() {}

type lifecycleRetryExecutor struct {
	calls    atomic.Int32
	first    *HomeDispatchSelection
	firstCtx context.Context
}

func (*lifecycleRetryExecutor) Identifier() string { return "home-execution" }
func (*lifecycleRetryExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e *lifecycleRetryExecutor) ExecuteStream(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if e.calls.Add(1) == 1 {
		e.first, _ = opts.ExecutionLifecycle.(*HomeDispatchSelection)
		e.firstCtx = ctx
		return nil, &Error{HTTPStatus: http.StatusUpgradeRequired, Message: "websocket upgrade required"}
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"type":"response.completed"}`)}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}
func (*lifecycleRetryExecutor) Refresh(context.Context, *Auth) (*Auth, error) { return nil, nil }
func (*lifecycleRetryExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (*lifecycleRetryExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestHomeStreamLifecycleFailureEndsBeforeFreshDispatch(t *testing.T) {
	executor := &lifecycleRetryExecutor{}
	dispatcher := &lifecycleRetryDispatcher{executor: executor}
	registry := executionregistry.New()
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.PublishHomeDispatch(dispatcher, registry, 1)
	manager.RegisterExecutor(executor)
	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	opts := cliproxyexecutor.Options{Stream: true, Metadata: map[string]any{
		cliproxyexecutor.ExecutionSessionMetadataKey: "session-426",
	}}

	result, errExecute := manager.ExecuteStream(ctx, []string{"home-execution"}, cliproxyexecutor.Request{Model: "model-a"}, opts)
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}
	for range result.Chunks {
	}
	if got := executor.calls.Load(); got != 2 {
		t.Fatalf("executor invocations = %d, want 2", got)
	}
	if got := dispatcher.calls.Load(); got != 2 {
		t.Fatalf("Home RPOP calls = %d, want 2", got)
	}
	if !dispatcher.firstEndedBeforeRedispatch.Load() {
		t.Fatal("failed stream attempt remained active when the fresh Home selection was dispatched")
	}
	if errDrain := registry.Drain(context.Background()); errDrain != nil {
		t.Fatalf("Drain() error = %v", errDrain)
	}
}

func TestHomeSelectionCancellationPreventsExecute(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.PublishHomeDispatch(homeExecutionDispatcher{}, executionregistry.New(), 1)
	executor := &homeExecutionExecutor{}
	manager.RegisterExecutor(executor)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, errExecute := manager.Execute(ctx, []string{"home-execution"}, cliproxyexecutor.Request{Model: "test"}, cliproxyexecutor.Options{})
	if errExecute == nil {
		t.Fatal("Execute() error = nil, want canceled context")
	}
	if executor.ctx != nil {
		t.Fatal("executor was invoked after attempt context cancellation")
	}
}

type retryingHomeStreamExecutor struct {
	calls atomic.Int32
}

func (*retryingHomeStreamExecutor) Identifier() string { return "home-execution" }
func (*retryingHomeStreamExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e *retryingHomeStreamExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if e.calls.Add(1) == 1 {
		return nil, &Error{HTTPStatus: http.StatusUnauthorized, Message: "expired"}
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("data: {\"type\":\"response.completed\"}\n\n")}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}
func (*retryingHomeStreamExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}
func (*retryingHomeStreamExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (*retryingHomeStreamExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestHomeStreamRetryUsesFreshSelection(t *testing.T) {
	dispatcher := &retainingHomeExecutionDispatcher{}
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
	executor := &retryingHomeStreamExecutor{}
	manager.RegisterExecutor(executor)

	result, errExecute := manager.ExecuteStream(context.Background(), []string{"home-execution"}, cliproxyexecutor.Request{Model: "model-a"}, cliproxyexecutor.Options{Stream: true})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}
	for range result.Chunks {
	}
	if got := dispatcher.calls.Load(); got != 2 {
		t.Fatalf("Home RPOP calls = %d, want 2 for retrying stream invocations", got)
	}
}

type cancellationBarrierExecutor struct {
	executeCalls atomic.Int32
	countCalls   atomic.Int32
	streamCalls  atomic.Int32
}

func (*cancellationBarrierExecutor) Identifier() string { return "home-execution" }
func (e *cancellationBarrierExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.executeCalls.Add(1)
	return cliproxyexecutor.Response{}, nil
}
func (e *cancellationBarrierExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.countCalls.Add(1)
	return cliproxyexecutor.Response{}, nil
}
func (e *cancellationBarrierExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.streamCalls.Add(1)
	return nil, nil
}
func (*cancellationBarrierExecutor) Refresh(context.Context, *Auth) (*Auth, error) { return nil, nil }
func (*cancellationBarrierExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestHomeCancellationBarrierPreventsEveryExecutorInvocation(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.PublishHomeDispatch(homeExecutionDispatcher{}, executionregistry.New(), 1)
	executor := &cancellationBarrierExecutor{}
	manager.RegisterExecutor(executor)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, errExecute := manager.Execute(ctx, []string{"home-execution"}, cliproxyexecutor.Request{Model: "test"}, cliproxyexecutor.Options{}); errExecute == nil {
		t.Fatal("Execute() error = nil, want canceled context")
	}
	if _, errCount := manager.ExecuteCount(ctx, []string{"home-execution"}, cliproxyexecutor.Request{Model: "test"}, cliproxyexecutor.Options{}); errCount == nil {
		t.Fatal("ExecuteCount() error = nil, want canceled context")
	}
	if _, errStream := manager.ExecuteStream(ctx, []string{"home-execution"}, cliproxyexecutor.Request{Model: "test"}, cliproxyexecutor.Options{Stream: true}); errStream == nil {
		t.Fatal("ExecuteStream() error = nil, want canceled context")
	}
	if got := executor.executeCalls.Load(); got != 0 {
		t.Fatalf("Execute calls = %d, want 0", got)
	}
	if got := executor.countCalls.Load(); got != 0 {
		t.Fatalf("CountTokens calls = %d, want 0", got)
	}
	if got := executor.streamCalls.Load(); got != 0 {
		t.Fatalf("ExecuteStream calls = %d, want 0", got)
	}
}

func TestHomeStreamEndsOnTerminalChunk(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	registry := executionregistry.New()
	manager.PublishHomeDispatch(homeExecutionDispatcher{}, registry, 1)
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("initial")}
	manager.RegisterExecutor(&homeExecutionStreamExecutor{chunks: chunks})

	result, errExecute := manager.ExecuteStream(context.Background(), []string{"home-execution"}, cliproxyexecutor.Request{Model: "test"}, cliproxyexecutor.Options{Stream: true})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}

	close(chunks)
	for range result.Chunks {
	}
	if errDrain := registry.Drain(context.Background()); errDrain != nil {
		t.Fatalf("Drain() error = %v", errDrain)
	}
}

func TestHomeWebsocketSessionReusesSelectionWithoutPinnedMetadataAndCachesRuntimeAuth(t *testing.T) {
	dispatcher := &retainingHomeExecutionDispatcher{}
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
	executor := &retainingHomeExecutionExecutor{}
	manager.RegisterExecutor(executor)

	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.ExecutionSessionMetadataKey: "session-without-pin",
	}}
	for range 2 {
		if _, errExecute := manager.Execute(ctx, []string{"home-execution"}, cliproxyexecutor.Request{Model: "model-a"}, opts); errExecute != nil {
			t.Fatalf("Execute() error = %v", errExecute)
		}
	}
	if got := dispatcher.calls.Load(); got != 1 {
		t.Fatalf("Home RPOP calls = %d, want 1 for a retained session without a pin", got)
	}
	if auth, ok := manager.GetExecutionSessionAuthByID("session-without-pin", "home-auth"); !ok || auth == nil {
		t.Fatal("retained selection did not populate the handler runtime auth cache")
	}
}

func TestCloseExecutionSessionReclaimsHomeSessionLock(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.ExecutionSessionMetadataKey: "reclaim-lock",
	}}
	unlock := manager.lockHomeWebsocketSession(ctx, opts)
	if unlock == nil {
		t.Fatal("lockHomeWebsocketSession() = nil")
	}
	unlock()
	if _, ok := manager.homeSessionLocks.Load("reclaim-lock"); !ok {
		t.Fatal("session lock was not created")
	}

	manager.CloseExecutionSession("reclaim-lock")
	if _, ok := manager.homeSessionLocks.Load("reclaim-lock"); ok {
		t.Fatal("closed session retained its mutex entry")
	}
}

type homePerSelectionDispatcher struct {
	auths             []Auth
	calls             atomic.Int32
	first             *HomeDispatchSelection
	firstEndedBefore2 atomic.Bool
}

func (*homePerSelectionDispatcher) HeartbeatOK() bool { return true }
func (d *homePerSelectionDispatcher) RPopAuth(context.Context, string, string, http.Header, int) ([]byte, error) {
	call := d.calls.Add(1)
	if call == 2 && d.first != nil {
		d.firstEndedBefore2.Store(!d.first.Active())
	}
	if int(call) > len(d.auths) {
		return nil, home.ErrAuthNotFound
	}
	return json.Marshal(homeAuthDispatchResponse{Auth: d.auths[call-1]})
}
func (*homePerSelectionDispatcher) AbortAmbiguousDispatch() {}

type homePerSelectionFailureExecutor struct {
	dispatcher  *homePerSelectionDispatcher
	selections  []*HomeDispatchSelection
	invocations []string
}

func (*homePerSelectionFailureExecutor) Identifier() string { return openAICompatPoolProviderKey }
func (e *homePerSelectionFailureExecutor) invoke(auth *Auth, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	selection, _ := opts.ExecutionLifecycle.(*HomeDispatchSelection)
	if e.selections == nil {
		e.selections = append(e.selections, selection)
	}
	if selection != nil && len(e.selections) == 1 {
		e.selections[0] = selection
		if e.dispatcher != nil {
			e.dispatcher.first = selection
		}
	}
	e.invocations = append(e.invocations, auth.ID)
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusBadGateway, Message: "upstream failed"}
}
func (e *homePerSelectionFailureExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.invoke(auth, opts)
}
func (*homePerSelectionFailureExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}
func (*homePerSelectionFailureExecutor) Refresh(context.Context, *Auth) (*Auth, error) {
	return nil, nil
}
func (e *homePerSelectionFailureExecutor) CountTokens(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.invoke(auth, opts)
}
func (*homePerSelectionFailureExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestHomeNonstreamAndCountUseOneModelPerSelection(t *testing.T) {
	for _, countTokens := range []bool{false, true} {
		t.Run(map[bool]string{false: "Execute", true: "CountTokens"}[countTokens], func(t *testing.T) {
			dispatcher := &homePerSelectionDispatcher{auths: []Auth{
				{ID: "home-auth-a", Provider: "home-pool", Status: StatusActive, Attributes: map[string]string{"api_key": "test-key", "compat_name": "pool", "provider_key": "pool"}},
				{ID: "home-auth-b", Provider: "home-pool", Status: StatusActive, Attributes: map[string]string{"api_key": "test-key", "compat_name": "pool", "provider_key": "pool"}},
			}}
			manager := NewManager(nil, nil, nil)
			manager.SetConfig(&internalconfig.Config{
				Home: internalconfig.HomeConfig{Enabled: true},
				OpenAICompatibility: []internalconfig.OpenAICompatibility{{
					Name:   "pool",
					Models: []internalconfig.OpenAICompatibilityModel{{Name: "upstream-a", Alias: "requested"}, {Name: "upstream-b", Alias: "requested"}},
				}},
			})
			manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
			executor := &homePerSelectionFailureExecutor{dispatcher: dispatcher}
			manager.RegisterExecutor(executor)

			var errExecute error
			if countTokens {
				_, errExecute = manager.ExecuteCount(context.Background(), []string{openAICompatPoolProviderKey}, cliproxyexecutor.Request{Model: "requested"}, cliproxyexecutor.Options{})
			} else {
				_, errExecute = manager.Execute(context.Background(), []string{openAICompatPoolProviderKey}, cliproxyexecutor.Request{Model: "requested"}, cliproxyexecutor.Options{})
			}
			if errExecute == nil {
				t.Fatal("execution error = nil, want upstream failure")
			}
			if len(executor.invocations) != 2 {
				t.Fatalf("execution error = %v; upstream invocations = %v, want one per Home selection", errExecute, executor.invocations)
			}
			if !dispatcher.firstEndedBefore2.Load() {
				t.Fatal("first Home selection was not ended before the next dispatch")
			}
		})
	}
}

func TestHomeStreamEndsOnErrorChunk(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	registry := executionregistry.New()
	manager.PublishHomeDispatch(homeExecutionDispatcher{}, registry, 1)
	chunks := make(chan cliproxyexecutor.StreamChunk, 2)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("initial")}
	chunks <- cliproxyexecutor.StreamChunk{Err: &Error{HTTPStatus: http.StatusBadGateway, Message: "upstream failed"}}
	close(chunks)
	manager.RegisterExecutor(&homeExecutionStreamExecutor{chunks: chunks})

	result, errExecute := manager.ExecuteStream(context.Background(), []string{"home-execution"}, cliproxyexecutor.Request{Model: "test"}, cliproxyexecutor.Options{Stream: true})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}
	sawError := false
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			sawError = true
		}
	}
	if !sawError {
		t.Fatal("stream did not preserve the upstream error chunk")
	}
	if errDrain := registry.Drain(context.Background()); errDrain != nil {
		t.Fatalf("Drain() error = %v", errDrain)
	}
}

type missingHomeStreamSourceExecutor struct{}

func (*missingHomeStreamSourceExecutor) Identifier() string { return "home-execution" }
func (*missingHomeStreamSourceExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (*missingHomeStreamSourceExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}
func (*missingHomeStreamSourceExecutor) Refresh(context.Context, *Auth) (*Auth, error) {
	return nil, nil
}
func (*missingHomeStreamSourceExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (*missingHomeStreamSourceExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

type accountedHomeExecutionDispatcher struct {
	calls atomic.Int32
	auths []Auth
}

func (*accountedHomeExecutionDispatcher) HeartbeatOK() bool { return true }
func (d *accountedHomeExecutionDispatcher) RPopAuth(_ context.Context, model string, _ string, _ http.Header, _ int) ([]byte, error) {
	index := int(d.calls.Add(1)) - 1
	if index >= len(d.auths) {
		return nil, home.ErrAuthNotFound
	}
	auth := d.auths[index]
	return json.Marshal(struct {
		Concurrency homeConcurrencyTuple `json:"concurrency"`
		Model       string               `json:"model"`
		AuthIndex   string               `json:"auth_index"`
		Auth        Auth                 `json:"auth"`
	}{
		Concurrency: homeConcurrencyTuple{Accounted: true, CredentialID: auth.ID, Model: model},
		Model:       model,
		AuthIndex:   auth.ID,
		Auth:        auth,
	})
}
func (*accountedHomeExecutionDispatcher) AbortAmbiguousDispatch() {}

func TestAccountedHomeExecuteAndCountReleaseOnce(t *testing.T) {
	for _, countTokens := range []bool{false, true} {
		t.Run(map[bool]string{false: "Execute", true: "Count"}[countTokens], func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
			registry := executionregistry.New()
			releases := make(chan executionregistry.ReleaseGroup, 2)
			registry.SetReleaseSink(func(group executionregistry.ReleaseGroup, _ int64) { releases <- group })
			manager.PublishHomeDispatch(&accountedHomeExecutionDispatcher{auths: []Auth{{
				ID: "cred-1", Provider: "home-execution", Status: StatusActive,
			}}}, registry, 1)
			manager.RegisterExecutor(&homeExecutionExecutor{})

			var errExecute error
			if countTokens {
				_, errExecute = manager.ExecuteCount(context.Background(), []string{"home-execution"}, cliproxyexecutor.Request{Model: "model-a"}, cliproxyexecutor.Options{})
			} else {
				_, errExecute = manager.Execute(context.Background(), []string{"home-execution"}, cliproxyexecutor.Request{Model: "model-a"}, cliproxyexecutor.Options{})
			}
			if errExecute != nil {
				t.Fatalf("execution error = %v", errExecute)
			}
			select {
			case group := <-releases:
				if group != (executionregistry.ReleaseGroup{CredentialID: "cred-1", Model: "model-a"}) {
					t.Fatalf("release group = %#v", group)
				}
			default:
				t.Fatal("accounted selection did not release")
			}
			select {
			case group := <-releases:
				t.Fatalf("duplicate release = %#v", group)
			default:
			}
		})
	}
}

func TestAccountedHomeStreamEndsOnlyAfterSourceTerminates(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	registry := executionregistry.New()
	releases := make(chan executionregistry.ReleaseGroup, 1)
	registry.SetReleaseSink(func(group executionregistry.ReleaseGroup, _ int64) { releases <- group })
	manager.PublishHomeDispatch(&accountedHomeExecutionDispatcher{auths: []Auth{{
		ID: "cred-1", Provider: "home-execution", Status: StatusActive,
	}}}, registry, 1)
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("initial")}
	manager.RegisterExecutor(&homeExecutionStreamExecutor{chunks: chunks})

	result, errExecute := manager.ExecuteStream(context.Background(), []string{"home-execution"}, cliproxyexecutor.Request{Model: "model-a"}, cliproxyexecutor.Options{Stream: true})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}
	if _, ok := <-result.Chunks; !ok {
		t.Fatal("stream closed before initial chunk")
	}
	select {
	case group := <-releases:
		t.Fatalf("stream released before source termination: %#v", group)
	default:
	}

	close(chunks)
	for range result.Chunks {
	}
	select {
	case group := <-releases:
		if group != (executionregistry.ReleaseGroup{CredentialID: "cred-1", Model: "model-a"}) {
			t.Fatalf("release group = %#v", group)
		}
	case <-time.After(time.Second):
		t.Fatal("stream did not release after source termination")
	}
}

func TestAccountedHomeStreamErrorDrainsUntilSourceClosesBeforeRelease(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	registry := executionregistry.New()
	releases := make(chan executionregistry.ReleaseGroup, 1)
	registry.SetReleaseSink(func(group executionregistry.ReleaseGroup, _ int64) { releases <- group })
	manager.PublishHomeDispatch(&accountedHomeExecutionDispatcher{auths: []Auth{{
		ID: "cred-1", Provider: "home-execution", Status: StatusActive,
	}}}, registry, 1)
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("initial")}
	manager.RegisterExecutor(&homeExecutionStreamExecutor{chunks: chunks})

	result, errExecute := manager.ExecuteStream(context.Background(), []string{"home-execution"}, cliproxyexecutor.Request{Model: "model-a"}, cliproxyexecutor.Options{Stream: true})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}
	if chunk, ok := <-result.Chunks; !ok || string(chunk.Payload) != "initial" {
		t.Fatalf("initial chunk = %#v, open = %v", chunk, ok)
	}
	chunks <- cliproxyexecutor.StreamChunk{Err: &Error{HTTPStatus: http.StatusBadGateway, Message: "upstream failed"}}
	if chunk, ok := <-result.Chunks; !ok || chunk.Err == nil {
		t.Fatalf("error chunk = %#v, open = %v", chunk, ok)
	}

	sent := make(chan struct{})
	go func() {
		chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("after-error-1")}
		chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("after-error-2")}
		close(sent)
	}()
	select {
	case <-sent:
	case <-time.After(time.Second):
		t.Fatal("stream source was not drained after its error chunk")
	}
	select {
	case group := <-releases:
		t.Fatalf("stream released while source remained open: %#v", group)
	default:
	}
	select {
	case chunk, ok := <-result.Chunks:
		t.Fatalf("chunk after error = %#v, open = %v", chunk, ok)
	case <-time.After(50 * time.Millisecond):
	}

	close(chunks)
	for range result.Chunks {
	}
	select {
	case group := <-releases:
		if group != (executionregistry.ReleaseGroup{CredentialID: "cred-1", Model: "model-a"}) {
			t.Fatalf("release group = %#v", group)
		}
	case <-time.After(time.Second):
		t.Fatal("stream did not release after the source closed")
	}
}

func TestAccountedHomeStreamErrorCancellationReleasesSelection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	registry := executionregistry.New()
	releases := make(chan executionregistry.ReleaseGroup, 1)
	registry.SetReleaseSink(func(group executionregistry.ReleaseGroup, _ int64) { releases <- group })
	manager.PublishHomeDispatch(&accountedHomeExecutionDispatcher{auths: []Auth{{
		ID: "cred-1", Provider: "home-execution", Status: StatusActive,
	}}}, registry, 1)
	chunks := make(chan cliproxyexecutor.StreamChunk, 2)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("initial")}
	chunks <- cliproxyexecutor.StreamChunk{Err: &Error{HTTPStatus: http.StatusBadGateway, Message: "upstream failed"}}
	manager.RegisterExecutor(&homeExecutionStreamExecutor{chunks: chunks})

	result, errExecute := manager.ExecuteStream(ctx, []string{"home-execution"}, cliproxyexecutor.Request{Model: "model-a"}, cliproxyexecutor.Options{Stream: true})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}
	if _, ok := <-result.Chunks; !ok {
		t.Fatal("stream closed before initial chunk")
	}
	if chunk, ok := <-result.Chunks; !ok || chunk.Err == nil {
		t.Fatalf("error chunk = %#v, open = %v", chunk, ok)
	}
	select {
	case group := <-releases:
		t.Fatalf("stream released before cancellation: %#v", group)
	default:
	}

	cancel()
	for range result.Chunks {
	}
	select {
	case group := <-releases:
		if group != (executionregistry.ReleaseGroup{CredentialID: "cred-1", Model: "model-a"}) {
			t.Fatalf("release group = %#v", group)
		}
	case <-time.After(time.Second):
		t.Fatal("stream did not release after cancellation")
	}
	close(chunks)
}

func TestAccountedHomeStreamConsumerCancellationEndsSelection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	registry := executionregistry.New()
	releases := make(chan executionregistry.ReleaseGroup, 1)
	registry.SetReleaseSink(func(group executionregistry.ReleaseGroup, _ int64) { releases <- group })
	manager.PublishHomeDispatch(&accountedHomeExecutionDispatcher{auths: []Auth{{
		ID: "cred-1", Provider: "home-execution", Status: StatusActive,
	}}}, registry, 1)
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("initial")}
	manager.RegisterExecutor(&homeExecutionStreamExecutor{chunks: chunks})

	result, errExecute := manager.ExecuteStream(ctx, []string{"home-execution"}, cliproxyexecutor.Request{Model: "model-a"}, cliproxyexecutor.Options{Stream: true})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}
	cancel()
	for range result.Chunks {
	}
	select {
	case group := <-releases:
		if group != (executionregistry.ReleaseGroup{CredentialID: "cred-1", Model: "model-a"}) {
			t.Fatalf("release group = %#v", group)
		}
	case <-time.After(time.Second):
		t.Fatal("stream did not release after consumer cancellation")
	}
}

type retryingAccountedHomeExecutor struct{ calls atomic.Int32 }

func (*retryingAccountedHomeExecutor) Identifier() string { return "home-execution" }
func (e *retryingAccountedHomeExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e.calls.Add(1) == 1 {
		return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusBadGateway, Message: "upstream failed"}
	}
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}
func (*retryingAccountedHomeExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}
func (*retryingAccountedHomeExecutor) Refresh(context.Context, *Auth) (*Auth, error) { return nil, nil }
func (*retryingAccountedHomeExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (*retryingAccountedHomeExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestAccountedHomeRetrySelectsAndReleasesEveryAttempt(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	registry := executionregistry.New()
	releases := make(chan executionregistry.ReleaseGroup, 2)
	registry.SetReleaseSink(func(group executionregistry.ReleaseGroup, _ int64) { releases <- group })
	dispatcher := &accountedHomeExecutionDispatcher{auths: []Auth{
		{ID: "cred-1", Provider: "home-execution", Status: StatusActive},
		{ID: "cred-2", Provider: "home-execution", Status: StatusActive},
	}}
	manager.PublishHomeDispatch(dispatcher, registry, 1)
	executor := &retryingAccountedHomeExecutor{}
	manager.RegisterExecutor(executor)

	if _, errExecute := manager.Execute(context.Background(), []string{"home-execution"}, cliproxyexecutor.Request{Model: "model-a"}, cliproxyexecutor.Options{}); errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if got := dispatcher.calls.Load(); got != 2 {
		t.Fatalf("Home selections = %d, want 2", got)
	}
	if got := executor.calls.Load(); got != 2 {
		t.Fatalf("executor attempts = %d, want 2", got)
	}
	groups := map[executionregistry.ReleaseGroup]bool{}
	for range 2 {
		groups[<-releases] = true
	}
	for _, credentialID := range []string{"cred-1", "cred-2"} {
		if !groups[executionregistry.ReleaseGroup{CredentialID: credentialID, Model: "model-a"}] {
			t.Fatalf("missing release for %s: %#v", credentialID, groups)
		}
	}
}

func TestHomeStreamWithoutSourceEndsSelection(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	registry := executionregistry.New()
	manager.PublishHomeDispatch(&homePerSelectionDispatcher{auths: []Auth{{
		ID: "home-auth", Provider: "home-execution", Status: StatusActive,
	}}}, registry, 1)
	manager.RegisterExecutor(&missingHomeStreamSourceExecutor{})

	result, errExecute := manager.ExecuteStream(context.Background(), []string{"home-execution"}, cliproxyexecutor.Request{Model: "test"}, cliproxyexecutor.Options{Stream: true})
	if errExecute == nil {
		t.Fatalf("ExecuteStream() result = %#v, want error", result)
	}

	drainCtx, cancelDrain := context.WithTimeout(context.Background(), time.Second)
	defer cancelDrain()
	if errDrain := registry.Drain(drainCtx); errDrain != nil {
		t.Fatalf("Drain() error = %v", errDrain)
	}
}

// homeRequestMetadataSnapshot captures the client request metadata a context carries.
type homeRequestMetadataSnapshot struct {
	requestedModel  string
	reasoningEffort string
	serviceTier     string
	generate        bool
}

func homeRequestMetadataFromContext(ctx context.Context) homeRequestMetadataSnapshot {
	return homeRequestMetadataSnapshot{
		requestedModel:  coreusage.RequestedModelAliasFromContext(ctx),
		reasoningEffort: coreusage.ReasoningEffortFromContext(ctx),
		serviceTier:     coreusage.ServiceTierFromContext(ctx),
		generate:        coreusage.GenerateFromContext(ctx),
	}
}

// homeRequestMetadataExecutor records the metadata visible at auth preparation and execution.
type homeRequestMetadataExecutor struct {
	mu              sync.Mutex
	prepareMetadata homeRequestMetadataSnapshot
	executeMetadata homeRequestMetadataSnapshot
	// prepareErrOnce fails only the first preparation so Home redispatch still terminates.
	prepareErrOnce error
	executeErr     error
}

func (*homeRequestMetadataExecutor) Identifier() string { return "home-execution" }

func (*homeRequestMetadataExecutor) ShouldPrepareRequestAuth(*Auth) bool { return true }

func (e *homeRequestMetadataExecutor) PrepareRequestAuth(ctx context.Context, auth *Auth) (*Auth, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.prepareMetadata = homeRequestMetadataFromContext(ctx)
	if e.prepareErrOnce != nil {
		errPrepare := e.prepareErrOnce
		e.prepareErrOnce = nil
		return nil, errPrepare
	}
	return auth, nil
}

func (e *homeRequestMetadataExecutor) recordExecution(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.executeMetadata = homeRequestMetadataFromContext(ctx)
	return e.executeErr
}

func (e *homeRequestMetadataExecutor) snapshots() (homeRequestMetadataSnapshot, homeRequestMetadataSnapshot) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.prepareMetadata, e.executeMetadata
}

func (e *homeRequestMetadataExecutor) Execute(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if errExecute := e.recordExecution(ctx); errExecute != nil {
		return cliproxyexecutor.Response{}, errExecute
	}
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (e *homeRequestMetadataExecutor) ExecuteStream(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if errExecute := e.recordExecution(ctx); errExecute != nil {
		return nil, errExecute
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("ok")}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (*homeRequestMetadataExecutor) Refresh(context.Context, *Auth) (*Auth, error) {
	return nil, nil
}

func (e *homeRequestMetadataExecutor) CountTokens(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if errExecute := e.recordExecution(ctx); errExecute != nil {
		return cliproxyexecutor.Response{}, errExecute
	}
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (*homeRequestMetadataExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

// homeRequestMetadataHook buffers every Home result so a synchronous OnResult never blocks execution.
type homeRequestMetadataHook struct {
	results chan homeRequestMetadataSnapshot
}

func newHomeRequestMetadataHook() *homeRequestMetadataHook {
	return &homeRequestMetadataHook{results: make(chan homeRequestMetadataSnapshot, 8)}
}

func (*homeRequestMetadataHook) OnAuthRegistered(context.Context, *Auth) {}
func (*homeRequestMetadataHook) OnAuthUpdated(context.Context, *Auth)    {}
func (h *homeRequestMetadataHook) OnResult(ctx context.Context, _ Result) {
	select {
	case h.results <- homeRequestMetadataFromContext(ctx):
	default:
	}
}

func (h *homeRequestMetadataHook) awaitResult(t *testing.T) homeRequestMetadataSnapshot {
	t.Helper()
	select {
	case snapshot := <-h.results:
		return snapshot
	case <-time.After(time.Second):
		t.Fatal("Home result hook did not run")
		return homeRequestMetadataSnapshot{}
	}
}

func assertHomeRequestMetadata(t *testing.T, got homeRequestMetadataSnapshot, serviceTier string) {
	t.Helper()
	want := homeRequestMetadataSnapshot{
		requestedModel:  "client-model",
		reasoningEffort: "high",
		serviceTier:     serviceTier,
		generate:        false,
	}
	if got != want {
		t.Fatalf("request metadata = %#v, want %#v", got, want)
	}
}

func newHomeRequestMetadataManager(t *testing.T, executor *homeRequestMetadataExecutor, hook Hook) *Manager {
	t.Helper()
	manager := NewManager(nil, nil, hook)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.PublishHomeDispatch(homeExecutionDispatcher{}, executionregistry.New(), 1)
	manager.RegisterExecutor(executor)
	return manager
}

// homeRequestMetadataOptions mirrors handler-populated metadata. Handlers already derive the
// OpenAI "auto" default for an omitted tier (see sdk/api/handlers metadata tests); this layer
// only has to carry whatever the handler resolved.
func homeRequestMetadataOptions(serviceTier string) cliproxyexecutor.Options {
	return cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.RequestedModelMetadataKey:  "client-model",
		cliproxyexecutor.ReasoningEffortMetadataKey: "high",
		cliproxyexecutor.ServiceTierMetadataKey:     serviceTier,
		cliproxyexecutor.GenerateMetadataKey:        false,
	}}
}

type homeRequestMetadataPath struct {
	name string
	run  func(*Manager, cliproxyexecutor.Options) error
}

func homeExecuteMetadataPath() homeRequestMetadataPath {
	return homeRequestMetadataPath{
		name: "execute",
		run: func(manager *Manager, opts cliproxyexecutor.Options) error {
			_, errExecute := manager.Execute(context.Background(), []string{"home-execution"}, cliproxyexecutor.Request{Model: "route-model"}, opts)
			return errExecute
		},
	}
}

func homeCountMetadataPath() homeRequestMetadataPath {
	return homeRequestMetadataPath{
		name: "count_tokens",
		run: func(manager *Manager, opts cliproxyexecutor.Options) error {
			_, errCount := manager.ExecuteCount(context.Background(), []string{"home-execution"}, cliproxyexecutor.Request{Model: "route-model"}, opts)
			return errCount
		},
	}
}

func homeStreamMetadataPath() homeRequestMetadataPath {
	return homeRequestMetadataPath{
		name: "stream",
		run: func(manager *Manager, opts cliproxyexecutor.Options) error {
			opts.Stream = true
			result, errStream := manager.ExecuteStream(context.Background(), []string{"home-execution"}, cliproxyexecutor.Request{Model: "route-model"}, opts)
			if errStream != nil {
				return errStream
			}
			for range result.Chunks {
			}
			return nil
		},
	}
}

// TestHomeExecutionPropagatesRequestMetadata covers the Home regression from issue #4791: the
// executor context must carry the client request metadata at auth preparation, at execution, and
// in the Home result usage record.
func TestHomeExecutionPropagatesRequestMetadata(t *testing.T) {
	paths := []homeRequestMetadataPath{homeExecuteMetadataPath(), homeCountMetadataPath(), homeStreamMetadataPath()}

	for _, path := range paths {
		for _, serviceTier := range []string{"priority", coreusage.AutoServiceTier} {
			t.Run(path.name+"/"+serviceTier, func(t *testing.T) {
				executor := &homeRequestMetadataExecutor{}
				hook := newHomeRequestMetadataHook()
				manager := newHomeRequestMetadataManager(t, executor, hook)

				if errRun := path.run(manager, homeRequestMetadataOptions(serviceTier)); errRun != nil {
					t.Fatalf("execution error = %v", errRun)
				}
				prepareMetadata, executeMetadata := executor.snapshots()
				assertHomeRequestMetadata(t, prepareMetadata, serviceTier)
				assertHomeRequestMetadata(t, executeMetadata, serviceTier)
				assertHomeRequestMetadata(t, hook.awaitResult(t), serviceTier)
			})
		}
	}
}

// TestHomeExecutionFailureResultPreservesRequestMetadata keeps the requested tier authoritative in
// the failure usage record instead of falling back to the upstream or default tier.
func TestHomeExecutionFailureResultPreservesRequestMetadata(t *testing.T) {
	paths := []homeRequestMetadataPath{homeExecuteMetadataPath(), homeCountMetadataPath()}

	for _, path := range paths {
		t.Run(path.name, func(t *testing.T) {
			executor := &homeRequestMetadataExecutor{
				executeErr: &Error{HTTPStatus: http.StatusBadRequest, Message: "invalid request"},
			}
			hook := newHomeRequestMetadataHook()
			manager := newHomeRequestMetadataManager(t, executor, hook)

			if errRun := path.run(manager, homeRequestMetadataOptions("priority")); errRun == nil {
				t.Fatal("execution error = nil, want invalid request")
			}
			assertHomeRequestMetadata(t, hook.awaitResult(t), "priority")
		})
	}
}

// TestHomePrepareFailureResultPreservesRequestMetadata covers the prepare_failed Home result paths,
// which report usage before any executor call happens.
func TestHomePrepareFailureResultPreservesRequestMetadata(t *testing.T) {
	paths := []homeRequestMetadataPath{homeExecuteMetadataPath(), homeCountMetadataPath(), homeStreamMetadataPath()}

	for _, path := range paths {
		t.Run(path.name, func(t *testing.T) {
			executor := &homeRequestMetadataExecutor{
				prepareErrOnce: &Error{Code: "prepare_failed", Message: "prepare failed"},
			}
			hook := newHomeRequestMetadataHook()
			manager := newHomeRequestMetadataManager(t, executor, hook)

			_ = path.run(manager, homeRequestMetadataOptions("priority"))
			prepareMetadata, _ := executor.snapshots()
			assertHomeRequestMetadata(t, prepareMetadata, "priority")
			assertHomeRequestMetadata(t, hook.awaitResult(t), "priority")
		})
	}
}
