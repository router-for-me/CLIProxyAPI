package openai

import (
	"context"
	"encoding/json"
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
)

func TestResponsesWebsocketToolSessionKeyUsesConnectionFallback(t *testing.T) {
	const scopedSessionKey = "client-session:caller:abc123"
	if got := responsesWebsocketToolSessionKey(scopedSessionKey, "connection-a"); got != scopedSessionKey {
		t.Fatalf("authenticated tool session key = %q, want %q", got, scopedSessionKey)
	}

	anonymousA := responsesWebsocketToolSessionKey("", "connection-a")
	anonymousB := responsesWebsocketToolSessionKey("", "connection-b")
	if anonymousA == "" {
		t.Fatal("anonymous active connection did not receive a tool session key")
	}
	if anonymousA == anonymousB {
		t.Fatalf("anonymous connections share tool session key %q", anonymousA)
	}
	cache := newWebsocketToolOutputCache(0, 8)
	cache.record(anonymousA, "call-1", json.RawMessage(`{"type":"function_call","call_id":"call-1"}`))
	if _, ok := cache.get(anonymousA, "call-1"); !ok {
		t.Fatal("anonymous active connection could not read its tool cache")
	}
	if _, ok := cache.get(anonymousB, "call-1"); ok {
		t.Fatal("anonymous connection read another connection's tool cache")
	}
	if got := responsesWebsocketToolSessionKey("", ""); got != "" {
		t.Fatalf("empty connection tool session key = %q, want empty", got)
	}
}

func TestWebsocketDownstreamSessionKeyPrefersStableSessionHeaders(t *testing.T) {
	req := httptest.NewRequest("GET", "/v1/responses/ws", nil)
	req.Header.Set("X-Session-ID", "x-session")
	req.Header.Set("Session_id", "codex-session")
	req.Header.Set("X-Codex-Turn-Metadata", `{"session_id":"turn-session"}`)
	req.Header.Set("X-Client-Request-Id", "request-attempt")

	if got := websocketDownstreamSessionKey(req); got != "x-session" {
		t.Fatalf("session key = %q, want x-session", got)
	}
	req.Header.Del("X-Session-ID")
	if got := websocketDownstreamSessionKey(req); got != "codex-session" {
		t.Fatalf("session key = %q, want codex-session", got)
	}
	req.Header.Del("Session_id")
	if got := websocketDownstreamSessionKey(req); got != "turn-session" {
		t.Fatalf("session key = %q, want turn-session", got)
	}
	req.Header.Del("X-Codex-Turn-Metadata")
	if got := websocketDownstreamSessionKey(req); got != "request-attempt" {
		t.Fatalf("session key = %q, want request-attempt", got)
	}
}

func TestResponsesWebsocketRecordsAnonymousToolCallUnderConnectionKey(t *testing.T) {
	gin.SetMode(gin.TestMode)

	oldOutputCache := defaultWebsocketToolOutputCache
	oldCallCache := defaultWebsocketToolCallCache
	oldSessionRefs := defaultWebsocketToolSessionRefs
	outputCache := newWebsocketToolOutputCache(0, websocketToolOutputCacheMaxPerSession)
	callCache := newWebsocketToolOutputCache(0, websocketToolOutputCacheMaxPerSession)
	defaultWebsocketToolOutputCache = outputCache
	defaultWebsocketToolCallCache = callCache
	defaultWebsocketToolSessionRefs = newWebsocketToolSessionRefCounter()
	t.Cleanup(func() {
		defaultWebsocketToolOutputCache = oldOutputCache
		defaultWebsocketToolCallCache = oldCallCache
		defaultWebsocketToolSessionRefs = oldSessionRefs
	})

	executor := &websocketCompactionCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: "auth-ws", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
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
	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"test-model","input":[{"type":"message","id":"msg-1"}]}`)); errWrite != nil {
		t.Fatalf("write websocket message: %v", errWrite)
	}
	if _, _, errRead := conn.ReadMessage(); errRead != nil {
		t.Fatalf("read websocket message: %v", errRead)
	}

	callCache.mu.Lock()
	foundConnectionKey := false
	for key, session := range callCache.sessions {
		if strings.HasPrefix(key, "connection:") && session != nil && len(session.outputs["call-1"]) != 0 {
			foundConnectionKey = true
			break
		}
	}
	callCache.mu.Unlock()
	if !foundConnectionKey {
		t.Fatal("anonymous websocket tool call was not recorded under a connection-scoped key")
	}

	if errClose := conn.Close(); errClose != nil {
		t.Fatalf("close websocket: %v", errClose)
	}
	deadline := time.Now().Add(time.Second)
	for {
		callCache.mu.Lock()
		remainingSessions := len(callCache.sessions)
		callCache.mu.Unlock()
		if remainingSessions == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("anonymous tool cache retained %d sessions after disconnect", remainingSessions)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
