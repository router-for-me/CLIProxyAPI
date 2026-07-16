package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// firstByteThenStatusExecutor stays silent (no first byte) until the per-attempt
// context is cancelled by the first-byte timer, then delivers a real upstream
// status error — reproducing a 429 that raced in exactly as the 15s timer fired.
type firstByteThenStatusExecutor struct {
	id     string
	status int
	mu     sync.Mutex
	calls  int
}

func (e *firstByteThenStatusExecutor) Identifier() string { return e.id }

func (e *firstByteThenStatusExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "Execute not implemented"}
}

func (e *firstByteThenStatusExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.mu.Lock()
	e.calls++
	e.mu.Unlock()
	status := e.status
	ch := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		<-ctx.Done() // first-byte timer fires and cancels the attempt
		// The upstream had actually produced a terminal 429 right as we timed out.
		ch <- cliproxyexecutor.StreamChunk{Err: &Error{HTTPStatus: status, Message: "rate limited"}}
		close(ch)
	}()
	return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Model": {req.Model}}, Chunks: ch}, nil
}

func (e *firstByteThenStatusExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *firstByteThenStatusExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "CountTokens not implemented"}
}

func (e *firstByteThenStatusExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "HttpRequest not implemented"}
}

func (e *firstByteThenStatusExecutor) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

// TestExecuteStream_RealStatusRacingFirstByteTimeoutCoolsAccount is the P1/P2
// regression: a real upstream 429 delivered right as the first-byte timer fires
// must still COOL the account — never be silently swallowed by the first-byte-
// timeout branch and hammered. When the status surfaces synchronously the P1
// gate routes it straight to MarkResult; when it lands just after the timeout
// give-up, drainAndCoolOnStatus captures it asynchronously. Either way the
// account must end up cooled (model NextRetryAfter set, Failed>0), which is the
// property that stops one poison request from hammering a rate-limited account.
func TestExecuteStream_RealStatusRacingFirstByteTimeoutCoolsAccount(t *testing.T) {
	alias := "gpt-5.5"
	executor := &firstByteThenStatusExecutor{id: openAICompatPoolProviderKey, status: http.StatusTooManyRequests}
	m := newFirstByteSameAuthManager(t, alias, executor, "auth-solo")

	opts := cliproxyexecutor.Options{
		Stream:                 true,
		StreamFirstByteTimeout: 40 * time.Millisecond,
		StreamFirstByteRetries: 1, // FBT re-roll enabled — the 429 must cool, not just reconnect forever
	}
	// The returned error type is not asserted: depending on scheduling the 429 may
	// surface synchronously (routed to MarkResult by the P1 gate), asynchronously
	// after the first-byte-timeout give-up (captured by drainAndCoolOnStatus), or
	// via the forwarder's emit. The invariant across all of them is that the
	// account ends up COOLED rather than hammered — that is what this asserts.
	res, _ := m.ExecuteStream(context.Background(), []string{openAICompatPoolProviderKey}, cliproxyexecutor.Request{Model: alias}, opts)
	if res != nil {
		// Drain any handed-off stream so the forwarder's emit path can run.
		for range res.Chunks {
		}
	}
	if got := executor.callCount(); got == 0 {
		t.Fatalf("executor never called: %d", got)
	}

	if !eventually(time.Second, func() bool {
		auth := markResultTestAuth(t, m, "auth-solo")
		st := auth.ModelStates[alias]
		return auth.Failed > 0 && st != nil && !st.NextRetryAfter.IsZero()
	}) {
		auth := markResultTestAuth(t, m, "auth-solo")
		t.Fatalf("account not cooled after a real 429 racing the first-byte timer: Failed=%d ModelState=%+v", auth.Failed, auth.ModelStates[alias])
	}
}
