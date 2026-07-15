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

// firstByteSameAuthExecutor records the auth ID of every streaming attempt and
// returns a "silent" stream (no first payload) for the first silentCalls calls,
// then a real payload. The silent stream only closes once the per-attempt context
// is cancelled — mimicking the real upstream request being aborted by the
// first-byte timer — so downstream draining never blocks.
type firstByteSameAuthExecutor struct {
	id          string
	mu          sync.Mutex
	streamAuths []string
	calls       int
	silentCalls int
}

func (e *firstByteSameAuthExecutor) Identifier() string { return e.id }

func (e *firstByteSameAuthExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "Execute not implemented"}
}

func (e *firstByteSameAuthExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.mu.Lock()
	e.calls++
	n := e.calls
	e.streamAuths = append(e.streamAuths, auth.ID)
	e.mu.Unlock()
	if n <= e.silentCalls {
		ch := make(chan cliproxyexecutor.StreamChunk)
		go func() {
			<-ctx.Done()
			close(ch)
		}()
		return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Model": {req.Model}}, Chunks: ch}, nil
	}
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte("hello")}
	close(ch)
	return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Model": {req.Model}}, Chunks: ch}, nil
}

func (e *firstByteSameAuthExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *firstByteSameAuthExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "CountTokens not implemented"}
}

func (e *firstByteSameAuthExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "HttpRequest not implemented"}
}

func (e *firstByteSameAuthExecutor) streamAuthCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.streamAuths))
	copy(out, e.streamAuths)
	return out
}

func newFirstByteSameAuthManager(t *testing.T, alias string, executor ProviderExecutor, authIDs ...string) *Manager {
	t.Helper()
	cfg := &internalconfig.Config{
		OpenAICompatibility: []internalconfig.OpenAICompatibility{{
			Name:   "pool",
			Models: []internalconfig.OpenAICompatibilityModel{{Name: alias, Alias: alias}},
		}},
	}
	m := NewManager(nil, nil, nil)
	m.SetConfig(cfg)
	m.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	for _, id := range authIDs {
		auth := &Auth{
			ID:       id,
			Provider: openAICompatPoolProviderKey,
			Status:   StatusActive,
			Metadata: map[string]any{"refresh_token": "rt"}, // inert unless the executor returns 401
			Attributes: map[string]string{
				"api_key":      "test-key",
				"compat_name":  "pool",
				"provider_key": openAICompatPoolProviderKey,
			},
		}
		if _, err := m.Register(context.Background(), auth); err != nil {
			t.Fatalf("register auth %s: %v", id, err)
		}
		reg.RegisterClient(auth.ID, openAICompatPoolProviderKey, []*registry.ModelInfo{{ID: alias}})
		idCopy := auth.ID
		t.Cleanup(func() { reg.UnregisterClient(idCopy) })
	}
	return m
}

func drainStreamPayload(t *testing.T, res *cliproxyexecutor.StreamResult) string {
	t.Helper()
	if res == nil {
		t.Fatal("expected stream result")
	}
	var payload []byte
	for chunk := range res.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
		payload = append(payload, chunk.Payload...)
	}
	return string(payload)
}

// TestExecuteStream_FirstByteTimeoutRerollsSameAuth verifies that a first-byte
// timeout re-issues the request on the SAME credential (prompt-cache affinity)
// up to StreamFirstByteRetries times, never rotating to another credential. Two
// auths are registered so a rotation would surface as a different auth ID; the
// test asserts every attempt used the first-selected auth.
func TestExecuteStream_FirstByteTimeoutRerollsSameAuth(t *testing.T) {
	alias := "gpt-5.5"
	executor := &firstByteSameAuthExecutor{id: openAICompatPoolProviderKey, silentCalls: 2}
	m := newFirstByteSameAuthManager(t, alias, executor, "auth-A", "auth-B")

	opts := cliproxyexecutor.Options{
		Stream:                 true,
		StreamFirstByteTimeout: 40 * time.Millisecond,
		StreamFirstByteRetries: 3,
	}
	res, err := m.ExecuteStream(context.Background(), []string{openAICompatPoolProviderKey}, cliproxyexecutor.Request{Model: alias}, opts)
	if err != nil {
		t.Fatalf("ExecuteStream returned error: %v", err)
	}
	if got := drainStreamPayload(t, res); got != "hello" {
		t.Fatalf("stream payload = %q, want %q", got, "hello")
	}

	calls := executor.streamAuthCalls()
	if len(calls) != 3 {
		t.Fatalf("stream attempts = %d (%v), want 3 (2 silent re-rolls + 1 success)", len(calls), calls)
	}
	for i, id := range calls {
		if id != calls[0] {
			t.Fatalf("attempt %d used auth %q, want same-auth re-roll on %q (got rotation): %v", i, id, calls[0], calls)
		}
	}
}

// TestExecuteStream_FirstByteTimeoutSameAuthDisabled verifies that with
// StreamFirstByteRetries == 0 a first-byte timeout is NOT re-rolled: the single
// attempt is made exactly once and surfaces a terminal 504 — it never rotates to
// another credential and never cools the account.
func TestExecuteStream_FirstByteTimeoutSameAuthDisabled(t *testing.T) {
	alias := "gpt-5.5"
	executor := &firstByteSameAuthExecutor{id: openAICompatPoolProviderKey, silentCalls: 100}
	m := newFirstByteSameAuthManager(t, alias, executor, "auth-solo")

	opts := cliproxyexecutor.Options{
		Stream:                 true,
		StreamFirstByteTimeout: 40 * time.Millisecond,
		StreamFirstByteRetries: 0,
	}
	res, err := m.ExecuteStream(context.Background(), []string{openAICompatPoolProviderKey}, cliproxyexecutor.Request{Model: alias}, opts)
	if res != nil {
		t.Fatalf("expected no stream result from an always-silent upstream, got %v", res)
	}
	if !IsFirstByteTimeoutExhausted(err) {
		t.Fatalf("err = %v, want terminal first-byte-timeout-exhausted", err)
	}
	if calls := executor.streamAuthCalls(); len(calls) != 1 {
		t.Fatalf("stream attempts = %d (%v), want exactly 1 (no same-auth re-roll when disabled)", len(calls), calls)
	}
}

// TestExecuteStream_FirstByteTimeoutExhaustedIsTerminal verifies that once the
// same-account reconnect budget is used up, the request fails with a terminal
// 504 (IsFirstByteTimeoutExhausted) WITHOUT rotating to the second registered
// credential. This is what stops one poison request from burning multiple
// healthy accounts.
func TestExecuteStream_FirstByteTimeoutExhaustedIsTerminal(t *testing.T) {
	alias := "gpt-5.5"
	executor := &firstByteSameAuthExecutor{id: openAICompatPoolProviderKey, silentCalls: 100}
	m := newFirstByteSameAuthManager(t, alias, executor, "auth-A", "auth-B")

	opts := cliproxyexecutor.Options{
		Stream:                 true,
		StreamFirstByteTimeout: 40 * time.Millisecond,
		StreamFirstByteRetries: 1, // first attempt + 1 same-auth re-roll, then terminal
	}
	res, err := m.ExecuteStream(context.Background(), []string{openAICompatPoolProviderKey}, cliproxyexecutor.Request{Model: alias}, opts)
	if res != nil {
		t.Fatalf("expected no stream result on exhaustion, got %v", res)
	}
	if !IsFirstByteTimeoutExhausted(err) {
		t.Fatalf("err = %v, want terminal first-byte-timeout-exhausted", err)
	}
	calls := executor.streamAuthCalls()
	if len(calls) != 2 {
		t.Fatalf("stream attempts = %d (%v), want 2 (attempt + 1 same-auth re-roll)", len(calls), calls)
	}
	// No rotation on exhaustion: exactly ONE of the two registered credentials is
	// used; the other must be called zero times (this is what stops one poison
	// request from burning multiple healthy accounts).
	perAuth := map[string]int{}
	for _, id := range calls {
		perAuth[id]++
	}
	if (perAuth["auth-A"] > 0) == (perAuth["auth-B"] > 0) {
		t.Fatalf("want exactly one credential used, got auth-A=%d auth-B=%d (rotation on exhaustion): %v", perAuth["auth-A"], perAuth["auth-B"], calls)
	}
	// No cooldown: an FBT must never route through MarkResult, so NEITHER the
	// account-level NOR the model-level cooldown state may be touched.
	for _, a := range m.snapshotAuths() {
		if a.Status == StatusError {
			t.Fatalf("FBT set auth %q Status=StatusError, want no cooldown", a.ID)
		}
		if a.Unavailable {
			t.Fatalf("FBT marked auth %q Unavailable, want available", a.ID)
		}
		if !a.NextRetryAfter.IsZero() {
			t.Fatalf("FBT set account-level NextRetryAfter on auth %q (%v), want zero", a.ID, a.NextRetryAfter)
		}
		if a.LastError != nil {
			t.Fatalf("FBT set LastError on auth %q (%v), want none", a.ID, a.LastError)
		}
		if a.Failed != 0 {
			t.Fatalf("FBT bumped auth %q Failed=%d, want 0 (proves MarkResult was never called)", a.ID, a.Failed)
		}
		if st := a.ModelStates[alias]; st != nil {
			if st.Unavailable {
				t.Fatalf("FBT marked auth %q model %q Unavailable, want available", a.ID, alias)
			}
			if !st.NextRetryAfter.IsZero() {
				t.Fatalf("FBT set NextRetryAfter on auth %q model %q (%v), want zero", a.ID, alias, st.NextRetryAfter)
			}
		}
	}
}

// TestExecuteStream_FirstByteTimeoutClientCancelStops verifies that a client
// cancellation during the reconnect loop stops promptly instead of exhausting the
// whole re-roll budget against an already-gone client.
func TestExecuteStream_FirstByteTimeoutClientCancelStops(t *testing.T) {
	alias := "gpt-5.5"
	executor := &firstByteSameAuthExecutor{id: openAICompatPoolProviderKey, silentCalls: 100}
	m := newFirstByteSameAuthManager(t, alias, executor, "auth-A")

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(60 * time.Millisecond)
		cancel()
	}()

	opts := cliproxyexecutor.Options{
		Stream:                 true,
		StreamFirstByteTimeout: 40 * time.Millisecond,
		StreamFirstByteRetries: 100, // large budget; the client cancel must cut it short
	}
	start := time.Now()
	_, err := m.ExecuteStream(ctx, []string{openAICompatPoolProviderKey}, cliproxyexecutor.Request{Model: alias}, opts)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error when the client cancels mid-reconnect")
	}
	// 100 re-rolls at 40ms would take ~4s; a prompt cancel must finish far sooner.
	if elapsed > time.Second {
		t.Fatalf("client cancel did not stop the reconnect loop promptly: elapsed=%s", elapsed)
	}
}

// firstByteRefreshExecutor emits an in-stream 401 on the first attempt (to trigger
// an OAuth refresh) and a SILENT stream on every attempt afterwards. It exists to
// prove the post-refresh attempt is still first-byte-timer protected: without the
// fix, the re-issued request read the stream with the timer already stopped and
// would hang forever on a silent refreshed upstream.
type firstByteRefreshExecutor struct {
	id           string
	mu           sync.Mutex
	streamCalls  int
	refreshCalls int
}

func (e *firstByteRefreshExecutor) Identifier() string { return e.id }

func (e *firstByteRefreshExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "Execute not implemented"}
}

func (e *firstByteRefreshExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.mu.Lock()
	e.streamCalls++
	n := e.streamCalls
	e.mu.Unlock()
	if n == 1 {
		// First attempt: emit a 401 as the first (error) chunk so readStreamBootstrap
		// returns an unauthorized bootstrap error and triggers the OAuth refresh path.
		ch := make(chan cliproxyexecutor.StreamChunk, 1)
		ch <- cliproxyexecutor.StreamChunk{Err: &Error{HTTPStatus: http.StatusUnauthorized, Message: "401 unauthorized"}}
		close(ch)
		return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
	}
	// Post-refresh: a silent stream (never yields a payload). It must be aborted by
	// a fresh first-byte timer rather than block forever.
	silent := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		<-ctx.Done()
		close(silent)
	}()
	return &cliproxyexecutor.StreamResult{Chunks: silent}, nil
}

func (e *firstByteRefreshExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	e.mu.Lock()
	e.refreshCalls++
	e.mu.Unlock()
	return auth, nil
}

func (e *firstByteRefreshExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "CountTokens not implemented"}
}

func (e *firstByteRefreshExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "HttpRequest not implemented"}
}

func (e *firstByteRefreshExecutor) refreshCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.refreshCalls
}

func (e *firstByteRefreshExecutor) streamCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.streamCalls
}

// TestExecuteStream_FirstByteTimeoutRefreshRestartsTimer guards the OAuth-refresh
// path fix: after an in-stream 401 triggers a credential refresh, the re-issued
// attempt must get a FRESH first-byte timer. Without it a silent refreshed
// upstream hangs forever (the original attempt's timer was already stopped). The
// test fails via the 3s watchdog on a hang and asserts prompt terminal 504.
func TestExecuteStream_FirstByteTimeoutRefreshRestartsTimer(t *testing.T) {
	alias := "gpt-5.5"
	executor := &firstByteRefreshExecutor{id: openAICompatPoolProviderKey}

	cfg := &internalconfig.Config{
		OpenAICompatibility: []internalconfig.OpenAICompatibility{{
			Name:   "pool",
			Models: []internalconfig.OpenAICompatibilityModel{{Name: alias, Alias: alias}},
		}},
	}
	m := NewManager(nil, nil, nil)
	m.SetConfig(cfg)
	m.RegisterExecutor(executor)

	auth := &Auth{
		ID:       "auth-refresh",
		Provider: openAICompatPoolProviderKey,
		Status:   StatusActive,
		Metadata: map[string]any{"refresh_token": "rt"}, // enables authHasRefreshCredential
		Attributes: map[string]string{
			"api_key":      "test-key",
			"compat_name":  "pool",
			"provider_key": openAICompatPoolProviderKey,
		},
	}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, openAICompatPoolProviderKey, []*registry.ModelInfo{{ID: alias}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	opts := cliproxyexecutor.Options{
		Stream:                 true,
		StreamFirstByteTimeout: 40 * time.Millisecond,
		StreamFirstByteRetries: 1,
	}

	type outcome struct {
		res *cliproxyexecutor.StreamResult
		err error
	}
	resultCh := make(chan outcome, 1)
	go func() {
		res, err := m.ExecuteStream(context.Background(), []string{openAICompatPoolProviderKey}, cliproxyexecutor.Request{Model: alias}, opts)
		resultCh <- outcome{res, err}
	}()

	select {
	case got := <-resultCh:
		if executor.refreshCount() == 0 {
			t.Fatal("expected the in-stream 401 to trigger an OAuth refresh")
		}
		if got.res != nil {
			t.Fatalf("expected no stream result, got %v", got.res)
		}
		if !IsFirstByteTimeoutExhausted(got.err) {
			t.Fatalf("err = %v, want terminal first-byte-timeout-exhausted (post-refresh silent stream must be FBT-terminated)", got.err)
		}
		// bootstrap-retries=1: 401→refresh, then FBT + 1 same-auth re-roll before
		// terminal = 3 upstream calls. A refresh must NOT consume the FBT budget
		// (that would give only 2 calls).
		if n := executor.streamCount(); n != 3 {
			t.Fatalf("upstream stream calls = %d, want 3 (refresh must not consume the first-byte retry budget)", n)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ExecuteStream hung after OAuth refresh — the post-refresh attempt lost its first-byte timer")
	}
}

// firstByteSyncExecutor drives the SYNCHRONOUS error paths of the reconnect loop
// (executor.ExecuteStream returns (nil, err) rather than an in-stream chunk):
//   - always401:   every call returns a synchronous 401 (infinite-refresh guard).
//   - stalledConn: every call blocks on ctx and returns ctx.Err() once the
//     first-byte timer cancels attemptCtx (the stalled connect/TLS scenario).
//   - otherwise:   call 1 returns a synchronous 401, calls 2+ return a silent
//     stream (sync-401 refresh, then FBT re-roll).
type firstByteSyncExecutor struct {
	id          string
	always401   bool
	stalledConn bool

	mu           sync.Mutex
	streamAuths  []string
	refreshCalls int
}

func (e *firstByteSyncExecutor) Identifier() string { return e.id }

func (e *firstByteSyncExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "Execute not implemented"}
}

func (e *firstByteSyncExecutor) ExecuteStream(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.mu.Lock()
	e.streamAuths = append(e.streamAuths, auth.ID)
	n := len(e.streamAuths)
	e.mu.Unlock()
	if e.stalledConn {
		<-ctx.Done() // first-byte timer cancels attemptCtx; return synchronously
		return nil, ctx.Err()
	}
	if e.always401 || n == 1 {
		return nil, &Error{HTTPStatus: http.StatusUnauthorized, Message: "401 unauthorized"}
	}
	silent := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		<-ctx.Done()
		close(silent)
	}()
	return &cliproxyexecutor.StreamResult{Chunks: silent}, nil
}

func (e *firstByteSyncExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	e.mu.Lock()
	e.refreshCalls++
	e.mu.Unlock()
	return auth, nil
}

func (e *firstByteSyncExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "CountTokens not implemented"}
}

func (e *firstByteSyncExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "HttpRequest not implemented"}
}

func (e *firstByteSyncExecutor) streamAuthCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.streamAuths))
	copy(out, e.streamAuths)
	return out
}

func (e *firstByteSyncExecutor) refreshCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.refreshCalls
}

// runFirstByteStreamWithWatchdog runs ExecuteStream on a goroutine and fails fast
// if it does not return within 3s (a hang means a lost first-byte timer or an
// unbounded refresh loop).
func runFirstByteStreamWithWatchdog(t *testing.T, m *Manager, alias string, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	t.Helper()
	type outcome struct {
		res *cliproxyexecutor.StreamResult
		err error
	}
	ch := make(chan outcome, 1)
	go func() {
		res, err := m.ExecuteStream(context.Background(), []string{openAICompatPoolProviderKey}, cliproxyexecutor.Request{Model: alias}, opts)
		ch <- outcome{res, err}
	}()
	select {
	case o := <-ch:
		return o.res, o.err
	case <-time.After(3 * time.Second):
		t.Fatal("ExecuteStream hung (lost first-byte timer or unbounded refresh loop)")
		return nil, nil
	}
}

// TestExecuteStream_FirstByteTimeoutSyncRefreshRestartsTimer guards the SYNC-401
// refresh path: a synchronous 401 from ExecuteStream must refresh, re-enter the
// loop with a FRESH first-byte timer, and NOT consume the first-byte retry budget.
// At bootstrap-retries=1 that is 3 upstream calls (==2 would mean the refresh
// stole a re-roll).
func TestExecuteStream_FirstByteTimeoutSyncRefreshRestartsTimer(t *testing.T) {
	alias := "gpt-5.5"
	executor := &firstByteSyncExecutor{id: openAICompatPoolProviderKey}
	m := newFirstByteSameAuthManager(t, alias, executor, "auth-refresh")

	opts := cliproxyexecutor.Options{Stream: true, StreamFirstByteTimeout: 40 * time.Millisecond, StreamFirstByteRetries: 1}
	res, err := runFirstByteStreamWithWatchdog(t, m, alias, opts)

	if got := executor.refreshCount(); got != 1 {
		t.Fatalf("refreshCount = %d, want 1", got)
	}
	if res != nil || !IsFirstByteTimeoutExhausted(err) {
		t.Fatalf("res=%v err=%v, want terminal first-byte-timeout-exhausted", res, err)
	}
	if n := len(executor.streamAuthCalls()); n != 3 {
		t.Fatalf("upstream calls = %d, want 3 (sync-401 refresh + FBT + 1 re-roll; refresh must not consume the budget)", n)
	}
}

// TestExecuteStream_FirstByteTimeoutSyncStalledConnect covers the SYNC first-byte
// abort branch: ExecuteStream blocks on a stalled connect and returns ctx.Err()
// when the timer cancels it. Must same-account re-roll then terminate, with no
// rotation and no cooldown.
func TestExecuteStream_FirstByteTimeoutSyncStalledConnect(t *testing.T) {
	alias := "gpt-5.5"
	executor := &firstByteSyncExecutor{id: openAICompatPoolProviderKey, stalledConn: true}
	m := newFirstByteSameAuthManager(t, alias, executor, "auth-A", "auth-B")

	opts := cliproxyexecutor.Options{Stream: true, StreamFirstByteTimeout: 40 * time.Millisecond, StreamFirstByteRetries: 1}
	res, err := runFirstByteStreamWithWatchdog(t, m, alias, opts)

	if res != nil || !IsFirstByteTimeoutExhausted(err) {
		t.Fatalf("res=%v err=%v, want terminal first-byte-timeout-exhausted", res, err)
	}
	calls := executor.streamAuthCalls()
	if len(calls) != 2 {
		t.Fatalf("upstream calls = %d (%v), want 2 (attempt + 1 same-auth re-roll)", len(calls), calls)
	}
	for i, id := range calls {
		if id != calls[0] {
			t.Fatalf("attempt %d rotated to auth %q, want same-auth %q: %v", i, id, calls[0], calls)
		}
	}
	for _, a := range m.snapshotAuths() {
		if a.Failed != 0 || !a.NextRetryAfter.IsZero() || a.Unavailable {
			t.Fatalf("sync FBT cooled auth %q (Failed=%d NextRetryAfter=%v Unavailable=%v), want none", a.ID, a.Failed, a.NextRetryAfter, a.Unavailable)
		}
	}
}

// TestExecuteStream_UnauthorizedRefreshBoundedNoLoop is the highest-risk guard:
// an upstream that returns 401 on EVERY call must refresh at most once (via
// didRefreshOnUnauthorized) and then fall to MarkResult — never loop refresh→401
// forever. FBT is off so this exercises the refresh cap independently of timers.
func TestExecuteStream_UnauthorizedRefreshBoundedNoLoop(t *testing.T) {
	alias := "gpt-5.5"
	executor := &firstByteSyncExecutor{id: openAICompatPoolProviderKey, always401: true}
	m := newFirstByteSameAuthManager(t, alias, executor, "auth-solo")

	opts := cliproxyexecutor.Options{Stream: true} // FBT disabled
	_, err := runFirstByteStreamWithWatchdog(t, m, alias, opts)

	if err == nil {
		t.Fatal("expected the 401 to surface after the single refresh")
	}
	if rc := executor.refreshCount(); rc != 1 {
		t.Fatalf("refreshCount = %d, want 1 (single refresh then MarkResult; >1 or a hang = unbounded-refresh regression)", rc)
	}
	if n := len(executor.streamAuthCalls()); n != 2 {
		t.Fatalf("upstream calls = %d, want 2 (401 → refresh → 401 → MarkResult)", n)
	}
}
