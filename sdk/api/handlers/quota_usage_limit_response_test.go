package handlers_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

const quotaFakeProvider = "quota-usage-limit-fake"

// quotaRefusal is the upstream 429 the fake executor returns for a credential.
type quotaRefusal struct {
	body       string
	retryAfter *time.Duration
	exhausted  bool
}

func (e quotaRefusal) Error() string                   { return e.body }
func (e quotaRefusal) StatusCode() int                 { return http.StatusTooManyRequests }
func (e quotaRefusal) RetryAfter() *time.Duration      { return e.retryAfter }
func (e quotaRefusal) IsQuotaExhausted() bool          { return e.exhausted }
func (e quotaRefusal) QuotaLimitWindowMinutes() int64  { return 300 }
func (e quotaRefusal) QuotaResetAfter() *time.Duration { return nil }
func (e quotaRefusal) IsCredentialScoped() bool        { return true }
func (e quotaRefusal) Headers() http.Header {
	if e.retryAfter == nil {
		return nil
	}
	return http.Header{"Retry-After": {strconv.Itoa(int(e.retryAfter.Seconds()))}}
}

// codexUsageLimit is an explicit quota exhaustion with a provider reset.
func codexUsageLimit(reset time.Duration) quotaRefusal {
	return quotaRefusal{body: `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached"}}`, retryAfter: &reset, exhausted: true}
}

// genericRateLimit is an unclassified 429, even with a long provider hint.
func genericRateLimit(reset time.Duration) quotaRefusal {
	return quotaRefusal{body: `{"error":{"type":"rate_limit_exceeded","message":"Rate limit reached"}}`, retryAfter: &reset}
}

type quotaFakeExecutor struct {
	refusals map[string]quotaRefusal
	calls    atomic.Int32
}

func (*quotaFakeExecutor) Identifier() string { return quotaFakeProvider }

func (e *quotaFakeExecutor) Execute(_ context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	e.calls.Add(1)
	return coreexecutor.Response{}, e.refusals[auth.ID]
}

func (e *quotaFakeExecutor) ExecuteStream(_ context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.calls.Add(1)
	return nil, e.refusals[auth.ID]
}

func (*quotaFakeExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (*quotaFakeExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (*quotaFakeExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

type quotaSurface struct {
	name, path, body string
	claude           bool
}

var quotaSurfaces = []quotaSurface{
	{name: "responses", path: "/v1/responses", body: `{"input":"hi"}`},
	{name: "chat", path: "/v1/chat/completions", body: `{"messages":[{"role":"user","content":"hi"}]}`},
	{name: "messages", path: "/v1/messages", claude: true, body: `{"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`},
}

type quotaHarness struct {
	router   *gin.Engine
	executor *quotaFakeExecutor
	model    string
}

func newQuotaHarness(t *testing.T, mode string, refusals ...quotaRefusal) *quotaHarness {
	t.Helper()
	gin.SetMode(gin.TestMode)
	coreauth.SetQuotaCooldownResponseMode(mode)
	t.Cleanup(func() { coreauth.SetQuotaCooldownResponseMode("") })

	executor := &quotaFakeExecutor{refusals: make(map[string]quotaRefusal)}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	model := "quota-model-" + strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
	for i, refusal := range refusals {
		authID := fmt.Sprintf("%s-auth-%d", model, i)
		executor.refusals[authID] = refusal
		if _, err := manager.Register(context.Background(), &coreauth.Auth{ID: authID, Provider: quotaFakeProvider, Status: coreauth.StatusActive}); err != nil {
			t.Fatalf("register %s: %v", authID, err)
		}
		registry.GetGlobalRegistry().RegisterClient(authID, quotaFakeProvider, []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	}

	// Passthrough is on so the test proves an upstream Retry-After is dropped.
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{PassthroughHeaders: true}, manager)
	router := gin.New()
	router.POST("/v1/responses", openai.NewOpenAIResponsesAPIHandler(base).Responses)
	router.POST("/v1/chat/completions", openai.NewOpenAIAPIHandler(base).ChatCompletions)
	router.POST("/v1/messages", claude.NewClaudeCodeAPIHandler(base).ClaudeMessages)
	return &quotaHarness{router: router, executor: executor, model: model}
}

func (h *quotaHarness) do(surface quotaSurface, stream bool) *httptest.ResponseRecorder {
	body := ojson.NewDocString(surface.body)
	for path, value := range map[string]any{"model": h.model, "stream": stream} {
		if errSet := body.Set(path, value); errSet != nil {
			panic(errSet)
		}
	}
	request := httptest.NewRequest(http.MethodPost, surface.path, strings.NewReader(body.String()))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	h.router.ServeHTTP(recorder, request)
	return recorder
}

var claudeResetSuffix = regexp.MustCompile(`\(reset after (1h59m5\ds|2h0m0s)\)$`)

// assertUsageLimitResponse checks a final usage limit two hours away on a
// 5-hour window.
func assertUsageLimitResponse(t *testing.T, surface quotaSurface, recorder *httptest.ResponseRecorder) {
	t.Helper()
	body, header := recorder.Body.Bytes(), recorder.Header()
	resetsAt := ojson.GetBytes(body, "error.resets_at").Int()
	if diff := resetsAt - time.Now().Add(2*time.Hour).Unix(); diff < -60 || diff > 60 {
		t.Fatalf("resets_at = %d, want about two hours from now; body=%s", resetsAt, body)
	}
	if recorder.Code != http.StatusTooManyRequests || header.Get("X-Should-Retry") != "false" || len(header.Values("Retry-After")) != 0 ||
		ojson.GetBytes(body, "error.retryable").Raw != "false" {
		t.Fatalf("status=%d headers=%v body=%s, want 429, X-Should-Retry false, no Retry-After, retryable false", recorder.Code, header, body)
	}
	if header.Get("X-Codex-Primary-Used-Percent") != "100" || header.Get("X-Codex-Primary-Window-Minutes") != "300" ||
		header.Get("X-Codex-Primary-Reset-At") != strconv.FormatInt(resetsAt, 10) {
		t.Fatalf("codex headers = %v, want the exhausted 5-hour window", header)
	}
	if surface.claude {
		if ojson.GetBytes(body, "error.type").String() != "rate_limit_error" || !claudeResetSuffix.MatchString(ojson.GetBytes(body, "error.message").String()) {
			t.Fatalf("body=%s, want a rate_limit_error ending in (reset after <2h>)", body)
		}
		return
	}
	if ojson.GetBytes(body, "error.type").String() != "usage_limit_reached" || ojson.GetBytes(body, "error.code").String() != "model_cooldown" ||
		ojson.GetBytes(body, "error.limit_window_minutes").Int() != 300 || !ojson.GetBytes(body, "error.reset_seconds").Exists() {
		t.Fatalf("body=%s, want usage_limit_reached on the model_cooldown body", body)
	}
}

// assertRetryableResponse checks today's answer: the cooldown with Retry-After
// when every credential was already cooling, else the upstream refusal.
func assertRetryableResponse(t *testing.T, recorder *httptest.ResponseRecorder, alreadyCooling bool) {
	t.Helper()
	body, header := recorder.Body.Bytes(), recorder.Header()
	if recorder.Code != http.StatusTooManyRequests || header.Get("X-Should-Retry") != "" || header.Get("X-Codex-Primary-Used-Percent") != "" ||
		ojson.GetBytes(body, "error.resets_at").Exists() || strings.Contains(string(body), "reset after") {
		t.Fatalf("status=%d headers=%v body=%s, want the retryable answer", recorder.Code, header, body)
	}
	if isCooldown := strings.Contains(string(body), "are cooling down"); isCooldown != alreadyCooling {
		t.Fatalf("cooldown answer = %t, want %t; body=%s", isCooldown, alreadyCooling, body)
	}
	if seconds, _ := strconv.Atoi(header.Get("Retry-After")); alreadyCooling && seconds <= 0 {
		t.Fatalf("Retry-After = %q, want a positive delay", header.Get("Retry-After"))
	}
}

// TestQuotaCooldownResponseModeHTTPSurfaces covers both modes, the two
// pre-commit paths (the last credential refuses during this request, or every
// credential is already cooling at selection) and the three HTTP surfaces,
// streaming and not.
func TestQuotaCooldownResponseModeHTTPSurfaces(t *testing.T) {
	for _, mode := range []string{"retryable", "usage-limit"} {
		for _, alreadyCooling := range []bool{false, true} {
			for _, surface := range quotaSurfaces {
				for _, stream := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/cooling=%t/%s/stream=%t", mode, alreadyCooling, surface.name, stream), func(t *testing.T) {
						h := newQuotaHarness(t, mode, codexUsageLimit(2*time.Hour), codexUsageLimit(2*time.Hour))
						if alreadyCooling {
							_ = h.do(surface, stream)
						}
						recorder := h.do(surface, stream)
						if got := h.executor.calls.Load(); got != 2 {
							t.Fatalf("upstream calls = %d, want 2", got)
						}
						if mode == "usage-limit" {
							assertUsageLimitResponse(t, surface, recorder)
						} else {
							assertRetryableResponse(t, recorder, alreadyCooling)
						}
					})
				}
			}
		}
	}
}

// TestQuotaCooldownUsageLimitModeKeepsRetryableUnlessEveryCandidateQualifies
// covers pools that keep today's answer in the usage-limit mode.
func TestQuotaCooldownUsageLimitModeKeepsRetryableUnlessEveryCandidateQualifies(t *testing.T) {
	missingReset := codexUsageLimit(0)
	missingReset.retryAfter = nil
	for name, refusals := range map[string][]quotaRefusal{
		"mixed pool":              {codexUsageLimit(2 * time.Hour), genericRateLimit(2 * time.Hour)},
		"generic long rate limit": {genericRateLimit(2 * time.Hour)},
		"reset under five min":    {codexUsageLimit(4 * time.Minute)},
		"missing reset":           {missingReset},
	} {
		t.Run(name, func(t *testing.T) {
			h := newQuotaHarness(t, "usage-limit", refusals...)
			assertRetryableResponse(t, h.do(quotaSurfaces[0], true), false)
			if name != "missing reset" {
				// The backoff after a 429 without a reset may already be over.
				assertRetryableResponse(t, h.do(quotaSurfaces[0], true), true)
			}
		})
	}
}

// The mode is read per response: evidence recorded in the retryable mode is
// reported as a usage limit right after switching, and switching back restores
// the retryable answer.
func TestQuotaCooldownResponseModeSwitchAppliesToExistingCooldowns(t *testing.T) {
	surface := quotaSurfaces[0]
	h := newQuotaHarness(t, "retryable", codexUsageLimit(2*time.Hour))
	assertRetryableResponse(t, h.do(surface, true), false)
	coreauth.SetQuotaCooldownResponseMode("usage-limit")
	assertUsageLimitResponse(t, surface, h.do(surface, true))
	coreauth.SetQuotaCooldownResponseMode("retryable")
	assertRetryableResponse(t, h.do(surface, true), true)
}
