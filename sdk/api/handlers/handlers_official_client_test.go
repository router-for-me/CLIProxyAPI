package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// officialClientCountingExecutor records how often the upstream would have been reached.
type officialClientCountingExecutor struct {
	provider string
	calls    *atomic.Int32
}

func (e officialClientCountingExecutor) Identifier() string { return e.provider }

func (e officialClientCountingExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	e.calls.Add(1)
	return coreexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (e officialClientCountingExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.calls.Add(1)
	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: []byte(`data: {"ok":true}`)}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e officialClientCountingExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e officialClientCountingExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	e.calls.Add(1)
	return coreexecutor.Response{Payload: []byte(`{"input_tokens":1}`)}, nil
}

func (e officialClientCountingExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

// drainOfficialClientStream consumes a stream and returns its terminal error, if any.
// A stream rejected before execution has a nil data channel.
func drainOfficialClientStream(dataChan <-chan []byte, errChan <-chan *interfaces.ErrorMessage) *interfaces.ErrorMessage {
	if dataChan != nil {
		for range dataChan {
		}
	}
	if errChan == nil {
		return nil
	}
	return <-errChan
}

func requestContextWithUserAgent(userAgent string) context.Context {
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	if userAgent != "" {
		ginCtx.Request.Header.Set("User-Agent", userAgent)
	}
	return context.WithValue(context.Background(), "gin", ginCtx)
}

// Requests reach the selector with their inbound headers, so a provider restricted by
// oauth.official-client-only must never be used (and the upstream never contacted) for
// a client that is not the official Codex CLI.
func TestOfficialClientOnlyEnforcedThroughHandlers(t *testing.T) {
	const model = "official-client-e2e-model"
	var calls atomic.Int32

	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	manager.RegisterExecutor(officialClientCountingExecutor{provider: "codex", calls: &calls})
	auth := &coreauth.Auth{
		ID:       "official-client-e2e-codex",
		Provider: "codex",
		Status:   coreauth.StatusActive,
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: model, Created: time.Now().Unix()}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

	var restricted atomic.Bool
	restricted.Store(true)
	coreauth.SetOfficialClientRequirement(func(provider string) bool { return provider == "codex" && restricted.Load() })
	coreauth.SetOfficialClientVerifier(helps.NewOfficialClientVerifier(nil))
	t.Cleanup(func() {
		coreauth.SetOfficialClientRequirement(nil)
		coreauth.SetOfficialClientVerifier(nil)
	})

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	body := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`)
	const codexUA = "codex_cli_rs/0.50.0 (Ubuntu 24.4.0; x86_64) xterm-256color"

	requireForbidden := func(t *testing.T, errMsg *interfaces.ErrorMessage) {
		t.Helper()
		if errMsg == nil {
			t.Fatal("request from a non official client was served")
		}
		if errMsg.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want %d (err=%v)", errMsg.StatusCode, http.StatusForbidden, errMsg.Error)
		}
	}

	for _, rejected := range []string{"", "curl/8.7.1", "python-requests/2.32.0", "claude-cli/2.1.280 (external, cli)"} {
		t.Run("rejects UA "+rejected, func(t *testing.T) {
			before := calls.Load()
			ctx := requestContextWithUserAgent(rejected)

			_, _, errMsg := handler.ExecuteWithAuthManager(ctx, "openai", model, body, "")
			requireForbidden(t, errMsg)

			dataChan, _, errChan := handler.ExecuteStreamWithAuthManager(ctx, "openai", model, body, "")
			requireForbidden(t, drainOfficialClientStream(dataChan, errChan))

			_, _, errMsg = handler.ExecuteCountWithAuthManager(ctx, "openai", model, body, "")
			requireForbidden(t, errMsg)

			if got := calls.Load(); got != before {
				t.Fatalf("upstream executor reached %d times for a rejected client", got-before)
			}
		})
	}

	t.Run("serves official codex client", func(t *testing.T) {
		before := calls.Load()
		ctx := requestContextWithUserAgent(codexUA)

		if _, _, errMsg := handler.ExecuteWithAuthManager(ctx, "openai", model, body, ""); errMsg != nil {
			t.Fatalf("execute: %v", errMsg.Error)
		}
		dataChan, _, errChan := handler.ExecuteStreamWithAuthManager(ctx, "openai", model, body, "")
		if errMsg := drainOfficialClientStream(dataChan, errChan); errMsg != nil {
			t.Fatalf("stream: %v", errMsg.Error)
		}
		if got := calls.Load() - before; got != 2 {
			t.Fatalf("executor calls = %d, want 2", got)
		}
	})

	t.Run("switching the restriction off serves any client", func(t *testing.T) {
		restricted.Store(false)
		if _, _, errMsg := handler.ExecuteWithAuthManager(requestContextWithUserAgent("curl/8.7.1"), "openai", model, body, ""); errMsg != nil {
			t.Fatalf("unrestricted provider rejected a client: %v", errMsg.Error)
		}
	})
}
