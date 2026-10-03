package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// liveQuotaExhaustedBody is the payload the production upstream returns for an exhausted
// weekly window: ErrorInfo carries QUOTA_EXHAUSTED and the message states when the
// window reopens. It has no RetryInfo, so the reset deadline is only recoverable from
// the message hint.
const liveQuotaExhaustedBody = `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 47h41m50s.","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED","domain":"cloudcode-pa.googleapis.com"}]}}`

// softRateLimitedBody is a plain rate limit: it carries no quota window, so the
// credential stays selectable and keeps serving as the failover target.
const softRateLimitedBody = `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Too many requests, slow down.","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED"}]}}`

// TestAntigravityManagerEndToEnd_QuotaExhaustedNotRepicked drives the real
// AntigravityExecutor through the real conductor against mock upstreams, using the
// production routing settings: cooldown.disable-cooling true and session affinity on.
//
// One credential's upstream always reports an exhausted weekly quota window; the other
// reports an ordinary rate limit and therefore stays selectable. After the first
// failure the exhausted credential must never be sent another request, instead of
// being re-picked every ~10s while its quota window is still ~48h from resetting.
//
// This covers the seam a unit test cannot: the real executor's status error reaching
// the real result builder and cooldown scheduler through the real selection path.
func TestAntigravityManagerEndToEnd_QuotaExhaustedNotRepicked(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	const model = "gemini-3.8-flash-high"
	const exhaustedID = "antigravity-e2e-exhausted"
	const softLimitedID = "antigravity-e2e-softlimited"

	var exhaustedCalls, softLimitedCalls atomic.Int64

	exhaustedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		exhaustedCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(liveQuotaExhaustedBody))
	}))
	t.Cleanup(exhaustedServer.Close)

	softLimitedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		softLimitedCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(softRateLimitedBody))
	}))
	t.Cleanup(softLimitedServer.Close)

	models := []*registry.ModelInfo{{ID: model}}
	for _, id := range []string{exhaustedID, softLimitedID} {
		registry.GetGlobalRegistry().RegisterClient(id, "antigravity", models)
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}

	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(NewAntigravityExecutor(&config.Config{}))
	manager.SetSelector(cliproxyauth.NewSessionAffinitySelector(nil))
	manager.SetConfig(&config.Config{DisableCooling: true, RequestRetry: 3, MaxRetryInterval: 30})
	t.Cleanup(func() { manager.SetConfig(&config.Config{}) })

	newAuth := func(id, baseURL string) *cliproxyauth.Auth {
		return &cliproxyauth.Auth{
			ID:         id,
			Provider:   "antigravity",
			Status:     cliproxyauth.StatusActive,
			Attributes: map[string]string{"base_url": baseURL},
			Metadata: map[string]any{
				"access_token": "test-token",
				"project_id":   "test-project",
				"expired":      time.Now().Add(time.Hour).Format(time.RFC3339),
			},
		}
	}
	if _, err := manager.Register(cliproxyauth.WithSkipPersist(context.Background()), newAuth(exhaustedID, exhaustedServer.URL)); err != nil {
		t.Fatalf("Register(exhausted) error = %v", err)
	}
	if _, err := manager.Register(cliproxyauth.WithSkipPersist(context.Background()), newAuth(softLimitedID, softLimitedServer.URL)); err != nil {
		t.Fatalf("Register(soft-limited) error = %v", err)
	}

	before := time.Now()
	for i := range 4 {
		opts := cliproxyexecutor.Options{
			SourceFormat:    sdktranslator.FormatOpenAI,
			OriginalRequest: []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"request-%d"}]}`, model, i)),
		}
		_, _ = manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model, Payload: []byte(`{}`)}, opts)
	}

	if got := exhaustedCalls.Load(); got != 1 {
		t.Fatalf("exhausted credential received %d upstream requests, want exactly 1: it must stay cooled until its ~48h reset", got)
	}
	if got := softLimitedCalls.Load(); got == 0 {
		t.Fatal("failover credential was never attempted; the exhausted one was reused instead")
	}

	stored, ok := manager.GetByID(exhaustedID)
	if !ok || stored == nil {
		t.Fatal("credential not found after Execute")
	}
	state := stored.ModelStates[model]
	if state == nil || !state.Quota.Exceeded {
		t.Fatalf("ModelStates[%q] = %+v, want a recorded quota cooldown", model, state)
	}
	if remaining := state.Quota.NextRecoverAt.Sub(before); remaining < 40*time.Hour {
		t.Fatalf("cooldown remaining = %v, want the ~48h upstream reset (state=%+v)", remaining, state)
	}
}
