package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/tidwall/gjson"
)

func TestResponsesErrorHeadersRespectPassthroughAndFiltering(t *testing.T) {
	msg := &interfaces.ErrorMessage{StatusCode: 429, Error: errors.New("rate limited"), Addon: http.Header{
		"Retry-After": {"38"}, "X-Request-Id": {"test"},
		"Content-Length": {"999"}, "Set-Cookie": {"synthetic=secret"},
		"Connection": {"X-Private"}, "X-Private": {"blocked"},
		"X-Cpa-Trace-Id": {"blocked"}, "X-Litellm-Trace": {"blocked"},
	}}
	for _, enabled := range []bool{false, true} {
		payload, err := buildResponsesWebsocketErrorPayload(msg, enabled)
		if err != nil {
			t.Fatal(err)
		}
		headers := gjson.GetBytes(payload, "headers")
		if headers.Exists() != enabled {
			t.Fatalf("enabled=%v headers=%s", enabled, headers.Raw)
		}
		if enabled {
			if headers.Get("Retry-After").String() != "38" || headers.Get("X-Request-Id").String() != "test" || len(headers.Map()) != 2 {
				t.Fatalf("unsafe or missing headers: %s", payload)
			}
		}
	}
}

func TestResponsesErrorHeadersRealWebsocketRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("enabled=%v", enabled), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "38")
				w.Header().Set("X-Request-Id", "route-test")
				w.Header().Set("Set-Cookie", "synthetic=secret")
				// Current upstream exposes request faults but deliberately keeps
				// quota/transport failures silent on this route. Preserve that policy.
				w.WriteHeader(400)
				_, _ = fmt.Fprint(w, `{"error":{"type":"invalid_request_error","message":"invalid request"}}`)
			}))
			t.Cleanup(upstream.Close)
			manager := coreauth.NewManager(nil, nil, nil)
			manager.RegisterExecutor(runtimeexecutor.NewCodexExecutor(&config.Config{}))
			id := "error-header-route-" + uuid.NewString()
			registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: "gpt-5.4"}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
			if _, err := manager.Register(context.Background(), &coreauth.Auth{ID: id, Provider: "codex", Attributes: map[string]string{"api_key": "synthetic", "base_url": upstream.URL}}); err != nil {
				t.Fatal(err)
			}
			handler := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{PassthroughHeaders: enabled}, manager))
			router := gin.New()
			router.GET("/v1/responses/ws", handler.ResponsesWebsocket)
			server := httptest.NewServer(router)
			t.Cleanup(server.Close)
			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses/ws", nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = conn.Close() })
			// Bound a broken test client, not production upstream execution.
			if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
				t.Fatal(err)
			}
			if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"gpt-5.4","input":[]}`)); err != nil {
				t.Fatal(err)
			}
			_, payload, err := conn.ReadMessage()
			if err != nil {
				t.Fatal(err)
			}
			headers := gjson.GetBytes(payload, "headers")
			if headers.Exists() != enabled {
				t.Fatalf("enabled=%v payload=%s", enabled, payload)
			}
			if enabled && (headers.Get("Retry-After").String() != "38" || headers.Get("X-Request-Id").String() != "route-test" || headers.Get("Set-Cookie").Exists()) {
				t.Fatalf("unsafe or missing route headers: %s", payload)
			}
		})
	}
}
