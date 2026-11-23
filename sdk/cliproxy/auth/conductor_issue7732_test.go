package auth

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type issue7732OrderedSelector struct {
	order []string
}

func (s *issue7732OrderedSelector) Pick(_ context.Context, _, _ string, _ cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	if len(auths) == 0 {
		return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	for _, id := range s.order {
		for _, candidate := range auths {
			if candidate != nil && candidate.ID == id {
				return candidate, nil
			}
		}
	}
	return auths[0], nil
}

type issue7732Hook struct {
	NoopHook
	mu      sync.Mutex
	results []Result
	notify  chan struct{}
}

func (h *issue7732Hook) OnResult(_ context.Context, result Result) {
	h.mu.Lock()
	h.results = append(h.results, result)
	h.mu.Unlock()
	select {
	case h.notify <- struct{}{}:
	default:
	}
}

func (h *issue7732Hook) Results() []Result {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Result, len(h.results))
	copy(out, h.results)
	return out
}

func (h *issue7732Hook) waitForResults(t *testing.T, want int, timeout time.Duration) []Result {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		if got := h.Results(); len(got) >= want {
			return got
		}
		select {
		case <-h.notify:
		case <-timer.C:
			return h.Results()
		}
	}
}

type issue7732Executor struct {
	identifier string

	mu           sync.Mutex
	calledAuths  []string
	refreshCalls int
	streamFn     func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, call int) (*cliproxyexecutor.StreamResult, error)
	refreshFn    func(ctx context.Context, auth *Auth) (*Auth, error)
}

func (e *issue7732Executor) Identifier() string { return e.identifier }

func (e *issue7732Executor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "Execute not implemented"}
}

func (e *issue7732Executor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.mu.Lock()
	call := len(e.calledAuths)
	e.calledAuths = append(e.calledAuths, auth.ID)
	e.mu.Unlock()
	return e.streamFn(ctx, auth, req, opts, call)
}

func (e *issue7732Executor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	e.mu.Lock()
	e.refreshCalls++
	e.mu.Unlock()
	if e.refreshFn != nil {
		return e.refreshFn(ctx, auth)
	}
	return auth, nil
}

func (e *issue7732Executor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "CountTokens not implemented"}
}

func (e *issue7732Executor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "HttpRequest not implemented"}
}

func (e *issue7732Executor) CalledAuths() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.calledAuths))
	copy(out, e.calledAuths)
	return out
}

func (e *issue7732Executor) RefreshCalls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.refreshCalls
}

func registerIssue7732Auth(t *testing.T, m *Manager, authID, provider, token, model string) *Auth {
	t.Helper()
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	auth := &Auth{
		ID:       authID,
		Provider: provider,
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":          provider,
			"access_token":  token,
			"refresh_token": "issue7732-refresh",
		},
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register(%s) error = %v", authID, errRegister)
	}
	return auth
}

func streamChunksChannel(chunks ...cliproxyexecutor.StreamChunk) <-chan cliproxyexecutor.StreamChunk {
	ch := make(chan cliproxyexecutor.StreamChunk, len(chunks))
	for _, chunk := range chunks {
		ch <- chunk
	}
	close(ch)
	return ch
}

// collectStreamResult drains a StreamResult returned by Manager.ExecuteStream.
// Bootstrap errors wrapped in streamBootstrapError are delivered through the
// chunk channel (streamErrorResult) rather than the direct error return value.
func collectStreamResult(streamRes *cliproxyexecutor.StreamResult, errStream error) ([]byte, error) {
	if errStream != nil {
		return nil, errStream
	}
	if streamRes == nil || streamRes.Chunks == nil {
		return nil, nil
	}
	var payload bytes.Buffer
	var firstErr error
	for chunk := range streamRes.Chunks {
		if chunk.Err != nil && firstErr == nil {
			firstErr = chunk.Err
		}
		if len(chunk.Payload) > 0 {
			payload.Write(chunk.Payload)
		}
	}
	return payload.Bytes(), firstErr
}

func TestReadStreamBootstrap_ZeroPayloadChunks(t *testing.T) {
	sentinelErr := errors.New("upstream bootstrap error")

	t.Run("only nil and empty payload chunks before close", func(t *testing.T) {
		ch := streamChunksChannel(
			cliproxyexecutor.StreamChunk{Payload: nil},
			cliproxyexecutor.StreamChunk{Payload: []byte{}},
		)
		buffered, closed, errBootstrap := readStreamBootstrap(context.Background(), ch)
		if errBootstrap != nil {
			t.Fatalf("readStreamBootstrap() error = %v, want nil", errBootstrap)
		}
		if !closed {
			t.Fatal("readStreamBootstrap() closed = false, want true")
		}
		if len(buffered) != 0 {
			t.Fatalf("len(buffered) = %d, want 0 for zero-payload stream", len(buffered))
		}
	})

	t.Run("only zero-payload incomplete chunk before close", func(t *testing.T) {
		ch := streamChunksChannel(
			cliproxyexecutor.StreamChunk{Payload: []byte{}, Incomplete: true},
		)
		buffered, closed, errBootstrap := readStreamBootstrap(context.Background(), ch)
		if errBootstrap != nil {
			t.Fatalf("readStreamBootstrap() error = %v, want nil", errBootstrap)
		}
		if !closed {
			t.Fatal("readStreamBootstrap() closed = false, want true")
		}
		if len(buffered) != 0 {
			t.Fatalf("len(buffered) = %d, want 0 for zero-payload incomplete stream", len(buffered))
		}
	})

	t.Run("many zero-payload chunks before first payload stay bounded", func(t *testing.T) {
		const zeroChunks = 200
		chunks := make([]cliproxyexecutor.StreamChunk, 0, zeroChunks+1)
		for range zeroChunks {
			chunks = append(chunks, cliproxyexecutor.StreamChunk{Payload: nil})
		}
		chunks = append(chunks, cliproxyexecutor.StreamChunk{Payload: []byte("data: ok\n\n")})

		buffered, closed, errBootstrap := readStreamBootstrap(context.Background(), streamChunksChannel(chunks...))
		if errBootstrap != nil {
			t.Fatalf("readStreamBootstrap() error = %v, want nil", errBootstrap)
		}
		if closed {
			t.Fatal("readStreamBootstrap() closed = true, want false")
		}
		if len(buffered) != 1 {
			t.Fatalf("len(buffered) = %d, want 1 (zero-payload chunks must not accumulate)", len(buffered))
		}
		if got := string(buffered[0].Payload); got != "data: ok\n\n" {
			t.Fatalf("buffered[0].Payload = %q, want %q", got, "data: ok\n\n")
		}
		if buffered[0].Incomplete {
			t.Fatal("buffered[0].Incomplete = true, want false")
		}
	})

	t.Run("preceding zero-payload incomplete flag preserved on first payload", func(t *testing.T) {
		ch := streamChunksChannel(
			cliproxyexecutor.StreamChunk{Payload: nil, Incomplete: true},
			cliproxyexecutor.StreamChunk{Payload: []byte{}},
			cliproxyexecutor.StreamChunk{Payload: []byte("data: partial\n\n")},
		)
		buffered, closed, errBootstrap := readStreamBootstrap(context.Background(), ch)
		if errBootstrap != nil {
			t.Fatalf("readStreamBootstrap() error = %v, want nil", errBootstrap)
		}
		if closed {
			t.Fatal("readStreamBootstrap() closed = true, want false")
		}
		if len(buffered) != 1 {
			t.Fatalf("len(buffered) = %d, want 1", len(buffered))
		}
		if got := string(buffered[0].Payload); got != "data: partial\n\n" {
			t.Fatalf("buffered[0].Payload = %q, want %q", got, "data: partial\n\n")
		}
		if !buffered[0].Incomplete {
			t.Fatal("buffered[0].Incomplete = false, want true to preserve preceding Incomplete flag")
		}
	})

	t.Run("zero-payload chunks followed by chunk error", func(t *testing.T) {
		ch := streamChunksChannel(
			cliproxyexecutor.StreamChunk{Payload: nil},
			cliproxyexecutor.StreamChunk{Err: sentinelErr},
		)
		buffered, closed, errBootstrap := readStreamBootstrap(context.Background(), ch)
		if !errors.Is(errBootstrap, sentinelErr) {
			t.Fatalf("readStreamBootstrap() error = %v, want %v", errBootstrap, sentinelErr)
		}
		if closed {
			t.Fatal("readStreamBootstrap() closed = true, want false")
		}
		if len(buffered) != 0 {
			t.Fatalf("len(buffered) = %d, want 0", len(buffered))
		}
	})
}

func TestExecuteStream_ZeroPayloadFailsOverToNextCredential(t *testing.T) {
	const (
		provider = "antigravity"
		model    = "issue-7732-model"
	)
	validPayload := []byte("data: {\"type\":\"response.completed\"}\n\n")

	cases := []struct {
		name   string
		chunks []cliproxyexecutor.StreamChunk
	}{
		{
			name:   "nil payload chunk then close",
			chunks: []cliproxyexecutor.StreamChunk{{Payload: nil}},
		},
		{
			name:   "empty incomplete chunk then close",
			chunks: []cliproxyexecutor.StreamChunk{{Payload: []byte{}, Incomplete: true}},
		},
		{
			name: "multiple zero-payload chunks then close",
			chunks: []cliproxyexecutor.StreamChunk{
				{Payload: nil},
				{Payload: []byte{}},
				{Payload: nil, Incomplete: true},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hook := &issue7732Hook{notify: make(chan struct{}, 8)}
			selector := &issue7732OrderedSelector{order: []string{"cred-7732-a", "cred-7732-b"}}
			m := NewManager(nil, selector, hook)

			registerIssue7732Auth(t, m, "cred-7732-a", provider, "token-a", model)
			registerIssue7732Auth(t, m, "cred-7732-b", provider, "token-b", model)

			exec := &issue7732Executor{
				identifier: provider,
				streamFn: func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options, _ int) (*cliproxyexecutor.StreamResult, error) {
					if auth.ID == "cred-7732-a" {
						return &cliproxyexecutor.StreamResult{
							Headers: http.Header{"X-Attempt": []string{"a"}},
							Chunks:  streamChunksChannel(tc.chunks...),
						}, nil
					}
					return &cliproxyexecutor.StreamResult{
						Headers: http.Header{"X-Attempt": []string{"b"}},
						Chunks:  streamChunksChannel(cliproxyexecutor.StreamChunk{Payload: validPayload}),
					}, nil
				},
			}
			m.RegisterExecutor(exec)

			streamRes, errStream := m.ExecuteStream(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
			if errStream != nil {
				t.Fatalf("ExecuteStream() error = %v, want nil after failover to cred-7732-b", errStream)
			}
			if streamRes == nil || streamRes.Chunks == nil {
				t.Fatal("ExecuteStream() returned nil stream result")
			}

			var delivered bytes.Buffer
			for chunk := range streamRes.Chunks {
				if chunk.Err != nil {
					t.Fatalf("unexpected stream chunk error: %v", chunk.Err)
				}
				delivered.Write(chunk.Payload)
			}
			if delivered.String() != string(validPayload) {
				t.Fatalf("delivered payload = %q, want %q", delivered.String(), string(validPayload))
			}

			called := exec.CalledAuths()
			if len(called) != 2 || called[0] != "cred-7732-a" || called[1] != "cred-7732-b" {
				t.Fatalf("called auths = %v, want [cred-7732-a cred-7732-b]", called)
			}

			results := hook.waitForResults(t, 2, 3*time.Second)
			if len(results) != 2 {
				t.Fatalf("recorded results count = %d, want 2 (%+v)", len(results), results)
			}
			if results[0].AuthID != "cred-7732-a" || results[0].Success {
				t.Fatalf("results[0] = {AuthID:%q Success:%v Error:%+v}, want cred-7732-a failure", results[0].AuthID, results[0].Success, results[0].Error)
			}
			if results[0].Error == nil || results[0].Error.Code != "empty_stream" {
				t.Fatalf("results[0].Error = %+v, want Code=empty_stream", results[0].Error)
			}
			if results[1].AuthID != "cred-7732-b" || !results[1].Success || !results[1].CompletedResponse {
				t.Fatalf("results[1] = {AuthID:%q Success:%v CompletedResponse:%v}, want cred-7732-b completed success", results[1].AuthID, results[1].Success, results[1].CompletedResponse)
			}
		})
	}
}

func TestExecuteStream_ZeroPayloadOnlyReturnsEmptyStreamError(t *testing.T) {
	const (
		provider = "antigravity"
		model    = "issue-7732-single-model"
	)
	hook := &issue7732Hook{notify: make(chan struct{}, 4)}
	m := NewManager(nil, nil, hook)
	registerIssue7732Auth(t, m, "cred-7732-only", provider, "token-only", model)

	exec := &issue7732Executor{
		identifier: provider,
		streamFn: func(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options, _ int) (*cliproxyexecutor.StreamResult, error) {
			return &cliproxyexecutor.StreamResult{
				Chunks: streamChunksChannel(cliproxyexecutor.StreamChunk{Payload: []byte{}, Incomplete: true}),
			}, nil
		},
	}
	m.RegisterExecutor(exec)

	streamRes, errStream := m.ExecuteStream(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
	delivered, finalErr := collectStreamResult(streamRes, errStream)
	if len(delivered) != 0 {
		t.Fatalf("delivered payload = %q, want empty", string(delivered))
	}
	if finalErr == nil {
		t.Fatal("ExecuteStream() produced no error, want empty_stream error")
	}
	var authErr *Error
	if !errors.As(finalErr, &authErr) || authErr.Code != "empty_stream" {
		t.Fatalf("ExecuteStream() error = %v, want *Error with Code=empty_stream", finalErr)
	}

	results := hook.waitForResults(t, 1, 3*time.Second)
	if len(results) != 1 || results[0].Success || results[0].Error == nil || results[0].Error.Code != "empty_stream" {
		t.Fatalf("recorded results = %+v, want single empty_stream failure", results)
	}
}

func TestExecuteStream_ZeroPayloadAfterUnauthorizedRefreshReturnsEmptyStream(t *testing.T) {
	const (
		provider = "codex"
		model    = "issue-7732-refresh-model"
	)
	hook := &issue7732Hook{notify: make(chan struct{}, 4)}
	m := NewManager(nil, nil, hook)
	registerIssue7732Auth(t, m, "cred-7732-refresh", provider, "token-initial", model)

	exec := &issue7732Executor{
		identifier: provider,
		streamFn: func(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options, call int) (*cliproxyexecutor.StreamResult, error) {
			if call == 0 {
				return &cliproxyexecutor.StreamResult{
					Chunks: streamChunksChannel(cliproxyexecutor.StreamChunk{
						Err: &Error{HTTPStatus: http.StatusUnauthorized, Code: "unauthorized", Message: "token expired"},
					}),
				}, nil
			}
			return &cliproxyexecutor.StreamResult{
				Chunks: streamChunksChannel(cliproxyexecutor.StreamChunk{Payload: nil}),
			}, nil
		},
		refreshFn: func(_ context.Context, auth *Auth) (*Auth, error) {
			cloned := auth.Clone()
			cloned.Metadata["access_token"] = "token-refreshed"
			return cloned, nil
		},
	}
	m.RegisterExecutor(exec)

	streamRes, errStream := m.ExecuteStream(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
	delivered, finalErr := collectStreamResult(streamRes, errStream)
	if len(delivered) != 0 {
		t.Fatalf("delivered payload = %q, want empty", string(delivered))
	}
	if finalErr == nil {
		t.Fatal("ExecuteStream() produced no error after refresh zero-payload stream, want empty_stream error")
	}
	var authErr *Error
	if !errors.As(finalErr, &authErr) || authErr.Code != "empty_stream" {
		t.Fatalf("ExecuteStream() error = %v, want *Error with Code=empty_stream", finalErr)
	}
	if exec.RefreshCalls() != 1 {
		t.Fatalf("RefreshCalls() = %d, want 1", exec.RefreshCalls())
	}

	results := hook.waitForResults(t, 1, 3*time.Second)
	if len(results) != 1 || results[0].Success || results[0].Error == nil || results[0].Error.Code != "empty_stream" {
		t.Fatalf("recorded results = %+v, want single empty_stream failure", results)
	}
}

func TestExecuteStream_LeadingZeroPayloadIncompleteFlagSuppressesCompletedResponse(t *testing.T) {
	const (
		provider = "codex"
		model    = "issue-7732-incomplete-flag-model"
	)
	payload := []byte("data: {\"type\":\"response.completed\"}\n\n")

	hook := &issue7732Hook{notify: make(chan struct{}, 4)}
	m := NewManager(nil, nil, hook)
	registerIssue7732Auth(t, m, "cred-7732-incomplete-flag", provider, "token-flag", model)

	exec := &issue7732Executor{
		identifier: provider,
		streamFn: func(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options, _ int) (*cliproxyexecutor.StreamResult, error) {
			return &cliproxyexecutor.StreamResult{
				Chunks: streamChunksChannel(
					cliproxyexecutor.StreamChunk{Payload: nil, Incomplete: true},
					cliproxyexecutor.StreamChunk{Payload: payload},
				),
			}, nil
		},
	}
	m.RegisterExecutor(exec)

	streamRes, errStream := m.ExecuteStream(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v, want nil", errStream)
	}

	var delivered bytes.Buffer
	for chunk := range streamRes.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
		delivered.Write(chunk.Payload)
	}
	if delivered.String() != string(payload) {
		t.Fatalf("delivered payload = %q, want %q", delivered.String(), string(payload))
	}

	results := hook.waitForResults(t, 1, 3*time.Second)
	if len(results) != 1 || !results[0].Success {
		t.Fatalf("recorded results = %+v, want single success result", results)
	}
	if results[0].CompletedResponse {
		t.Fatal("results[0].CompletedResponse = true, want false when a leading zero-payload chunk carried Incomplete=true")
	}
}
