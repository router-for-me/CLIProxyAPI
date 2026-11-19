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
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

const decisionsDispatchModel = "gemini-decision-1"

type decisionsCaptureExecutor struct {
	mu      sync.Mutex
	req     coreexecutor.Request
	calls   int
	payload []byte
	status  int
	err     error
}

func (*decisionsCaptureExecutor) Identifier() string { return "gemini" }

func (e *decisionsCaptureExecutor) Execute(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	e.mu.Lock()
	e.calls++
	e.req = coreexecutor.Request{Model: req.Model, Payload: append([]byte(nil), req.Payload...), Metadata: req.Metadata}
	payload, status, errExecute := e.payload, e.status, e.err
	e.mu.Unlock()
	if errExecute != nil {
		return coreexecutor.Response{}, errExecute
	}
	if status != 0 {
		return coreexecutor.Response{}, &decisionsStatusError{status: status, message: "upstream rejected"}
	}
	return coreexecutor.Response{Payload: payload}, nil
}

func (*decisionsCaptureExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func (*decisionsCaptureExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (*decisionsCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (*decisionsCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (e *decisionsCaptureExecutor) snapshot() (coreexecutor.Request, int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.req, e.calls
}

type decisionsStatusError struct {
	status  int
	message string
}

func (e *decisionsStatusError) Error() string   { return e.message }
func (e *decisionsStatusError) StatusCode() int { return e.status }

func newDecisionsTestRouter(t *testing.T, executor *decisionsCaptureExecutor, authID string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: authID, Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: decisionsDispatchModel}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	router := gin.New()
	router.POST("/v1beta/decisions", NewGeminiAPIHandler(base).Decisions)
	return router
}

func TestDecisionsHandlerDispatchesWithActionMetadata(t *testing.T) {
	const upstream = `{"answers":{"q":{"type":"noul","noul":0.9,"confidence":0.9}},"usage":{"total_input_tokens":2,"total_output_tokens":0,"total_tokens":2}}`
	executor := &decisionsCaptureExecutor{payload: []byte(upstream)}
	router := newDecisionsTestRouter(t, executor, "decisions-dispatch")

	body := `{"model":"models/gemini-decision-1","input":"hi","questions":{"q":{"type":"noul","instructions":"ok?"}}}`
	request := httptest.NewRequest(http.MethodPost, "/v1beta/decisions", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if recorder.Body.String() != upstream {
		t.Fatalf("body = %q, want upstream body unchanged", recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("response Content-Type = %q, want application/json", got)
	}

	gotReq, calls := executor.snapshot()
	if calls != 1 {
		t.Fatalf("executor calls = %d, want 1", calls)
	}
	if gotReq.Model != decisionsDispatchModel {
		t.Fatalf("executor model = %q, want %q (models/ prefix stripped)", gotReq.Model, decisionsDispatchModel)
	}
	if got, _ := gotReq.Metadata[coreexecutor.RequestActionMetadataKey].(string); got != helps.GeminiDecisionsAction {
		t.Fatalf("req.Metadata[action] = %#v, want %q", gotReq.Metadata[coreexecutor.RequestActionMetadataKey], helps.GeminiDecisionsAction)
	}
}

func TestDecisionsHandlerRejectsInvalidRequests(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
	}{
		{name: "invalid json", body: `{"model":`},
		{name: "missing model", body: `{"input":"hi","questions":{"q":{"type":"noul"}}}`},
		{name: "blank model", body: `{"model":"  ","input":"hi","questions":{"q":{"type":"noul"}}}`},
		{name: "stream true", body: `{"model":"gemini-decision-1","input":"hi","questions":{"q":{"type":"noul"}},"stream":true}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			executor := &decisionsCaptureExecutor{payload: []byte(`{}`)}
			router := newDecisionsTestRouter(t, executor, "decisions-invalid")

			request := httptest.NewRequest(http.MethodPost, "/v1beta/decisions", strings.NewReader(tt.body))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), "invalid_request_error") {
				t.Fatalf("body = %q, want invalid_request_error", recorder.Body.String())
			}
			if _, calls := executor.snapshot(); calls != 0 {
				t.Fatalf("executor calls = %d, want 0", calls)
			}
		})
	}
}

func TestDecisionsHandlerMapsExecutorError(t *testing.T) {
	executor := &decisionsCaptureExecutor{status: http.StatusBadRequest}
	router := newDecisionsTestRouter(t, executor, "decisions-error")

	request := httptest.NewRequest(http.MethodPost, "/v1beta/decisions", strings.NewReader(`{"model":"gemini-decision-1","input":"hi","questions":{"q":{"type":"noul"}}}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "upstream rejected") {
		t.Fatalf("body = %q, want upstream error message", recorder.Body.String())
	}
}
