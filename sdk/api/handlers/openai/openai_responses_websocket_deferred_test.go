package openai

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	requestlogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

// websocketDeferredRequestExecutor records each upstream request the way real
// executors do with request-log disabled, which defers its body on the gin
// context for a possible error-only log on plain HTTP requests.
type websocketDeferredRequestExecutor struct {
	websocketDirectCaptureExecutor
	failOnTurn int
}

func (e *websocketDeferredRequestExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	helps.RecordAPIRequest(ctx, &internalconfig.Config{}, helps.UpstreamRequestLog{URL: "https://upstream.example/responses", Method: "POST", Body: req.Payload})
	e.websocketDirectCaptureExecutor.mu.Lock()
	turn := len(e.websocketDirectCaptureExecutor.payloads) + 1
	if e.failOnTurn > 0 && turn == e.failOnTurn {
		e.websocketDirectCaptureExecutor.failStatus = 502
	} else {
		e.websocketDirectCaptureExecutor.failStatus = 0
	}
	e.websocketDirectCaptureExecutor.mu.Unlock()
	return e.websocketDirectCaptureExecutor.ExecuteStream(ctx, auth, req, opts)
}

// websocketDeferredRouterHost records an upstream request from the model
// router, as a plugin router's host HTTP callback does, and leaves the route
// unchanged.
type websocketDeferredRouterHost struct{}

func (*websocketDeferredRouterHost) HasModelRouters() bool { return true }

func (*websocketDeferredRouterHost) RouteModel(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
	helps.RecordAPIRequest(ctx, &internalconfig.Config{}, helps.UpstreamRequestLog{URL: "https://router.example/route", Method: "POST", Body: req.Body})
	return pluginapi.ModelRouteResponse{}, false
}

func TestResponsesWebsocketKeepsNoDeferredUpstreamRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const turns = 3
	// The last turn fails upstream, which ends the socket.
	executor := &websocketDeferredRequestExecutor{failOnTurn: turns}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: "auth-ws-deferred", Provider: "codex", Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	base.SetModelRouterHost(&websocketDeferredRouterHost{})
	h := NewOpenAIResponsesAPIHandler(base)
	// Snapshot the deferred requests on the request goroutine once the handler
	// returns, so the check cannot race the handler.
	handlerDone := make(chan []requestlogging.DeferredAPIRequest, 1)
	router := gin.New()
	router.GET("/v1/responses", func(c *gin.Context) {
		h.ResponsesWebsocket(c)
		value, _ := c.Get(requestlogging.DeferredAPIRequestContextKey)
		requests, _ := value.([]requestlogging.DeferredAPIRequest)
		handlerDone <- requests
	})
	server := httptest.NewServer(router)
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	readType := func(label string) string {
		t.Helper()
		_, msg, errRead := conn.ReadMessage()
		if errRead != nil {
			t.Fatalf("%s read: %v", label, errRead)
		}
		return gjson.GetBytes(msg, "type").String()
	}

	// A prewarm turn is answered locally after routing: only the router records.
	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"test-model","generate":false}`)); errWrite != nil {
		t.Fatalf("prewarm write: %v", errWrite)
	}
	if got := readType("prewarm created"); got != "response.created" {
		t.Fatalf("prewarm first event = %q, want response.created", got)
	}
	if got := readType("prewarm completed"); got != wsEventTypeCompleted {
		t.Fatalf("prewarm second event = %q, want %s", got, wsEventTypeCompleted)
	}

	payload := `{"type":"response.create","model":"test-model","input":[{"type":"message","role":"user","content":"` + strings.Repeat("x", 1024) + `"}]}`
	for turn := 1; turn < turns; turn++ {
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(payload)); errWrite != nil {
			t.Fatalf("turn %d write: %v", turn, errWrite)
		}
		if got := readType("turn"); got != wsEventTypeCompleted {
			t.Fatalf("turn %d type = %q, want %s", turn, got, wsEventTypeCompleted)
		}
	}

	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(payload)); errWrite != nil {
		t.Fatalf("failed turn write: %v", errWrite)
	}
	// A terminal upstream error ends the socket: an error event, or a close
	// (non-exposed upstream errors drop the connection without a frame).
	if _, msg, errRead := conn.ReadMessage(); errRead == nil {
		if got := gjson.GetBytes(msg, "type").String(); got != "error" {
			t.Fatalf("failed turn type = %q, want error (%s)", got, msg)
		}
	} else if closeErr := (*websocket.CloseError)(nil); !errors.As(errRead, &closeErr) {
		t.Fatalf("failed turn read: %v, want an error event or a close frame", errRead)
	}

	var requests []requestlogging.DeferredAPIRequest
	select {
	case requests = <-handlerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("websocket handler did not return after the failed turn")
	}
	if len(requests) != 0 {
		t.Fatalf("deferred upstream requests = %d on the websocket context, want none", len(requests))
	}
	if got := len(executor.Payloads()); got != turns {
		t.Fatalf("upstream requests = %d, want %d", got, turns)
	}
}
