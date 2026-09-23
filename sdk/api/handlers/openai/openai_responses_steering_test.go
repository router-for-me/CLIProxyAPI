package openai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

// TestResponsesSteerRejectedWhenDisabled verifies that when codex.response-steering is
// false (default), response.steer is rejected as an unsupported request type and is
// never rewritten or converted into response.create.
func TestResponsesSteerRejectedWhenDisabled(t *testing.T) {
	raw := []byte(`{"type":"response.steer","previous_response_id":"resp-1","input":"Keep the scope small."}`)
	lastRequest := []byte(`{"model":"gpt-6-astra","stream":true,"input":[]}`)

	_, _, errMsg := normalizeResponsesWebsocketRequest(raw, lastRequest, []byte("[]"))
	if errMsg == nil {
		t.Fatal("expected response.steer to be rejected when steering is disabled")
	}
	if !strings.Contains(errMsg.Error.Error(), "unsupported websocket request type: response.steer") {
		t.Fatalf("unexpected error message: %v", errMsg.Error)
	}
}

// TestResponsesSteerInFlightWebSocket verifies that when codex.response-steering is true,
// an in-flight response.steer frame sent while generation is running is forwarded directly
// to the same upstream connection without waiting or converting to response.create.
func TestResponsesSteerInFlightWebSocket(t *testing.T) {
	upstreamReceivedSteer := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = c.Close() }()
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))

		// Read create
		_, p, err := c.ReadMessage()
		if err != nil {
			t.Errorf("read create: %v", err)
			return
		}
		if gjson.GetBytes(p, "type").String() != "response.create" {
			t.Errorf("expected response.create, got %s", p)
			return
		}

		// Emit response.created
		_ = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"r1"}}`))

		// Read next frame: in-flight response.steer
		_, steerPayload, err := c.ReadMessage()
		if err != nil {
			t.Errorf("read steer: %v", err)
			return
		}
		if gjson.GetBytes(steerPayload, "type").String() == "response.steer" {
			close(upstreamReceivedSteer)
			_ = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.steer.accepted","steer":{"id":"s1","previous_response_id":"r1"}}`))
		} else {
			t.Errorf("expected response.steer, got %s", steerPayload)
		}

		_ = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"r1","output":[]}}`))
	}))
	defer upstream.Close()

	cfg := &config.Config{}
	cfg.Codex.ResponseSteering = true
	cfg.CodexResponseSteering = true
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))
	authID := "steering-red-test"
	model := "steering-red-model"
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:         authID,
		Provider:   "codex",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"api_key": "test-key", "base_url": upstream.URL, "websockets": "true"},
	}); err != nil {
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
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))

	create := fmt.Sprintf(`{"type":"response.create","model":%q,"input":[]}`, model)
	if err := c.WriteMessage(websocket.TextMessage, []byte(create)); err != nil {
		t.Fatal(err)
	}

	// Read response.created
	_, p, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("read response.created: %v", err)
	}
	if gjson.GetBytes(p, "type").String() != "response.created" {
		t.Fatalf("expected response.created, got %s", p)
	}

	// Send in-flight response.steer while response is running
	steer := `{"type":"response.steer","previous_response_id":"r1","input":"Focus on networking"}`
	if err := c.WriteMessage(websocket.TextMessage, []byte(steer)); err != nil {
		t.Fatal(err)
	}

	select {
	case <-upstreamReceivedSteer:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream never received response.steer frame during in-flight generation")
	}
}
