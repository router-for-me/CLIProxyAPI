package openai

import (
	"bytes"
	"context"
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

// Exercise the actual downstream handler, auth manager and Codex executor.
// All coordination uses protocol events, never sleep-based timing guesses.
func TestResponsesSteeringFullDuplexIntegration(t *testing.T) {
	for _, scenario := range []string{"successor", "tool_pending", "disconnect_accepted", "disconnect_pending"} {
		t.Run(scenario, func(t *testing.T) {
			var connections atomic.Int32
			done := make(chan struct{})
			control1 := []byte(`{"type":"response.steer","previous_response_id":"r1","input":"one"}`)
			control2 := []byte(`{"type":"response.steer","previous_response_id":"r1","input":"two"}`)
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
					_, b, err := c.ReadMessage()
					if err != nil {
						t.Errorf("upstream read: %v", err)
					}
					return b
				}
				write := func(s string) {
					if err := c.WriteMessage(websocket.TextMessage, []byte(s)); err != nil {
						t.Errorf("upstream write: %v", err)
					}
				}
				if gjson.GetBytes(read(), "type").String() != "response.create" {
					t.Error("missing initial create")
				}
				write(`{"type":"response.created","response":{"id":"r1"}}`)
				if b := read(); !bytes.Equal(b, control1) {
					t.Errorf("first steer altered: %s", b)
				}
				if b := read(); !bytes.Equal(b, control2) {
					t.Errorf("second steer altered: %s", b)
				}
				write(`{"type":"response.steer.accepted","steer":{"id":"s1","previous_response_id":"r1"}}`)
				write(`{"type":"response.steer.accepted","steer":{"id":"s2","previous_response_id":"r1"}}`)
				if scenario == "disconnect_accepted" {
					return
				}
				if scenario == "tool_pending" || scenario == "disconnect_pending" {
					write(`{"type":"response.completed","response":{"id":"r1","output":[{"type":"function_call","call_id":"call1","name":"lookup","arguments":"{}"}]}}`)
					write(`{"type":"response.steer.pending","steer":{"id":"s1","previous_response_id":"r1"},"reason":"waiting_for_required_input","required_input":[{"type":"function_call_output","call_id":"call1","name":"lookup"}]}`)
					if scenario == "disconnect_pending" {
						return
					}
					b := read()
					if gjson.GetBytes(b, "type").String() != "response.create" || gjson.GetBytes(b, "input.0.call_id").String() != "call1" {
						t.Errorf("required input lost: %s", b)
					}
				} else {
					write(`{"type":"response.incomplete","response":{"id":"r1","incomplete_details":{"reason":"steered"},"output":[]}}`)
				}
				write(`{"type":"response.created","response":{"id":"r2"}}`)
				write(`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"STEER_OK"}]}}`)
				write(`{"type":"response.completed","response":{"id":"r2","output":[]}}`)
				_, _, _ = c.ReadMessage()
			}))
			defer upstream.Close()
			cfg := &config.Config{}
			cfg.Codex.ResponseSteering = true
			cfg.CodexResponseSteering = true
			manager := coreauth.NewManager(nil, nil, nil)
			manager.SetConfig(cfg)
			manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))
			authID := "steering-integration-" + scenario
			_, err := manager.Register(context.Background(), &coreauth.Auth{ID: authID, Provider: "codex", Status: coreauth.StatusActive, Attributes: map[string]string{"api_key": "test-key", "base_url": upstream.URL, "websockets": "true"}})
			if err != nil {
				t.Fatal(err)
			}
			registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: "steering-test-model"}})
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
			send := func(b []byte) {
				if err := c.WriteMessage(websocket.TextMessage, b); err != nil {
					t.Fatal(err)
				}
			}
			send([]byte(`{"type":"response.create","model":"steering-test-model","input":[]}`))
			completed := false
			accepted := 0
			pendingSeen := false
			for {
				_, b, err := c.ReadMessage()
				if err != nil {
					if !strings.HasPrefix(scenario, "disconnect_") {
						t.Fatalf("early close: %v", err)
					}
					break
				}
				kind := gjson.GetBytes(b, "type").String()
				if kind == "response.created" && gjson.GetBytes(b, "response.id").String() == "r1" {
					send(control1)
					send(control2)
				}
				if kind == "response.steer.accepted" {
					accepted++
				}
				if kind == "response.steer.pending" {
					pendingSeen = true
				}
				if kind == "response.steer.pending" && scenario == "tool_pending" {
					send([]byte(`{"type":"response.create","previous_response_id":"r1","input":[{"type":"function_call_output","call_id":"call1","output":"found"}]}`))
				}
				if kind == "response.completed" && gjson.GetBytes(b, "response.id").String() == "r2" {
					completed = gjson.GetBytes(b, "response.output.0.content.0.text").String() == "STEER_OK"
					_ = c.Close()
					break
				}
			}
			if accepted != 2 {
				t.Fatalf("expected both acknowledgements before termination; got %d", accepted)
			}
			if (scenario == "tool_pending" || scenario == "disconnect_pending") && !pendingSeen {
				t.Fatal("connection ended before the pending event was forwarded")
			}
			if !strings.HasPrefix(scenario, "disconnect_") && !completed {
				t.Fatal("successor did not complete")
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("connection cleanup stalled")
			}
			if connections.Load() != 1 {
				t.Fatalf("unexpected reconnect/replay: %d", connections.Load())
			}
		})
	}
}
