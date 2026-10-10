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
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

// identityHTTPExecutor never creates an upstream websocket. Its second response
// waits for cancellation so a stale interrupt cannot hide behind normal completion.
type identityHTTPExecutor struct {
	calls    atomic.Int32
	canceled chan string
}

func (*identityHTTPExecutor) Identifier() string { return "codex" }

func (*identityHTTPExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *identityHTTPExecutor) ExecuteStream(ctx context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	call := e.calls.Add(1)
	responseID := fmt.Sprintf("http-response-%d", call)
	chunks := make(chan coreexecutor.StreamChunk, 2)
	go func() {
		defer close(chunks)
		chunks <- coreexecutor.StreamChunk{Payload: []byte(fmt.Sprintf(`{"type":"response.created","response":{"id":%q}}`, responseID))}
		if call == 2 {
			<-ctx.Done()
			e.canceled <- responseID
			return
		}
		chunks <- coreexecutor.StreamChunk{Payload: []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":%q,"status":"completed","output":[]}}`, responseID))}
	}()
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (*identityHTTPExecutor) Refresh(context.Context, *coreauth.Auth) (*coreauth.Auth, error) {
	return nil, errors.New("not implemented")
}

func (*identityHTTPExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (*identityHTTPExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (*identityHTTPExecutor) InterruptExecutionSession(context.Context, string, []byte) error {
	return coreexecutor.ErrNoActiveUpstreamWebsocket
}

func TestResponsesHTTPInterruptResponseIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, scenario := range []string{"late_idle", "stale_active", "unknown_active"} {
		t.Run(scenario, func(t *testing.T) {
			executor := &identityHTTPExecutor{canceled: make(chan string, 1)}
			cfg := &config.Config{}
			manager := coreauth.NewManager(nil, nil, nil)
			manager.SetConfig(cfg)
			manager.RegisterExecutor(executor)
			authID := "http-identity-" + scenario
			model := "model-" + authID
			if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
				ID: authID, Provider: "codex", Status: coreauth.StatusActive,
				Attributes: map[string]string{"api_key": "synthetic-test-key"},
			}); errRegister != nil {
				t.Fatal(errRegister)
			}
			registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
			defer registry.GetGlobalRegistry().UnregisterClient(authID)
			handler := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
			router := gin.New()
			router.GET("/v1/responses", handler.ResponsesWebsocket)
			server := httptest.NewServer(router)
			defer server.Close()
			client, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
			if errDial != nil {
				t.Fatal(errDial)
			}
			defer func() {
				if errClose := client.Close(); errClose != nil {
					t.Errorf("close client: %v", errClose)
				}
			}()
			if errDeadline := client.SetReadDeadline(time.Now().Add(10 * time.Second)); errDeadline != nil {
				t.Fatal(errDeadline)
			}
			send := func(payload string) {
				t.Helper()
				if errWrite := client.WriteMessage(websocket.TextMessage, []byte(payload)); errWrite != nil {
					t.Fatal(errWrite)
				}
			}
			expect := func(eventType, responseID string) []byte {
				t.Helper()
				_, payload, errRead := client.ReadMessage()
				if errRead != nil {
					t.Fatal(errRead)
				}
				if gjson.GetBytes(payload, "type").String() != eventType || gjson.GetBytes(payload, "response.id").String() != responseID {
					t.Fatalf("want %s for %q, got %s", eventType, responseID, payload)
				}
				return payload
			}
			create := func(previousID string) {
				send(fmt.Sprintf(`{"type":"response.create","model":%q,"previous_response_id":%q,"input":[]}`, model, previousID))
			}
			interrupt := func(responseID string) {
				send(fmt.Sprintf(`{"type":"response.interrupt","response_id":%q,"mode":"discard_partial_items"}`, responseID))
			}
			rejectUnknown := func() {
				t.Helper()
				interrupt("unknown-response")
				payload := expect("error", "")
				if gjson.GetBytes(payload, "status").Int() != http.StatusBadRequest {
					t.Fatalf("unknown interrupt must return 400, got %s", payload)
				}
			}

			create("")
			expect("response.created", "http-response-1")
			expect("response.completed", "http-response-1")
			if scenario == "late_idle" {
				interrupt("http-response-1")
				interrupt("http-response-1")
			}
			create("http-response-1")
			expect("response.created", "http-response-2")
			if scenario == "stale_active" {
				interrupt("http-response-1")
				interrupt("http-response-1")
			}
			// The unknown frame is a reader-order barrier: both preceding stale
			// frames must be handled before this error can reach the client.
			rejectUnknown()
			select {
			case responseID := <-executor.canceled:
				t.Fatalf("stale or unknown interrupt canceled %s", responseID)
			default:
			}
			interrupt("http-response-2")
			payload := expect("response.incomplete", "http-response-2")
			if gjson.GetBytes(payload, "response.incomplete_details.reason").String() != "interrupted" {
				t.Fatalf("unexpected interrupt reason: %s", payload)
			}
			select {
			case responseID := <-executor.canceled:
				if responseID != "http-response-2" {
					t.Fatalf("canceled %s, want current response", responseID)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("current HTTP request was not canceled")
			}
			interrupt("http-response-2")
			interrupt("http-response-2")
			create("http-response-2")
			expect("response.created", "http-response-3")
			expect("response.completed", "http-response-3")
			if executor.calls.Load() != 3 {
				t.Fatalf("dispatch count = %d, want 3", executor.calls.Load())
			}
		})
	}
}
