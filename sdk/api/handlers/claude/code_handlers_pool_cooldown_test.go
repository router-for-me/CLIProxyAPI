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

type issue7521UpstreamError struct {
	status     int
	body       string
	retryAfter time.Duration
	headers    http.Header
}

func (e *issue7521UpstreamError) Error() string   { return e.body }
func (e *issue7521UpstreamError) StatusCode() int { return e.status }
func (e *issue7521UpstreamError) RetryAfter() *time.Duration {
	d := e.retryAfter
	return &d
}
func (e *issue7521UpstreamError) Headers() http.Header {
	if e.headers == nil {
		return nil
	}
	return e.headers.Clone()
}

type issue7521ClaudeExecutor struct {
	executeFn func(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error)
	streamFn  func(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error)
	countFn   func(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error)
}

func (e *issue7521ClaudeExecutor) Identifier() string { return "claude" }

func (e *issue7521ClaudeExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	coreexecutor.MarkUpstreamAttempt(ctx)
	return e.executeFn(ctx, auth, req, opts)
}

func (e *issue7521ClaudeExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	coreexecutor.MarkUpstreamAttempt(ctx)
	return e.streamFn(ctx, auth, req, opts)
}

func (e *issue7521ClaudeExecutor) CountTokens(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	coreexecutor.MarkUpstreamAttempt(ctx)
	return e.countFn(ctx, auth, req, opts)
}

func (e *issue7521ClaudeExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *issue7521ClaudeExecutor) HttpRequest(_ context.Context, _ *coreauth.Auth, _ *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestClaudeLastCredential429AppendsPoolResetTime(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const model = "issue-7521-claude-route-model"
	shortHint := 95 * time.Minute
	longHint := 3 * time.Hour

	makeErr := func(authID string) error {
		if strings.HasSuffix(authID, "-a") {
			return &issue7521UpstreamError{
				status:     http.StatusTooManyRequests,
				body:       `{"type":"error","error":{"type":"rate_limit_error","message":"First account rate limited."}}`,
				retryAfter: shortHint,
				headers:    http.Header{"Retry-After": {"5700"}},
			}
		}
		return &issue7521UpstreamError{
			status:     http.StatusTooManyRequests,
			body:       `{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed your account's monthly spend limit. Please try again later."}}`,
			retryAfter: longHint,
			headers:    http.Header{"Retry-After": {"10800"}, "X-Upstream-Trace": {"trace-b"}},
		}
	}

	for _, tc := range []struct {
		name      string
		path      string
		streaming bool
	}{
		{name: "messages_non_stream", path: "/v1/messages", streaming: false},
		{name: "messages_stream", path: "/v1/messages", streaming: true},
		{name: "count_tokens", path: "/v1/messages/count_tokens", streaming: false},
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
			for _, suffix := range []string{"-a", "-b"} {
				authID := "issue-7521-route-" + tc.name + suffix
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
			seconds, errParse := strconv.ParseInt(rec.Header().Get("Retry-After"), 10, 64)
			if errParse != nil || seconds <= 0 {
				t.Fatalf("Retry-After = %q, want positive integer seconds", rec.Header().Get("Retry-After"))
			}
			if seconds > int64((shortHint + 10*time.Second).Seconds()) {
				t.Fatalf("Retry-After = %d, want pool deadline near %d rather than last attempt 10800", seconds, int64(shortHint.Seconds()))
			}
			if gotTrace := rec.Header().Get("X-Upstream-Trace"); gotTrace != "trace-b" {
				t.Fatalf("X-Upstream-Trace = %q, want trace-b preserved under passthrough-headers", gotTrace)
			}
			if gotType := ojson.GetBytesNoCopy(rec.Body.Bytes(), "error.type").String(); gotType != "rate_limit_error" {
				t.Fatalf("error.type = %q, want rate_limit_error", gotType)
			}
			wantDelay := (time.Duration(seconds) * time.Second).String()
			wantMessage := "This request would exceed your account's monthly spend limit. Please try again later. (reset after " + wantDelay + ")"
			if gotMsg := ojson.GetBytesNoCopy(rec.Body.Bytes(), "error.message").String(); gotMsg != wantMessage {
				t.Fatalf("error.message = %q, want %q", gotMsg, wantMessage)
			}
		})
	}
}
