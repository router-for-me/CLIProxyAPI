package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type modelExecutionCaptureExecutor struct {
	provider string

	mu          sync.Mutex
	lastRequest coreexecutor.Request
	lastOptions coreexecutor.Options
	execute     func(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error)
	stream      func(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error)
}

type modelExecutionStatusHeaderError struct {
	statusCode int
	message    string
	headers    http.Header
}

func (e modelExecutionStatusHeaderError) Error() string {
	return e.message
}

func (e modelExecutionStatusHeaderError) StatusCode() int {
	return e.statusCode
}

func (e modelExecutionStatusHeaderError) Headers() http.Header {
	return e.headers
}

func (e *modelExecutionCaptureExecutor) Identifier() string {
	if e.provider != "" {
		return e.provider
	}
	return "codex"
}

func (e *modelExecutionCaptureExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	e.capture(req, opts)
	if e.execute != nil {
		return e.execute(ctx, auth, req, opts)
	}
	return coreexecutor.Response{Payload: []byte("model-execution-ok")}, nil
}

func (e *modelExecutionCaptureExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.capture(req, opts)
	if e.stream != nil {
		return e.stream(ctx, auth, req, opts)
	}
	chunks := make(chan coreexecutor.StreamChunk)
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *modelExecutionCaptureExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *modelExecutionCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{Payload: []byte("0")}, nil
}

func (e *modelExecutionCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, &coreauth.Error{Code: "not_implemented", Message: "HttpRequest not implemented", HTTPStatus: http.StatusNotImplemented}
}

func (e *modelExecutionCaptureExecutor) capture(req coreexecutor.Request, opts coreexecutor.Options) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.lastRequest = coreexecutor.Request{
		Model:    req.Model,
		Payload:  cloneBytes(req.Payload),
		Format:   req.Format,
		Metadata: req.Metadata,
	}
	e.lastOptions = coreexecutor.Options{
		Stream:          opts.Stream,
		Alt:             opts.Alt,
		Headers:         cloneHeader(opts.Headers),
		Query:           cloneURLValues(opts.Query),
		OriginalRequest: cloneBytes(opts.OriginalRequest),
		SourceFormat:    opts.SourceFormat,
		ResponseFormat:  opts.ResponseFormat,
		Metadata:        opts.Metadata,
	}
}

func (e *modelExecutionCaptureExecutor) captured() (coreexecutor.Request, coreexecutor.Options) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastRequest, e.lastOptions
}

func newModelExecutionHandler(t *testing.T, model string, executor *modelExecutionCaptureExecutor, cfg *sdkconfig.SDKConfig) *BaseAPIHandler {
	t.Helper()
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{
		ID:       "model-execution-" + model,
		Provider: executor.Identifier(),
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"email": model + "@example.com"},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("manager.Register(): %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})
	return NewBaseAPIHandlers(cfg, manager)
}

func TestExecuteModelCarriesEntryAndExitProtocols(t *testing.T) {
	model := "model-execution-nonstream-model"
	requestBody := []byte(fmt.Sprintf(`{"model":%q}`, model))
	executor := &modelExecutionCaptureExecutor{
		execute: func(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
			return coreexecutor.Response{
				Payload: []byte(`{"ok":true}`),
				Headers: http.Header{
					"X-Upstream": []string{"nonstream"},
				},
			}, nil
		},
	}
	handler := newModelExecutionHandler(t, model, executor, &sdkconfig.SDKConfig{PassthroughHeaders: true})

	resp, errMsg := handler.ExecuteModel(context.Background(), ModelExecutionRequest{
		EntryProtocol: "openai",
		ExitProtocol:  "claude",
		Model:         model,
		Body:          requestBody,
		Headers:       http.Header{"X-Callback": []string{"nonstream"}},
		Query:         url.Values{"q": []string{"callback"}},
	})
	if errMsg != nil {
		t.Fatalf("ExecuteModel() error = %+v", errMsg)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if string(resp.Body) != `{"ok":true}` {
		t.Fatalf("body = %q, want executor response", resp.Body)
	}
	if resp.Headers.Get("X-Upstream") != "nonstream" {
		t.Fatalf("headers = %#v, want upstream header", resp.Headers)
	}

	gotReq, gotOpts := executor.captured()
	if gotReq.Model != model {
		t.Fatalf("executor model = %q, want %q", gotReq.Model, model)
	}
	if string(gotReq.Payload) != string(requestBody) {
		t.Fatalf("executor payload = %q, want %q", gotReq.Payload, requestBody)
	}
	if gotOpts.Stream {
		t.Fatal("executor stream option = true, want false")
	}
	if gotOpts.SourceFormat != sdktranslator.FormatOpenAI {
		t.Fatalf("SourceFormat = %q, want %q", gotOpts.SourceFormat, sdktranslator.FormatOpenAI)
	}
	if gotOpts.ResponseFormat != sdktranslator.FormatClaude {
		t.Fatalf("ResponseFormat = %q, want %q", gotOpts.ResponseFormat, sdktranslator.FormatClaude)
	}
	if gotOpts.Metadata[coreexecutor.RequestedModelMetadataKey] != model {
		t.Fatalf("requested model metadata = %#v, want %q", gotOpts.Metadata[coreexecutor.RequestedModelMetadataKey], model)
	}
	if gotOpts.Metadata[modelExecutionMetadataSourceKey] != modelExecutionInternalSource {
		t.Fatalf("source metadata = %#v, want %q", gotOpts.Metadata[modelExecutionMetadataSourceKey], modelExecutionInternalSource)
	}
	if gotOpts.Headers.Get("X-Callback") != "nonstream" {
		t.Fatalf("executor headers = %#v, want callback header", gotOpts.Headers)
	}
	if gotOpts.Query.Get("q") != "callback" {
		t.Fatalf("executor query = %#v, want callback query", gotOpts.Query)
	}
}

func TestExecuteModelStream(t *testing.T) {
	model := "model-execution-stream-model"
	requestBody := []byte(fmt.Sprintf(`{"model":%q,"stream":true}`, model))
	executor := &modelExecutionCaptureExecutor{
		stream: func(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
			chunks := make(chan coreexecutor.StreamChunk, 1)
			chunks <- coreexecutor.StreamChunk{Payload: []byte("stream-one")}
			close(chunks)
			return &coreexecutor.StreamResult{
				Headers: http.Header{"X-Upstream": []string{"stream"}},
				Chunks:  chunks,
			}, nil
		},
	}
	handler := newModelExecutionHandler(t, model, executor, &sdkconfig.SDKConfig{PassthroughHeaders: true})

	stream, errMsg := handler.ExecuteModelStream(context.Background(), ModelExecutionRequest{
		EntryProtocol: "openai",
		ExitProtocol:  "claude",
		Model:         model,
		Stream:        true,
		Body:          requestBody,
		Headers:       http.Header{"X-Callback": []string{"stream"}},
	})
	if errMsg != nil {
		t.Fatalf("ExecuteModelStream() error = %+v", errMsg)
	}
	if stream.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", stream.StatusCode, http.StatusOK)
	}
	if stream.Headers.Get("X-Upstream") != "stream" {
		t.Fatalf("headers = %#v, want upstream header", stream.Headers)
	}
	chunk, ok := <-stream.Chunks
	if !ok {
		t.Fatal("stream chunks closed before payload")
	}
	if chunk.Err != nil {
		t.Fatalf("stream chunk error = %+v", chunk.Err)
	}
	if string(chunk.Payload) != "stream-one" {
		t.Fatalf("stream chunk payload = %q, want stream-one", chunk.Payload)
	}
	if chunk, ok = <-stream.Chunks; ok {
		t.Fatalf("unexpected extra stream chunk: %+v", chunk)
	}

	gotReq, gotOpts := executor.captured()
	if gotReq.Model != model {
		t.Fatalf("executor model = %q, want %q", gotReq.Model, model)
	}
	if string(gotReq.Payload) != string(requestBody) {
		t.Fatalf("executor payload = %q, want %q", gotReq.Payload, requestBody)
	}
	if !gotOpts.Stream {
		t.Fatal("executor stream option = false, want true")
	}
	if gotOpts.SourceFormat != sdktranslator.FormatOpenAI {
		t.Fatalf("SourceFormat = %q, want %q", gotOpts.SourceFormat, sdktranslator.FormatOpenAI)
	}
	if gotOpts.ResponseFormat != sdktranslator.FormatClaude {
		t.Fatalf("ResponseFormat = %q, want %q", gotOpts.ResponseFormat, sdktranslator.FormatClaude)
	}
	if gotOpts.Metadata[coreexecutor.RequestedModelMetadataKey] != model {
		t.Fatalf("requested model metadata = %#v, want %q", gotOpts.Metadata[coreexecutor.RequestedModelMetadataKey], model)
	}
	if gotOpts.Metadata[modelExecutionMetadataSourceKey] != modelExecutionInternalSource {
		t.Fatalf("source metadata = %#v, want %q", gotOpts.Metadata[modelExecutionMetadataSourceKey], modelExecutionInternalSource)
	}
	if gotOpts.Headers.Get("X-Callback") != "stream" {
		t.Fatalf("executor headers = %#v, want callback header", gotOpts.Headers)
	}
}

func TestExecuteModelStreamStartupError(t *testing.T) {
	model := "model-execution-stream-startup-error-model"
	requestBody := []byte(fmt.Sprintf(`{"model":%q,"stream":true}`, model))
	executor := &modelExecutionCaptureExecutor{
		stream: func(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
			chunks := make(chan coreexecutor.StreamChunk, 1)
			chunks <- coreexecutor.StreamChunk{Err: fmt.Errorf("startup failed")}
			close(chunks)
			return &coreexecutor.StreamResult{Chunks: chunks}, nil
		},
	}
	handler := newModelExecutionHandler(t, model, executor, &sdkconfig.SDKConfig{})

	stream, errMsg := handler.ExecuteModelStream(context.Background(), ModelExecutionRequest{
		EntryProtocol: "openai",
		ExitProtocol:  "claude",
		Model:         model,
		Stream:        true,
		Body:          requestBody,
	})
	if errMsg == nil {
		t.Fatal("ExecuteModelStream() error = nil, want startup error")
	}
	if errMsg.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", errMsg.StatusCode, http.StatusInternalServerError)
	}
	if errMsg.Error == nil || errMsg.Error.Error() != "startup failed" {
		t.Fatalf("error = %v, want startup failed", errMsg.Error)
	}
	if stream.Chunks != nil {
		t.Fatal("stream chunks created for startup error")
	}
}

func TestExecuteModelStreamTerminalError(t *testing.T) {
	model := "model-execution-stream-terminal-error-model"
	requestBody := []byte(fmt.Sprintf(`{"model":%q,"stream":true}`, model))
	errorHeaders := http.Header{"X-Stream-Error": []string{"terminal"}}
	executor := &modelExecutionCaptureExecutor{
		stream: func(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
			chunks := make(chan coreexecutor.StreamChunk, 2)
			chunks <- coreexecutor.StreamChunk{Payload: []byte("stream-before-error")}
			chunks <- coreexecutor.StreamChunk{Err: modelExecutionStatusHeaderError{
				statusCode: http.StatusTooManyRequests,
				message:    "rate limited",
				headers:    errorHeaders,
			}}
			close(chunks)
			return &coreexecutor.StreamResult{Chunks: chunks}, nil
		},
	}
	handler := newModelExecutionHandler(t, model, executor, &sdkconfig.SDKConfig{})

	stream, errMsg := handler.ExecuteModelStream(context.Background(), ModelExecutionRequest{
		EntryProtocol: "openai",
		ExitProtocol:  "claude",
		Model:         model,
		Stream:        true,
		Body:          requestBody,
	})
	if errMsg != nil {
		t.Fatalf("ExecuteModelStream() error = %+v", errMsg)
	}

	chunk, ok := <-stream.Chunks
	if !ok {
		t.Fatal("stream chunks closed before payload")
	}
	if chunk.Err != nil {
		t.Fatalf("first stream chunk error = %+v", chunk.Err)
	}
	if string(chunk.Payload) != "stream-before-error" {
		t.Fatalf("first stream chunk payload = %q, want stream-before-error", chunk.Payload)
	}

	chunk, ok = <-stream.Chunks
	if !ok {
		t.Fatal("stream chunks closed before terminal error")
	}
	if len(chunk.Payload) != 0 {
		t.Fatalf("terminal stream chunk payload = %q, want empty", chunk.Payload)
	}
	if chunk.Err == nil {
		t.Fatal("terminal stream chunk error = nil")
	}
	if chunk.Err.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("terminal status = %d, want %d", chunk.Err.StatusCode, http.StatusTooManyRequests)
	}
	if chunk.Err.Message != "rate limited" {
		t.Fatalf("terminal message = %q, want rate limited", chunk.Err.Message)
	}
	if chunk.Err.Error() != "rate limited" {
		t.Fatalf("terminal Error() = %q, want rate limited", chunk.Err.Error())
	}
	if chunk.Err.Headers.Get("X-Stream-Error") != "terminal" {
		t.Fatalf("terminal headers = %#v, want stream error header", chunk.Err.Headers)
	}
	if chunk, ok = <-stream.Chunks; ok {
		t.Fatalf("unexpected extra stream chunk: %+v", chunk)
	}
}

func TestExecuteModelStreamContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	dataChan := make(chan []byte)
	errChan := make(chan *interfaces.ErrorMessage)
	chunks := wrapModelExecutionChunks(ctx, dataChan, errChan, nil)

	cancel()

	timeout := time.NewTimer(time.Second)
	defer timeout.Stop()
	select {
	case chunk, ok := <-chunks:
		if ok {
			t.Fatalf("stream chunks yielded after cancel: %+v", chunk)
		}
	case <-timeout.C:
		t.Fatal("stream chunks did not close after context cancellation")
	}
}
