package test

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
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	openaihandlers "github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func TestCodexWebsocketQuotaCooldownBeforeDownstreamClose(t *testing.T) {
	for _, tc := range []struct {
		name, failure string
		started       bool
	}{
		{"error_initial", `{"type":"error","status":429,"error":{"type":"usage_limit_reached","message":"quota exhausted","resets_in_seconds":600,"limit_window_minutes":300}}`, false},
		{"failed_initial", `{"type":"response.failed","response":{"id":"quota-response","error":{"type":"usage_limit_reached","message":"quota exhausted","resets_in_seconds":600,"limit_window_minutes":300}}}`, false},
		{"error_started", `{"type":"error","status":429,"error":{"type":"usage_limit_reached","message":"quota exhausted","resets_in_seconds":600,"limit_window_minutes":300}}`, true},
		{"failed_started", `{"type":"response.failed","response":{"id":"quota-response","error":{"type":"usage_limit_reached","message":"quota exhausted","resets_in_seconds":600,"limit_window_minutes":300}}}`, true},
	} {
		for _, buffering := range []bool{false, true} {
			for _, closeImmediately := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/buffering=%t/close=%t", tc.name, buffering, closeImmediately), func(t *testing.T) {
					var calls atomic.Int32
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
						if errUpgrade != nil {
							t.Error(errUpgrade)
							return
						}
						defer func() { _ = conn.Close() }()
						if _, _, errRead := conn.ReadMessage(); errRead != nil {
							t.Error(errRead)
							return
						}
						calls.Add(1)
						if tc.started {
							if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"quota-response","output":[]}}`)); errWrite != nil {
								t.Error(errWrite)
								return
							}
						}
						if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(tc.failure)); errWrite != nil {
							t.Error(errWrite)
							return
						}
						if !closeImmediately {
							_, _, _ = conn.ReadMessage()
						}
					}))
					defer upstream.Close()

					cfg := &config.Config{}
					cfg.Codex.StreamBootstrapBuffering = buffering
					manager := cliproxyauth.NewManager(nil, nil, nil)
					manager.SetConfig(cfg)
					manager.SetRetryConfig(0, 0, 0)
					executor := runtimeexecutor.NewCodexWebsocketsExecutor(cfg)
					manager.RegisterExecutor(executor)
					model := fmt.Sprintf("quota-cooldown-%s-%t-%t", tc.name, buffering, closeImmediately)
					authID := model + "-auth"
					registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
					if _, errRegister := manager.Register(context.Background(), &cliproxyauth.Auth{
						ID: authID, Provider: "codex", Status: cliproxyauth.StatusActive,
						Attributes: map[string]string{"api_key": "synthetic", "base_url": upstream.URL, "websockets": "true"},
					}); errRegister != nil {
						t.Fatal(errRegister)
					}
					gin.SetMode(gin.TestMode)
					router := gin.New()
					handler := openaihandlers.NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
					router.GET("/v1/responses", handler.ResponsesWebsocket)
					proxy := httptest.NewServer(router)
					defer proxy.Close()

					before := time.Now()
					for attempt := 0; attempt < 3; attempt++ {
						conn, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(proxy.URL, "http")+"/v1/responses", nil)
						if errDial != nil {
							t.Fatal(errDial)
						}
						if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"type":"response.create","model":%q,"input":[],"store":false}`, model))); errWrite != nil {
							_ = conn.Close()
							t.Fatal(errWrite)
						}
						// This deadline only bounds the test client, not proxy/upstream behavior.
						_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
						_, payload, errRead := conn.ReadMessage()
						for errRead == nil && gjson.GetBytes(payload, "type").String() == "response.created" {
							_, payload, errRead = conn.ReadMessage()
						}
						_ = conn.Close()
						// Credential errors may close the socket without exposing their body.
						var closeErr *websocket.CloseError
						if errRead != nil && !errors.As(errRead, &closeErr) {
							t.Fatalf("attempt %d: websocket did not terminate: %v", attempt, errRead)
						}
						if got := gjson.GetBytes(payload, "status").Int(); errRead == nil && got != http.StatusTooManyRequests {
							t.Fatalf("attempt %d: status=%d, payload=%s", attempt, got, payload)
						}
						current, _ := manager.GetByID(authID)
						if !current.Unavailable || current.Quota.NextRecoverAt.Before(before.Add(600*time.Second)) {
							t.Fatalf("attempt %d: quota cooldown missing before downstream close: %+v", attempt, current.Quota)
						}
						if got := calls.Load(); got != 1 {
							t.Fatalf("attempt %d: exhausted credential called %d times, want 1", attempt, got)
						}
					}
				})
			}
		}
	}
}
