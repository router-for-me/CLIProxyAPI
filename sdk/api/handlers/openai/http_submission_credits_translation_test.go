package openai

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
func TestHTTPSubmissionAntigravityCreditsTranslationIsTerminal(t *testing.T) {
	for _, credentials := range []int{1, 2} {
		t.Run(fmt.Sprintf("credentials=%d", credentials), func(t *testing.T) {
			var submissions atomic.Int32
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
				submissions.Add(1)
				if ojson.GetBytes(body, "enabledCreditTypes.0").String() != "GOOGLE_ONE_AI" {
					t.Errorf("generation did not come through credits fallback: %s", body)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"functions__apply_patch","args":{"input":1}}}]},"finishReason":"STOP"}]}}`)

			}))
			defer upstream.Close()

			cfg := &config.Config{QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true}, OpenAICompatibility: []config.OpenAICompatibility{{Name: "antigravity", Models: []config.OpenAICompatibilityModel{{Name: "gemini-2.5-flash", Alias: "claude-credits-translation"}}}}}
			manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
			manager.SetConfig(cfg)
			manager.SetRetryConfig(0, 0, 0)
			manager.RegisterExecutor(runtimeexecutor.NewAntigravityExecutor(cfg))
			manager.RegisterExecutor(&creditsPoolRoutedAntigravityExecutor{runtimeexecutor.NewAntigravityExecutor(cfg)})
			const model = "claude-credits-translation"
			until := time.Now().Add(30 * time.Minute)
			for index := 0; index < credentials; index++ {
				authID := fmt.Sprintf("credits-terminal-%s-%d", t.Name(), index)
				auth := &coreauth.Auth{
					ID: authID, Provider: "antigravity", Status: coreauth.StatusActive,
					ProxyURL:   "direct",
					Attributes: map[string]string{"base_url": upstream.URL, "source": "config:antigravity", "compat_name": "antigravity", "provider_key": "antigravity"},
					Metadata: map[string]any{
						"access_token": "synthetic-credits-token",
						"project_id":   "synthetic-credits-project",
						"expired":      time.Now().Add(time.Hour).Format(time.RFC3339),
					},
					ModelStates: map[string]*coreauth.ModelState{
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
			router.POST("/v1/responses", NewOpenAIResponsesAPIHandler(base).Responses)
			body := ojson.NewDocString(`{"input":"make a patch","stream":false,"tools":[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"apply_patch","format":{"type":"grammar","definition":"start: patch"}}]}]}`)
			if errSet := body.Set("model", model); errSet != nil {
				t.Fatal(errSet)
			}

			request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body.String()))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)

			wire := response.Body.String()
			t.Logf("generation submissions=%d status=%d body=%s", submissions.Load(), response.Code, wire)
			if got := submissions.Load(); got != 1 {
				t.Errorf("credits fallback submitted generation %d times after accepted translation failure, want 1", got)
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
