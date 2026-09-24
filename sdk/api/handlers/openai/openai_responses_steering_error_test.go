package openai

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

// Exercise both WebSocket endpoints with the real handler, manager and executor.
// Only a later payload error on an opted-in duplex route is nonterminal.
func TestResponsesSteeringErrorRecoveryIntegration(t *testing.T) {
	for _, tc := range []struct {
		name                            string
		enabled, initial, upstreamClose bool
	}{
		{"later_error_corrected_create", true, false, false},
		{"initial_error_remains_terminal", true, true, false},
		{"disabled_error_remains_terminal", false, false, false},
		{"later_error_then_upstream_close", true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var connections, frames atomic.Int32
			done := make(chan struct{})
			rejection := []byte(`{"type":"error","status":400,"event_id":"rejected-create","error":{"type":"invalid_request_error","message":"Correct the request"}}`)
			recoverable := tc.enabled && !tc.initial && !tc.upstreamClose
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(done)
				connections.Add(1)
				c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer func() { _ = c.Close() }()
				_ = c.SetReadDeadline(time.Now().Add(8 * time.Second))
				read := func() []byte {
					_, p, e := c.ReadMessage()
					if e != nil {
						t.Errorf("upstream read: %v", e)
						return nil
					}
					frames.Add(1)
					return p
				}
				write := func(p []byte) {
					if e := c.WriteMessage(websocket.TextMessage, p); e != nil {
						t.Error(e)
					}
				}
				read()
				if !tc.initial {
					write([]byte(`{"type":"response.created","response":{"id":"first","output":[]}}`))
					write([]byte(`{"type":"response.completed","response":{"id":"first","output":[]}}`))
					read()
				}
				write(rejection)
				if tc.upstreamClose {
					return
				}
				if recoverable {
					corrected := read()
					if gjson.GetBytes(corrected, "instructions").String() != "CORRECTED" {
						t.Errorf("corrected create missing: %s", corrected)
					}
					write([]byte(`{"type":"response.created","response":{"id":"corrected","output":[]}}`))
					write([]byte(`{"type":"response.completed","response":{"id":"corrected","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"RECOVERED"}]}]}}`))
				}
				_, _, _ = c.ReadMessage()
			}))
			defer upstream.Close()
			cfg := &config.Config{}
			cfg.Codex.ResponseSteering = tc.enabled
			cfg.CodexResponseSteering = tc.enabled
			manager := coreauth.NewManager(nil, nil, nil)
			manager.SetConfig(cfg)
			manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))
			authID := "steering-error-" + tc.name
			model := "steering-error-model"
			if _, err := manager.Register(context.Background(), &coreauth.Auth{ID: authID, Provider: "codex", Status: coreauth.StatusActive, Attributes: map[string]string{"api_key": "test-key", "base_url": upstream.URL, "websockets": "true"}}); err != nil {
				t.Fatal(err)
			}
			registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
			defer registry.GetGlobalRegistry().UnregisterClient(authID)
			h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
			router := gin.New()
			router.GET("/v1/responses", h.ResponsesWebsocket)
			downstream := httptest.NewServer(router)
			defer downstream.Close()
			c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(downstream.URL, "http")+"/v1/responses", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = c.Close() }()
			_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
			send := func(instructions string) {
				p := []byte(fmt.Sprintf(`{"type":"response.create","model":%q,"instructions":%q,"input":[]}`, model, instructions))
				if e := c.WriteMessage(websocket.TextMessage, p); e != nil {
					t.Fatal(e)
				}
			}
			send("INITIAL")
			errorsSeen, completed := 0, false
			for {
				_, p, e := c.ReadMessage()
				if e != nil {
					if recoverable {
						t.Fatalf("socket closed before corrected create completed: %v", e)
					}
					if netErr, ok := e.(interface{ Timeout() bool }); ok && netErr.Timeout() {
						t.Fatal("terminal failure did not close the socket")
					}
					break
				}
				switch gjson.GetBytes(p, "type").String() {
				case "response.completed":
					if gjson.GetBytes(p, "response.id").String() == "first" {
						send("REJECTED")
					} else {
						completed = gjson.GetBytes(p, "response.output.0.content.0.text").String() == "RECOVERED"
						_ = c.Close()
					}
				case "error":
					errorsSeen++
					if tc.enabled && !tc.initial && !bytes.Equal(p, rejection) {
						t.Errorf("recoverable error payload changed: %s", p)
					}
					if recoverable {
						send("CORRECTED")
					}
				}
				if completed {
					break
				}
			}
			if errorsSeen != 1 || completed != recoverable {
				t.Fatalf("errors=%d completed=%t want completed=%t", errorsSeen, completed, recoverable)
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("upstream cleanup stalled")
			}
			wantFrames := int32(2)
			if tc.initial {
				wantFrames = 1
			} else if recoverable {
				wantFrames = 3
			}
			if connections.Load() != 1 || frames.Load() != wantFrames {
				t.Fatalf("connections=%d frames=%d want frames=%d", connections.Load(), frames.Load(), wantFrames)
			}
		})
	}
}
