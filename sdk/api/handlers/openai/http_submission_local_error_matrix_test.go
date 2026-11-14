package openai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestHTTPSubmissionAcceptedLocalFailureMatrix(t *testing.T) {
	const geminiBadTool = `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"functions__apply_patch","args":{"input":1}}}]},"finishReason":"STOP"}]}`
	const openaiBadTool = `{"id":"synthetic","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call_synthetic","type":"function","function":{"name":"functions__apply_patch","arguments":"{\"input\":1}"}}]},"finish_reason":"tool_calls"}]}`
	for _, tc := range []struct {
		name      string
		model     string
		executor  coreauth.ProviderExecutor
		payload   string
		encoding  string
		mediaType string
	}{
		{"gemini_tool_translation", "gemini-2.5-flash", runtimeexecutor.NewGeminiExecutor(&config.Config{}), geminiBadTool, "", "application/json"},
		{"vertex_tool_translation", "gemini-2.5-flash", runtimeexecutor.NewGeminiVertexExecutor(&config.Config{}), geminiBadTool, "", "application/json"},
		{"antigravity_tool_translation", "gemini-2.5-flash", runtimeexecutor.NewAntigravityExecutor(&config.Config{}), `{"response":` + geminiBadTool + `}`, "", "application/json"},
		{"openai_tool_translation", "local-error-openai", runtimeexecutor.NewOpenAICompatExecutor("local-error-openai", &config.Config{}), openaiBadTool, "", "application/json"},
		{"claude_buffered_validation", "claude-opus-5", runtimeexecutor.NewClaudeExecutor(&config.Config{}), "data: {\"type\":\"message_start\",\"message\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n", "", "text/event-stream"},
		{"codex_response_decoder", "gpt-5.2-codex", runtimeexecutor.NewCodexExecutor(&config.Config{}), `{}`, "synthetic-unsupported", "application/json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var submissions atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if _, errRead := io.Copy(io.Discard, r.Body); errRead != nil {
					t.Error(errRead)
					return
				}
				if r.Method != http.MethodPost {
					t.Errorf("unexpected upstream method=%s", r.Method)
				}
				submissions.Add(1)
				w.Header().Set("Content-Type", tc.mediaType)
				if tc.encoding != "" {
					w.Header().Set("Content-Encoding", tc.encoding)
				}
				_, _ = io.WriteString(w, tc.payload)
			}))
			defer upstream.Close()
			manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
			manager.RegisterExecutor(tc.executor)
			for _, suffix := range []string{"a", "b"} {
				auth := &coreauth.Auth{ID: t.Name() + suffix, Provider: tc.executor.Identifier(), Status: coreauth.StatusActive,
					ProxyURL: "direct", Attributes: map[string]string{"base_url": upstream.URL, "api_key": "synthetic"},
					Metadata: map[string]any{"access_token": "synthetic", "project_id": "synthetic", "expired": time.Now().Add(time.Hour).Format(time.RFC3339)}}
				if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
					t.Fatal(errRegister)
				}
				registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: tc.model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			}
			base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
			router := gin.New()
			router.POST("/v1/responses", NewOpenAIResponsesAPIHandler(base).Responses)
			body := ojson.NewDocString(`{"model":"","input":"make a patch","stream":false,"tools":[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"apply_patch","format":{"type":"grammar","definition":"start: patch"}}]}]}`)
			if errSet := body.Set("model", tc.model); errSet != nil {
				t.Fatal(errSet)
			}
			request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body.String()))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			t.Logf("accepted=%d status=%d retry=%q body=%s", submissions.Load(), response.Code, response.Header().Get("x-should-retry"), response.Body.String())
			if got := submissions.Load(); got != 1 {
				t.Errorf("accepted upstream POSTs=%d, want 1", got)
			}
			if response.Code != http.StatusBadGateway || ojson.Get(response.Body.String(), "error.code").String() != "upstream_outcome_unknown" || ojson.Get(response.Body.String(), "error.retryable").Raw != "false" || response.Header().Get("x-should-retry") != "false" {
				t.Fatal("local executor failure lost the nonretryable unknown outcome")
			}
		})
	}
}
