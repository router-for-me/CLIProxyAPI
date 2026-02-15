package auth

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

type scriptedOutcome struct {
	resp cliproxyexecutor.Response
	err  error
}

type scriptedProviderExecutor struct {
	mu sync.Mutex

	executeOutcomes map[string][]scriptedOutcome
	countOutcomes   map[string][]scriptedOutcome
	executeCalls    map[string]int
	countCalls      map[string]int
}

func newScriptedProviderExecutor(executeOutcomes, countOutcomes map[string][]scriptedOutcome) *scriptedProviderExecutor {
	return &scriptedProviderExecutor{
		executeOutcomes: executeOutcomes,
		countOutcomes:   countOutcomes,
		executeCalls:    make(map[string]int),
		countCalls:      make(map[string]int),
	}
}

func (e *scriptedProviderExecutor) Identifier() string {
	return "claude"
}

func (e *scriptedProviderExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.nextOutcome(auth.ID, false)
}

func (e *scriptedProviderExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (<-chan cliproxyexecutor.StreamChunk, error) {
	return nil, &Error{
		Code:       "not_implemented",
		Message:    "ExecuteStream not implemented in test executor",
		HTTPStatus: http.StatusNotImplemented,
	}
}

func (e *scriptedProviderExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *scriptedProviderExecutor) CountTokens(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.nextOutcome(auth.ID, true)
}

func (e *scriptedProviderExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{
		Code:       "not_implemented",
		Message:    "HttpRequest not implemented in test executor",
		HTTPStatus: http.StatusNotImplemented,
	}
}

func (e *scriptedProviderExecutor) ExecuteCalls(authID string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.executeCalls[authID]
}

func (e *scriptedProviderExecutor) CountCalls(authID string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.countCalls[authID]
}

func (e *scriptedProviderExecutor) nextOutcome(authID string, count bool) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	var (
		seq []scriptedOutcome
		idx int
	)
	if count {
		seq = e.countOutcomes[authID]
		idx = e.countCalls[authID]
		e.countCalls[authID] = idx + 1
	} else {
		seq = e.executeOutcomes[authID]
		idx = e.executeCalls[authID]
		e.executeCalls[authID] = idx + 1
	}

	if len(seq) == 0 {
		return cliproxyexecutor.Response{}, nil
	}
	if idx >= len(seq) {
		last := seq[len(seq)-1]
		return last.resp, last.err
	}
	outcome := seq[idx]
	return outcome.resp, outcome.err
}

func registerTestAuthForModel(t *testing.T, manager *Manager, authID, model string) {
	t.Helper()

	auth := &Auth{
		ID:       authID,
		Provider: "claude",
		Status:   StatusActive,
		Metadata: map[string]any{"email": authID + "@test.local"},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth %s: %v", authID, err)
	}

	registry.GetGlobalRegistry().RegisterClient(authID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(authID)
	})
}

func uniqueTestModel(t *testing.T) string {
	t.Helper()
	return "count-tokens-" + strings.ReplaceAll(strings.ToLower(t.Name()), "/", "-") + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)
}

func TestExecuteCountMixedOnce_FourXXFallsThroughWithoutCooldown(t *testing.T) {
	model := uniqueTestModel(t)
	executor := newScriptedProviderExecutor(
		nil,
		map[string][]scriptedOutcome{
			"a-auth": {
				{
					err: &Error{
						Code:       "not_found",
						Message:    "count_tokens route missing",
						HTTPStatus: http.StatusNotFound,
					},
				},
			},
			"b-auth": {
				{resp: cliproxyexecutor.Response{Payload: []byte(`{"input_tokens":12}`)}},
			},
		},
	)

	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	registerTestAuthForModel(t, manager, "a-auth", model)
	registerTestAuthForModel(t, manager, "b-auth", model)

	resp, err := manager.ExecuteCount(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("ExecuteCount() error = %v", err)
	}
	if len(resp.Payload) == 0 {
		t.Fatalf("ExecuteCount() returned empty payload")
	}
	if got := executor.CountCalls("a-auth"); got != 1 {
		t.Fatalf("CountCalls(a-auth) = %d, want 1", got)
	}
	if got := executor.CountCalls("b-auth"); got != 1 {
		t.Fatalf("CountCalls(b-auth) = %d, want 1", got)
	}

	authA, ok := manager.GetByID("a-auth")
	if !ok || authA == nil {
		t.Fatalf("expected a-auth to be present")
	}
	if authA.ModelStates != nil {
		if state, exists := authA.ModelStates[model]; exists && state != nil {
			t.Fatalf("expected no cooldown state for a-auth on 4xx count_tokens, got unavailable=%v nextRetryAfter=%v", state.Unavailable, state.NextRetryAfter)
		}
	}
}

func TestExecuteCountMixedOnce_AllFourXXDoesNotFreezeKeys(t *testing.T) {
	model := uniqueTestModel(t)
	executor := newScriptedProviderExecutor(
		nil,
		map[string][]scriptedOutcome{
			"a-auth": {
				{
					err: &Error{
						Code:       "not_found",
						Message:    "count_tokens route missing",
						HTTPStatus: http.StatusNotFound,
					},
				},
			},
			"b-auth": {
				{
					err: &Error{
						Code:       "not_found",
						Message:    "count_tokens route missing",
						HTTPStatus: http.StatusNotFound,
					},
				},
			},
		},
	)

	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	registerTestAuthForModel(t, manager, "a-auth", model)
	registerTestAuthForModel(t, manager, "b-auth", model)

	for i := 0; i < 2; i++ {
		_, err := manager.ExecuteCount(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
		if err == nil {
			t.Fatalf("ExecuteCount() attempt %d expected error, got nil", i+1)
		}
		if status := statusCodeFromError(err); status != http.StatusNotFound {
			t.Fatalf("ExecuteCount() attempt %d status = %d, want %d; err=%v", i+1, status, http.StatusNotFound, err)
		}
	}

	if got := executor.CountCalls("a-auth"); got != 2 {
		t.Fatalf("CountCalls(a-auth) = %d, want 2", got)
	}
	if got := executor.CountCalls("b-auth"); got != 2 {
		t.Fatalf("CountCalls(b-auth) = %d, want 2", got)
	}
}

func TestExecuteMixedOnce_ClaudeInvalidRequestErrorFallsBack(t *testing.T) {
	model := uniqueTestModel(t)
	executor := newScriptedProviderExecutor(
		map[string][]scriptedOutcome{
			"a-auth": {
				{
					err: &Error{
						Code:       "invalid_request_error",
						Message:    "invalid_request_error: structured output schema is invalid",
						HTTPStatus: http.StatusBadRequest,
					},
				},
			},
			"b-auth": {
				{resp: cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}},
			},
		},
		nil,
	)

	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	registerTestAuthForModel(t, manager, "a-auth", model)
	registerTestAuthForModel(t, manager, "b-auth", model)

	resp, err := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if string(resp.Payload) != `{"ok":true}` {
		t.Fatalf("Execute() payload = %q, want %q", string(resp.Payload), `{"ok":true}`)
	}
	if got := executor.ExecuteCalls("a-auth"); got != 1 {
		t.Fatalf("ExecuteCalls(a-auth) = %d, want 1", got)
	}
	if got := executor.ExecuteCalls("b-auth"); got != 1 {
		t.Fatalf("ExecuteCalls(b-auth) = %d, want 1", got)
	}
}
