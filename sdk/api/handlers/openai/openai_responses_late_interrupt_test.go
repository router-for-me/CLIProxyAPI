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

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

// A delayed control frame must not queue an error for the next create, nor
// cancel its response when the old response ID is repeated during that turn.
func TestResponsesLateInterruptDoesNotPoisonNextCreate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var connections atomic.Int32
	httpCanceled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			w.Header().Set("Content-Type", "text/event-stream")
			if _, errWrite := fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r-http\"}}\n\n"); errWrite != nil {
				t.Errorf("HTTP created: %v", errWrite)
				return
			}
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			close(httpCanceled)
			return
		}
		connections.Add(1)
		conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Errorf("upgrade: %v", errUpgrade)
			return
		}
		defer func() {
			if errClose := conn.Close(); errClose != nil {
				t.Errorf("close upstream: %v", errClose)
			}
		}()
		for _, id := range []string{"r1", "r2"} {
			_, create, errRead := conn.ReadMessage()
			if errRead != nil {
				return
			}
			if gjson.GetBytes(create, "type").String() != "response.create" {
				t.Errorf("terminal interrupt reached upstream: %s", create)
				return
			}
			if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"type":"response.created","response":{"id":%q}}`, id))); errWrite != nil {
				t.Errorf("created: %v", errWrite)
				return
			}
			if id == "r2" {
				// A valid active-r2 interrupt is a deterministic barrier proving
				// preceding duplicate-r1 interrupts were consumed downstream.
				_, interrupt, errInterrupt := conn.ReadMessage()
				if errInterrupt != nil {
					return
				}
				if gjson.GetBytes(interrupt, "response_id").String() != "r2" {
					t.Errorf("old interrupt affected active r2: %s", interrupt)
					return
				}
			}
			if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":%q,"status":"completed","output":[]}}`, id))); errWrite != nil {
				t.Errorf("completed: %v", errWrite)
				return
			}
		}
		_, _, _ = conn.ReadMessage()
	}))
	defer upstream.Close()
	cfg := &config.Config{}
	cfg.RequestLog = true
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))
	const authID = "late-interrupt-integration"
	const model = "late-interrupt-model"
	_, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID: authID, Provider: "codex", Status: coreauth.StatusActive,
		Attributes: map[string]string{"api_key": "synthetic-key", "base_url": upstream.URL, "websockets": "true"},
	})
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
	defer registry.GetGlobalRegistry().UnregisterClient(authID)
	handler := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
	timelines := make(chan string, 1)
	router := gin.New()
	router.GET("/v1/responses", func(c *gin.Context) {
		handler.ResponsesWebsocket(c)
		body, _ := c.Get(wsTimelineBodyKey)
		data, _ := body.([]byte)
		timelines <- string(data)
	})
	downstream := httptest.NewServer(router)
	defer downstream.Close()
	client, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(downstream.URL, "http")+"/v1/responses", nil)
	if errDial != nil {
		t.Fatal(errDial)
	}
	clientClosed := false
	defer func() {
		if !clientClosed {
			if errClose := client.Close(); errClose != nil {
				t.Errorf("close client: %v", errClose)
			}
		}
	}()
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
		if gjson.GetBytes(payload, "type").String() != event || gjson.GetBytes(payload, "response.id").String() != id {
			t.Fatalf("expected %s for %s, got %s", event, id, payload)
		}
	}
	send(fmt.Sprintf(`{"type":"response.create","model":%q,"input":[]}`, model))
	expect("response.created", "r1")
	expect("response.completed", "r1")
	send(`{"type":"response.interrupt","response_id":"unknown"}`)
	expect("error", "")
	send(`{"type":"response.interrupt","response_id":"r1","mode":"discard_partial_items"}`)
	send(fmt.Sprintf(`{"type":"response.create","model":%q,"previous_response_id":"r1","input":[]}`, model))
	expect("response.created", "r2")
	for i := 0; i < 2; i++ {
		send(`{"type":"response.interrupt","response_id":"r1","mode":"discard_partial_items"}`)
	}
	send(`{"type":"response.interrupt","response_id":"r2","mode":"discard_partial_items"}`)
	expect("response.completed", "r2")
	// Switching the next turn to HTTP leaves an idle websocket cached. A
	// terminal r1 interrupt must be a no-op, while r-http must cancel locally.
	auth, _ := manager.GetByID(authID)
	auth.Attributes["websockets"] = "false"
	if _, errUpdate := manager.Update(context.Background(), auth); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	send(fmt.Sprintf(`{"type":"response.create","model":%q,"input":[{"role":"user","content":"next turn"}]}`, model))
	expect("response.created", "r-http")
	send(`{"type":"response.interrupt","response_id":"r1"}`)
	send(`{"type":"response.interrupt","response_id":"r-http"}`)
	expect("response.incomplete", "r-http")
	select {
	case <-httpCanceled:
	case <-time.After(3 * time.Second):
		t.Fatal("HTTP fallback was not canceled locally")
	}
	if got := connections.Load(); got != 1 {
		t.Fatalf("connections = %d, want one retained upstream", got)
	}
	if errClose := client.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	clientClosed = true
	select {
	case timeline := <-timelines:
		for _, outcome := range []string{"handled", "local_http", "rejected"} {
			if !strings.Contains(timeline, `"outcome":"`+outcome+`"`) {
				t.Errorf("missing interrupt outcome %s", outcome)
			}
		}
	case <-time.After(3 * time.Second):
		t.Fatal("interrupt timeline was not finalized")
	}
}

// Interrupt diagnostics must exist even before the first create, and must not
// copy private extension fields, credentials, or raw client-controlled IDs.
func TestResponsesInterruptDiagnosticsAreSanitized(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.SDKConfig{RequestLog: true}
	handler := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(cfg, nil))
	timelines := make(chan string, 1)
	router := gin.New()
	router.GET("/v1/responses", func(c *gin.Context) {
		handler.ResponsesWebsocket(c)
		body, _ := c.Get(wsTimelineBodyKey)
		data, _ := body.([]byte)
		timelines <- string(data)
	})
	downstream := httptest.NewServer(router)
	defer downstream.Close()
	client, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(downstream.URL, "http")+"/v1/responses", nil)
	if errDial != nil {
		t.Fatal(errDial)
	}
	clientClosed := false
	defer func() {
		if !clientClosed {
			if errClose := client.Close(); errClose != nil {
				t.Errorf("close client: %v", errClose)
			}
		}
	}()
	if errWrite := client.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.interrupt","response_id":"private-id-token","mode":"secret-mode","authorization":"Bearer secret-credential","input":"private-conversation"}`)); errWrite != nil {
		t.Fatal(errWrite)
	}
	if errDeadline := client.SetReadDeadline(time.Now().Add(3 * time.Second)); errDeadline != nil {
		t.Fatal(errDeadline)
	}
	_, payload, errRead := client.ReadMessage()
	if errRead != nil {
		t.Fatal(errRead)
	}
	if gjson.GetBytes(payload, "type").String() != "error" {
		t.Fatalf("unknown interrupt must still fail: %s", payload)
	}
	if errClose := client.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	clientClosed = true
	select {
	case timeline := <-timelines:
		for _, event := range []string{"Event: websocket.interrupt", "Event: websocket.interrupt.outcome", `"outcome":"rejected"`} {
			if !strings.Contains(timeline, event) {
				t.Errorf("missing %s in timeline: %s", event, timeline)
			}
		}
		for _, secret := range []string{"private-id-token", "secret-mode", "secret-credential", "private-conversation"} {
			if strings.Contains(timeline, secret) {
				t.Errorf("interrupt diagnostic leaked %s", secret)
			}
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeline was not finalized")
	}
}
