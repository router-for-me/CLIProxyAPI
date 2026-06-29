package openai

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

type websocketContextLostReplayExecutor struct {
	mu       sync.Mutex
	payloads [][]byte
	active   bool
}

func (e *websocketContextLostReplayExecutor) Identifier() string { return "codex" }

func (e *websocketContextLostReplayExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketContextLostReplayExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.mu.Lock()
	e.payloads = append(e.payloads, bytes.Clone(req.Payload))
	count := len(e.payloads)
	e.mu.Unlock()

	chunks := make(chan coreexecutor.StreamChunk, 1)
	if count == 2 {
		chunks <- coreexecutor.StreamChunk{Payload: []byte(`{"type":"error","status":500,"error":{"message":"codex websockets executor: request requires existing websocket session","type":"server_error","code":"internal_server_error"}}`)}
	} else {
		chunks <- coreexecutor.StreamChunk{Payload: []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp-%d","output":[{"type":"message","id":"out-%d"}]}}`, count, count))}
	}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *websocketContextLostReplayExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *websocketContextLostReplayExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketContextLostReplayExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (e *websocketContextLostReplayExecutor) UpstreamSessionActive(string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.active
}

func (e *websocketContextLostReplayExecutor) Payloads() [][]byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([][]byte, len(e.payloads))
	for i := range e.payloads {
		out[i] = bytes.Clone(e.payloads[i])
	}
	return out
}

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

func TestResponsesWebsocketRetriesAppendAfterPassthroughSessionLoss(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &websocketContextLostReplayExecutor{active: true}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{
		ID:         "auth-ws-lost-append",
		Provider:   "codex",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model-lost-append"}})
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

	firstRequest := []byte(`{"type":"response.create","model":"test-model-lost-append","input":[{"type":"message","id":"msg-1","role":"user","content":"first"}]}`)
	if errWrite := conn.WriteMessage(websocket.TextMessage, firstRequest); errWrite != nil {
		t.Fatalf("write first websocket message: %v", errWrite)
	}
	if _, payload, errRead := conn.ReadMessage(); errRead != nil {
		t.Fatalf("read first websocket response: %v", errRead)
	} else if got := gjson.GetBytes(payload, "type").String(); got != wsEventTypeCompleted {
		t.Fatalf("first response type = %s, want %s: %s", got, wsEventTypeCompleted, payload)
	}

	secondRequest := []byte(`{"type":"response.append","input":[{"type":"message","id":"msg-2","role":"user","content":"second"}]}`)
	if errWrite := conn.WriteMessage(websocket.TextMessage, secondRequest); errWrite != nil {
		t.Fatalf("write second websocket message: %v", errWrite)
	}
	if _, payload, errRead := conn.ReadMessage(); errRead != nil {
		t.Fatalf("read second websocket response: %v", errRead)
	} else if got := gjson.GetBytes(payload, "type").String(); got != wsEventTypeCompleted {
		t.Fatalf("second response type = %s, want %s: %s", got, wsEventTypeCompleted, payload)
	}

	payloads := executor.Payloads()
	if len(payloads) != 3 {
		t.Fatalf("upstream payload count = %d, want 3", len(payloads))
	}
	if got := gjson.GetBytes(payloads[1], "type").String(); got != wsRequestTypeAppend {
		t.Fatalf("second upstream payload type = %s, want %s: %s", got, wsRequestTypeAppend, payloads[1])
	}
	replayPayload := payloads[2]
	if got := gjson.GetBytes(replayPayload, "type").String(); got != "" {
		t.Fatalf("replay payload type = %s, want empty: %s", got, replayPayload)
	}
	if gjson.GetBytes(replayPayload, "previous_response_id").Exists() {
		t.Fatalf("previous_response_id leaked into replay payload: %s", replayPayload)
	}
	input := gjson.GetBytes(replayPayload, "input").Array()
	if len(input) != 3 {
		t.Fatalf("replay input len = %d, want 3: %s", len(input), replayPayload)
	}
	if input[0].Get("id").String() != "msg-1" || input[1].Get("id").String() != "out-1" || input[2].Get("id").String() != "msg-2" {
		t.Fatalf("unexpected replay input order: %s", replayPayload)
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

func TestResponsesWebsocketTranscriptStateCacheEvictsOldestSession(t *testing.T) {
	cache := newResponsesWebsocketTranscriptStateCache(time.Minute)
	cache.maxSessions = 2
	cache.maxBytes = 0

	cache.record("session-a", testResponsesWebsocketTranscriptState("request-a"))
	cache.record("session-b", testResponsesWebsocketTranscriptState("request-b"))

	cache.mu.Lock()
	entryA := cache.sessions["session-a"]
	entryA.lastSeen = time.Now().Add(-time.Minute)
	cache.sessions["session-a"] = entryA
	cache.mu.Unlock()

	cache.record("session-c", testResponsesWebsocketTranscriptState("request-c"))

	if _, ok := cache.get("session-a"); ok {
		t.Fatal("expected oldest session to be evicted")
	}
	if _, ok := cache.get("session-b"); !ok {
		t.Fatal("expected newer session to remain cached")
	}
	if _, ok := cache.get("session-c"); !ok {
		t.Fatal("expected newest session to remain cached")
	}
}

func TestResponsesWebsocketTranscriptStateCacheDropsOversizedSession(t *testing.T) {
	cache := newResponsesWebsocketTranscriptStateCache(time.Minute)
	cache.maxSessions = 0
	cache.maxBytes = responsesWebsocketTranscriptStateEntrySize("session-a", testResponsesWebsocketTranscriptState("small")) + 1

	cache.record("session-a", testResponsesWebsocketTranscriptState("small"))
	if _, ok := cache.get("session-a"); !ok {
		t.Fatal("expected small session to be cached")
	}

	oversized := testResponsesWebsocketTranscriptState(strings.Repeat("x", cache.maxBytes+1))
	cache.record("session-a", oversized)
	if _, ok := cache.get("session-a"); ok {
		t.Fatal("expected oversized session to be dropped with stale state removed")
	}
}

func testResponsesWebsocketTranscriptState(request string) responsesWebsocketTranscriptState {
	return responsesWebsocketTranscriptState{
		lastRequest:                    []byte(request),
		lastResponseOutput:             []byte(`[{"id":"out-1"}]`),
		lastResponseID:                 "resp-1",
		lastResponsePendingToolCallIDs: []string{"call-1"},
		passthroughModelName:           "codex",
	}
}
