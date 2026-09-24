package executor

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	auth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	core "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	translator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexDuplexConnectionTimeoutDoesNotCoolHealthyAccount(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = c.Close() }()
		if _, _, err = c.ReadMessage(); err != nil {
			t.Error(err)
			return
		}
		for _, payload := range []string{`{"type":"response.created","response":{"id":"r1"}}`, `{"type":"response.completed","response":{"id":"r1","output":[]}}`} {
			if err = c.WriteMessage(websocket.TextMessage, []byte(payload)); err != nil {
				t.Error(err)
				return
			}
		}
		_, _, _ = c.ReadMessage()
	}))
	defer upstream.Close()
	cfg := &config.Config{}
	cfg.Codex.ResponseSteering = true
	cfg.CodexResponseSteering = true
	manager := auth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.RegisterExecutor(NewCodexAutoExecutor(cfg))
	id, model := "duplex-health-account", "duplex-health-model"
	_, err := manager.Register(context.Background(), &auth.Auth{ID: id, Provider: "codex", Status: auth.StatusActive, Attributes: map[string]string{"api_key": "test-key", "base_url": upstream.URL, "websockets": "true"}})
	if err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: model}})
	defer registry.GetGlobalRegistry().UnregisterClient(id)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	input := make(chan core.WebsocketInput, 1)
	ctx = core.WithWebsocketInput(core.WithDownstreamWebsocket(ctx), input)
	stream, err := manager.ExecuteStream(ctx, []string{"codex"}, core.Request{Model: model, Payload: []byte(`{"model":"duplex-health-model","input":[]}`)}, core.Options{SourceFormat: translator.FromString("codex"), Metadata: map[string]any{core.ExecutionSessionMetadataKey: "duplex-health-session"}})
	if err != nil {
		t.Fatal(err)
	}
	sawCompleted, sawTimeout := false, false
	for chunk := range stream.Chunks {
		if gjson.GetBytes(chunk.Payload, "type").String() == "response.completed" {
			sawCompleted = true
			// Inject a real net.Error through the input seam after a successful response.
			// No wall-clock sleep or production network deadline is needed.
			input <- core.WebsocketInput{Err: &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}}
		}
		if chunk.Err != nil {
			sawTimeout = errors.Is(chunk.Err, os.ErrDeadlineExceeded)
		}
	}
	if !sawCompleted || !sawTimeout {
		t.Fatalf("completed=%v timeout=%v", sawCompleted, sawTimeout)
	}
	current, ok := manager.GetByID(id)
	if !ok {
		t.Fatal("account disappeared")
	}
	if current.Unavailable {
		t.Fatal("connection timeout cooled the account")
	}
	if state := current.ModelStates[model]; state != nil && state.Unavailable {
		t.Fatalf("connection timeout cooled the model: %+v", state.LastError)
	}
}
