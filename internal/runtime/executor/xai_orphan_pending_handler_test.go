package executor

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	openai "github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers/openai"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

// xaiHandlerPrepareCaptureExecutor is the xAI executor boundary used by the
// Responses handler. It runs the real prepare path on the handler ctx, then
// returns a synthetic completion so the test never dials upstream.
type xaiHandlerPrepareCaptureExecutor struct {
	ws              *XAIWebsocketsExecutor
	mu              sync.Mutex
	prepared        [][]byte
	responseOutputs [][]byte
	done            chan struct{}
	doneOnce        sync.Once
}

func (e *xaiHandlerPrepareCaptureExecutor) Identifier() string { return "xai" }

func (e *xaiHandlerPrepareCaptureExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if err := e.capturePrepared(ctx, req, opts, false); err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"id":"resp-http","output":[]}`)}, nil
}

func (e *xaiHandlerPrepareCaptureExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if err := e.capturePrepared(ctx, req, opts, true); err != nil {
		return nil, err
	}

	e.mu.Lock()
	count := len(e.prepared)
	responseOutput := []byte(`[]`)
	if count <= len(e.responseOutputs) {
		responseOutput = e.responseOutputs[count-1]
	}
	e.mu.Unlock()

	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp-%d","output":%s}}`, count, responseOutput))}
	close(chunks)
	if count >= 2 && e.done != nil {
		e.doneOnce.Do(func() { close(e.done) })
	}
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *xaiHandlerPrepareCaptureExecutor) capturePrepared(ctx context.Context, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream bool) error {
	var (
		prepared *xaiPreparedRequest
		err      error
	)
	if stream {
		prepared, err = e.ws.prepareResponsesWebsocketRequest(ctx, req, opts)
	} else {
		prepared, err = e.ws.prepareResponsesRequest(ctx, req, opts, false)
	}
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.prepared = append(e.prepared, bytes.Clone(prepared.body))
	e.mu.Unlock()
	return nil
}

func (e *xaiHandlerPrepareCaptureExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *xaiHandlerPrepareCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, fmt.Errorf("not implemented")
}

func (e *xaiHandlerPrepareCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("not implemented")
}

func (e *xaiHandlerPrepareCaptureExecutor) preparedBodies() [][]byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([][]byte, len(e.prepared))
	for i := range e.prepared {
		out[i] = bytes.Clone(e.prepared[i])
	}
	return out
}

func newXAIOrphanPendingHandler(t *testing.T, executor *xaiHandlerPrepareCaptureExecutor, websockets bool) (*openai.OpenAIResponsesAPIHandler, string) {
	t.Helper()

	modelID := "xai-orphan-" + strings.ReplaceAll(t.Name(), "/", "-")
	authID := "auth-" + modelID
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "xai",
		Status:   coreauth.StatusActive,
		ProxyURL: "direct",
	}
	if websockets {
		auth.Attributes = map[string]string{"websockets": "true"}
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: modelID}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{CodexOrphanDelegationCompatibility: true}, manager)
	return openai.NewOpenAIResponsesAPIHandler(base), modelID
}

func newEnabledXAIWebsocketsExecutor() *XAIWebsocketsExecutor {
	return NewXAIWebsocketsExecutor(&config.Config{
		Codex: config.CodexConfig{OrphanDelegationCompatibility: true},
	})
}

func TestXAIHandlerPendingDelegationThroughExecutorPrepare(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("native websocket continuation preserves pending output", func(t *testing.T) {
		executor := &xaiHandlerPrepareCaptureExecutor{
			ws:   newEnabledXAIWebsocketsExecutor(),
			done: make(chan struct{}),
			responseOutputs: [][]byte{
				[]byte(`[{"type":"function_call","call_id":"call-pending","name":"create_thread","namespace":"codex_app","arguments":"{}"}]`),
				[]byte(`[]`),
			},
		}
		handler, modelID := newXAIOrphanPendingHandler(t, executor, true)
		router := gin.New()
		router.GET("/v1/responses/ws", handler.ResponsesWebsocket)
		server := httptest.NewServer(router)
		t.Cleanup(server.Close)

		conn := dialResponsesWebsocket(t, server)
		t.Cleanup(func() { _ = conn.Close() })

		writeResponsesWebsocketJSON(t, conn, fmt.Sprintf(`{"type":"response.create","model":%q,"input":[{"type":"message","role":"user","content":"start"}]}`, modelID))
		readResponsesWebsocketMessage(t, conn)
		writeResponsesWebsocketJSON(t, conn, `{"type":"response.append","input":[{"type":"function_call_output","call_id":"call-pending","name":"create_thread","namespace":"codex_app","output":"completed"}]}`)
		readResponsesWebsocketMessage(t, conn)
		waitPrepared(t, executor.done)

		prepared := executor.preparedBodies()
		if len(prepared) != 2 {
			t.Fatalf("prepared payload count = %d, want 2", len(prepared))
		}
		if itemType := firstInputType(prepared[1]); itemType != "function_call_output" {
			t.Fatalf("xAI continuation rewrite dropped pending output: %s", prepared[1])
		}
	})

	t.Run("native websocket reset does not inherit pending", func(t *testing.T) {
		executor := &xaiHandlerPrepareCaptureExecutor{
			ws:   newEnabledXAIWebsocketsExecutor(),
			done: make(chan struct{}),
			responseOutputs: [][]byte{
				[]byte(`[{"type":"function_call","call_id":"call-pending","name":"create_thread","namespace":"codex_app","arguments":"{}"}]`),
				[]byte(`[]`),
			},
		}
		handler, modelID := newXAIOrphanPendingHandler(t, executor, true)
		router := gin.New()
		router.GET("/v1/responses/ws", handler.ResponsesWebsocket)
		server := httptest.NewServer(router)
		t.Cleanup(server.Close)

		conn := dialResponsesWebsocket(t, server)
		t.Cleanup(func() { _ = conn.Close() })

		writeResponsesWebsocketJSON(t, conn, fmt.Sprintf(`{"type":"response.create","model":%q,"input":[{"type":"message","role":"user","content":"start"}]}`, modelID))
		readResponsesWebsocketMessage(t, conn)
		writeResponsesWebsocketJSON(t, conn, `{"type":"response.create","input":[{"type":"function_call_output","call_id":"call-pending","name":"create_thread","namespace":"codex_app","output":"completed"}]}`)
		readResponsesWebsocketMessage(t, conn)
		waitPrepared(t, executor.done)

		prepared := executor.preparedBodies()
		if len(prepared) != 2 {
			t.Fatalf("prepared payload count = %d, want 2", len(prepared))
		}
		if itemType := firstInputType(prepared[1]); itemType != "message" {
			t.Fatalf("xAI reset retained stale pending output: %s", prepared[1])
		}
	})

	t.Run("stateless HTTP does not inherit pending", func(t *testing.T) {
		executor := &xaiHandlerPrepareCaptureExecutor{ws: newEnabledXAIWebsocketsExecutor()}
		handler, modelID := newXAIOrphanPendingHandler(t, executor, false)
		router := gin.New()
		router.POST("/v1/responses", handler.Responses)

		payload := fmt.Sprintf(`{
			"model": %q,
			"stream": false,
			"previous_response_id": "resp-previous",
			"input": [{
				"type": "function_call_output",
				"call_id": "call-pending",
				"name": "create_thread",
				"namespace": "codex_app",
				"output": "completed"
			}]
		}`, modelID)
		request := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(payload))
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
		}

		prepared := executor.preparedBodies()
		if len(prepared) != 1 {
			t.Fatalf("prepared payload count = %d, want 1", len(prepared))
		}
		if itemType := firstInputType(prepared[0]); itemType != "message" {
			t.Fatalf("stateless HTTP retained unpaired output: %s", prepared[0])
		}
	})
}

func dialResponsesWebsocket(t *testing.T, server *httptest.Server) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, errDial := websocket.DefaultDialer.Dial(wsURL, nil)
	if errDial != nil {
		t.Fatalf("dial websocket: %v", errDial)
	}
	return conn
}

func writeResponsesWebsocketJSON(t *testing.T, conn *websocket.Conn, payload string) {
	t.Helper()
	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(payload)); errWrite != nil {
		t.Fatalf("write websocket message: %v", errWrite)
	}
}

func readResponsesWebsocketMessage(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	if _, _, errRead := conn.ReadMessage(); errRead != nil {
		t.Fatalf("read websocket response: %v", errRead)
	}
}

func waitPrepared(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for xAI prepare")
	}
}

func firstInputType(payload []byte) string {
	return gjson.GetBytes(payload, "input.0.type").String()
}
