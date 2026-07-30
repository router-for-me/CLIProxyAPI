package openai

import (
	"context"
	"errors"
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

type responsesBootstrapTestExecutor struct {
	chunks <-chan coreexecutor.StreamChunk
	mu     sync.Mutex
	calls  int
}

func (e *responsesBootstrapTestExecutor) Identifier() string { return "responses-bootstrap-test" }

func (e *responsesBootstrapTestExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *responsesBootstrapTestExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.mu.Lock()
	e.calls++
	e.mu.Unlock()
	return &coreexecutor.StreamResult{
		Headers: http.Header{"X-Upstream-Attempt": {"1"}},
		Chunks:  e.chunks,
	}, nil
}

func (e *responsesBootstrapTestExecutor) Calls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

func (e *responsesBootstrapTestExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *responsesBootstrapTestExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *responsesBootstrapTestExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

type responsesBootstrapInterceptorHost struct{}

func (*responsesBootstrapInterceptorHost) InterceptRequestBeforeAuth(_ context.Context, req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	return pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: req.Body}
}

func (*responsesBootstrapInterceptorHost) InterceptRequestAfterAuth(_ context.Context, req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	return pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: req.Body}
}

func (*responsesBootstrapInterceptorHost) InterceptResponse(_ context.Context, req pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
	return pluginapi.ResponseInterceptResponse{Headers: req.ResponseHeaders, Body: req.Body}
}

func (*responsesBootstrapInterceptorHost) InterceptStreamChunk(_ context.Context, req pluginapi.StreamChunkInterceptRequest) pluginapi.StreamChunkInterceptResponse {
	headers := req.ResponseHeaders.Clone()
	headers.Set("X-Stream-Interceptor", "active")
	return pluginapi.StreamChunkInterceptResponse{Headers: headers, Body: req.Body}
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

type signalingResponseWriter struct {
	gin.ResponseWriter
	flushed chan struct{}
	once    sync.Once
}

func (w *signalingResponseWriter) Flush() {
	w.ResponseWriter.Flush()
	w.once.Do(func() { close(w.flushed) })
}

func newResponsesBootstrapEndpointHandler(t *testing.T, cfg *sdkconfig.SDKConfig, chunks <-chan coreexecutor.StreamChunk) (*OpenAIResponsesAPIHandler, *responsesBootstrapTestExecutor) {
	t.Helper()
	const model = "responses-bootstrap-endpoint-model"
	manager := coreauth.NewManager(nil, nil, nil)
	executor := &responsesBootstrapTestExecutor{chunks: chunks}
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{
		ID:       "responses-bootstrap-endpoint-auth",
		Provider: executor.Identifier(),
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"email": "responses-bootstrap@example.com"},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("manager.Register(): %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	return NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(cfg, manager)), executor
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

func TestResponsesBootstrapCommitStartsKeepAliveWithObservableHeaders(t *testing.T) {
	tests := []struct {
		name       string
		cfg        *sdkconfig.SDKConfig
		pluginHost handlers.PluginInterceptorHost
		header     string
		value      string
	}{
		{
			name:   "passthrough headers",
			cfg:    &sdkconfig.SDKConfig{PassthroughHeaders: true, Streaming: sdkconfig.StreamingConfig{BootstrapRetries: 1}},
			header: "X-Upstream-Attempt",
			value:  "1",
		},
		{
			name:       "stream interceptor headers",
			cfg:        &sdkconfig.SDKConfig{Streaming: sdkconfig.StreamingConfig{BootstrapRetries: 1}},
			pluginHost: &responsesBootstrapInterceptorHost{},
			header:     "X-Stream-Interceptor",
			value:      "active",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			chunks := make(chan coreexecutor.StreamChunk, 3)
			chunks <- coreexecutor.StreamChunk{Payload: []byte("event: response.created")}
			chunks <- coreexecutor.StreamChunk{Payload: []byte(`data: {"type":"response.created","response":{"id":"resp-1","status":"in_progress"}}`)}
			h, executor := newResponsesBootstrapEndpointHandler(t, test.cfg, chunks)
			if test.pluginHost != nil {
				h.SetPluginHost(test.pluginHost)
			}

			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			writer := &signalingResponseWriter{ResponseWriter: c.Writer, flushed: make(chan struct{})}
			c.Writer = writer

			data, errs, committer := h.ExecuteStreamWithAuthManagerBootstrapCommit(
				context.Background(),
				h.HandlerType(),
				"responses-bootstrap-endpoint-model",
				[]byte(`{"model":"responses-bootstrap-endpoint-model","input":"hello"}`),
				"",
			)
			go func() {
				<-writer.flushed
				chunks <- coreexecutor.StreamChunk{Err: &coreauth.Error{
					Code:       "upstream_closed",
					Message:    "stream failed after heartbeat commit",
					HTTPStatus: http.StatusRequestTimeout,
				}}
				close(chunks)
			}()

			var canceled error
			h.forwardInitialResponsesStream(
				c,
				writer,
				func(err error) { canceled = err },
				data,
				errs,
				committer.Commit,
				committer.Headers,
				&responsesSSEFramer{},
				10*time.Millisecond,
			)

			if canceled == nil || !strings.Contains(canceled.Error(), "stream failed after heartbeat commit") {
				t.Fatalf("stream cancel error = %v, want post-commit upstream failure", canceled)
			}
			if got := recorder.Header().Get(test.header); got != test.value {
				t.Fatalf("%s = %q, want %q", test.header, got, test.value)
			}
			body := recorder.Body.String()
			keepAliveIndex := strings.Index(body, ": keep-alive\n\n")
			createdIndex := strings.Index(body, "event: response.created")
			if keepAliveIndex < 0 || createdIndex < 0 || keepAliveIndex >= createdIndex {
				t.Fatalf("expected keep-alive before provisional Responses prefix, got %q", body)
			}
			if !strings.Contains(body, "event: error") || !strings.Contains(body, `"type":"error"`) {
				t.Fatalf("expected post-commit Responses SSE error, got %q", body)
			}
			if executor.Calls() != 1 {
				t.Fatalf("stream attempts = %d, want no redispatch after heartbeat commit", executor.Calls())
			}
		})
	}
}

func TestForwardInitialResponsesStreamEmitsKeepAliveBeforeFirstData(t *testing.T) {
	h, recorder, c, _ := newResponsesStreamTestHandler(t)
	writer := &signalingResponseWriter{ResponseWriter: c.Writer, flushed: make(chan struct{})}
	c.Writer = writer

	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage)
	go func() {
		<-writer.flushed
		data <- []byte(`data: {"type":"response.completed","response":{"id":"resp-1","output":[]}}`)
		close(data)
		close(errs)
	}()

	var canceled error
	h.forwardInitialResponsesStream(
		c,
		writer,
		func(err error) { canceled = err },
		data,
		errs,
		func() http.Header { return nil },
		func() http.Header { return nil },
		&responsesSSEFramer{},
		10*time.Millisecond,
	)

	if canceled != nil {
		t.Fatalf("stream canceled with error: %v", canceled)
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content type = %q, want text/event-stream", got)
	}
	body := recorder.Body.String()
	keepAliveIndex := strings.Index(body, ": keep-alive\n\n")
	dataIndex := strings.Index(body, "data: {")
	if keepAliveIndex < 0 || dataIndex < 0 || keepAliveIndex >= dataIndex {
		t.Fatalf("expected keep-alive before delayed first data chunk, got %q", body)
	}
}

func TestForwardInitialResponsesStreamUsesSSEErrorAfterKeepAlive(t *testing.T) {
	h, recorder, c, _ := newResponsesStreamTestHandler(t)
	writer := &signalingResponseWriter{ResponseWriter: c.Writer, flushed: make(chan struct{})}
	c.Writer = writer

	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage)
	go func() {
		<-writer.flushed
		errs <- &interfaces.ErrorMessage{
			StatusCode: http.StatusBadGateway,
			Error:      errors.New("upstream failed after bootstrap"),
		}
		close(data)
		close(errs)
	}()

	var canceled error
	h.forwardInitialResponsesStream(
		c,
		writer,
		func(err error) { canceled = err },
		data,
		errs,
		func() http.Header { return nil },
		func() http.Header { return nil },
		&responsesSSEFramer{},
		10*time.Millisecond,
	)

	if canceled == nil || canceled.Error() != "upstream failed after bootstrap" {
		t.Fatalf("cancel error = %v, want upstream bootstrap failure", canceled)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want committed streaming status 200", recorder.Code)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, ": keep-alive\n\n") ||
		!strings.Contains(body, "event: error") ||
		!strings.Contains(body, `"type":"error"`) {
		t.Fatalf("expected keep-alive followed by Responses SSE error, got %q", body)
	}
	if strings.Contains(body, `"error":{`) {
		t.Fatalf("unexpected non-streaming JSON error after committed keep-alive: %q", body)
	}
}

func TestForwardInitialResponsesStreamPrefersPendingErrorWhenDataCloses(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)
	data := make(chan []byte)
	close(data)
	errs := make(chan *interfaces.ErrorMessage, 1)
	errs <- &interfaces.ErrorMessage{
		StatusCode: http.StatusBadGateway,
		Error:      errors.New("bootstrap failed before commit"),
	}
	close(errs)

	var canceled error
	h.forwardInitialResponsesStream(
		c,
		flusher,
		func(err error) { canceled = err },
		data,
		errs,
		func() http.Header { return nil },
		func() http.Header { return nil },
		&responsesSSEFramer{},
		0,
	)

	if canceled == nil || canceled.Error() != "bootstrap failed before commit" {
		t.Fatalf("cancel error = %v, want pending bootstrap failure", canceled)
	}
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "bootstrap failed before commit") {
		t.Fatalf("expected JSON bootstrap error, got %q", recorder.Body.String())
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
