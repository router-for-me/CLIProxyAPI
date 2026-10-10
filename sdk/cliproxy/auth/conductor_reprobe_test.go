package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type reprobeTestStatusError struct {
	code int
	msg  string
}

func (e reprobeTestStatusError) Error() string   { return e.msg }
func (e reprobeTestStatusError) StatusCode() int { return e.code }

type reprobeTestExecutor struct {
	calls      int
	err        error
	lastOpts   cliproxyexecutor.Options
	prepare    bool
	prepareErr error
}

func (e *reprobeTestExecutor) Identifier() string { return "reprobe-test" }

func (e *reprobeTestExecutor) ShouldPrepareRequestAuth(*Auth) bool { return e.prepare }

func (e *reprobeTestExecutor) PrepareRequestAuth(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, e.prepareErr
}

func (e *reprobeTestExecutor) Execute(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.calls++
	e.lastOpts = opts
	if e.err != nil {
		return cliproxyexecutor.Response{}, e.err
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"status":"ok"}`)}, nil
}

func (e *reprobeTestExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, errors.New("stream not supported")
}

func (e *reprobeTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *reprobeTestExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not supported")
}

func (e *reprobeTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not supported")
}

func registerReprobeTestModels(t *testing.T, authID string, models ...string) {
	t.Helper()
	infos := make([]*registry.ModelInfo, 0, len(models))
	for _, model := range models {
		infos = append(infos, &registry.ModelInfo{ID: model})
	}
	registry.GetGlobalRegistry().RegisterClient(authID, "reprobe-test", infos)
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
}

func TestReprobeExpiredTimer(t *testing.T) {
	t.Parallel()

	now := time.Now()
	past := now.Add(-time.Minute)
	future := now.Add(time.Hour)

	cases := []struct {
		name          string
		unavailable   bool
		quotaExceeded bool
		nextRetry     time.Time
		nextRecover   time.Time
		want          bool
	}{
		{name: "no flags", want: false},
		{name: "permanent unavailable without timer", unavailable: true, want: false},
		{name: "unavailable retry still pending", unavailable: true, nextRetry: future, want: false},
		{name: "unavailable retry elapsed", unavailable: true, nextRetry: past, want: true},
		{name: "quota recover elapsed", quotaExceeded: true, nextRecover: past, want: true},
		{name: "quota recover pending", quotaExceeded: true, nextRecover: future, want: false},
		{name: "retry elapsed but recover pending", unavailable: true, quotaExceeded: true, nextRetry: past, nextRecover: future, want: false},
		{name: "both timers elapsed", unavailable: true, quotaExceeded: true, nextRetry: past, nextRecover: past, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := reprobeExpiredTimer(tc.unavailable, tc.quotaExceeded, tc.nextRetry, tc.nextRecover, now)
			if got != tc.want {
				t.Fatalf("reprobeExpiredTimer() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCollectReprobeCandidates(t *testing.T) {
	t.Parallel()

	now := time.Now()
	past := now.Add(-time.Minute)
	future := now.Add(time.Hour)

	m := NewManager(nil, nil, nil)
	ctx := context.Background()

	auths := []*Auth{
		// Credential-scope cooldown whose timer elapsed.
		{ID: "cred-expired", Provider: "reprobe-test", Unavailable: true, NextRetryAfter: past},
		// Model-scope cooldowns: one elapsed, one still pending.
		{ID: "model-mixed", Provider: "reprobe-test", ModelStates: map[string]*ModelState{
			"gpt-5": {Unavailable: true, NextRetryAfter: past},
			"gpt-4": {Unavailable: true, NextRetryAfter: future},
		}},
		// Credential gate still active; per-model probe must not fire early.
		{ID: "cred-pending", Provider: "reprobe-test", Unavailable: true, NextRetryAfter: future, ModelStates: map[string]*ModelState{
			"gpt-5": {Unavailable: true, NextRetryAfter: past},
		}},
		// Disabled credentials are never probed.
		{ID: "disabled", Provider: "reprobe-test", Disabled: true, Unavailable: true, NextRetryAfter: past},
		// Disabled model states are never probed, even with elapsed timers.
		{ID: "model-disabled", Provider: "reprobe-test", ModelStates: map[string]*ModelState{
			"gpt-5": {Status: StatusDisabled, Unavailable: true, NextRetryAfter: past},
		}},
		// Aggregate cooldown with a disabled route model must not probe it.
		{ID: "fallback-disabled", Provider: "reprobe-test", Unavailable: true, NextRetryAfter: past, ModelStates: map[string]*ModelState{
			"gpt-5": {Status: StatusDisabled},
			"gpt-4": {},
		}},
		// Aggregate cooldown must not probe a model still inside its own cooldown.
		{ID: "fallback-blocked", Provider: "reprobe-test", Unavailable: true, NextRetryAfter: past, ModelStates: map[string]*ModelState{
			"gpt-5": {Unavailable: true, NextRetryAfter: future},
		}},
		// Aggregate cooldown skips blocked registered models for a free one.
		{ID: "fallback-next", Provider: "reprobe-test", Unavailable: true, NextRetryAfter: past, ModelStates: map[string]*ModelState{
			"gpt-4": {Unavailable: true, NextRetryAfter: future},
			"gpt-5": {},
		}},
		// Aggregate cooldown probes a healthy registered model.
		{ID: "fallback-open", Provider: "reprobe-test", Unavailable: true, NextRetryAfter: past, ModelStates: map[string]*ModelState{
			"gpt-4": {},
		}},
		// Image-endpoint models are never probed with a chat payload.
		{ID: "nonchat-image", Provider: "reprobe-test", ModelStates: map[string]*ModelState{
			"imagen-4": {Unavailable: true, NextRetryAfter: past},
		}},
		// User-defined compat models have unknown capability: skip, do not probe.
		{ID: "nonchat-compat", Provider: "reprobe-test", ModelStates: map[string]*ModelState{
			"compat-1": {Unavailable: true, NextRetryAfter: past},
		}},
		// Healthy credential.
		{ID: "open", Provider: "reprobe-test"},
	}
	for _, a := range auths {
		if _, err := m.Register(ctx, a); err != nil {
			t.Fatalf("Register(%s): %v", a.ID, err)
		}
	}
	registry.GetGlobalRegistry().RegisterClient("model-mixed", "reprobe-test", []*registry.ModelInfo{{ID: "gpt-4"}, {ID: "gpt-5"}})
	registry.GetGlobalRegistry().RegisterClient("fallback-disabled", "reprobe-test", []*registry.ModelInfo{{ID: "gpt-5"}})
	registry.GetGlobalRegistry().RegisterClient("fallback-blocked", "reprobe-test", []*registry.ModelInfo{{ID: "gpt-5"}})
	registry.GetGlobalRegistry().RegisterClient("fallback-next", "reprobe-test", []*registry.ModelInfo{{ID: "gpt-4"}, {ID: "gpt-5"}})
	registry.GetGlobalRegistry().RegisterClient("fallback-open", "reprobe-test", []*registry.ModelInfo{{ID: "gpt-4"}})
	registry.GetGlobalRegistry().RegisterClient("nonchat-image", "reprobe-test", []*registry.ModelInfo{{ID: "imagen-4", SupportedGenerationMethods: []string{"predict"}, SupportedOutputModalities: []string{"image"}}})
	registry.GetGlobalRegistry().RegisterClient("nonchat-compat", "reprobe-test", []*registry.ModelInfo{{ID: "compat-1", UserDefined: true}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient("model-mixed")
		registry.GetGlobalRegistry().UnregisterClient("fallback-disabled")
		registry.GetGlobalRegistry().UnregisterClient("fallback-blocked")
		registry.GetGlobalRegistry().UnregisterClient("fallback-next")
		registry.GetGlobalRegistry().UnregisterClient("fallback-open")
		registry.GetGlobalRegistry().UnregisterClient("nonchat-image")
		registry.GetGlobalRegistry().UnregisterClient("nonchat-compat")
	})

	candidates := m.collectReprobeCandidates(now)
	got := make(map[string]reprobeCandidate, len(candidates))
	for _, c := range candidates {
		got[c.auth.ID] = c
	}

	cred, ok := got["cred-expired"]
	if !ok {
		t.Fatal("expected credential-scope candidate for cred-expired")
	}
	if cred.routeModel != "" {
		t.Fatalf("cred-expired candidate = %+v, want empty routeModel", cred)
	}
	mixed, ok := got["model-mixed"]
	if !ok {
		t.Fatal("expected model candidate for model-mixed")
	}
	if mixed.routeModel != "gpt-5" {
		t.Fatalf("model-mixed candidate = %+v, want routeModel gpt-5", mixed)
	}
	fallback, ok := got["fallback-open"]
	if !ok {
		t.Fatal("expected fallback candidate for fallback-open")
	}
	if fallback.routeModel != "gpt-4" {
		t.Fatalf("fallback-open candidate = %+v, want routeModel gpt-4", fallback)
	}
	next, ok := got["fallback-next"]
	if !ok {
		t.Fatal("expected fallback candidate for fallback-next")
	}
	if next.routeModel != "gpt-5" {
		t.Fatalf("fallback-next candidate = %+v, want routeModel gpt-5", next)
	}
	for _, absent := range []string{"cred-pending", "disabled", "model-disabled", "fallback-disabled", "fallback-blocked", "nonchat-image", "nonchat-compat", "open"} {
		if _, ok := got[absent]; ok {
			t.Fatalf("unexpected candidate for %s", absent)
		}
	}
}

func TestRunReprobeCandidateSuccessClearsCooldown(t *testing.T) {
	t.Parallel()

	now := time.Now()
	executor := &reprobeTestExecutor{}
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(executor)

	auth := &Auth{ID: "a1-success", Provider: "reprobe-test", Status: StatusActive, ModelStates: map[string]*ModelState{
		"gpt-5": {Unavailable: true, NextRetryAfter: now.Add(-time.Minute)},
	}}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register: %v", err)
	}
	registerReprobeTestModels(t, "a1-success", "gpt-5")

	m.runReprobeCandidate(context.Background(), reprobeCandidate{auth: m.auths["a1-success"].Clone(), routeModel: "gpt-5"})

	if executor.calls != 1 {
		t.Fatalf("executor calls = %d, want 1", executor.calls)
	}
	if marked, ok := executor.lastOpts.Metadata[cliproxyexecutor.CooldownReprobeMetadataKey].(bool); !ok || !marked {
		t.Fatal("expected CooldownReprobeMetadataKey marker on probe options")
	}
	m.mu.RLock()
	state := m.auths["a1-success"].ModelStates["gpt-5"]
	got := state != nil && state.Unavailable
	m.mu.RUnlock()
	if got {
		t.Fatal("expected model cooldown cleared after successful re-probe")
	}
}

func TestRunReprobeCandidateFailureRecools(t *testing.T) {
	t.Parallel()

	now := time.Now()
	executor := &reprobeTestExecutor{err: reprobeTestStatusError{code: http.StatusServiceUnavailable, msg: "upstream 503"}}
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(executor)

	auth := &Auth{ID: "a1-fail", Provider: "reprobe-test", Status: StatusActive, ModelStates: map[string]*ModelState{
		"gpt-5": {Unavailable: true, NextRetryAfter: now.Add(-time.Minute)},
	}}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register: %v", err)
	}
	registerReprobeTestModels(t, "a1-fail", "gpt-5")

	m.runReprobeCandidate(context.Background(), reprobeCandidate{auth: m.auths["a1-fail"].Clone(), routeModel: "gpt-5"})

	if executor.calls != 1 {
		t.Fatalf("executor calls = %d, want 1", executor.calls)
	}
	m.mu.RLock()
	state := m.auths["a1-fail"].ModelStates["gpt-5"]
	stillCooling := state != nil && state.Unavailable && state.NextRetryAfter.After(time.Now())
	m.mu.RUnlock()
	if !stillCooling {
		t.Fatal("expected fresh model cooldown after failed re-probe")
	}
}

func TestReprobeResultSkipsSessionAffinityMutation(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelector(&RoundRobinSelector{})
	defer selector.Stop()

	authA := &Auth{ID: "auth-a"}
	authB := &Auth{ID: "auth-b"}
	auths := []*Auth{authA, authB}
	payload := []byte(`{"messages":[{"role":"user","content":"ping"}]}`)
	pickOpts := cliproxyexecutor.Options{OriginalRequest: payload}

	first, errFirst := selector.Pick(context.Background(), "reprobe-test", "gpt-5", pickOpts, auths)
	if errFirst != nil || first == nil {
		t.Fatalf("first Pick() = (%v, %v)", first, errFirst)
	}

	probeResult := Result{
		AuthID:   first.ID,
		Provider: "reprobe-test",
		Model:    "gpt-5",
		Success:  false,
		Error:    &Error{Code: "rate_limited", Message: "rate limited"},
		Options: cliproxyexecutor.Options{
			OriginalRequest: payload,
			Metadata:        map[string]any{cliproxyexecutor.CooldownReprobeMetadataKey: true},
		},
	}
	selector.OnResult(probeResult)

	second, errSecond := selector.Pick(context.Background(), "reprobe-test", "gpt-5", pickOpts, auths)
	if errSecond != nil || second == nil {
		t.Fatalf("second Pick() = (%v, %v)", second, errSecond)
	}
	if second.ID != first.ID {
		t.Fatalf("marked re-probe result released session binding: got %q, want %q", second.ID, first.ID)
	}

	unmarked := probeResult
	unmarked.Options = cliproxyexecutor.Options{OriginalRequest: payload}
	selector.OnResult(unmarked)

	third, errThird := selector.Pick(context.Background(), "reprobe-test", "gpt-5", pickOpts, auths)
	if errThird != nil || third == nil {
		t.Fatalf("third Pick() = (%v, %v)", third, errThird)
	}
	if third.ID == first.ID {
		t.Fatal("expected unmarked identical failure to release the session binding")
	}
}

func TestRunReprobeCandidatePrepareFailurePublishesNoErrorEvent(t *testing.T) {
	withEnabledErrorQueue(t)
	subscriber, unsubscribe := redisqueue.SubscribeErrors()
	defer unsubscribe()

	now := time.Now()
	executor := &reprobeTestExecutor{prepare: true, prepareErr: errors.New("token refresh failed")}
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(executor)

	auth := &Auth{ID: "a1-prepare", Provider: "reprobe-test", Status: StatusActive, ModelStates: map[string]*ModelState{
		"gpt-5": {Unavailable: true, NextRetryAfter: now.Add(-time.Minute)},
	}}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register: %v", err)
	}
	registerReprobeTestModels(t, "a1-prepare", "gpt-5")

	m.runReprobeCandidate(context.Background(), reprobeCandidate{auth: m.auths["a1-prepare"].Clone(), routeModel: "gpt-5"})

	if executor.calls != 0 {
		t.Fatalf("executor calls = %d, want 0 after prepare failure", executor.calls)
	}
	select {
	case got := <-subscriber:
		t.Fatalf("received prepare-failure probe event %q, want none", string(got))
	default:
	}
}

func TestRunReprobeCandidateSkipsWhenCooldownCleared(t *testing.T) {
	t.Parallel()

	now := time.Now()
	executor := &reprobeTestExecutor{}
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(executor)

	auth := &Auth{ID: "a1-cleared", Provider: "reprobe-test", Status: StatusActive, ModelStates: map[string]*ModelState{
		"gpt-5": {Unavailable: true, NextRetryAfter: now.Add(-time.Minute)},
	}}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register: %v", err)
	}
	registerReprobeTestModels(t, "a1-cleared", "gpt-5")
	candidate := reprobeCandidate{auth: m.auths["a1-cleared"].Clone(), routeModel: "gpt-5"}

	m.mu.Lock()
	live := m.auths["a1-cleared"].ModelStates["gpt-5"]
	live.Unavailable = false
	live.NextRetryAfter = time.Time{}
	m.mu.Unlock()

	m.runReprobeCandidate(context.Background(), candidate)

	if executor.calls != 0 {
		t.Fatalf("executor calls = %d, want 0 after the live cooldown cleared", executor.calls)
	}
}

func TestRunReprobeCandidateSkipsDisabledModel(t *testing.T) {
	t.Parallel()

	now := time.Now()
	executor := &reprobeTestExecutor{}
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(executor)

	auth := &Auth{ID: "a1-disabled", Provider: "reprobe-test", Status: StatusActive, ModelStates: map[string]*ModelState{
		"gpt-5": {Unavailable: true, NextRetryAfter: now.Add(-time.Minute)},
	}}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register: %v", err)
	}
	registerReprobeTestModels(t, "a1-disabled", "gpt-5")
	candidate := reprobeCandidate{auth: m.auths["a1-disabled"].Clone(), routeModel: "gpt-5"}

	m.mu.Lock()
	m.auths["a1-disabled"].ModelStates["gpt-5"].Status = StatusDisabled
	m.mu.Unlock()

	m.runReprobeCandidate(context.Background(), candidate)

	if executor.calls != 0 {
		t.Fatalf("executor calls = %d, want 0 for a disabled model state", executor.calls)
	}
}
