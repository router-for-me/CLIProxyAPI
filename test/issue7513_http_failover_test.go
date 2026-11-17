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
	claudehandlers "github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/claude"
	openaihandlers "github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestIssue7513_ClaudeAndOpenAIEndpointsRotateAndExhaustTo502(t *testing.T) {
	gin.SetMode(gin.TestMode)

	endpoints := []struct {
		name         string
		path         string
		finishReason string
		streamBody   string
		nonStream    string
		wantSubstr   string
	}{
		{
			name:         "claude_messages",
			path:         "/v1/messages",
			finishReason: "MALFORMED_FUNCTION_CALL",
			streamBody:   `{"model":"MODEL","max_tokens":1024,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			nonStream:    `{"model":"MODEL","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`,
			wantSubstr:   "recovered via vertex",
		},
		{
			name:         "openai_chat_completions",
			path:         "/v1/chat/completions",
			finishReason: "UNEXPECTED_TOOL_CALL",
			streamBody:   `{"model":"MODEL","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			nonStream:    `{"model":"MODEL","messages":[{"role":"user","content":"hi"}]}`,
			wantSubstr:   "recovered via vertex",
		},
		{
			name:         "openai_responses",
			path:         "/v1/responses",
			finishReason: "MALFORMED_RESPONSE",
			streamBody:   `{"model":"MODEL","stream":true,"input":"hi"}`,
			nonStream:    `{"model":"MODEL","input":"hi"}`,
			wantSubstr:   "recovered via vertex",
		},
	}

	for _, ep := range endpoints {
		t.Run(ep.name, func(t *testing.T) {
			var agCalls atomic.Int32
			agServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				agCalls.Add(1)
				body := `{"response":{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"` + ep.finishReason + `"}],"usageMetadata":{"promptTokenCount":50,"candidatesTokenCount":1,"totalTokenCount":51},"modelVersion":"gemini-4-hydrogen"}}`
				if strings.Contains(r.URL.Path, "streamGenerateContent") {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: "+body+"\n\n")
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, body)
			}))
			defer agServer.Close()

			var vertexCalls atomic.Int32
			vertexServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				vertexCalls.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"recovered via vertex"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":50,"candidatesTokenCount":4,"totalTokenCount":54},"modelVersion":"gemini-4-hydrogen"}`+"\n\n")
			}))
			defer vertexServer.Close()

			model := "gemini-4-hydrogen-7513-" + ep.name
			cfg := &config.Config{}

			// Subcase 1: Antigravity zero-output rotates cleanly to healthy Vertex without committing HTTP 200 prematurely.
			manager := coreauth.NewManager(nil, nil, nil)
			affinity := coreauth.NewSessionAffinitySelectorWithConfig(coreauth.SessionAffinityConfig{
				Fallback: &coreauth.RoundRobinSelector{},
				TTL:      time.Hour,
				RebindOn: coreauth.SessionAffinityRebindOnQuota,
			})
			defer affinity.Stop()
			manager.SetSelector(affinity)
			manager.RegisterExecutor(runtimeexecutor.NewAntigravityExecutor(cfg))
			manager.RegisterExecutor(runtimeexecutor.NewGeminiVertexExecutor(cfg))

			agAuth := &coreauth.Auth{
				ID:       "ag-fail-" + ep.name,
				Provider: "antigravity",
				Status:   coreauth.StatusActive,
				Metadata: map[string]any{
					"access_token": "token-ag",
					"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
					"project_id":   "project-ag",
				},
				Attributes: map[string]string{
					"base_url": agServer.URL,
					"priority": "10",
				},
			}
			vertexAuth := &coreauth.Auth{
				ID:       "vertex-ok-" + ep.name,
				Provider: "vertex",
				Status:   coreauth.StatusActive,
				Attributes: map[string]string{
					"api_key":  "vertex-key",
					"base_url": vertexServer.URL,
					"priority": "1",
				},
			}
			for _, a := range []*coreauth.Auth{agAuth, vertexAuth} {
				if _, errReg := manager.Register(context.Background(), a); errReg != nil {
					t.Fatalf("Register(%s) error = %v", a.ID, errReg)
				}
				registry.GetGlobalRegistry().RegisterClient(a.ID, a.Provider, []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() {
					registry.GetGlobalRegistry().UnregisterClient(a.ID)
				})
			}

			base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
			claudeH := claudehandlers.NewClaudeCodeAPIHandler(base)
			openaiH := openaihandlers.NewOpenAIAPIHandler(base)
			responsesH := openaihandlers.NewOpenAIResponsesAPIHandler(base)

			router := gin.New()
			router.POST("/v1/messages", claudeH.ClaudeMessages)
			router.POST("/v1/chat/completions", openaiH.ChatCompletions)
			router.POST("/v1/responses", responsesH.Responses)

			streamPayload := strings.ReplaceAll(ep.streamBody, "MODEL", model)
			req := httptest.NewRequest(http.MethodPost, ep.path, strings.NewReader(streamPayload))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("failover status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), ep.wantSubstr) {
				t.Fatalf("failover body missing %q: %s", ep.wantSubstr, rec.Body.String())
			}
			if agCalls.Load() != 1 || vertexCalls.Load() != 1 {
				t.Fatalf("calls = (ag=%d, vertex=%d), want (1, 1)", agCalls.Load(), vertexCalls.Load())
			}

			// Subcase 2: When all credentials return zero-output abnormal finish, both stream and non-stream return HTTP 502.
			exhaustedStreamModel := model + "-exhausted-stream"
			exhaustedNonStreamModel := model + "-exhausted-nonstream"
			exhaustedManager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
			exhaustedManager.RegisterExecutor(runtimeexecutor.NewAntigravityExecutor(cfg))
			for _, suffix := range []string{"a", "b"} {
				failAuth := &coreauth.Auth{
					ID:       "ag-exhausted-" + ep.name + "-" + suffix,
					Provider: "antigravity",
					Status:   coreauth.StatusActive,
					Metadata: map[string]any{
						"access_token": "token-ag-" + suffix,
						"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
						"project_id":   "project-ag",
					},
					Attributes: map[string]string{"base_url": agServer.URL},
				}
				if _, errReg := exhaustedManager.Register(context.Background(), failAuth); errReg != nil {
					t.Fatalf("Register(%s) error = %v", failAuth.ID, errReg)
				}
				registry.GetGlobalRegistry().RegisterClient(failAuth.ID, failAuth.Provider, []*registry.ModelInfo{
					{ID: exhaustedStreamModel},
					{ID: exhaustedNonStreamModel},
				})
				t.Cleanup(func() {
					registry.GetGlobalRegistry().UnregisterClient(failAuth.ID)
				})
			}

			exhaustedBase := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, exhaustedManager)
			exhaustedRouter := gin.New()
			exhaustedRouter.POST("/v1/messages", claudehandlers.NewClaudeCodeAPIHandler(exhaustedBase).ClaudeMessages)
			exhaustedRouter.POST("/v1/chat/completions", openaihandlers.NewOpenAIAPIHandler(exhaustedBase).ChatCompletions)
			exhaustedRouter.POST("/v1/responses", openaihandlers.NewOpenAIResponsesAPIHandler(exhaustedBase).Responses)

			beforeAgCalls := agCalls.Load()
			exStreamReq := httptest.NewRequest(http.MethodPost, ep.path, strings.NewReader(strings.ReplaceAll(ep.streamBody, "MODEL", exhaustedStreamModel)))
			exStreamReq.Header.Set("Content-Type", "application/json")
			exStreamRec := httptest.NewRecorder()
			exhaustedRouter.ServeHTTP(exStreamRec, exStreamReq)
			if exStreamRec.Code != http.StatusBadGateway {
				t.Fatalf("exhausted stream status = %d, want 502; body=%s", exStreamRec.Code, exStreamRec.Body.String())
			}
			if gotCalls := agCalls.Load() - beforeAgCalls; gotCalls != 2 {
				t.Fatalf("expected exhausted stream to rotate across both credentials (2 calls), got %d", gotCalls)
			}

			beforeNonStreamCalls := agCalls.Load()
			exNonStreamReq := httptest.NewRequest(http.MethodPost, ep.path, strings.NewReader(strings.ReplaceAll(ep.nonStream, "MODEL", exhaustedNonStreamModel)))
			exNonStreamReq.Header.Set("Content-Type", "application/json")
			exNonStreamRec := httptest.NewRecorder()
			exhaustedRouter.ServeHTTP(exNonStreamRec, exNonStreamReq)
			if exNonStreamRec.Code != http.StatusBadGateway {
				t.Fatalf("exhausted non-stream status = %d, want 502; body=%s", exNonStreamRec.Code, exNonStreamRec.Body.String())
			}
			if gotCalls := agCalls.Load() - beforeNonStreamCalls; gotCalls != 2 {
				t.Fatalf("expected exhausted non-stream to rotate across both credentials (2 calls), got %d", gotCalls)
			}
		})
	}
}

func TestIssue7513_ApplyPatchAbnormalEOFWithoutUsageRotatesAndExhaustsTo502(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var agCalls atomic.Int32
	agServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		agCalls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		// Zero-output abnormal finish WITHOUT usageMetadata, immediately followed by EOF.
		_, _ = io.WriteString(w, `data: {"response":{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"modelVersion":"gemini-4-hydrogen"}}`+"\n\n")
	}))
	defer agServer.Close()

	var vertexCalls atomic.Int32
	vertexServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		vertexCalls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"apply_patch fallback ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":3,"totalTokenCount":23},"modelVersion":"gemini-4-hydrogen"}`+"\n\n")
	}))
	defer vertexServer.Close()

	cfg := &config.Config{}
	model := "gemini-4-hydrogen-7513-apply-patch"
	patchReq := `{"model":"` + model + `","stream":true,"input":"patch","tools":[{"type":"custom","name":"apply_patch"}]}`

	// Subcase 1: Rotates from Antigravity to healthy Vertex without emitting response.failed HTTP 200.
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	manager.RegisterExecutor(runtimeexecutor.NewAntigravityExecutor(cfg))
	manager.RegisterExecutor(runtimeexecutor.NewGeminiVertexExecutor(cfg))

	agAuth := &coreauth.Auth{
		ID:       "ag-patch-fail",
		Provider: "antigravity",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"access_token": "token-ag",
			"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			"project_id":   "project-ag",
		},
		Attributes: map[string]string{"base_url": agServer.URL, "priority": "10"},
	}
	vertexAuth := &coreauth.Auth{
		ID:         "vertex-patch-ok",
		Provider:   "vertex",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"api_key": "vertex-key", "base_url": vertexServer.URL, "priority": "1"},
	}
	for _, a := range []*coreauth.Auth{agAuth, vertexAuth} {
		if _, errReg := manager.Register(context.Background(), a); errReg != nil {
			t.Fatalf("Register(%s) error = %v", a.ID, errReg)
		}
		registry.GetGlobalRegistry().RegisterClient(a.ID, a.Provider, []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() {
			registry.GetGlobalRegistry().UnregisterClient(a.ID)
		})
	}

	router := gin.New()
	router.POST("/v1/responses", openaihandlers.NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)).Responses)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(patchReq))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "apply_patch fallback ok") {
		t.Fatalf("apply_patch failover = (%d, %s), want 200 with fallback text", rec.Code, rec.Body.String())
	}
	if agCalls.Load() != 1 || vertexCalls.Load() != 1 {
		t.Fatalf("calls = (ag=%d, vertex=%d), want (1, 1)", agCalls.Load(), vertexCalls.Load())
	}

	// Subcase 2: Exhausted apply_patch stream returns HTTP 502 (not HTTP 200 response.failed).
	exhaustedModel := model + "-exhausted"
	exManager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	exManager.RegisterExecutor(runtimeexecutor.NewAntigravityExecutor(cfg))
	exAuth := &coreauth.Auth{
		ID:       "ag-patch-exhausted",
		Provider: "antigravity",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"access_token":    "token-ag",
			"expired":         time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			"project_id":      "project-ag",
			"disable_cooling": true,
		},
		Attributes: map[string]string{"base_url": agServer.URL},
	}
	if _, errReg := exManager.Register(context.Background(), exAuth); errReg != nil {
		t.Fatalf("Register error = %v", errReg)
	}
	registry.GetGlobalRegistry().RegisterClient(exAuth.ID, exAuth.Provider, []*registry.ModelInfo{{ID: exhaustedModel}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(exAuth.ID)
	})

	exRouter := gin.New()
	exRouter.POST("/v1/responses", openaihandlers.NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, exManager)).Responses)
	exReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(strings.ReplaceAll(patchReq, model, exhaustedModel)))
	exReq.Header.Set("Content-Type", "application/json")
	exRec := httptest.NewRecorder()
	exRouter.ServeHTTP(exRec, exReq)
	if exRec.Code != http.StatusBadGateway {
		t.Fatalf("exhausted apply_patch status = %d, want 502; body=%s", exRec.Code, exRec.Body.String())
	}
	if strings.Contains(exRec.Body.String(), "response.failed") {
		t.Fatalf("exhausted apply_patch leaked response.failed instead of clean 502: %s", exRec.Body.String())
	}
}

func TestIssue7513_PartialTextWithMalformedFunctionCallPreservedOverClaudeHTTP(t *testing.T) {
	gin.SetMode(gin.TestMode)

	agServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "streamGenerateContent") {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"partial answer"}]}}],"modelVersion":"gemini-4-hydrogen"}}`+"\n\n")
			_, _ = io.WriteString(w, `data: {"response":{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":3,"totalTokenCount":23},"modelVersion":"gemini-4-hydrogen"}}`+"\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"partial answer"}]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":3,"totalTokenCount":23},"modelVersion":"gemini-4-hydrogen"}}`)
	}))
	defer agServer.Close()

	model := "gemini-4-hydrogen-7513-partial-http"
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	manager.RegisterExecutor(runtimeexecutor.NewAntigravityExecutor(&config.Config{}))
	auth := &coreauth.Auth{
		ID:       "ag-partial-http",
		Provider: "antigravity",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"access_token": "token-ag",
			"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			"project_id":   "project-ag",
		},
		Attributes: map[string]string{"base_url": agServer.URL},
	}
	if _, errReg := manager.Register(context.Background(), auth); errReg != nil {
		t.Fatalf("Register error = %v", errReg)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	router := gin.New()
	router.POST("/v1/messages", claudehandlers.NewClaudeCodeAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)).ClaudeMessages)

	for _, stream := range []bool{false, true} {
		body := `{"model":"` + model + `","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`
		if stream {
			body = `{"model":"` + model + `","max_tokens":1024,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
		}
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "partial answer") {
			t.Fatalf("stream=%v status=%d body=%s, want 200 preserving partial answer", stream, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "event: error\n") {
			t.Fatalf("stream=%v unexpectedly emitted error event: %s", stream, rec.Body.String())
		}
	}
}
