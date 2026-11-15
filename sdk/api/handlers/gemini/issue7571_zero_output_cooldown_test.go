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
	antigravityServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqBytes, _ := io.ReadAll(r.Body)
		isMalformedPrompt := strings.Contains(string(reqBytes), "trigger-malformed-tool-call")
		isStream := strings.Contains(r.URL.Path, "streamGenerateContent")

		if isMalformedPrompt {
			malformedCalls.Add(1)
			payload := `{"response":{"candidates":[{"content":{"role":"model","parts":[{"thoughtSignature":"c2ln","text":""}]},"finishReason":"MALFORMED_FUNCTION_CALL","finishMessage":"Malformed function call: run_cmd()"}],"usageMetadata":{"promptTokenCount":25,"candidatesTokenCount":1,"totalTokenCount":26},"modelVersion":"gemini-4-hydrogen"}}`
			if isStream {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: "+payload+"\n\n")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, payload)
			return
		}

		healthyCalls.Add(1)
		payload := `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"healthy session ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":3,"totalTokenCount":15},"modelVersion":"gemini-4-hydrogen"}}`
		if isStream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: "+payload+"\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, payload)
	}))
	defer antigravityServer.Close()

	cfg := &config.Config{DisableCooling: false}
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	manager.SetConfig(cfg)
	manager.SetRetryConfig(3, 30*time.Second, 0)
	manager.RegisterExecutor(executor.NewAntigravityExecutor(cfg))

	model := "gemini-4-hydrogen-issue7571"
	for _, suffix := range []string{"1", "2"} {
		agAuth := &coreauth.Auth{
			ID:       "ag-issue7571-" + suffix,
			Provider: "antigravity",
			Status:   coreauth.StatusActive,
			Metadata: map[string]any{
				"access_token": "token-ag-" + suffix,
				"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
				"project_id":   "project-ag-" + suffix,
			},
			Attributes: map[string]string{"base_url": antigravityServer.URL},
		}
		if _, errRegister := manager.Register(context.Background(), agAuth); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", agAuth.ID, errRegister)
		}
		authID := agAuth.ID
		registry.GetGlobalRegistry().RegisterClient(authID, agAuth.Provider, []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() {
			registry.GetGlobalRegistry().UnregisterClient(authID)
		})
	}

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewGeminiAPIHandler(base)
	router := gin.New()
	router.POST("/v1beta/models/*action", h.GeminiHandler)

	// 1. Problematic non-streaming request rotates across both credentials and fails with HTTP 502.
	badNonStream := httptest.NewRequest(http.MethodPost, "/v1beta/models/"+model+":generateContent", strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"trigger-malformed-tool-call"}]}]}`))
	badNonStream.Header.Set("Content-Type", "application/json")
	badNonStreamRec := httptest.NewRecorder()
	router.ServeHTTP(badNonStreamRec, badNonStream)
	if badNonStreamRec.Code != http.StatusBadGateway {
		t.Fatalf("bad non-stream status = %d, want 502; body=%s", badNonStreamRec.Code, badNonStreamRec.Body.String())
	}
	if malformedCalls.Load() != 2 {
		t.Fatalf("expected non-stream zero-output request to rotate across both credentials (2 calls), got %d", malformedCalls.Load())
	}

	// 2. Unrelated healthy streaming session immediately afterward must NOT hit 503 auth_unavailable.
	okStream := httptest.NewRequest(http.MethodPost, "/v1beta/models/"+model+":streamGenerateContent?alt=sse", strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"normal question"}]}]}`))
	okStream.Header.Set("Content-Type", "application/json")
	okStreamRec := httptest.NewRecorder()
	router.ServeHTTP(okStreamRec, okStream)
	if okStreamRec.Code != http.StatusOK {
		t.Fatalf("unrelated healthy stream status = %d, want 200 (must not cool credentials into 503 auth_unavailable); body=%s", okStreamRec.Code, okStreamRec.Body.String())
	}
	if !strings.Contains(okStreamRec.Body.String(), "healthy session ok") {
		t.Fatalf("healthy stream missing expected output: %s", okStreamRec.Body.String())
	}

	// 3. Problematic streaming request also rotates across both credentials and fails with HTTP 502 without cooling.
	beforeMalformed := malformedCalls.Load()
	badStream := httptest.NewRequest(http.MethodPost, "/v1beta/models/"+model+":streamGenerateContent?alt=sse", strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"trigger-malformed-tool-call"}]}]}`))
	badStream.Header.Set("Content-Type", "application/json")
	badStreamRec := httptest.NewRecorder()
	router.ServeHTTP(badStreamRec, badStream)
	if badStreamRec.Code != http.StatusBadGateway {
		t.Fatalf("bad stream status = %d, want 502; body=%s", badStreamRec.Code, badStreamRec.Body.String())
	}
	if got := malformedCalls.Load() - beforeMalformed; got != 2 {
		t.Fatalf("expected stream zero-output request to rotate across both credentials (2 calls), got %d", got)
	}

	// 4. Unrelated healthy non-streaming session immediately afterward also succeeds with HTTP 200.
	okNonStream := httptest.NewRequest(http.MethodPost, "/v1beta/models/"+model+":generateContent", strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"normal question 2"}]}]}`))
	okNonStream.Header.Set("Content-Type", "application/json")
	okNonStreamRec := httptest.NewRecorder()
	router.ServeHTTP(okNonStreamRec, okNonStream)
	if okNonStreamRec.Code != http.StatusOK {
		t.Fatalf("unrelated healthy non-stream status = %d, want 200; body=%s", okNonStreamRec.Code, okNonStreamRec.Body.String())
	}
	if healthyCalls.Load() != 2 {
		t.Fatalf("healthy upstream calls = %d, want 2", healthyCalls.Load())
	}
}
