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

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

const usageLimitWebsocketProvider = "usage-limit-ws-fake"

// usageLimitWebsocketExecutor fails every request with err, after
// response.created when midStream is set.
type usageLimitWebsocketExecutor struct {
	err       error
	midStream bool
}

func (usageLimitWebsocketExecutor) Identifier() string { return usageLimitWebsocketProvider }

func (e usageLimitWebsocketExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, e.err
}

func (e usageLimitWebsocketExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	if !e.midStream {
		return nil, e.err
	}
	chunks := make(chan coreexecutor.StreamChunk, 2)
	chunks <- coreexecutor.StreamChunk{Payload: []byte(`{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","status":"in_progress"}}`)}
	chunks <- coreexecutor.StreamChunk{Err: e.err}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (usageLimitWebsocketExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (usageLimitWebsocketExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (usageLimitWebsocketExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

// websocketQuotaErr is an upstream 429 with a 3h reset; with exhausted it
// carries quota evidence for a 5-hour window.
type websocketQuotaErr struct{ exhausted bool }

func (e websocketQuotaErr) Error() string {
	return fmt.Sprintf(`{"error":{"exhausted":%t}}`, e.exhausted)
}
func (websocketQuotaErr) StatusCode() int                 { return http.StatusTooManyRequests }
func (websocketQuotaErr) RetryAfter() *time.Duration      { d := 3 * time.Hour; return &d }
func (e websocketQuotaErr) IsQuotaExhausted() bool        { return e.exhausted }
func (websocketQuotaErr) QuotaLimitWindowMinutes() int64  { return 300 }
func (websocketQuotaErr) QuotaResetAfter() *time.Duration { return nil }
func (websocketQuotaErr) IsCredentialScoped() bool        { return true }

type websocketTurn struct {
	mode      string
	exhausted bool
	// preCooled cools every credential before the turn; otherwise the
	// executor refuses during it, after response.created when midStream.
	preCooled bool
	midStream bool
}

// newUsageLimitWebsocketManager registers one credential (a started stream
// cannot move to another) or, before a stream starts, two.
func newUsageLimitWebsocketManager(t *testing.T, turn websocketTurn) (*coreauth.Manager, string) {
	t.Helper()
	coreauth.SetQuotaCooldownResponseMode(turn.mode)
	t.Cleanup(func() { coreauth.SetQuotaCooldownResponseMode("") })
	model := "usage-limit-ws-" + uuid.NewString()
	manager := coreauth.NewManager(nil, nil, nil)
	refusal := websocketQuotaErr{exhausted: turn.exhausted}
	manager.RegisterExecutor(usageLimitWebsocketExecutor{err: refusal, midStream: turn.midStream})
	authCount := 2
	if turn.midStream {
		authCount = 1
	}
	for i := 0; i < authCount; i++ {
		auth := &coreauth.Auth{ID: fmt.Sprintf("%s-%d", model, i), Provider: usageLimitWebsocketProvider, Status: coreauth.StatusActive}
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("Register() error = %v", errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, usageLimitWebsocketProvider, []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
		if turn.preCooled {
			retry := 3 * time.Hour
			manager.MarkResult(context.Background(), coreauth.Result{
				AuthID: auth.ID, Provider: usageLimitWebsocketProvider, Model: model,
				Error:      &coreauth.Error{HTTPStatus: http.StatusTooManyRequests, Message: refusal.Error()},
				RetryAfter: &retry, CredentialScope: true, QuotaExhausted: turn.exhausted, QuotaLimitWindowMinutes: 300,
			})
		}
	}
	return manager, model
}

// runResponsesWebsocketTurn returns the frames a client receives for one
// response.create before the socket closes.
func runResponsesWebsocketTurn(t *testing.T, turn websocketTurn) [][]byte {
	t.Helper()
	gin.SetMode(gin.TestMode)
	manager, model := newUsageLimitWebsocketManager(t, turn)
	router := gin.New()
	router.GET("/v1/responses", NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)).ResponsesWebsocket)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	conn, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
	if errDial != nil {
		t.Fatalf("dial websocket: %v", errDial)
	}
	defer func() { _ = conn.Close() }()
	create := fmt.Sprintf(`{"type":"response.create","model":%q,"input":[{"type":"message","role":"user","content":"ping"}]}`, model)
	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(create)); errWrite != nil {
		t.Fatalf("write create: %v", errWrite)
	}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	var frames [][]byte
	for {
		_, payload, errRead := conn.ReadMessage()
		if errRead != nil {
			var netErr interface{ Timeout() bool }
			if errors.As(errRead, &netErr) && netErr.Timeout() {
				t.Fatalf("websocket stayed open after the refusal; frames=%q", frames)
			}
			return frames
		}
		frames = append(frames, payload)
	}
}

func assertWebsocketUsageLimitEvent(t *testing.T, payload []byte) {
	t.Helper()
	wantReset := time.Now().Add(3 * time.Hour).Unix()
	if got := ojson.GetBytes(payload, "error.resets_at").Int(); got < wantReset-5 || got > wantReset+5 ||
		ojson.GetBytes(payload, "type").String() != "error" || ojson.GetBytes(payload, "status").Int() != http.StatusTooManyRequests ||
		ojson.GetBytes(payload, "error.type").String() != "usage_limit_reached" ||
		ojson.GetBytes(payload, "headers.X-Should-Retry").String() != "false" ||
		ojson.GetBytes(payload, "headers.X-Codex-Primary-Window-Minutes").String() != "300" ||
		ojson.GetBytes(payload, "headers.Retry-After").Exists() {
		t.Fatalf("event = %s, want a 429 usage_limit_reached error event with the usage-limit headers", payload)
	}
}

// The client gets the usage limit as an error event before the socket closes,
// instead of a silent close; the retryable mode and a pool that is only rate
// limited keep the silent close.
func TestResponsesWebsocketUsageLimit(t *testing.T) {
	for _, tc := range []struct {
		name      string
		turn      websocketTurn
		wantEvent bool
	}{
		{name: "already cooling", turn: websocketTurn{mode: "usage-limit", exhausted: true, preCooled: true}, wantEvent: true},
		{name: "refused during turn", turn: websocketTurn{mode: "usage-limit", exhausted: true}, wantEvent: true},
		{name: "refused after response started", turn: websocketTurn{mode: "usage-limit", exhausted: true, midStream: true}, wantEvent: true},
		{name: "retryable mode after response started", turn: websocketTurn{mode: "retryable", exhausted: true, midStream: true}},
		{name: "rate limited after response started", turn: websocketTurn{mode: "usage-limit", midStream: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frames := runResponsesWebsocketTurn(t, tc.turn)
			if tc.turn.midStream {
				if len(frames) == 0 || ojson.GetBytes(frames[0], "type").String() != "response.created" {
					t.Fatalf("frames = %q, want response.created first", frames)
				}
				frames = frames[1:]
			}
			if len(frames) != map[bool]int{false: 0, true: 1}[tc.wantEvent] {
				t.Fatalf("frames = %q, want usage-limit event %t", frames, tc.wantEvent)
			}
			if tc.wantEvent {
				assertWebsocketUsageLimitEvent(t, frames[0])
			}
		})
	}
}
