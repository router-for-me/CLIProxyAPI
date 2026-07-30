package auth

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type recoverySequenceExecutor struct {
	mu               sync.Mutex
	attempts         [][]cliproxyexecutor.StreamChunk
	directErrors     []error
	calls            int
	authIDs          []string
	recoveryContexts []bool
	notify           chan int
}

func (e *recoverySequenceExecutor) Identifier() string { return "codex" }

func (e *recoverySequenceExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *recoverySequenceExecutor) ExecuteStream(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.mu.Lock()
	call := e.calls
	e.calls++
	e.recoveryContexts = append(e.recoveryContexts, streamRecoveryFromContext(ctx) != nil)
	if auth != nil {
		e.authIDs = append(e.authIDs, auth.ID)
	}
	var attempt []cliproxyexecutor.StreamChunk
	if call < len(e.attempts) {
		attempt = append(attempt, e.attempts[call]...)
	}
	var directErr error
	if call < len(e.directErrors) {
		directErr = e.directErrors[call]
	}
	notify := e.notify
	e.mu.Unlock()
	if notify != nil {
		notify <- call + 1
	}
	if directErr != nil {
		return nil, directErr
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, len(attempt))
	for _, chunk := range attempt {
		chunks <- chunk
	}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Attempt": {string(rune('1' + call))}}, Chunks: chunks}, nil
}

func (e *recoverySequenceExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}
func (e *recoverySequenceExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e *recoverySequenceExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestExecuteStreamWithRecoveryDiscardsFailedAttempt(t *testing.T) {
	executor := &recoverySequenceExecutor{attempts: [][]cliproxyexecutor.StreamChunk{
		{
			{Payload: []byte("losing-start"), Commitment: cliproxyexecutor.StreamCommitmentProvisional},
			{Payload: []byte("losing-text"), Commitment: cliproxyexecutor.StreamCommitmentSemantic},
			{Err: &Error{HTTPStatus: http.StatusBadGateway, Message: "server error"}},
		},
		{
			{Payload: []byte("winning-start"), Commitment: cliproxyexecutor.StreamCommitmentProvisional},
			{Payload: []byte("winning-text"), Commitment: cliproxyexecutor.StreamCommitmentSemantic},
			{Payload: []byte("winning-stop"), Commitment: cliproxyexecutor.StreamCommitmentTerminal},
		},
	}}
	policy := cliproxyexecutor.StreamRecoveryPolicy{
		Attempts:       1,
		MaxBufferBytes: 1024,
		MaxRetryWindow: time.Second,
		InitialBackoff: time.Nanosecond,
		MaxBackoff:     time.Nanosecond,
	}
	ctx := context.WithValue(context.Background(), streamRecoveryContextKey{}, &streamRecoveryState{policy: policy, started: time.Now(), remaining: 1})
	result, buffered, remaining, err := (&Manager{}).executeStreamWithRecovery(ctx, executor, &Auth{}, cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, "codex", "gpt-test")
	if err != nil {
		t.Fatalf("executeStreamWithRecovery error: %v", err)
	}
	if got := result.Headers.Get("X-Attempt"); got != "2" {
		t.Fatalf("winning header = %q, want 2", got)
	}
	if executor.calls != 2 {
		t.Fatalf("calls = %d, want 2", executor.calls)
	}
	var got string
	for _, chunk := range buffered {
		got += string(chunk.Payload)
	}
	for chunk := range remaining {
		got += string(chunk.Payload)
	}
	if got != "winning-startwinning-textwinning-stop" {
		t.Fatalf("winning payload = %q", got)
	}
}

func TestManagerExecuteStreamRecoveryRetriesSynchronousTransientStatuses(t *testing.T) {
	for _, status := range []int{http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			executor := &recoverySequenceExecutor{
				attempts: [][]cliproxyexecutor.StreamChunk{
					nil,
					{{Payload: []byte("winning"), Commitment: cliproxyexecutor.StreamCommitmentTerminal}},
				},
				directErrors: []error{
					&Error{Code: "server_is_overloaded", Message: "temporary upstream capacity", HTTPStatus: status},
					nil,
				},
			}
			manager := NewManager(nil, nil, nil)
			manager.RegisterExecutor(executor)
			authID := "synchronous-recovery-auth-" + strconv.Itoa(status)
			model := "synchronous-recovery-model-" + strconv.Itoa(status)
			auth := &Auth{ID: authID, Provider: "codex", Status: StatusActive}
			if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
				t.Fatalf("register auth: %v", errRegister)
			}
			registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			manager.RefreshSchedulerEntry(auth.ID)

			result, errExecute := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true, StreamRecovery: cliproxyexecutor.StreamRecoveryPolicy{
				Attempts: 1, MaxBufferBytes: 1024, MaxRetryWindow: time.Second, MaxConcurrent: 1, InitialBackoff: time.Nanosecond, MaxBackoff: time.Nanosecond,
			}})
			if errExecute != nil {
				t.Fatalf("ExecuteStream: %v", errExecute)
			}
			var payload string
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					t.Fatalf("stream error: %v", chunk.Err)
				}
				payload += string(chunk.Payload)
			}
			if payload != "winning" {
				t.Fatalf("payload = %q, want winning", payload)
			}
			if got := result.Headers.Get("X-Attempt"); got != "2" {
				t.Fatalf("winning header = %q, want 2", got)
			}
			if executor.calls != 2 {
				t.Fatalf("calls = %d, want 2", executor.calls)
			}
			if len(executor.authIDs) != 2 || executor.authIDs[0] != authID || executor.authIDs[1] != authID {
				t.Fatalf("auth attempts = %v, want %q twice", executor.authIDs, authID)
			}
			updated, ok := manager.GetByID(authID)
			if !ok {
				t.Fatal("auth missing")
			}
			if updated.Unavailable || updated.Status != StatusActive || updated.Failed != 0 {
				t.Fatalf("recovered direct error changed auth availability: %+v", updated)
			}
		})
	}
}

func TestExecuteStreamWithRecoveryPreservesSynchronousErrorOnExhaustion(t *testing.T) {
	firstErr := &Error{Code: "server_is_overloaded", Message: "first temporary overload", HTTPStatus: http.StatusBadGateway}
	finalErr := &Error{Code: "server_is_overloaded", Message: "final structured overload", HTTPStatus: http.StatusServiceUnavailable}
	executor := &recoverySequenceExecutor{
		attempts:     make([][]cliproxyexecutor.StreamChunk, 2),
		directErrors: []error{firstErr, finalErr},
	}
	policy := cliproxyexecutor.StreamRecoveryPolicy{
		Attempts:       1,
		MaxBufferBytes: 1024,
		MaxRetryWindow: time.Second,
		InitialBackoff: time.Nanosecond,
		MaxBackoff:     time.Nanosecond,
	}
	ctx := context.WithValue(context.Background(), streamRecoveryContextKey{}, &streamRecoveryState{policy: policy, started: time.Now(), remaining: 1})
	result, buffered, remaining, errRecovery := (&Manager{}).executeStreamWithRecovery(ctx, executor, &Auth{}, cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, "codex", "gpt-test")
	if errRecovery != finalErr {
		t.Fatalf("error = %v, want final structured error %v", errRecovery, finalErr)
	}
	if result != nil || buffered != nil || remaining != nil {
		t.Fatalf("exhausted direct error returned stream state: result=%v buffered=%v remaining=%v", result, buffered, remaining)
	}
	if statusCodeFromError(errRecovery) != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", statusCodeFromError(errRecovery))
	}
	if executor.calls != 2 {
		t.Fatalf("calls = %d, want 2", executor.calls)
	}
}

func TestManagerExecuteStreamRecoveryReturnsOnlyWinningAttempt(t *testing.T) {
	executor := &recoverySequenceExecutor{attempts: [][]cliproxyexecutor.StreamChunk{
		{
			{Payload: []byte("losing"), Commitment: cliproxyexecutor.StreamCommitmentSemantic},
			{Err: &Error{HTTPStatus: http.StatusBadGateway, Message: "server error"}},
		},
		{
			{Payload: []byte("winning"), Commitment: cliproxyexecutor.StreamCommitmentSemantic},
			{Payload: []byte("done"), Commitment: cliproxyexecutor.StreamCommitmentTerminal},
		},
	}}
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &Auth{ID: "recovery-auth", Provider: "codex", Status: StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "gpt-test"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	manager.RefreshSchedulerEntry(auth.ID)

	result, err := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-test"}, cliproxyexecutor.Options{Stream: true, StreamRecovery: cliproxyexecutor.StreamRecoveryPolicy{
		Attempts:       1,
		MaxBufferBytes: 1024,
		MaxRetryWindow: time.Second,
		MaxConcurrent:  1,
		InitialBackoff: time.Nanosecond,
		MaxBackoff:     time.Nanosecond,
	}})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	var got string
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream error: %v", chunk.Err)
		}
		got += string(chunk.Payload)
	}
	if got != "winningdone" {
		t.Fatalf("payload = %q, want winningdone", got)
	}
	if result.Headers.Get("X-Attempt") != "2" {
		t.Fatalf("headers = %v, want winning attempt", result.Headers)
	}
}

func TestManagerExecuteStreamRecoveryRetriesPinnedAuthOnly(t *testing.T) {
	executor := &recoverySequenceExecutor{attempts: [][]cliproxyexecutor.StreamChunk{
		{{Err: &Error{HTTPStatus: http.StatusBadGateway, Message: "temporary"}}},
		{{Payload: []byte("ok"), Commitment: cliproxyexecutor.StreamCommitmentTerminal}},
	}}
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	for _, id := range []string{"pinned-auth", "other-auth"} {
		auth := &Auth{ID: id, Provider: "codex", Status: StatusActive}
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
		registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: "pinned-model"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	result, err := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "pinned-model"}, cliproxyexecutor.Options{Stream: true, Metadata: map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: "pinned-auth"}, StreamRecovery: cliproxyexecutor.StreamRecoveryPolicy{
		Attempts: 1, MaxBufferBytes: 1024, MaxRetryWindow: time.Second, MaxConcurrent: 1, InitialBackoff: time.Nanosecond, MaxBackoff: time.Nanosecond,
	}})
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}
	for range result.Chunks {
	}
	if len(executor.authIDs) != 2 || executor.authIDs[0] != "pinned-auth" || executor.authIDs[1] != "pinned-auth" {
		t.Fatalf("auth attempts = %v, want pinned auth twice", executor.authIDs)
	}
}

func TestRecoveryExhaustedIncompleteStreamPreservesRequestScopeWithoutCooldownOrFallback(t *testing.T) {
	executor := &recoverySequenceExecutor{attempts: [][]cliproxyexecutor.StreamChunk{
		{{Payload: []byte("start-1"), Commitment: cliproxyexecutor.StreamCommitmentProvisional}},
		{{Payload: []byte("start-2"), Commitment: cliproxyexecutor.StreamCommitmentProvisional}},
	}}
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	for _, id := range []string{"aa-incomplete-auth", "zz-backup-auth"} {
		auth := &Auth{ID: id, Provider: "codex", Status: StatusActive}
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("register auth: %v", err)
		}
		registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: "incomplete-model"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
		manager.RefreshSchedulerEntry(id)
	}
	_, err := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "incomplete-model"}, cliproxyexecutor.Options{Stream: true, StreamRecovery: cliproxyexecutor.StreamRecoveryPolicy{
		Attempts: 1, MaxBufferBytes: 1024, MaxRetryWindow: time.Second, MaxConcurrent: 1, InitialBackoff: time.Nanosecond, MaxBackoff: time.Nanosecond,
	}})
	if err == nil || statusCodeFromError(err) != http.StatusRequestTimeout {
		t.Fatalf("error = %v, want request-scoped 408", err)
	}
	if !isRequestScopedError(err) {
		t.Fatalf("error is not request scoped: %v", err)
	}
	if executor.calls != 2 || len(executor.authIDs) != 2 || executor.authIDs[0] != "aa-incomplete-auth" || executor.authIDs[1] != "aa-incomplete-auth" {
		t.Fatalf("attempts=%d auths=%v, want two attempts on primary only", executor.calls, executor.authIDs)
	}
	updated, ok := manager.GetByID("aa-incomplete-auth")
	if !ok {
		t.Fatal("auth missing")
	}
	state := updated.ModelStates["incomplete-model"]
	if state != nil && (state.Unavailable || !state.NextRetryAfter.IsZero()) {
		t.Fatalf("request-scoped incomplete stream cooled model: %+v", state)
	}
}

func TestRecoveryExhaustedGenericServerErrorAppliesCooldown(t *testing.T) {
	executor := &recoverySequenceExecutor{attempts: [][]cliproxyexecutor.StreamChunk{
		{{Err: &Error{HTTPStatus: http.StatusBadGateway, Message: "temporary-1"}}},
		{{Err: &Error{HTTPStatus: http.StatusBadGateway, Message: "temporary-2"}}},
	}}
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &Auth{ID: "server-error-auth", Provider: "codex", Status: StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "server-error-model"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	manager.RefreshSchedulerEntry(auth.ID)
	_, _ = manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "server-error-model"}, cliproxyexecutor.Options{Stream: true, StreamRecovery: cliproxyexecutor.StreamRecoveryPolicy{
		Attempts: 1, MaxBufferBytes: 1024, MaxRetryWindow: time.Second, MaxConcurrent: 1, InitialBackoff: time.Nanosecond, MaxBackoff: time.Nanosecond,
	}})
	if executor.calls != 2 {
		t.Fatalf("calls = %d, want 2", executor.calls)
	}
	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatal("auth missing")
	}
	state := updated.ModelStates["server-error-model"]
	if state == nil || !state.Unavailable || state.NextRetryAfter.IsZero() {
		t.Fatalf("generic 502 exhaustion did not cool model: %+v", state)
	}
}

func TestRecoverySlotHeldUntilBufferedResultDrained(t *testing.T) {
	executor := &recoverySequenceExecutor{attempts: [][]cliproxyexecutor.StreamChunk{
		{{Payload: []byte("first"), Commitment: cliproxyexecutor.StreamCommitmentTerminal}},
		{{Payload: []byte("second"), Commitment: cliproxyexecutor.StreamCommitmentUnknown}},
	}}
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &Auth{ID: "slot-auth", Provider: "codex", Status: StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "slot-model"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	manager.RefreshSchedulerEntry(auth.ID)
	policy := cliproxyexecutor.StreamRecoveryPolicy{Attempts: 1, MaxBufferBytes: 1024, MaxRetryWindow: time.Second, MaxConcurrent: 1, InitialBackoff: time.Nanosecond, MaxBackoff: time.Nanosecond}

	first, err := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "slot-model"}, cliproxyexecutor.Options{Stream: true, StreamRecovery: policy})
	if err != nil {
		t.Fatalf("first ExecuteStream: %v", err)
	}
	if got := manager.recoveryInFlight.Load(); got != 1 {
		t.Fatalf("recovery slots = %d, want 1 before drain", got)
	}
	second, err := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "slot-model"}, cliproxyexecutor.Options{Stream: true, StreamRecovery: policy})
	if err != nil {
		t.Fatalf("second ExecuteStream: %v", err)
	}
	for range second.Chunks {
	}
	if got := manager.recoveryInFlight.Load(); got != 1 {
		t.Fatalf("recovery slots = %d, want first slot still held", got)
	}
	for range first.Chunks {
	}
	deadline := time.Now().Add(time.Second)
	for manager.recoveryInFlight.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := manager.recoveryInFlight.Load(); got != 0 {
		t.Fatalf("recovery slots = %d, want 0 after drain", got)
	}
}

func TestRecoveryOverflowHoldsSlotThroughRetainedPrefixAndOverflowChunk(t *testing.T) {
	executor := &recoverySequenceExecutor{attempts: [][]cliproxyexecutor.StreamChunk{
		{
			{Payload: []byte("abc"), Commitment: cliproxyexecutor.StreamCommitmentProvisional},
			{Payload: []byte("def"), Commitment: cliproxyexecutor.StreamCommitmentSemantic},
			{Payload: []byte("tail"), Commitment: cliproxyexecutor.StreamCommitmentTerminal},
		},
		{{Payload: []byte("second"), Commitment: cliproxyexecutor.StreamCommitmentUnknown}},
	}}
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &Auth{ID: "overflow-slot-auth", Provider: "codex", Status: StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "overflow-slot-model"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	manager.RefreshSchedulerEntry(auth.ID)

	policy := cliproxyexecutor.StreamRecoveryPolicy{Attempts: 1, MaxBufferBytes: 5, MaxRetryWindow: time.Second, MaxConcurrent: 1, InitialBackoff: time.Nanosecond, MaxBackoff: time.Nanosecond}
	result, err := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "overflow-slot-model"}, cliproxyexecutor.Options{Stream: true, StreamRecovery: policy})
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}
	if got := manager.recoveryInFlight.Load(); got != 1 {
		t.Fatalf("recovery slots = %d before prefix drain, want 1", got)
	}
	second, err := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "overflow-slot-model"}, cliproxyexecutor.Options{Stream: true, StreamRecovery: policy})
	if err != nil {
		t.Fatalf("second ExecuteStream: %v", err)
	}
	for range second.Chunks {
	}
	executor.mu.Lock()
	recoveryContexts := append([]bool(nil), executor.recoveryContexts...)
	executor.mu.Unlock()
	if len(recoveryContexts) != 2 || !recoveryContexts[0] || recoveryContexts[1] {
		t.Fatalf("recovery contexts = %v, want [true false] while overflow slot is held", recoveryContexts)
	}
	if got := manager.recoveryInFlight.Load(); got != 1 {
		t.Fatalf("recovery slots = %d after gated second request, want 1", got)
	}
	if chunk := <-result.Chunks; string(chunk.Payload) != "abc" {
		t.Fatalf("prefix chunk = %q, want abc", chunk.Payload)
	}
	if got := manager.recoveryInFlight.Load(); got != 1 {
		t.Fatalf("recovery slots = %d before overflow chunk, want 1", got)
	}
	if chunk := <-result.Chunks; string(chunk.Payload) != "def" {
		t.Fatalf("overflow chunk = %q, want def", chunk.Payload)
	}
	deadline := time.Now().Add(time.Second)
	for manager.recoveryInFlight.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := manager.recoveryInFlight.Load(); got != 0 {
		t.Fatalf("recovery slots = %d after overflow prefix drain, want 0", got)
	}
	var tail string
	for chunk := range result.Chunks {
		tail += string(chunk.Payload)
	}
	if tail != "tail" {
		t.Fatalf("ordinary tail = %q, want tail", tail)
	}
}

func TestReadStreamBootstrapDefaultForwardsProvisionalImmediately(t *testing.T) {
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("start"), Commitment: cliproxyexecutor.StreamCommitmentProvisional}
	type bootstrapResult struct {
		buffered []cliproxyexecutor.StreamChunk
		closed   bool
		err      error
	}
	resultCh := make(chan bootstrapResult, 1)
	go func() {
		buffered, closed, err := readStreamBootstrap(context.Background(), chunks, false)
		resultCh <- bootstrapResult{buffered: buffered, closed: closed, err: err}
	}()
	select {
	case result := <-resultCh:
		if result.err != nil || result.closed || len(result.buffered) != 1 || string(result.buffered[0].Payload) != "start" {
			t.Fatalf("buffered=%v closed=%v err=%v", result.buffered, result.closed, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("default bootstrap blocked on provisional lifecycle chunk")
	}
	close(chunks)
}

func TestReadStreamBootstrapWaitsThroughProvisionalChunks(t *testing.T) {
	chunks := make(chan cliproxyexecutor.StreamChunk, 2)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("start"), Commitment: cliproxyexecutor.StreamCommitmentProvisional}
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("text"), Commitment: cliproxyexecutor.StreamCommitmentSemantic}
	close(chunks)
	buffered, closed, err := readStreamBootstrap(context.Background(), chunks, true)
	if err != nil || closed || len(buffered) != 2 {
		t.Fatalf("buffered=%v closed=%v err=%v", buffered, closed, err)
	}
}

func TestReadStreamBootstrapProvisionalOnlyCloseFails(t *testing.T) {
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("start"), Commitment: cliproxyexecutor.StreamCommitmentProvisional}
	close(chunks)
	_, _, err := readStreamBootstrap(context.Background(), chunks, true)
	if err == nil {
		t.Fatal("expected provisional-only stream failure")
	}
}

func TestCollectRecoveryAttemptOverflowFailsOpenExactlyOnce(t *testing.T) {
	chunks := make(chan cliproxyexecutor.StreamChunk, 3)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("abc"), Commitment: cliproxyexecutor.StreamCommitmentProvisional}
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("def"), Commitment: cliproxyexecutor.StreamCommitmentSemantic}
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("ghi"), Commitment: cliproxyexecutor.StreamCommitmentTerminal}
	close(chunks)
	buffered, remaining, terminal, failOpen, err := collectRecoveryAttempt(context.Background(), &cliproxyexecutor.StreamResult{Chunks: chunks}, 5)
	if err != nil || terminal || failOpen != "buffer_overflow" {
		t.Fatalf("terminal=%v failOpen=%v err=%v", terminal, failOpen, err)
	}
	if gotBytes := bufferedStreamBytes(buffered); gotBytes > 5 {
		t.Fatalf("buffered bytes = %d, exceeds limit", gotBytes)
	}
	var got string
	for _, chunk := range buffered {
		got += string(chunk.Payload)
	}
	for chunk := range remaining {
		got += string(chunk.Payload)
	}
	if got != "abcdefghi" {
		t.Fatalf("payload = %q, want abcdefghi", got)
	}
}

type cancelBufferingExecutor struct {
	started chan struct{}
}

func (e *cancelBufferingExecutor) Identifier() string { return "codex" }
func (e *cancelBufferingExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e *cancelBufferingExecutor) ExecuteStream(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	chunks := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(chunks)
		chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("start"), Commitment: cliproxyexecutor.StreamCommitmentProvisional}
		close(e.started)
		<-ctx.Done()
	}()
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}
func (e *cancelBufferingExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}
func (e *cancelBufferingExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e *cancelBufferingExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestRecoveryCancellationDuringBufferingDoesNotCooldown(t *testing.T) {
	executor := &cancelBufferingExecutor{started: make(chan struct{})}
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &Auth{ID: "cancel-auth", Provider: "codex", Status: StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "cancel-model"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	manager.RefreshSchedulerEntry(auth.ID)
	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan error, 1)
	go func() {
		_, err := manager.ExecuteStream(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: "cancel-model"}, cliproxyexecutor.Options{Stream: true, StreamRecovery: cliproxyexecutor.StreamRecoveryPolicy{Attempts: 1, MaxBufferBytes: 1024, MaxRetryWindow: time.Second, MaxConcurrent: 1, InitialBackoff: time.Second, MaxBackoff: time.Second}})
		resultCh <- err
	}()
	<-executor.started
	cancel()
	if err := <-resultCh; err != context.Canceled {
		t.Fatalf("ExecuteStream error = %v, want context.Canceled", err)
	}
	if got := manager.recoveryInFlight.Load(); got != 0 {
		t.Fatalf("recovery slots = %d after cancellation, want 0", got)
	}
	manager.mu.RLock()
	updated := manager.auths[auth.ID].Clone()
	manager.mu.RUnlock()
	if updated.Failed != 0 || updated.Unavailable || !updated.NextRetryAfter.IsZero() {
		t.Fatalf("cancellation changed auth availability: %+v", updated)
	}
	if state := updated.ModelStates["cancel-model"]; state != nil && (state.Unavailable || !state.NextRetryAfter.IsZero()) {
		t.Fatalf("cancellation cooled model: %+v", state)
	}
}

func TestRecoveryCancellationDuringBackoffDoesNotRetryOrCooldown(t *testing.T) {
	executor := &recoverySequenceExecutor{notify: make(chan int, 2), attempts: [][]cliproxyexecutor.StreamChunk{
		{{Err: &Error{HTTPStatus: http.StatusBadGateway, Message: "temporary"}}},
		{{Payload: []byte("unexpected"), Commitment: cliproxyexecutor.StreamCommitmentTerminal}},
	}}
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &Auth{ID: "backoff-auth", Provider: "codex", Status: StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "backoff-model"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	manager.RefreshSchedulerEntry(auth.ID)
	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan error, 1)
	go func() {
		_, err := manager.ExecuteStream(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: "backoff-model"}, cliproxyexecutor.Options{Stream: true, StreamRecovery: cliproxyexecutor.StreamRecoveryPolicy{Attempts: 1, MaxBufferBytes: 1024, MaxRetryWindow: 2 * time.Hour, MaxConcurrent: 1, InitialBackoff: time.Hour, MaxBackoff: time.Hour}})
		resultCh <- err
	}()
	if call := <-executor.notify; call != 1 {
		t.Fatalf("first call = %d", call)
	}
	time.Sleep(10 * time.Millisecond)
	cancel()
	if err := <-resultCh; err != context.Canceled {
		t.Fatalf("ExecuteStream error = %v, want context.Canceled", err)
	}
	executor.mu.Lock()
	calls := executor.calls
	executor.mu.Unlock()
	if calls != 1 {
		t.Fatalf("stream calls = %d, want 1", calls)
	}
	manager.mu.RLock()
	updated := manager.auths[auth.ID].Clone()
	manager.mu.RUnlock()
	if updated.Failed != 0 {
		t.Fatalf("cancellation recorded %d failures", updated.Failed)
	}
}

func TestCollectRecoveryAttemptOverflowPreservesLaterError(t *testing.T) {
	laterErr := &Error{HTTPStatus: http.StatusBadGateway, Message: "later failure"}
	chunks := make(chan cliproxyexecutor.StreamChunk, 3)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("abc"), Commitment: cliproxyexecutor.StreamCommitmentProvisional}
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("def"), Commitment: cliproxyexecutor.StreamCommitmentSemantic}
	chunks <- cliproxyexecutor.StreamChunk{Err: laterErr}
	close(chunks)
	buffered, remaining, _, failOpen, err := collectRecoveryAttempt(context.Background(), &cliproxyexecutor.StreamResult{Chunks: chunks}, 5)
	if err != nil || failOpen != "buffer_overflow" || bufferedStreamBytes(buffered) != 3 {
		t.Fatalf("buffered=%d failOpen=%v err=%v", bufferedStreamBytes(buffered), failOpen, err)
	}
	first := <-remaining
	if string(first.Payload) != "def" {
		t.Fatalf("overflow chunk = %q, want def", first.Payload)
	}
	second := <-remaining
	if second.Err != laterErr {
		t.Fatalf("later error = %v, want %v", second.Err, laterErr)
	}
}

func TestStreamRecoveryBudgetAndWindow(t *testing.T) {
	state := &streamRecoveryState{policy: cliproxyexecutor.StreamRecoveryPolicy{MaxRetryWindow: time.Second}, started: time.Now(), remaining: 1}
	if !state.canRetry() || state.canRetry() {
		t.Fatal("attempt budget was not enforced exactly")
	}
	state = &streamRecoveryState{policy: cliproxyexecutor.StreamRecoveryPolicy{MaxRetryWindow: time.Nanosecond}, started: time.Now().Add(-time.Second), remaining: 1}
	if state.canRetry() {
		t.Fatal("retry started after retry window")
	}
}

func TestStreamRecoveryGateIsNonblocking(t *testing.T) {
	manager := &Manager{}
	if !manager.acquireStreamRecovery(1) {
		t.Fatal("first acquisition failed")
	}
	if manager.acquireStreamRecovery(1) {
		t.Fatal("saturated acquisition unexpectedly succeeded")
	}
	manager.recoveryInFlight.Add(-1)
	if !manager.acquireStreamRecovery(1) {
		t.Fatal("slot was not released")
	}
	manager.recoveryInFlight.Add(-1)
}

func TestStreamRecoveryBackoffHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := streamRecoveryBackoff(ctx, cliproxyexecutor.StreamRecoveryPolicy{InitialBackoff: time.Second, MaxBackoff: time.Second}, 1)
	if err != context.Canceled {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}
