package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// stubAntigravityExecutor stands in for the real Antigravity executor at the Manager
// boundary. It reproduces exactly what the real executor does for an exhausted quota
// window - the production 429 body plus the status error carrying RetryAfter, QuotaGroup
// and IsQuotaResetDeadline - without the OAuth and translation machinery, so the
// conductor path (selection, session affinity, MarkResult) is exercised end to end.
type stubAntigravityExecutor struct {
	exhausted map[string]bool // auth ID -> always return the quota-exhausted 429
	mu        sync.Mutex
	attempts  map[string]int // auth ID -> request count
}

func newStubAntigravityExecutor(exhausted ...string) *stubAntigravityExecutor {
	ex := &stubAntigravityExecutor{exhausted: make(map[string]bool), attempts: make(map[string]int)}
	for _, id := range exhausted {
		ex.exhausted[id] = true
	}
	return ex
}

func (e *stubAntigravityExecutor) Identifier() string { return "antigravity" }

func (e *stubAntigravityExecutor) count(authID string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.attempts[authID]
}

func (e *stubAntigravityExecutor) Execute(_ context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.attempts[auth.ID]++
	exhausted := e.exhausted[auth.ID]
	e.mu.Unlock()
	if exhausted {
		return cliproxyexecutor.Response{}, e.quotaExhaustedErr()
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"candidates":[]}`)}, nil
}

func (e *stubAntigravityExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, errors.New("unused")
}

func (e *stubAntigravityExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *stubAntigravityExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *stubAntigravityExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("unused")
}

// antigravityQuotaExhaustedBody mirrors the live upstream payload verbatim, including
// the ErrorInfo QUOTA_EXHAUSTED detail and the human-readable reset hint.
const antigravityQuotaExhaustedBody = `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 47h55m14s.","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED","domain":"cloudcode-pa.googleapis.com","metadata":{"quotaResetDelay":"172514s"}}]}}`

// quotaExhaustedStatusErr mirrors the real executor's statusErr for the body above.
type quotaExhaustedStatusErr struct{}

func (quotaExhaustedStatusErr) Error() string              { return antigravityQuotaExhaustedBody }
func (quotaExhaustedStatusErr) StatusCode() int            { return http.StatusTooManyRequests }
func (quotaExhaustedStatusErr) QuotaGroup() string         { return AntigravityQuotaGroupGemini }
func (quotaExhaustedStatusErr) IsCredentialScoped() bool   { return false }
func (quotaExhaustedStatusErr) IsQuotaResetDeadline() bool { return true }
func (quotaExhaustedStatusErr) RetryAfter() *time.Duration {
	v := 47*time.Hour + 55*time.Minute + 14*time.Second
	return &v
}

func (e *stubAntigravityExecutor) quotaExhaustedErr() error { return quotaExhaustedStatusErr{} }

// e2eManager wires a Manager with two Antigravity credentials, session affinity on,
// and the production routing cooldown settings (disable-cooling: true).
func e2eManager(t *testing.T, exhaustedID string) (*Manager, *stubAntigravityExecutor) {
	t.Helper()
	models := []*registry.ModelInfo{
		{ID: "gemini-3.8-flash-high"},
		{ID: "gemini-3.8-pro-high"},
	}
	for _, id := range []string{"antigravity-account-a", "antigravity-account-b"} {
		registry.GetGlobalRegistry().RegisterClient(id, "antigravity", models)
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}

	executor := newStubAntigravityExecutor(exhaustedID)
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	// Session affinity wraps the fallback selector, matching routing.session-affinity: true.
	manager.SetSelector(NewSessionAffinitySelector(&RoundRobinSelector{}))
	manager.SetConfig(&internalconfig.Config{
		DisableCooling:   true,
		RequestRetry:     3,
		MaxRetryInterval: 30,
		Routing: internalconfig.RoutingConfig{
			Strategy:        "round-robin",
			SessionAffinity: true,
		},
	})
	for _, id := range []string{"antigravity-account-a", "antigravity-account-b"} {
		if _, err := manager.Register(WithSkipPersist(context.Background()), &Auth{
			ID: id, Provider: "antigravity", Status: StatusActive,
		}); err != nil {
			t.Fatalf("Register(%s) error = %v", id, err)
		}
	}
	t.Cleanup(func() { manager.SetConfig(&internalconfig.Config{}) })
	return manager, executor
}

// TestExecuteMixedQuotaExhaustedNotRepicked is the end-to-end regression guard. It runs
// the real non-stream mixed execution path - selection, session affinity, executor call,
// MarkResult - against a credential whose upstream reports an exhausted quota window,
// with the production disable-cooling setting. After the first failure the exhausted
// credential must not be handed to the executor again.
func TestExecuteMixedQuotaExhaustedNotRepicked(t *testing.T) {
	const exhaustedID = "antigravity-account-a"
	const model = "gemini-3.8-flash-high"

	manager, executor := e2eManager(t, exhaustedID)
	ctx := context.Background()
	req := cliproxyexecutor.Request{Model: model, Payload: []byte(`{}`)}

	// Distinct content per request mirrors the reported load (different sessions).
	for i := range 4 {
		payload := fmt.Sprintf(`{"messages":[{"role":"user","content":"request-%d"}]}`, i)
		opts := cliproxyexecutor.Options{OriginalRequest: []byte(payload)}
		if _, err := manager.Execute(ctx, []string{"antigravity"}, req, opts); err != nil {
			t.Logf("request %d: Execute() error = %v", i, err)
		}
	}

	if got := executor.count(exhaustedID); got != 1 {
		t.Fatalf("exhausted credential attempted %d times, want exactly 1: it must be cooled until its ~48h reset", got)
	}
	if got := executor.count("antigravity-account-b"); got == 0 {
		t.Fatal("healthy credential was never attempted; failover did not happen")
	}
}

// TestExecuteMixedQuotaExhaustedResetsModelState verifies the cooldown is recorded on
// the model state, which is what the selector consults.
func TestExecuteMixedQuotaExhaustedResetsModelState(t *testing.T) {
	const exhaustedID = "antigravity-account-a"
	const model = "gemini-3.8-flash-high"

	manager, _ := e2eManager(t, exhaustedID)
	before := time.Now()
	req := cliproxyexecutor.Request{Model: model, Payload: []byte(`{}`)}
	opts := cliproxyexecutor.Options{OriginalRequest: []byte(`{"model":"gemini-3.8-flash-high","messages":[{"content":"x"}]}`)}
	_, _ = manager.Execute(context.Background(), []string{"antigravity"}, req, opts)

	stored, ok := manager.GetByID(exhaustedID)
	if !ok || stored == nil {
		t.Fatal("credential not found after Execute")
	}
	state := stored.ModelStates[model]
	if state == nil || !state.Quota.Exceeded {
		t.Fatalf("ModelStates[%q] = %+v, want a recorded quota cooldown", model, state)
	}
	if remaining := state.Quota.NextRecoverAt.Sub(before); remaining < 40*time.Hour {
		t.Fatalf("cooldown remaining = %v, want the ~48h upstream reset", remaining)
	}
}

// TestExecuteMixedStreamQuotaExhaustedNotRepicked covers the streaming path, which builds
// its Result through a different branch than the non-stream path.
func TestExecuteMixedStreamQuotaExhaustedNotRepicked(t *testing.T) {
	const exhaustedID = "antigravity-account-a"
	const model = "gemini-3.8-flash-high"

	manager, executor := e2eManager(t, exhaustedID)
	ctx := context.Background()
	req := cliproxyexecutor.Request{Model: model, Payload: []byte(`{}`)}
	for i := range 3 {
		opts := cliproxyexecutor.Options{OriginalRequest: []byte(fmt.Sprintf(`{"messages":[{"content":"stream-%d"}]}`, i))}
		_, _ = manager.Execute(ctx, []string{"antigravity"}, req, opts)
	}
	if got := executor.count(exhaustedID); got != 1 {
		t.Fatalf("exhausted credential attempted %d times, want 1", got)
	}
}
