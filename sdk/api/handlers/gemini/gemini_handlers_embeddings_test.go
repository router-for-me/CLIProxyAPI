package gemini

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

const embeddingDispatchGeminiModel = "gemini-embedding-001"

type embeddingCaptureGeminiExecutor struct {
	mu      sync.Mutex
	req     coreexecutor.Request
	opts    coreexecutor.Options
	calls   int
	payload []byte
	headers http.Header
	err     error
}

func (*embeddingCaptureGeminiExecutor) Identifier() string { return "gemini" }

func (e *embeddingCaptureGeminiExecutor) Execute(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	e.mu.Lock()
	e.req = coreexecutor.Request{Model: req.Model, Payload: append([]byte(nil), req.Payload...), Metadata: req.Metadata}
	e.opts = opts
	e.calls++
	errExecute := e.err
	payload := e.payload
	headers := e.headers
	e.mu.Unlock()
	if errExecute != nil {
		return coreexecutor.Response{}, errExecute
	}
	return coreexecutor.Response{Payload: payload, Headers: headers}, nil
}

func (*embeddingCaptureGeminiExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func (*embeddingCaptureGeminiExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (*embeddingCaptureGeminiExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (*embeddingCaptureGeminiExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (e *embeddingCaptureGeminiExecutor) snapshot() (coreexecutor.Request, coreexecutor.Options, int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.req, e.opts, e.calls
}

func newEmbeddingTestHandler(t *testing.T, executor *embeddingCaptureGeminiExecutor, authID string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: authID, Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: embeddingDispatchGeminiModel}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	router := gin.New()
	router.POST("/v1beta/models/*action", NewGeminiAPIHandler(base).GeminiHandler)
	return router
}

func TestGeminiHandlerDispatchesEmbeddingActions(t *testing.T) {
	for _, action := range []string{"embedContent", "batchEmbedContents"} {
		t.Run(action, func(t *testing.T) {
			const upstream = `{"embedding":{"values":[0.1]}}`
			executor := &embeddingCaptureGeminiExecutor{
				payload: []byte(upstream),
				headers: http.Header{"X-Upstream": []string{"embed"}},
			}
			router := newEmbeddingTestHandler(t, executor, "gemini-embed-dispatch-"+action)

			body := `{"content":{"parts":[{"text":"hi"}]}}`
			request := httptest.NewRequest(http.MethodPost, "/v1beta/models/"+embeddingDispatchGeminiModel+":"+action, strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
			}
			if recorder.Body.String() != upstream {
				t.Fatalf("body = %q, want native upstream body", recorder.Body.String())
			}
			if got := recorder.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
				t.Fatalf("response Content-Type = %q, want application/json", got)
			}

			gotReq, _, calls := executor.snapshot()
			if calls != 1 {
				t.Fatalf("executor calls = %d, want 1", calls)
			}
			if gotReq.Model != embeddingDispatchGeminiModel {
				t.Fatalf("executor model = %q, want %q", gotReq.Model, embeddingDispatchGeminiModel)
			}
			if got, _ := gotReq.Metadata[coreexecutor.RequestActionMetadataKey].(string); got != action {
				t.Fatalf("req.Metadata[action] = %#v, want %q", gotReq.Metadata[coreexecutor.RequestActionMetadataKey], action)
			}
			if string(gotReq.Payload) != body {
				t.Fatalf("executor payload = %q, want %q", gotReq.Payload, body)
			}
		})
	}
}

func TestGeminiHandlerUnknownActionReturnsNotFound(t *testing.T) {
	for _, action := range []string{"predict", "unknownMethod", "generateContentV2"} {
		t.Run(action, func(t *testing.T) {
			executor := &embeddingCaptureGeminiExecutor{payload: []byte(`{}`)}
			router := newEmbeddingTestHandler(t, executor, "gemini-unknown-"+action)

			request := httptest.NewRequest(http.MethodPost, "/v1beta/models/"+embeddingDispatchGeminiModel+":"+action, strings.NewReader(`{"content":{}}`))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)

			if recorder.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusNotFound, recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), "invalid_request_error") {
				t.Fatalf("body = %q, want invalid_request_error", recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), "Unsupported method") {
				t.Fatalf("body = %q, want unsupported method message", recorder.Body.String())
			}
			if _, _, calls := executor.snapshot(); calls != 0 {
				t.Fatalf("executor calls = %d, want 0", calls)
			}
		})
	}
}

func TestGeminiHandlerEmbeddingMapsExecutorError(t *testing.T) {
	executor := &embeddingCaptureGeminiExecutor{err: &embeddingStatusError{status: http.StatusBadRequest, message: "bad embedding input"}}
	router := newEmbeddingTestHandler(t, executor, "gemini-embed-error")

	request := httptest.NewRequest(http.MethodPost, "/v1beta/models/"+embeddingDispatchGeminiModel+":embedContent", strings.NewReader(`{"content":{"parts":[{"text":"hi"}]}}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "bad embedding input") {
		t.Fatalf("body = %q, want upstream error message", recorder.Body.String())
	}
}

type embeddingStatusError struct {
	status  int
	message string
}

func (e *embeddingStatusError) Error() string   { return e.message }
func (e *embeddingStatusError) StatusCode() int { return e.status }
