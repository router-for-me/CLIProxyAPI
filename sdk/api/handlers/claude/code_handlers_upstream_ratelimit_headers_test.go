package claude

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// upstreamRateLimit is how a fake Anthropic upstream answers one credential.
type upstreamRateLimit struct {
	headers map[string]string
}

// newUpstreamRateLimitRoute serves /v1/messages through the real Claude
// executor, with one credential per entry in limits. Each credential's API key
// selects its upstream answer, which is always a 429.
func newUpstreamRateLimitRoute(t *testing.T, name string, passthrough bool, limits []upstreamRateLimit) (*gin.Engine, string) {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("X-Api-Key")
		if key == "" {
			key = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		}
		index, errIndex := strconv.Atoi(strings.TrimPrefix(key, "key-"))
		if errIndex != nil || index < 0 || index >= len(limits) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Anthropic-Organization-Id", "org-should-not-leak")
		w.Header().Set("Set-Cookie", "session=should-not-leak")
		for key, value := range limits[index].headers {
			w.Header().Set(key, value)
		}
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed your account's rate limit. Please try again later."}}`))
	}))
	t.Cleanup(upstream.Close)

	model := "claude-upstream-ratelimit-" + name
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	manager.RegisterExecutor(executor.NewClaudeExecutor(&config.Config{}))
	for index := range limits {
		authID := model + "-auth-" + strconv.Itoa(index)
		if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
			ID:       authID,
			Provider: "claude",
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"api_key":  "key-" + strconv.Itoa(index),
				"base_url": upstream.URL,
			},
		}); errRegister != nil {
			t.Fatalf("register auth: %v", errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient(authID, "claude", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	}

	handler := NewClaudeCodeAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{PassthroughHeaders: passthrough}, manager))
	router := gin.New()
	router.POST("/v1/messages", handler.ClaudeMessages)
	return router, model
}

func postUpstreamRateLimitRoute(t *testing.T, router *gin.Engine, model string, streaming bool) (*httptest.ResponseRecorder, int64, int64) {
	t.Helper()
	requestBody := `{"model":"` + model + `","max_tokens":16,"messages":[{"role":"user","content":"hi"}],"stream":` + strconv.FormatBool(streaming) + `}`
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(requestBody))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	before := time.Now().Unix()
	router.ServeHTTP(recorder, request)
	after := time.Now().Unix()
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429; body=%s", recorder.Code, recorder.Body.String())
	}
	for _, leaked := range []string{"Anthropic-Organization-Id", "Set-Cookie"} {
		if got := recorder.Header().Get(leaked); got != "" {
			t.Fatalf("%s = %q, want it dropped", leaked, got)
		}
	}
	return recorder, before, after
}

func retryAfterSeconds(t *testing.T, recorder *httptest.ResponseRecorder) int64 {
	t.Helper()
	seconds, errSeconds := strconv.ParseInt(recorder.Header().Get("Retry-After"), 10, 64)
	if errSeconds != nil || seconds <= 0 {
		t.Fatalf("Retry-After = %q, want positive seconds", recorder.Header().Get("Retry-After"))
	}
	return seconds
}

// assertUnifiedResetMatchesRetryAfter checks that the usage-limit reset the
// client sees is the moment Retry-After points to. Both values are rounded up
// to whole seconds at slightly different instants, so allow one second.
func assertUnifiedResetMatchesRetryAfter(t *testing.T, recorder *httptest.ResponseRecorder, before, after int64) {
	t.Helper()
	seconds := retryAfterSeconds(t, recorder)
	resetAt, errReset := strconv.ParseInt(recorder.Header().Get("Anthropic-Ratelimit-Unified-Reset"), 10, 64)
	if errReset != nil || resetAt < before+seconds-1 || resetAt > after+seconds+1 {
		t.Fatalf("Anthropic-Ratelimit-Unified-Reset = %q, want unix epoch in [%d, %d] to match Retry-After %d",
			recorder.Header().Get("Anthropic-Ratelimit-Unified-Reset"), before+seconds-1, after+seconds+1, seconds)
	}
}

func unixIn(d time.Duration) string {
	return strconv.FormatInt(time.Now().Add(d).Unix(), 10)
}

func fiveHourRejection(reset string) upstreamRateLimit {
	return upstreamRateLimit{headers: map[string]string{
		"Anthropic-Ratelimit-Unified-Status":               "rejected",
		"Anthropic-Ratelimit-Unified-5h-Status":            "rejected",
		"Anthropic-Ratelimit-Unified-5h-Reset":             reset,
		"Anthropic-Ratelimit-Unified-7d-Status":            "allowed",
		"Anthropic-Ratelimit-Unified-Representative-Claim": "five_hour",
		"Anthropic-Ratelimit-Unified-Reset":                reset,
	}}
}

// TestClaudeMessagesRoute_UpstreamRateLimit429CarriesUnifiedHeaders covers the
// first 429 of a usage limit: the one Anthropic sends, before any local
// cooldown exists. With passthrough-headers enabled, Claude Code needs the
// Anthropic-Ratelimit-Unified-* headers on that response to report the
// subscription limit and its reset time instead of a generic rate-limit error.
func TestClaudeMessagesRoute_UpstreamRateLimit429CarriesUnifiedHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, passthrough := range []bool{false, true} {
		for _, streaming := range []bool{false, true} {
			name := "passthrough_" + strconv.FormatBool(passthrough) + "_stream_" + strconv.FormatBool(streaming)
			t.Run(name, func(t *testing.T) {
				router, model := newUpstreamRateLimitRoute(t, "single-"+name, passthrough, []upstreamRateLimit{fiveHourRejection(unixIn(2 * time.Hour))})
				recorder, before, after := postUpstreamRateLimitRoute(t, router, model, streaming)

				if !passthrough {
					if got := recorder.Header().Get("Anthropic-Ratelimit-Unified-Status"); got != "" {
						t.Fatalf("passthrough=false forwarded Anthropic-Ratelimit-Unified-Status = %q", got)
					}
					return
				}
				for key, want := range map[string]string{
					"Anthropic-Ratelimit-Unified-Status":               "rejected",
					"Anthropic-Ratelimit-Unified-5h-Status":            "rejected",
					"Anthropic-Ratelimit-Unified-Representative-Claim": "five_hour",
				} {
					if got := recorder.Header().Get(key); got != want {
						t.Errorf("%s = %q, want %q", key, got, want)
					}
				}
				assertUnifiedResetMatchesRetryAfter(t, recorder, before, after)
				if got, want := recorder.Header().Get("Anthropic-Ratelimit-Unified-5h-Reset"), recorder.Header().Get("Anthropic-Ratelimit-Unified-Reset"); got != want {
					t.Fatalf("Anthropic-Ratelimit-Unified-5h-Reset = %q, want %q", got, want)
				}
			})
		}
	}
}

// TestClaudeMessagesRoute_UpstreamRateLimit429DescribesThePool covers a pool of
// credentials that all fail the same request. The unified headers must describe
// the pool, as the cooldown 429s that follow do, not the credential tried last.
func TestClaudeMessagesRoute_UpstreamRateLimit429DescribesThePool(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, streaming := range []bool{false, true} {
		t.Run("ordinary 429 in the pool sends no usage limit/stream="+strconv.FormatBool(streaming), func(t *testing.T) {
			// One credential has a short ordinary rate limit; the other hit
			// its 5h usage limit. The pool recovers in about a minute, so the
			// client must not be told it hit its usage limit.
			router, model := newUpstreamRateLimitRoute(t, "mixed-"+strconv.FormatBool(streaming), true, []upstreamRateLimit{
				{headers: map[string]string{"Retry-After": "60"}},
				fiveHourRejection(unixIn(5 * time.Hour)),
			})
			recorder, _, _ := postUpstreamRateLimitRoute(t, router, model, streaming)
			if seconds := retryAfterSeconds(t, recorder); seconds > 120 {
				t.Fatalf("Retry-After = %d, want the ordinary limit's minute", seconds)
			}
			for key := range recorder.Header() {
				if strings.HasPrefix(strings.ToLower(key), "anthropic-ratelimit-unified-") {
					t.Fatalf("%s = %q, want no unified headers while a credential only has an ordinary rate limit", key, recorder.Header().Get(key))
				}
			}
		})
		t.Run("usage limits report the earliest reset/stream="+strconv.FormatBool(streaming), func(t *testing.T) {
			// Both credentials hit their 5h usage limit. One resets in 1h,
			// the other in 5h; the pool is usable again after 1h.
			router, model := newUpstreamRateLimitRoute(t, "exhausted-"+strconv.FormatBool(streaming), true, []upstreamRateLimit{
				fiveHourRejection(unixIn(time.Hour)),
				fiveHourRejection(unixIn(5 * time.Hour)),
			})
			recorder, before, after := postUpstreamRateLimitRoute(t, router, model, streaming)
			if got := recorder.Header().Get("Anthropic-Ratelimit-Unified-Status"); got != "rejected" {
				t.Fatalf("Anthropic-Ratelimit-Unified-Status = %q, want rejected", got)
			}
			if seconds := retryAfterSeconds(t, recorder); seconds > int64((time.Hour + 5*time.Minute).Seconds()) {
				t.Fatalf("Retry-After = %d, want about one hour", seconds)
			}
			assertUnifiedResetMatchesRetryAfter(t, recorder, before, after)
		})
	}
}
