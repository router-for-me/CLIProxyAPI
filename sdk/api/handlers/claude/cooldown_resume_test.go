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

// Exercise the real routes and manager, including the first request that
// exhausts the last account and subsequent requests during the cooldown.
func TestClaudeCooldownResumeRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { coreauth.SetModelCooldownStatusCode(0) })
	for _, configured := range []int{0, 529} {
		t.Run(strconv.Itoa(configured), func(t *testing.T) {
			coreauth.SetModelCooldownStatusCode(configured)
			for _, streaming := range []bool{false, true} {
				for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens"} {
					if streaming && strings.HasSuffix(path, "count_tokens") {
						continue
					}
					t.Run(path+"/stream="+strconv.FormatBool(streaming), func(t *testing.T) {
						manager := coreauth.NewManager(nil, nil, nil)
						manager.SetRetryConfig(0, 0, 0)
						attempts := 0
						quotaError := func() error {
							attempts++
							return &issue7521UpstreamError{
								status:     http.StatusTooManyRequests,
								body:       `{"type":"error","error":{"type":"rate_limit_error","message":"Five-hour quota exhausted"}}`,
								retryAfter: time.Hour,
								headers:    http.Header{"Retry-After": {"3600"}},
							}
						}
						manager.RegisterExecutor(&issue7521ClaudeExecutor{
							executeFn: func(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
								return coreexecutor.Response{}, quotaError()
							},
							streamFn: func(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
								chunks := make(chan coreexecutor.StreamChunk, 1)
								chunks <- coreexecutor.StreamChunk{Err: quotaError()}
								close(chunks)
								return &coreexecutor.StreamResult{Chunks: chunks}, nil
							},
							countFn: func(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
								return coreexecutor.Response{}, quotaError()
							},
						})
						const model = "claude-cooldown-resume-test"
						for _, id := range []string{"resume-account-a", "resume-account-b"} {
							_, err := manager.Register(context.Background(), &coreauth.Auth{
								ID: id, Provider: "claude", Status: coreauth.StatusActive,
								Attributes: map[string]string{"api_key": "test-only"},
							})
							if err != nil {
								t.Fatal(err)
							}
							registry.GetGlobalRegistry().RegisterClient(id, "claude", []*registry.ModelInfo{{ID: model}})
							t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
						}
						// Passthrough must not restore the upstream rate_limit_error
						// type or lose the trusted cooldown header in 529 mode.
						handler := NewClaudeCodeAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{PassthroughHeaders: true}, manager))
						router := gin.New()
						router.POST("/v1/messages", handler.ClaudeMessages)
						router.POST("/v1/messages/count_tokens", handler.ClaudeCountTokens)
						for request := 0; request < 2; request++ {
							payload := `{"model":"` + model + `","messages":[{"role":"user","content":"hello"}],"stream":` + strconv.FormatBool(streaming) + `}`
							req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(payload))
							req.Header.Set("Content-Type", "application/json")
							rec := httptest.NewRecorder()
							router.ServeHTTP(rec, req)
							wantStatus, wantType := http.StatusTooManyRequests, "rate_limit_error"
							if configured == 529 {
								wantStatus, wantType = 529, "overloaded_error"
							}
							if rec.Code != wantStatus || ojson.GetBytesNoCopy(rec.Body.Bytes(), "error.type").String() != wantType {
								t.Fatalf("request %d: status=%d body=%s; want %d %s", request, rec.Code, rec.Body.String(), wantStatus, wantType)
							}
							seconds, err := strconv.Atoi(rec.Header().Get("Retry-After"))
							if err != nil || seconds < 3590 || seconds > 3600 {
								t.Fatalf("request %d: Retry-After=%q, want live pool reset", request, rec.Header().Get("Retry-After"))
							}
							if attempts != 2 {
								t.Fatalf("request %d: %d upstream calls; want exactly two account attempts and no calls during cooldown", request, attempts)
							}
						}
					})
				}
			}
		})
	}
}
