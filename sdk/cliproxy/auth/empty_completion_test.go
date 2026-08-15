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
			name:     "openai sse refusal is not credential empty",
			payload:  []byte("data: {\"choices\":[{\"delta\":{\"refusal\":\"I cannot help with that\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"),
			expected: false,
		},
		{
			name:     "unterminated is not empty",
			payload:  []byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":null}]}\n\n"),
			expected: false,
		},
		{
			name:     "claude sse message_stop without end_turn is not empty",
			payload:  []byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"),
			expected: false,
		},
		{
			name:     "unrecognized format is not empty",
			payload:  []byte("data: {\"unknown_payload\":true}\n\n"),
			expected: false,
		},
		{
			name:     "unknown sse data followed by done is not empty",
			payload:  []byte("data: {\"vendor_event\":\"usable-or-unknown\"}\n\ndata: [DONE]\n\n"),
			expected: false,
		},
		{
			name:     "done-only stream remains intentionally empty",
			payload:  []byte("data: [DONE]\n\n"),
			expected: true,
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
			name:     "openai non-stream refusal is not credential empty",
			payload:  []byte(`{"choices":[{"message":{"content":"","refusal":"I cannot help with that"},"finish_reason":"stop"}],"usage":{"completion_tokens":0}}`),
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
			name:     "claude non-stream max_tokens is not credential empty",
			payload:  []byte(`{"type":"message","content":[],"stop_reason":"max_tokens","usage":{"output_tokens":0}}`),
			expected: false,
		},
		{
			name:     "claude sse refusal is not credential empty",
			payload:  []byte("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"refusal\"},\"usage\":{\"output_tokens\":0}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"),
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
			name:     "gemini prompt feedback safety is not credential empty",
			payload:  []byte(`{"promptFeedback":{"blockReason":"SAFETY"},"candidates":[]}`),
			expected: false,
		},
		{
			name:     "gemini sse prompt feedback safety is not credential empty",
			payload:  []byte("data: {\"promptFeedback\":{\"blockReason\":\"SAFETY\"},\"candidates\":[]}\n\ndata: [DONE]\n\n"),
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
			name:     "codex responses-api non-stream with custom_tool_call is not empty",
			payload:  []byte(`{"object":"response","status":"completed","output":[{"type":"custom_tool_call","name":"shell","input":"pwd"}],"usage":{"output_tokens":0}}`),
			expected: false,
		},
		{
			name:     "codex responses-api sse with custom_tool_call is not empty",
			payload:  []byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"custom_tool_call\",\"name\":\"shell\",\"input\":\"pwd\"}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[],\"usage\":{\"output_tokens\":0}}}\n\ndata: [DONE]\n\n"),
			expected: false,
		},
		{
			name:     "codex responses-api non-stream with image_generation_call is not empty",
			payload:  []byte(`{"object":"response","status":"completed","output":[{"type":"image_generation_call","status":"completed","result":"image-data"}],"usage":{"output_tokens":0}}`),
			expected: false,
		},
		{
			name:     "codex responses-api sse with image_generation_call is not empty",
			payload:  []byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"image_generation_call\",\"status\":\"completed\",\"result\":\"image-data\"}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[],\"usage\":{\"output_tokens\":0}}}\n\ndata: [DONE]\n\n"),
			expected: false,
		},
		{
			name:     "codex responses-api sse with reasoning item is not empty",
			payload:  []byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"reasoning\",\"status\":\"completed\",\"summary\":[]}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[],\"usage\":{\"output_tokens\":0}}}\n\ndata: [DONE]\n\n"),
			expected: false,
		},
		{
			name:     "codex responses-api non-stream refusal is not credential empty",
			payload:  []byte(`{"object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"refusal","refusal":"I cannot help with that"}]}],"usage":{"output_tokens":0}}`),
			expected: false,
		},
		{
			name:     "codex responses-api sse refusal item is not credential empty",
			payload:  []byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"refusal\",\"refusal\":\"I cannot help with that\"}]}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[],\"usage\":{\"output_tokens\":0}}}\n\ndata: [DONE]\n\n"),
			expected: false,
		},
		{
			name:     "codex responses-api non-stream incomplete is not credential empty",
			payload:  []byte(`{"object":"response","status":"incomplete","output":[],"usage":{"output_tokens":0}}`),
			expected: false,
		},
		{
			name:     "codex responses-api sse incomplete is not credential empty",
			payload:  []byte("data: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\",\"output\":[],\"usage\":{\"output_tokens\":0}}}\n\ndata: [DONE]\n\n"),
			expected: false,
		},
		{
			name:     "codex responses-api sse failed is not credential empty",
			payload:  []byte("data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"output\":[],\"usage\":{\"output_tokens\":0}}}\n\ndata: [DONE]\n\n"),
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
func TestEmptyCompletionTolerantUsage(t *testing.T) {
	cases := []struct {
		name     string
		payload  []byte
		expected bool
	}{
		{
			name:     "openai completion_tokens 1e2 positive is not empty",
			payload:  []byte(`{"choices":[{"message":{"content":""},"finish_reason":"stop"}],"usage":{"completion_tokens":1e2}}`),
			expected: false,
		},
		{
			name:     "openai completion_tokens 1.5 positive is not empty",
			payload:  []byte(`{"choices":[{"message":{"content":""},"finish_reason":"stop"}],"usage":{"completion_tokens":1.5}}`),
			expected: false,
		},
		{
			name:     "openai completion_tokens 100.0 positive is not empty",
			payload:  []byte(`{"choices":[{"message":{"content":""},"finish_reason":"stop"}],"usage":{"completion_tokens":100.0}}`),
			expected: false,
		},
		{
			name:     "openai completion_tokens zero stays empty",
			payload:  []byte(`{"choices":[{"message":{"content":""},"finish_reason":"stop"}],"usage":{"completion_tokens":0}}`),
			expected: true,
		},
		{
			name:     "openai completed payload with empty choices array is empty",
			payload:  []byte(`{"id":"chatcmpl-x","object":"chat.completion","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":0,"total_tokens":10}}`),
			expected: true,
		},
		{
			name:     "openai empty choices without usage is not judged terminal",
			payload:  []byte(`{"choices":[]}`),
			expected: false,
		},
		{
			name:     "openai completion_tokens negative stays empty",
			payload:  []byte(`{"choices":[{"message":{"content":""},"finish_reason":"stop"}],"usage":{"completion_tokens":-5}}`),
			expected: true,
		},
		{
			name:     "openai completion_tokens overflow stays empty",
			payload:  []byte(`{"choices":[{"message":{"content":""},"finish_reason":"stop"}],"usage":{"completion_tokens":1e999}}`),
			expected: true,
		},
		{
			name:     "openai malformed completion_tokens with content is not empty",
			payload:  []byte(`{"choices":[{"message":{"content":"hello"},"finish_reason":"stop"}],"usage":{"completion_tokens":"abc"}}`),
			expected: false,
		},
		{
			name:     "openai malformed completion_tokens alone stays empty",
			payload:  []byte(`{"choices":[{"message":{"content":""},"finish_reason":"stop"}],"usage":{"completion_tokens":"abc"}}`),
			expected: true,
		},
		{
			name:     "claude message usage exponent positive is not empty",
			payload:  []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"stop_reason\":null,\"usage\":{\"output_tokens\":1e2}}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":0}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"),
			expected: false,
		},
		{
			name:     "openai responses output_tokens decimal positive keeps terminal blocking",
			payload:  []byte(`{"object":"response","id":"r","status":"completed","output":[],"usage":{"output_tokens":1.5}}`),
			expected: false,
		},
		{
			name:     "gemini candidatesTokenCount exponent positive is not empty",
			payload:  []byte(`{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":1e2}}`),
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

// TestStreamBootstrapDetectorClaudePing is a regression guard for the codex
// P2 finding on PR #4881: Claude streams may emit {"type":"ping"} keep-alive
// events. evalClaude used to treat them as unknown payloads, permanently
// switching the detector to forwarding mode, so a terminally empty completion
// after a ping bypassed failover and surfaced as a successful empty stream.
func TestStreamBootstrapDetectorClaudePing(t *testing.T) {
	var detector StreamBootstrapDetector
	if detector.Observe([]byte("event: ping\ndata: {\"type\":\"ping\"}\n\n")) {
		t.Fatal("Observe() forwarded after Claude ping keep-alive")
	}
	if detector.Observe([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")) {
		t.Fatal("Observe() forwarded terminal empty Claude stream preceded by ping")
	}
}

func TestStreamBootstrapStateForwardsAtMetadataLimit(t *testing.T) {
	var state streamBootstrapState
	metadata := []byte("data: {\"type\":\"response.in_progress\",\"response\":{\"status\":\"in_progress\"}}\n\n")
	for state.bytes+len(metadata) <= maxStreamBootstrapBytes {
		if state.observe(metadata) {
			t.Fatal("bootstrap forwarded recognized metadata before reaching its byte limit")
		}
	}
	if !state.observe(metadata) {
		t.Fatal("bootstrap did not conservatively forward after reaching its byte limit")
	}
}

func TestStreamBootstrapDetector(t *testing.T) {
	var detector StreamBootstrapDetector
	if detector.Observe([]byte("data: {\"type\":\"response.created\",\"response\":{\"status\":\"in_progress\"}}\n\n")) {
		t.Fatal("StreamBootstrapDetector.Observe() = true for metadata-only prefix")
	}
	if !detector.Observe([]byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"custom_tool_call\"}}\n\n")) {
		t.Fatal("StreamBootstrapDetector.Observe() = false after complete custom tool output")
	}
}

func TestStreamBootstrapDetectorRequiresResponsesDiscriminator(t *testing.T) {
	t.Run("status-only custom JSON forwards", func(t *testing.T) {
		var detector StreamBootstrapDetector
		if !detector.Observe([]byte(`{"status":"running"}`)) {
			t.Fatal("StreamBootstrapDetector.Observe() buffered status-only custom JSON")
		}
	})

	t.Run("responses object remains buffered", func(t *testing.T) {
		var detector StreamBootstrapDetector
		if detector.Observe([]byte(`{"object":"response","status":"in_progress"}`)) {
			t.Fatal("StreamBootstrapDetector.Observe() forwarded Responses metadata")
		}
	})

	t.Run("known responses event remains buffered", func(t *testing.T) {
		var detector StreamBootstrapDetector
		if detector.Observe([]byte(`{"type":"response.in_progress","status":"in_progress"}`)) {
			t.Fatal("StreamBootstrapDetector.Observe() forwarded known Responses event")
		}
	})
}

func TestStreamBootstrapDetectorHandlesSplitSSEFrames(t *testing.T) {
	t.Run("terminal empty remains buffered", func(t *testing.T) {
		var detector StreamBootstrapDetector
		fragments := [][]byte{
			[]byte("da"),
			[]byte("ta: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n"),
			[]byte("\nda"),
			[]byte("ta: [DO"),
			[]byte("NE]\n\n"),
		}
		for i, fragment := range fragments {
			if detector.Observe(fragment) {
				t.Fatalf("Observe(fragment %d) forwarded terminal empty stream", i)
			}
		}
	})

	t.Run("meaningful output forwards after complete line", func(t *testing.T) {
		var detector StreamBootstrapDetector
		if detector.Observe([]byte("da")) {
			t.Fatal("Observe() forwarded incomplete SSE prefix")
		}
		if detector.Observe([]byte("ta: {\"type\":\"response.output_text.delta\",\"delta\":\"hel")) {
			t.Fatal("Observe() forwarded incomplete meaningful SSE line")
		}
		if !detector.Observe([]byte("lo\"}\n\n")) {
			t.Fatal("Observe() did not forward completed meaningful SSE line")
		}
	})

	t.Run("opaque payload forwards promptly", func(t *testing.T) {
		var detector StreamBootstrapDetector
		if !detector.Observe([]byte("opaque-provider-payload")) {
			t.Fatal("Observe() buffered definitely unrecognized payload")
		}
	})

	t.Run("event line waits for empty claude data", func(t *testing.T) {
		var detector StreamBootstrapDetector
		fragments := [][]byte{
			[]byte("event: message_start\n"),
			[]byte("data: {\"type\":\"message_start\",\"message\":{\"type\":\"message\",\"content\":[],\"stop_reason\":null,\"usage\":{\"output_tokens\":0}}}\n\n"),
			[]byte("event: message_delta\n"),
			[]byte("data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":0}}\n\n"),
			[]byte("event: message_stop\n"),
			[]byte("data: {\"type\":\"message_stop\"}\n\n"),
		}
		for i, fragment := range fragments {
			if detector.Observe(fragment) {
				t.Fatalf("Observe(fragment %d) forwarded empty Claude stream", i)
			}
		}
	})

	t.Run("split comment waits for terminal empty data", func(t *testing.T) {
		var detector StreamBootstrapDetector
		fragments := [][]byte{
			[]byte(":"),
			[]byte(" ping\n"),
			[]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"),
			[]byte("data: [DONE]\n\n"),
		}
		for i, fragment := range fragments {
			if detector.Observe(fragment) {
				t.Fatalf("Observe(fragment %d) forwarded comment-prefixed empty stream", i)
			}
		}
	})

	t.Run("comment then opaque line forwards", func(t *testing.T) {
		var detector StreamBootstrapDetector
		if detector.Observe([]byte(":")) || detector.Observe([]byte(" heartbeat\n")) {
			t.Fatal("Observe() forwarded a valid split SSE comment")
		}
		if !detector.Observe([]byte("opaque-provider-payload\n")) {
			t.Fatal("Observe() buffered a definitely non-SSE line after a comment")
		}
	})
}

func TestReadStreamBootstrapWithholdsSplitClaudeEmptyCompletion(t *testing.T) {
	fragments := [][]byte{
		[]byte("event: message_start\n"),
		[]byte("data: {\"type\":\"message_start\",\"message\":{\"type\":\"message\",\"content\":[],\"stop_reason\":null,\"usage\":{\"output_tokens\":0}}}\n\n"),
		[]byte("event: message_delta\n"),
		[]byte("data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":0}}\n\n"),
		[]byte("event: message_stop\n"),
		[]byte("data: {\"type\":\"message_stop\"}\n\n"),
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, len(fragments))
	for _, fragment := range fragments {
		chunks <- cliproxyexecutor.StreamChunk{Payload: fragment}
	}
	close(chunks)

	buffered, closed, err := readStreamBootstrap(context.Background(), chunks)
	if err != nil {
		t.Fatalf("readStreamBootstrap() error = %v", err)
	}
	if !closed {
		t.Fatal("readStreamBootstrap() forwarded empty Claude stream")
	}
	if !isEmptyCompletion(buffered) {
		t.Fatal("split Claude stream was not classified as empty at close")
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
func TestEmptyCompletionAudio(t *testing.T) {
	cases := []struct {
		name     string
		payload  []byte
		expected bool
	}{
		{
			name:     "delta audio transcript plus data is not empty",
			payload:  []byte(`data: {"id":"1","choices":[{"index":0,"delta":{"audio":{"transcript":"hi","data":"AQID"}},"finish_reason":"stop"}]}` + "\n\n" + `data: [DONE]` + "\n\n"),
			expected: false,
		},
		{
			name:     "delta audio transcript only is not empty",
			payload:  []byte(`data: {"id":"1","choices":[{"index":0,"delta":{"audio":{"transcript":"hi"}},"finish_reason":"stop"}]}` + "\n\n" + `data: [DONE]` + "\n\n"),
			expected: false,
		},
		{
			name:     "delta audio data only is not empty",
			payload:  []byte(`data: {"id":"1","choices":[{"index":0,"delta":{"audio":{"data":"AQID"}},"finish_reason":"stop"}]}` + "\n\n" + `data: [DONE]` + "\n\n"),
			expected: false,
		},
		{
			name:     "message audio non-stream is not empty",
			payload:  []byte(`{"id":"1","choices":[{"index":0,"message":{"role":"assistant","content":"","audio":{"transcript":"hi","data":"AQID"}},"finish_reason":"stop"}],"usage":{"completion_tokens":0}}`),
			expected: false,
		},
		{
			name:     "delta audio null stays empty",
			payload:  []byte(`data: {"id":"1","choices":[{"index":0,"delta":{"audio":null},"finish_reason":"stop"}]}` + "\n\n" + `data: [DONE]` + "\n\n"),
			expected: true,
		},
		{
			name:     "delta audio empty object stays empty",
			payload:  []byte(`data: {"id":"1","choices":[{"index":0,"delta":{"audio":{}},"finish_reason":"stop"}]}` + "\n\n" + `data: [DONE]` + "\n\n"),
			expected: true,
		},
		{
			name:     "delta audio empty fields stay empty",
			payload:  []byte(`data: {"id":"1","choices":[{"index":0,"delta":{"audio":{"transcript":"","data":""}},"finish_reason":"stop"}]}` + "\n\n" + `data: [DONE]` + "\n\n"),
			expected: true,
		},
		{
			name:     "message audio recursively empty stays empty",
			payload:  []byte(`{"id":"1","choices":[{"index":0,"message":{"audio":{"transcript":"   ","nested":{"items":[null,false,0,"",{},[]]}}},"finish_reason":"stop"}]}`),
			expected: true,
		},
		{
			name:     "delta audio id only is not empty",
			payload:  []byte(`data: {"id":"1","choices":[{"index":0,"delta":{"audio":{"id":"audio-1"}},"finish_reason":"stop"}]}` + "\n\n" + `data: [DONE]` + "\n\n"),
			expected: false,
		},
		{
			name:     "delta audio positive expires at is not empty",
			payload:  []byte(`data: {"id":"1","choices":[{"index":0,"delta":{"audio":{"expires_at":1}},"finish_reason":"stop"}]}` + "\n\n" + `data: [DONE]` + "\n\n"),
			expected: false,
		},
		{
			name:     "delta audio malformed frame fails safe as non-empty",
			payload:  []byte(`data: {"id":"1","choices":[{"index":0,"delta":{"audio":"unterminated},"finish_reason":"stop"}]}` + "\n\n" + `data: [DONE]` + "\n\n"),
			expected: false,
		},
		{
			name:     "delta audio malformed with text stays not empty",
			payload:  []byte(`data: {"id":"1","choices":[{"index":0,"delta":{"content":"text","audio":"unterminated},"finish_reason":"stop"}]}` + "\n\n" + `data: [DONE]` + "\n\n"),
			expected: false,
		},
		{
			name:     "audio with malformed usage stays not empty",
			payload:  []byte(`data: {"id":"1","choices":[{"index":0,"delta":{"audio":{"transcript":"hi"}},"finish_reason":"stop"}],"usage":{"completion_tokens":"abc"}}` + "\n\n" + `data: [DONE]` + "\n\n"),
			expected: false,
		},
		{
			name:     "raw json audio frame is not empty",
			payload:  []byte(`{"id":"1","choices":[{"index":0,"delta":{"audio":{"transcript":"hi","data":"AQID"}},"finish_reason":"stop"}]}`),
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

// TestEmptyCompletionMeaningfulFields covers the targeted meaningful-content
// fields: Gemini executableCode/codeExecutionResult parts and OpenAI legacy
// message.function_call (and its streaming delta.function_call form). A value
// is meaningful only when it carries actual payload; null, empty string, empty
// object, and empty array stay empty.
func TestEmptyCompletionMeaningfulFields(t *testing.T) {
	cases := []struct {
		name     string
		payload  []byte
		expected bool // true = empty completion
	}{
		{
			name:     "gemini executableCode with payload is not empty",
			payload:  []byte(`data: {"candidates":[{"content":{"role":"model","parts":[{"executableCode":{"language":"python","code":"print(1)"}}]},"finishReason":"STOP"}]}`),
			expected: false,
		},
		{
			name:     "gemini executableCode null stays empty",
			payload:  []byte(`data: {"candidates":[{"content":{"role":"model","parts":[{"executableCode":null}]},"finishReason":"STOP"}]}`),
			expected: true,
		},
		{
			name:     "gemini executableCode empty object stays empty",
			payload:  []byte(`data: {"candidates":[{"content":{"role":"model","parts":[{"executableCode":{}}]},"finishReason":"STOP"}]}`),
			expected: true,
		},
		{
			name:     "gemini codeExecutionResult with payload is not empty",
			payload:  []byte(`data: {"candidates":[{"content":{"role":"model","parts":[{"codeExecutionResult":{"outcome":"OK","output":"1"}}]},"finishReason":"STOP"}]}`),
			expected: false,
		},
		{
			name:     "gemini codeExecutionResult null stays empty",
			payload:  []byte(`data: {"candidates":[{"content":{"role":"model","parts":[{"codeExecutionResult":null}]},"finishReason":"STOP"}]}`),
			expected: true,
		},
		{
			name:     "gemini codeExecutionResult empty object stays empty",
			payload:  []byte(`data: {"candidates":[{"content":{"role":"model","parts":[{"codeExecutionResult":{}}]},"finishReason":"STOP"}]}`),
			expected: true,
		},
		{
			name:     "openai non-stream message function_call name only is not empty",
			payload:  []byte(`{"choices":[{"message":{"function_call":{"name":"get_weather","arguments":""}},"finish_reason":"stop"}]}`),
			expected: false,
		},
		{
			name:     "openai non-stream message function_call arguments only is not empty",
			payload:  []byte(`{"choices":[{"message":{"function_call":{"name":"","arguments":"{\"city\":\"x\"}"}},"finish_reason":"stop"}]}`),
			expected: false,
		},
		{
			name:     "openai non-stream message function_call empty object stays empty",
			payload:  []byte(`{"choices":[{"message":{"function_call":{}},"finish_reason":"stop"}]}`),
			expected: true,
		},
		{
			name:     "openai non-stream message function_call null stays empty",
			payload:  []byte(`{"choices":[{"message":{"function_call":null},"finish_reason":"stop"}]}`),
			expected: true,
		},
		{
			name:     "openai non-stream message function_call whitespace fields stay empty",
			payload:  []byte(`{"choices":[{"message":{"function_call":{"name":"  ","arguments":"  "}},"finish_reason":"stop"}]}`),
			expected: true,
		},
		{
			name:     "openai sse delta function_call is not empty",
			payload:  []byte("data: {\"choices\":[{\"delta\":{\"function_call\":{\"name\":\"get_weather\",\"arguments\":\"{}\"}},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"),
			expected: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsEmptyCompletionPayload(tc.payload)
			if got != tc.expected {
				t.Fatalf("IsEmptyCompletionPayload = %v, want %v\npayload: %s", got, tc.expected, tc.payload)
			}
		})
	}
}

// TestEmptyCompletionFraming covers aggregated raw JSON payloads that carry one
// or more top-level values (NDJSON, whitespace/concat, pretty) evaluated through
// the protocol evaluators. Malformed or trailing garbage must stay non-empty
// (safe to forward).
func TestEmptyCompletionFraming(t *testing.T) {
	cases := []struct {
		name     string
		payload  []byte
		expected bool // true = empty completion
	}{
		{
			name:     "ndjson second frame meaningful",
			payload:  []byte("{\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"completion_tokens\":0}}\n{\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}],\"usage\":{\"completion_tokens\":5}}"),
			expected: false,
		},
		{
			name:     "concatenated second frame meaningful",
			payload:  []byte("{\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"completion_tokens\":0}}{\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}],\"usage\":{\"completion_tokens\":5}}"),
			expected: false,
		},
		{
			name:     "ndjson all empty terminal",
			payload:  []byte("{\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"completion_tokens\":0}}\n{\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"completion_tokens\":0}}"),
			expected: true,
		},
		{
			name:     "pretty multiline meaningful",
			payload:  []byte("{\n  \"choices\": [\n    {\"delta\": {\"content\": \"hi\"}, \"finish_reason\": \"stop\"}\n  ],\n  \"usage\": {\"completion_tokens\": 5}\n}"),
			expected: false,
		},
		{
			name:     "ndjson trailing garbage not empty",
			payload:  []byte("{\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"completion_tokens\":0}}\nnot-json"),
			expected: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsEmptyCompletionPayload(tc.payload)
			if got != tc.expected {
				t.Fatalf("IsEmptyCompletionPayload = %v, want %v\npayload: %s", got, tc.expected, tc.payload)
			}
		})
	}
}

// TestStreamBootstrapDetectorRawJSON verifies a single raw JSON value split at
// every byte boundary (including inside string escapes and multi-byte UTF-8)
// never forwards prematurely and forwards promptly once complete.
func TestStreamBootstrapDetectorRawJSON(t *testing.T) {
	raws := []struct {
		name string
		raw  []byte
	}{
		{"ascii", []byte(`{"choices":[{"delta":{"content":"hi"},"finish_reason":"stop"}]}`)},
		{"string-escape", []byte(`{"choices":[{"delta":{"content":"a\nb"},"finish_reason":"stop"}]}`)},
		{"utf8", []byte(`{"choices":[{"delta":{"content":"😀"},"finish_reason":"stop"}]}`)},
	}
	for _, r := range raws {
		for i := 0; i <= len(r.raw); i++ {
			d := &StreamBootstrapDetector{}
			first := d.Observe(r.raw[:i])
			second := d.Observe(r.raw[i:])
			if i == len(r.raw) {
				if !first {
					t.Fatalf("%s split at %d: expected forward after full value, got %v", r.name, i, first)
				}
				continue
			}
			if first {
				t.Fatalf("%s split at %d: premature forward on prefix %q", r.name, i, r.raw[:i])
			}
			if !second {
				t.Fatalf("%s split at %d: expected forward after completion, got %v", r.name, i, second)
			}
		}
	}
}

// TestStreamBootstrapDetectorRawConcatenated verifies two raw JSON frames
// delivered as concatenated values (no newline); the detector must not forward
// on the empty first frame and must forward once the meaningful second lands.
func TestStreamBootstrapDetectorRawConcatenated(t *testing.T) {
	d := &StreamBootstrapDetector{}
	if got := d.Observe([]byte(`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"completion_tokens":0}}`)); got != false {
		t.Fatalf("Observe(first empty frame) = %v, want false", got)
	}
	if got := d.Observe([]byte(`{"choices":[{"delta":{"content":"hi"},"finish_reason":"stop"}],"usage":{"completion_tokens":5}}`)); got != true {
		t.Fatalf("Observe(second meaningful frame) = %v, want true", got)
	}
}

// TestStreamBootstrapDetectorRawSSEPrefixes verifies incomplete SSE command
// prefixes (d/da/data/data:/: and a split [DONE]) keep buffering, preserving
// the current SSE bootstrap contract.
func TestStreamBootstrapDetectorRawSSEPrefixes(t *testing.T) {
	for _, p := range [][]byte{[]byte("d"), []byte("da"), []byte("data"), []byte("data:"), []byte(":")} {
		d := &StreamBootstrapDetector{}
		if got := d.Observe(p); got != false {
			t.Fatalf("Observe(%q) = %v, want false (incomplete SSE prefix)", p, got)
		}
	}
	d := &StreamBootstrapDetector{}
	if got := d.Observe([]byte("data: [DO")); got != false {
		t.Fatalf("Observe(split [DONE]) = %v, want false", got)
	}
	if got := d.Observe([]byte("NE]\n\n")); got != false {
		t.Fatalf("Observe(completed [DONE]) = %v, want false (empty terminal stays buffered)", got)
	}
}

func TestStreamBootstrapDetectorNewlineLessSSE(t *testing.T) {
	t.Run("complete newline-less content frame forwards immediately", func(t *testing.T) {
		d := &StreamBootstrapDetector{}
		payload := []byte(`data: {"choices":[{"delta":{"content":"hello"}}],"finish_reason":null}`)
		if got := d.Observe(payload); got != true {
			t.Fatalf("Observe(newline-less content) = %v, want true", got)
		}
		if !d.state.forward {
			t.Fatal("state.forward = false, want true")
		}
	})

	t.Run("complete newline-less empty terminal frame stays buffered", func(t *testing.T) {
		d := &StreamBootstrapDetector{}
		payload := []byte(`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`)
		if got := d.Observe(payload); got != false {
			t.Fatalf("Observe(newline-less empty terminal) = %v, want false", got)
		}
		if d.state.forward {
			t.Fatal("state.forward = true, want false")
		}
		if !d.state.acc.empty() {
			t.Fatal("acc.empty() = false, want true for empty terminal frame")
		}
	})

	t.Run("following newline-less [DONE] remains terminal-empty", func(t *testing.T) {
		d := &StreamBootstrapDetector{}
		emptyFrame := []byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		if got := d.Observe(emptyFrame); got != false {
			t.Fatalf("Observe(empty frame) = %v, want false", got)
		}
		doneFrame := []byte("data: [DONE]")
		if got := d.Observe(doneFrame); got != false {
			t.Fatalf("Observe(newline-less [DONE]) = %v, want false", got)
		}
		if d.state.forward {
			t.Fatal("state.forward = true, want false")
		}
		if !d.state.acc.empty() {
			t.Fatal("acc.empty() = false, want true after [DONE]")
		}
	})

	t.Run("split truncated JSON and split [DONE] do not forward prematurely", func(t *testing.T) {
		d := &StreamBootstrapDetector{}
		if got := d.Observe([]byte(`data: {"choices":[{"delta":{"content":"hel`)); got != false {
			t.Fatalf("Observe(truncated JSON) = %v, want false", got)
		}
		if got := d.Observe([]byte(`lo"}}],"finish_reason":null}`)); got != true {
			t.Fatalf("Observe(completed JSON remainder) = %v, want true", got)
		}

		d2 := &StreamBootstrapDetector{}
		if got := d2.Observe([]byte("data: [DO")); got != false {
			t.Fatalf("Observe(split [DONE] part 1) = %v, want false", got)
		}
		if got := d2.Observe([]byte("NE]")); got != false {
			t.Fatalf("Observe(split [DONE] part 2) = %v, want false", got)
		}
		if d2.state.forward {
			t.Fatal("state.forward after split [DONE] = true, want false")
		}
		if !d2.state.acc.empty() {
			t.Fatal("acc.empty() = false, want true after complete [DONE]")
		}
	})
}

func TestEmptyCompletion_MultiChunkBoundarySafety(t *testing.T) {
	t.Run("two complete newline-less data chunks plus terminal DONE classify empty", func(t *testing.T) {
		chunks := []cliproxyexecutor.StreamChunk{
			{Payload: []byte("data: {\"id\":\"1\",\"choices\":[{\"delta\":{\"content\":\"\"}}]}")},
			{Payload: []byte("data: [DONE]")},
		}
		if !isEmptyCompletion(chunks) {
			t.Fatal("isEmptyCompletion = false, want true")
		}
	})

	t.Run("split JSON string fragments concatenate and classify non-empty", func(t *testing.T) {
		chunks := []cliproxyexecutor.StreamChunk{
			{Payload: []byte("data: {\"id\":\"1\",\"choices\":[{\"delta\":{\"content\":\"hello ")},
			{Payload: []byte("world\"}}]}\n")},
		}
		if isEmptyCompletion(chunks) {
			t.Fatal("isEmptyCompletion = true, want false")
		}
	})

	t.Run("boundary before nested object remains valid", func(t *testing.T) {
		chunks := []cliproxyexecutor.StreamChunk{
			{Payload: []byte("data: ")},
			{Payload: []byte("{\"id\":\"1\",\"choices\":[{\"delta\":{\"content\":\"\"}}]}")},
			{Payload: []byte("\ndata: [DONE]\n")},
		}
		if !isEmptyCompletion(chunks) {
			t.Fatal("isEmptyCompletion = false, want true")
		}
	})

	t.Run("unknown custom stream remains unrecognized and non-empty", func(t *testing.T) {
		chunks := []cliproxyexecutor.StreamChunk{
			{Payload: []byte("custom_binary_payload_format")},
		}
		if isEmptyCompletion(chunks) {
			t.Fatal("isEmptyCompletion = true, want false for unrecognized stream")
		}
	})

	t.Run("detector finish flushes pending at EOF", func(t *testing.T) {
		var detector StreamBootstrapDetector
		if detector.Observe([]byte("data: {\"id\":\"1\",\"choices\":[{\"delta\":{\"content\":\"\"}}]}")) {
			t.Fatal("Observe() = true, want false")
		}
		if detector.Observe([]byte("data: [DONE]")) {
			t.Fatal("Observe() = true, want false")
		}
		if !detector.Finish() {
			t.Fatal("detector.Finish() = false, want true")
		}
	})
}
