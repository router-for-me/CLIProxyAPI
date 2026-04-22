package openai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	innerconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/api/middleware"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

type framedResponsesStreamExecutor struct{}

func (e *framedResponsesStreamExecutor) Identifier() string { return "test-provider" }

func (e *framedResponsesStreamExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *framedResponsesStreamExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	ch := make(chan coreexecutor.StreamChunk, 2)
	ch <- coreexecutor.StreamChunk{Payload: []byte("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_handler\",\"status\":\"in_progress\"}}\n\n")}
	ch <- coreexecutor.StreamChunk{Payload: []byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_handler\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")}
	close(ch)
	return &coreexecutor.StreamResult{Chunks: ch}, nil
}

func (e *framedResponsesStreamExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *framedResponsesStreamExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *framedResponsesStreamExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestOpenAIResponsesStreamingWritesCompleteSSEFrames(t *testing.T) {
	gin.SetMode(gin.TestMode)
	executor := &framedResponsesStreamExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{ID: "auth-framed", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	manager.RefreshSchedulerEntry(auth.ID)
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses", h.Responses)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test-model","input":"hello","stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%q", resp.Code, http.StatusOK, resp.Body.String())
	}

	body := resp.Body.String()
	if strings.HasPrefix(body, "\n") {
		t.Fatalf("response body should not start with an extra newline: %q", body)
	}
	if strings.Contains(body, "\n\n\n") {
		t.Fatalf("response body should not introduce triple newlines between frames: %q", body)
	}
	if !strings.Contains(body, "event: response.created\ndata: {\"type\":\"response.created\"") {
		t.Fatalf("response body missing created SSE frame: %q", body)
	}
	if !strings.Contains(body, "event: response.completed\ndata: {\"type\":\"response.completed\"") {
		t.Fatalf("response body missing completed SSE frame: %q", body)
	}
	if !strings.HasSuffix(body, "\n\n") {
		t.Fatalf("response body should end with SSE frame delimiter, got %q", body)
	}
}

func TestOpenAIResponsesRequestLogKeepsEventLines(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_log\",\"created_at\":1775540000,\"model\":\"Qwen3.6-Plus\",\"status\":\"in_progress\",\"output\":[]}}\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = w.Write([]byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_log\",\"created_at\":1775540000,\"model\":\"Qwen3.6-Plus\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":2,\"output_tokens\":1,\"total_tokens\":3}}}\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	executor := runtimeexecutor.NewOpenAICompatExecutor("openai-compatibility", &innerconfig.Config{
		SDKConfig: innerconfig.SDKConfig{RequestLog: true},
	})
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{
		ID:       "auth-log-sse",
		Provider: executor.Identifier(),
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"base_url":     upstream.URL + "/v1",
			"api_key":      "test-key",
			"compat_name":  "openai-compatibility",
			"provider_key": "openai-compatibility",
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	manager.RefreshSchedulerEntry(auth.ID)
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)

	logDir := t.TempDir()
	requestLogger := logging.NewFileRequestLogger(true, logDir, logDir, 0)

	router := gin.New()
	router.Use(func(c *gin.Context) {
		logging.SetGinRequestID(c, "req-sse-log")
		c.Next()
	})
	router.Use(middleware.RequestLoggingMiddleware(requestLogger))
	router.POST("/v1/responses", h.Responses)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test-model","input":"hello","stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%q", resp.Code, http.StatusOK, resp.Body.String())
	}

	matches, err := filepath.Glob(filepath.Join(logDir, "request-*.log"))
	if err != nil {
		t.Fatalf("glob request logs: %v", err)
	}
	if len(matches) != 1 {
		entries, _ := os.ReadDir(logDir)
		t.Fatalf("request log count = %d, want 1; entries=%v", len(matches), entries)
	}

	record, err := logging.ExtractRequestRecordByID(matches[0], "req-sse-log")
	if err != nil {
		t.Fatalf("extract request log: %v", err)
	}
	logText := string(record)
	if !strings.Contains(logText, "=== RESPONSE ===") {
		t.Fatalf("request log missing response section: %q", logText)
	}
	if !strings.Contains(logText, "event: response.created") {
		t.Fatalf("request log response section lost created event line: %q", logText)
	}
	if !strings.Contains(logText, "event: response.completed") {
		t.Fatalf("request log response section lost completed event line: %q", logText)
	}
}
