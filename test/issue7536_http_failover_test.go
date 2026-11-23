package test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	openaihandlers "github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestIssue7536_SignatureOnlyZeroOutputNoFinishReasonRotatesAndExhaustsTo502(t *testing.T) {
	gin.SetMode(gin.TestMode)

	zeroOutputSSE := `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"thoughtSignature":"opaque-sig-7536","text":""}]}}],"usageMetadata":{"promptTokenCount":378515,"cachedContentTokenCount":376466,"totalTokenCount":378515},"modelVersion":"gemini-4-hydrogen","responseId":"resp-7536"},"traceId":"trace-7536","metadata":{}}` + "\n\n"

	var badCalls atomic.Int32
	badServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		badCalls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, zeroOutputSSE)
	}))
	defer badServer.Close()

	var okCalls atomic.Int32
	okServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		okCalls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"recovered on second credential"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":378515,"candidatesTokenCount":5,"totalTokenCount":378520},"modelVersion":"gemini-4-hydrogen"}}`+"\n\n")
	}))
	defer okServer.Close()

	model := "gemini-4-hydrogen-7536"
	cfg := &config.Config{}

	// 1. Credential 1 returns signature-only zero-output EOF -> rotates to Credential 2 and returns 200 with real output.
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	manager.RegisterExecutor(runtimeexecutor.NewAntigravityExecutor(cfg))

	badAuth := &coreauth.Auth{
		ID:       "ag-7536-bad",
		Provider: "antigravity",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"access_token": "token-bad",
			"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			"project_id":   "project-bad",
		},
		Attributes: map[string]string{
			"base_url": badServer.URL,
			"priority": "10",
		},
	}
	okAuth := &coreauth.Auth{
		ID:       "ag-7536-ok",
		Provider: "antigravity",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"access_token": "token-ok",
			"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			"project_id":   "project-ok",
		},
		Attributes: map[string]string{
			"base_url": okServer.URL,
			"priority": "1",
		},
	}
	for _, a := range []*coreauth.Auth{badAuth, okAuth} {
		if _, errReg := manager.Register(context.Background(), a); errReg != nil {
			t.Fatalf("Register(%s) error = %v", a.ID, errReg)
		}
		registry.GetGlobalRegistry().RegisterClient(a.ID, a.Provider, []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() {
			registry.GetGlobalRegistry().UnregisterClient(a.ID)
		})
	}

	router := gin.New()
	router.POST("/v1/chat/completions", openaihandlers.NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)).ChatCompletions)

	reqBody := `{"model":"` + model + `","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("failover status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "recovered on second credential") {
		t.Fatalf("expected failover response from second credential, got: %s", rec.Body.String())
	}
	if badCalls.Load() != 1 || okCalls.Load() != 1 {
		t.Fatalf("calls = (bad=%d, ok=%d), want (1, 1)", badCalls.Load(), okCalls.Load())
	}

	// 2. When all credentials return signature-only zero-output EOF, return HTTP 502 (never HTTP 200 + finish_reason:"stop").
	exhaustedModel := model + "-exhausted"
	exhaustedManager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	exhaustedManager.RegisterExecutor(runtimeexecutor.NewAntigravityExecutor(cfg))
	for _, suffix := range []string{"a", "b"} {
		a := &coreauth.Auth{
			ID:       "ag-7536-exhausted-" + suffix,
			Provider: "antigravity",
			Status:   coreauth.StatusActive,
			Metadata: map[string]any{
				"access_token": "token-" + suffix,
				"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
				"project_id":   "project-" + suffix,
			},
			Attributes: map[string]string{"base_url": badServer.URL},
		}
		if _, errReg := exhaustedManager.Register(context.Background(), a); errReg != nil {
			t.Fatalf("Register(%s) error = %v", a.ID, errReg)
		}
		registry.GetGlobalRegistry().RegisterClient(a.ID, a.Provider, []*registry.ModelInfo{{ID: exhaustedModel}})
		t.Cleanup(func() {
			registry.GetGlobalRegistry().UnregisterClient(a.ID)
		})
	}

	exhaustedRouter := gin.New()
	exhaustedRouter.POST("/v1/chat/completions", openaihandlers.NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		Streaming: sdkconfig.StreamingConfig{BootstrapRetries: 1},
	}, exhaustedManager)).ChatCompletions)

	beforeCalls := badCalls.Load()
	exReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"`+exhaustedModel+`","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	exReq.Header.Set("Content-Type", "application/json")
	exRec := httptest.NewRecorder()
	exhaustedRouter.ServeHTTP(exRec, exReq)

	if exRec.Code != http.StatusBadGateway {
		t.Fatalf("exhausted status = %d, want 502; body=%s", exRec.Code, exRec.Body.String())
	}
	if strings.Contains(exRec.Body.String(), `"finish_reason":"stop"`) {
		t.Fatalf("exhausted response must not emit finish_reason:stop: %s", exRec.Body.String())
	}
	if got := badCalls.Load() - beforeCalls; got < 2 {
		t.Fatalf("expected credential rotation & bootstrap retry (>=2 calls), got %d", got)
	}
}
