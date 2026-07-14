package openai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

type delayedResponsesStreamExecutor struct {
	release <-chan struct{}
	headers http.Header
	payload []byte
}

func (e *delayedResponsesStreamExecutor) Identifier() string { return "test-provider" }

func (e *delayedResponsesStreamExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *delayedResponsesStreamExecutor) ExecuteStream(ctx context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	chunks := make(chan coreexecutor.StreamChunk)
	go func() {
		defer close(chunks)
		select {
		case <-ctx.Done():
		case <-e.release:
			if len(e.payload) > 0 {
				select {
				case <-ctx.Done():
				case chunks <- coreexecutor.StreamChunk{Payload: e.payload}:
				}
			}
		}
	}()
	return &coreexecutor.StreamResult{Headers: e.headers.Clone(), Chunks: chunks}, nil
}

func (e *delayedResponsesStreamExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *delayedResponsesStreamExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *delayedResponsesStreamExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

type responsesStreamInterceptorHost struct {
	interceptStreamChunk func(context.Context, pluginapi.StreamChunkInterceptRequest) pluginapi.StreamChunkInterceptResponse
}

func (h *responsesStreamInterceptorHost) InterceptRequestBeforeAuth(_ context.Context, req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	return pluginapi.RequestInterceptResponse{Headers: req.Headers.Clone(), Body: append([]byte(nil), req.Body...)}
}

func (h *responsesStreamInterceptorHost) InterceptRequestAfterAuth(_ context.Context, req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	return pluginapi.RequestInterceptResponse{Headers: req.Headers.Clone(), Body: append([]byte(nil), req.Body...)}
}

func (h *responsesStreamInterceptorHost) InterceptResponse(_ context.Context, req pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
	return pluginapi.ResponseInterceptResponse{Headers: req.ResponseHeaders.Clone(), Body: append([]byte(nil), req.Body...)}
}

func (h *responsesStreamInterceptorHost) InterceptStreamChunk(ctx context.Context, req pluginapi.StreamChunkInterceptRequest) pluginapi.StreamChunkInterceptResponse {
	if h != nil && h.interceptStreamChunk != nil {
		return h.interceptStreamChunk(ctx, req)
	}
	return pluginapi.StreamChunkInterceptResponse{Headers: req.ResponseHeaders.Clone(), Body: append([]byte(nil), req.Body...)}
}

func (h *responsesStreamInterceptorHost) HasRequestInterceptors() bool { return false }

func (h *responsesStreamInterceptorHost) HasStreamInterceptors() bool {
	return h != nil && h.interceptStreamChunk != nil
}

func newDelayedResponsesStreamServer(t *testing.T, cfg *sdkconfig.SDKConfig, upstreamHeaders http.Header, pluginHost handlers.PluginInterceptorHost) (*httptest.Server, func(), string) {
	t.Helper()

	gin.SetMode(gin.TestMode)
	releaseChan := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			close(releaseChan)
		})
	}
	executor := &delayedResponsesStreamExecutor{
		release: releaseChan,
		headers: upstreamHeaders,
		payload: []byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-keepalive\",\"output\":[]}}\n\n"),
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	authID := t.Name() + "-auth"
	auth := &coreauth.Auth{ID: authID, Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	modelID := t.Name() + "-model"
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: modelID}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(cfg, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	h.SetPluginHost(pluginHost)
	router := gin.New()
	router.POST("/v1/responses", h.Responses)
	return httptest.NewServer(router), release, modelID
}

func newResponsesStreamTestHandler(t *testing.T) (*OpenAIResponsesAPIHandler, *httptest.ResponseRecorder, *gin.Context, http.Flusher) {
	t.Helper()

	gin.SetMode(gin.TestMode)
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	h := NewOpenAIResponsesAPIHandler(base)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		t.Fatalf("expected gin writer to implement http.Flusher")
	}

	return h, recorder, c, flusher
}

func TestResponsesStreamingSendsConfiguredKeepAliveBeforeFirstChunk(t *testing.T) {
	testCases := []struct {
		name       string
		pluginHost handlers.PluginInterceptorHost
	}{
		{name: "without_plugin_host"},
		{name: "with_inactive_plugin_host", pluginHost: &responsesStreamInterceptorHost{}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &sdkconfig.SDKConfig{Streaming: sdkconfig.StreamingConfig{KeepAliveSeconds: 1}}
			server, release, modelID := newDelayedResponsesStreamServer(t, cfg, nil, tc.pluginHost)
			defer server.Close()
			defer release()

			req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/responses", strings.NewReader(`{"model":"`+modelID+`","stream":true,"input":"hello"}`))
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			req.Header.Set("Content-Type", "application/json")
			client := server.Client()
			client.Timeout = 2500 * time.Millisecond

			started := time.Now()
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("stream did not start before the first upstream chunk: %v", err)
			}
			if elapsed := time.Since(started); elapsed >= 2*time.Second {
				t.Fatalf("stream headers took %s, want less than 2s", elapsed)
			}
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
			}
			if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
				t.Fatalf("Content-Type = %q, want text/event-stream", got)
			}

			release()
			body, err := io.ReadAll(resp.Body)
			if errClose := resp.Body.Close(); err == nil && errClose != nil {
				err = errClose
			}
			if err != nil {
				t.Fatalf("read response body: %v", err)
			}
			if !strings.HasPrefix(string(body), ": keep-alive\n\n") {
				t.Fatalf("body = %q, want pre-first-chunk keep-alive", body)
			}
		})
	}
}

func TestResponsesStreamingPreservesPassthroughHeadersBeforeKeepAlive(t *testing.T) {
	cfg := &sdkconfig.SDKConfig{
		PassthroughHeaders: true,
		Streaming:          sdkconfig.StreamingConfig{KeepAliveSeconds: 1},
	}
	server, release, modelID := newDelayedResponsesStreamServer(t, cfg, http.Header{"X-Request-ID": {"req-delayed"}}, nil)
	defer server.Close()
	defer release()
	timer := time.AfterFunc(1200*time.Millisecond, release)
	defer timer.Stop()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/responses", strings.NewReader(`{"model":"`+modelID+`","stream":true,"input":"hello"}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	client := server.Client()
	client.Timeout = 3 * time.Second

	started := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("stream did not start after upstream headers became available: %v", err)
	}
	if elapsed := time.Since(started); elapsed < time.Second {
		t.Fatalf("stream committed after %s, before delayed upstream headers were available", elapsed)
	}
	if got := resp.Header.Get("X-Request-ID"); got != "req-delayed" {
		t.Fatalf("X-Request-ID = %q, want req-delayed", got)
	}
	body, err := io.ReadAll(resp.Body)
	if errClose := resp.Body.Close(); err == nil && errClose != nil {
		err = errClose
	}
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if strings.HasPrefix(string(body), ": keep-alive\n\n") {
		t.Fatalf("body = %q, keep-alive committed before passthrough headers", body)
	}
}

func TestResponsesStreamingPreservesInterceptorHeadersBeforeKeepAlive(t *testing.T) {
	cfg := &sdkconfig.SDKConfig{Streaming: sdkconfig.StreamingConfig{KeepAliveSeconds: 1}}
	pluginHost := &responsesStreamInterceptorHost{
		interceptStreamChunk: func(_ context.Context, req pluginapi.StreamChunkInterceptRequest) pluginapi.StreamChunkInterceptResponse {
			headers := req.ResponseHeaders.Clone()
			if headers == nil {
				headers = make(http.Header)
			}
			if req.ChunkIndex == 0 {
				time.Sleep(1200 * time.Millisecond)
				headers.Set("X-Plugin-Header", "ready")
			}
			return pluginapi.StreamChunkInterceptResponse{Headers: headers, Body: append([]byte(nil), req.Body...)}
		},
	}
	server, release, modelID := newDelayedResponsesStreamServer(t, cfg, nil, pluginHost)
	defer server.Close()
	release()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/responses", strings.NewReader(`{"model":"`+modelID+`","stream":true,"input":"hello"}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	client := server.Client()
	client.Timeout = 3 * time.Second

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("stream did not start after interceptor headers became available: %v", err)
	}
	if got := resp.Header.Get("X-Plugin-Header"); got != "ready" {
		t.Fatalf("X-Plugin-Header = %q, want ready", got)
	}
	body, err := io.ReadAll(resp.Body)
	if errClose := resp.Body.Close(); err == nil && errClose != nil {
		err = errClose
	}
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if strings.HasPrefix(string(body), ": keep-alive\n\n") {
		t.Fatalf("body = %q, keep-alive committed before interceptor headers", body)
	}
}

func TestForwardResponsesStreamSeparatesDataOnlySSEChunks(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	data := make(chan []byte, 2)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"arguments\":\"{}\"}}")
	data <- []byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"output\":[]}}")
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)
	body := recorder.Body.String()
	parts := strings.Split(strings.TrimSpace(body), "\n\n")
	if len(parts) != 2 {
		t.Fatalf("expected 2 SSE events, got %d. Body: %q", len(parts), body)
	}

	expectedPart1 := "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"arguments\":\"{}\"}}"
	if parts[0] != expectedPart1 {
		t.Errorf("unexpected first event.\nGot: %q\nWant: %q", parts[0], expectedPart1)
	}

	expectedPart2 := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"output\":[{\"type\":\"function_call\",\"arguments\":\"{}\"}]}}"
	if parts[1] != expectedPart2 {
		t.Errorf("unexpected second event.\nGot: %q\nWant: %q", parts[1], expectedPart2)
	}
}

func TestForwardResponsesStreamRepairsEmptyCompletedOutputFromDoneItems(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	data := make(chan []byte, 3)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte(`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs-1","summary":[]}}`)
	data <- []byte(`data: {"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"fc-1","call_id":"call-1","name":"shell","arguments":"{\"cmd\":\"pwd\"}","status":"completed"}}`)
	data <- []byte(`data: {"type":"response.completed","response":{"id":"resp-1","output":[]}}`)
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)

	parts := strings.Split(strings.TrimSpace(recorder.Body.String()), "\n\n")
	if len(parts) != 3 {
		t.Fatalf("expected 3 SSE events, got %d. Body: %q", len(parts), recorder.Body.String())
	}

	payload := strings.TrimPrefix(parts[2], "data: ")
	output := gjson.Get(payload, "response.output")
	if !output.IsArray() || len(output.Array()) != 2 {
		t.Fatalf("expected repaired completed output with 2 items, got %s", output.Raw)
	}
	if got := gjson.Get(payload, "response.output.1.name").String(); got != "shell" {
		t.Fatalf("expected function_call name to be preserved, got %q in %s", got, payload)
	}
	if got := gjson.Get(payload, "response.output.1.arguments").String(); got != `{"cmd":"pwd"}` {
		t.Fatalf("expected function_call arguments to be preserved, got %q in %s", got, payload)
	}
}

func TestForwardResponsesStreamRepairsMixedIndexedAndUnindexedDoneItems(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	data := make(chan []byte, 3)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte(`data: {"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"fc-1","call_id":"call-1","name":"shell","arguments":"{}","status":"completed"}}`)
	data <- []byte(`data: {"type":"response.output_item.done","item":{"type":"message","id":"msg-1","role":"assistant","content":[{"type":"output_text","text":"done"}]}}`)
	data <- []byte(`data: {"type":"response.completed","response":{"id":"resp-1","output":[]}}`)
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)

	parts := strings.Split(strings.TrimSpace(recorder.Body.String()), "\n\n")
	if len(parts) != 3 {
		t.Fatalf("expected 3 SSE events, got %d. Body: %q", len(parts), recorder.Body.String())
	}

	payload := strings.TrimPrefix(parts[2], "data: ")
	output := gjson.Get(payload, "response.output")
	if !output.IsArray() || len(output.Array()) != 2 {
		t.Fatalf("expected repaired completed output with 2 items, got %s", output.Raw)
	}
	if got := gjson.Get(payload, "response.output.0.name").String(); got != "shell" {
		t.Fatalf("expected indexed function_call to be preserved first, got %q in %s", got, payload)
	}
	if got := gjson.Get(payload, "response.output.1.id").String(); got != "msg-1" {
		t.Fatalf("expected unindexed message to be appended, got %q in %s", got, payload)
	}
}

func TestForwardResponsesStreamRepairsMultilineCompletedOutputAsSSEDataLines(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	data := make(chan []byte, 2)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte(`data: {"type":"response.output_item.done","item":{"type":"function_call","arguments":"{}"}}`)
	data <- []byte("data: {\"type\":\"response.completed\",\ndata: \"response\":{\"id\":\"resp-1\",\"output\":[]}}\n\n")
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)

	parts := strings.Split(strings.TrimSpace(recorder.Body.String()), "\n\n")
	if len(parts) != 2 {
		t.Fatalf("expected 2 SSE events, got %d. Body: %q", len(parts), recorder.Body.String())
	}

	completedFrame := []byte(parts[1])
	for _, line := range strings.Split(parts[1], "\n") {
		if line != "" && !strings.HasPrefix(line, "data: ") {
			t.Fatalf("expected every completed payload line to be an SSE data line, got %q in %q", line, parts[1])
		}
	}

	payload, ok := responsesSSEDataPayload(completedFrame)
	if !ok {
		t.Fatalf("expected completed frame to contain data payload: %q", parts[1])
	}
	output := gjson.GetBytes(payload, "response.output")
	if !output.IsArray() || len(output.Array()) != 1 {
		t.Fatalf("expected repaired completed output with 1 item, got %s from %q", output.Raw, payload)
	}
}

func TestForwardResponsesStreamReassemblesSplitSSEEventChunks(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	data := make(chan []byte, 3)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte("event: response.created")
	data <- []byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\"}}")
	data <- []byte("\n")
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)

	got := strings.TrimSuffix(recorder.Body.String(), "\n")
	want := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\"}}\n\n"
	if got != want {
		t.Fatalf("unexpected split-event framing.\nGot:  %q\nWant: %q", got, want)
	}
}

func TestForwardResponsesStreamPreservesValidFullSSEEventChunks(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	data := make(chan []byte, 1)
	errs := make(chan *interfaces.ErrorMessage)
	chunk := []byte("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\"}}\n\n")
	data <- chunk
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)

	got := strings.TrimSuffix(recorder.Body.String(), "\n")
	if got != string(chunk) {
		t.Fatalf("unexpected full-event framing.\nGot:  %q\nWant: %q", got, string(chunk))
	}
}

func TestForwardResponsesStreamBuffersSplitDataPayloadChunks(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	data := make(chan []byte, 2)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte("data: {\"type\":\"response.created\"")
	data <- []byte(",\"response\":{\"id\":\"resp-1\"}}")
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)

	got := recorder.Body.String()
	want := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\"}}\n\n\n"
	if got != want {
		t.Fatalf("unexpected split-data framing.\nGot:  %q\nWant: %q", got, want)
	}
}

func TestResponsesSSENeedsLineBreakSkipsChunksThatAlreadyStartWithNewline(t *testing.T) {
	if responsesSSENeedsLineBreak([]byte("event: response.created"), []byte("\n")) {
		t.Fatal("expected no injected newline before newline-only chunk")
	}
	if responsesSSENeedsLineBreak([]byte("event: response.created"), []byte("\r\n")) {
		t.Fatal("expected no injected newline before CRLF chunk")
	}
}

func TestForwardResponsesStreamDropsIncompleteTrailingDataChunkOnFlush(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	data := make(chan []byte, 1)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte("data: {\"type\":\"response.created\"")
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)

	if got := recorder.Body.String(); got != "\n" {
		t.Fatalf("expected incomplete trailing data to be dropped on flush.\nGot: %q", got)
	}
}
