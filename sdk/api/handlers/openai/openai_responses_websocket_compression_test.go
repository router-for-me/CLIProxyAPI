package openai

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	log "github.com/sirupsen/logrus"
)

func TestResponsesWebsocketCompressionRoundTrip(t *testing.T) {
	upgrader := responsesWebsocketUpgrader
	upgrader.EnableCompression = true
	serverError := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			serverError <- errUpgrade
			return
		}
		defer func() {
			if errClose := conn.Close(); errClose != nil {
				log.Errorf("close compression echo server websocket: %v", errClose)
			}
		}()
		conn.EnableWriteCompression(true)
		for _, wantType := range []int{websocket.TextMessage, websocket.BinaryMessage} {
			messageType, payload, errReadMessage := conn.ReadMessage()
			if errReadMessage != nil {
				serverError <- errReadMessage
				return
			}
			if messageType != wantType {
				serverError <- fmt.Errorf("message type=%d, want %d", messageType, wantType)
				return
			}
			if errWriteMessage := conn.WriteMessage(messageType, payload); errWriteMessage != nil {
				serverError <- errWriteMessage
				return
			}
		}
		serverError <- nil
	}))
	defer server.Close()
	dialer := websocket.Dialer{EnableCompression: true}
	conn, response, errDial := dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if errDial != nil {
		t.Fatal(errDial)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			log.Errorf("close compression echo client websocket: %v", errClose)
		}
	}()
	if !strings.Contains(response.Header.Get("Sec-WebSocket-Extensions"), "permessage-deflate") {
		t.Fatal("compression was not negotiated")
	}
	conn.EnableWriteCompression(true)
	for _, messageType := range []int{websocket.TextMessage, websocket.BinaryMessage} {
		payload := bytes.Repeat([]byte("compressed websocket payload "), 4096)
		if messageType == websocket.BinaryMessage {
			payload = bytes.Repeat([]byte{0x00, 0xff, 0x28, 0xb5, 0x2f, 0xfd}, 16384)
		}
		if errWriteMessage := conn.WriteMessage(messageType, payload); errWriteMessage != nil {
			t.Fatal(errWriteMessage)
		}
		gotType, got, errReadMessage := conn.ReadMessage()
		if errReadMessage != nil {
			t.Fatal(errReadMessage)
		}
		if gotType != messageType || !bytes.Equal(got, payload) {
			t.Fatalf("compressed round trip mismatch: type=%d bytes=%d, want type=%d bytes=%d", gotType, len(got), messageType, len(payload))
		}
	}
	if errServer := <-serverError; errServer != nil {
		t.Fatal(errServer)
	}
}

func TestResponsesWebsocketCompressionToggle(t *testing.T) {
	previous := responsesWebsocketCompressionEnabled.Load()
	upgraderCompression := responsesWebsocketUpgrader.EnableCompression
	defer SetResponsesWebsocketCompressionEnabled(previous)
	for _, enabled := range []bool{true, false, true} {
		SetResponsesWebsocketCompressionEnabled(enabled)
		handler := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&config.SDKConfig{}, nil))
		engine := gin.New()
		done := make(chan struct{})
		engine.GET("/", func(c *gin.Context) { defer close(done); handler.ResponsesWebsocket(c) })
		server := httptest.NewServer(engine)
		dialer := websocket.Dialer{EnableCompression: true}
		conn, response, errDial := dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
		if errDial != nil {
			server.Close()
			t.Fatal(errDial)
		}
		negotiated := strings.Contains(response.Header.Get("Sec-WebSocket-Extensions"), "permessage-deflate")
		if errClose := conn.Close(); errClose != nil {
			log.Errorf("close downstream compression test websocket: %v", errClose)
		}
		<-done
		server.Close()
		if negotiated != enabled {
			t.Fatalf("compression negotiated = %v, want %v", negotiated, enabled)
		}
		if responsesWebsocketUpgrader.EnableCompression != upgraderCompression {
			t.Fatal("shared upgrader was mutated")
		}
	}
}
