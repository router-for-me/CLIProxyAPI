package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// emptyCompletionTestExecutor returns a configurable payload per auth, allowing
// tests to make one auth produce an empty completion and another a real one.
type emptyCompletionTestExecutor struct {
	executePayloads map[string][]byte   // auth ID -> non-stream payload
	streamPayloads  map[string][][]byte // auth ID -> SSE chunk payloads
	executeErr      map[string]error    // auth ID -> forced execute error
	streamErr       map[string]error    // auth ID -> forced stream error
	executeCalls    map[string]int      // auth ID -> call count (non-stream)
	streamCalls     map[string]int      // auth ID -> call count (stream)
	hook            func(authID, kind string)

	// firstExecute records the first auth that was picked for a non-stream
	// execution, so tests can deterministically wire the empty payload to it
	// regardless of global selector state.
	firstExecute string
	// firstStream records the first auth picked for a stream execution.
	firstStream string
}

func (e *emptyCompletionTestExecutor) Identifier() string { return "claude" }

func (*emptyCompletionTestExecutor) ShouldPrepareRequestAuth(*Auth) bool { return false }

func (e *emptyCompletionTestExecutor) PrepareRequestAuth(ctx context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *emptyCompletionTestExecutor) Execute(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.executeCalls[auth.ID]++
	if e.firstExecute == "" {
		e.firstExecute = auth.ID
	}
	if err := e.executeErr[auth.ID]; err != nil {
		return cliproxyexecutor.Response{}, err
	}
	// The first auth picked returns an empty completion; every subsequent auth
	// returns real content. This guarantees the rotation test exercises the
	// empty-completion failure path regardless of global selector state.
	if e.firstExecute == auth.ID {
		return cliproxyexecutor.Response{Payload: []byte(`{"choices":[{"message":{"content":""},"finish_reason":"stop"}],"usage":{"completion_tokens":0}}`)}, nil
	}
	if p, ok := e.executePayloads[auth.ID]; ok {
		return cliproxyexecutor.Response{Payload: p}, nil
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"choices":[{"message":{"content":"real"},"finish_reason":"stop"}]}`)}, nil
}

func (e *emptyCompletionTestExecutor) CountTokens(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (e *emptyCompletionTestExecutor) ExecuteStream(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.streamCalls[auth.ID]++
	if e.firstStream == "" {
		e.firstStream = auth.ID
	}
	if err := e.streamErr[auth.ID]; err != nil {
		return nil, err
	}
	// When the test pre-wires explicit payloads (e.g. the thinking-then-content
	// positive control), honor them. Otherwise force the first auth to stream an
	// empty completion and subsequent auths to stream real content, so rotation
	// tests are deterministic regardless of global selector state.
	if len(e.streamPayloads) == 0 && e.firstStream == auth.ID {
		empty := [][]byte{
			[]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"),
			[]byte("data: [DONE]\n\n"),
		}
		chunks := make(chan cliproxyexecutor.StreamChunk, len(empty))
		for _, p := range empty {
			chunks <- cliproxyexecutor.StreamChunk{Payload: p}
		}
		close(chunks)
		return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
	}
	if payloads, ok := e.streamPayloads[auth.ID]; ok && len(payloads) > 0 {
		chunks := make(chan cliproxyexecutor.StreamChunk, len(payloads))
		for _, p := range payloads {
			chunks <- cliproxyexecutor.StreamChunk{Payload: p}
		}
		close(chunks)
		return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
	}
	content := [][]byte{
		[]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\n"),
		[]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"),
		[]byte("data: [DONE]\n\n"),
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, len(content))
	for _, p := range content {
		chunks <- cliproxyexecutor.StreamChunk{Payload: p}
	}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *emptyCompletionTestExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (*emptyCompletionTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

// newEmptyCompletionTestManager registers two auths for the same model and
// returns the manager, the auth IDs, the model name, and a result-capture hook.
func newEmptyCompletionTestManager(t *testing.T, executor *emptyCompletionTestExecutor) (*Manager, []string, string, *resultCaptureHook) {
	t.Helper()
	model := "empty-completion-model-" + uuid.NewString()
	capture := &resultCaptureHook{}
	manager := NewManager(nil, nil, capture)
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(executor)

	var ids []string
	for i := 0; i < 2; i++ {
		auth := &Auth{
			ID:         "empty-completion-auth-" + uuid.NewString(),
			Provider:   "claude",
			Attributes: map[string]string{"auth_kind": "oauth"},
			Metadata: map[string]any{
				"access_token":  "access-token",
				"refresh_token": "refresh-token",
				"request_retry": float64(0),
			},
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", auth.ID, errRegister)
		}
		ids = append(ids, auth.ID)
	}
	return manager, ids, model, capture
}

func TestEmptyCompletionPredicate(t *testing.T) {
	cases := []struct {
		name     string
		payload  []byte
		expected bool
	}{
		{
			name: "openai sse empty",
			payload: []byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"),
			expected: true,
		},
		{
			name: "openai sse thinking then content is not empty",
			payload: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"),
			expected: false,
		},
		{
			name: "whitespace only zero tokens is empty",
			payload: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"   \"},\"finish_reason\":\"stop\"}],\"usage\":{\"completion_tokens\":0}}\n\ndata: [DONE]\n\n"),
			expected: true,
		},
		{
			name: "non zero tokens is not empty",
			payload: []byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"completion_tokens\":5}}\n\ndata: [DONE]\n\n"),
			expected: false,
		},
		{
			name: "tool calls are not empty",
			payload: []byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"id\":\"x\"}]},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"),
			expected: false,
		},
		{
			name: "openai sse reasoning only is not empty",
			payload: []byte("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"thinking step by step\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"),
			expected: false,
		},
		{
			name: "unterminated is not empty",
			payload: []byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":null}]}\n\n"),
			expected: false,
		},
		{
			name: "non openai format is not empty",
			payload: []byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"),
			expected: false,
		},
		{
			name: "non stream empty json",
			payload: []byte(`{"choices":[{"message":{"content":""},"finish_reason":"stop"}],"usage":{"completion_tokens":0}}`),
			expected: true,
		},
		{
			name: "non stream content is not empty",
			payload: []byte(`{"choices":[{"message":{"content":"hi"},"finish_reason":"stop"}]}`),
			expected: false,
		},
		{
			name: "non stream reasoning only is not empty",
			payload: []byte(`{"choices":[{"message":{"reasoning_content":"thinking"},"finish_reason":"stop"}]}`),
			expected: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isEmptyCompletionPayload(tc.payload); got != tc.expected {
				t.Fatalf("isEmptyCompletionPayload() = %v, want %v", got, tc.expected)
			}
		})
	}
}

func TestExecuteEmptyCompletionRotatesAuth(t *testing.T) {
	executor := &emptyCompletionTestExecutor{
		executePayloads: map[string][]byte{},
		executeCalls:    map[string]int{},
	}
	manager, ids, model, capture := newEmptyCompletionTestManager(t, executor)

	resp, err := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(string(resp.Payload), "real") {
		t.Fatalf("Execute() payload = %q, want success content from the non-empty auth", resp.Payload)
	}
	// The first-picked auth (which returned empty) must have been recorded as a
	// failure; the other must have succeeded.
	emptyFirst := executor.firstExecute
	if emptyFirst == "" {
		t.Fatal("executor never executed any auth")
	}
	other := ids[0]
	if emptyFirst == ids[0] {
		other = ids[1]
	}
	var emptyRecorded bool
	var otherSucceeded bool
	for _, r := range capture.Results() {
		if r.AuthID == emptyFirst && !r.Success {
			emptyRecorded = true
		}
		if r.AuthID == other && r.Success {
			otherSucceeded = true
		}
	}
	if !emptyRecorded {
		t.Fatalf("empty auth %q was not recorded as a failure result; results=%v", emptyFirst, capture.Results())
	}
	if !otherSucceeded {
		t.Fatalf("content auth %q was not recorded as a success result; results=%v", other, capture.Results())
	}
}

func TestExecuteStreamEmptyCompletionRotatesAuth(t *testing.T) {
	executor := &emptyCompletionTestExecutor{
		streamPayloads: map[string][][]byte{},
		streamCalls:    map[string]int{},
	}
	manager, ids, model, capture := newEmptyCompletionTestManager(t, executor)

	stream, err := manager.ExecuteStream(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	if stream == nil {
		t.Fatal("ExecuteStream() returned nil stream")
	}
	var got strings.Builder
	for chunk := range stream.Chunks {
		got.Write(chunk.Payload)
	}
	if !strings.Contains(got.String(), "hello") {
		t.Fatalf("stream payload = %q, want content from the non-empty auth", got.String())
	}
	// The first-streamed auth (which returned empty) must have been recorded as
	// a failure; the other must have succeeded.
	emptyFirst := executor.firstStream
	if emptyFirst == "" {
		t.Fatal("executor never streamed any auth")
	}
	other := ids[0]
	if emptyFirst == ids[0] {
		other = ids[1]
	}
	var emptyRecorded bool
	var otherSucceeded bool
	for _, r := range capture.Results() {
		if r.AuthID == emptyFirst && !r.Success {
			emptyRecorded = true
		}
		if r.AuthID == other && r.Success {
			otherSucceeded = true
		}
	}
	if !emptyRecorded {
		t.Fatalf("empty auth %q was not recorded as a failure result; results=%v", emptyFirst, capture.Results())
	}
	if !otherSucceeded {
		t.Fatalf("content auth %q was not recorded as a success result; results=%v", other, capture.Results())
	}
}

func TestExecuteStreamThinkingThenContentNotRotated(t *testing.T) {
	executor := &emptyCompletionTestExecutor{
		streamPayloads: map[string][][]byte{},
		streamCalls:    map[string]int{},
	}
	manager, ids, model, _ := newEmptyCompletionTestManager(t, executor)

	// Positive control: a thinking-first-then-content stream must NOT be
	// treated as empty, so it should not rotate to the second auth.
	content := [][]byte{
		[]byte("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"thinking\"},\"finish_reason\":null}]}\n\n"),
		[]byte("data: {\"choices\":[{\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\n"),
		[]byte("data: [DONE]\n\n"),
	}
	executor.streamPayloads[ids[0]] = content
	executor.streamPayloads[ids[1]] = content

	var streamCalls int
	stream, err := manager.ExecuteStream(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	var got strings.Builder
	for chunk := range stream.Chunks {
		got.Write(chunk.Payload)
	}
	_ = streamCalls
	if !strings.Contains(got.String(), "answer") {
		t.Fatalf("stream payload = %q, want thinking-then-content stream to pass through", got.String())
	}
	// The first auth must NOT have been cooled (it produced a real completion).
	if auth, ok := manager.GetByID(ids[0]); ok && auth != nil {
		if auth.Unavailable || !auth.NextRetryAfter.IsZero() {
			t.Fatalf("auth %q was cooled despite producing a real completion", ids[0])
		}
	}
}