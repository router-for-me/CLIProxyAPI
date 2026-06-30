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

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

type websocketCompactStateExecutor struct {
	mu             sync.Mutex
	streamPayloads [][]byte
	compactPayload []byte
}

func (e *websocketCompactStateExecutor) Identifier() string { return "codex" }

func (e *websocketCompactStateExecutor) Execute(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	e.mu.Lock()
	e.compactPayload = bytes.Clone(req.Payload)
	e.mu.Unlock()
	if opts.Alt != "responses/compact" {
		return coreexecutor.Response{}, fmt.Errorf("unexpected non-compact execute alt: %q", opts.Alt)
	}
	return coreexecutor.Response{Payload: []byte(`{"id":"resp-compact","object":"response.compaction","output":[{"type":"compaction","encrypted_content":"compact-state"}]}`)}, nil
}

func (e *websocketCompactStateExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.mu.Lock()
	callIndex := len(e.streamPayloads)
	e.streamPayloads = append(e.streamPayloads, bytes.Clone(req.Payload))
	e.mu.Unlock()

	payload := []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp-%d","output":[{"type":"message","id":"assistant-%d"}]}}`, callIndex+1, callIndex+1))
	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: payload}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *websocketCompactStateExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *websocketCompactStateExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketCompactStateExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestResponsesWebsocketUsesCompactStateForNextIncrementalTurn(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &websocketCompactStateExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{
		ID:         "auth-codex-compact-state",
		Provider:   "codex",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "gpt-5-codex"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)
	router.POST("/v1/responses/compact", h.Compact)

	server := httptest.NewServer(router)
	defer server.Close()

	headers := http.Header{}
	headers.Set("X-Codex-Turn-Metadata", `{"session_id":"compact-state-session"}`)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			t.Fatalf("close websocket: %v", errClose)
		}
	}()

	first := `{"type":"response.create","model":"gpt-5-codex","input":[{"type":"message","id":"msg-old","role":"user","content":"old"}]}`
	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(first)); errWrite != nil {
		t.Fatalf("write first websocket message: %v", errWrite)
	}
	if _, payload, errRead := conn.ReadMessage(); errRead != nil {
		t.Fatalf("read first websocket message: %v", errRead)
	} else if got := gjson.GetBytes(payload, "type").String(); got != wsEventTypeCompleted {
		t.Fatalf("first payload type = %s, want %s", got, wsEventTypeCompleted)
	}

	compactReq, errReq := http.NewRequest(http.MethodPost, server.URL+"/v1/responses/compact", strings.NewReader(`{"model":"gpt-5-codex","input":[{"type":"message","id":"msg-old","role":"user","content":"old"}]}`))
	if errReq != nil {
		t.Fatalf("new compact request: %v", errReq)
	}
	compactReq.Header.Set("Content-Type", "application/json")
	compactReq.Header.Set("X-Codex-Turn-Metadata", `{"session_id":"compact-state-session"}`)
	compactResp, errPost := server.Client().Do(compactReq)
	if errPost != nil {
		t.Fatalf("compact request failed: %v", errPost)
	}
	if errClose := compactResp.Body.Close(); errClose != nil {
		t.Fatalf("close compact response body: %v", errClose)
	}
	if compactResp.StatusCode != http.StatusOK {
		t.Fatalf("compact status = %d, want %d", compactResp.StatusCode, http.StatusOK)
	}

	next := `{"type":"response.create","input":[{"type":"message","id":"msg-next","role":"user","content":"next"}]}`
	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(next)); errWrite != nil {
		t.Fatalf("write next websocket message: %v", errWrite)
	}
	if _, payload, errRead := conn.ReadMessage(); errRead != nil {
		t.Fatalf("read next websocket message: %v", errRead)
	} else if got := gjson.GetBytes(payload, "type").String(); got != wsEventTypeCompleted {
		t.Fatalf("next payload type = %s, want %s", got, wsEventTypeCompleted)
	}

	executor.mu.Lock()
	defer executor.mu.Unlock()
	if executor.compactPayload == nil {
		t.Fatal("compact payload was not captured")
	}
	if len(executor.streamPayloads) != 2 {
		t.Fatalf("stream payload count = %d, want 2", len(executor.streamPayloads))
	}

	replayed := executor.streamPayloads[1]
	input := gjson.GetBytes(replayed, "input").Array()
	if len(input) != 2 {
		t.Fatalf("post-compact input len = %d, want compact item plus next message: %s", len(input), replayed)
	}
	if got := input[0].Get("type").String(); got != "compaction" {
		t.Fatalf("post-compact input[0].type = %q, want compaction: %s", got, replayed)
	}
	if got := input[1].Get("id").String(); got != "msg-next" {
		t.Fatalf("post-compact input[1].id = %q, want msg-next: %s", got, replayed)
	}
	if strings.Contains(string(replayed), "msg-old") || strings.Contains(string(replayed), "assistant-1") {
		t.Fatalf("post-compact replay included stale pre-compact state: %s", replayed)
	}
}
