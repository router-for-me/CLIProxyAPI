package openai

import (
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
)

func TestResponsesSteeringDisabledAccountCannotSendAnotherFrame(t *testing.T) {
	var frames atomic.Int32
	done := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = c.Close() }()
		_ = c.SetReadDeadline(time.Now().Add(8 * time.Second))
		if _, _, err := c.ReadMessage(); err != nil {
			t.Error(err)
			return
		}
		frames.Add(1)
		_ = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"r1"}}`))
		_ = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"r1","output":[]}}`))
		if _, _, err := c.ReadMessage(); err == nil {
			frames.Add(1)
		}
	}))
	defer upstream.Close()
	cfg := &config.Config{}
	cfg.Codex.ResponseSteering = true
	cfg.CodexResponseSteering = true
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))
	id := "steering-disable-test"
	auth, err := manager.Register(context.Background(), &coreauth.Auth{ID: id, Provider: "codex", Status: coreauth.StatusActive, Attributes: map[string]string{"api_key": "test-key", "base_url": upstream.URL, "websockets": "true"}})
	if err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: "steering-disable-model"}})
	defer registry.GetGlobalRegistry().UnregisterClient(id)
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
	_ = c.SetReadDeadline(time.Now().Add(8 * time.Second))
	if err := c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"steering-disable-model","input":[]}`)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, _, err := c.ReadMessage(); err != nil {
			t.Fatal(err)
		}
	}
	disabled := auth.Clone()
	disabled.Disabled = true
	disabled.Status = coreauth.StatusDisabled
	if _, err := manager.Update(context.Background(), disabled); err != nil {
		t.Fatal(err)
	}
	if err := c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.steer","previous_response_id":"r1","input":"must not be sent"}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.ReadMessage(); err == nil {
		t.Fatal("disabled account connection remained usable")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream close stalled")
	}
	if frames.Load() != 1 {
		t.Fatalf("disabled credential sent %d frames", frames.Load())
	}
}
