package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/tidwall/gjson"
)

func TestResponsesWebsocketExhaustedPoolReturnsCooldown(t *testing.T) {
	gin.SetMode(gin.TestMode)
	modelName := "websocket-exhausted-pool-test-model"
	executor := &websocketCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	for _, authID := range []string{"websocket-cooldown-first", "websocket-cooldown-second"} {
		auth := &coreauth.Auth{
			ID:       authID,
			Provider: executor.Identifier(),
			Status:   coreauth.StatusActive,
			ModelStates: map[string]*coreauth.ModelState{
				modelName: {
					Unavailable:    true,
					NextRetryAfter: time.Now().Add(time.Hour),
					Quota:          coreauth.QuotaState{Exceeded: true},
				},
			},
		}
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register credential: %v", errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient(authID, auth.Provider, []*registry.ModelInfo{{ID: modelName}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	}
	handler := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))
	router := gin.New()
	done := make(chan struct{})
	router.GET("/v1/responses", func(ctx *gin.Context) {
		defer close(done)
		handler.ResponsesWebsocket(ctx)
	})
	server := httptest.NewServer(router)
	defer server.Close()
	connection, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
	if errDial != nil {
		t.Fatalf("dial websocket: %v", errDial)
	}
	defer func() { _ = connection.Close() }()
	request := fmt.Sprintf(`{"type":"response.create","model":%q,"input":[{"role":"user","content":"hello"}]}`, modelName)
	if errWrite := connection.WriteMessage(websocket.TextMessage, []byte(request)); errWrite != nil {
		t.Fatalf("send request: %v", errWrite)
	}
	_, payload, errRead := connection.ReadMessage()
	if errRead != nil {
		t.Fatalf("expected exhausted-pool error, got network disconnect: %v", errRead)
	}
	if gjson.GetBytes(payload, "status").Int() != http.StatusTooManyRequests || gjson.GetBytes(payload, "error.code").String() != "model_cooldown" {
		t.Fatalf("expected cooldown error, got %s", payload)
	}
	if gjson.GetBytes(payload, "headers.Retry-After").String() == "" {
		t.Fatalf("cooldown retry header missing: %s", payload)
	}
	if _, duplicate, errRead := connection.ReadMessage(); errRead == nil {
		t.Fatalf("unexpected additional frame: %s", duplicate)
	}
	<-done
	if executor.streamCalls != 0 {
		t.Fatalf("cooling credentials reached executor %d times", executor.streamCalls)
	}
}

func TestResponsesWebsocketExposesModelCooldown(t *testing.T) {
	cooldown := coreauth.NewModelCooldownError("test-model", "codex", time.Minute)
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "typed", err: cooldown},
		{name: "wrapped", err: fmt.Errorf("selection failed: %w", cooldown)},
		{name: "serialized", err: errors.New(cooldown.Error())},
	} {
		t.Run(test.name, func(t *testing.T) {
			errMsg := &interfaces.ErrorMessage{StatusCode: http.StatusTooManyRequests, Error: test.err}
			if !shouldExposeResponsesUpstreamError(errMsg) {
				t.Fatal("exhausted credential pool must expose its cooldown instead of a network disconnect")
			}
			if shouldReplayResponsesWebsocketPinnedAuthFailure(errMsg) {
				t.Fatal("exhausted credential pool must not request a pinned-auth replay")
			}
		})
	}
}

func TestResponsesWebsocketWritesModelCooldownBeforeClosing(t *testing.T) {
	for _, source := range []string{"stream error", "payload error", "disconnect"} {
		t.Run(source, func(t *testing.T) {
			cooldown := coreauth.NewModelCooldownError("test-model", "codex", time.Minute)
			errMsg := handlers.ExecutionErrorMessage(cooldown)
			serverErr := make(chan error, 1)
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				connection, errUpgrade := responsesWebsocketUpgrader.Upgrade(response, request, nil)
				if errUpgrade != nil {
					serverErr <- errUpgrade
					return
				}
				writer := newResponsesWebsocketWriter(connection)
				if source == "disconnect" {
					writer.closeForUpstreamDisconnect(cooldown)
					serverErr <- nil
					return
				}
				var payload []byte
				if source == "payload error" {
					var errBuild error
					payload, errBuild = buildResponsesWebsocketErrorPayload(errMsg)
					if errBuild != nil {
						_, _ = writer.closeWithoutError()
						serverErr <- errBuild
						return
					}
					errMsg = responsesWebsocketErrorMessageFromPayload(payload)
				}
				_, _, errWrite := writeResponsesWebsocketTerminalError(writer, nil, errMsg, payload)
				if !errors.Is(errWrite, websocket.ErrCloseSent) {
					serverErr <- fmt.Errorf("terminal write: %w", errWrite)
					return
				}
				serverErr <- nil
			}))
			defer server.Close()
			connection, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
			if errDial != nil {
				t.Fatalf("dial websocket: %v", errDial)
			}
			defer func() { _ = connection.Close() }()
			_, payload, errRead := connection.ReadMessage()
			if errRead != nil {
				t.Fatalf("expected quota error before socket close: %v", errRead)
			}
			if gjson.GetBytes(payload, "type").String() != "error" || gjson.GetBytes(payload, "status").Int() != http.StatusTooManyRequests {
				t.Fatalf("expected websocket 429 error, got %s", payload)
			}
			if gjson.GetBytes(payload, "error.code").String() != "model_cooldown" || gjson.GetBytes(payload, "error.reset_seconds").Int() != 60 {
				t.Fatalf("cooldown details not preserved: %s", payload)
			}
			if source != "payload error" && gjson.GetBytes(payload, "headers.Retry-After").String() != "60" {
				t.Fatalf("retry header not preserved: %s", payload)
			}
			if _, duplicate, errRead := connection.ReadMessage(); errRead == nil {
				t.Fatalf("unexpected additional error frame: %s", duplicate)
			}
			if errServer := <-serverErr; errServer != nil {
				t.Fatal(errServer)
			}
		})
	}
}
