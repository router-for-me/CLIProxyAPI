package auth

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type serverToolTestExecutor struct {
	prepareCalls   atomic.Int32
	executeCalls   atomic.Int32
	streamCalls    atomic.Int32
	executePayload []byte
	executeHeaders http.Header
	streamPayload  []byte
	streamHeaders  http.Header
}

func (e *serverToolTestExecutor) Identifier() string { return "antigravity" }

func (e *serverToolTestExecutor) ShouldPrepareRequestAuth(auth *Auth) bool {
	return auth == nil || auth.Metadata == nil || testStringValue(auth.Metadata["project_id"]) == ""
}

func (e *serverToolTestExecutor) PrepareRequestAuth(_ context.Context, auth *Auth) (*Auth, error) {
	e.prepareCalls.Add(1)
	updated := auth.Clone()
	if updated.Metadata == nil {
		updated.Metadata = make(map[string]any)
	}
	updated.Metadata["project_id"] = "prepared-project"
	return updated, nil
}

func (e *serverToolTestExecutor) Execute(_ context.Context, _ *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.executeCalls.Add(1)
	e.executePayload = append([]byte(nil), req.Payload...)
	e.executeHeaders = reqHeaderClone(opts.Headers)
	return cliproxyexecutor.Response{Payload: []byte("native")}, nil
}

func (e *serverToolTestExecutor) ExecuteStream(_ context.Context, _ *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.streamCalls.Add(1)
	e.streamPayload = append([]byte(nil), req.Payload...)
	e.streamHeaders = reqHeaderClone(opts.Headers)
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("native-stream")}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *serverToolTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *serverToolTestExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "count not implemented"}
}

func (e *serverToolTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "http not implemented"}
}

func reqHeaderClone(src http.Header) http.Header {
	out := make(http.Header, len(src))
	for key, values := range src {
		out[key] = append([]string(nil), values...)
	}
	return out
}

type fakePluginServerToolHandler struct {
	resp        pluginapi.ServerToolResponse
	streamResp  pluginapi.ServerToolStreamResponse
	handled     bool
	err         error
	calls       int
	streamCalls int
	requests    []pluginapi.ServerToolRequest
}

func (h *fakePluginServerToolHandler) HandleServerTool(ctx context.Context, req pluginapi.ServerToolRequest) (pluginapi.ServerToolResponse, bool, error) {
	h.calls++
	h.requests = append(h.requests, req)
	return h.resp, h.handled, h.err
}

func (h *fakePluginServerToolHandler) HandleServerToolStream(ctx context.Context, req pluginapi.ServerToolRequest) (pluginapi.ServerToolStreamResponse, bool, error) {
	h.streamCalls++
	h.requests = append(h.requests, req)
	return h.streamResp, h.handled, h.err
}

func (h *fakePluginServerToolHandler) HasServerToolHandler() bool {
	return true
}

type recordingHook struct {
	mu      sync.Mutex
	results []Result
}

func (h *recordingHook) OnAuthRegistered(context.Context, *Auth) {}
func (h *recordingHook) OnAuthUpdated(context.Context, *Auth)    {}
func (h *recordingHook) OnResult(_ context.Context, result Result) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.results = append(h.results, result)
}

func (h *recordingHook) lastResult() (Result, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.results) == 0 {
		return Result{}, false
	}
	return h.results[len(h.results)-1], true
}

func TestManagerExecuteServerToolHandledSkipsNativeExecutorAfterPrepare(t *testing.T) {
	const model = "gemini-3.5-flash"
	executor := &serverToolTestExecutor{}
	hook := &recordingHook{}
	manager := NewManager(nil, nil, hook)
	manager.RegisterExecutor(executor)
	handler := &fakePluginServerToolHandler{
		handled: true,
		resp: pluginapi.ServerToolResponse{
			Handled: true,
			Headers: map[string][]string{"Content-Type": {"application/json"}},
			Payload: []byte(`{"type":"message"}`),
		},
	}
	manager.SetPluginServerToolHandler(handler)
	registerServerToolTestAuth(t, manager, model)

	resp, errExecute := manager.Execute(context.Background(), []string{"antigravity"}, serverToolRequest(model), serverToolOptions(false))
	if errExecute != nil {
		t.Fatalf("Execute error: %v", errExecute)
	}
	if string(resp.Payload) != `{"type":"message"}` {
		t.Fatalf("payload = %q, want plugin response", resp.Payload)
	}
	if got := executor.executeCalls.Load(); got != 0 {
		t.Fatalf("native execute calls = %d, want 0", got)
	}
	if got := executor.prepareCalls.Load(); got != 1 {
		t.Fatalf("prepare calls = %d, want 1", got)
	}
	if handler.calls != 1 {
		t.Fatalf("handler calls = %d, want 1", handler.calls)
	}
	gotReq := handler.requests[0]
	if gotReq.Provider != "antigravity" || gotReq.AuthProvider != "antigravity" ||
		gotReq.RouteModel != model || gotReq.UpstreamModel != model ||
		gotReq.SourceFormat != sdktranslator.FormatClaude.String() {
		t.Fatalf("server tool request = %#v", gotReq)
	}
	if got := testStringValue(gotReq.AuthMetadata["project_id"]); got != "prepared-project" {
		t.Fatalf("handler project_id = %q, want prepared-project", got)
	}
	result, ok := hook.lastResult()
	if !ok || !result.Success || result.Provider != "antigravity" || result.Model != model {
		t.Fatalf("last result = %#v, ok = %v; want plugin success for %s", result, ok, model)
	}
}

func TestManagerExecuteServerToolUnhandledFallsBackToNativeExecutor(t *testing.T) {
	const model = "gemini-3.5-flash"
	executor := &serverToolTestExecutor{}
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	handler := &fakePluginServerToolHandler{handled: false, resp: pluginapi.ServerToolResponse{Handled: false}}
	manager.SetPluginServerToolHandler(handler)
	registerServerToolTestAuth(t, manager, model)

	resp, errExecute := manager.Execute(context.Background(), []string{"antigravity"}, serverToolRequest(model), serverToolOptions(false))
	if errExecute != nil {
		t.Fatalf("Execute error: %v", errExecute)
	}
	if string(resp.Payload) != "native" {
		t.Fatalf("payload = %q, want native", resp.Payload)
	}
	if got := executor.executeCalls.Load(); got != 1 {
		t.Fatalf("native execute calls = %d, want 1", got)
	}
	if handler.calls != 1 {
		t.Fatalf("handler calls = %d, want 1", handler.calls)
	}
}

func TestManagerExecuteServerToolUsesPostAuthInterceptedRequest(t *testing.T) {
	const model = "gemini-3.5-flash"
	executor := &serverToolTestExecutor{}
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	handler := &fakePluginServerToolHandler{
		handled: true,
		resp: pluginapi.ServerToolResponse{
			Handled: true,
			Payload: []byte(`{"type":"message"}`),
		},
	}
	manager.SetPluginServerToolHandler(handler)
	registerServerToolTestAuth(t, manager, model)

	opts := serverToolOptions(false)
	opts.RequestAfterAuthInterceptor = func(_ context.Context, req cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
		if string(req.Body) == "" {
			t.Fatal("post-auth interceptor body was empty")
		}
		return cliproxyexecutor.RequestAfterAuthInterceptResponse{
			Headers: http.Header{"X-Post-Auth": {"1"}},
			Body:    []byte(`{"tools":[{"type":"web_search_20250305","name":"web_search"}],"patched":true}`),
		}
	}

	if _, errExecute := manager.Execute(context.Background(), []string{"antigravity"}, serverToolRequest(model), opts); errExecute != nil {
		t.Fatalf("Execute error: %v", errExecute)
	}
	if handler.calls != 1 {
		t.Fatalf("handler calls = %d, want 1", handler.calls)
	}
	gotReq := handler.requests[0]
	if string(gotReq.Payload) != `{"tools":[{"type":"web_search_20250305","name":"web_search"}],"patched":true}` {
		t.Fatalf("server tool payload = %q", gotReq.Payload)
	}
	if got := gotReq.Headers.Get("X-Post-Auth"); got != "1" {
		t.Fatalf("server tool X-Post-Auth = %q, want 1", got)
	}
	if got := executor.executeCalls.Load(); got != 0 {
		t.Fatalf("native execute calls = %d, want 0", got)
	}
}

func TestManagerExecuteServerToolUnhandledNativeUsesPostAuthInterceptedRequest(t *testing.T) {
	const model = "gemini-3.5-flash"
	executor := &serverToolTestExecutor{}
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	handler := &fakePluginServerToolHandler{handled: false, resp: pluginapi.ServerToolResponse{Handled: false}}
	manager.SetPluginServerToolHandler(handler)
	registerServerToolTestAuth(t, manager, model)

	opts := serverToolOptions(false)
	opts.RequestAfterAuthInterceptor = func(context.Context, cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
		return cliproxyexecutor.RequestAfterAuthInterceptResponse{
			Headers: http.Header{"X-Post-Auth": {"fallback"}},
			Body:    []byte(`{"patched":"native"}`),
		}
	}

	if _, errExecute := manager.Execute(context.Background(), []string{"antigravity"}, serverToolRequest(model), opts); errExecute != nil {
		t.Fatalf("Execute error: %v", errExecute)
	}
	if string(executor.executePayload) != `{"patched":"native"}` {
		t.Fatalf("native payload = %q", executor.executePayload)
	}
	if got := executor.executeHeaders.Get("X-Post-Auth"); got != "fallback" {
		t.Fatalf("native X-Post-Auth = %q, want fallback", got)
	}
}

func TestManagerExecuteStreamServerToolHandledUsesStreamWrapper(t *testing.T) {
	const model = "gemini-3.5-flash"
	executor := &serverToolTestExecutor{}
	hook := &recordingHook{}
	manager := NewManager(nil, nil, hook)
	manager.RegisterExecutor(executor)
	chunks := make(chan pluginapi.ServerToolStreamChunk, 2)
	chunks <- pluginapi.ServerToolStreamChunk{Payload: []byte("event: message_start\n\n")}
	chunks <- pluginapi.ServerToolStreamChunk{Payload: []byte("event: message_stop\n\n")}
	close(chunks)
	handler := &fakePluginServerToolHandler{
		handled: true,
		streamResp: pluginapi.ServerToolStreamResponse{
			Handled: true,
			Headers: map[string][]string{"Content-Type": {"text/event-stream"}},
			Chunks:  chunks,
		},
	}
	manager.SetPluginServerToolHandler(handler)
	registerServerToolTestAuth(t, manager, model)

	streamResult, errExecute := manager.ExecuteStream(context.Background(), []string{"antigravity"}, serverToolRequest(model), serverToolOptions(true))
	if errExecute != nil {
		t.Fatalf("ExecuteStream error: %v", errExecute)
	}
	var payload string
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
		payload += string(chunk.Payload)
	}
	if payload != "event: message_start\n\nevent: message_stop\n\n" {
		t.Fatalf("stream payload = %q", payload)
	}
	if got := executor.streamCalls.Load(); got != 0 {
		t.Fatalf("native stream calls = %d, want 0", got)
	}
	if handler.streamCalls != 1 {
		t.Fatalf("handler stream calls = %d, want 1", handler.streamCalls)
	}
	result, ok := hook.lastResult()
	if !ok || !result.Success || result.Provider != "antigravity" || result.Model != model {
		t.Fatalf("last result = %#v, ok = %v; want stream success for %s", result, ok, model)
	}
}

func TestManagerExecuteServerToolAddsAntigravityWebSearchMetadata(t *testing.T) {
	const model = "gemini-3.5-flash"
	executor := &serverToolTestExecutor{}
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	handler := &fakePluginServerToolHandler{
		handled: true,
		resp: pluginapi.ServerToolResponse{
			Handled: true,
			Payload: []byte(`{"type":"message"}`),
		},
	}
	manager.SetPluginServerToolHandler(handler)
	registerServerToolTestAuth(t, manager, model)
	registry.GetGlobalRegistry().RegisterClient("auth-server-tool-websearch", "antigravity", []*registry.ModelInfo{
		{ID: "gemini-3.1-flash-lite", SupportsWebSearch: true},
	})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("auth-server-tool-websearch") })

	if _, errExecute := manager.Execute(context.Background(), []string{"antigravity"}, serverToolRequest(model), serverToolOptions(false)); errExecute != nil {
		t.Fatalf("Execute error: %v", errExecute)
	}
	if handler.calls != 1 {
		t.Fatalf("handler calls = %d, want 1", handler.calls)
	}
	gotReq := handler.requests[0]
	if got := gotReq.Metadata["request_id"]; got != "req-1" {
		t.Fatalf("request_id metadata = %#v, want req-1", got)
	}
	if got := gotReq.Metadata["client"]; got != "claude-code" {
		t.Fatalf("client metadata = %#v, want claude-code", got)
	}
	if got := gotReq.Metadata["antigravity_web_search_backend_model"]; got != "gemini-3.1-flash-lite" {
		t.Fatalf("antigravity_web_search_backend_model = %#v, want gemini-3.1-flash-lite", got)
	}
	modelIDs, ok := gotReq.Metadata["antigravity_web_search_model_ids"].([]string)
	if !ok {
		t.Fatalf("antigravity_web_search_model_ids type = %T, want []string", gotReq.Metadata["antigravity_web_search_model_ids"])
	}
	for _, modelID := range modelIDs {
		if modelID == "gemini-3.1-flash-lite" {
			return
		}
	}
	t.Fatalf("antigravity_web_search_model_ids = %#v, want gemini-3.1-flash-lite", modelIDs)
}

func registerServerToolTestAuth(t *testing.T, manager *Manager, model string) {
	t.Helper()
	auth := &Auth{
		ID:       "auth-server-tool",
		Provider: "antigravity",
		Metadata: map[string]any{"access_token": "token"},
	}
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, "antigravity", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
}

func serverToolRequest(model string) cliproxyexecutor.Request {
	return cliproxyexecutor.Request{
		Model:   model,
		Format:  sdktranslator.FormatClaude,
		Payload: []byte(`{"tools":[{"type":"web_search_20250305","name":"web_search"}]}`),
		Metadata: map[string]any{
			"request_id": "req-1",
		},
	}
}

func serverToolOptions(stream bool) cliproxyexecutor.Options {
	return cliproxyexecutor.Options{
		Stream:          stream,
		Headers:         http.Header{"Anthropic-Version": {"2023-06-01"}},
		OriginalRequest: []byte(`{"model":"gemini-3.5-flash"}`),
		SourceFormat:    sdktranslator.FormatClaude,
		Metadata: map[string]any{
			"client": "claude-code",
		},
	}
}
