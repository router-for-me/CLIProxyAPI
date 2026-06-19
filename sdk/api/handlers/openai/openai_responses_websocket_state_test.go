package openai

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

func TestResponsesWebsocketCodexInactiveSessionReplaysIncrementalCreate(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &websocketDirectCaptureExecutor{done: make(chan struct{})}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{
		ID:         "auth-ws-inactive-create",
		Provider:   "codex",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model-inactive-create"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() { _ = conn.Close() }()

	firstRequest := []byte(`{"type":"response.create","model":"test-model-inactive-create","input":[{"type":"message","id":"msg-1","role":"user","content":"first"}]}`)
	if errWrite := conn.WriteMessage(websocket.TextMessage, firstRequest); errWrite != nil {
		t.Fatalf("write first websocket message: %v", errWrite)
	}
	if _, _, errRead := conn.ReadMessage(); errRead != nil {
		t.Fatalf("read first websocket response: %v", errRead)
	}

	secondRequest := []byte(`{"type":"response.create","input":[{"type":"message","id":"msg-2","role":"user","content":"second"}]}`)
	if errWrite := conn.WriteMessage(websocket.TextMessage, secondRequest); errWrite != nil {
		t.Fatalf("write second websocket message: %v", errWrite)
	}
	if _, _, errRead := conn.ReadMessage(); errRead != nil {
		t.Fatalf("read second websocket response: %v", errRead)
	}

	select {
	case <-executor.done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for websocket requests")
	}

	payloads := executor.Payloads()
	if len(payloads) != 2 {
		t.Fatalf("passthrough payload count = %d, want 2", len(payloads))
	}
	secondPayload := payloads[1]
	if gjson.GetBytes(secondPayload, "previous_response_id").Exists() {
		t.Fatalf("previous_response_id leaked into replay payload: %s", secondPayload)
	}
	if got := gjson.GetBytes(secondPayload, "type").String(); got != "" {
		t.Fatalf("replay payload type = %s, want empty: %s", got, secondPayload)
	}
	input := gjson.GetBytes(secondPayload, "input").Array()
	if len(input) != 3 {
		t.Fatalf("replay input len = %d, want 3: %s", len(input), secondPayload)
	}
	if input[0].Get("id").String() != "msg-1" || input[1].Get("id").String() != "out-1" || input[2].Get("id").String() != "msg-2" {
		t.Fatalf("unexpected replay input order: %s", secondPayload)
	}
}

func TestResponsesWebsocketRequestRequiresUpstreamContext(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
		want bool
	}{
		{
			name: "append",
			raw:  []byte(`{"type":"response.append","input":[{"type":"message","id":"msg-2"}]}`),
			want: true,
		},
		{
			name: "previous response id",
			raw:  []byte(`{"type":"response.create","previous_response_id":"resp-1","input":[{"type":"message","id":"msg-2"}]}`),
			want: true,
		},
		{
			name: "incremental create",
			raw:  []byte(`{"type":"response.create","input":[{"type":"message","id":"msg-2","role":"user"}]}`),
			want: true,
		},
		{
			name: "transcript replacement create",
			raw:  []byte(`{"type":"response.create","input":[{"type":"message","id":"msg-2","role":"assistant"}]}`),
			want: false,
		},
		{
			name: "compaction create",
			raw:  []byte(`{"type":"response.create","input":[{"type":"message","id":"msg-2","role":"user"},{"type":"compaction","encrypted_content":"summary"}]}`),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := responsesWebsocketRequestRequiresUpstreamContext(tt.raw); got != tt.want {
				t.Fatalf("requires upstream context = %v, want %v", got, tt.want)
			}
		})
	}
}
