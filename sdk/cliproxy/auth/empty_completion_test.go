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

	// emptyStreamPayload/contentStreamPayload override the default first-auth
	// empty stream and subsequent-auth content stream (used to exercise
	// non-OpenAI stream formats).
	emptyStreamPayload   [][]byte
	contentStreamPayload [][]byte
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
		empty := e.emptyStreamPayload
		if len(empty) == 0 {
			empty = [][]byte{
				[]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"),
				[]byte("data: [DONE]\n\n"),
			}
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
	content := e.contentStreamPayload
	if len(content) == 0 {
		content = [][]byte{
			[]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\n"),
			[]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"),
			[]byte("data: [DONE]\n\n"),
		}
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
			name:     "openai sse empty",
			payload:  []byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"),
			expected: true,
		},
		{
			name:     "openai sse thinking then content is not empty",
			payload:  []byte("data: {\"choices\":[{\"delta\":{\"content\":\"\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"),
			expected: false,
		},
		{
			name:     "whitespace only zero tokens is empty",
			payload:  []byte("data: {\"choices\":[{\"delta\":{\"content\":\"   \"},\"finish_reason\":\"stop\"}],\"usage\":{\"completion_tokens\":0}}\n\ndata: [DONE]\n\n"),
			expected: true,
		},
		{
			name:     "non zero tokens is not empty",
			payload:  []byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"completion_tokens\":5}}\n\ndata: [DONE]\n\n"),
			expected: false,
		},
		{
			name:     "tool calls are not empty",
			payload:  []byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"id\":\"x\"}]},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"),
			expected: false,
		},
		{
			name:     "openai sse reasoning only is not empty",
			payload:  []byte("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"thinking step by step\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"),
			expected: false,
		},
		{
			name:     "unterminated is not empty",
			payload:  []byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":null}]}\n\n"),
			expected: false,
		},
		{
			name:     "claude sse message_stop only is empty",
			payload:  []byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"),
			expected: true,
		},
		{
			name:     "unrecognized format is not empty",
			payload:  []byte("data: {\"unknown_payload\":true}\n\n"),
			expected: false,
		},
		{
			name:     "non stream empty json",
			payload:  []byte(`{"choices":[{"message":{"content":""},"finish_reason":"stop"}],"usage":{"completion_tokens":0}}`),
			expected: true,
		},
		{
			name:     "non stream content is not empty",
			payload:  []byte(`{"choices":[{"message":{"content":"hi"},"finish_reason":"stop"}]}`),
			expected: false,
		},
		{
			name:     "non stream reasoning only is not empty",
			payload:  []byte(`{"choices":[{"message":{"reasoning_content":"thinking"},"finish_reason":"stop"}]}`),
			expected: false,
		},
		{
			name:     "claude non-stream empty message is empty",
			payload:  []byte(`{"type":"message","id":"msg_1","role":"assistant","content":[],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":0}}`),
			expected: true,
		},
		{
			name:     "claude non-stream empty message without usage is empty",
			payload:  []byte(`{"type":"message","id":"msg_1","role":"assistant","content":[],"stop_reason":"end_turn"}`),
			expected: true,
		},
		{
			name:     "claude non-stream with tool_use is not empty",
			payload:  []byte(`{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"get_weather","input":{}}],"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":5}}`),
			expected: false,
		},
		{
			name:     "claude non-stream thinking-block-only is not empty",
			payload:  []byte(`{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"thinking","thinking":"let me think","signature":"sig"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`),
			expected: false,
		},
		{
			name:     "claude non-stream with text content is not empty",
			payload:  []byte(`{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":1}}`),
			expected: false,
		},
		{
			name:     "claude sse empty stream is empty",
			payload:  []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"stop_reason\":null,\"usage\":{\"output_tokens\":0}}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":0}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"),
			expected: true,
		},
		{
			name:     "gemini non-stream empty candidates is empty",
			payload:  []byte(`{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":0}}`),
			expected: true,
		},
		{
			name:     "gemini non-stream whitespace parts is empty",
			payload:  []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"   "}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":0}}`),
			expected: true,
		},
		{
			name:     "gemini non-stream without usage is empty",
			payload:  []byte(`{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}]}`),
			expected: true,
		},
		{
			name:     "gemini non-stream with functionCall part is not empty",
			payload:  []byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"search","args":{}}}]},"finishReason":"STOP"}]}`),
			expected: false,
		},
		{
			name:     "gemini non-stream with text content is not empty",
			payload:  []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":"STOP"}]}`),
			expected: false,
		},
		{
			name:     "gemini non-stream with thought part is not empty",
			payload:  []byte(`{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"thinking"}]},"finishReason":"STOP"}]}`),
			expected: false,
		},
		{
			name:     "gemini sse empty stream is empty",
			payload:  []byte("data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"candidatesTokenCount\":0}}\n\n"),
			expected: true,
		},
		{
			name:     "gemini blocked safety is not empty",
			payload:  []byte(`{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"SAFETY"}]}`),
			expected: false,
		},
		{
			name:     "gemini blocked recitation is not empty",
			payload:  []byte(`{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"RECITATION"}]}`),
			expected: false,
		},
		{
			name:     "gemini max tokens with empty content is not empty",
			payload:  []byte(`{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"MAX_TOKENS"}]}`),
			expected: false,
		},
		{
			name:     "gemini sse blocked safety closed by done is not empty",
			payload:  []byte("data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[]},\"finishReason\":\"SAFETY\"}]}\n\ndata: [DONE]\n\n"),
			expected: false,
		},
		{
			name:     "openai sse empty choices array then done is empty",
			payload:  []byte("data: {\"choices\":[]}\n\ndata: [DONE]\n\n"),
			expected: true,
		},
		{
			name:     "openai sse content_filter with empty content is not empty",
			payload:  []byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"content_filter\"}]}\n\ndata: [DONE]\n\n"),
			expected: false,
		},
		{
			name:     "openai sse length with empty content is not empty",
			payload:  []byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n"),
			expected: false,
		},
		{
			name:     "openai non-stream content_filter with empty content is not empty",
			payload:  []byte(`{"choices":[{"message":{"content":""},"finish_reason":"content_filter"}],"usage":{"completion_tokens":0}}`),
			expected: false,
		},
		{
			name:     "openai non-stream length with empty content is not empty",
			payload:  []byte(`{"choices":[{"message":{"content":""},"finish_reason":"length"}],"usage":{"completion_tokens":0}}`),
			expected: false,
		},
		{
			name:     "openai non-stream stop empty is empty",
			payload:  []byte(`{"choices":[{"message":{"content":""},"finish_reason":"stop"}],"usage":{"completion_tokens":0}}`),
			expected: true,
		},
		{
			name:     "codex responses-api sse completed with empty output is empty",
			payload:  []byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[],\"usage\":{\"output_tokens\":0}}}\n\ndata: [DONE]\n\n"),
			expected: true,
		},
		{
			name:     "codex responses-api non-stream completed with empty output is empty",
			payload:  []byte(`{"object":"response","id":"r","status":"completed","output":[],"usage":{"output_tokens":0}}`),
			expected: true,
		},
		{
			name:     "codex responses-api sse output_item message empty then completed is empty",
			payload:  []byte("data: {\"type\":\"response.output_item.done\",\"output\":{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"\",\"annotations\":[]}],\"status\":\"completed\"}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"\",\"annotations\":[]}]}],\"usage\":{\"output_tokens\":0}}}\n\ndata: [DONE]\n\n"),
			expected: true,
		},
		{
			name:     "codex responses-api non-stream with function_call is not empty",
			payload:  []byte(`{"object":"response","id":"r","status":"completed","output":[{"type":"function_call","name":"get_weather","arguments":"{}","call_id":"call_1"}],"usage":{"output_tokens":5}}`),
			expected: false,
		},
		{
			name:     "codex responses-api sse with function_call is not empty",
			payload:  []byte("data: {\"type\":\"response.output_item.done\",\"output\":{\"type\":\"function_call\",\"name\":\"get_weather\",\"arguments\":\"{}\",\"call_id\":\"call_1\"}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[{\"type\":\"function_call\",\"name\":\"get_weather\",\"arguments\":\"{}\",\"call_id\":\"call_1\"}],\"usage\":{\"output_tokens\":5}}}\n\ndata: [DONE]\n\n"),
			expected: false,
		},
		{
			name:     "gemini non-stream empty candidates array is empty",
			payload:  []byte(`{"candidates":[],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":0}}`),
			expected: true,
		},
		{
			name:     "gemini sse empty candidates array is empty",
			payload:  []byte("data: {\"candidates\":[],\"usageMetadata\":{\"candidatesTokenCount\":0}}\n\n"),
			expected: true,
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
	assertRotatesToContent(t, ids, executor.firstExecute, string(resp.Payload), "real", capture)
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
	assertRotatesToContent(t, ids, executor.firstStream, got.String(), "hello", capture)
}

func TestExecuteStreamEmptyGeminiStreamRotatesAuth(t *testing.T) {
	executor := &emptyCompletionTestExecutor{
		streamPayloads: map[string][][]byte{},
		streamCalls:    map[string]int{},
		emptyStreamPayload: [][]byte{
			[]byte("data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"candidatesTokenCount\":0}}\n\n"),
		},
		contentStreamPayload: [][]byte{
			[]byte("data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"functionCall\":{\"name\":\"search\",\"args\":{}}}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"candidatesTokenCount\":5}}\n\n"),
		},
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
	assertRotatesToContent(t, ids, executor.firstStream, got.String(), "functionCall", capture)
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

// assertRotatesToContent verifies that an empty-completion from the first-picked
// auth rotates to the other auth, which then succeeds with the given content.
func assertRotatesToContent(t *testing.T, ids []string, emptyFirst, gotPayload, wantSubstr string, capture *resultCaptureHook) {
	t.Helper()
	if emptyFirst == "" {
		t.Fatal("executor never executed/streamed any auth")
	}
	if !strings.Contains(gotPayload, wantSubstr) {
		t.Fatalf("payload = %q, want %q from the non-empty auth", gotPayload, wantSubstr)
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
