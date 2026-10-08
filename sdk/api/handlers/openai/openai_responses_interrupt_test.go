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
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func TestResponsesInterruptInFlight(t *testing.T) {
	for _, steering := range []bool{false, true} {
		t.Run(fmt.Sprintf("steering_%t", steering), func(t *testing.T) {
			interrupt := []byte(`{"type":"response.interrupt","response_id":"r1","mode":"discard_partial_items","extension":"keep"}`)
			var connections atomic.Int32
			upstreamDone := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				connections.Add(1)
				defer close(upstreamDone)
				c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer c.Close()
				c.SetReadDeadline(time.Now().Add(5 * time.Second))
				read := func() []byte {
					_, p, err := c.ReadMessage()
					if err != nil {
						t.Error(err)
					}
					return p
				}
				write := func(p string) {
					if err := c.WriteMessage(websocket.TextMessage, []byte(p)); err != nil {
						t.Error(err)
					}
				}
				if p := read(); gjson.GetBytes(p, "type").String() != "response.create" {
					t.Errorf("expected create, got %s", p)
					return
				}
				write(`{"type":"response.created","response":{"id":"r1"}}`)
				// Completion waits for the interrupt, proving it bypasses the active response.
				if p := read(); !bytes.Equal(p, interrupt) {
					t.Errorf("interrupt changed: %s", p)
					return
				}
				write(`{"type":"response.completed","response":{"id":"r1","status":"completed","output":[]}}`)
				if p := read(); gjson.GetBytes(p, "type").String() != "response.create" {
					t.Errorf("expected follow-up create, got %s", p)
					return
				}
				write(`{"type":"response.created","response":{"id":"r2"}}`)
				write(`{"type":"response.completed","response":{"id":"r2","status":"completed","output":[]}}`)
				_, _, _ = c.ReadMessage()
			}))
			defer upstream.Close()
			cfg := &config.Config{}
			cfg.Codex.ResponseSteering = steering
			cfg.CodexResponseSteering = steering
			manager := coreauth.NewManager(nil, nil, nil)
			manager.SetConfig(cfg)
			manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))
			authID := fmt.Sprintf("interrupt-%t", steering)
			model := "interrupt-model"
			_, err := manager.Register(context.Background(), &coreauth.Auth{ID: authID, Provider: "codex", Status: coreauth.StatusActive, Attributes: map[string]string{"api_key": "test-key", "base_url": upstream.URL, "websockets": "true"}})
			if err != nil {
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
			defer c.Close()
			c.SetReadDeadline(time.Now().Add(8 * time.Second))
			send := func(p []byte) {
				if err := c.WriteMessage(websocket.TextMessage, p); err != nil {
					t.Fatal(err)
				}
			}
			expect := func(event string) {
				_, p, err := c.ReadMessage()
				if err != nil {
					t.Fatal(err)
				}
				if gjson.GetBytes(p, "type").String() != event {
					t.Fatalf("expected %s, got %s", event, p)
				}
			}
			send([]byte(`{"type":"response.interrupt"}`))
			expect("error")
			send(interrupt)
			expect("error")
			if connections.Load() != 0 {
				t.Fatal("idle interrupt opened an upstream connection")
			}
			send([]byte(fmt.Sprintf(`{"type":"response.create","model":%q,"input":[]}`, model)))
			expect("response.created")
			send([]byte(`{"type":"response.interrupt","response_id":42}`))
			expect("error")
			auth, _ := manager.GetByID(authID)
			auth.Disabled = true
			if _, err := manager.Update(context.Background(), auth); err != nil {
				t.Fatal(err)
			}
			send(interrupt)
			expect("error")
			auth.Disabled = false
			if _, err := manager.Update(context.Background(), auth); err != nil {
				t.Fatal(err)
			}
			send(interrupt)
			expect("response.completed")
			send([]byte(fmt.Sprintf(`{"type":"response.create","model":%q,"previous_response_id":"r1","input":[]}`, model)))
			expect("response.created")
			expect("response.completed")
			c.Close()
			select {
			case <-upstreamDone:
			case <-time.After(5 * time.Second):
				t.Fatal("upstream did not close")
			}
			if connections.Load() != 1 {
				t.Fatalf("upstream connections=%d, want 1", connections.Load())
			}
		})
	}
}
