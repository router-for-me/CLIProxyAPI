package openai

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/middleware"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

type websocketLoggingExecutor struct {
	websocketDirectCaptureExecutor
	record func(context.Context, string, []byte)
}

func (e *websocketLoggingExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.record(ctx, "executor", req.Payload)
	e.mu.Lock()
	if len(e.payloads) == 2 {
		e.failStatus = http.StatusBadGateway
	}
	e.mu.Unlock()
	return e.websocketDirectCaptureExecutor.ExecuteStream(ctx, auth, req, opts)
}

type websocketLoggingRouter struct {
	record   func(context.Context, string, []byte)
	contexts chan context.Context
}

func (*websocketLoggingRouter) HasModelRouters() bool { return true }

func (r *websocketLoggingRouter) RouteModel(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
	r.record(ctx, "router", req.Body)
	if r.contexts != nil {
		r.contexts <- ctx
	}
	return pluginapi.ModelRouteResponse{}, false
}

func TestResponsesWebsocketRequestLoggingLifecycle(t *testing.T) {
	for _, fullLog := range []bool{false, true} {
		name := "error-only"
		if fullLog {
			name = "full"
		}
		t.Run(name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			cfg := &config.Config{SDKConfig: config.SDKConfig{RequestLog: fullLog}}
			observations := make(chan int, 16)
			record := func(ctx context.Context, source string, body []byte) {
				helps.RecordAPIRequest(ctx, cfg, helps.UpstreamRequestLog{URL: "https://example.invalid/" + source, Method: http.MethodPost, Body: body})
				c, _ := ctx.Value("gin").(*gin.Context)
				value, _ := c.Get(logging.DeferredAPIRequestContextKey)
				requests, _ := value.([]logging.DeferredAPIRequest)
				observations <- len(requests)
			}
			executor := &websocketLoggingExecutor{record: record}
			manager := coreauth.NewManager(nil, nil, nil)
			manager.RegisterExecutor(executor)
			authID := "websocket-log-" + name
			if _, err := manager.Register(context.Background(), &coreauth.Auth{ID: authID, Provider: "codex", Status: coreauth.StatusActive}); err != nil {
				t.Fatal(err)
			}
			registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: "logging-model"}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
			base := handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)
			modelRouter := &websocketLoggingRouter{record: record, contexts: make(chan context.Context, 8)}
			base.SetModelRouterHost(modelRouter)
			h := NewOpenAIResponsesAPIHandler(base)
			logsDir := t.TempDir()
			logger := logging.NewFileRequestLogger(fullLog, logsDir, "", 10)
			handlerDone := make(chan struct{})
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Next()
				close(handlerDone)
			})
			router.Use(middleware.RequestLoggingMiddleware(logger))
			router.GET("/v1/responses", h.ResponsesWebsocket)
			server := httptest.NewServer(router)
			defer server.Close()
			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
			send := func(payload string) {
				t.Helper()
				if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(payload)); errWrite != nil {
					t.Fatal(errWrite)
				}
			}
			readEvent := func(want string) {
				t.Helper()
				_, data, errRead := conn.ReadMessage()
				if errRead != nil {
					t.Fatal(errRead)
				}
				if got := gjson.GetBytes(data, "type").String(); got != want {
					t.Fatalf("event = %q, want %q", got, want)
				}
			}
			assertNoCapture := func() {
				t.Helper()
				select {
				case n := <-observations:
					if n != 0 {
						t.Fatalf("open WebSocket retained %d deferred requests", n)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("request capture was not observed")
				}
			}
			// Routing happens even for a locally answered prewarm request.
			send(`{"type":"response.create","model":"logging-model","generate":false}`)
			readEvent("response.created")
			readEvent("response.completed")
			assertNoCapture()
			callbackCtx := <-modelRouter.contexts
			payload := `{"type":"response.create","model":"logging-model","input":[{"role":"user","content":"` + strings.Repeat("x", 256<<10) + `"}]}`
			for range 2 {
				send(payload)
				readEvent("response.completed")
				assertNoCapture()
				assertNoCapture()
			}
			// A lifecycle callback can perform host HTTP work after its turn ended.
			go record(context.WithoutCancel(callbackCtx), "late-plugin", []byte("callback-body"))
			assertNoCapture()
			send(payload)
			if _, data, errRead := conn.ReadMessage(); errRead == nil {
				if gjson.GetBytes(data, "type").String() != "error" {
					t.Fatalf("failed upstream returned %s", data)
				}
			} else {
				var closeErr *websocket.CloseError
				if !errors.As(errRead, &closeErr) {
					t.Fatalf("upstream failure returned %v, want an error event or close frame", errRead)
				}
			}
			assertNoCapture()
			assertNoCapture()
			_ = conn.Close()
			select {
			case <-handlerDone:
			case <-time.After(5 * time.Second):
				t.Fatal("WebSocket handler did not finish")
			}
			if got := len(executor.Payloads()); got != 3 {
				t.Fatalf("upstream calls = %d, want 3", got)
			}
			if fullLog {
				entries, errReadDir := os.ReadDir(logsDir)
				if errReadDir != nil {
					t.Fatal(errReadDir)
				}
				var logged []byte
				for _, entry := range entries {
					if !strings.HasSuffix(entry.Name(), ".log") {
						continue
					}
					body, errRead := os.ReadFile(filepath.Join(logsDir, entry.Name()))
					if errRead != nil {
						t.Fatal(errRead)
					}
					logged = append(logged, body...)
				}
				for _, source := range []string{"router", "executor", "late-plugin"} {
					if !bytes.Contains(logged, []byte("https://example.invalid/"+source)) {
						t.Errorf("full request log lost %s requests", source)
					}
				}
			}
		})
	}
}

func TestResponsesWebsocketRejectedUpgradeKeepsErrorCapture(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	c.Request.Header.Set("Connection", "Upgrade")
	c.Request.Header.Set("Upgrade", "websocket")
	// Missing handshake headers make the upgrade fail before a socket exists.
	(&OpenAIResponsesAPIHandler{}).ResponsesWebsocket(c)
	if c.Writer.Status() < http.StatusBadRequest {
		t.Fatal("invalid WebSocket handshake was accepted")
	}
	ctx := context.WithValue(c.Request.Context(), "gin", c)
	helps.RecordAPIRequest(ctx, &config.Config{}, helps.UpstreamRequestLog{Body: []byte("handshake-error")})
	value, _ := c.Get(logging.DeferredAPIRequestContextKey)
	requests, _ := value.([]logging.DeferredAPIRequest)
	if len(requests) != 1 || !bytes.Contains(requests[0](), []byte("handshake-error")) {
		t.Fatal("failed upgrade lost ordinary HTTP error capture")
	}
}
