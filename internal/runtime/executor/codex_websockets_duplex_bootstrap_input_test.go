package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
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

func TestCodexDuplexBootstrapPreservesFollowup(t *testing.T) {
	for _, kind := range []string{"response.steer", "response.create"} {
		t.Run(kind, func(t *testing.T) {
			var rejected, succeeded, rejectedFollowups, healthyFollowups atomic.Int32
			rejectedDone, healthyDone := make(chan struct{}), make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				bad := r.Header.Get("Authorization") == "Bearer bad-key"
				if bad {
					defer close(rejectedDone)
				} else {
					defer close(healthyDone)
				}
				c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer func() { _ = c.Close() }()
				_ = c.SetReadDeadline(time.Now().Add(8 * time.Second))
				if _, _, err = c.ReadMessage(); err != nil {
					t.Error(err)
					return
				}
				if bad {
					rejected.Add(1)
					if err = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"error","status":401,"error":{"type":"authentication_error","message":"expired credential"}}`)); err != nil {
						t.Error(err)
						return
					}
					// Keep this socket open; bootstrap cleanup must close it without forwarding input.
					for {
						if _, _, err = c.ReadMessage(); err != nil {
							return
						}
						rejectedFollowups.Add(1)
					}
				}
				succeeded.Add(1)
				if err = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"healthy","output":[]}}`)); err != nil {
					t.Error(err)
					return
				}
				_, payload, err := c.ReadMessage()
				if err != nil {
					t.Errorf("healthy account did not receive queued follow-up: %v", err)
					return
				}
				healthyFollowups.Add(1)
				if gjson.GetBytes(payload, "type").String() != kind || gjson.GetBytes(payload, "input.0.content.0.text").String() != "PRESERVE_ME" {
					t.Errorf("follow-up changed: %s", payload)
				}
				if err = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"healthy","output":[]}}`)); err != nil {
					t.Error(err)
					return
				}
				_, _, _ = c.ReadMessage()
			}))
			defer upstream.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			input := make(chan core.WebsocketInput, 1)
			input <- core.WebsocketInput{Payload: []byte(fmt.Sprintf(`{"type":%q,"input":[{"role":"user","content":[{"type":"input_text","text":"PRESERVE_ME"}]}]}`, kind))}
			ctx = core.WithWebsocketInput(core.WithDownstreamWebsocket(ctx), input)
			cfg := &config.Config{}
			cfg.Codex.ResponseSteering = true
			cfg.CodexResponseSteering = true
			executor := NewCodexWebsocketsExecutor(cfg)
			executor.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
			manager := auth.NewManager(nil, &auth.FillFirstSelector{}, nil)
			manager.SetConfig(cfg)
			manager.SetRetryConfig(3, 30*time.Second, 0)
			manager.RegisterExecutor(executor)
			model := "duplex-bootstrap-followup-model"
			for i, key := range []string{"bad-key", "good-key"} {
				candidate := &auth.Auth{ID: fmt.Sprintf("%s-%d", t.Name(), i), Provider: "codex", Status: auth.StatusActive, Attributes: map[string]string{
					"api_key": key, "base_url": upstream.URL, "websockets": "true", "priority": fmt.Sprint(4 - i),
				}}
				registry.GetGlobalRegistry().RegisterClient(candidate.ID, "codex", []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(candidate.ID) })
				if _, err := manager.Register(ctx, candidate); err != nil {
					t.Fatal(err)
				}
			}
			result, err := manager.ExecuteStream(ctx, []string{"codex"}, core.Request{Model: model, Payload: []byte(fmt.Sprintf(`{"model":%q,"input":[]}`, model))}, core.Options{
				SourceFormat: translator.FromString("codex"), Metadata: map[string]any{core.ExecutionSessionMetadataKey: t.Name()},
			})
			if err != nil {
				t.Fatal(err)
			}
			completed := false
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					t.Fatal(chunk.Err)
				}
				if gjson.GetBytes(chunk.Payload, "type").String() == "response.completed" {
					completed = true
					cancel()
				}
			}
			if !completed {
				t.Error("healthy account never completed the follow-up")
			}
			for _, done := range []chan struct{}{rejectedDone, healthyDone} {
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Fatal("bootstrap socket cleanup stalled")
				}
			}
			if rejected.Load() != 1 || succeeded.Load() != 1 || rejectedFollowups.Load() != 0 || healthyFollowups.Load() != 1 {
				t.Fatalf("attempts rejected=%d healthy=%d; follow-ups rejected=%d healthy=%d", rejected.Load(), succeeded.Load(), rejectedFollowups.Load(), healthyFollowups.Load())
			}
		})
	}
}

func TestCodexDuplexBootstrapCancellationPreservesInput(t *testing.T) {
	initialRead, closed := make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(closed)
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = c.Close() }()
		_ = c.SetReadDeadline(time.Now().Add(8 * time.Second))
		if _, _, err = c.ReadMessage(); err != nil {
			t.Error(err)
			return
		}
		close(initialRead)
		// No response.created: cancellation must release the blocked writer.
		_, _, _ = c.ReadMessage()
	}))
	defer upstream.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	input := make(chan core.WebsocketInput, 1)
	input <- core.WebsocketInput{Payload: []byte(`{"type":"response.steer","input":[]}`)}
	ctx = core.WithWebsocketInput(core.WithDownstreamWebsocket(ctx), input)
	cfg := &config.Config{}
	cfg.Codex.ResponseSteering = true
	cfg.CodexResponseSteering = true
	executor := NewCodexWebsocketsExecutor(cfg)
	executor.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
	candidate := &auth.Auth{ID: t.Name(), Provider: "codex", Status: auth.StatusActive, Attributes: map[string]string{"api_key": "test-key", "base_url": upstream.URL, "websockets": "true"}}
	result, err := executor.ExecuteStream(ctx, candidate, core.Request{Model: "bootstrap-cancel-model", Payload: []byte(`{"model":"bootstrap-cancel-model","input":[]}`)}, core.Options{
		SourceFormat: translator.FromString("codex"), Metadata: map[string]any{core.ExecutionSessionMetadataKey: t.Name()},
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-initialRead:
	case <-ctx.Done():
		t.Fatal("initial request not sent")
	}
	cancel()
	select {
	case _, open := <-result.Chunks:
		if open {
			t.Fatal("cancelled bootstrap emitted a chunk")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled bootstrap writer did not exit")
	}
	if len(input) != 1 {
		t.Fatal("follow-up consumed before response.created")
	}
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled upstream remained open")
	}
}
