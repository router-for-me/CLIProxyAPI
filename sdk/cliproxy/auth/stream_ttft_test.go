package auth

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type ttftTestExecutor struct {
	id string

	mu          sync.Mutex
	streamCalls []string
	delayAuthA  time.Duration
	delayAuthB  time.Duration
}

func (e *ttftTestExecutor) Identifier() string { return e.id }

func (e *ttftTestExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func (e *ttftTestExecutor) ExecuteStream(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.mu.Lock()
	e.streamCalls = append(e.streamCalls, auth.ID)
	delayA := e.delayAuthA
	delayB := e.delayAuthB
	e.mu.Unlock()

	if auth.ID == "auth-a" && delayA > 0 {
		select {
		case <-time.After(delayA):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	if auth.ID == "auth-b" && delayB > 0 {
		select {
		case <-time.After(delayB):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	ch := make(chan cliproxyexecutor.StreamChunk, 2)
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte(`data: {"type":"content_block_delta","delta":{"text":"chunk-from-` + auth.ID + `"}}` + "\n\n")}
	close(ch)
	return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Auth": {auth.ID}}, Chunks: ch}, nil
}

func (e *ttftTestExecutor) Refresh(context.Context, *Auth) (*Auth, error) {
	return nil, nil
}

func (e *ttftTestExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *ttftTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *ttftTestExecutor) StreamCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.streamCalls))
	copy(out, e.streamCalls)
	return out
}

func TestManagerExecuteStream_TTFTTimeoutFailsOverToNextAuth(t *testing.T) {
	model := "gpt-5.5"
	authA := &Auth{ID: "auth-a", Provider: "codex"}
	authB := &Auth{ID: "auth-b", Provider: "codex"}

	executor := &ttftTestExecutor{
		id:         "codex",
		delayAuthA: 500 * time.Millisecond,
	}

	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authA.ID, "codex", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(authB.ID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(authA.ID)
		reg.UnregisterClient(authB.ID)
	})

	if _, err := m.Register(context.Background(), authA); err != nil {
		t.Fatalf("register authA: %v", err)
	}
	if _, err := m.Register(context.Background(), authB); err != nil {
		t.Fatalf("register authB: %v", err)
	}

	start := time.Now()
	opts := cliproxyexecutor.Options{
		Metadata: map[string]any{
			"stream_first_chunk_timeout_ms": 50,
		},
	}

	stream, errStream := m.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, opts)
	elapsed := time.Since(start)

	if errStream != nil {
		t.Fatalf("ExecuteStream error = %v, expected failover to authB", errStream)
	}
	if stream == nil || stream.Chunks == nil {
		t.Fatalf("expected stream result from authB")
	}

	var chunks []cliproxyexecutor.StreamChunk
	for chunk := range stream.Chunks {
		chunks = append(chunks, chunk)
	}

	if len(chunks) == 0 {
		t.Fatalf("expected chunks from authB")
	}
	if got := string(chunks[0].Payload); !strings.Contains(got, "chunk-from-auth-b") {
		t.Fatalf("chunk payload = %q, expected bytes ONLY from authB", got)
	}

	calls := executor.StreamCalls()
	if len(calls) != 2 || calls[0] != "auth-a" || calls[1] != "auth-b" {
		t.Fatalf("executor stream calls = %v, expected [auth-a, auth-b] called once each", calls)
	}

	if elapsed > 400*time.Millisecond {
		t.Fatalf("elapsed time = %v, expected under 400ms", elapsed)
	}

	updatedA, ok := m.GetByID("auth-a")
	if !ok || updatedA == nil {
		t.Fatalf("auth-a missing from manager")
	}
	if !updatedA.Unavailable {
		t.Fatalf("expected auth-a to be marked unavailable after TTFT timeout")
	}
}

func TestManagerExecuteStream_PostFirstChunkDelayNotCutOffByTTFT(t *testing.T) {
	model := "gpt-5.5"
	authA := &Auth{ID: "auth-a", Provider: "codex"}

	delayedStreamExec := &postFirstChunkExecutor{id: "codex", authID: "auth-a"}

	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(delayedStreamExec)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authA.ID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(authA.ID)
	})

	if _, err := m.Register(context.Background(), authA); err != nil {
		t.Fatalf("register authA: %v", err)
	}

	opts := cliproxyexecutor.Options{
		Metadata: map[string]any{
			"stream_first_chunk_timeout_ms": 40,
		},
	}

	stream, errStream := m.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, opts)
	if errStream != nil {
		t.Fatalf("ExecuteStream error = %v, want success", errStream)
	}

	var chunks []cliproxyexecutor.StreamChunk
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
		chunks = append(chunks, chunk)
	}

	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want 2 chunks (both first and delayed second chunk)", len(chunks))
	}
}

type postFirstChunkExecutor struct {
	id     string
	authID string
}

func (e *postFirstChunkExecutor) Identifier() string { return e.id }
func (e *postFirstChunkExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e *postFirstChunkExecutor) Refresh(context.Context, *Auth) (*Auth, error) { return nil, nil }
func (e *postFirstChunkExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e *postFirstChunkExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *postFirstChunkExecutor) ExecuteStream(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	ch := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(ch)
		ch <- cliproxyexecutor.StreamChunk{Payload: []byte(`data: {"type":"content_block_delta","delta":{"text":"chunk1"}}` + "\n\n")}
		select {
		case <-time.After(120 * time.Millisecond):
		case <-ctx.Done():
			return
		}
		ch <- cliproxyexecutor.StreamChunk{Payload: []byte(`data: {"type":"content_block_delta","delta":{"text":"chunk2"}}` + "\n\n")}
	}()
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}

type metadataFirstPostCommitExecutor struct {
	id     string
	authID string

	mu              sync.Mutex
	streamCalls     []string
	metadataWait    time.Duration
	postCommitErr   bool
	postCommitDelay time.Duration
	// provideContent controls whether a final erroring stream also emits a
	// content chunk first, exercising the true "post-commit" path.
	emitContentBeforeErr bool
}

func (e *metadataFirstPostCommitExecutor) Identifier() string { return e.id }

func (e *metadataFirstPostCommitExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *metadataFirstPostCommitExecutor) Refresh(context.Context, *Auth) (*Auth, error) {
	return nil, nil
}

func (e *metadataFirstPostCommitExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *metadataFirstPostCommitExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *metadataFirstPostCommitExecutor) ExecuteStream(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.mu.Lock()
	e.streamCalls = append(e.streamCalls, auth.ID)
	wait := e.metadataWait
	postErr := e.postCommitErr
	postDelay := e.postCommitDelay
	emitContent := e.emitContentBeforeErr
	e.mu.Unlock()

	ch := make(chan cliproxyexecutor.StreamChunk, 4)
	go func() {
		defer close(ch)
		// Recognized metadata/comment first: liveness but no semantic content.
		ch <- cliproxyexecutor.StreamChunk{Payload: []byte(": keepalive\n\n")}
		if wait > 0 {
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return
			}
		}
		if emitContent {
			ch <- cliproxyexecutor.StreamChunk{Payload: []byte(`data: {"type":"content_block_delta","delta":{"text":"chunk-from-` + auth.ID + `"}}` + "\n\n")}
			if postDelay > 0 {
				select {
				case <-time.After(postDelay):
				case <-ctx.Done():
					return
				}
			}
		}
		if postErr {
			ch <- cliproxyexecutor.StreamChunk{Err: &Error{HTTPStatus: http.StatusBadGateway, Message: "upstream failed after first chunk"}}
		}
	}()
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}

func (e *metadataFirstPostCommitExecutor) StreamCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.streamCalls))
	copy(out, e.streamCalls)
	return out
}

func TestManagerExecuteStream_MetadataFirstPrefixStopsTTFTWithoutFailover(t *testing.T) {
	model := "gpt-5.5"
	authA := &Auth{ID: "auth-a", Provider: "codex"}
	authB := &Auth{ID: "auth-b", Provider: "codex"}

	executor := &metadataFirstPostCommitExecutor{id: "codex", authID: "auth-a", metadataWait: 150 * time.Millisecond, emitContentBeforeErr: true}

	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authA.ID, "codex", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(authB.ID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(authA.ID)
		reg.UnregisterClient(authB.ID)
	})

	if _, err := m.Register(context.Background(), authA); err != nil {
		t.Fatalf("register authA: %v", err)
	}
	if _, err := m.Register(context.Background(), authB); err != nil {
		t.Fatalf("register authB: %v", err)
	}

	opts := cliproxyexecutor.Options{
		Metadata: map[string]any{
			"stream_first_chunk_timeout_ms": 40,
		},
	}

	stream, errStream := m.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, opts)
	if errStream != nil {
		t.Fatalf("ExecuteStream error = %v, want success from auth-a (metadata kept TTFT alive)", errStream)
	}
	if stream == nil || stream.Chunks == nil {
		t.Fatal("expected stream result")
	}

	var chunks []cliproxyexecutor.StreamChunk
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
		chunks = append(chunks, chunk)
	}

	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want 2 (buffered metadata + content)", len(chunks))
	}
	if got := string(chunks[0].Payload); got != ": keepalive\n\n" {
		t.Fatalf("first chunk payload = %q, want buffered metadata kept first", got)
	}
	if got := string(chunks[1].Payload); !strings.Contains(got, "chunk-from-auth-a") {
		t.Fatalf("second chunk payload = %q, want content from auth-a", got)
	}

	calls := executor.StreamCalls()
	if len(calls) != 1 || calls[0] != "auth-a" {
		t.Fatalf("executor stream calls = %v, want [auth-a] only (no failover to auth-b)", calls)
	}

	updatedA, ok := m.GetByID("auth-a")
	if !ok || updatedA == nil {
		t.Fatal("auth-a missing from manager")
	}
	if updatedA.Unavailable {
		t.Fatal("expected auth-a to remain available after metadata-first stream")
	}
}

func TestManagerExecuteStream_PostCommitErrorNotRetried(t *testing.T) {
	model := "gpt-5.5"
	authA := &Auth{ID: "auth-a", Provider: "codex"}
	authB := &Auth{ID: "auth-b", Provider: "codex"}

	executor := &metadataFirstPostCommitExecutor{
		id:                   "codex",
		authID:               "auth-a",
		metadataWait:         10 * time.Millisecond,
		postCommitErr:        true,
		postCommitDelay:      10 * time.Millisecond,
		emitContentBeforeErr: true,
	}

	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authA.ID, "codex", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(authB.ID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(authA.ID)
		reg.UnregisterClient(authB.ID)
	})

	if _, err := m.Register(context.Background(), authA); err != nil {
		t.Fatalf("register authA: %v", err)
	}
	if _, err := m.Register(context.Background(), authB); err != nil {
		t.Fatalf("register authB: %v", err)
	}

	opts := cliproxyexecutor.Options{
		Metadata: map[string]any{
			"stream_first_chunk_timeout_ms": 40,
		},
	}

	stream, errStream := m.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, opts)
	if errStream != nil {
		t.Fatalf("ExecuteStream error = %v, want success (stream handed to consumer)", errStream)
	}

	var chunks []cliproxyexecutor.StreamChunk
	var terminalErr error
	seenContent := false
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			if terminalErr != nil {
				t.Fatalf("duplicate terminal chunk error: %v after %v", chunk.Err, terminalErr)
			}
			terminalErr = chunk.Err
			continue
		}
		if len(chunk.Payload) > 0 && strings.Contains(string(chunk.Payload), "chunk-from-auth-a") {
			seenContent = true
			chunks = append(chunks, chunk)
		}
	}

	if terminalErr == nil {
		t.Fatal("expected post-commit error to reach the result stream")
	}
	if !seenContent {
		t.Fatal("expected content chunk from auth-a before the terminal error")
	}
	// No duplicate content replay from a backend retry.
	if len(chunks) != 1 {
		t.Fatalf("got %d content chunks, want exactly 1 (no backend replay)", len(chunks))
	}

	calls := executor.StreamCalls()
	if len(calls) != 1 || calls[0] != "auth-a" {
		t.Fatalf("executor stream calls = %v, want [auth-a] only (post-commit error must not retry)", calls)
	}
}

func TestStreamFirstChunkTimeout_ConfigAndMetadata(t *testing.T) {
	m := NewManager(nil, nil, nil)

	if got := m.streamFirstChunkTimeout(cliproxyexecutor.Options{}); got != 0 {
		t.Fatalf("default streamFirstChunkTimeout = %v, want disabled", got)
	}

	cfg := &internalconfig.Config{
		SDKConfig: internalconfig.SDKConfig{
			Streaming: internalconfig.StreamingConfig{
				StreamFirstChunkTimeoutSeconds: 10,
			},
		},
	}
	m.runtimeConfig.Store(cfg)
	if got := m.streamFirstChunkTimeout(cliproxyexecutor.Options{}); got != 10*time.Second {
		t.Fatalf("custom streamFirstChunkTimeout = %v, want 10s", got)
	}

	cfgDisabled := &internalconfig.Config{
		SDKConfig: internalconfig.SDKConfig{
			Streaming: internalconfig.StreamingConfig{
				StreamFirstChunkTimeoutSeconds: -1,
			},
		},
	}
	m.runtimeConfig.Store(cfgDisabled)
	if got := m.streamFirstChunkTimeout(cliproxyexecutor.Options{}); got != 0 {
		t.Fatalf("disabled streamFirstChunkTimeout = %v, want 0", got)
	}

	opts := cliproxyexecutor.Options{
		Metadata: map[string]any{
			"stream_first_chunk_timeout_ms": 75,
		},
	}
	if got := m.streamFirstChunkTimeout(opts); got != 75*time.Millisecond {
		t.Fatalf("metadata streamFirstChunkTimeout = %v, want 75ms", got)
	}
}

type ttftRefreshExecutor struct {
	id string

	mu             sync.Mutex
	streamCalls    int
	refreshCalls   int
	tokenInvalid   map[string]struct{}
	refreshTokens  map[string]string
	delayedStreams []string // auth IDs whose retry stream must delay its first chunk
	staleDelay     time.Duration
	retryDelay     time.Duration
}

func (e *ttftRefreshExecutor) Identifier() string { return e.id }

func (e *ttftRefreshExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func (e *ttftRefreshExecutor) ExecuteStream(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.mu.Lock()
	e.streamCalls++
	call := e.streamCalls
	token := authAccessToken(auth)
	_, invalid := e.tokenInvalid[token]
	delayed := false
	for _, id := range e.delayedStreams {
		if id == auth.ID {
			delayed = true
			break
		}
	}
	staleDelay := e.staleDelay
	retryDelay := e.retryDelay
	e.mu.Unlock()

	if invalid {
		// The stale-token attempt consumes TTFT budget before failing, so a
		// shared (non-fresh) timer would be near-firing by the time the retry
		// starts its own (possibly long) first-chunk wait.
		if call == 1 && staleDelay > 0 {
			select {
			case <-time.After(staleDelay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return nil, &Error{
			HTTPStatus: http.StatusUnauthorized,
			Message:    "Your authentication token has been invalidated. Please try signing in again.",
		}
	}

	if delayed && call > 1 && retryDelay > 0 {
		select {
		case <-time.After(retryDelay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte(auth.ID + ":" + token)}
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}

func (e *ttftRefreshExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.refreshCalls++
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	next := e.refreshTokens[auth.ID]
	if next == "" {
		next = "refreshed-access-token"
	}
	auth.Metadata["access_token"] = next
	return auth, nil
}

func (e *ttftRefreshExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *ttftRefreshExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *ttftRefreshExecutor) StreamCalls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.streamCalls
}

func (e *ttftRefreshExecutor) RefreshCalls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.refreshCalls
}

func newTTFTRefreshFixture(t *testing.T, staleDelay, retryDelay time.Duration) (*Manager, *ttftRefreshExecutor, *Auth, string) {
	t.Helper()

	model := "gpt-5.5"
	primary := &Auth{
		ID:       "ttft-primary",
		Provider: "codex",
		Metadata: map[string]any{
			"access_token":  "ttft-stale-token",
			"refresh_token": "ttft-refresh-token",
		},
	}

	executor := &ttftRefreshExecutor{
		id: "codex",
		tokenInvalid: map[string]struct{}{
			"ttft-stale-token": {},
		},
		staleDelay:     staleDelay,
		retryDelay:     retryDelay,
		delayedStreams: []string{primary.ID},
		refreshTokens: map[string]string{
			primary.ID: "ttft-fresh-token",
		},
	}

	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(primary.ID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(primary.ID)
	})

	if _, errRegister := m.Register(context.Background(), primary); errRegister != nil {
		t.Fatalf("register primary: %v", errRegister)
	}

	return m, executor, primary, model
}

func TestManagerExecuteStream_RefreshRetryGetsFreshTTFTTimer(t *testing.T) {
	// First attempt consumes 90ms of a 120ms budget before failing with the
	// stale-token 401, then triggers a refresh. The retry must receive a fresh
	// TTFT budget: its first chunk arrives 70ms into the retry, well inside a
	// fresh 120ms window. A shared (non-fresh) timer would have already elapsed
	// 90ms and fired at ~50ms into the retry, cutting the 70ms chunk off.
	m, executor, primary, model := newTTFTRefreshFixture(t, 90*time.Millisecond, 70*time.Millisecond)

	opts := cliproxyexecutor.Options{
		Metadata: map[string]any{
			"stream_first_chunk_timeout_ms": 120,
		},
	}

	stream, errStream := m.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, opts)
	if errStream != nil {
		t.Fatalf("ExecuteStream error = %v, want success on refreshed retry", errStream)
	}
	if stream == nil || stream.Chunks == nil {
		t.Fatal("expected stream result")
	}
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
		if got := string(chunk.Payload); got != primary.ID+":ttft-fresh-token" {
			t.Fatalf("payload = %q, want refreshed primary response (refreshCalls=%d streamCalls=%d)", got, executor.RefreshCalls(), executor.StreamCalls())
		}
	}

	if got := executor.RefreshCalls(); got != 1 {
		t.Fatalf("Refresh calls = %d, want 1", got)
	}
	if got := executor.StreamCalls(); got != 2 {
		t.Fatalf("Stream calls = %d, want 2 (initial + refreshed retry)", got)
	}
}

func TestStreamConnectTimeout_ConfigAndMetadata(t *testing.T) {
	m := NewManager(nil, nil, nil)

	// Canonical config key
	cfg := &internalconfig.Config{
		SDKConfig: internalconfig.SDKConfig{
			Streaming: internalconfig.StreamingConfig{
				StreamConnectTimeoutSeconds: 15,
			},
		},
	}
	m.runtimeConfig.Store(cfg)
	if got := m.streamFirstChunkTimeout(cliproxyexecutor.Options{}); got != 15*time.Second {
		t.Fatalf("canonical StreamConnectTimeoutSeconds = %v, want 15s", got)
	}

	// Precedence: canonical config overrides legacy alias
	cfgBoth := &internalconfig.Config{
		SDKConfig: internalconfig.SDKConfig{
			Streaming: internalconfig.StreamingConfig{
				StreamConnectTimeoutSeconds:    20,
				StreamFirstChunkTimeoutSeconds: 5,
			},
		},
	}
	m.runtimeConfig.Store(cfgBoth)
	if got := m.streamFirstChunkTimeout(cliproxyexecutor.Options{}); got != 20*time.Second {
		t.Fatalf("StreamConnectTimeoutSeconds precedence = %v, want 20s", got)
	}

	// Canonical metadata key
	opts := cliproxyexecutor.Options{
		Metadata: map[string]any{
			"stream_connect_timeout_ms": 60,
		},
	}
	if got := m.streamFirstChunkTimeout(opts); got != 60*time.Millisecond {
		t.Fatalf("canonical stream_connect_timeout_ms = %v, want 60ms", got)
	}

	// Precedence: canonical metadata overrides legacy metadata alias
	optsBoth := cliproxyexecutor.Options{
		Metadata: map[string]any{
			"stream_connect_timeout_ms":     90,
			"stream_first_chunk_timeout_ms": 30,
		},
	}
	if got := m.streamFirstChunkTimeout(optsBoth); got != 90*time.Millisecond {
		t.Fatalf("stream_connect_timeout_ms metadata precedence = %v, want 90ms", got)
	}
}
