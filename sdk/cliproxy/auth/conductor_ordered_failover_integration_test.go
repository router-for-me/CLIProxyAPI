package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// failoverExecutor is a stub ProviderExecutor that returns a configurable
// error or response per requested model. It records every call so the test
// can assert which candidates the conductor tried and in what order.
type failoverExecutor struct {
	id string

	mu          sync.Mutex
	errByModel  map[string]error
	respByModel map[string]cliproxyexecutor.Response
	calls       []string
}

func (e *failoverExecutor) Identifier() string { return e.id }

func (e *failoverExecutor) Execute(ctx context.Context, _ *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.calls = append(e.calls, req.Model)
	model := req.Model
	e.mu.Unlock()
	if err, ok := e.errByModel[model]; ok {
		return cliproxyexecutor.Response{}, err
	}
	if resp, ok := e.respByModel[model]; ok {
		return resp, nil
	}
	return cliproxyexecutor.Response{Payload: []byte(model)}, nil
}

func (e *failoverExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "stream not implemented"}
}

func (e *failoverExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) { return auth, nil }

func (e *failoverExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "CountTokens not implemented"}
}

func (e *failoverExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "HttpRequest not implemented"}
}

func (e *failoverExecutor) Calls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.calls))
	copy(out, e.calls)
	return out
}

// newOrderedFailoverManager builds a Manager wired for ordered-failover
// integration tests: one claude auth, one executor that returns configurable
// errors per model, and an ordered candidate pool mapping the alias "sonnet"
// to [claude-opus-4, gpt-5, kimi-k3].
func newOrderedFailoverManager(t *testing.T) (*Manager, *failoverExecutor) {
	t.Helper()
	const provider = "claude"
	manager := NewManager(nil, nil, nil)
	executor := &failoverExecutor{
		id:          provider,
		errByModel:  make(map[string]error),
		respByModel: map[string]cliproxyexecutor.Response{
			// default success payloads
		},
	}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {
			{Name: "claude-opus-4", Alias: "sonnet"},
			{Name: "gpt-5", Alias: "sonnet"},
			{Name: "kimi-k3", Alias: "sonnet"},
		},
	})

	auth := &Auth{
		ID:       "claude-failover-auth",
		Provider: provider,
		Status:   StatusActive,
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{
		{ID: "claude-opus-4"},
		{ID: "gpt-5"},
		{ID: "kimi-k3"},
		{ID: "sonnet"},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})
	manager.RefreshSchedulerEntry(auth.ID)
	// Let any async registration settle.
	time.Sleep(20 * time.Millisecond)
	return manager, executor
}

// TestOrderedFailover_AdvancesOnRetryable429 proves AC#3+AC#4: when the first
// candidate returns 429 (retryable pre-first-byte), the conductor advances to
// the next candidate in the ordered pool.
func TestOrderedFailover_AdvancesOnRetryable429(t *testing.T) {
	manager, executor := newOrderedFailoverManager(t)
	executor.errByModel["claude-opus-4"] = &Error{HTTPStatus: 429, Message: "rate limited"}
	executor.respByModel["gpt-5"] = cliproxyexecutor.Response{Payload: []byte("gpt-5-ok")}

	resp, err := manager.Execute(context.Background(), []string{"claude"},
		cliproxyexecutor.Request{Model: "sonnet"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute error = %v, want success after advancing to gpt-5", err)
	}
	if string(resp.Payload) != "gpt-5-ok" {
		t.Fatalf("Execute payload = %q, want gpt-5-ok", string(resp.Payload))
	}
	calls := executor.Calls()
	if len(calls) < 2 {
		t.Fatalf("expected at least 2 calls (claude-opus-4 then gpt-5), got %d: %v", len(calls), calls)
	}
	// First call must be the primary candidate.
	if calls[0] != "claude-opus-4" {
		t.Fatalf("first call = %q, want claude-opus-4", calls[0])
	}
}

// TestOrderedFailover_AdvancesOnRetryable503 proves AC#4: 5xx advances.
func TestOrderedFailover_AdvancesOnRetryable503(t *testing.T) {
	manager, executor := newOrderedFailoverManager(t)
	executor.errByModel["claude-opus-4"] = &Error{HTTPStatus: 503, Message: "unavailable"}
	executor.errByModel["gpt-5"] = &Error{HTTPStatus: 504, Message: "gateway timeout"}
	executor.respByModel["kimi-k3"] = cliproxyexecutor.Response{Payload: []byte("kimi-ok")}

	resp, err := manager.Execute(context.Background(), []string{"claude"},
		cliproxyexecutor.Request{Model: "sonnet"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute error = %v, want success after advancing through the chain", err)
	}
	if string(resp.Payload) != "kimi-ok" {
		t.Fatalf("Execute payload = %q, want kimi-ok", string(resp.Payload))
	}
}

// TestOrderedFailover_StopsOnPermanent401 proves AC#4: 401 stops the chain.
func TestOrderedFailover_StopsOnPermanent401(t *testing.T) {
	manager, executor := newOrderedFailoverManager(t)
	executor.errByModel["claude-opus-4"] = &Error{HTTPStatus: 401, Message: "unauthorized"}

	_, err := manager.Execute(context.Background(), []string{"claude"},
		cliproxyexecutor.Request{Model: "sonnet"}, cliproxyexecutor.Options{})
	if err == nil {
		t.Fatal("Execute error = nil, want unauthorized error")
	}
	calls := executor.Calls()
	// Only claude-opus-4 should have been called; the chain must stop on 401.
	for _, c := range calls {
		if c != "claude-opus-4" {
			t.Fatalf("chain advanced past permanent 401; call observed: %q (all calls: %v)", c, calls)
		}
	}
}

// TestOrderedFailover_StopsOnPermanent400 proves AC#4: 400 invalid_request stops.
func TestOrderedFailover_StopsOnPermanent400(t *testing.T) {
	manager, executor := newOrderedFailoverManager(t)
	executor.errByModel["claude-opus-4"] = &Error{HTTPStatus: 400, Message: "invalid_request_error: bad shape"}

	_, err := manager.Execute(context.Background(), []string{"claude"},
		cliproxyexecutor.Request{Model: "sonnet"}, cliproxyexecutor.Options{})
	if err == nil {
		t.Fatal("Execute error = nil, want 400 permanent error")
	}
	calls := executor.Calls()
	for _, c := range calls {
		if c != "claude-opus-4" {
			t.Fatalf("chain advanced past permanent 400; call observed: %q (all calls: %v)", c, calls)
		}
	}
}

// TestOrderedFailover_ReturnsLastErrWhenChainExhausted proves that when every
// candidate returns a retryable error, the wrapper returns the last error
// annotated with the ordered fallback trace.
func TestOrderedFailover_ReturnsLastErrWhenChainExhausted(t *testing.T) {
	manager, executor := newOrderedFailoverManager(t)
	executor.errByModel["claude-opus-4"] = &Error{HTTPStatus: 503, Message: "primary unavailable"}
	executor.errByModel["gpt-5"] = &Error{HTTPStatus: 503, Message: "secondary unavailable"}
	executor.errByModel["kimi-k3"] = &Error{HTTPStatus: 503, Message: "terminal unavailable"}

	_, err := manager.Execute(context.Background(), []string{"claude"},
		cliproxyexecutor.Request{Model: "sonnet"}, cliproxyexecutor.Options{})
	if err == nil {
		t.Fatal("Execute error = nil, want the terminal candidate's 503 error")
	}
	// Every candidate was tried in order.
	calls := executor.Calls()
	wantSequence := []string{"claude-opus-4", "gpt-5", "kimi-k3"}
	if len(calls) < len(wantSequence) {
		t.Fatalf("expected at least %d calls, got %d: %v", len(wantSequence), len(calls), calls)
	}
	for i, want := range wantSequence {
		if calls[i] != want {
			t.Fatalf("call[%d] = %q, want %q (full sequence: %v)", i, calls[i], want, calls)
		}
	}
}

// TestOrderedFailover_SameModelSameAuthNoOpChain proves that when the alias
// resolves to a single candidate, the legacy executeMixedOnce path is used
// unchanged (no chain overhead).
func TestOrderedFailover_SingleCandidatePreservesLegacy(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	const provider = "claude"
	executor := &failoverExecutor{
		id:          provider,
		respByModel: map[string]cliproxyexecutor.Response{"claude-opus-4": {Payload: []byte("ok")}},
	}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {
			{Name: "claude-opus-4", Alias: "claude-sonnet-4"}, // single candidate
		},
	})

	auth := &Auth{
		ID:       "single-candidate-auth",
		Provider: provider,
		Status:   StatusActive,
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{
		{ID: "claude-opus-4"},
		{ID: "claude-sonnet-4"},
	})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	manager.RefreshSchedulerEntry(auth.ID)
	time.Sleep(20 * time.Millisecond)

	resp, err := manager.Execute(context.Background(), []string{"claude"},
		cliproxyexecutor.Request{Model: "claude-sonnet-4"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if string(resp.Payload) != "ok" {
		t.Fatalf("Execute payload = %q, want ok", string(resp.Payload))
	}
}
