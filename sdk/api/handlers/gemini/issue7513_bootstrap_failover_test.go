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

func TestIssue7513_GeminiStreamRotatesFromAntigravityZeroOutputToHealthyVertex(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var antigravityCalls atomic.Int32
	antigravityServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		antigravityCalls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"response":{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"usageMetadata":{"promptTokenCount":540000,"candidatesTokenCount":10,"thoughtsTokenCount":400,"totalTokenCount":540410},"modelVersion":"gemini-4-hydrogen"}}`+"\n\n")
	}))
	defer antigravityServer.Close()

	var vertexCalls atomic.Int32
	vertexServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		vertexCalls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"recovered from vertex"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":540000,"candidatesTokenCount":4,"totalTokenCount":540004},"modelVersion":"gemini-4-hydrogen"}`+"\n\n")
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

	model := "gemini-4-hydrogen-issue7513-failover"
	agAuth := &coreauth.Auth{
		ID:       "ag-issue7513-primary",
		Provider: "antigravity",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"access_token": "token-ag",
			"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			"project_id":   "project-ag",
		},
		Attributes: map[string]string{
			"base_url": antigravityServer.URL,
			"priority": "10",
		},
	}
	vertexAuth := &coreauth.Auth{
		ID:       "vertex-issue7513-fallback",
		Provider: "vertex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"api_key":  "vertex-test-key",
			"base_url": vertexServer.URL,
			"priority": "1",
		},
	}
	for _, auth := range []*coreauth.Auth{agAuth, vertexAuth} {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", auth.ID, errRegister)
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

	reqBody := `{"contents":[{"role":"user","parts":[{"text":"turn 1"}]},{"role":"model","parts":[{"text":"ok"}]},{"role":"user","parts":[{"text":"turn 2"}]}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/"+model+":streamGenerateContent?alt=sse", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 after failover; body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "recovered from vertex") {
		t.Fatalf("expected failover response from vertex, got: %s", recorder.Body.String())
	}
	if antigravityCalls.Load() != 1 || vertexCalls.Load() != 1 {
		t.Fatalf("calls = (antigravity=%d, vertex=%d), want (1, 1)", antigravityCalls.Load(), vertexCalls.Load())
	}
}

func TestIssue7513_GeminiStreamAndNonStreamReturnHTTP502WhenRetriesExhausted(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var upstreamCalls atomic.Int32
	antigravityServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		if strings.Contains(r.URL.Path, "streamGenerateContent") {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, `data: {"response":{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"usageMetadata":{"promptTokenCount":540000,"candidatesTokenCount":10,"thoughtsTokenCount":400,"totalTokenCount":540410},"modelVersion":"gemini-4-hydrogen"}}`+"\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"response":{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"usageMetadata":{"promptTokenCount":540000,"candidatesTokenCount":10,"thoughtsTokenCount":400,"totalTokenCount":540410},"modelVersion":"gemini-4-hydrogen"}}`)
	}))
	defer antigravityServer.Close()

	cfg := &config.Config{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor.NewAntigravityExecutor(cfg))

	model := "gemini-4-hydrogen-issue7513-exhausted"
	agAuth := &coreauth.Auth{
		ID:       "ag-issue7513-only",
		Provider: "antigravity",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"access_token":    "token-ag",
			"expired":         time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			"project_id":      "project-ag",
			"disable_cooling": true,
		},
		Attributes: map[string]string{"base_url": antigravityServer.URL},
	}
	if _, errRegister := manager.Register(context.Background(), agAuth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(agAuth.ID, agAuth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(agAuth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		Streaming: sdkconfig.StreamingConfig{
			BootstrapRetries: 1,
		},
	}, manager)
	h := NewGeminiAPIHandler(base)
	router := gin.New()
	router.POST("/v1beta/models/*action", h.GeminiHandler)

	streamReq := httptest.NewRequest(http.MethodPost, "/v1beta/models/"+model+":streamGenerateContent?alt=sse", strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
	streamReq.Header.Set("Content-Type", "application/json")
	streamRec := httptest.NewRecorder()
	router.ServeHTTP(streamRec, streamReq)
	if streamRec.Code != http.StatusBadGateway {
		t.Fatalf("stream status = %d, want 502; body=%s", streamRec.Code, streamRec.Body.String())
	}
	if upstreamCalls.Load() < 2 {
		t.Fatalf("expected bootstrap retry to retry upstream at least once, got %d calls", upstreamCalls.Load())
	}

	nonStreamReq := httptest.NewRequest(http.MethodPost, "/v1beta/models/"+model+":generateContent", strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
	nonStreamReq.Header.Set("Content-Type", "application/json")
	nonStreamRec := httptest.NewRecorder()
	router.ServeHTTP(nonStreamRec, nonStreamReq)
	if nonStreamRec.Code != http.StatusBadGateway {
		t.Fatalf("non-stream status = %d, want 502; body=%s", nonStreamRec.Code, nonStreamRec.Body.String())
	}
}
