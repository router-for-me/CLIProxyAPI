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
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func TestResponsesSteeringLocalValidationRecovery(t *testing.T) {
	var connections, frames atomic.Int32
	done := make(chan struct{})
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
		write := func(p string) {
			if e := c.WriteMessage(websocket.TextMessage, []byte(p)); e != nil {
				t.Error(e)
			}
		}
		read()
		write(`{"type":"response.created","response":{"id":"first","output":[]}}`)
		if p := read(); gjson.GetBytes(p, "type").String() != "response.steer" {
			t.Errorf("expected corrected steering, got %s", p)
			return
		}
		write(`{"type":"response.steer.accepted","steer":{"id":"corrected","previous_response_id":"first"}}`)
		write(`{"type":"response.completed","response":{"id":"first","output":[]}}`)
		write(`{"type":"response.created","response":{"id":"steered","previous_response_id":"first","output":[]}}`)
		write(`{"type":"response.completed","response":{"id":"steered","output":[]}}`)
		if p := read(); gjson.GetBytes(p, "type").String() != "response.create" {
			t.Errorf("expected corrected create, got %s", p)
			return
		}
		write(`{"type":"response.created","response":{"id":"second","output":[]}}`)
		write(`{"type":"response.completed","response":{"id":"second","output":[]}}`)
		_, _, _ = c.ReadMessage()
	}))
	defer upstream.Close()
	cfg := &config.Config{}
	cfg.Codex.ResponseSteering = true
	cfg.CodexResponseSteering = true
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))
	authID, model := "steering-local-validation", "steering-local-validation-model"
	if _, err := manager.Register(context.Background(), &coreauth.Auth{ID: authID, Provider: "codex", Status: coreauth.StatusActive, Attributes: map[string]string{"api_key": "test-key", "base_url": upstream.URL, "websockets": "true"}}); err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
	defer registry.GetGlobalRegistry().UnregisterClient(authID)
	handler := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
	router := gin.New()
	router.GET("/v1/responses", handler.ResponsesWebsocket)
	downstream := httptest.NewServer(router)
	defer downstream.Close()
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(downstream.URL, "http")+"/v1/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	send := func(p string) {
		if e := c.WriteMessage(websocket.TextMessage, []byte(p)); e != nil {
			t.Fatal(e)
		}
	}
	create := fmt.Sprintf(`{"type":"response.create","model":%q,"input":[]}`, model)
	send(create)
	// Queue an invalid frame even before bootstrap finishes. response.created must
	// still reach downstream before this recoverable local error.
	send("{")
	errorsSeen, accepted, completed := 0, false, false
	for !completed {
		_, p, e := c.ReadMessage()
		if e != nil {
			t.Fatalf("local validation closed the established socket: %v", e)
		}
		switch gjson.GetBytes(p, "type").String() {
		case "response.created":
			if gjson.GetBytes(p, "response.id").String() == "first" && errorsSeen != 0 {
				t.Fatal("local error preceded bootstrap")
			}
		case "error":
			errorsSeen++
			if gjson.GetBytes(p, "status").Int() != 400 || gjson.GetBytes(p, "error.type").String() != "invalid_request_error" {
				t.Fatalf("invalid error envelope: %s", p)
			}
			switch errorsSeen {
			case 1:
				if !strings.Contains(gjson.GetBytes(p, "error.message").String(), "JSON") {
					t.Errorf("missing JSON validation message: %s", p)
				}
				send(`{"type":"unsupported.request"}`)
			case 2:
				if !strings.Contains(gjson.GetBytes(p, "error.message").String(), "unsupported") {
					t.Errorf("missing type validation message: %s", p)
				}
				send(`{"type":"response.steer","steering_id":"corrected","input":[{"role":"user","content":[{"type":"input_text","text":"continue"}]}]}`)
			default:
				t.Fatalf("unexpected error: %s", p)
			}
		case "response.steer.accepted":
			accepted = true
		case "response.completed":
			if gjson.GetBytes(p, "response.id").String() == "first" {
				send(create)
			} else if gjson.GetBytes(p, "response.id").String() == "second" {
				completed = true
			}
		}
	}
	if errorsSeen != 2 || !accepted {
		t.Fatalf("errors=%d accepted=%t", errorsSeen, accepted)
	}
	_ = c.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream cleanup stalled")
	}
	if connections.Load() != 1 || frames.Load() != 3 {
		t.Fatalf("connections=%d upstream frames=%d", connections.Load(), frames.Load())
	}
	current, _ := manager.GetByID(authID)
	if current.Unavailable || current.LastError != nil {
		t.Fatalf("local validation cooled healthy credential: %+v", current.LastError)
	}
}
