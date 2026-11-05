package claude

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	executor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type cooldownHeaderError struct{ header http.Header }

func (e *cooldownHeaderError) Error() string { return "synthetic quota rejection" }
func (e *cooldownHeaderError) ResponseBody() []byte {
	return []byte(`{"error":{"type":"rate_limit_error","message":"synthetic quota rejection"}}`)
}
func (e *cooldownHeaderError) StatusCode() int            { return 429 }
func (e *cooldownHeaderError) RetryAfter() *time.Duration { d := time.Hour; return &d }
func (e *cooldownHeaderError) Headers() http.Header       { return e.header }

type cooldownHeaderExecutor struct {
	coreauth.ProviderExecutor
	cause *cooldownHeaderError
}

func (*cooldownHeaderExecutor) Identifier() string { return "claude" }
func (e *cooldownHeaderExecutor) Execute(ctx context.Context, _ *coreauth.Auth, _ executor.Request, _ executor.Options) (executor.Response, error) {
	executor.MarkUpstreamAttempt(ctx)
	return executor.Response{}, e.cause
}
func (e *cooldownHeaderExecutor) ExecuteStream(ctx context.Context, a *coreauth.Auth, r executor.Request, o executor.Options) (*executor.StreamResult, error) {
	_, err := e.Execute(ctx, a, r, o)
	ch := make(chan executor.StreamChunk, 1)
	ch <- executor.StreamChunk{Err: err}
	close(ch)
	return &executor.StreamResult{Headers: e.cause.header.Clone(), Chunks: ch}, nil
}

func TestProxyCooldownPreservesComposedErrorHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stream := range []bool{false, true} {
		for _, passthrough := range []bool{false, true} {
			t.Run(map[bool]string{false: "execute", true: "bootstrap"}[stream]+map[bool]string{false: "/disabled", true: "/enabled"}[passthrough], func(t *testing.T) {
				manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
				manager.SetRetryConfig(0, 0, 0)
				cause := &cooldownHeaderError{http.Header{"X-Request-Id": {"synthetic-bootstrap"}, "Retry-After": {"3600"}}}
				manager.RegisterExecutor(&cooldownHeaderExecutor{cause: cause})
				id, model := t.Name(), "claude-opus-5-5"
				registry.GetGlobalRegistry().RegisterClient(id, "claude", []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
				if _, err := manager.Register(context.Background(), &coreauth.Auth{ID: id, Provider: "claude", Status: coreauth.StatusActive}); err != nil {
					t.Fatal(err)
				}
				var err error
				if stream {
					var result *executor.StreamResult
					result, err = manager.ExecuteStream(context.Background(), []string{"claude"}, executor.Request{Model: model}, executor.Options{})
					if err != nil || result == nil {
						t.Fatalf("stream: %v", err)
					}
					if result.Headers.Get("X-Request-Id") != "synthetic-bootstrap" {
						t.Fatal("manager stream headers lost")
					}
					err = (<-result.Chunks).Err
				} else {
					_, err = manager.Execute(context.Background(), []string{"claude"}, executor.Request{Model: model}, executor.Options{})
				}
				if _, ok := coreauth.ProxyCooldownNextReevaluation(err); !ok {
					t.Fatalf("no concrete cooldown wrapper: %T", err)
				}
				msg := handlers.ExecutionErrorMessage(err)
				if msg.Addon.Get("X-Request-Id") != "synthetic-bootstrap" || msg.Addon.Get("Retry-After") != "3600" {
					t.Fatalf("converted addon lost: %#v", msg.Addon)
				}
				// Both the wrapper and converter must return detached header maps.
				msg.Addon.Set("X-Request-Id", "mutated")
				provider := err.(interface{ Headers() http.Header })
				detached := provider.Headers()
				detached.Set("X-Request-Id", "also-mutated")
				msg = handlers.ExecutionErrorMessage(err)
				if cause.header.Get("X-Request-Id") != "synthetic-bootstrap" || msg.Addon.Get("X-Request-Id") != "synthetic-bootstrap" {
					t.Fatal("headers aliased upstream cause")
				}
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
				handler := NewClaudeCodeAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{PassthroughHeaders: passthrough, APIKeys: []string{"synthetic-downstream-token"}}, manager))
				handler.WriteErrorResponse(c, msg)
				if recorder.Code != 429 {
					t.Fatalf("status=%d", recorder.Code)
				}
				want := ""
				if passthrough {
					want = "synthetic-bootstrap"
				}
				if recorder.Header().Get("X-Request-Id") != want {
					t.Fatalf("passthrough=%v header=%q", passthrough, recorder.Header().Get("X-Request-Id"))
				}
				if passthrough && recorder.Header().Get("Retry-After") != "3600" {
					t.Fatal("original retry-after lost")
				}
				// Exercise the actual JSON and SSE serializers with a concrete
				// manager-generated summary, including unchanged default output.
				for _, transport := range []string{"json", "sse"} {
					for _, opt := range []string{"signed-v1", "", "unsupported", "missing-key"} {
						rec := httptest.NewRecorder()
						ctx, _ := gin.CreateTestContext(rec)
						ctx.Request = httptest.NewRequest("POST", "/v1/messages", nil)
						ctx.Request.Header.Set("X-CLIProxyAPI-Cooldown-Metadata", opt)
						if opt != "missing-key" {
							ctx.Set("userApiKey", "synthetic-downstream-token")
							ctx.Set("accessProvider", "config-inline")
						}
						if opt == "missing-key" {
							ctx.Request.Header.Set("X-CLIProxyAPI-Cooldown-Metadata", "signed-v1")
						}
						if transport == "json" {
							handler.WriteErrorResponse(ctx, msg)
						} else {
							data := make(chan []byte)
							errs := make(chan *interfaces.ErrorMessage, 1)
							errs <- msg
							close(errs)
							handler.forwardClaudeStream(ctx, ctx.Writer, func(error) {}, data, errs)
						}
						payload := rec.Body.String()
						if transport == "sse" {
							start := strings.Index(payload, "data: ")
							if start < 0 {
								t.Fatalf("missing SSE data: %q", payload)
							}
							payload = strings.TrimSpace(payload[start+len("data: "):])
						}
						var body claudeErrorResponse
						if err := json.Unmarshal([]byte(payload), &body); err != nil {
							t.Fatal(err)
						}
						expected := handler.toClaudeError(msg)
						if opt == "signed-v1" {
							deadline, _ := coreauth.ProxyCooldownNextReevaluation(msg.Error)
							expected.Error.Message = proxyCooldownMarker(deadline, "synthetic-downstream-token", time.Now()) + expected.Error.Message
						}
						if body != expected {
							t.Fatalf("%s opt=%q: got %#v want %#v", transport, opt, body, expected)
						}
						if transport == "json" && rec.Code != 429 {
							t.Fatalf("%s status=%d", transport, rec.Code)
						}
					}
				}

			})
		}
	}
}

// WriteProxyCooldownTestResponse exposes only the real serializers to the
// external-package routed middleware test; it is not part of the SDK API.
func WriteProxyCooldownTestResponse(handler *ClaudeCodeAPIHandler, c *gin.Context, msg *interfaces.ErrorMessage, stream bool) {
	if !stream {
		handler.WriteErrorResponse(c, msg)
		return
	}
	errs := make(chan *interfaces.ErrorMessage, 1)
	errs <- msg
	close(errs)
	handler.forwardClaudeStream(c, c.Writer, func(error) {}, make(chan []byte), errs)
}
