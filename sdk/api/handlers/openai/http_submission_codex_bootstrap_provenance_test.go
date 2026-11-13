package openai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestHTTPSubmissionCodexBootstrapPreservesProviderProvenance(t *testing.T) {
	for _, tc := range []struct {
		name     string
		payload  string
		provider bool
	}{
		{"malformed_overload", `{"type":"response.failed","response":{"error":{"code":"server_is_overloaded"}}`, false},
		{"parsed_overload", `{"type":"response.failed","response":{"error":{"code":"server_is_overloaded"}}}`, true},
		{"top_level_provider_message", `{"type":"error","message":"provider overloaded"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var submissions atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				if submissions.Add(1) == 1 {
					_, _ = io.WriteString(w, "data: "+tc.payload+"\n\n")
					return
				}
				_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic\",\"object\":\"response\",\"status\":\"completed\",\"output\":[{\"id\":\"msg_synthetic\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"done\"}]}]}}\n\n")
			}))
			defer upstream.Close()
			cfg := &config.Config{}
			cfg.Codex.StreamBootstrapBuffering = true
			manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
			manager.RegisterExecutor(runtimeexecutor.NewCodexExecutor(cfg))
			const model = "gpt-5.2-codex"
			for _, suffix := range []string{"a", "b"} {
				auth := &coreauth.Auth{ID: t.Name() + suffix, Provider: "codex", Status: coreauth.StatusActive,
					ProxyURL: "direct", Attributes: map[string]string{"base_url": upstream.URL, "api_key": "synthetic"}}
				if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
					t.Fatal(errRegister)
				}
				registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			}
			base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
			router := gin.New()
			router.POST("/v1/responses", NewOpenAIResponsesAPIHandler(base).Responses)
			request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.2-codex","input":"hello","stream":true}`))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			t.Logf("accepted=%d status=%d retry=%q body=%s", submissions.Load(), response.Code, response.Header().Get("x-should-retry"), response.Body.String())
			if tc.provider {
				if submissions.Load() != 2 || response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "done") {
					t.Fatal("parsed provider error lost failover")
				}
				return
			}
			if submissions.Load() != 1 || response.Code != http.StatusBadGateway || ojson.Get(response.Body.String(), "error.code").String() != "upstream_outcome_unknown" || response.Header().Get("x-should-retry") != "false" {
				t.Fatal("malformed provider bytes regained replay permission")
			}
		})
	}
}
