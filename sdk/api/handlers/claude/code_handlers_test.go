package claude

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

type claudeMessagesCaptureExecutor struct {
	mu      sync.Mutex
	payload []byte
}

func (e *claudeMessagesCaptureExecutor) Identifier() string { return "test-provider" }

func (e *claudeMessagesCaptureExecutor) Execute(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	e.mu.Lock()
	e.payload = bytes.Clone(req.Payload)
	e.mu.Unlock()
	return coreexecutor.Response{Payload: []byte(`{"type":"message","content":[]}`)}, nil
}

func (e *claudeMessagesCaptureExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.mu.Lock()
	e.payload = bytes.Clone(req.Payload)
	e.mu.Unlock()
	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: []byte("event: message_stop\ndata: {}\n\n")}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *claudeMessagesCaptureExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *claudeMessagesCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *claudeMessagesCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestClaudeMessagesRewritesLongCallIDsBeforeUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &claudeMessagesCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: "auth-claude", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "claude-test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewClaudeCodeAPIHandler(base)
	router := gin.New()
	router.POST("/v1/messages", h.ClaudeMessages)

	longCallID := "mcp__synthpilot__connect_hardware_server-1776264133559318385-226896"
	if len(longCallID) <= 64 {
		t.Fatalf("test setup error: longCallID len = %d, want > 64", len(longCallID))
	}
	body := `{"model":"claude-test-model","stream":false,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"` + longCallID + `","name":"mcp__synthpilot__connect_hardware_server","input":{}},{"type":"tool_result","tool_use_id":"` + longCallID + `","content":"ok"}]}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
	}

	executor.mu.Lock()
	captured := bytes.Clone(executor.payload)
	executor.mu.Unlock()
	if len(captured) == 0 {
		t.Fatalf("no upstream payload captured")
	}
	msgContent := gjson.GetBytes(captured, "messages.0.content").Array()
	if len(msgContent) != 2 {
		t.Fatalf("captured content len = %d, want 2: %s", len(msgContent), captured)
	}
	id0 := msgContent[0].Get("id").String()
	id1 := msgContent[1].Get("tool_use_id").String()
	if id0 == longCallID || id1 == longCallID {
		t.Fatalf("claude messages handler left long tool id in upstream payload: %s", captured)
	}
	if len(id0) > 64 || len(id1) > 64 {
		t.Fatalf("captured tool id exceeds 64 chars: %q / %q", id0, id1)
	}
	if id0 == "" || id1 == "" || id0 != id1 {
		t.Fatalf("captured tool id mismatch: %q / %q", id0, id1)
	}
	if !strings.HasPrefix(id0, "mcp__synthpilot__connect_hardware_server_") {
		t.Fatalf("captured tool id should keep readable tool-name prefix, got %q", id0)
	}
}
