package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	core "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// A duplex turn reuses the bound credential without selection, so a later
// ultrafast create on an ineligible account must replay instead of being sent.
func TestCodexDuplexUltrafastTurnOnIneligibleCredentialRequiresReplay(t *testing.T) {
	var creates atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		for {
			_, message, errRead := conn.ReadMessage()
			if errRead != nil {
				return
			}
			if creates.Add(1) > 1 {
				t.Errorf("ineligible credential received a later create: %s", gjson.GetBytes(message, "service_tier").String())
			}
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"resp-1","output":[]}}`))
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"resp-1","output":[]}}`))
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	input := make(chan core.WebsocketInput, 2)
	ctx = core.WithWebsocketInput(core.WithDownstreamWebsocket(ctx), input)

	exec := NewCodexWebsocketsExecutor(&config.Config{Codex: config.CodexConfig{ResponseSteering: true}})
	exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
	auth := &cliproxyauth.Auth{ID: "duplex-pro", Provider: "codex",
		Attributes: map[string]string{"base_url": server.URL, "websockets": "true", "plan_type": "pro"},
		Metadata:   map[string]any{"access_token": "pro-token"}}
	req := core.Request{Model: "gpt-6.1-sol", Payload: []byte(`{"model":"gpt-6.1-sol","service_tier":"priority","input":[{"role":"user","content":"turn 1"}]}`)}
	opts := core.Options{SourceFormat: sdktranslator.FromString("codex"), Metadata: map[string]any{core.ExecutionSessionMetadataKey: "duplex-tier"}}

	result, err := exec.ExecuteStream(ctx, auth, req, opts)
	if err != nil {
		t.Fatal(err)
	}
	var replayed bool
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			replayed = core.IsUpstreamWebsocketReplayRequired(chunk.Err)
			break
		}
		if gjson.GetBytes(chunk.Payload, "type").String() == "response.completed" {
			input <- core.WebsocketInput{Payload: []byte(`{"type":"response.create","model":"gpt-6.1-sol","service_tier":"ultrafast","input":[{"role":"user","content":"turn 2"}]}`)}
		}
	}
	cancel()
	if !replayed {
		t.Fatal("ultrafast turn on a pro credential did not require replay")
	}
	if got := creates.Load(); got != 1 {
		t.Fatalf("upstream creates = %d, want 1", got)
	}
}
