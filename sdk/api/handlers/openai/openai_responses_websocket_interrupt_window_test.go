package openai

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/tidwall/gjson"
)

// Once a turn's terminal event has reached the client, the turn is over even if
// the upstream data channel has not closed yet. A late response.interrupt must
// then be rejected by the input reader, not completed locally as a synthetic
// response.incomplete for an unrelated response_id.
func TestForwardResponsesWebsocketClosesLocalInterruptWindowAtTerminalEvent(t *testing.T) {
	for _, tc := range []struct {
		terminal string
		// wantForwardErr is the existing end-of-stream result. A failed turn is
		// not completed, so the forwarder closes the client socket when the
		// data channel closes; only the interrupt window is under test here.
		wantForwardErr error
	}{
		{terminal: `{"type":"response.completed","response":{"id":"r1","status":"completed","output":[]}}`},
		{terminal: `{"type":"response.incomplete","response":{"id":"r1","status":"incomplete","output":[]}}`},
		{
			terminal:       `{"type":"response.failed","response":{"id":"r1","status":"failed","error":{"code":"server_error","message":"upstream failed"}}}`,
			wantForwardErr: websocket.ErrCloseSent,
		},
	} {
		terminal := tc.terminal
		event := gjson.Get(terminal, "type").String()
		t.Run(event, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			local := newResponsesLocalInterrupt()
			data := make(chan []byte, 2)
			errCh := make(chan *interfaces.ErrorMessage)
			delivered := make(chan bool, 1)
			forwardDone := make(chan error, 1)

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := responsesWebsocketUpgrader.Upgrade(w, r, nil)
				if err != nil {
					forwardDone <- err
					return
				}
				defer func() { _ = conn.Close() }()
				ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
				ctx.Request = r
				h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil))
				_, _, _, _, errForward := h.forwardResponsesWebsocket(ctx, newResponsesWebsocketWriter(conn), func(...interface{}) {},
					data, errCh, newInMemoryWebsocketTimelineLog(), "session-1",
					responsesWebsocketForwardOptions{localInterrupt: local})
				forwardDone <- errForward
			}))
			defer server.Close()

			client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = client.Close() }()
			if err := client.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
				t.Fatal(err)
			}

			data <- []byte(`{"type":"response.created","response":{"id":"r1"}}`)
			data <- []byte(terminal)
			for _, want := range []string{"response.created", event} {
				_, payload, errRead := client.ReadMessage()
				if errRead != nil {
					t.Fatal(errRead)
				}
				if got := gjson.GetBytes(payload, "type").String(); got != want {
					t.Fatalf("event = %q, want %q: %s", got, want, payload)
				}
			}
			// The client has seen the terminal event; the upstream channel is still
			// open. An interrupt arriving now belongs to no running turn.
			delivered <- local.deliver([]byte(`{"type":"response.interrupt","response_id":"unknown"}`))
			if <-delivered {
				close(data)
				<-forwardDone
				t.Fatal("late interrupt was accepted for local completion after the terminal event")
			}
			close(data)
			select {
			case errForward := <-forwardDone:
				if !errors.Is(errForward, tc.wantForwardErr) {
					t.Fatalf("forward error = %v, want %v", errForward, tc.wantForwardErr)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("forward did not return after the data channel closed")
			}
		})
	}
}

// A local interrupt delivered while a response is running still completes it.
func TestForwardResponsesWebsocketLocalInterruptStillCompletesRunningTurn(t *testing.T) {
	for _, name := range []string{"first response", "second response on the same stream"} {
		t.Run(name, func(t *testing.T) { testLocalInterruptCompletesRunningTurn(t, name != "first response") })
	}
}

func testLocalInterruptCompletesRunningTurn(t *testing.T, afterEarlierResponse bool) {
	gin.SetMode(gin.TestMode)
	local := newResponsesLocalInterrupt()
	data := make(chan []byte, 3)
	errCh := make(chan *interfaces.ErrorMessage)
	forwardDone := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := responsesWebsocketUpgrader.Upgrade(w, r, nil)
		if err != nil {
			forwardDone <- err
			return
		}
		defer func() { _ = conn.Close() }()
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = r
		h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil))
		_, _, _, _, errForward := h.forwardResponsesWebsocket(ctx, newResponsesWebsocketWriter(conn), func(...interface{}) {},
			data, errCh, newInMemoryWebsocketTimelineLog(), "session-1",
			responsesWebsocketForwardOptions{localInterrupt: local})
		forwardDone <- errForward
	}))
	defer server.Close()
	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	if err := client.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if afterEarlierResponse {
		data <- []byte(`{"type":"response.created","response":{"id":"r0"}}`)
		data <- []byte(`{"type":"response.completed","response":{"id":"r0","status":"completed","output":[]}}`)
		for i := 0; i < 2; i++ {
			if _, _, errRead := client.ReadMessage(); errRead != nil {
				t.Fatal(errRead)
			}
		}
	}
	data <- []byte(`{"type":"response.created","response":{"id":"r-http"}}`)
	if _, payload, errRead := client.ReadMessage(); errRead != nil || gjson.GetBytes(payload, "type").String() != "response.created" {
		t.Fatalf("created = %s, err %v", payload, errRead)
	}
	if !local.deliver([]byte(`{"type":"response.interrupt","response_id":"r-http"}`)) {
		t.Fatal("interrupt for the running turn was not accepted")
	}
	_, payload, errRead := client.ReadMessage()
	if errRead != nil {
		t.Fatal(errRead)
	}
	if got := fmt.Sprintf("%s/%s", gjson.GetBytes(payload, "type"), gjson.GetBytes(payload, "response.id")); got != "response.incomplete/r-http" {
		t.Fatalf("interrupt result = %s, want response.incomplete/r-http: %s", got, payload)
	}
	<-forwardDone
}
