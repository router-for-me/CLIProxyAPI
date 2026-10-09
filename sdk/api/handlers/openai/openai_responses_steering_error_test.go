package openai

import (
	"bytes"
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

// Exercise both WebSocket endpoints with the real handler, manager and executor.
// Only a later payload error on an opted-in duplex route is nonterminal.
func TestResponsesSteeringErrorRecoveryIntegration(t *testing.T) {
	for _, tc := range []struct {
		name                            string
		enabled, initial, upstreamClose bool
		staleOAuthScope                 bool
	}{
		{"later_error_corrected_create", true, false, false, false},
		{"initial_error_remains_terminal", true, true, false, false},
		{"disabled_error_remains_terminal", false, false, false, false},
		{"later_error_then_upstream_close", true, false, true, false},
		{"stale_oauth_scope_does_not_disable_shared_api_key_steering", true, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var connections, frames atomic.Int32
			done := make(chan struct{})
			rejection := []byte(`{"type":"error","status":400,"event_id":"rejected-create","error":{"type":"invalid_request_error","message":"Correct the request"}}`)
			effectiveSteering := tc.enabled
			recoverable := effectiveSteering && !tc.initial && !tc.upstreamClose
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
			if tc.staleOAuthScope {
				cfg.OAuthOnlyFields = map[string]bool{"codex.response-steering": true}
			}
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
					if effectiveSteering && !tc.initial && !bytes.Equal(p, rejection) {
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

func TestResponsesWebsocketClosesOnIdleCodexDisconnect(t *testing.T) {
	for _, tc := range []struct {
		name, yaml string
		oauth      bool
	}{
		{"legacy_disabled_api_key", "codex: {response-steering: false}\n", false},
		{"legacy_enabled_api_key", "codex: {response-steering: true}\n", false},
		{"v8_enabled_api_key", "oauth: {providers: {codex: {response-steering: true}}}\n", false},
		{"v8_enabled_oauth", "oauth: {providers: {codex: {response-steering: true}}}\n", true},
		{"upstream_enabled_api_key", "upstream: {codex: {response-steering: true}}\n", false},
		{"upstream_enabled_oauth", "upstream: {codex: {response-steering: true}}\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			closeUpstream := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer func() {
					if errClose := conn.Close(); errClose != nil {
						t.Errorf("close upstream: %v", errClose)
					}
				}()
				if _, _, errRead := conn.ReadMessage(); errRead != nil {
					t.Errorf("read initial request: %v", errRead)
					return
				}
				for _, payload := range []string{
					`{"type":"response.created","response":{"id":"first","output":[]}}`,
					`{"type":"response.completed","response":{"id":"first","output":[]}}`,
				} {
					if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(payload)); errWrite != nil {
						t.Errorf("write response: %v", errWrite)
						return
					}
				}
				// Close only after the client receives the completed response.
				select {
				case <-closeUpstream:
				case <-t.Context().Done():
					return
				}
				if errWrite := conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseGoingAway, "idle upstream disconnect")); errWrite != nil {
					t.Errorf("write upstream close: %v", errWrite)
				}
			}))
			t.Cleanup(upstream.Close)
			cfg, err := config.ParseConfigBytes([]byte(tc.yaml))
			if err != nil {
				t.Fatal(err)
			}
			// Match the SDK flag populated by the server's effectiveSDKConfig.
			cfg.CodexResponseSteering = cfg.Codex.ResponseSteering
			manager := coreauth.NewManager(nil, nil, nil)
			manager.SetConfig(cfg)
			manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))
			authID := "idle-disconnect-" + tc.name
			model := "idle-disconnect-model"
			credential := &coreauth.Auth{
				ID: authID, Provider: "codex", Status: coreauth.StatusActive,
				Attributes: map[string]string{"base_url": upstream.URL, "websockets": "true"},
			}
			if tc.oauth {
				credential.Metadata = map[string]any{"access_token": "test-token"}
			} else {
				credential.Attributes["api_key"] = "test-key"
			}
			if _, err = manager.Register(context.Background(), credential); err != nil {
				t.Fatal(err)
			}
			registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
			h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
			router := gin.New()
			router.GET("/v1/responses", h.ResponsesWebsocket)
			downstream := httptest.NewServer(router)
			t.Cleanup(downstream.Close)
			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(downstream.URL, "http")+"/v1/responses", nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if errClose := conn.Close(); errClose != nil {
					t.Errorf("close downstream: %v", errClose)
				}
			})
			if err = conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			request := fmt.Sprintf(`{"type":"response.create","model":%q,"input":[]}`, model)
			if err = conn.WriteMessage(websocket.TextMessage, []byte(request)); err != nil {
				t.Fatal(err)
			}
			for {
				_, payload, errRead := conn.ReadMessage()
				if errRead != nil {
					t.Fatalf("read response before upstream close: %v", errRead)
				}
				if gjson.GetBytes(payload, "type").String() == "response.completed" {
					break
				}
			}
			close(closeUpstream)
			_, _, err = conn.ReadMessage()
			var closeErr *websocket.CloseError
			if !errors.As(err, &closeErr) {
				t.Fatalf("expected downstream close after idle upstream disconnect, got %v", err)
			}
		})
	}
}
