package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// End-to-end coverage for issue 7017 across Execute, ExecuteCount and
// ExecuteStream: a typed transient capacity 429 cools only the reporting
// model, and the terminal error carries the trusted Retry-After to clients
// that do not opt into waiting locally.

type issue7017HintError struct {
	msg   string
	hint  *time.Duration
	scope bool
}

func (e issue7017HintError) Error() string              { return e.msg }
func (e issue7017HintError) StatusCode() int            { return http.StatusTooManyRequests }
func (e issue7017HintError) RetryAfter() *time.Duration { return e.hint }
func (e issue7017HintError) IsTransientRateLimit() bool { return true }
func (e issue7017HintError) IsCredentialScoped() bool   { return e.scope }

type issue7017Executor struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (e *issue7017Executor) Identifier() string { return "codex" }

func (e *issue7017Executor) record(auth *Auth) {
	e.mu.Lock()
	e.calls = append(e.calls, auth.ID)
	e.mu.Unlock()
}

func (e *issue7017Executor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.record(auth)
	return cliproxyexecutor.Response{}, e.err
}

func (e *issue7017Executor) ExecuteStream(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.record(auth)
	return nil, e.err
}

func (e *issue7017Executor) CountTokens(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.record(auth)
	return cliproxyexecutor.Response{}, e.err
}

func (*issue7017Executor) Refresh(_ context.Context, auth *Auth) (*Auth, error) { return auth, nil }

func (*issue7017Executor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *issue7017Executor) callIDs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.calls...)
}

func registerRetryRoundLocalAuthsSkipCoolingOverride(t *testing.T, manager *Manager, authIDs ...string) {
	t.Helper()
	reg := registry.GetGlobalRegistry()
	for _, id := range authIDs {
		reg.RegisterClient(id, "codex", []*registry.ModelInfo{{ID: "gpt-6.1-sol"}, {ID: "gpt-6-astra"}})
		t.Cleanup(func() { reg.UnregisterClient(id) })
		if _, errRegister := manager.Register(WithSkipPersist(context.Background()), &Auth{
			ID:       id,
			Provider: "codex",
			Metadata: map[string]any{"type": "codex"},
		}); errRegister != nil {
			t.Fatalf("register %s: %v", id, errRegister)
		}
	}
}

func issue7017Setup(t *testing.T, retry int, maxWait time.Duration, execErr error, authIDs ...string) (*Manager, *issue7017Executor) {
	t.Helper()
	withQuotaCooldownEnabled(t)
	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(retry, maxWait, 0)
	executor := &issue7017Executor{err: execErr}
	manager.RegisterExecutor(executor)
	reg := registry.GetGlobalRegistry()
	for _, id := range authIDs {
		reg.RegisterClient(id, "codex", []*registry.ModelInfo{{ID: "gpt-6.1-sol"}, {ID: "gpt-6-astra"}})
		t.Cleanup(func() { reg.UnregisterClient(id) })
		if _, errRegister := manager.Register(WithSkipPersist(context.Background()), &Auth{
			ID:       id,
			Provider: "codex",
			Metadata: map[string]any{"type": "codex"},
		}); errRegister != nil {
			t.Fatalf("register %s: %v", id, errRegister)
		}
	}
	return manager, executor
}

func TestIssue7017ExecuteSingleCredentialTerminalRetryAfter(t *testing.T) {
	hint := 256 * time.Second
	manager, executor := issue7017Setup(t, 0, 0, issue7017HintError{msg: issue7017TurnBody(), hint: &hint}, "turn-single")

	_, errExecute := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-6.1-sol"}, cliproxyexecutor.Options{})
	if errExecute == nil {
		t.Fatal("expected terminal 429")
	}
	if got := SafeResponseHeaders(errExecute).Get("Retry-After"); got != "256" {
		t.Fatalf("terminal Retry-After = %q, want 256 (err=%v)", got, errExecute)
	}
	if calls := executor.callIDs(); len(calls) != 1 {
		t.Fatalf("calls = %v, want exactly one upstream attempt", calls)
	}
	got, _ := manager.GetByID("turn-single")
	state := got.ModelStates["gpt-6.1-sol"]
	if state == nil || state.Quota.Exceeded || state.Quota.Reason == "quota" {
		t.Fatalf("model state must cool without quota, got %+v", state)
	}

	// A follow-up request inside the deadline advertises the locally computed
	// remaining time, not the stale raw upstream header value.
	_, errFollowUp := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-6.1-sol"}, cliproxyexecutor.Options{})
	if errFollowUp == nil {
		t.Fatal("expected follow-up rejection while cooling")
	}
	if gotRetry := SafeResponseHeaders(errFollowUp).Get("Retry-After"); gotRetry == "" {
		t.Fatalf("follow-up must carry a trusted Retry-After (err=%v)", errFollowUp)
	}
	if calls := executor.callIDs(); len(calls) != 1 {
		t.Fatalf("cooling follow-up must not reach upstream, calls=%v", calls)
	}
}

func TestIssue7017ExecuteStreamMultiCredentialFailsOver(t *testing.T) {
	hint := 256 * time.Second
	manager, executor := issue7017Setup(t, 0, 0, issue7017HintError{msg: issue7017TurnBody(), hint: &hint}, "turn-a", "turn-b")

	_, errStream := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-6.1-sol"}, cliproxyexecutor.Options{Stream: true})
	if errStream == nil {
		t.Fatal("expected terminal error after both credentials reject")
	}
	calls := executor.callIDs()
	if len(calls) != 2 || calls[0] == calls[1] {
		t.Fatalf("stream must fail over to the second credential, calls=%v", calls)
	}
	if got := SafeResponseHeaders(errStream).Get("Retry-After"); got != "256" {
		t.Fatalf("stream terminal Retry-After = %q, want 256 (err=%v)", got, errStream)
	}
	for _, id := range []string{"turn-a", "turn-b"} {
		got, _ := manager.GetByID(id)
		state := got.ModelStates["gpt-6.1-sol"]
		if state == nil || state.Quota.Exceeded || state.Quota.Reason == "quota" {
			t.Fatalf("%s model state must not be quota: %+v", id, state)
		}
		if sibling := got.ModelStates["gpt-6-astra"]; sibling != nil && (sibling.Unavailable || sibling.Quota.Exceeded) {
			t.Fatalf("%s sibling model must stay available: %+v", id, sibling)
		}
	}
}

func TestIssue7017ExecuteCountCoolsWithoutQuota(t *testing.T) {
	hint := 60 * time.Second
	manager, _ := issue7017Setup(t, 0, 0, issue7017HintError{msg: issue7017TurnBody(), hint: &hint}, "turn-count")

	_, errCount := manager.ExecuteCount(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-6.1-sol"}, cliproxyexecutor.Options{})
	if errCount == nil {
		t.Fatal("expected terminal 429")
	}
	if got := SafeResponseHeaders(errCount).Get("Retry-After"); got != "60" {
		t.Fatalf("count terminal Retry-After = %q, want 60 (err=%v)", got, errCount)
	}
	got, _ := manager.GetByID("turn-count")
	state := got.ModelStates["gpt-6.1-sol"]
	if state == nil || state.Quota.Exceeded || state.Quota.Reason == "quota" {
		t.Fatalf("count model state must not be quota: %+v", state)
	}
}

func TestIssue7017CredentialScopedTransientFallsBackToQuota(t *testing.T) {
	hint := 60 * time.Second
	manager := NewManager(nil, nil, nil)
	withQuotaCooldownEnabled(t)
	auth := &Auth{ID: "turn-scoped", Provider: "codex", Metadata: map[string]any{"type": "codex"}}
	issue7017Register(t, manager, auth)

	resultErr := resultErrorFromError(issue7017HintError{msg: issue7017TurnBody(), hint: &hint, scope: true})
	if resultErr.Code == ErrorCodeTransientRateLimit {
		t.Fatal("credential-scoped errors must not downgrade to transient rate limit")
	}
	manager.MarkResult(context.Background(), Result{
		AuthID:          auth.ID,
		Provider:        "codex",
		Model:           "gpt-6.1-sol",
		Success:         false,
		RetryAfter:      &hint,
		CredentialScope: true,
		Error:           resultErr,
	})
	got, _ := manager.GetByID(auth.ID)
	if !got.Quota.Exceeded || got.Quota.Reason != "credential_quota" {
		t.Fatalf("credential-scoped quota must still apply, got %+v", got.Quota)
	}
}

func TestIssue7017TransientZeroCooldownKeepsQuotaDeadline(t *testing.T) {
	withQuotaCooldownEnabled(t)
	prev := transientErrorCooldownSeconds.Load()
	transientErrorCooldownSeconds.Store(-1)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(prev) })

	future := time.Now().Add(2 * time.Hour)
	manager := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "turn-zero-cooldown",
		Provider: "codex",
		Metadata: map[string]any{"type": "codex"},
		ModelStates: map[string]*ModelState{
			"gpt-6.1-sol": {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: future,
				Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: future, BackoffLevel: 2},
			},
		},
	}
	issue7017Register(t, manager, auth)

	manager.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: "codex",
		Model:    "gpt-6.1-sol",
		Success:  false,
		Error: &Error{
			Code:       ErrorCodeTransientRateLimit,
			Message:    issue7017TurnBody(),
			Retryable:  true,
			HTTPStatus: http.StatusTooManyRequests,
		},
	})

	got, _ := manager.GetByID(auth.ID)
	state := got.ModelStates["gpt-6.1-sol"]
	if state == nil {
		t.Fatal("expected model state")
	}
	if !state.Quota.Exceeded || !state.Quota.NextRecoverAt.Equal(future) || state.Quota.BackoffLevel != 2 {
		t.Fatalf("live quota deadline must survive a zero transient cooldown: %+v", state.Quota)
	}
	if !state.NextRetryAfter.Equal(future) || !state.Unavailable {
		t.Fatalf("model must stay blocked until the quota deadline: next=%v unavailable=%t", state.NextRetryAfter, state.Unavailable)
	}
}
