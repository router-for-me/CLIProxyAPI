package claude

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoeForge/moego/json"

	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type requestBodyCaptureExecutor struct {
	calls   int
	payload []byte
}

func (e *requestBodyCaptureExecutor) Identifier() string { return "claude-request-body-test" }

func (e *requestBodyCaptureExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	e.calls++
	e.payload = append([]byte(nil), req.Payload...)
	return coreexecutor.Response{Payload: []byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)}, nil
}

func (e *requestBodyCaptureExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func (e *requestBodyCaptureExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *requestBodyCaptureExecutor) CountTokens(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	e.calls++
	e.payload = append([]byte(nil), req.Payload...)
	return coreexecutor.Response{Payload: []byte(`{"input_tokens":3}`)}, nil
}

func (e *requestBodyCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func zstdCompress(t *testing.T, body string) []byte {
	t.Helper()
	var compressed bytes.Buffer
	encoder, err := zstd.NewWriter(&compressed)
	if err != nil {
		t.Fatalf("zstd.NewWriter: %v", err)
	}
	if _, errWrite := encoder.Write([]byte(body)); errWrite != nil {
		t.Fatalf("zstd write: %v", errWrite)
	}
	if errClose := encoder.Close(); errClose != nil {
		t.Fatalf("zstd close: %v", errClose)
	}
	return compressed.Bytes()
}

func newRequestBodyTestRouter(t *testing.T, authID string) (*gin.Engine, *requestBodyCaptureExecutor) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	executor := &requestBodyCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: authID, Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "claude-request-body-test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})
	h := NewClaudeCodeAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))
	router := gin.New()
	router.POST("/v1/messages", h.ClaudeMessages)
	router.POST("/v1/messages/count_tokens", h.ClaudeCountTokens)
	return router, executor
}

func TestClaudeMessagesDecodesZstdRequestBody(t *testing.T) {
	router, executor := newRequestBodyTestRouter(t, "claude-request-body-messages")
	body := `{"model":"claude-request-body-test-model","max_tokens":5,"messages":[{"role":"user","content":"` + strings.Repeat("zstd-marker ", 512) + `"}]}`

	compressed := zstdCompress(t, body)
	if len(compressed)*4 > len(body) {
		t.Fatalf("test body did not compress: %d -> %d bytes", len(body), len(compressed))
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(compressed))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "zstd")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if executor.calls != 1 {
		t.Fatalf("executor calls = %d, want 1", executor.calls)
	}
	if !json.Valid(executor.payload) || !strings.Contains(string(executor.payload), "zstd-marker zstd-marker") {
		t.Fatalf("executor payload does not contain the decoded request: %s", executor.payload)
	}
}

func TestClaudeCountTokensDecodesZstdRequestBody(t *testing.T) {
	router, executor := newRequestBodyTestRouter(t, "claude-request-body-count-tokens")
	body := `{"model":"claude-request-body-test-model","messages":[{"role":"user","content":"` + strings.Repeat("zstd-marker ", 512) + `"}]}`

	compressed := zstdCompress(t, body)
	if len(compressed)*4 > len(body) {
		t.Fatalf("test body did not compress: %d -> %d bytes", len(body), len(compressed))
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", bytes.NewReader(compressed))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "zstd")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if executor.calls != 1 {
		t.Fatalf("executor calls = %d, want 1", executor.calls)
	}
	if !json.Valid(executor.payload) || !strings.Contains(string(executor.payload), "zstd-marker zstd-marker") {
		t.Fatalf("executor payload does not contain the decoded request: %s", executor.payload)
	}
}
