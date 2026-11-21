package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type issue7740HandlerStreamExecutor struct {
	id       string
	calls    atomic.Int32
	streamFn func(ctx context.Context, auth *coreauth.Auth) (*coreexecutor.StreamResult, error)
}

func (e *issue7740HandlerStreamExecutor) Identifier() string { return e.id }

func (e *issue7740HandlerStreamExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *issue7740HandlerStreamExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.calls.Add(1)
	coreexecutor.MarkUpstreamAttempt(ctx)
	return e.streamFn(ctx, auth)
}

func (e *issue7740HandlerStreamExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *issue7740HandlerStreamExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *issue7740HandlerStreamExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestIssue7740_ExecuteStreamWithAuthManager_PreservesBootstrapHeadersWithoutReplay(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("rule_matched_stop_bootstrap_writes_upstream_headers_without_bootstrap_retry", func(t *testing.T) {
		model := "claude-sonnet-4-6-7740-http-stop-" + uuid.NewString()
		manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
		manager.SetRetryConfig(2, 0, 2)
		manager.SetConfig(&internalconfig.Config{
			OAuthRequestScopedErrors: map[string][]internalconfig.RequestScopedErrorRule{
				"antigravity": {
					{
						Status: http.StatusTooManyRequests,
						Match:  []string{"quota_stop_http_7740"},
						Action: "stop",
					},
				},
			},
		})
		for _, suffix := range []string{"1", "2"} {
			id := "ag-7740-http-stop-" + suffix + "-" + uuid.NewString()
			if _, errReg := manager.Register(context.Background(), &coreauth.Auth{
				ID:         id,
				Provider:   "antigravity",
				Status:     coreauth.StatusActive,
				Attributes: map[string]string{coreauth.AttributeAuthKind: coreauth.AuthKindOAuth},
			}); errReg != nil {
				t.Fatalf("Register(%s): %v", id, errReg)
			}
			registry.GetGlobalRegistry().RegisterClient(id, "antigravity", []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
		}

		exec := &issue7740HandlerStreamExecutor{
			id: "antigravity",
			streamFn: func(_ context.Context, _ *coreauth.Auth) (*coreexecutor.StreamResult, error) {
				ch := make(chan coreexecutor.StreamChunk, 1)
				ch <- coreexecutor.StreamChunk{Err: &coreauth.Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota_stop_http_7740"}}
				close(ch)
				return &coreexecutor.StreamResult{
					Headers: http.Header{
						"Retry-After":      []string{"9"},
						"X-Upstream-Trace": []string{"trace-stop-7740"},
					},
					Chunks: ch,
				}, nil
			},
		}
		manager.RegisterExecutor(exec)

		handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{
			PassthroughHeaders: true,
			Streaming:          sdkconfig.StreamingConfig{BootstrapRetries: 2},
		}, manager)

		dataChan, _, errChan := handler.ExecuteStreamWithAuthManager(context.Background(), "openai-response", model, []byte(`{"model":"`+model+`"}`), "")
		if dataChan != nil {
			for range dataChan {
			}
		}
		var errMsg *interfaces.ErrorMessage
		for msg := range errChan {
			if msg != nil {
				errMsg = msg
			}
		}
		if errMsg == nil {
			t.Fatal("expected error message, got nil")
		}
		if got := exec.calls.Load(); got != 1 {
			t.Fatalf("upstream ExecuteStream calls = %d, want 1 (BootstrapRetries must not replay stopped request)", got)
		}

		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		handler.WriteErrorResponse(c, errMsg)

		if recorder.Code != http.StatusTooManyRequests {
			t.Fatalf("HTTP status = %d, want 429", recorder.Code)
		}
		if got := recorder.Header().Get("Retry-After"); got != "9" {
			t.Fatalf("Retry-After = %q, want %q", got, "9")
		}
		if got := recorder.Header().Get("X-Upstream-Trace"); got != "trace-stop-7740" {
			t.Fatalf("X-Upstream-Trace = %q, want %q", got, "trace-stop-7740")
		}
	})

	t.Run("terminated_bootstrap_writes_stream_and_termination_headers_with_termination_precedence", func(t *testing.T) {
		for _, passthrough := range []bool{true, false} {
			name := "passthrough=false"
			if passthrough {
				name = "passthrough=true"
			}
			t.Run(name, func(t *testing.T) {
				model := "claude-sonnet-4-6-7740-http-term-" + uuid.NewString()
				manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
				manager.SetRetryConfig(2, 0, 2)
				for _, suffix := range []string{"1", "2"} {
					id := "ag-7740-http-term-" + suffix + "-" + uuid.NewString()
					if _, errReg := manager.Register(context.Background(), &coreauth.Auth{
						ID:       id,
						Provider: "antigravity",
						Status:   coreauth.StatusActive,
					}); errReg != nil {
						t.Fatalf("Register(%s): %v", id, errReg)
					}
					registry.GetGlobalRegistry().RegisterClient(id, "antigravity", []*registry.ModelInfo{{ID: model}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
				}

				exec := &issue7740HandlerStreamExecutor{
					id: "antigravity",
					streamFn: func(_ context.Context, _ *coreauth.Auth) (*coreexecutor.StreamResult, error) {
						ch := make(chan coreexecutor.StreamChunk, 1)
						ch <- coreexecutor.StreamChunk{
							Err: &coreexecutor.RequestTerminatedError{
								HTTPStatus: http.StatusTooManyRequests,
								Header: http.Header{
									"X-Term":    []string{"term-7740"},
									"X-Overlap": []string{"term-wins"},
								},
								Body: []byte(`{"detail":"terminated"}`),
							},
						}
						close(ch)
						return &coreexecutor.StreamResult{
							Headers: http.Header{
								"Retry-After":      []string{"13"},
								"X-Upstream-Trace": []string{"stream-trace-7740"},
								"X-Overlap":        []string{"stream-loses"},
							},
							Chunks: ch,
						}, nil
					},
				}
				manager.RegisterExecutor(exec)

				handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{
					PassthroughHeaders: passthrough,
					Streaming:          sdkconfig.StreamingConfig{BootstrapRetries: 2},
				}, manager)

				dataChan, _, errChan := handler.ExecuteStreamWithAuthManager(context.Background(), "openai-response", model, []byte(`{"model":"`+model+`"}`), "")
				if dataChan != nil {
					for range dataChan {
					}
				}
				var errMsg *interfaces.ErrorMessage
				for msg := range errChan {
					if msg != nil {
						errMsg = msg
					}
				}
				if errMsg == nil {
					t.Fatal("expected error message, got nil")
				}
				if got := exec.calls.Load(); got != 1 {
					t.Fatalf("upstream ExecuteStream calls = %d, want 1", got)
				}

				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
				handler.WriteErrorResponse(c, errMsg)

				if recorder.Code != http.StatusTooManyRequests {
					t.Fatalf("HTTP status = %d, want 429", recorder.Code)
				}
				if recorder.Body.String() != `{"detail":"terminated"}` {
					t.Fatalf("body = %q, want %q", recorder.Body.String(), `{"detail":"terminated"}`)
				}
				if got := recorder.Header().Get("X-Term"); got != "term-7740" {
					t.Fatalf("X-Term = %q, want %q", got, "term-7740")
				}
				if got := recorder.Header().Get("X-Overlap"); got != "term-wins" {
					t.Fatalf("X-Overlap = %q, want %q (termination header must take precedence)", got, "term-wins")
				}
				wantTrace := ""
				wantRetryAfter := ""
				if passthrough {
					wantTrace = "stream-trace-7740"
					wantRetryAfter = "13"
				}
				if got := recorder.Header().Get("X-Upstream-Trace"); got != wantTrace {
					t.Fatalf("X-Upstream-Trace = %q, want %q", got, wantTrace)
				}
				if got := recorder.Header().Get("Retry-After"); got != wantRetryAfter {
					t.Fatalf("Retry-After = %q, want %q", got, wantRetryAfter)
				}
			})
		}
	})
}
