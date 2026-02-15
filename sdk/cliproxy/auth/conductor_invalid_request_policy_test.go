package auth

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

type streamScriptedOutcome struct {
	chunks []cliproxyexecutor.StreamChunk
	err    error
}

type providerScriptedExecutor struct {
	mu sync.Mutex

	provider string

	executeOutcomes map[string][]scriptedOutcome
	streamOutcomes  map[string][]streamScriptedOutcome

	executeCalls map[string]int
	streamCalls  map[string]int
}

func newProviderScriptedExecutor(provider string, executeOutcomes map[string][]scriptedOutcome, streamOutcomes map[string][]streamScriptedOutcome) *providerScriptedExecutor {
	return &providerScriptedExecutor{
		provider:        provider,
		executeOutcomes: executeOutcomes,
		streamOutcomes:  streamOutcomes,
		executeCalls:    make(map[string]int),
		streamCalls:     make(map[string]int),
	}
}

func (e *providerScriptedExecutor) Identifier() string {
	return e.provider
}

func (e *providerScriptedExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	seq := e.executeOutcomes[auth.ID]
	idx := e.executeCalls[auth.ID]
	e.executeCalls[auth.ID] = idx + 1
	if len(seq) == 0 {
		return cliproxyexecutor.Response{}, nil
	}
	if idx >= len(seq) {
		last := seq[len(seq)-1]
		return last.resp, last.err
	}
	return seq[idx].resp, seq[idx].err
}

func (e *providerScriptedExecutor) ExecuteStream(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (<-chan cliproxyexecutor.StreamChunk, error) {
	e.mu.Lock()
	seq := e.streamOutcomes[auth.ID]
	idx := e.streamCalls[auth.ID]
	e.streamCalls[auth.ID] = idx + 1

	if len(seq) == 0 {
		e.mu.Unlock()
		ch := make(chan cliproxyexecutor.StreamChunk)
		close(ch)
		return ch, nil
	}

	var outcome streamScriptedOutcome
	if idx >= len(seq) {
		outcome = seq[len(seq)-1]
	} else {
		outcome = seq[idx]
	}
	e.mu.Unlock()

	if outcome.err != nil {
		return nil, outcome.err
	}

	ch := make(chan cliproxyexecutor.StreamChunk, len(outcome.chunks))
	for _, chunk := range outcome.chunks {
		ch <- chunk
	}
	close(ch)
	return ch, nil
}

func (e *providerScriptedExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *providerScriptedExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{
		Code:       "not_implemented",
		Message:    "CountTokens not implemented in test executor",
		HTTPStatus: http.StatusNotImplemented,
	}
}

func (e *providerScriptedExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{
		Code:       "not_implemented",
		Message:    "HttpRequest not implemented in test executor",
		HTTPStatus: http.StatusNotImplemented,
	}
}

func (e *providerScriptedExecutor) ExecuteCalls(authID string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.executeCalls[authID]
}

func (e *providerScriptedExecutor) StreamCalls(authID string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.streamCalls[authID]
}

func registerTestAuthForProviderModel(t *testing.T, manager *Manager, authID, provider, model string) {
	t.Helper()

	auth := &Auth{
		ID:       authID,
		Provider: provider,
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

func TestExecuteMixedOnce_NonClaudeInvalidRequestStillStopsFallback(t *testing.T) {
	model := uniqueTestModel(t)
	executor := newProviderScriptedExecutor(
		"openai",
		map[string][]scriptedOutcome{
			"a-auth": {
				{
					err: &Error{
						Code:       "invalid_request_error",
						Message:    "invalid_request_error: bad payload",
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
	registerTestAuthForProviderModel(t, manager, "a-auth", "openai", model)
	registerTestAuthForProviderModel(t, manager, "b-auth", "openai", model)

	_, err := manager.Execute(context.Background(), []string{"openai"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err == nil {
		t.Fatalf("Execute() expected invalid_request_error, got nil")
	}
	if status := statusCodeFromError(err); status != http.StatusBadRequest {
		t.Fatalf("Execute() status = %d, want %d", status, http.StatusBadRequest)
	}
	if !strings.Contains(err.Error(), "invalid_request_error") {
		t.Fatalf("Execute() error = %q, want invalid_request_error", err.Error())
	}
	if got := executor.ExecuteCalls("a-auth"); got != 1 {
		t.Fatalf("ExecuteCalls(a-auth) = %d, want 1", got)
	}
	if got := executor.ExecuteCalls("b-auth"); got != 0 {
		t.Fatalf("ExecuteCalls(b-auth) = %d, want 0", got)
	}
}

func TestExecuteStreamMixedOnce_ClaudeInvalidRequestFallsBack(t *testing.T) {
	model := uniqueTestModel(t)
	executor := newProviderScriptedExecutor(
		"claude",
		nil,
		map[string][]streamScriptedOutcome{
			"a-auth": {
				{
					err: &Error{
						Code:       "invalid_request_error",
						Message:    "invalid_request_error: unsupported structured output",
						HTTPStatus: http.StatusBadRequest,
					},
				},
			},
			"b-auth": {
				{
					chunks: []cliproxyexecutor.StreamChunk{
						{Payload: []byte("event: message_start\n\n")},
					},
				},
			},
		},
	)

	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	registerTestAuthForProviderModel(t, manager, "a-auth", "claude", model)
	registerTestAuthForProviderModel(t, manager, "b-auth", "claude", model)

	stream, err := manager.ExecuteStream(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v, want nil", err)
	}

	var received int
	for chunk := range stream {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream chunk error: %v", chunk.Err)
		}
		if len(chunk.Payload) > 0 {
			received++
		}
	}

	if received == 0 {
		t.Fatalf("expected to receive stream payload from fallback auth")
	}
	if got := executor.StreamCalls("a-auth"); got != 1 {
		t.Fatalf("StreamCalls(a-auth) = %d, want 1", got)
	}
	if got := executor.StreamCalls("b-auth"); got != 1 {
		t.Fatalf("StreamCalls(b-auth) = %d, want 1", got)
	}
}

func TestExecuteStreamMixedOnce_NonClaudeInvalidRequestStillStopsFallback(t *testing.T) {
	model := uniqueTestModel(t)
	executor := newProviderScriptedExecutor(
		"openai",
		nil,
		map[string][]streamScriptedOutcome{
			"a-auth": {
				{
					err: &Error{
						Code:       "invalid_request_error",
						Message:    "invalid_request_error: bad stream payload",
						HTTPStatus: http.StatusBadRequest,
					},
				},
			},
			"b-auth": {
				{
					chunks: []cliproxyexecutor.StreamChunk{
						{Payload: []byte("event: message_start\n\n")},
					},
				},
			},
		},
	)

	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	registerTestAuthForProviderModel(t, manager, "a-auth", "openai", model)
	registerTestAuthForProviderModel(t, manager, "b-auth", "openai", model)

	_, err := manager.ExecuteStream(context.Background(), []string{"openai"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err == nil {
		t.Fatalf("ExecuteStream() expected invalid_request_error, got nil")
	}
	if status := statusCodeFromError(err); status != http.StatusBadRequest {
		t.Fatalf("ExecuteStream() status = %d, want %d", status, http.StatusBadRequest)
	}
	if !strings.Contains(err.Error(), "invalid_request_error") {
		t.Fatalf("ExecuteStream() error = %q, want invalid_request_error", err.Error())
	}
	if got := executor.StreamCalls("a-auth"); got != 1 {
		t.Fatalf("StreamCalls(a-auth) = %d, want 1", got)
	}
	if got := executor.StreamCalls("b-auth"); got != 0 {
		t.Fatalf("StreamCalls(b-auth) = %d, want 0", got)
	}
}

func TestMarkResult_NotFoundKeepsModelLongCooldown(t *testing.T) {
	model := uniqueTestModel(t)
	manager := NewManager(nil, nil, nil)
	registerTestAuthForProviderModel(t, manager, "a-auth", "claude", model)

	before := time.Now()
	manager.MarkResult(context.Background(), Result{
		AuthID:   "a-auth",
		Provider: "claude",
		Model:    model,
		Success:  false,
		Error: &Error{
			Code:       "not_found",
			Message:    "model not found",
			HTTPStatus: http.StatusNotFound,
		},
	})
	after := time.Now()

	authA, ok := manager.GetByID("a-auth")
	if !ok || authA == nil {
		t.Fatalf("expected a-auth to be present")
	}
	state := authA.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state for %s", model)
	}
	if state.NextRetryAfter.IsZero() {
		t.Fatalf("expected non-zero NextRetryAfter for 404")
	}

	minExpected := before.Add(11*time.Hour + 59*time.Minute)
	maxExpected := after.Add(12*time.Hour + time.Minute)
	if state.NextRetryAfter.Before(minExpected) || state.NextRetryAfter.After(maxExpected) {
		t.Fatalf("NextRetryAfter = %v, want around 12h window [%v, %v]", state.NextRetryAfter, minExpected, maxExpected)
	}
}

func TestMarkResult_TooManyRequestsKeepsQuotaCooldown(t *testing.T) {
	model := uniqueTestModel(t)
	manager := NewManager(nil, nil, nil)
	registerTestAuthForProviderModel(t, manager, "a-auth", "claude", model)

	retryAfter := 5 * time.Second
	before := time.Now()
	manager.MarkResult(context.Background(), Result{
		AuthID:     "a-auth",
		Provider:   "claude",
		Model:      model,
		Success:    false,
		RetryAfter: &retryAfter,
		Error: &Error{
			Code:       "rate_limit",
			Message:    "too many requests",
			HTTPStatus: http.StatusTooManyRequests,
		},
	})
	after := time.Now()

	authA, ok := manager.GetByID("a-auth")
	if !ok || authA == nil {
		t.Fatalf("expected a-auth to be present")
	}
	state := authA.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state for %s", model)
	}
	if !state.Quota.Exceeded {
		t.Fatalf("expected quota exceeded for 429")
	}
	if state.NextRetryAfter.IsZero() {
		t.Fatalf("expected non-zero NextRetryAfter for 429")
	}

	minExpected := before.Add(4 * time.Second)
	maxExpected := after.Add(6 * time.Second)
	if state.NextRetryAfter.Before(minExpected) || state.NextRetryAfter.After(maxExpected) {
		t.Fatalf("NextRetryAfter = %v, want around 5s window [%v, %v]", state.NextRetryAfter, minExpected, maxExpected)
	}
}
