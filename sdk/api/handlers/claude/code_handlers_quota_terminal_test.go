package claude

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// quotaExhaustedUpstreamError is an upstream 429 explicitly marked as quota
// exhaustion, as Codex usage_limit_reached is.
type quotaExhaustedUpstreamError struct {
	issue7521UpstreamError
}

func (e *quotaExhaustedUpstreamError) IsQuotaExhausted() bool { return true }

// The request that exhausts the last credentials gets the final usage limit:
// no Retry-After (neither the pool deadline nor the passthrough upstream
// header), X-Should-Retry: false, and the reset time in the message.
func TestClaudeLastCredentialQuotaExhaustionIsFinalUsageLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const model = "quota-terminal-claude-route-model"
	hints := map[string]time.Duration{"-a": 3 * time.Hour, "-b": 4 * time.Hour}
	makeErr := func(authID string) error {
		hint := hints[authID[len(authID)-2:]]
		return &quotaExhaustedUpstreamError{issue7521UpstreamError{
			status:     http.StatusTooManyRequests,
			body:       `{"type":"error","error":{"type":"usage_limit_reached","message":"The usage limit has been reached."}}`,
			retryAfter: hint,
			headers:    http.Header{"Retry-After": {strconv.Itoa(int(hint.Seconds()))}},
		}}
	}

	for _, tc := range []struct {
		name      string
		path      string
		streaming bool
	}{
		{name: "messages_non_stream", path: "/v1/messages"},
		{name: "messages_stream", path: "/v1/messages", streaming: true},
		{name: "count_tokens", path: "/v1/messages/count_tokens"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := coreauth.NewManager(nil, nil, nil)
			manager.RegisterExecutor(&issue7521ClaudeExecutor{
				executeFn: func(_ context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
					return coreexecutor.Response{}, makeErr(auth.ID)
				},
				streamFn: func(_ context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
					chunks := make(chan coreexecutor.StreamChunk, 1)
					chunks <- coreexecutor.StreamChunk{Err: makeErr(auth.ID)}
					close(chunks)
					return &coreexecutor.StreamResult{Chunks: chunks}, nil
				},
				countFn: func(_ context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
					return coreexecutor.Response{}, makeErr(auth.ID)
				},
			})

			reg := registry.GetGlobalRegistry()
			for suffix := range hints {
				authID := "quota-terminal-route-" + tc.name + suffix
				auth := &coreauth.Auth{
					ID:         authID,
					Provider:   "claude",
					Status:     coreauth.StatusActive,
					Attributes: map[string]string{"api_key": "key" + suffix},
				}
				if _, errReg := manager.Register(context.Background(), auth); errReg != nil {
					t.Fatalf("register auth: %v", errReg)
				}
				reg.RegisterClient(authID, "claude", []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() {
					reg.UnregisterClient(authID)
				})
			}

			handler := NewClaudeCodeAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{PassthroughHeaders: true}, manager))
			router := gin.New()
			router.POST("/v1/messages", handler.ClaudeMessages)
			router.POST("/v1/messages/count_tokens", handler.ClaudeCountTokens)

			body := `{"model":"` + model + `","messages":[{"role":"user","content":"hi"}],"stream":` + strconv.FormatBool(tc.streaming) + `}`
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusTooManyRequests {
				t.Fatalf("status = %d, want 429; body=%s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Retry-After"); got != "" {
				t.Fatalf("Retry-After = %q, want absent for an exhausted pool", got)
			}
			if got := rec.Header().Get("X-Should-Retry"); got != "false" {
				t.Fatalf("X-Should-Retry = %q, want false", got)
			}
			if gotType := ojson.GetBytesNoCopy(rec.Body.Bytes(), "error.type").String(); gotType != "rate_limit_error" {
				t.Fatalf("error.type = %q, want rate_limit_error; body=%s", gotType, rec.Body.String())
			}
			gotMsg := ojson.GetBytesNoCopy(rec.Body.Bytes(), "error.message").String()
			if !strings.HasPrefix(gotMsg, "The usage limit has been reached. (reset after 2h59m") && gotMsg != "The usage limit has been reached. (reset after 3h0m0s)" {
				t.Fatalf("error.message = %q, want the upstream message with the ~3h pool reset", gotMsg)
			}
		})
	}
}
