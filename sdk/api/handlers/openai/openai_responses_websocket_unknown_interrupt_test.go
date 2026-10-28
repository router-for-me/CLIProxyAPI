package openai

import (
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

// An unknown interrupt while a websocket response is running belongs to the
// live upstream socket: it must be rejected outright, never fall back to a
// local cancellation that fabricates an incomplete event and kills the
// running turn. Only a turn with no active websocket (HTTP fallback) may be
// interrupted locally.
func TestResponsesUnknownInterruptDoesNotCancelWebsocketTurn(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var connections atomic.Int32
	release := make(chan struct{})
	var releaseOnce atomic.Bool
	// Always unblock the upstream goroutine, even on assertion failure.
	defer func() {
		if releaseOnce.CompareAndSwap(false, true) {
			close(release)
		}
	}()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connections.Add(1)
		conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Errorf("upgrade: %v", errUpgrade)
			return
		}
		defer func() {
			if errClose := conn.Close(); errClose != nil {
				t.Errorf("close upstream: %v", errClose)
			}
		}()
		_, create, errRead := conn.ReadMessage()
		if errRead != nil {
			return
		}
		if gjson.GetBytes(create, "type").String() != "response.create" {
			t.Errorf("expected response.create, got %s", create)
			return
		}
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"r1"}}`)); errWrite != nil {
			t.Errorf("created: %v", errWrite)
			return
		}
		// Keep r1 running until the test checked the unknown interrupt.
		<-release
		// The only frame allowed next is the valid r1 interrupt.
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, interrupt, errInterrupt := conn.ReadMessage()
		if errInterrupt != nil {
			return
		}
		if gjson.GetBytes(interrupt, "response_id").String() != "r1" {
			t.Errorf("upstream received unexpected frame: %s", interrupt)
			return
		}
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.incomplete","response":{"id":"r1","status":"incomplete","incomplete_details":{"reason":"interrupted"},"output":[]}}`)); errWrite != nil {
			t.Errorf("incomplete: %v", errWrite)
			return
		}
		_, _, _ = conn.ReadMessage()
	}))
	defer upstream.Close()

	cfg := &config.Config{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))
	const authID = "unknown-interrupt-ws-turn"
	const model = "unknown-interrupt-model"
	_, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID: authID, Provider: "codex", Status: coreauth.StatusActive,
		Attributes: map[string]string{"api_key": "synthetic-key", "base_url": upstream.URL, "websockets": "true"},
	})
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
	defer registry.GetGlobalRegistry().UnregisterClient(authID)
	handler := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
	router := gin.New()
	router.GET("/v1/responses", handler.ResponsesWebsocket)
	downstream := httptest.NewServer(router)
	defer downstream.Close()

	client, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(downstream.URL, "http")+"/v1/responses", nil)
	if errDial != nil {
		t.Fatal(errDial)
	}
	clientClosed := false
	defer func() {
		if !clientClosed {
			if errClose := client.Close(); errClose != nil {
				t.Errorf("close client: %v", errClose)
			}
		}
	}()
	if errDeadline := client.SetReadDeadline(time.Now().Add(8 * time.Second)); errDeadline != nil {
		t.Fatal(errDeadline)
	}
	send := func(payload string) {
		t.Helper()
		if errWrite := client.WriteMessage(websocket.TextMessage, []byte(payload)); errWrite != nil {
			t.Fatal(errWrite)
		}
	}
	expect := func(event, id string) {
		t.Helper()
		_, payload, errRead := client.ReadMessage()
		if errRead != nil {
			t.Fatal(errRead)
		}
		if gjson.GetBytes(payload, "type").String() != event || gjson.GetBytes(payload, "response.id").String() != id {
			t.Fatalf("expected %s for %s, got %s", event, id, payload)
		}
	}

	send(fmt.Sprintf(`{"type":"response.create","model":%q,"input":[]}`, model))
	expect("response.created", "r1")

	// Unknown target on a live websocket turn: reject, do not cancel r1.
	send(`{"type":"response.interrupt","response_id":"unknown"}`)
	expect("error", "")

	// r1 must still be alive: a valid interrupt now forwards upstream and
	// the upstream terminal event comes back on the same socket.
	if releaseOnce.CompareAndSwap(false, true) {
		close(release)
	}
	send(`{"type":"response.interrupt","response_id":"r1"}`)
	expect("response.incomplete", "r1")
	if got := connections.Load(); got != 1 {
		t.Fatalf("connections = %d, want 1", got)
	}
	if errClose := client.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	clientClosed = true
}
