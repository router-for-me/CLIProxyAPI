package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type reprobeTestStatusError struct {
	code int
	msg  string
}

func (e reprobeTestStatusError) Error() string   { return e.msg }
func (e reprobeTestStatusError) StatusCode() int { return e.code }

type reprobeTestExecutor struct {
	calls    int
	err      error
	lastOpts cliproxyexecutor.Options
}

func (e *reprobeTestExecutor) Identifier() string { return "reprobe-test" }

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
		// Healthy credential.
		{ID: "open", Provider: "reprobe-test"},
	}
	for _, a := range auths {
		if _, err := m.Register(ctx, a); err != nil {
			t.Fatalf("Register(%s): %v", a.ID, err)
		}
	}

	candidates := m.collectReprobeCandidates(now)
	got := make(map[string]reprobeCandidate, len(candidates))
	for _, c := range candidates {
		got[c.auth.ID] = c
	}

	cred, ok := got["cred-expired"]
	if !ok {
		t.Fatal("expected credential-scope candidate for cred-expired")
	}
	if !cred.credentialScope || cred.routeModel != "" {
		t.Fatalf("cred-expired candidate = %+v, want credentialScope with empty routeModel", cred)
	}
	mixed, ok := got["model-mixed"]
	if !ok {
		t.Fatal("expected model candidate for model-mixed")
	}
	if mixed.routeModel != "gpt-5" || mixed.credentialScope {
		t.Fatalf("model-mixed candidate = %+v, want routeModel gpt-5", mixed)
	}
	for _, absent := range []string{"cred-pending", "disabled", "open"} {
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

	auth := &Auth{ID: "a1", Provider: "reprobe-test", Status: StatusActive, ModelStates: map[string]*ModelState{
		"gpt-5": {Unavailable: true, NextRetryAfter: now.Add(-time.Minute)},
	}}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register: %v", err)
	}

	m.runReprobeCandidate(context.Background(), reprobeCandidate{auth: m.auths["a1"].Clone(), routeModel: "gpt-5"})

	if executor.calls != 1 {
		t.Fatalf("executor calls = %d, want 1", executor.calls)
	}
	if marked, ok := executor.lastOpts.Metadata[cliproxyexecutor.CooldownReprobeMetadataKey].(bool); !ok || !marked {
		t.Fatal("expected CooldownReprobeMetadataKey marker on probe options")
	}
	m.mu.RLock()
	state := m.auths["a1"].ModelStates["gpt-5"]
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

	auth := &Auth{ID: "a1", Provider: "reprobe-test", Status: StatusActive, ModelStates: map[string]*ModelState{
		"gpt-5": {Unavailable: true, NextRetryAfter: now.Add(-time.Minute)},
	}}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register: %v", err)
	}

	m.runReprobeCandidate(context.Background(), reprobeCandidate{auth: m.auths["a1"].Clone(), routeModel: "gpt-5"})

	if executor.calls != 1 {
		t.Fatalf("executor calls = %d, want 1", executor.calls)
	}
	m.mu.RLock()
	state := m.auths["a1"].ModelStates["gpt-5"]
	stillCooling := state != nil && state.Unavailable && state.NextRetryAfter.After(time.Now())
	m.mu.RUnlock()
	if !stillCooling {
		t.Fatal("expected fresh model cooldown after failed re-probe")
	}
}
