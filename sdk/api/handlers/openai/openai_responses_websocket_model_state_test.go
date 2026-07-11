package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

func TestResponsesWebsocketClearsPassthroughModelAfterNonPassthroughTurn(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldTranscriptCache := defaultResponsesWebsocketTranscriptStateCache
	defaultResponsesWebsocketTranscriptStateCache = newResponsesWebsocketTranscriptStateCache(0)
	t.Cleanup(func() {
		defaultResponsesWebsocketTranscriptStateCache = oldTranscriptCache
	})
	recordResponsesWebsocketTranscriptState("model-transition-session", responsesWebsocketTranscriptState{
		lastRequest:          []byte(`{"model":"passthrough-model","input":[{"type":"message","id":"msg-1","role":"user","content":"first"}]}`),
		lastResponseOutput:   []byte(`[{"type":"message","id":"out-1"}]`),
		lastResponseID:       "resp-1",
		passthroughModelName: "passthrough-model",
	})

	executor := &websocketDirectCaptureExecutor{active: true}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	authWebsocket := &coreauth.Auth{
		ID:         "auth-model-transition-ws",
		Provider:   executor.Identifier(),
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
	}
	if _, err := manager.Register(context.Background(), authWebsocket); err != nil {
		t.Fatalf("Register websocket auth: %v", err)
	}
	authHTTP := &coreauth.Auth{
		ID:       "auth-model-transition-http",
		Provider: executor.Identifier(),
		Status:   coreauth.StatusActive,
	}
	if _, err := manager.Register(context.Background(), authHTTP); err != nil {
		t.Fatalf("Register HTTP auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(authWebsocket.ID, authWebsocket.Provider, []*registry.ModelInfo{{ID: "passthrough-model"}})
	registry.GetGlobalRegistry().RegisterClient(authHTTP.ID, authHTTP.Provider, []*registry.ModelInfo{{ID: "http-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(authWebsocket.ID)
		registry.GetGlobalRegistry().UnregisterClient(authHTTP.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)
	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	headers := http.Header{}
	headers.Set("X-Codex-Turn-Metadata", `{"session_id":"model-transition-session"}`)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() { _ = conn.Close() }()

	requests := [][]byte{
		[]byte(`{"type":"response.create","model":"http-model","input":[{"type":"message","id":"msg-2","role":"user","content":"second"}]}`),
		[]byte(`{"type":"response.create","input":[{"type":"message","id":"msg-3","role":"user","content":"third"}]}`),
	}
	for i, request := range requests {
		if errWrite := conn.WriteMessage(websocket.TextMessage, request); errWrite != nil {
			t.Fatalf("write websocket message %d: %v", i+1, errWrite)
		}
		_, payload, errRead := conn.ReadMessage()
		if errRead != nil {
			t.Fatalf("read websocket response %d: %v", i+1, errRead)
		}
		if got := gjson.GetBytes(payload, "type").String(); got != wsEventTypeCompleted {
			t.Fatalf("response %d type = %s, want %s: %s", i+1, got, wsEventTypeCompleted, payload)
		}
	}

	payloads := executor.Payloads()
	if len(payloads) != 2 {
		t.Fatalf("upstream payload count = %d, want 2", len(payloads))
	}
	if got := gjson.GetBytes(payloads[1], "model").String(); got != "http-model" {
		t.Fatalf("second upstream model = %s, want http-model after non-passthrough turn: %s", got, payloads[1])
	}
}
