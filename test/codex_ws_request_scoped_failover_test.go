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

const (
	// The same rejection with a top-level status, which the backend sometimes adds. It routes
	// through the websocket-error branch instead of the terminal-failure branch, so both wire
	// shapes of the production rejection must be covered by the failover tests.
	codexWSTestFlaggedStatusEvent = `{"type":"error","status":400,"error":{"type":"invalid_request_error","code":"invalid_prompt","message":"flagged as potentially violating our usage policy"}}`
	// A downstream read that never sees a single frame must fail fast instead of hanging.
	codexWSDownstreamFirstFrameTimeout = 10 * time.Second
	// After response.completed the downstream socket may stay open for another response.create,
	// so keep reading for a short quiet window: a duplicated or orphaned frame emitted after the
	// first completion would otherwise be missed by an early break.
	codexWSDownstreamDrainWindow = 750 * time.Millisecond
	// codex.stream-bootstrap-buffering with a short timeout expires the bootstrap window while
	// the flagged upstream is still silent, which is the spent-budget path under test.
	codexWSShortBootstrapTimeout = "50ms"
	// Longer than codexWSShortBootstrapTimeout so the first upstream read happens after the
	// bootstrap budget is already spent.
	codexWSBudgetExceededDelay = 200 * time.Millisecond
)

// codexWSTestRejection is one wire shape of the credential-scoped rejection under test.
type codexWSTestRejection struct {
	name  string
	frame string
}

// codexWSTestFlaggedRejections returns both wire shapes of the production invalid_prompt
// rejection so a test can pin the failover rule for each one.
func codexWSTestFlaggedRejections() []codexWSTestRejection {
	return []codexWSTestRejection{
		{name: "no-status", frame: codexWSTestFlaggedEvent},
		{name: "status-bearing", frame: codexWSTestFlaggedStatusEvent},
	}
}

// codexWSTestSuccessFrames returns the created/completed frames for a healthy credential. The
// response id is tagged with the account so a frame leaked from the rejected first attempt can
// never be mistaken for the successful retry's output.
func codexWSTestSuccessFrames(account string) (created, completed string) {
	id := "resp_ws_scoped_" + account
	created = fmt.Sprintf(`{"type":"response.created","response":{"id":%q}}`, id)
	completed = fmt.Sprintf(`{"type":"response.completed","response":{"id":%q,"status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`, id)
	return created, completed
}

// codexWSTestFlaggedAccount reports whether a bearer key belongs to a credential this test
// server rejects. Keys are matched by prefix so a test can register several distinct flagged
// credentials and still tell which one was attempted.
func codexWSTestFlaggedAccount(account string) bool {
	return strings.HasPrefix(account, "account-flagged")
}

// newCodexWSFailoverServerScript upgrades every request and answers per Authorization bearer
// key. A flagged key is answered by flaggedScript, which owns both the timing and the content of
// the rejected attempt, so a test can reproduce a rejection that arrives after the bootstrap
// budget is spent. Connections are deliberately held open after the frames so the executor's own
// teardown path runs, not the reader observing EOF.
func newCodexWSFailoverServerScript(t *testing.T, attempts *[]string, mu *sync.Mutex, flaggedScript func(account string, conn *websocket.Conn)) *httptest.Server {
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

		if codexWSTestFlaggedAccount(account) {
			flaggedScript(account, conn)
		} else {
			created, completed := codexWSTestSuccessFrames(account)
			for _, frame := range []string{created, completed} {
				if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(frame)); errWrite != nil {
					t.Errorf("write websocket event: %v", errWrite)
					return
				}
			}
		}
		for {
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				return
			}
		}
	}))
}

// newCodexWSFailoverServer answers the flagged credential with the fixed frame list produced by
// flaggedFrames, so a caller describes the rejected attempt's content but not its timing.
func newCodexWSFailoverServer(t *testing.T, attempts *[]string, mu *sync.Mutex, flaggedFrames func(account string) []string) *httptest.Server {
	t.Helper()
	return newCodexWSFailoverServerScript(t, attempts, mu, func(account string, conn *websocket.Conn) {
		for _, frame := range flaggedFrames(account) {
			if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(frame)); errWrite != nil {
				t.Errorf("write websocket event: %v", errWrite)
				return
			}
		}
	})
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
	return newCodexWSScopedManagerWithTimeout(t, buffering, "")
}

// newCodexWSScopedManagerWithTimeout additionally sets codex.stream-bootstrap-timeout, the knob
// that lets the bootstrap budget run out while the upstream is still silent.
func newCodexWSScopedManagerWithTimeout(t *testing.T, buffering bool, bootstrapTimeout string) (*cliproxyauth.Manager, *runtimeexecutor.CodexWebsocketsExecutor) {
	t.Helper()
	manager := cliproxyauth.NewManager(nil, &cliproxyauth.RoundRobinSelector{}, nil)
	manager.SetRetryConfig(0, 0, 2)
	cfg := &config.Config{Codex: config.CodexConfig{StreamBootstrapBuffering: buffering, StreamBootstrapTimeout: bootstrapTimeout}}
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

// codexWSDownstreamRead is every frame a downstream client observed during one response.create
// round trip, so missing, extra, or reordered frames are all visible to the caller.
type codexWSDownstreamRead struct {
	eventTypes  []string
	responseIDs []string
	errorFrames []string
}

// readCodexWSDownstreamFrames reads until the downstream connection closes or falls quiet for
// codexWSDownstreamDrainWindow after the first terminal frame, so a duplicate or orphaned frame
// emitted after the completion is still observed instead of being truncated by an early break.
// It never fails the test itself; the caller asserts on the collected frames.
func readCodexWSDownstreamFrames(t *testing.T, conn *websocket.Conn) codexWSDownstreamRead {
	t.Helper()
	var read codexWSDownstreamRead
	deadline := codexWSDownstreamFirstFrameTimeout
	for {
		_ = conn.SetReadDeadline(time.Now().Add(deadline))
		_, payload, errRead := conn.ReadMessage()
		if errRead != nil {
			return read
		}
		eventType := gjson.GetBytes(payload, "type").String()
		read.eventTypes = append(read.eventTypes, eventType)
		if eventType == "error" {
			read.errorFrames = append(read.errorFrames, string(payload))
		}
		if id := gjson.GetBytes(payload, "response.id").String(); id != "" {
			read.responseIDs = append(read.responseIDs, id)
		}
		if eventType == "response.completed" || eventType == "response.done" || eventType == "response.incomplete" {
			deadline = codexWSDownstreamDrainWindow
		}
	}
}

// A request-scoped rejection that arrives after the bootstrap time budget is spent but before
// any payload frame was buffered must still fail over transparently: nothing has reached the
// client yet, so the retry can be delivered on the same socket. Notifying the downstream
// disconnect here would close the client session and cost one credential per connection, even
// though the conductor can still answer on the next credential. Both wire shapes of the
// rejection are covered because they take different executor branches.
func TestCodexWSRequestScopedSpentBudgetWithoutBufferedFrameFailsOver(t *testing.T) {
	for _, rejection := range codexWSTestFlaggedRejections() {
		t.Run(rejection.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)

			var mu sync.Mutex
			var attempts []string
			upstream := newCodexWSFailoverServerScript(t, &attempts, &mu, func(_ string, conn *websocket.Conn) {
				// Stay silent past the bootstrap budget, then reject before any frame was buffered.
				time.Sleep(codexWSBudgetExceededDelay)
				if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(rejection.frame)); errWrite != nil {
					t.Errorf("write websocket event: %v", errWrite)
				}
			})
			defer upstream.Close()

			cfg := &config.Config{Codex: config.CodexConfig{
				StreamBootstrapBuffering: true,
				StreamBootstrapTimeout:   codexWSShortBootstrapTimeout,
			}}
			manager := cliproxyauth.NewManager(nil, &cliproxyauth.RoundRobinSelector{}, nil)
			manager.SetRetryConfig(0, 0, 2)
			manager.RegisterExecutor(runtimeexecutor.NewCodexWebsocketsExecutor(cfg))
			registerCodexWSTestAuth(t, manager, "ws-budget-flagged", 100, upstream.URL, "account-flagged", cliproxyauth.RequestScopedActionContinueAndCooldown)
			registerCodexWSTestAuth(t, manager, "ws-budget-ok", 0, upstream.URL, "account-ok", "")

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

			read := readCodexWSDownstreamFrames(t, conn)
			if len(read.errorFrames) != 0 {
				t.Fatalf("downstream received an error instead of a transparent failover: %v", read.errorFrames)
			}
			if len(read.eventTypes) != 2 || read.eventTypes[0] != "response.created" || read.eventTypes[1] != "response.completed" {
				t.Fatalf("downstream event sequence = %v, want exactly [response.created response.completed]", read.eventTypes)
			}
			for _, id := range read.responseIDs {
				if id != "resp_ws_scoped_account-ok" {
					t.Fatalf("downstream saw a response id from the rejected attempt: %q (all ids: %v)", id, read.responseIDs)
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

// The same spent budget with a payload frame already buffered is the opposite case: the buffered
// handshake is released before the rejection, so the conductor can no longer retry the request.
// The teardown must stay the ordinary notifying one, and the rejection must be delivered
// in-stream after the buffered frame instead of silently rotating a second credential. Both wire
// shapes of the rejection are covered because they take different executor branches.
func TestCodexWSRequestScopedSpentBudgetWithBufferedFrameNotifiesAndSurfacesRejection(t *testing.T) {
	for _, rejection := range codexWSTestFlaggedRejections() {
		t.Run(rejection.name, func(t *testing.T) {
			var mu sync.Mutex
			var attempts []string
			upstream := newCodexWSFailoverServerScript(t, &attempts, &mu, func(account string, conn *websocket.Conn) {
				created, _ := codexWSTestSuccessFrames(account)
				if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(created)); errWrite != nil {
					t.Errorf("write websocket event: %v", errWrite)
					return
				}
				// The buffered frame is already in hand; the rejection only arrives once the budget is spent.
				time.Sleep(codexWSBudgetExceededDelay)
				if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(rejection.frame)); errWrite != nil {
					t.Errorf("write websocket event: %v", errWrite)
				}
			})
			defer upstream.Close()

			manager, exec := newCodexWSScopedManagerWithTimeout(t, true, codexWSShortBootstrapTimeout)
			registerCodexWSTestAuth(t, manager, "ws-budget-buffered-flagged", 100, upstream.URL, "account-flagged", cliproxyauth.RequestScopedActionContinueAndCooldown)
			registerCodexWSTestAuth(t, manager, "ws-budget-buffered-ok", 0, upstream.URL, "account-ok", "")

			const sessionID = "ws-request-scoped-spent-budget-buffered-frame"
			disconnectCh := exec.UpstreamDisconnectChan(sessionID)
			defer exec.CloseExecutionSession(sessionID)

			body, chunkErr := runCodexWSStream(t, manager, sessionID)
			if chunkErr == nil || !strings.Contains(chunkErr.Error(), "invalid_prompt") {
				t.Fatalf("the spent-budget buffered rejection must be delivered in-stream, got %v", chunkErr)
			}
			if events := codexWSStreamEvents(t, body); len(events) != 1 || events[0] != "response.created" {
				t.Fatalf("downstream events = %v, want the buffered [response.created]", events)
			}

			mu.Lock()
			gotAttempts := append([]string(nil), attempts...)
			mu.Unlock()
			if len(gotAttempts) != 1 || gotAttempts[0] != "account-flagged" {
				t.Fatalf("attempts = %v, want [account-flagged]: a released frame rules out a clean retry", gotAttempts)
			}

			select {
			case <-disconnectCh:
			case <-time.After(time.Second):
				t.Fatal("a spent-budget rejection after a buffered frame must still signal the downstream disconnect")
			}
		})
	}
}
