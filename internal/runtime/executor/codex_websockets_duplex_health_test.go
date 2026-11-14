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

	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	auth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	core "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	translator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
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
		if ojson.GetBytes(chunk.Payload, "type").String() == "response.completed" {
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

func TestCodexDuplexSummaryCredentialFailuresThroughManager(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !websocket.IsWebSocketUpgrade(r) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(status)
					_, _ = w.Write([]byte(`{"error":{"message":"summary credential rejected"}}`))
					return
				}
				c, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if errUpgrade != nil {
					t.Error(errUpgrade)
					return
				}
				defer func() {
					if errClose := c.Close(); errClose != nil {
						t.Errorf("close upstream websocket: %v", errClose)
					}
				}()
				if _, _, errRead := c.ReadMessage(); errRead != nil {
					t.Error(errRead)
					return
				}
				for _, payload := range []string{`{"type":"response.created","response":{"id":"r1"}}`, `{"type":"response.completed","response":{"id":"r1","output":[]}}`} {
					if errWrite := c.WriteMessage(websocket.TextMessage, []byte(payload)); errWrite != nil {
						t.Error(errWrite)
						return
					}
				}
				_, _, _ = c.ReadMessage()
			}))
			defer upstream.Close()
			const id, model = "duplex-summary-health-account", "duplex-summary-health-model"
			cfg := &config.Config{CodexKey: []config.CodexKey{{APIKey: "test-key", BaseURL: upstream.URL, Models: []config.CodexModel{{Name: model, IsCompat: true}}}}}
			cfg.Codex.ResponseSteering = true
			cfg.CodexResponseSteering = true
			manager := auth.NewManager(nil, nil, nil)
			manager.SetConfig(cfg)
			exec := NewCodexAutoExecutor(cfg)
			manager.RegisterExecutor(exec)
			defer exec.CloseExecutionSession("summary-health-session")
			if _, errRegister := manager.Register(context.Background(), &auth.Auth{ID: id, Provider: "codex", Status: auth.StatusActive, Attributes: map[string]string{"api_key": "test-key", "base_url": upstream.URL, "websockets": "true"}}); errRegister != nil {
				t.Fatal(errRegister)
			}
			registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: model, IsCompat: true}})
			defer registry.GetGlobalRegistry().UnregisterClient(id)
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			input := make(chan core.WebsocketInput, 1)
			ctx = core.WithWebsocketInput(core.WithDownstreamWebsocket(ctx), input)
			stream, errStream := manager.ExecuteStream(ctx, []string{"codex"}, core.Request{Model: model, Payload: []byte(`{"model":"duplex-summary-health-model","input":[]}`)}, core.Options{SourceFormat: translator.FormatOpenAIResponse, Metadata: map[string]any{core.ExecutionSessionMetadataKey: "summary-health-session"}})
			if errStream != nil {
				t.Fatal(errStream)
			}
			var terminal error
			for chunk := range stream.Chunks {
				if ojson.GetBytes(chunk.Payload, "type").String() == "response.completed" {
					input <- core.WebsocketInput{Payload: []byte(`{"type":"response.create","input":[{"role":"user","content":"history"},{"type":"compaction_trigger"}]}`)}
				}
				if chunk.Err != nil {
					terminal = chunk.Err
				}
			}
			var statusHolder interface{ StatusCode() int }
			if !errors.As(terminal, &statusHolder) || statusHolder.StatusCode() != status {
				t.Fatalf("terminal error = %v, want HTTP %d", terminal, status)
			}
			var scoped interface{ IsRequestScoped() bool }
			if errors.As(terminal, &scoped) && scoped.IsRequestScoped() {
				t.Fatalf("credential failure incorrectly classified as request-scoped: %v", terminal)
			}
			current, ok := manager.GetByID(id)
			if !ok {
				t.Fatal("account disappeared")
			}
			state := current.ModelStates[model]
			if state == nil || state.LastError == nil || state.LastError.HTTPStatus != status || state.LastError.IsRequestScoped() || !state.Unavailable || state.NextRetryAfter.IsZero() {
				t.Fatalf("HTTP %d did not retain credential failure state: %+v", status, state)
			}
		})
	}
}
