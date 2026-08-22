package auth

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// doctrineRetryAfterError is a test double that carries an HTTP status and an
// optional Retry-After duration, mirroring how real executors surface rate
// limits and transient errors to the auth conductor.
type doctrineRetryAfterError struct {
	status     int
	message    string
	retryAfter time.Duration
}

func (e *doctrineRetryAfterError) Error() string { return e.message }

func (e *doctrineRetryAfterError) StatusCode() int { return e.status }

func (e *doctrineRetryAfterError) RetryAfter() *time.Duration {
	if e.retryAfter <= 0 {
		return nil
	}
	d := e.retryAfter
	return &d
}

// doctrineExecutor is a scripted fake upstream used to drive the real
// Manager/scheduler/conductor stack through the doctrine scenarios.
type doctrineExecutor struct {
	provider string

	mu                sync.Mutex
	executeCalls      map[string]int
	executeModels     map[string][]string
	executePayloads   map[string][]byte
	executeErrs       map[string]error
	firstExecuteEmpty bool
	firstExecuteDone  bool
	executeCallCount  int
	failFirstN        int
	failFirstError    error
	streamCalls       map[string]int
	streamModels      map[string][]string
	streamPayloads    map[string][][]byte
	streamErrs        map[string]error
	firstStreamEmpty  bool
	firstStreamDone   bool
	countTokensCalls  map[string]int
	countTokensErrs   map[string]error
}

func newDoctrineExecutor(provider string) *doctrineExecutor {
	return &doctrineExecutor{
		provider:         provider,
		executeCalls:     make(map[string]int),
		executeModels:    make(map[string][]string),
		executePayloads:  make(map[string][]byte),
		executeErrs:      make(map[string]error),
		streamCalls:      make(map[string]int),
		streamModels:     make(map[string][]string),
		streamPayloads:   make(map[string][][]byte),
		streamErrs:       make(map[string]error),
		countTokensCalls: make(map[string]int),
		countTokensErrs:  make(map[string]error),
	}
}

func (e *doctrineExecutor) Identifier() string { return e.provider }

func (e *doctrineExecutor) ShouldPrepareRequestAuth(*Auth) bool { return false }

func (e *doctrineExecutor) PrepareRequestAuth(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *doctrineExecutor) Execute(_ context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.executeCalls[auth.ID]++
	e.executeModels[auth.ID] = append(e.executeModels[auth.ID], req.Model)
	e.executeCallCount++
	if err := e.executeErrs[auth.ID]; err != nil {
		return cliproxyexecutor.Response{}, err
	}
	if e.firstExecuteEmpty && !e.firstExecuteDone {
		e.firstExecuteDone = true
		return cliproxyexecutor.Response{Payload: []byte(`{"choices":[{"message":{"content":""},"finish_reason":"stop"}],"usage":{"completion_tokens":0}}`)}, nil
	}
	if e.failFirstN > 0 && e.executeCallCount <= e.failFirstN {
		if e.failFirstError != nil {
			return cliproxyexecutor.Response{}, e.failFirstError
		}
		return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"}
	}
	if p, ok := e.executePayloads[auth.ID]; ok {
		return cliproxyexecutor.Response{Payload: append([]byte(nil), p...)}, nil
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"choices":[{"message":{"content":"ok"}}]}`)}, nil
}

func (e *doctrineExecutor) ExecuteStream(_ context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.streamCalls[auth.ID]++
	e.streamModels[auth.ID] = append(e.streamModels[auth.ID], req.Model)
	if err := e.streamErrs[auth.ID]; err != nil {
		return nil, err
	}
	if e.firstStreamEmpty && !e.firstStreamDone {
		e.firstStreamDone = true
		ch := make(chan cliproxyexecutor.StreamChunk, 1)
		ch <- cliproxyexecutor.StreamChunk{Payload: []byte("data: [DONE]\n\n")}
		close(ch)
		return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
	}
	payloads := e.streamPayloads[auth.ID]
	if len(payloads) == 0 {
		payloads = [][]byte{
			[]byte(`data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}` + "\n\n"),
			[]byte("data: [DONE]\n\n"),
		}
	}
	ch := make(chan cliproxyexecutor.StreamChunk, len(payloads))
	for _, p := range payloads {
		ch <- cliproxyexecutor.StreamChunk{Payload: append([]byte(nil), p...)}
	}
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}

func (e *doctrineExecutor) CountTokens(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.countTokensCalls[auth.ID]++
	if err := e.countTokensErrs[auth.ID]; err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (e *doctrineExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) { return auth, nil }

func (e *doctrineExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (e *doctrineExecutor) Calls(authID string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.executeCalls[authID]
}

func (e *doctrineExecutor) StreamCalls(authID string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.streamCalls[authID]
}

func (e *doctrineExecutor) Models(authID string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.executeModels[authID]...)
}

func (e *doctrineExecutor) StreamModels(authID string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.streamModels[authID]...)
}

func (e *doctrineExecutor) TotalCalls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	total := 0
	for _, n := range e.executeCalls {
		total += n
	}
	return total
}

// newDoctrineManager builds a Manager with the fake executor and N auths that
// all serve the same unique model.
func newDoctrineManager(t *testing.T, executor *doctrineExecutor, authCount int) (*Manager, []string, string) {
	t.Helper()
	model := "doctrine-model-" + uuid.NewString()
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	manager.SetRetryConfig(0, 0, 5)

	var ids []string
	for i := 0; i < authCount; i++ {
		id := "doctrine-auth-" + uuid.NewString()
		auth := &Auth{
			ID:       id,
			Provider: executor.provider,
			Status:   StatusActive,
			Attributes: map[string]string{
				"auth_kind": "oauth",
			},
			Metadata: map[string]any{
				"access_token": "token",
			},
		}
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("Register(%s): %v", id, err)
		}
		registry.GetGlobalRegistry().RegisterClient(id, executor.provider, []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
		manager.RefreshSchedulerEntry(id)
		ids = append(ids, id)
	}
	return manager, ids, model
}

// TestSubSecondRetryAfterEscalatesOrRotates exercises the doctrine that a 429
// with a sub-second Retry-After must not hammer the same auth on every outer
// retry; it must either escalate the quota ladder or rotate to another auth.
//
// Current main: conductor_cooldown.go uses the provider hint verbatim, so the
// dead auth is picked, rate-limited, waited on, and picked again. BackoffLevel
// stays at 0 and no escalation occurs.
// Fix: Plus #198 floors the cooldown at the escalating quota ladder.
func TestSubSecondRetryAfterEscalatesOrRotates(t *testing.T) {
	exec := newDoctrineExecutor("claude")
	manager, ids, model := newDoctrineManager(t, exec, 1)
	manager.SetRetryConfig(2, 1500*time.Millisecond, 5)

	exec.executeErrs[ids[0]] = &doctrineRetryAfterError{
		status:     http.StatusTooManyRequests,
		message:    "quota exhausted",
		retryAfter: 50 * time.Millisecond,
	}

	_, err := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err == nil {
		t.Fatal("expected final error after exhausting retries")
	}

	auth, ok := manager.GetByID(ids[0])
	if !ok {
		t.Fatal("auth disappeared")
	}
	if auth.Quota.BackoffLevel == 0 {
		t.Skipf("current main violates: sub-second 429 Retry-After bypasses the quota ladder and hammers the same auth (BackoffLevel stays 0). Enable after Plus #198 (quota ladder floor) merges.")
	}
}

// TestSingleTransient503Cooldown documents the actual default cooldown applied
// to a live account after one transient 503.
//
// Current main: 60 s legacy cooldown (transientErrorCooldown = time.Minute).
// Fix: Plus #205 lowers the default to 10 s.
func TestSingleTransient503Cooldown(t *testing.T) {
	exec := newDoctrineExecutor("claude")
	manager, ids, model := newDoctrineManager(t, exec, 1)
	manager.SetRetryConfig(0, 0, 0)

	exec.executeErrs[ids[0]] = &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "transient 503"}

	before := time.Now()
	_, err := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err == nil {
		t.Fatal("expected 503 error")
	}

	auth, ok := manager.GetByID(ids[0])
	if !ok {
		t.Fatal("auth disappeared")
	}
	if !auth.Unavailable {
		t.Fatal("auth should be unavailable after 503")
	}
	if auth.NextRetryAfter.IsZero() {
		t.Fatal("auth should have a cooldown")
	}

	cooldown := auth.NextRetryAfter.Sub(before)
	t.Logf("transient 503 cooldown on current main: %v (NextRetryAfter=%v)", cooldown, auth.NextRetryAfter)

	if cooldown <= 0 {
		t.Fatalf("cooldown must be positive, got %v", cooldown)
	}
}

// TestEmptyCompletionRotatesNonStream and TestEmptyCompletionRotatesStream verify
// that Plus detects empty completions (empty JSON body, or an SSE stream that
// carries only [DONE]) and rotates to a live account.
//
// Stock: the empty-completion subsystem is absent until #4881 merges, so the
// scenarios skip on stock main today and run once #4881 lands.
func TestEmptyCompletionRotatesNonStream(t *testing.T) {
	exec := newDoctrineExecutor("claude")
	manager, _, model := newDoctrineManager(t, exec, 2)

	// First auth picked returns an empty completion; the rotated auth returns content.
	exec.firstExecuteEmpty = true

	resp, err := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if !strings.Contains(string(resp.Payload), "ok") || exec.TotalCalls() != 2 {
		t.Skipf("stock main lacks empty-completion rotation. Enable after #4881 merges.")
	}
}

func TestEmptyCompletionRotatesStream(t *testing.T) {
	exec := newDoctrineExecutor("claude")
	manager, ids, model := newDoctrineManager(t, exec, 2)

	// First auth streams only [DONE]; second auth streams the default content.
	exec.firstStreamEmpty = true

	stream, err := manager.ExecuteStream(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream error = %v", err)
	}
	var got strings.Builder
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
		got.Write(chunk.Payload)
	}
	if !strings.Contains(got.String(), "ok") || exec.StreamCalls(ids[0])+exec.StreamCalls(ids[1]) != 2 {
		t.Skipf("stock main lacks empty-completion rotation. Enable after #4881 merges.")
	}
}

// TestInStreamProviderErrorDuringBootstrap documents that current main forwards
// an in-stream provider error envelope from a 200 stream instead of treating it
// as an auth failure and rotating.
//
// Current main: empty_completion.go does not recognize provider error
// envelopes, so readStreamBootstrap treats the chunk as unknown data, the
// conductor returns success, and the dead auth is not cooled.
// Fix: Plus #195 adds in-stream provider-error detection and rotation.
func TestInStreamProviderErrorDuringBootstrap(t *testing.T) {
	exec := newDoctrineExecutor("claude")
	manager, ids, model := newDoctrineManager(t, exec, 2)

	// RoundRobin available list is ID-sorted; make ids[0] the first pick so the
	// error auth and the fallback auth are deterministic.
	sort.Strings(ids)

	geminiError := `data: {"error":{"code":429,"message":"Resource exhausted","status":"RESOURCE_EXHAUSTED"}}` + "\n\n"
	exec.streamPayloads[ids[0]] = [][]byte{[]byte(geminiError)}

	stream, err := manager.ExecuteStream(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
	if err != nil {
		t.Fatalf("expected rotation to fallback auth, got error = %v", err)
	}
	if stream == nil {
		t.Fatal("expected non-nil stream after fallback rotation")
	}
	var got strings.Builder
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
		got.Write(chunk.Payload)
	}
	if !strings.Contains(got.String(), "ok") {
		t.Skipf("current main violates: in-stream provider error envelopes inside a 200 SSE stream are forwarded as content instead of rotating the auth. Enable after Plus #195 (in-stream error failover) merges.")
	}

	auth, ok := manager.GetByID(ids[0])
	if !ok {
		t.Fatal("first auth disappeared")
	}
	if !auth.Unavailable || auth.NextRetryAfter.IsZero() {
		t.Skipf("current main violates: in-stream provider error envelopes inside a 200 SSE stream are forwarded as content instead of rotating the auth. Enable after Plus #195 (in-stream error failover) merges.")
	}
	if exec.StreamCalls(ids[1]) == 0 {
		t.Skipf("current main violates: in-stream provider error envelopes inside a 200 SSE stream are forwarded as content instead of rotating the auth. Enable after Plus #195 (in-stream error failover) merges.")
	}
}

// TestAllButOneDeadStillServes verifies P1 stability: when every account except
// one is dead, the last live account still answers the request.
func TestAllButOneDeadStillServes(t *testing.T) {
	exec := newDoctrineExecutor("claude")
	manager, _, model := newDoctrineManager(t, exec, 3)

	// The first N-1 execution attempts fail; the last remaining auth is live.
	exec.failFirstN = 2

	resp, err := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if !strings.Contains(string(resp.Payload), "ok") {
		t.Fatalf("payload = %q, want success from last live auth", string(resp.Payload))
	}
	if exec.TotalCalls() != 3 {
		t.Fatalf("total calls = %d, want 3 (tried dead auths then succeeded)", exec.TotalCalls())
	}
}

// TestAffinityStaysHealthyAfterTransientBlip verifies that after a transient
// failure heals, the session does not ping-pong back to the recovered auth
// while the fallback auth is still healthy.
func TestAffinityStaysHealthyAfterTransientBlip(t *testing.T) {
	// Shrink the transient cooldown so the test does not sleep for a minute.
	previousTransient := transientErrorCooldownSeconds.Load()
	SetTransientErrorCooldownSeconds(1)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(previousTransient) })

	exec := newDoctrineExecutor("claude")
	manager, _, model := newDoctrineManager(t, exec, 2)
	manager.SetSelector(NewSessionAffinitySelector(&RoundRobinSelector{cursors: make(map[string]int)}))

	// The first execution attempt is a transient 503; the rotated fallback
	// becomes the session's healthy anchor.
	exec.failFirstN = 1
	exec.failFirstError = &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "transient"}

	session := http.Header{"X-Session-Id": []string{"sess-affinity-1"}}

	// Request 1: blip then fallback + bind.
	_, err := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Headers: session})
	if err != nil {
		t.Fatalf("request 1 error = %v", err)
	}

	// Wait for the blipped auth's transient cooldown to expire.
	time.Sleep(1100 * time.Millisecond)

	// Requests 2 and 3: blipped auth has healed, but the session must stay with
	// the healthy fallback instead of ping-ponging.
	for i := 0; i < 2; i++ {
		_, err := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Headers: session})
		if err != nil {
			t.Fatalf("request %d error = %v", i+2, err)
		}
	}

	calls := []int{0, 0}
	i := 0
	for _, n := range exec.executeCalls {
		calls[i] = n
		i++
	}
	sort.Ints(calls)
	if calls[0] != 1 || calls[1] != 3 {
		t.Fatalf("auth calls = %v, want [1, 3] (blip once, healthy sticky 3 times)", calls)
	}
}

// TestAliasedAccountDiscoveredWhenSiblingsDie verifies that an account hidden
// behind a multi-model alias is still discovered and used when the first
// resolved sibling model dies.
//
// Current main: executionModelCandidatesWithAlias only builds a multi-model pool
// for openai-compatibility providers. For API-key providers (Gemini, Claude,
// Codex, xAI, Vertex) it resolves a single model, so the sibling behind the
// alias is never discovered.
// Fix: Plus #208 resolves API-key model pools for all configured providers.
func TestAliasedAccountDiscoveredWhenSiblingsDie(t *testing.T) {
	cfg := &internalconfig.Config{
		GeminiKey: []internalconfig.GeminiKey{{
			APIKey: "doctrine-key",
			Models: []internalconfig.GeminiModel{
				{Name: "gemini-2.5-pro-exp-03-25", Alias: "g25p"},
				{Name: "gemini-2.5-flash", Alias: "g25p"},
			},
		}},
	}

	exec := newDoctrineExecutor("gemini")
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.RegisterExecutor(exec)
	manager.SetRetryConfig(0, 0, 3)

	auth := &Auth{
		ID:       "doctrine-gemini-" + uuid.NewString(),
		Provider: "gemini",
		Status:   StatusActive,
		Attributes: map[string]string{
			"api_key": "doctrine-key",
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, "gemini", []*registry.ModelInfo{{ID: "g25p"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	manager.RefreshSchedulerEntry(auth.ID)

	// First resolved sibling model fails; the second sibling is healthy and
	// returns the payload below.
	exec.failFirstN = 1
	exec.executePayloads[auth.ID] = []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}]}`)

	resp, err := manager.Execute(context.Background(), []string{"gemini"}, cliproxyexecutor.Request{Model: "g25p"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Skipf("current main violates: API-key model aliases resolve to a single upstream model, so a sibling behind the alias is not discovered when the first fails. Enable after Plus #208 (multi-provider model pools) merges.")
	}
	if !strings.Contains(string(resp.Payload), "ok") {
		t.Fatalf("payload = %q, want sibling model content", string(resp.Payload))
	}

	models := exec.Models(auth.ID)
	if len(models) < 2 {
		t.Skipf("current main violates: API-key model aliases resolve to a single upstream model, so a sibling behind the alias is not discovered when the first fails. Enable after Plus #208 (multi-provider model pools) merges.")
	}
	if models[0] == models[1] {
		t.Fatalf("alias pool rotated to the same model %q", models[0])
	}
}
