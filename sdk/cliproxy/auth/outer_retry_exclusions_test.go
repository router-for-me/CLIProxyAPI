package auth

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type outerRetryTestExecutor struct {
	mu           sync.Mutex
	executeCalls map[string]int
	streamCalls  map[string]int
	totalCalls   int
	failFirstN   int
	executeErrs  map[string]error
	streamErrs   map[string]error
	responses    map[string]cliproxyexecutor.Response
}

func newOuterRetryTestExecutor() *outerRetryTestExecutor {
	return &outerRetryTestExecutor{
		executeCalls: make(map[string]int),
		streamCalls:  make(map[string]int),
		executeErrs:  make(map[string]error),
		streamErrs:   make(map[string]error),
		responses:    make(map[string]cliproxyexecutor.Response),
	}
}

func (e *outerRetryTestExecutor) Identifier() string                { return "claude" }
func (*outerRetryTestExecutor) ShouldPrepareRequestAuth(*Auth) bool { return false }
func (e *outerRetryTestExecutor) PrepareRequestAuth(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *outerRetryTestExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.executeCalls[auth.ID]++
	e.totalCalls++
	if err, ok := e.executeErrs[auth.ID]; ok && err != nil {
		return cliproxyexecutor.Response{}, err
	}
	if e.totalCalls <= e.failFirstN {
		return cliproxyexecutor.Response{}, &Error{Code: "service_unavailable", Message: "503 Service Unavailable"}
	}
	if resp, ok := e.responses[auth.ID]; ok {
		return resp, nil
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"choices":[{"message":{"content":"ok"}}]}`)}, nil
}

func (e *outerRetryTestExecutor) ExecuteStream(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.streamCalls[auth.ID]++
	if err, ok := e.streamErrs[auth.ID]; ok && err != nil {
		return nil, err
	}
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte(`data: {"choices":[{"delta":{"content":"ok"}}]}\n\n`)}
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}

func (e *outerRetryTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (*outerRetryTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (e *outerRetryTestExecutor) CountTokens(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func newOuterRetryTestManager(t *testing.T, executor *outerRetryTestExecutor, authCount int, disableCooling bool) (*Manager, []string, string) {
	t.Helper()
	model := "outer-retry-model-" + uuid.NewString()
	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(3, 0, 3)
	manager.RegisterExecutor(executor)

	var ids []string
	for i := 0; i < authCount; i++ {
		authID := "outer-retry-auth-" + uuid.NewString()
		auth := &Auth{
			ID:         authID,
			Provider:   "claude",
			Attributes: map[string]string{"auth_kind": "oauth"},
			Metadata: map[string]any{
				"access_token":    "access-token",
				"refresh_token":   "refresh-token",
				"disable_cooling": disableCooling,
			},
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("Register() error = %v", errRegister)
		}
		ids = append(ids, auth.ID)
	}
	return manager, ids, model
}

func TestOuterRetryExclusions_NonStream_DisableCooling_InvokedOnce(t *testing.T) {
	exec := newOuterRetryTestExecutor()
	manager, ids, model := newOuterRetryTestManager(t, exec, 1, true)
	// A 429 carrying Retry-After plus a positive max-wait interval creates a
	// real outer retry opportunity (each outer attempt would re-invoke the
	// executor before the exclusion fix). disable_cooling keeps the auth
	// eligible across attempts so the loop is not short-circuited by cooldown.
	manager.SetRetryConfig(3, 100*time.Millisecond, 3)
	exec.executeErrs[ids[0]] = &retryAfterStatusError{
		status:     http.StatusTooManyRequests,
		message:    "quota exhausted",
		retryAfter: 5 * time.Millisecond,
	}

	req := cliproxyexecutor.Request{Model: model}
	opts := cliproxyexecutor.Options{}

	_, err := manager.Execute(context.Background(), []string{"claude"}, req, opts)
	if err == nil {
		t.Fatal("Execute() expected error, got nil")
	}

	exec.mu.Lock()
	calls := exec.executeCalls[ids[0]]
	exec.mu.Unlock()

	if calls != 1 {
		t.Fatalf("auth executed %d times across outer retries, want exactly 1", calls)
	}
}

func TestOuterRetryExclusions_Stream_DisableCooling_InvokedOnce(t *testing.T) {
	exec := newOuterRetryTestExecutor()
	manager, ids, model := newOuterRetryTestManager(t, exec, 1, true)
	// Same non-vacuous setup as the non-stream variant: a 429 + Retry-After
	// with a positive max-wait interval guarantees the outer retry loop has a
	// real opportunity to re-invoke the executor before the exclusion fix.
	manager.SetRetryConfig(3, 100*time.Millisecond, 3)
	exec.streamErrs[ids[0]] = &retryAfterStatusError{
		status:     http.StatusTooManyRequests,
		message:    "quota exhausted",
		retryAfter: 5 * time.Millisecond,
	}

	req := cliproxyexecutor.Request{Model: model}
	opts := cliproxyexecutor.Options{}

	_, err := manager.ExecuteStream(context.Background(), []string{"claude"}, req, opts)
	if err == nil {
		t.Fatal("ExecuteStream() expected error, got nil")
	}

	exec.mu.Lock()
	calls := exec.streamCalls[ids[0]]
	exec.mu.Unlock()

	if calls != 1 {
		t.Fatalf("auth streamed %d times across outer retries, want exactly 1", calls)
	}
}

func TestOuterRetryExclusions_FailedAuth_FallbackToHealthyNextAuth(t *testing.T) {
	exec := newOuterRetryTestExecutor()
	exec.failFirstN = 1
	manager, _, model := newOuterRetryTestManager(t, exec, 2, true)

	req := cliproxyexecutor.Request{Model: model}
	opts := cliproxyexecutor.Options{}

	resp, err := manager.Execute(context.Background(), []string{"claude"}, req, opts)
	if err != nil {
		t.Fatalf("Execute() error = %v, expected success on second auth", err)
	}
	if string(resp.Payload) == "" {
		t.Fatal("Execute() returned empty payload")
	}

	exec.mu.Lock()
	total := exec.totalCalls
	exec.mu.Unlock()

	if total != 2 {
		t.Fatalf("total execute calls = %d, want 2 (1 failed + 1 healthy)", total)
	}
}

func TestOuterRetryExclusions_PreservesCallerSuppliedExclusions(t *testing.T) {
	exec := newOuterRetryTestExecutor()
	exec.failFirstN = 1
	manager, ids, model := newOuterRetryTestManager(t, exec, 3, false)

	req := cliproxyexecutor.Request{Model: model}
	opts := cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.ExcludedAuthIDsMetadataKey: map[string]struct{}{
				ids[0]: {},
			},
		},
	}

	resp, err := manager.Execute(context.Background(), []string{"claude"}, req, opts)
	if err != nil {
		t.Fatalf("Execute() error = %v, expected success on third auth", err)
	}
	if string(resp.Payload) == "" {
		t.Fatal("Execute() returned empty payload")
	}

	exec.mu.Lock()
	c0 := exec.executeCalls[ids[0]]
	total := exec.totalCalls
	exec.mu.Unlock()

	if c0 != 0 {
		t.Fatalf("caller-excluded auth executed %d times, want 0", c0)
	}
	if total != 2 {
		t.Fatalf("total execute calls = %d, want 2", total)
	}
}

func TestOuterRetryExclusions_CoolingEnabled_RemainsGreen(t *testing.T) {
	exec := newOuterRetryTestExecutor()
	exec.failFirstN = 1
	manager, _, model := newOuterRetryTestManager(t, exec, 2, false)

	req := cliproxyexecutor.Request{Model: model}
	opts := cliproxyexecutor.Options{}

	_, err := manager.Execute(context.Background(), []string{"claude"}, req, opts)
	if err != nil {
		t.Fatalf("Execute() error = %v, expected fallback success with cooling enabled", err)
	}

	exec.mu.Lock()
	total := exec.totalCalls
	exec.mu.Unlock()

	if total != 2 {
		t.Fatalf("total execute calls = %d, want 2", total)
	}
}
