package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func TestResponsesWebsocketSameUpstreamRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, provider := range []string{"codex", "xai"} {
		for _, scenario := range []string{"recovers", "exhausted", "after-payload"} {
			t.Run(provider+"/"+scenario, func(t *testing.T) {
				var attempts atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
					if errUpgrade != nil {
						t.Error(errUpgrade)
						return
					}
					defer func() { _ = conn.Close() }()
					if _, _, errRead := conn.ReadMessage(); errRead != nil {
						return
					}
					attempt := attempts.Add(1)
					if scenario == "exhausted" || (scenario == "recovers" && attempt < 3) {
						return
					}
					_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"retry-response","status":"in_progress"}}`))
					if scenario == "after-payload" {
						return
					}
					_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"retry-response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`))
					_, _, _ = conn.ReadMessage()
				}))
				defer upstream.Close()
				cfg := &config.Config{SameUpstreamRetry: 2}
				cfg.Codex.StreamBootstrapBuffering = scenario != "after-payload"
				manager := coreauth.NewManager(nil, nil, nil)
				manager.SetConfig(cfg)
				manager.SetRetryConfig(0, 0, 1)
				if provider == "codex" {
					manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))
				} else {
					manager.RegisterExecutor(runtimeexecutor.NewXAIAutoExecutor(cfg))
				}
				authID, model := t.Name(), "same-upstream-"+provider
				if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
					ID: authID, Provider: provider, Status: coreauth.StatusActive,
					Attributes: map[string]string{"api_key": "test-key", "base_url": upstream.URL, "websockets": "true"},
				}); errRegister != nil {
					t.Fatal(errRegister)
				}
				registry.GetGlobalRegistry().RegisterClient(authID, provider, []*registry.ModelInfo{{ID: model}})
				defer registry.GetGlobalRegistry().UnregisterClient(authID)
				handler := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
				router := gin.New()
				router.GET("/v1/responses", handler.ResponsesWebsocket)
				downstream := httptest.NewServer(router)
				defer downstream.Close()
				conn, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(downstream.URL, "http")+"/v1/responses", nil)
				if errDial != nil {
					t.Fatal(errDial)
				}
				defer func() { _ = conn.Close() }()
				_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
				request := fmt.Sprintf(`{"type":"response.create","model":%q,"input":[]}`, model)
				if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(request)); errWrite != nil {
					t.Fatal(errWrite)
				}
				completed := false
				for {
					_, payload, errRead := conn.ReadMessage()
					if errRead != nil {
						var closeErr *websocket.CloseError
						if !errors.As(errRead, &closeErr) {
							t.Fatal(errRead)
						}
						break
					}
					if gjson.GetBytes(payload, "type").String() == "response.completed" {
						completed = true
						break
					}
				}
				wantAttempts := int32(3)
				if scenario == "after-payload" {
					wantAttempts = 1
				}
				if attempts.Load() != wantAttempts || completed != (scenario == "recovers") {
					t.Fatalf("attempts=%d completed=%t, want attempts=%d completed=%t", attempts.Load(), completed, wantAttempts, scenario == "recovers")
				}
			})
		}
	}
}
