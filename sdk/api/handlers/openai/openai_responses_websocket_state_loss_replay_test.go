package openai

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

type websocketStateLossReplayExecutor struct {
	mu               sync.Mutex
	payloads         [][]byte
	downstreamSocket []bool
}

func (e *websocketStateLossReplayExecutor) Identifier() string { return "codex" }

func (e *websocketStateLossReplayExecutor) Execute(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketStateLossReplayExecutor) ExecuteStream(ctx context.Context, _ *coreauth.Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.mu.Lock()
	e.payloads = append(e.payloads, bytes.Clone(req.Payload))
	e.downstreamSocket = append(e.downstreamSocket, cliproxyexecutor.DownstreamWebsocket(ctx))
	call := len(e.payloads)
	e.mu.Unlock()

	chunks := make(chan cliproxyexecutor.StreamChunk, 3)
	switch call {
	case 1:
		chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"type":"response.completed","response":{"id":"resp-1","output":[]}}`)}
	case 2:
		chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"type":"response.created","response":{"id":"resp-2"}}`)}
		chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"type":"response.output_item.added","output_index":0,"item":{"id":"rs-1","type":"reasoning","summary":[]}}`)}
		chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"type":"error","status":400,"error":{"type":"invalid_request_error","message":"No tool output found for function call call-1.","param":"input"}}`)}
	default:
		chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"type":"response.completed","response":{"id":"resp-3","output":[{"type":"message","id":"msg-3","role":"assistant","content":[{"type":"output_text","text":"done"}]}]}}`)}
	}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *websocketStateLossReplayExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *websocketStateLossReplayExecutor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketStateLossReplayExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (e *websocketStateLossReplayExecutor) attempts() ([][]byte, []bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	payloads := make([][]byte, len(e.payloads))
	for i := range e.payloads {
		payloads[i] = bytes.Clone(e.payloads[i])
	}
	return payloads, append([]bool(nil), e.downstreamSocket...)
}

func TestResponsesWebsocketReplaysStateLossOverHTTPWithoutLeakingProvisionalEvents(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &websocketStateLossReplayExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{
		ID:         "auth-ws",
		Provider:   executor.Identifier(),
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)
	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, errDial := websocket.DefaultDialer.Dial(wsURL, nil)
	if errDial != nil {
		t.Fatalf("dial websocket: %v", errDial)
	}
	defer func() { _ = conn.Close() }()

	requests := []string{
		`{"type":"response.create","model":"test-model","generate":false,"input":[{"type":"message","role":"user","content":"warm up"}]}`,
		`{"type":"response.create","model":"test-model","generate":true,"input":[{"type":"function_call_output","call_id":"call-1","output":"ok"}]}`,
	}
	for i := range requests {
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(requests[i])); errWrite != nil {
			t.Fatalf("write request %d: %v", i+1, errWrite)
		}
		for {
			_, payload, errRead := conn.ReadMessage()
			if errRead != nil {
				t.Fatalf("read request %d response: %v", i+1, errRead)
			}
			eventType := gjson.GetBytes(payload, "type").String()
			if eventType == wsEventTypeError {
				t.Fatalf("request %d leaked upstream error: %s", i+1, payload)
			}
			if eventType == "response.created" || eventType == "response.output_item.added" {
				t.Fatalf("request %d leaked provisional event before replay: %s", i+1, payload)
			}
			if eventType == wsEventTypeCompleted {
				break
			}
		}
	}

	payloads, downstreamSocket := executor.attempts()
	if len(payloads) != 3 {
		t.Fatalf("upstream attempts = %d, want 3", len(payloads))
	}
	if len(downstreamSocket) != 3 || !downstreamSocket[0] || !downstreamSocket[1] || downstreamSocket[2] {
		t.Fatalf("downstream websocket markers = %v, want [true true false]", downstreamSocket)
	}
	if !gjson.GetBytes(payloads[1], "generate").Exists() {
		t.Fatalf("websocket attempt unexpectedly stripped generate: %s", payloads[1])
	}
	if gjson.GetBytes(payloads[2], "generate").Exists() {
		t.Fatalf("HTTP replay leaked generate: %s", payloads[2])
	}
}
