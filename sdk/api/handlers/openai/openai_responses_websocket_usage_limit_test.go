package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestShouldExposeResponsesUsageLimit(t *testing.T) {
	usageLimit := coreauth.NewUsageLimitModelCooldownError("gpt-6-astra", "codex", time.Now().Add(3*time.Hour), 300)
	if !shouldExposeResponsesUpstreamError(&interfaces.ErrorMessage{StatusCode: http.StatusTooManyRequests, Error: usageLimit}) {
		t.Fatalf("usage limit is not exposed")
	}
	cooldown := coreauth.NewModelCooldownError("gpt-6-astra", "codex", 3*time.Hour)
	if shouldExposeResponsesUpstreamError(&interfaces.ErrorMessage{StatusCode: http.StatusTooManyRequests, Error: cooldown}) {
		t.Fatalf("retryable cooldown is exposed")
	}
}

func TestResponsesWebsocketUsageLimitPayloadCarriesCodexHeaders(t *testing.T) {
	resetAt := time.Now().Add(3 * time.Hour)
	usageLimit := coreauth.NewUsageLimitModelCooldownError("gpt-6-astra", "codex", resetAt, 300)
	for name, errMsg := range map[string]*interfaces.ErrorMessage{
		"execution":   handlers.ExecutionErrorMessage(usageLimit),
		"wrapped":     {StatusCode: http.StatusTooManyRequests, Error: fmt.Errorf("select credential: %w", usageLimit)},
		"bare status": {StatusCode: http.StatusTooManyRequests, Error: usageLimit},
	} {
		t.Run(name, func(t *testing.T) {
			payload, errBuild := buildResponsesWebsocketErrorPayload(errMsg)
			if errBuild != nil {
				t.Fatalf("buildResponsesWebsocketErrorPayload() error = %v", errBuild)
			}
			if got := ojson.GetBytes(payload, "status").Int(); got != http.StatusTooManyRequests {
				t.Fatalf("status = %d, want 429: %s", got, payload)
			}
			headers := ojson.GetBytes(payload, "headers")
			if got := headers.Get("X-Codex-Primary-Used-Percent").String(); got != "100" {
				t.Fatalf("headers.X-Codex-Primary-Used-Percent = %q, want 100: %s", got, payload)
			}
			if got := headers.Get("X-Codex-Primary-Reset-At").String(); got != strconv.FormatInt(resetAt.Unix(), 10) {
				t.Fatalf("headers.X-Codex-Primary-Reset-At = %q, want %d: %s", got, resetAt.Unix(), payload)
			}
			if headers.Get("Retry-After").Exists() {
				t.Fatalf("headers.Retry-After is set: %s", payload)
			}
			if got := ojson.GetBytes(payload, "error.type").String(); got != "usage_limit_reached" {
				t.Fatalf("error.type = %q, want usage_limit_reached: %s", got, payload)
			}
			if got := ojson.GetBytes(payload, "error.resets_at").Int(); got != resetAt.Unix() {
				t.Fatalf("error.resets_at = %d, want %d: %s", got, resetAt.Unix(), payload)
			}
		})
	}
}

type usageLimitWebsocketExecutor struct{}

func (usageLimitWebsocketExecutor) Identifier() string { return "codex" }

func (usageLimitWebsocketExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("no credential should be used")
}

func (usageLimitWebsocketExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, errors.New("no credential should be used")
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

// runRefusedResponsesWebsocketTurn cools every Codex credential down with
// lastErrorBody and returns the frames a client receives for one create.
func runRefusedResponsesWebsocketTurn(t *testing.T, lastErrorBody string) [][]byte {
	t.Helper()
	gin.SetMode(gin.TestMode)
	// The refusals under test need cooling on, which is the default.
	coreauth.SetQuotaCooldownDisabled(false)
	ctx := context.Background()
	model := "usage-limit-ws-" + uuid.NewString()
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(usageLimitWebsocketExecutor{})
	retry := 3 * time.Hour
	for _, suffix := range []string{"pro", "team"} {
		auth := &coreauth.Auth{ID: model + "-" + suffix, Provider: "codex", Status: coreauth.StatusActive}
		if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
			t.Fatalf("Register() error = %v", errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
		manager.MarkResult(ctx, coreauth.Result{
			AuthID:          auth.ID,
			Provider:        "codex",
			Model:           model,
			Error:           &coreauth.Error{HTTPStatus: http.StatusTooManyRequests, Message: lastErrorBody},
			RetryAfter:      &retry,
			CredentialScope: true,
		})
	}

	h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))
	router := gin.New()
	router.GET("/v1/responses", h.ResponsesWebsocket)
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
			return frames
		}
		frames = append(frames, payload)
	}
}

func TestResponsesWebsocketSendsUsageLimitWhenEveryCredentialIsOut(t *testing.T) {
	resetAt := time.Now().Add(3 * time.Hour)
	body := fmt.Sprintf(`{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","resets_at":%d,"limit_window_minutes":300}}`, resetAt.Unix())
	frames := runRefusedResponsesWebsocketTurn(t, body)
	if len(frames) != 1 {
		t.Fatalf("frames = %d (%q), want one error event", len(frames), frames)
	}
	payload := frames[0]
	if got := ojson.GetBytes(payload, "type").String(); got != "error" {
		t.Fatalf("type = %q, want error: %s", got, payload)
	}
	if got := ojson.GetBytes(payload, "error.type").String(); got != "usage_limit_reached" {
		t.Fatalf("error.type = %q, want usage_limit_reached: %s", got, payload)
	}
	if got := ojson.GetBytes(payload, "headers.X-Codex-Primary-Used-Percent").String(); got != "100" {
		t.Fatalf("headers.X-Codex-Primary-Used-Percent = %q, want 100: %s", got, payload)
	}
}

func TestResponsesWebsocketKeepsRetryableCooldownSilent(t *testing.T) {
	frames := runRefusedResponsesWebsocketTurn(t, `{"error":{"type":"rate_limit_exceeded","message":"slow down"}}`)
	if len(frames) != 0 {
		t.Fatalf("frames = %q, want the connection closed without an event", frames)
	}
}
