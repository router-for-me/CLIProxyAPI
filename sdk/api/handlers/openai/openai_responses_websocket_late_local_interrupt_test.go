package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/tidwall/gjson"
)

// Once a terminal payload (completion or failure) has been written
// downstream, a local interrupt must no longer be accepted for that turn:
// accepting it either fabricates a response.incomplete or drops the frame
// when the turn ends. The data channel stays open in this test so the
// forward loop is deterministically still in its post-terminal window when
// deliver is attempted.
func TestForwardResponsesWebsocketRejectsLocalInterruptAfterCompletion(t *testing.T) {
	testForwardResponsesWebsocketRejectsLocalInterruptAfterTerminal(t,
		`{"type":"response.completed","response":{"id":"resp-1","output":[]}}`, wsEventTypeCompleted, true)
}

func TestForwardResponsesWebsocketRejectsLocalInterruptAfterFailure(t *testing.T) {
	testForwardResponsesWebsocketRejectsLocalInterruptAfterTerminal(t,
		`{"type":"response.failed","response":{"id":"resp-1","status":"failed","output":[]}}`, "response.failed", false)
}

func testForwardResponsesWebsocketRejectsLocalInterruptAfterTerminal(t *testing.T, terminalPayload, terminalType string, wantCompleted bool) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	local := newResponsesLocalInterrupt()
	data := make(chan []byte, 1)
	errCh := make(chan *interfaces.ErrorMessage)
	var streamClosed atomic.Bool
	closeStream := func() {
		if streamClosed.CompareAndSwap(false, true) {
			close(data)
			close(errCh)
		}
	}
	// Always end the forward turn, even on assertion failure.
	defer closeStream()
	data <- []byte(terminalPayload)
	serverErrCh := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := responsesWebsocketUpgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			serverErrCh <- errUpgrade
			return
		}
		defer func() {
			if errClose := conn.Close(); errClose != nil {
				serverErrCh <- errClose
			}
		}()

		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = r

		// A nil handler panics on the stream-closed-before-completed path
		// (LoggingAPIResponseError), which the failure variant reaches; use
		// a real handler shell without an auth manager.
		testHandler := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(nil, nil))
		_, completedResponseID, _, errMsg, errForward := testHandler.forwardResponsesWebsocket(
			ctx,
			newResponsesWebsocketWriter(conn),
			func(...interface{}) {},
			data,
			errCh,
			newInMemoryWebsocketTimelineLog(),
			"session-1",
			responsesWebsocketForwardOptions{localInterrupt: local},
		)
		if errForward != nil {
			// The test ends the turn by canceling the request context. The
			// failure variant instead ends inside the pre-completion close
			// path, which reports ErrCloseSent after closing the socket.
			if errForward != context.Canceled && !errors.Is(errForward, websocket.ErrCloseSent) {
				serverErrCh <- errForward
				return
			}
		} else if wantCompleted {
			if errMsg != nil {
				serverErrCh <- fmt.Errorf("unexpected websocket error message: %v", errMsg.Error)
				return
			}
			if completedResponseID != "resp-1" {
				serverErrCh <- fmt.Errorf("completed response id = %q, want resp-1", completedResponseID)
				return
			}
		} else {
			// A failed turn carries no completed identity.
			if completedResponseID != "" {
				serverErrCh <- fmt.Errorf("completed response id = %q, want empty after failure", completedResponseID)
				return
			}
		}
		serverErrCh <- nil
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, errDial := websocket.DefaultDialer.Dial(wsURL, nil)
	if errDial != nil {
		t.Fatalf("dial websocket: %v", errDial)
	}
	closed := false
	defer func() {
		if !closed {
			if errClose := conn.Close(); errClose != nil {
				t.Errorf("close websocket: %v", errClose)
			}
		}
	}()
	if errDeadline := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); errDeadline != nil {
		t.Fatal(errDeadline)
	}

	_, payload, errRead := conn.ReadMessage()
	if errRead != nil {
		t.Fatalf("read terminal message: %v", errRead)
	}
	if got := gjson.GetBytes(payload, "type").String(); got != terminalType {
		t.Fatalf("payload type = %s, want %s", got, terminalType)
	}

	// The terminal event was already written, so no local interrupt may be taken.
	if local.deliver([]byte(`{"type":"response.interrupt","response_id":"unknown"}`)) {
		t.Fatal("local interrupt accepted after terminal event was written downstream")
	}

	// The turn is still alive; closing the stream ends it.
	closeStream()
	if errClose := conn.Close(); errClose != nil {
		t.Fatalf("close websocket: %v", errClose)
	}
	closed = true
	select {
	case errServer := <-serverErrCh:
		if errServer != nil {
			t.Fatalf("server error: %v", errServer)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("forward loop did not finish after client close")
	}
}
