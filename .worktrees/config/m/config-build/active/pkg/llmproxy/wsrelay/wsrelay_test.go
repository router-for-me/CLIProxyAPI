package wsrelay

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestManager_Handler(t *testing.T) {
	mgr := NewManager(Options{})
	ts := httptest.NewServer(mgr.Handler())
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + mgr.Path()
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if mgr.Path() != "/v1/ws" {
		t.Errorf("got path %q, want /v1/ws", mgr.Path())
	}
}

func TestManager_Stop(t *testing.T) {
	mgr := NewManager(Options{})
	ts := httptest.NewServer(mgr.Handler())
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + mgr.Path()
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer func() { _ = conn.Close() }()

	err = mgr.Stop(context.Background())
	if err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
}
