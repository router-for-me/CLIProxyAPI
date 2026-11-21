package openai

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// Model cooldown forces entry through the real credits fallback. Home mode would
// skip that fallback entirely, so a Home dispatcher cannot exercise this bug.
func TestHTTPSubmissionAntigravityCreditsAliasPool(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, refusal := range []bool{false, true} {
			const credentials = 1
			t.Run(fmt.Sprintf("stream=%t/refusal=%t", stream, refusal), func(t *testing.T) {
				var submissions atomic.Int32
				var modelsMu sync.Mutex
				var models []string
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, errRead := io.ReadAll(r.Body)
					if errRead != nil {
						t.Error(errRead)
						return
					}
					if r.Method != http.MethodPost || (r.URL.Path != "/v1internal:generateContent" && r.URL.Path != "/v1internal:streamGenerateContent") {
						t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					attempt := submissions.Add(1)
					modelsMu.Lock()
					models = append(models, ojson.GetBytes(body, "model").String())
					modelsMu.Unlock()
					if ojson.GetBytes(body, "enabledCreditTypes.0").String() != "GOOGLE_ONE_AI" {
						t.Errorf("generation did not come through credits fallback: %s", body)
					}
					if refusal {
						if attempt == 1 {
							w.WriteHeader(http.StatusServiceUnavailable)
							_, _ = io.WriteString(w, `{"error":{"code":503,"status":"UNAVAILABLE","message":"generation rejected"}}`)
							return
						}
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"safe-fallback"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}}`+"\n\n")
						return
					}
					// Read the complete generation request, then lose the connection
					// before response headers. Its execution outcome is unknowable.
					conn, _, errHijack := w.(http.Hijacker).Hijack()
					if errHijack != nil {
						t.Error(errHijack)
						return
					}
					_ = conn.Close()
				}))
				defer upstream.Close()

				const model = "claude-credits-alias-pool"
				cfg := &config.Config{
					QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
					OpenAICompatibility: []config.OpenAICompatibility{{
						Name: "antigravity",
						Models: []config.OpenAICompatibilityModel{
							{Name: "claude-sonnet-4-6", Alias: model},
							{Name: "claude-opus-4-6", Alias: model},
						},
					}},
				}
				manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
				manager.SetConfig(cfg)
				manager.SetRetryConfig(0, 0, 0)
				adapter := runtimeexecutor.NewAntigravityExecutor(cfg)
				manager.RegisterExecutor(adapter)
				// Configured model pools resolve this credential through the
				// compatibility provider key. Keep the real adapter behind that key.
				manager.RegisterExecutor(&creditsPoolRoutedAntigravityExecutor{adapter})
				until := time.Now().Add(30 * time.Minute)
				for index := 0; index < credentials; index++ {
					authID := fmt.Sprintf("credits-alias-pool-%s-%d", t.Name(), index)
					auth := &coreauth.Auth{
						ID: authID, Provider: "antigravity", Status: coreauth.StatusActive,
						ProxyURL: "direct",
						Attributes: map[string]string{
							"base_url":     upstream.URL,
							"source":       "config:antigravity",
							"compat_name":  "antigravity",
							"provider_key": "antigravity",
						},
						Metadata: map[string]any{
							"access_token": "synthetic-credits-token",
							"project_id":   "synthetic-credits-project",
							"expired":      time.Now().Add(time.Hour).Format(time.RFC3339),
						},
						ModelStates: map[string]*coreauth.ModelState{
							"claude-sonnet-4-6": {
								Unavailable: true, NextRetryAfter: until,
								Quota: coreauth.QuotaState{Exceeded: true, NextRecoverAt: until},
							},
							"claude-opus-4-6": {
								Unavailable: true, NextRetryAfter: until,
								Quota: coreauth.QuotaState{Exceeded: true, NextRecoverAt: until},
							},
							model: {
								Unavailable: true, NextRetryAfter: until,
								Quota: coreauth.QuotaState{Exceeded: true, NextRecoverAt: until},
							},
						},
					}
					if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
						t.Fatal(errRegister)
					}
					registry.GetGlobalRegistry().RegisterClient(authID, "antigravity", []*registry.ModelInfo{{ID: model}})
					// Fresh hints prevent background loadCodeAssist refreshes.
					coreauth.SetAntigravityCreditsHint(authID, coreauth.AntigravityCreditsHint{Known: true, Available: true, UpdatedAt: time.Now()})
					t.Cleanup(func() {
						registry.GetGlobalRegistry().UnregisterClient(authID)
						coreauth.SetAntigravityCreditsHint(authID, coreauth.AntigravityCreditsHint{})
					})
				}

				base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
				router := gin.New()
				router.POST("/v1/chat/completions", NewOpenAIAPIHandler(base).ChatCompletions)
				body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}],"max_tokens":64,"stream":%t}`, model, stream)
				request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
				request.Header.Set("Content-Type", "application/json")
				response := httptest.NewRecorder()
				router.ServeHTTP(response, request)

				wire := response.Body.String()
				modelsMu.Lock()
				defer modelsMu.Unlock()
				t.Logf("generation submissions=%d status=%d body=%s", submissions.Load(), response.Code, wire)
				if refusal {
					if len(models) != 2 || models[0] == models[1] {
						t.Errorf("refusal did not advance within the model pool: %v", models)
					}
					if got := submissions.Load(); got != 2 || response.Code != http.StatusOK || !strings.Contains(wire, "safe-fallback") {
						t.Errorf("definitive refusal did not fail over safely: submissions=%d status=%d body=%s", got, response.Code, wire)
					}
					return
				}
				if got := submissions.Load(); got != 1 {
					t.Errorf("credits fallback submitted generation %d times after post-write disconnect, want 1", got)
				}
				if response.Code != http.StatusBadGateway {
					t.Errorf("status=%d, want 502 for unknown generation outcome", response.Code)
				}
				if !ojson.Valid(wire) || ojson.Get(wire, "error.code").String() != "upstream_outcome_unknown" {
					t.Errorf("credits fallback discarded unknown outcome: %s", wire)
				}
				retryable := ojson.Get(wire, "error.retryable")
				if !retryable.Exists() || retryable.Bool() {
					t.Errorf("unknown outcome must advertise retryable=false: %s", wire)
				}
				if got := response.Header().Get("x-should-retry"); got != "false" {
					t.Errorf("x-should-retry=%q, want false", got)
				}
			})
		}
	}
}

// Only registry identity changes; both generation methods are the real adapter.
type creditsPoolRoutedAntigravityExecutor struct {
	*runtimeexecutor.AntigravityExecutor
}

func (*creditsPoolRoutedAntigravityExecutor) Identifier() string {
	return "openai-compatible-antigravity"
}
