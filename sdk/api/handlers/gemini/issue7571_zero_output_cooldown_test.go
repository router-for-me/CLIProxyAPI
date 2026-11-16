package gemini

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
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestIssue7571_ZeroOutputMalformedFunctionCallRotatesWithoutCoolingUnrelatedSessions(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var malformedCalls atomic.Int32
	var healthyCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		bodyText := string(bodyBytes)
		isHealthy := strings.Contains(bodyText, `"functionDeclarations"`)

		if strings.Contains(r.URL.Path, "streamGenerateContent") {
			w.Header().Set("Content-Type", "text/event-stream")
			if isHealthy {
				healthyCalls.Add(1)
				_, _ = io.WriteString(w, `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"healthy stream ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":3,"totalTokenCount":23},"modelVersion":"gemini-4-hydrogen"}}`+"\n\n")
				return
			}
			malformedCalls.Add(1)
			_, _ = io.WriteString(w, `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"thoughtSignature":"sig-only","text":""}]},"finishReason":"MALFORMED_FUNCTION_CALL","finishMessage":"Malformed function call: run_command"}],"usageMetadata":{"promptTokenCount":50,"thoughtsTokenCount":10,"totalTokenCount":60},"modelVersion":"gemini-4-hydrogen"}}`+"\n\n")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if isHealthy {
			healthyCalls.Add(1)
			_, _ = io.WriteString(w, `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"healthy non-stream ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":3,"totalTokenCount":23},"modelVersion":"gemini-4-hydrogen"}}`)
			return
		}
		malformedCalls.Add(1)
		_, _ = io.WriteString(w, `{"response":{"candidates":[{"content":{"role":"model","parts":[{"thoughtSignature":"sig-only","text":""}]},"finishReason":"MALFORMED_FUNCTION_CALL","finishMessage":"Malformed function call: run_command"}],"usageMetadata":{"promptTokenCount":50,"thoughtsTokenCount":10,"totalTokenCount":60},"modelVersion":"gemini-4-hydrogen"}}`)
	}))
	defer upstream.Close()

	for _, scenario := range []struct {
		name           string
		malformedPath  string
		healthyPath    string
		wantHealthyStr string
	}{
		{
			name:           "nonstream_malformed_then_healthy_stream",
			malformedPath:  ":generateContent",
			healthyPath:    ":streamGenerateContent?alt=sse",
			wantHealthyStr: "healthy stream ok",
		},
		{
			name:           "stream_malformed_then_healthy_nonstream",
			malformedPath:  ":streamGenerateContent?alt=sse",
			healthyPath:    ":generateContent",
			wantHealthyStr: "healthy non-stream ok",
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			malformedCalls.Store(0)
			healthyCalls.Store(0)

			cfg := &config.Config{}
			manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
			manager.RegisterExecutor(executor.NewAntigravityExecutor(cfg))

			model := "gemini-4-hydrogen-issue7571-" + scenario.name
			for _, suffix := range []string{"1", "2"} {
				// Deliberately leave disable_cooling unset to match production cooling behavior.
				auth := &coreauth.Auth{
					ID:       "ag-issue7571-" + scenario.name + "-" + suffix,
					Provider: "antigravity",
					Status:   coreauth.StatusActive,
					Metadata: map[string]any{
						"access_token": "token-" + suffix,
						"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
						"project_id":   "project-" + suffix,
					},
					Attributes: map[string]string{"base_url": upstream.URL},
				}
				if _, errReg := manager.Register(context.Background(), auth); errReg != nil {
					t.Fatalf("Register(%s) error = %v", auth.ID, errReg)
				}
				registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() {
					registry.GetGlobalRegistry().UnregisterClient(auth.ID)
				})
			}

			base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
			h := NewGeminiAPIHandler(base)
			router := gin.New()
			router.POST("/v1beta/models/*action", h.GeminiHandler)

			// 1. Problematic request without tools rotates across both credentials and returns 502.
			badReqBody := `{"contents":[{"role":"user","parts":[{"text":"run ls -la"}]}]}`
			badReq := httptest.NewRequest(http.MethodPost, "/v1beta/models/"+model+scenario.malformedPath, strings.NewReader(badReqBody))
			badReq.Header.Set("Content-Type", "application/json")
			badRec := httptest.NewRecorder()
			router.ServeHTTP(badRec, badReq)

			if badRec.Code != http.StatusBadGateway {
				t.Fatalf("malformed status = %d, want 502; body=%s", badRec.Code, badRec.Body.String())
			}
			if malformedCalls.Load() != 2 {
				t.Fatalf("malformed upstream calls = %d, want 2 (rotation across both credentials)", malformedCalls.Load())
			}
			for _, suffix := range []string{"1", "2"} {
				authID := "ag-issue7571-" + scenario.name + "-" + suffix
				updated, ok := manager.GetByID(authID)
				if !ok || updated == nil {
					t.Fatalf("expected auth %s to remain present", authID)
				}
				if updated.Unavailable || !updated.NextRetryAfter.IsZero() {
					t.Fatalf("auth %s unexpectedly cooled: unavailable=%v next_retry_after=%v", authID, updated.Unavailable, updated.NextRetryAfter)
				}
				if state := updated.ModelStates[model]; state != nil && (state.Unavailable || !state.NextRetryAfter.IsZero()) {
					t.Fatalf("auth %s model state unexpectedly cooled: unavailable=%v next_retry_after=%v", authID, state.Unavailable, state.NextRetryAfter)
				}
			}

			// 2. Unrelated healthy session with valid tools must immediately return 200, never 503 auth_unavailable.
			goodReqBody := `{"contents":[{"role":"user","parts":[{"text":"list files"}]}],"tools":[{"functionDeclarations":[{"name":"run_command","description":"run command"}]}]}`
			goodReq := httptest.NewRequest(http.MethodPost, "/v1beta/models/"+model+scenario.healthyPath, strings.NewReader(goodReqBody))
			goodReq.Header.Set("Content-Type", "application/json")
			goodRec := httptest.NewRecorder()
			router.ServeHTTP(goodRec, goodReq)

			if goodRec.Code != http.StatusOK {
				t.Fatalf("healthy session status = %d, want 200 (must not get 503 auth_unavailable); body=%s", goodRec.Code, goodRec.Body.String())
			}
			if !strings.Contains(goodRec.Body.String(), scenario.wantHealthyStr) {
				t.Fatalf("healthy session body missing %q: %s", scenario.wantHealthyStr, goodRec.Body.String())
			}
			if healthyCalls.Load() != 1 {
				t.Fatalf("healthy upstream calls = %d, want 1", healthyCalls.Load())
			}
		})
	}

	t.Run("cross_provider_failover_with_session_affinity_keeps_primary_uncooled", func(t *testing.T) {
		malformedCalls.Store(0)
		healthyCalls.Store(0)

		vertexServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"vertex failover ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":3,"totalTokenCount":23},"modelVersion":"gemini-4-hydrogen"}`+"\n\n")
		}))
		defer vertexServer.Close()

		cfg := &config.Config{}
		manager := coreauth.NewManager(nil, nil, nil)
		affinity := coreauth.NewSessionAffinitySelectorWithConfig(coreauth.SessionAffinityConfig{
			Fallback: &coreauth.RoundRobinSelector{},
			TTL:      time.Hour,
			RebindOn: coreauth.SessionAffinityRebindOnQuota,
		})
		defer affinity.Stop()
		manager.SetSelector(affinity)
		manager.RegisterExecutor(executor.NewAntigravityExecutor(cfg))
		manager.RegisterExecutor(executor.NewGeminiVertexExecutor(cfg))

		model := "gemini-4-hydrogen-issue7571-cross-provider"
		agAuth := &coreauth.Auth{
			ID:       "ag-issue7571-primary",
			Provider: "antigravity",
			Status:   coreauth.StatusActive,
			Metadata: map[string]any{
				"access_token": "token-ag",
				"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
				"project_id":   "project-ag",
			},
			Attributes: map[string]string{
				"base_url": upstream.URL,
				"priority": "10",
			},
		}
		vertexAuth := &coreauth.Auth{
			ID:       "vertex-issue7571-fallback",
			Provider: "vertex",
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"api_key":  "vertex-key",
				"base_url": vertexServer.URL,
				"priority": "1",
			},
		}
		for _, auth := range []*coreauth.Auth{agAuth, vertexAuth} {
			if _, errReg := manager.Register(context.Background(), auth); errReg != nil {
				t.Fatalf("Register(%s) error = %v", auth.ID, errReg)
			}
			registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() {
				registry.GetGlobalRegistry().UnregisterClient(auth.ID)
			})
		}

		base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
		h := NewGeminiAPIHandler(base)
		router := gin.New()
		router.POST("/v1beta/models/*action", h.GeminiHandler)

		// 1. Stream request without tools fails zero-output on Antigravity and fails over to Vertex.
		failoverReq := httptest.NewRequest(http.MethodPost, "/v1beta/models/"+model+":streamGenerateContent?alt=sse", strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"run ls -la"}]}]}`))
		failoverReq.Header.Set("Content-Type", "application/json")
		failoverRec := httptest.NewRecorder()
		router.ServeHTTP(failoverRec, failoverReq)
		if failoverRec.Code != http.StatusOK {
			t.Fatalf("failover status = %d, want 200; body=%s", failoverRec.Code, failoverRec.Body.String())
		}
		if !strings.Contains(failoverRec.Body.String(), "vertex failover ok") {
			t.Fatalf("failover body missing vertex response: %s", failoverRec.Body.String())
		}
		if malformedCalls.Load() != 1 {
			t.Fatalf("antigravity malformed calls = %d, want 1", malformedCalls.Load())
		}

		// 2. Subsequent healthy request with tools still selects higher-priority Antigravity (not cooled).
		goodReq := httptest.NewRequest(http.MethodPost, "/v1beta/models/"+model+":streamGenerateContent?alt=sse", strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"list files"}]}],"tools":[{"functionDeclarations":[{"name":"run_command","description":"run command"}]}]}`))
		goodReq.Header.Set("Content-Type", "application/json")
		goodRec := httptest.NewRecorder()
		router.ServeHTTP(goodRec, goodReq)
		if goodRec.Code != http.StatusOK {
			t.Fatalf("healthy status = %d, want 200; body=%s", goodRec.Code, goodRec.Body.String())
		}
		if !strings.Contains(goodRec.Body.String(), "healthy stream ok") {
			t.Fatalf("expected higher-priority uncooled antigravity to serve healthy request, got: %s", goodRec.Body.String())
		}
		if healthyCalls.Load() != 1 {
			t.Fatalf("antigravity healthy calls = %d, want 1", healthyCalls.Load())
		}
	})
}
