package executor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	auth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	core "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	translator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestCodexWebsocketsBridgeDuplexGuardRejectsBeforeDialAndUnlocks(t *testing.T) {
	var upstreamRequests atomic.Int32
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamRequests.Add(1)
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		_ = conn.Close()
	}))
	defer upstream.Close()

	cfg := &config.Config{}
	cfg.Codex.ResponseSteering = true
	cfg.CodexResponseSteering = true
	exec := NewCodexWebsocketsExecutor(cfg)
	sessionID := t.Name()
	exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}

	ctx := core.WithWireContract(context.Background(), &core.WireContract{
		SearchAliases: []string{"tool_search"},
	})
	ctx = core.WithDownstreamWebsocket(ctx)
	ctx = core.WithWebsocketInput(ctx, make(chan core.WebsocketInput))

	request := core.Request{
		Model:   "gpt-5.6",
		Payload: []byte(`{"model":"gpt-5.6","input":[],"tools":[{"type":"function","name":"tool_search","parameters":{"type":"object"}}]}`),
	}
	opts := core.Options{
		SourceFormat: translator.FromString("codex"),
		Metadata: map[string]any{
			core.ExecutionSessionMetadataKey: sessionID,
		},
	}
	credential := &auth.Auth{
		ID:       "responses-tools-websocket-guard",
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":    "test-key",
			"base_url":   upstream.URL,
			"websockets": "true",
		},
	}

	_, err := exec.ExecuteStream(ctx, credential, request, opts)
	if err == nil {
		t.Fatal("bridged request with raw duplex input must be rejected")
	}
	var status interface{ StatusCode() int }
	if !errors.As(err, &status) || status.StatusCode() != http.StatusUnprocessableEntity {
		t.Errorf("error = %v, want request-scoped 422", err)
	}
	var requestScoped interface{ IsRequestScoped() bool }
	if !errors.As(err, &requestScoped) || !requestScoped.IsRequestScoped() {
		t.Errorf("error = %v, want a request-scoped bridge error", err)
	}
	var availabilityNeutral interface{ AvailabilityNeutral() bool }
	if !errors.As(err, &availabilityNeutral) || !availabilityNeutral.AvailabilityNeutral() {
		t.Errorf("error = %v, want an availability-neutral bridge error", err)
	}
	if got := upstreamRequests.Load(); got != 0 {
		t.Errorf("upstream requests = %d, want no dial before rejection", got)
	}

	session := exec.getOrCreateSession(sessionID)
	if session == nil || !session.reqMu.TryLock() {
		t.Fatal("rejected request left its websocket session locked")
	}
	session.reqMu.Unlock()
}

func TestCodexWebsocketsFinalToolGuardRejectsBeforeDialAndUnlocks(t *testing.T) {
	var upstreamRequests atomic.Int32
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamRequests.Add(1)
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		if err := conn.WriteJSON(map[string]any{
			"type": "response.completed",
			"response": map[string]any{
				"id":     "resp_test",
				"status": "completed",
				"output": []any{},
			},
		}); err != nil {
			t.Errorf("write terminal response: %v", err)
		}
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
		_ = conn.Close()
	}))
	defer upstream.Close()

	for _, executeStream := range []bool{false, true} {
		name := "execute"
		if executeStream {
			name = "execute-stream"
		}
		t.Run(name, func(t *testing.T) {
			exec := NewCodexWebsocketsExecutor(&config.Config{})
			sessionID := t.Name()
			exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
			ctx := core.WithWireContract(context.Background(), &core.WireContract{
				RequiredFunctionNames: []string{"must_survive"},
			})
			ctx = core.WithDownstreamWebsocket(ctx)
			request := core.Request{
				Model:   "gpt-5.6",
				Payload: []byte(`{"model":"gpt-5.6","input":[]}`),
			}
			opts := core.Options{
				SourceFormat: translator.FromString("codex"),
				Metadata: map[string]any{
					core.ExecutionSessionMetadataKey: sessionID,
				},
			}
			credential := &auth.Auth{
				ID:       "responses-tools-websocket-final-guard",
				Provider: "codex",
				Attributes: map[string]string{
					"api_key":    "test-key",
					"base_url":   upstream.URL,
					"websockets": "true",
				},
			}

			var err error
			if executeStream {
				_, err = exec.ExecuteStream(ctx, credential, request, opts)
			} else {
				_, err = exec.Execute(ctx, credential, request, opts)
			}
			if err == nil {
				t.Fatal("request that lost a required tool declaration must be rejected")
			}
			var status interface{ StatusCode() int }
			if !errors.As(err, &status) || status.StatusCode() != http.StatusUnprocessableEntity {
				t.Errorf("error = %v, want request-scoped 422", err)
			}
			if got := upstreamRequests.Load(); got != 0 {
				t.Errorf("upstream requests = %d, want no dial before rejection", got)
			}

			session := exec.getOrCreateSession(sessionID)
			if session == nil || !session.reqMu.TryLock() {
				t.Fatal("rejected request left its websocket session locked")
			}
			session.reqMu.Unlock()
		})
	}
}
