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

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Codex sends response.interrupt for the response it is still reading, so the
// frame can cross that response's terminal event. On an HTTP upstream there is
// no socket to forward it to, and the turn is already over: the frame must be a
// no-op, as it is on a websocket upstream. An error here is read by Codex as the
// first event of its next response.create and fails the whole turn.
func TestResponsesHTTPLateInterruptForEndedResponseIsNoop(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := fmt.Sprintf("r%d", requests.Add(1))
		w.Header().Set("Content-Type", "text/event-stream")
		if _, errWrite := fmt.Fprintf(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":%q}}\n\n", id); errWrite != nil {
			t.Errorf("created: %v", errWrite)
			return
		}
		if _, errWrite := fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":%q,\"status\":\"completed\",\"output\":[]}}\n\n", id); errWrite != nil {
			t.Errorf("completed: %v", errWrite)
		}
	}))
	defer upstream.Close()
	cfg := &config.Config{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))
	const authID = "http-late-interrupt"
	const model = "http-late-interrupt-model"
	_, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID: authID, Provider: "codex", Status: coreauth.StatusActive,
		Attributes: map[string]string{"api_key": "synthetic-key", "base_url": upstream.URL},
	})
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
	defer registry.GetGlobalRegistry().UnregisterClient(authID)
	handler := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
	router := gin.New()
	router.GET("/v1/responses", handler.ResponsesWebsocket)
	downstream := httptest.NewServer(router)
	defer downstream.Close()
	client, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(downstream.URL, "http")+"/v1/responses", nil)
	if errDial != nil {
		t.Fatal(errDial)
	}
	defer func() { _ = client.Close() }()
	if errDeadline := client.SetReadDeadline(time.Now().Add(5 * time.Second)); errDeadline != nil {
		t.Fatal(errDeadline)
	}
	send := func(payload string) {
		t.Helper()
		if errWrite := client.WriteMessage(websocket.TextMessage, []byte(payload)); errWrite != nil {
			t.Fatal(errWrite)
		}
	}
	expect := func(event, id string) {
		t.Helper()
		_, payload, errRead := client.ReadMessage()
		if errRead != nil {
			t.Fatal(errRead)
		}
		if ojson.GetBytesNoCopy(payload, "type").String() != event || ojson.GetBytesNoCopy(payload, "response.id").String() != id {
			t.Fatalf("expected %s for %s, got %s", event, id, payload)
		}
	}
	send(fmt.Sprintf(`{"type":"response.create","model":%q,"input":[]}`, model))
	expect("response.created", "r1")
	expect("response.completed", "r1")
	send(`{"type":"response.interrupt","response_id":"r1","mode":"discard_partial_items"}`)
	send(fmt.Sprintf(`{"type":"response.create","model":%q,"input":[]}`, model))
	expect("response.created", "r2")
	expect("response.completed", "r2")
	// An id this socket never ran is still rejected.
	send(`{"type":"response.interrupt","response_id":"unknown"}`)
	expect("error", "")
}
