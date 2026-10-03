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
	codexWSTestModel        = "gpt-5.6-terra"
)

// codexWSTestSuccessFrames returns the created/completed frames for a healthy credential. The
// response id is tagged with the account so a frame leaked from the rejected first attempt can
// never be mistaken for the successful retry's output.
func codexWSTestSuccessFrames(account string) (created, completed string) {
	id := "resp_ws_scoped_" + account
	created = fmt.Sprintf(`{"type":"response.created","response":{"id":%q}}`, id)
	completed = fmt.Sprintf(`{"type":"response.completed","response":{"id":%q,"status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`, id)
	return created, completed
}

// newCodexWSFailoverServer upgrades every request and answers per Authorization bearer key.
// Accounts other than "account-flagged" complete normally; the flagged account answers with the
// frames produced by flaggedFrames. Connections are deliberately held open after the frames so
// the executor's own teardown path runs, not the reader observing EOF.
func newCodexWSFailoverServer(t *testing.T, attempts *[]string, mu *sync.Mutex, flaggedFrames func(account string) []string) *httptest.Server {
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

		created, completed := codexWSTestSuccessFrames(account)
		frames := []string{created, completed}
		if account == "account-flagged" {
			frames = flaggedFrames(account)
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

// codexWSFailoverServer rejects the flagged credential with the rejection as the very first
// frame, so even the pre-fix code rotates (the first chunk is an Err).
func codexWSFailoverServer(t *testing.T, attempts *[]string, mu *sync.Mutex) *httptest.Server {
	t.Helper()
	return newCodexWSFailoverServer(t, attempts, mu, func(string) []string {
		return []string{codexWSTestFlaggedEvent}
	})
}

// codexWSFailoverServerCreatedFirst emits response.created before the request-scoped rejection,
// so the executor already holds a payload frame when the rejection arrives. That buffered frame
// is the actual trigger for failing the attempt instead of forwarding it plus the rejection.
func codexWSFailoverServerCreatedFirst(t *testing.T, attempts *[]string, mu *sync.Mutex) *httptest.Server {
	t.Helper()
	return newCodexWSFailoverServer(t, attempts, mu, func(account string) []string {
		created, _ := codexWSTestSuccessFrames(account)
		return []string{created, codexWSTestFlaggedEvent}
	})
}

// registerCodexWSTestAuth registers a codex credential. A non-empty action installs the
// invalid_prompt request-scoped rule with that action; an empty action installs no rule.
func registerCodexWSTestAuth(t *testing.T, manager *cliproxyauth.Manager, id string, priority int, baseURL, apiKey, action string) {
	t.Helper()
	registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: codexWSTestModel}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })

	metadata := map[string]any{"disable_cooling": false}
	if action != "" {
		metadata["request_scoped_errors"] = []config.RequestScopedErrorRule{
			{
				Status: http.StatusBadRequest,
				Match:  []string{"invalid_prompt"},
				Action: action,
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

// codexWSStreamEvents extracts the event type of each SSE frame in the collected manager-level
// stream body, in order, so duplicated or reordered frames are visible.
func codexWSStreamEvents(t *testing.T, body []byte) []string {
	t.Helper()
	var events []string
	for _, part := range strings.Split(string(body), "data:") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if idx := strings.Index(part, "\n\n"); idx >= 0 {
			part = part[:idx]
		}
		if eventType := gjson.Get(part, "type").String(); eventType != "" {
			events = append(events, eventType)
		}
	}
	return events
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

// A credential rejected with a request-scoped continue action must be answered by the next
// credential on the same downstream session, without ever signalling the downstream disconnect
// (which would close the client socket). continue cools the credential; continue-and-cooldown
// cools it and then makes it selectable again only after the cooldown.
func TestCodexWSRequestScopedFlaggedCredentialFailsOverWithinDownstreamSession(t *testing.T) {
	for _, action := range []string{cliproxyauth.RequestScopedActionContinueAndCooldown, cliproxyauth.RequestScopedActionContinue} {
		for _, buffering := range []bool{true, false} {
			t.Run(fmt.Sprintf("action=%s/buffering=%t", action, buffering), func(t *testing.T) {
				var mu sync.Mutex
				var attempts []string
				server := codexWSFailoverServer(t, &attempts, &mu)
				defer server.Close()

				manager, exec := newCodexWSScopedManager(t, buffering)
				registerCodexWSTestAuth(t, manager, "ws-scoped-flagged", 100, server.URL, "account-flagged", action)
				registerCodexWSTestAuth(t, manager, "ws-scoped-ok", 0, server.URL, "account-ok", "")

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
				if !ok || flagged == nil {
					t.Fatalf("flagged credential not found, got ok=%v auth=%+v", ok, flagged)
				}
				if action == cliproxyauth.RequestScopedActionContinueAndCooldown {
					if !flagged.Unavailable || flagged.NextRetryAfter.IsZero() {
						t.Fatalf("continue-and-cooldown must cool the flagged credential, got %+v", flagged)
					}
				} else if flagged.Unavailable || !flagged.NextRetryAfter.IsZero() {
					t.Fatalf("continue must leave the flagged credential selectable, got Unavailable=%v NextRetryAfter=%v", flagged.Unavailable, flagged.NextRetryAfter)
				}

				select {
				case disconnectErr := <-disconnectCh:
					t.Fatalf("the retryable credential rejection must not signal a downstream disconnect: %v", disconnectErr)
				default:
				}
			})
		}
	}
}

// A credential that emits response.created and only then the request-scoped rejection leaves a
// payload frame already in hand when the rejection arrives. Because the buffered bootstrap has
// not released that frame yet, the attempt can be failed and retried with a clean downstream
// sequence: exactly one response.created and one response.completed, in that order, and no error
// chunk. This is the trigger the pre-fix code missed, which only failed over when the rejection
// was the very first frame.
func TestCodexWSRequestScopedRejectionAfterBufferedFrameFailsOver(t *testing.T) {
	for _, buffering := range []bool{true, false} {
		t.Run(fmt.Sprintf("buffering=%t", buffering), func(t *testing.T) {
			var mu sync.Mutex
			var attempts []string
			server := codexWSFailoverServerCreatedFirst(t, &attempts, &mu)
			defer server.Close()

			manager, exec := newCodexWSScopedManager(t, buffering)
			registerCodexWSTestAuth(t, manager, "ws-frame-flagged", 100, server.URL, "account-flagged", cliproxyauth.RequestScopedActionContinueAndCooldown)
			registerCodexWSTestAuth(t, manager, "ws-frame-ok", 0, server.URL, "account-ok", "")

			const sessionID = "ws-request-scoped-buffered-frame"
			disconnectCh := exec.UpstreamDisconnectChan(sessionID)
			defer exec.CloseExecutionSession(sessionID)

			body, chunkErr := runCodexWSStream(t, manager, sessionID)

			mu.Lock()
			gotAttempts := append([]string(nil), attempts...)
			mu.Unlock()

			if !buffering {
				// Without bootstrap buffering the created frame is already on the wire before the
				// rejection is read, so no retry can avoid a duplicated frame: the orphaned frame
				// and the rejection surface on the same stream. Pin that boundary explicitly.
				if len(gotAttempts) != 1 || gotAttempts[0] != "account-flagged" {
					t.Fatalf("unbuffered attempts = %v, want [account-flagged]: an already-forwarded frame makes a clean retry impossible", gotAttempts)
				}
				if chunkErr == nil || !strings.Contains(chunkErr.Error(), "invalid_prompt") {
					t.Fatalf("the unbuffered stream must surface the rejection, got %v", chunkErr)
				}
				if events := codexWSStreamEvents(t, body); len(events) != 1 || events[0] != "response.created" {
					t.Fatalf("unbuffered downstream events = %v, want the orphaned [response.created]", events)
				}
			} else {
				// The buffered bootstrap still holds response.created, so the attempt can be failed
				// before anything reaches downstream and the healthy credential answers cleanly.
				if len(gotAttempts) != 2 || gotAttempts[0] != "account-flagged" || gotAttempts[1] != "account-ok" {
					t.Fatalf("attempts = %v, want [account-flagged, account-ok]", gotAttempts)
				}
				if chunkErr != nil {
					t.Fatalf("a successful retry must not surface an error chunk: %v", chunkErr)
				}
				if events := codexWSStreamEvents(t, body); len(events) != 2 || events[0] != "response.created" || events[1] != "response.completed" {
					t.Fatalf("downstream events = %v, want exactly [response.created response.completed]", events)
				}
				flagged, ok := manager.GetByID("ws-frame-flagged")
				if !ok || flagged == nil || !flagged.Unavailable || flagged.NextRetryAfter.IsZero() {
					t.Fatalf("continue-and-cooldown must cool the flagged credential, got ok=%v auth=%+v", ok, flagged)
				}
			}

			select {
			case disconnectErr := <-disconnectCh:
				t.Fatalf("a request-scoped rejection must not signal a downstream disconnect: %v", disconnectErr)
			default:
			}
		})
	}
}

// The downstream Responses WebSocket session must survive the flagged credential: the client
// issues one response.create and receives exactly the completion produced by the next credential
// on the same socket, never an error frame, a duplicate created frame, or a close.
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
			registerCodexWSTestAuth(t, manager, "ws-e2e-flagged", 100, upstream.URL, "account-flagged", cliproxyauth.RequestScopedActionContinueAndCooldown)
			registerCodexWSTestAuth(t, manager, "ws-e2e-ok", 0, upstream.URL, "account-ok", "")

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
			var eventTypes []string
			var responseIDs []string
			for {
				_, payload, errRead := conn.ReadMessage()
				if errRead != nil {
					t.Fatalf("downstream session was torn down instead of failing over: %v", errRead)
				}
				eventType := gjson.GetBytes(payload, "type").String()
				if eventType == "error" {
					t.Fatalf("downstream received an error instead of a transparent failover: %s", payload)
				}
				eventTypes = append(eventTypes, eventType)
				if id := gjson.GetBytes(payload, "response.id").String(); id != "" {
					responseIDs = append(responseIDs, id)
				}
				if eventType == "response.completed" {
					break
				}
			}

			if len(eventTypes) != 2 || eventTypes[0] != "response.created" || eventTypes[1] != "response.completed" {
				t.Fatalf("downstream event sequence = %v, want exactly [response.created response.completed]", eventTypes)
			}
			for _, id := range responseIDs {
				if id != "resp_ws_scoped_account-ok" {
					t.Fatalf("downstream saw a response id from the rejected attempt: %q (all ids: %v)", id, responseIDs)
				}
			}

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
// and the request fault must still reach the caller instead of being swallowed. No credential
// is attempted twice and the request-scoped rejections never signal a downstream disconnect.
func TestCodexWSRequestScopedAllCandidatesFlaggedSurfacesError(t *testing.T) {
	for _, buffering := range []bool{true, false} {
		t.Run(fmt.Sprintf("buffering=%t", buffering), func(t *testing.T) {
			var mu sync.Mutex
			var attempts []string
			server := codexWSFailoverServer(t, &attempts, &mu)
			defer server.Close()

			manager, exec := newCodexWSScopedManager(t, buffering)
			registerCodexWSTestAuth(t, manager, "ws-scoped-flagged-a", 100, server.URL, "account-flagged", cliproxyauth.RequestScopedActionContinueAndCooldown)
			registerCodexWSTestAuth(t, manager, "ws-scoped-flagged-b", 0, server.URL, "account-flagged", cliproxyauth.RequestScopedActionContinueAndCooldown)

			const sessionID = "ws-request-scoped-exhausted"
			disconnectCh := exec.UpstreamDisconnectChan(sessionID)
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

			mu.Lock()
			gotAttempts := append([]string(nil), attempts...)
			mu.Unlock()
			if len(gotAttempts) != 2 || gotAttempts[0] != "account-flagged" || gotAttempts[1] != "account-flagged" {
				t.Fatalf("each credential must be attempted exactly once, got %v", gotAttempts)
			}

			select {
			case disconnectErr := <-disconnectCh:
				t.Fatalf("request-scoped rejections must not signal a downstream disconnect: %v", disconnectErr)
			default:
			}
		})
	}
}
