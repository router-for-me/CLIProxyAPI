package claude

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

func TestClaudeErrorRequestID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tt := range []struct {
		name      string
		body      string
		committed bool
		want      string
	}{
		{name: "local", body: "upstream unavailable", want: "req_abc12345"},
		{name: "upstream", body: `{"type":"error","error":{"type":"api_error","message":"unavailable"},"request_id":"req_upstream"}`, want: "req_upstream"},
		{name: "committed upstream", body: `{"type":"error","error":{"type":"api_error","message":"unavailable"},"request_id":"req_upstream"}`, committed: true, want: "req_abc12345"},
		{name: "invalid upstream", body: `{"type":"error","error":{"message":"unavailable"},"request_id":"req_bad\r\nInjected: yes"}`, want: "req_abc12345"},
		{name: "numeric upstream", body: `{"type":"error","error":{"message":"unavailable"},"request_id":123}`, want: "req_abc12345"},
		{name: "non-Claude upstream", body: `{"error":{"message":"unavailable"},"request_id":"other-provider-id"}`, want: "req_abc12345"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			logging.SetGinRequestID(c, "abc12345")
			if tt.committed {
				c.Header("Request-Id", "req_abc12345")
				c.Writer.WriteHeaderNow()
			}
			handler := NewClaudeCodeAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{PassthroughHeaders: true}, nil))
			handler.WriteErrorResponse(c, &interfaces.ErrorMessage{
				StatusCode: http.StatusBadGateway,
				Error:      errors.New(tt.body),
				Addon:      http.Header{"Request-Id": {"req_addon"}},
			})
			if got := recorder.Result().Header.Get("Request-Id"); got != tt.want {
				t.Errorf("wire request-id = %q, want %q", got, tt.want)
			}
			if got := gjson.GetBytes(recorder.Body.Bytes(), "request_id").String(); got != tt.want {
				t.Errorf("body request_id = %q, want %q; body=%s", got, tt.want, recorder.Body.String())
			}
		})
	}
}

func TestClaudeDirectErrorPreservesRequestIDAndBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	logging.SetGinRequestID(c, "abc12345")
	body := []byte(`{ "type": "error", "error": {"type":"rate_limit_error","message":"wait"}, "request_id": "req_direct", "extra": true }`)
	handler := NewClaudeCodeAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil))
	handler.WriteErrorResponse(c, &interfaces.ErrorMessage{
		StatusCode: http.StatusTooManyRequests, DirectResponse: true, Body: body,
		Headers: http.Header{"Request-Id": {"req_different_header"}, "Retry-After": {"4"}},
	})
	if got := recorder.Result().Header.Get("Request-Id"); got != "req_direct" {
		t.Errorf("request-id = %q, want body ID req_direct", got)
	}
	if recorder.Body.String() != string(body) || recorder.Code != http.StatusTooManyRequests || recorder.Header().Get("Retry-After") != "4" {
		t.Fatalf("direct response changed: status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestClaudeCommittedDirectErrorUsesWireRequestID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tt := range []struct {
		name    string
		body    string
		rewrite bool
	}{
		{name: "Claude error", body: `{ "type": "error", "error": {"type":"rate_limit_error","message":"wait"}, "request_id": "req_direct", "extra": true }`, rewrite: true},
		{name: "opaque response", body: `{"message":"custom response","request_id":"opaque"}`},
		{name: "matching ID", body: `{ "type": "error", "error": {"message":"wait"}, "request_id": "req_abc12345" }`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			logging.SetGinRequestID(c, "abc12345")
			EnsureRequestID(c)
			// Model the header commitment performed by non-stream keep-alive.
			c.Writer.Flush()
			handler := NewClaudeCodeAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil))
			handler.WriteErrorResponse(c, &interfaces.ErrorMessage{
				StatusCode: http.StatusTooManyRequests, DirectResponse: true, Body: []byte(tt.body),
				Headers: http.Header{"Request-Id": {"req_upstream_header"}},
			})
			if recorder.Code != http.StatusOK || recorder.Result().Header.Get("Request-Id") != "req_abc12345" {
				t.Fatalf("committed response changed: %d %v", recorder.Code, recorder.Result().Header)
			}
			body := recorder.Body.String()
			if tt.rewrite {
				if gjson.Get(body, "request_id").String() != "req_abc12345" || !gjson.Get(body, "extra").Bool() || gjson.Get(body, "error.message").String() != "wait" {
					t.Errorf("direct error did not retain wire ID and error details: %s", body)
				}
			} else if body != tt.body {
				t.Errorf("direct body changed: %s", body)
			}
		})
	}
}

type claudeRequestIDExecutor struct {
	mode    string
	flushed <-chan struct{}
}

func (*claudeRequestIDExecutor) Identifier() string { return "claude-request-id-test" }

func (e *claudeRequestIDExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	if e.mode == "error" {
		return coreexecutor.Response{}, errors.New("local execution failed")
	}
	return coreexecutor.Response{Payload: []byte(`{"type":"message","content":[]}`), Headers: http.Header{"Request-Id": {"req_upstream_header"}}}, nil
}

func (e *claudeRequestIDExecutor) ExecuteStream(ctx context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	if e.mode == "early error" {
		return nil, errors.New("local execution failed")
	}
	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_test\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n")}
	go func() {
		defer close(chunks)
		select {
		case <-ctx.Done():
			return
		case <-e.flushed:
		}
		if e.mode == "late error" {
			chunks <- coreexecutor.StreamChunk{Err: errors.New(`{"type":"error","error":{"type":"api_error","message":"stream failed"},"request_id":"req_late_upstream"}`)}
		} else {
			chunks <- coreexecutor.StreamChunk{Payload: []byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")}
		}
	}()
	return &coreexecutor.StreamResult{Chunks: chunks, Headers: http.Header{"Request-Id": {"req_upstream_header"}}}, nil
}

func (*claudeRequestIDExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *claudeRequestIDExecutor) CountTokens(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	return e.Execute(ctx, auth, req, opts)
}

func (*claudeRequestIDExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

type claudeRequestIDRecorder struct {
	*httptest.ResponseRecorder
	flushed chan struct{}
}

func (r *claudeRequestIDRecorder) Flush() {
	r.ResponseRecorder.Flush()
	select {
	case <-r.flushed:
	default:
		close(r.flushed)
	}
}

func TestClaudeRequestIDAcrossExecutionPaths(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tt := range []struct {
		name       string
		path       string
		mode       string
		stream     bool
		wantStatus int
	}{
		{name: "messages success", path: "/v1/messages", mode: "success", wantStatus: 200},
		{name: "messages failure", path: "/v1/messages", mode: "error", wantStatus: 500},
		{name: "count success", path: "/v1/messages/count_tokens", mode: "success", wantStatus: 200},
		{name: "count failure", path: "/v1/messages/count_tokens", mode: "error", wantStatus: 500},
		{name: "stream success", path: "/v1/messages", mode: "success", stream: true, wantStatus: 200},
		{name: "stream early failure", path: "/v1/messages", mode: "early error", stream: true, wantStatus: 500},
		{name: "stream committed failure", path: "/v1/messages", mode: "late error", stream: true, wantStatus: 200},
	} {
		t.Run(tt.name, func(t *testing.T) {
			recorder := &claudeRequestIDRecorder{ResponseRecorder: httptest.NewRecorder(), flushed: make(chan struct{})}
			executor := &claudeRequestIDExecutor{mode: tt.mode, flushed: recorder.flushed}
			manager := coreauth.NewManager(nil, nil, nil)
			manager.RegisterExecutor(executor)
			auth := &coreauth.Auth{ID: "claude-request-id-test-auth", Provider: executor.Identifier(), Status: coreauth.StatusActive}
			if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
				t.Fatal(errRegister)
			}
			registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "claude-request-id-model"}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			handler := NewClaudeCodeAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{PassthroughHeaders: true}, manager))
			router := gin.New()
			router.Use(func(c *gin.Context) { logging.SetGinRequestID(c, "abc12345") })
			router.POST("/v1/messages", handler.ClaudeMessages)
			router.POST("/v1/messages/count_tokens", handler.ClaudeCountTokens)
			body := fmt.Sprintf(`{"model":"claude-request-id-model","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":%t}`, tt.stream)
			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(body))
			req.Header.Set("Request-Id", "req_client_spoof")
			router.ServeHTTP(recorder, req)
			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, tt.wantStatus, recorder.Body.String())
			}
			wantID := "req_abc12345"
			if got := recorder.Result().Header.Get("Request-Id"); got != wantID {
				t.Errorf("wire request-id = %q, want %q", got, wantID)
			}
			responseBody := recorder.Body.String()
			if tt.mode == "late error" {
				_, responseBody, _ = strings.Cut(responseBody, "event: error\ndata: ")
			}
			if tt.mode != "success" {
				if got := gjson.Get(responseBody, "request_id").String(); got != wantID {
					t.Errorf("error request_id = %q, want %q; body=%s", got, wantID, recorder.Body.String())
				}
			}
		})
	}
}
