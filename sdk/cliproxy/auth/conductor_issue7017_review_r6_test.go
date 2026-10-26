package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// Review round 6 regression: a Home aggregate immediate (explicit zero from
// an earlier credential) must not be replaced by the last attempt's positive
// trusted hint, in headers or scheduling.

func TestIssue7017HomeAggregateImmediateWinsHeadersAndScheduling(t *testing.T) {
	zero := time.Duration(0)
	positive := 256 * time.Second
	cause := withTrustedRetryAfter(issue7017HintError{msg: issue7017TurnBody(), hint: &positive}, &positive)
	_ = zero
	// roundTiming encodes an observed explicit zero as RetryAfter() == nil.
	timing := &homeRetryRoundTiming{}
	timing.Observe(issue7017HintError{msg: issue7017TurnBody(), hint: &zero})
	timing.Observe(cause)
	terminal := markHomeRetryRoundExhausted(cause, timing.RetryAfter(), true)

	if got := SafeResponseHeaders(terminal).Get("Retry-After"); got != "" {
		t.Fatalf("aggregate immediate must not advertise the last attempt hint, got %q", got)
	}
	if got := retryAfterFromError(terminal); got != nil {
		t.Fatalf("aggregate immediate must schedule as nil, got %v", got)
	}
	if !HasTrustedRetryDeadline(terminal) {
		t.Fatal("aggregate immediate must still suppress raw passthrough Retry-After")
	}

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.SetRetryConfig(1, time.Second, 0)
	wait, shouldRetry := manager.shouldRetryAfterError(terminal, 0, []string{"codex"}, "gpt", time.Second)
	if !shouldRetry || wait != 0 {
		t.Fatalf("aggregate immediate must retry now: wait=%v shouldRetry=%t", wait, shouldRetry)
	}
}

func TestIssue7017HomeAggregateImmediateStreamRound(t *testing.T) {
	withQuotaCooldownEnabled(t)
	zero := time.Duration(0)
	positive := 256 * time.Second
	dispatcher := &retryContractHomeDispatcher{authIDs: []string{"home-retry-a", "home-retry-b"}}
	executor := &retryContractHomeExecutor{
		failAll:         true,
		streamBootstrap: true,
		streamHeaders:   http.Header{"Retry-After": {"30"}},
		failures: map[string]error{
			"home-retry-a": issue7017HintError{msg: issue7017TurnBody(), hint: &zero},
			"home-retry-b": issue7017HintError{msg: issue7017TurnBody(), hint: &positive},
		},
	}
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.SetRetryConfig(0, time.Second, 2)
	manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
	manager.RegisterExecutor(executor)

	result, errExecute := manager.ExecuteStream(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{Stream: true})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}
	if result == nil {
		t.Fatal("ExecuteStream() result = nil")
	}
	chunk, ok := <-result.Chunks
	if !ok || chunk.Err == nil {
		t.Fatalf("stream bootstrap chunk = %#v, %t; want terminal error", chunk, ok)
	}
	if !isHomeRetryRoundExhausted(chunk.Err) {
		t.Fatalf("stream bootstrap error = %v, want exhausted retry round", chunk.Err)
	}
	if got := SafeResponseHeaders(chunk.Err).Get("Retry-After"); got != "" {
		t.Fatalf("aggregate immediate stream Retry-After = %q, want empty", got)
	}
}
