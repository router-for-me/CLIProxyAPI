package test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	openaihandlers "github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

const (
	// The rejection the Codex backend returns for a credential whose traffic it flagged.
	// It carries no top-level status, matching the observed production payload.
	codexWSTestFlaggedEvent = `{"type":"error","error":{"type":"invalid_request_error","code":"invalid_prompt","message":"flagged as potentially violating our usage policy"}}`
	codexWSTestCreatedEvent = `{"type":"response.created","response":{"id":"resp_ws_scoped"}}`
	codexWSTestDoneEvent    = `{"type":"response.completed","response":{"id":"resp_ws_scoped","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`
	codexWSTestModel        = "gpt-5.6-terra"
)

// codexWSFailoverServer upgrades every request and answers per Authorization bearer key.
// "account-flagged" is rejected with the invalid_prompt request-scoped rejection; every
// other account completes. Connections are deliberately held open after the frames so the
// executor's own teardown path runs, not the reader observing EOF.
func codexWSFailoverServer(t *testing.T, attempts *[]string, mu *sync.Mutex) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		account := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		*attempts = append(*attempts, account)
		mu.Unlock()

		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Errorf("upgrade websocket: %v", errUpgrade)
			return
		}
		defer func() { _ = conn.Close() }()
		if _, _, errRead := conn.ReadMessage(); errRead != nil {
			t.Errorf("read websocket request: %v", errRead)
			return
		}

		frames := []string{codexWSTestCreatedEvent, codexWSTestDoneEvent}
		if account == "account-flagged" {
			frames = []string{codexWSTestFlaggedEvent}
		}
		for _, frame := range frames {
			if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(frame)); errWrite != nil {
				t.Errorf("write websocket event: %v", errWrite)
				return
			}
		}
		for {
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				return
			}
		}
	}))
}

func registerCodexWSTestAuth(t *testing.T, manager *cliproxyauth.Manager, id string, priority int, baseURL, apiKey string, rule bool) {
	t.Helper()
	registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: codexWSTestModel}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })

	metadata := map[string]any{"disable_cooling": false}
	if rule {
		metadata["request_scoped_errors"] = []config.RequestScopedErrorRule{
			{
				Status: http.StatusBadRequest,
				Match:  []string{"invalid_prompt"},
				Action: cliproxyauth.RequestScopedActionContinueAndCooldown,
			},
		}
	}
	if _, errRegister := manager.Register(context.Background(), &cliproxyauth.Auth{
		ID:       id,
		Provider: "codex",
		Status:   cliproxyauth.StatusActive,
		Attributes: map[string]string{
			"priority": fmt.Sprint(priority),
			"base_url": baseURL,
			"api_key":  apiKey,
		},
		Metadata: metadata,
	}); errRegister != nil {
		t.Fatal(errRegister)
	}
}

// runCodexWSStream drives one manager-level stream and returns the collected payloads plus
// the first terminal chunk error, if any.
func runCodexWSStream(t *testing.T, manager *cliproxyauth.Manager, sessionID string) ([]byte, error) {
	t.Helper()
	payload := []byte(fmt.Sprintf(`{"model":%q,"input":"hello"}`, codexWSTestModel))
	opts := cliproxyexecutor.Options{
		Stream:          true,
		SourceFormat:    sdktranslator.FromString("openai-response"),
		OriginalRequest: payload,
	}
	if sessionID != "" {
		opts.Metadata = map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: sessionID}
	}
	result, errStream := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{
		Model: codexWSTestModel, Payload: payload,
	}, opts)
	if errStream != nil {
		return nil, errStream
	}
	var body []byte
	var chunkErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			if chunkErr == nil {
				chunkErr = chunk.Err
			}
			continue
		}
		body = append(body, chunk.Payload...)
	}
	return body, chunkErr
}

func newCodexWSScopedManager(t *testing.T, buffering bool) (*cliproxyauth.Manager, *runtimeexecutor.CodexWebsocketsExecutor) {
	t.Helper()
	manager := cliproxyauth.NewManager(nil, &cliproxyauth.RoundRobinSelector{}, nil)
	manager.SetRetryConfig(0, 0, 2)
	cfg := &config.Config{Codex: config.CodexConfig{StreamBootstrapBuffering: buffering}}
	exec := runtimeexecutor.NewCodexWebsocketsExecutor(cfg)
	manager.RegisterExecutor(exec)
	return manager, exec
}

// A credential rejected with a request-scoped continue-and-cooldown rejection must be
// cooled down and the same downstream session must be answered by the next credential,
// without ever signalling the downstream disconnect (which would close the client socket).
func TestCodexWSRequestScopedFlaggedCredentialFailsOverWithinDownstreamSession(t *testing.T) {
	for _, buffering := range []bool{true, false} {
		t.Run(fmt.Sprintf("buffering=%t", buffering), func(t *testing.T) {
			var mu sync.Mutex
			var attempts []string
			server := codexWSFailoverServer(t, &attempts, &mu)
			defer server.Close()

			manager, exec := newCodexWSScopedManager(t, buffering)
			registerCodexWSTestAuth(t, manager, "ws-scoped-flagged", 100, server.URL, "account-flagged", true)
			registerCodexWSTestAuth(t, manager, "ws-scoped-ok", 0, server.URL, "account-ok", false)

			const sessionID = "ws-request-scoped-failover"
			disconnectCh := exec.UpstreamDisconnectChan(sessionID)
			defer exec.CloseExecutionSession(sessionID)

			body, errStream := runCodexWSStream(t, manager, sessionID)
			if errStream != nil {
				t.Fatalf("flagged credential must not surface an error when another credential succeeds: %v", errStream)
			}
			if !strings.Contains(string(body), "response.completed") {
				t.Fatalf("stream body missing completion from the healthy credential: %s", body)
			}

			mu.Lock()
			gotAttempts := append([]string(nil), attempts...)
			mu.Unlock()
			if len(gotAttempts) != 2 || gotAttempts[0] != "account-flagged" || gotAttempts[1] != "account-ok" {
				t.Fatalf("attempts = %v, want [account-flagged, account-ok]", gotAttempts)
			}

			flagged, ok := manager.GetByID("ws-scoped-flagged")
			if !ok || flagged == nil || !flagged.Unavailable || flagged.NextRetryAfter.IsZero() {
				t.Fatalf("continue-and-cooldown must cool the flagged credential, got ok=%v auth=%+v", ok, flagged)
			}

			select {
			case disconnectErr := <-disconnectCh:
				t.Fatalf("the retryable credential rejection must not signal a downstream disconnect: %v", disconnectErr)
			default:
			}
		})
	}
}

// The downstream Responses WebSocket session must survive the flagged credential: the client
// issues one response.create and receives the completion produced by the next credential on the
// same socket, never an error frame and never a close.
func TestCodexWSRequestScopedFlaggedCredentialKeepsDownstreamSessionAlive(t *testing.T) {
	for _, buffering := range []bool{true, false} {
		t.Run(fmt.Sprintf("buffering=%t", buffering), func(t *testing.T) {
			gin.SetMode(gin.TestMode)

			var mu sync.Mutex
			var attempts []string
			upstream := codexWSFailoverServer(t, &attempts, &mu)
			defer upstream.Close()

			cfg := &config.Config{Codex: config.CodexConfig{StreamBootstrapBuffering: buffering}}
			manager := cliproxyauth.NewManager(nil, &cliproxyauth.RoundRobinSelector{}, nil)
			manager.SetRetryConfig(0, 0, 2)
			manager.RegisterExecutor(runtimeexecutor.NewCodexWebsocketsExecutor(cfg))
			registerCodexWSTestAuth(t, manager, "ws-e2e-flagged", 100, upstream.URL, "account-flagged", true)
			registerCodexWSTestAuth(t, manager, "ws-e2e-ok", 0, upstream.URL, "account-ok", false)

			base := handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)
			handler := openaihandlers.NewOpenAIResponsesAPIHandler(base)
			router := gin.New()
			router.GET("/v1/responses/ws", handler.ResponsesWebsocket)
			server := httptest.NewServer(router)
			defer server.Close()

			wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
			conn, _, errDial := websocket.DefaultDialer.Dial(wsURL, nil)
			if errDial != nil {
				t.Fatalf("dial downstream websocket: %v", errDial)
			}
			defer func() { _ = conn.Close() }()

			request := fmt.Sprintf(`{"type":"response.create","model":%q,"input":[{"type":"message","role":"user","content":"hello"}]}`, codexWSTestModel)
			if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(request)); errWrite != nil {
				t.Fatalf("write downstream request: %v", errWrite)
			}

			_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
			for {
				_, payload, errRead := conn.ReadMessage()
				if errRead != nil {
					t.Fatalf("downstream session was torn down instead of failing over: %v", errRead)
				}
				switch gjson.GetBytes(payload, "type").String() {
				case "error":
					t.Fatalf("downstream received an error instead of a transparent failover: %s", payload)
				case "response.completed":
					goto completed
				}
			}
		completed:

			mu.Lock()
			gotAttempts := append([]string(nil), attempts...)
			mu.Unlock()
			if len(gotAttempts) != 2 || gotAttempts[0] != "account-flagged" || gotAttempts[1] != "account-ok" {
				t.Fatalf("attempts = %v, want [account-flagged, account-ok]", gotAttempts)
			}
		})
	}
}

// When every candidate is rejected by the flagged credential the retry budget is exhausted
// and the request fault must still reach the caller instead of being swallowed.
func TestCodexWSRequestScopedAllCandidatesFlaggedSurfacesError(t *testing.T) {
	for _, buffering := range []bool{true, false} {
		t.Run(fmt.Sprintf("buffering=%t", buffering), func(t *testing.T) {
			var mu sync.Mutex
			var attempts []string
			server := codexWSFailoverServer(t, &attempts, &mu)
			defer server.Close()

			manager, exec := newCodexWSScopedManager(t, buffering)
			registerCodexWSTestAuth(t, manager, "ws-scoped-flagged-a", 100, server.URL, "account-flagged", true)
			registerCodexWSTestAuth(t, manager, "ws-scoped-flagged-b", 0, server.URL, "account-flagged", true)

			const sessionID = "ws-request-scoped-exhausted"
			defer exec.CloseExecutionSession(sessionID)

			_, errStream := runCodexWSStream(t, manager, sessionID)
			if errStream == nil {
				t.Fatal("expected the exhausted retry budget to surface the rejection")
			}
			var statusErr interface{ StatusCode() int }
			if !errors.As(errStream, &statusErr) || statusErr.StatusCode() != http.StatusBadRequest {
				t.Fatalf("surfaced error = %v, want a 400 request fault", errStream)
			}
			if !strings.Contains(errStream.Error(), "invalid_prompt") {
				t.Fatalf("surfaced error must carry the upstream rejection: %v", errStream)
			}
		})
	}
}
