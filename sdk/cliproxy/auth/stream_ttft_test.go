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
	if got := string(chunks[0].Payload); !containsString(got, "chunk-from-auth-b") {
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

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
