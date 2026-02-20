package api

import (
	"errors"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestResponsesWebSocketHandler_Proxying(t *testing.T) {
	// Start a mock server that handles the POST /v1/responses
	mockPostServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Logf("mockPostServer received %s %s", r.Method, r.URL.Path)
		if r.Method == http.MethodGet {
			t.Log("mockPostServer upgrading to WebSocket")
			ResponsesWebSocketHandler().ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		t.Log("mockPostServer handling POST request")
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: hello\n\n"))
		_, _ = w.Write([]byte("data: world\n\n"))
	}))
	defer mockPostServer.Close()

	wsURL := "ws" + strings.TrimPrefix(mockPostServer.URL, "http") + "/v1/responses"
	t.Logf("Connecting to %s", wsURL)

	header := http.Header{}
	header.Add("Authorization", "Bearer test-key")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer func() { _ = conn.Close() }()
	t.Log("Connected to WebSocket")

	// Send a request via WS
	err = conn.WriteMessage(websocket.TextMessage, []byte(`{"model":"test"}`))
	if err != nil {
		t.Fatalf("failed to write: %v", err)
	}
	t.Log("Sent message via WebSocket")

	// Read responses
	var fullMessage strings.Builder
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan bool)
	go func() {
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				done <- true
				return
			}
			fullMessage.Write(msg)
			if strings.Contains(fullMessage.String(), "world") {
				done <- true
				return
			}
		}
	}()

	select {
	case <-ctx.Done():
		t.Errorf("timeout waiting for messages, got: %s", fullMessage.String())
	case <-done:
	}

	if fullMessage.String() != "data: hello\n\ndata: world\n\n" {
		t.Errorf("unexpected combined message: %q", fullMessage.String())
	}
}

func TestResponsesWebSocketHandler_NotWebSocket(t *testing.T) {
	handler := ResponsesWebSocketHandler()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("got status %d, want 400", w.Code)
	}
}

func TestResponsesWebSocketHandler_MethodNotAllowed(t *testing.T) {
	handler := ResponsesWebSocketHandler()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("got status %d, want 405", w.Code)
	}
}

func TestWriteWSError(t *testing.T) {
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade failed: %v", err)
		}
		defer func() { _ = conn.Close() }()

		writeWSError(conn, errors.New("boom"))
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("failed to dial websocket: %v", err)
	}
	defer func() { _ = conn.Close() }()

	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("failed to read message: %v", err)
	}

	got := string(msg)
	if got != `{"error":"boom"}` {
		t.Fatalf("got %q, want %q", got, `{"error":"boom"}`)
	}
}
