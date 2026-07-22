package openai

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
)

func TestForwardResponsesWebsocketInterceptsMissingToolOutputAfterProvisionalEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)

	serverErrCh := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := responsesWebsocketUpgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			serverErrCh <- errUpgrade
			return
		}
		defer func() { _ = conn.Close() }()

		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = r
		data := make(chan []byte, 1)
		data <- []byte("{\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\"}}\n" +
			"{\"type\":\"error\",\"status\":400,\"error\":{\"message\":\"No tool output found for function call call-1.\"}}")
		close(data)
		errCh := make(chan *interfaces.ErrorMessage)

		intercepted := false
		_, _, _, errMsg, errForward := (*OpenAIResponsesAPIHandler)(nil).forwardResponsesWebsocket(
			ctx,
			newResponsesWebsocketWriter(conn),
			func(...interface{}) {},
			data,
			errCh,
			newInMemoryWebsocketTimelineLog(),
			"tool-session",
			"session-1",
			func(*interfaces.ErrorMessage) bool {
				intercepted = true
				return true
			},
		)
		if errForward != nil {
			serverErrCh <- errForward
			return
		}
		if !intercepted {
			serverErrCh <- errors.New("missing tool output was not intercepted after provisional event")
			return
		}
		if errMsg == nil || errMsg.Error == nil {
			serverErrCh <- errors.New("expected intercepted missing tool output error")
			return
		}
		serverErrCh <- nil
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, errDial := websocket.DefaultDialer.Dial(wsURL, nil)
	if errDial != nil {
		t.Fatalf("dial websocket: %v", errDial)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, errRead := conn.ReadMessage(); errRead == nil {
		t.Fatal("provisional response must not be sent before replayable error interception")
	}
	if errServer := <-serverErrCh; errServer != nil {
		t.Fatalf("server error: %v", errServer)
	}
}

func TestForwardResponsesWebsocketInterceptsErrorChannelAfterProvisionalEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)

	serverErrCh := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := responsesWebsocketUpgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			serverErrCh <- errUpgrade
			return
		}
		defer func() { _ = conn.Close() }()

		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = r
		data := make(chan []byte, 1)
		data <- []byte(`{"type":"response.created","response":{"id":"resp-1"}}`)
		errCh := make(chan *interfaces.ErrorMessage)
		go func() {
			time.Sleep(10 * time.Millisecond)
			errCh <- &interfaces.ErrorMessage{
				StatusCode: http.StatusBadRequest,
				Error:      errors.New("No tool output found for function call call-1."),
			}
		}()

		intercepted := false
		_, _, _, errMsg, errForward := (*OpenAIResponsesAPIHandler)(nil).forwardResponsesWebsocket(
			ctx,
			newResponsesWebsocketWriter(conn),
			func(...interface{}) {},
			data,
			errCh,
			newInMemoryWebsocketTimelineLog(),
			"tool-session",
			"session-1",
			func(*interfaces.ErrorMessage) bool {
				intercepted = true
				return true
			},
		)
		if errForward != nil {
			serverErrCh <- errForward
			return
		}
		if !intercepted || errMsg == nil || errMsg.Error == nil {
			serverErrCh <- errors.New("error channel was not intercepted after provisional event")
			return
		}
		serverErrCh <- nil
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, errDial := websocket.DefaultDialer.Dial(wsURL, nil)
	if errDial != nil {
		t.Fatalf("dial websocket: %v", errDial)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, errRead := conn.ReadMessage(); errRead == nil {
		t.Fatal("provisional response must not be sent before error-channel interception")
	}
	if errServer := <-serverErrCh; errServer != nil {
		t.Fatalf("server error: %v", errServer)
	}
}

func TestForwardResponsesWebsocketInterceptsMissingToolOutputAfterBufferedMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)

	serverErrCh := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := responsesWebsocketUpgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			serverErrCh <- errUpgrade
			return
		}
		defer func() { _ = conn.Close() }()

		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = r
		data := make(chan []byte, 1)
		data <- []byte("{\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\"}}\n" +
			"{\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"custom_tool_call\",\"call_id\":\"call-1\"}}\n" +
			"{\"type\":\"error\",\"status\":400,\"error\":{\"message\":\"No tool output found for custom tool call call-1.\"}}")
		close(data)
		errCh := make(chan *interfaces.ErrorMessage)
		intercepted := false
		_, _, _, errMsg, errForward := (*OpenAIResponsesAPIHandler)(nil).forwardResponsesWebsocket(
			ctx,
			newResponsesWebsocketWriter(conn),
			func(...interface{}) {},
			data,
			errCh,
			newInMemoryWebsocketTimelineLog(),
			"tool-session",
			"session-1",
			func(*interfaces.ErrorMessage) bool {
				intercepted = true
				return true
			},
		)
		if errForward != nil {
			serverErrCh <- errForward
			return
		}
		if !intercepted || errMsg == nil || errMsg.Error == nil {
			serverErrCh <- errors.New("missing tool output was not intercepted after metadata")
			return
		}
		serverErrCh <- nil
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, errDial := websocket.DefaultDialer.Dial(wsURL, nil)
	if errDial != nil {
		t.Fatalf("dial websocket: %v", errDial)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, errRead := conn.ReadMessage(); errRead == nil {
		t.Fatal("buffered metadata must not be sent before replayable error interception")
	}
	if errServer := <-serverErrCh; errServer != nil {
		t.Fatalf("server error: %v", errServer)
	}
}

func TestForwardResponsesWebsocketDoesNotInterceptErrorAfterMeaningfulDelta(t *testing.T) {
	gin.SetMode(gin.TestMode)

	serverErrCh := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := responsesWebsocketUpgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			serverErrCh <- errUpgrade
			return
		}
		defer func() { _ = conn.Close() }()

		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = r
		data := make(chan []byte, 1)
		data <- []byte("{\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\"}}\n" +
			"{\"type\":\"response.output_text.delta\",\"delta\":\"visible\"}\n" +
			"{\"type\":\"error\",\"status\":400,\"error\":{\"message\":\"No tool output found for function call call-1.\"}}")
		close(data)
		errCh := make(chan *interfaces.ErrorMessage)
		intercepted := false
		_, _, _, errMsg, errForward := (*OpenAIResponsesAPIHandler)(nil).forwardResponsesWebsocket(
			ctx,
			newResponsesWebsocketWriter(conn),
			func(...interface{}) {},
			data,
			errCh,
			newInMemoryWebsocketTimelineLog(),
			"tool-session",
			"session-1",
			func(*interfaces.ErrorMessage) bool {
				intercepted = true
				return true
			},
		)
		if errForward != nil {
			serverErrCh <- errForward
			return
		}
		if intercepted {
			serverErrCh <- errors.New("error was intercepted after meaningful output")
			return
		}
		if errMsg == nil || errMsg.Error == nil {
			serverErrCh <- errors.New("expected upstream error after meaningful output")
			return
		}
		serverErrCh <- nil
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, errDial := websocket.DefaultDialer.Dial(wsURL, nil)
	if errDial != nil {
		t.Fatalf("dial websocket: %v", errDial)
	}
	defer func() { _ = conn.Close() }()
	wantEvents := []string{"response.created", "response.output_text.delta", wsEventTypeError}
	for i := range wantEvents {
		_, payload, errRead := conn.ReadMessage()
		if errRead != nil {
			t.Fatalf("read event %d: %v", i+1, errRead)
		}
		if got := websocketPayloadEventType(payload); got != wantEvents[i] {
			t.Fatalf("event %d = %s, want %s: %s", i+1, got, wantEvents[i], payload)
		}
	}
	if errServer := <-serverErrCh; errServer != nil {
		t.Fatalf("server error: %v", errServer)
	}
}

func TestForwardResponsesWebsocketFlushesProvisionalEventBeforeNonRetryableError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	serverErrCh := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := responsesWebsocketUpgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			serverErrCh <- errUpgrade
			return
		}
		defer func() { _ = conn.Close() }()

		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = r
		data := make(chan []byte, 1)
		data <- []byte("{\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\"}}\n" +
			"{\"type\":\"error\",\"status\":429,\"error\":{\"message\":\"rate limited\"}}")
		close(data)
		errCh := make(chan *interfaces.ErrorMessage)

		_, _, _, errMsg, errForward := (*OpenAIResponsesAPIHandler)(nil).forwardResponsesWebsocket(
			ctx,
			newResponsesWebsocketWriter(conn),
			func(...interface{}) {},
			data,
			errCh,
			newInMemoryWebsocketTimelineLog(),
			"tool-session",
			"session-1",
			nil,
		)
		if errForward != nil {
			serverErrCh <- errForward
			return
		}
		if errMsg == nil || errMsg.StatusCode != http.StatusTooManyRequests {
			serverErrCh <- errors.New("expected non-retryable rate limit error")
			return
		}
		serverErrCh <- nil
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, errDial := websocket.DefaultDialer.Dial(wsURL, nil)
	if errDial != nil {
		t.Fatalf("dial websocket: %v", errDial)
	}
	defer func() { _ = conn.Close() }()

	_, createdPayload, errRead := conn.ReadMessage()
	if errRead != nil {
		t.Fatalf("read response.created: %v", errRead)
	}
	if got := websocketPayloadEventType(createdPayload); got != "response.created" {
		t.Fatalf("first event = %s, want response.created", got)
	}
	_, errorPayload, errRead := conn.ReadMessage()
	if errRead != nil {
		t.Fatalf("read error: %v", errRead)
	}
	if got := websocketPayloadEventType(errorPayload); got != wsEventTypeError {
		t.Fatalf("second event = %s, want %s", got, wsEventTypeError)
	}
	if errServer := <-serverErrCh; errServer != nil {
		t.Fatalf("server error: %v", errServer)
	}
}
