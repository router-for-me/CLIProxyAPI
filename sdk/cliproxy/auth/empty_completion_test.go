package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

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
	leaveStreamOpen      bool
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
	if len(e.executePayloads) == 0 && e.firstExecute == auth.ID {
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
		if !e.leaveStreamOpen {
			close(chunks)
		}
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
			name:     "openai sse semantically empty tool_calls null",
			payload:  []byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[null]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n"),
			expected: true,
		},
		{
			name:     "openai sse semantically empty tool_calls empty object",
			payload:  []byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n"),
			expected: true,
		},
		{
			name:     "openai json semantically empty tool_calls empty fields",
			payload:  []byte(`{"choices":[{"message":{"tool_calls":[{"id":"","type":"function","function":{"name":"","arguments":""}}]},"finish_reason":"tool_calls"}]}`),
			expected: true,
		},
		{
			name:     "openai json semantically empty tool_calls empty object args",
			payload:  []byte(`{"choices":[{"message":{"tool_calls":[{"id":"","type":"function","function":{"name":"","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`),
			expected: true,
		},
		{
			name:     "openai json semantically empty tool_calls empty array args",
			payload:  []byte(`{"choices":[{"message":{"tool_calls":[{"id":"","type":"function","function":{"name":"","arguments":"[]"}}]},"finish_reason":"tool_calls"}]}`),
			expected: true,
		},
		{
			name:     "openai json semantically empty tool_calls null args",
			payload:  []byte(`{"choices":[{"message":{"tool_calls":[{"id":"","type":"function","function":{"name":"","arguments":"null"}}]},"finish_reason":"tool_calls"}]}`),
			expected: true,
		},
		{
			name:     "openai sse semantically empty tool_calls empty object args",
			payload:  []byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"id\":\"\",\"function\":{\"name\":\"\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n"),
			expected: true,
		},
		{
			name:     "openai json semantically empty legacy function_call empty object args",
			payload:  []byte(`{"choices":[{"message":{"function_call":{"name":"","arguments":"{}"}},"finish_reason":"function_call"}]}`),
			expected: true,
		},
		{
			name:     "openai json semantically empty legacy function_call null args",
			payload:  []byte(`{"choices":[{"message":{"function_call":{"name":"","arguments":"null"}},"finish_reason":"function_call"}]}`),
			expected: true,
		},
		{
			name:     "openai json meaningful tool_calls with real args",
			payload:  []byte(`{"choices":[{"message":{"tool_calls":[{"id":"","type":"function","function":{"name":"","arguments":"{\"location\":\"Paris\"}"}}]},"finish_reason":"tool_calls"}]}`),
			expected: false,
		},
		{
			name:     "openai json meaningful legacy function_call with real args",
			payload:  []byte(`{"choices":[{"message":{"function_call":{"name":"","arguments":"{\"query\":\"test\"}"}},"finish_reason":"function_call"}]}`),
			expected: false,
		},
		{
			name:     "openai json meaningful tool_calls",
			payload:  []byte(`{"choices":[{"message":{"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`),
			expected: false,
		},
		{
			name:     "openai sse meaningful tool_calls",
			payload:  []byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"id\":\"call_1\",\"function\":{\"name\":\"get_weather\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n"),
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
			name:     "unterminated is empty",
			payload:  []byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":null}]}\n\n"),
			expected: true,
		},
		{
			name:     "claude sse message_stop without end_turn is empty",
			payload:  []byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"),
			expected: true,
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
			name:     "openai sse with id and retry metadata then empty is empty",
			payload:  []byte("id: 12345\nretry: 3000\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"completion_tokens\":0}}\n\ndata: [DONE]\n\n"),
			expected: true,
		},
		{
			name:     "openai sse with unknown field then empty is not empty",
			payload:  []byte("x-unknown: 123\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"completion_tokens\":0}}\n\ndata: [DONE]\n\n"),
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
			name:     "openai json semantically empty tool_calls null",
			payload:  []byte(`{"choices":[{"message":{"tool_calls":[null]},"finish_reason":"tool_calls"}]}`),
			expected: true,
		},
		{
			name:     "openai json semantically empty tool_calls empty object",
			payload:  []byte(`{"choices":[{"message":{"tool_calls":[{}]},"finish_reason":"tool_calls"}]}`),
			expected: true,
		},
		{
			name:     "openai json semantically empty tool_calls empty fields",
			payload:  []byte(`{"choices":[{"message":{"tool_calls":[{"id":"","type":"function","function":{"name":"","arguments":""}}]},"finish_reason":"tool_calls"}]}`),
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
			name:     "gemini non-stream null functionCall part is empty",
			payload:  []byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":null}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":0}}`),
			expected: true,
		},
		{
			name:     "gemini non-stream empty inlineData object part is empty",
			payload:  []byte(`{"candidates":[{"content":{"role":"model","parts":[{"inlineData":{}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":0}}`),
			expected: true,
		},
		{
			name:     "gemini non-stream whitespace object inlineData part is empty",
			payload:  []byte(`{"candidates":[{"content":{"role":"model","parts":[{"inlineData":{  }}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":0}}`),
			expected: true,
		},
		{
			name:     "gemini non-stream whitespace array functionCall part is empty",
			payload:  []byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":[  ]}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":0}}`),
			expected: true,
		},
		{
			name:     "gemini sse whitespace inlineData stream is empty",
			payload:  []byte("data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"inlineData\":{ }}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"candidatesTokenCount\":0}}\n\n"),
			expected: true,
		},
		{
			name:     "gemini sse whitespace functionCall array stream is empty",
			payload:  []byte("data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"functionCall\":[ ]}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"candidatesTokenCount\":0}}\n\n"),
			expected: true,
		},
		{
			name:     "gemini non-stream null functionResponse part is empty",
			payload:  []byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionResponse":null}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":0}}`),
			expected: true,
		},
		{
			name:     "gemini non-stream with functionCall part is not empty",
			payload:  []byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"search","args":{}}}]},"finishReason":"STOP"}]}`),
			expected: false,
		},
		{
			name:     "gemini non-stream with empty-name empty-args functionCall is empty",
			payload:  []byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"","args":{}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":0}}`),
			expected: true,
		},
		{
			name:     "gemini sse with empty-name empty-args functionCall is empty",
			payload:  []byte("data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"functionCall\":{\"name\":\"\",\"args\":{}}}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"candidatesTokenCount\":0}}\n\n"),
			expected: true,
		},
		{
			name:     "gemini non-stream with functionCall args and empty name is not empty",
			payload:  []byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"","args":{"query":"hello"}}}]},"finishReason":"STOP"}]}`),
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
			name:     "gemini non-stream with empty text and thought flag is empty",
			payload:  []byte(`{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":""}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":0}}`),
			expected: true,
		},
		{
			name:     "gemini non-stream with thought flag only is empty",
			payload:  []byte(`{"candidates":[{"content":{"role":"model","parts":[{"thought":true}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":0}}`),
			expected: true,
		},
		{
			name:     "gemini sse stream with empty text and thought flag is empty",
			payload:  []byte("data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"thought\":true,\"text\":\"\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"candidatesTokenCount\":0}}\n\n"),
			expected: true,
		},
		{
			name:     "antigravity stream with empty text and thought flag is empty",
			payload:  []byte("data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"thought\":true,\"text\":\"\"}]},\"finishReason\":\"STOP\"}]}}\n\n"),
			expected: true,
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
			name:     "openai legacy non-stream text content is not empty",
			payload:  []byte(`{"choices":[{"text":"hello","finish_reason":"stop"}]}`),
			expected: false,
		},
		{
			name:     "openai legacy non-stream text content with zero usage is not empty",
			payload:  []byte(`{"choices":[{"text":"hello","finish_reason":"stop"}],"usage":{"completion_tokens":0}}`),
			expected: false,
		},
		{
			name:     "openai legacy non-stream empty text is empty",
			payload:  []byte(`{"choices":[{"text":"","finish_reason":"stop"}],"usage":{"completion_tokens":0}}`),
			expected: true,
		},
		{
			name:     "openai legacy non-stream whitespace text is empty",
			payload:  []byte(`{"choices":[{"text":"   ","finish_reason":"stop"}],"usage":{"completion_tokens":0}}`),
			expected: true,
		},
		{
			name:     "openai legacy sse text content stream is not empty",
			payload:  []byte("data: {\"choices\":[{\"text\":\"hello\",\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"text\":\"\",\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"),
			expected: false,
		},
		{
			name:     "openai legacy sse empty text stream is empty",
			payload:  []byte("data: {\"choices\":[{\"text\":\"\",\"finish_reason\":\"stop\"}],\"usage\":{\"completion_tokens\":0}}\n\ndata: [DONE]\n\n"),
			expected: true,
		},
		{
			name:     "openai legacy sse whitespace text stream is empty",
			payload:  []byte("data: {\"choices\":[{\"text\":\"   \",\"finish_reason\":\"stop\"}],\"usage\":{\"completion_tokens\":0}}\n\ndata: [DONE]\n\n"),
			expected: true,
		},
		{
			name:     "codex responses-api sse completed with empty output passes through (never empty by contract)",
			payload:  []byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[],\"usage\":{\"output_tokens\":0}}}\n\ndata: [DONE]\n\n"),
			expected: false,
		},
		{
			name:     "codex responses-api non-stream completed with empty output passes through (never empty by contract)",
			payload:  []byte(`{"object":"response","id":"r","status":"completed","output":[],"usage":{"output_tokens":0}}`),
			expected: false,
		},
		{
			name:     "codex responses-api sse output_item message empty then completed passes through",
			payload:  []byte("data: {\"type\":\"response.output_item.done\",\"output\":{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"\",\"annotations\":[]}],\"status\":\"completed\"}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"\",\"annotations\":[]}]}],\"usage\":{\"output_tokens\":0}}}\n\ndata: [DONE]\n\n"),
			expected: false,
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
			name:     "openai empty choices without usage is terminal and empty",
			payload:  []byte(`{"choices":[]}`),
			expected: true,
		},
		{
			name:     "openai empty choices with null usage is terminal and empty",
			payload:  []byte(`{"choices":[],"usage":null}`),
			expected: true,
		},
		{
			name:     "openai array content parts pass through as unknown data",
			payload:  []byte(`{"id":"chatcmpl-x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":[{"type":"output_text","text":"hello"}]},"finish_reason":"stop"}]}`),
			expected: false,
		},
		{
			name:     "openai empty refusal string is terminal and empty",
			payload:  []byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"","refusal":""},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":0,"total_tokens":5}}`),
			expected: true,
		},
		{
			name:     "openai real refusal string is not empty",
			payload:  []byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":null,"refusal":"I cannot help with that"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":0,"total_tokens":5}}`),
			expected: false,
		},
		{
			name:     "zero-length body is an empty completion",
			payload:  []byte(``),
			expected: true,
		},
		{
			name:     "whitespace-only body is an empty completion",
			payload:  []byte("  \n\t "),
			expected: true,
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

func TestStreamBootstrapDetectorSSEMetadataFields(t *testing.T) {
	var detector StreamBootstrapDetector
	if detector.Observe([]byte("id: evt_12345\nretry: 5000\n")) {
		t.Fatal("Observe() forwarded after standard SSE id/retry metadata")
	}
	if detector.Observe([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"completion_tokens\":0}}\n\n")) {
		t.Fatal("Observe() forwarded recognized empty terminal chunk")
	}
	if detector.Observe([]byte("data: [DONE]\n\n")) {
		t.Fatal("Observe() forwarded [DONE]")
	}
	if !detector.Finish() {
		t.Fatal("Finish() = false, want empty completion recognized despite id/retry metadata")
	}

	var detectorUnknown StreamBootstrapDetector
	if !detectorUnknown.Observe([]byte("x-unknown-metadata: foo\n")) {
		t.Fatal("Observe() = false, want unknown SSE metadata to force forwarding")
	}
}

func TestStreamBootstrapDetectorMetadataOnlyEOF(t *testing.T) {
	t.Run("comments and keepalive only then EOF classifies as empty", func(t *testing.T) {
		var detector StreamBootstrapDetector
		if detector.Observe([]byte(": keep-alive\n\n")) {
			t.Fatal("Observe() forwarded keep-alive comment")
		}
		if !detector.Finish() {
			t.Fatal("Finish() = false, want metadata-only stream recognized as empty completion at EOF")
		}
	})

	t.Run("id and retry metadata only then EOF classifies as empty", func(t *testing.T) {
		var detector StreamBootstrapDetector
		if detector.Observe([]byte("id: evt_12345\nretry: 5000\n\n")) {
			t.Fatal("Observe() forwarded id/retry metadata")
		}
		if !detector.Finish() {
			t.Fatal("Finish() = false, want id/retry-only stream recognized as empty completion at EOF")
		}
	})

	t.Run("claude ping only then EOF classifies as empty", func(t *testing.T) {
		var detector StreamBootstrapDetector
		if detector.Observe([]byte("event: ping\ndata: {\"type\":\"ping\"}\n\n")) {
			t.Fatal("Observe() forwarded ping metadata")
		}
		if !detector.Finish() {
			t.Fatal("Finish() = false, want ping-only stream recognized as empty completion at EOF")
		}
	})

	t.Run("data-bearing stream still forwards and does not classify as empty", func(t *testing.T) {
		var detector StreamBootstrapDetector
		if !detector.Observe([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")) {
			t.Fatal("Observe() = false, want data-bearing stream to forward")
		}
		if detector.Finish() {
			t.Fatal("Finish() = true, want data-bearing stream not recognized as empty completion")
		}
	})

	t.Run("unknown non-SSE format keeps existing behavior", func(t *testing.T) {
		var detector StreamBootstrapDetector
		if !detector.Observe([]byte("{\"status\":\"running\"}")) {
			t.Fatal("Observe() = false, want unknown format to forward")
		}
		if detector.Finish() {
			t.Fatal("Finish() = true, want unknown format not recognized as empty completion")
		}
	})

	t.Run("isEmptyCompletionPayload classifies metadata-only SSE payloads as empty", func(t *testing.T) {
		if !IsEmptyCompletionPayload([]byte(": keep-alive\n\n")) {
			t.Fatal("IsEmptyCompletionPayload() = false for comment-only SSE")
		}
		if !IsEmptyCompletionPayload([]byte("id: evt_12345\nretry: 5000\n\n")) {
			t.Fatal("IsEmptyCompletionPayload() = false for id/retry-only SSE")
		}
		if !IsEmptyCompletionPayload([]byte("event: ping\ndata: {\"type\":\"ping\"}\n\n")) {
			t.Fatal("IsEmptyCompletionPayload() = false for ping-only SSE")
		}
	})
}

func TestStreamBootstrapDetectorTerminalBlockedForwardsImmediately(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{
			name:    "openai content_filter",
			payload: "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"content_filter\"}]}\n\n",
		},
		{
			name:    "openai length",
			payload: "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"length\"}]}\n\n",
		},
		{
			name:    "gemini safety candidate",
			payload: "data: {\"candidates\":[{\"finishReason\":\"SAFETY\"}]}\n\n",
		},
		{
			name:    "gemini prompt feedback block",
			payload: "data: {\"promptFeedback\":{\"blockReason\":\"SAFETY\"}}\n\n",
		},
		{
			name:    "claude refusal",
			payload: "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"refusal\"}}\n\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var detector StreamBootstrapDetector
			if !detector.Observe([]byte(tt.payload)) {
				t.Fatalf("Observe() = false, want terminal blocked frame to forward immediately without waiting for EOF")
			}
			if detector.Finish() {
				t.Fatalf("Finish() = true, want terminal blocked stream not to be classified as empty completion")
			}
		})
	}
}

func TestStreamBootstrapDetectorEmptyDataEventsClassifyAsEmpty(t *testing.T) {
	t.Run("single empty data event", func(t *testing.T) {
		var detector StreamBootstrapDetector
		if detector.Observe([]byte("data:\n\n")) {
			t.Fatal("Observe() forwarded empty data event")
		}
		if !detector.Finish() {
			t.Fatal("Finish() = false, want empty data event stream classified as empty completion at EOF")
		}
	})

	t.Run("empty data event with whitespace", func(t *testing.T) {
		var detector StreamBootstrapDetector
		if detector.Observe([]byte("data:   \n\n")) {
			t.Fatal("Observe() forwarded whitespace empty data event")
		}
		if !detector.Finish() {
			t.Fatal("Finish() = false, want whitespace empty data event stream classified as empty completion at EOF")
		}
	})

	t.Run("multiple empty data events", func(t *testing.T) {
		var detector StreamBootstrapDetector
		if detector.Observe([]byte("data:\n\ndata:\n\n")) {
			t.Fatal("Observe() forwarded multiple empty data events")
		}
		if !detector.Finish() {
			t.Fatal("Finish() = false, want multiple empty data events classified as empty completion at EOF")
		}
	})

	t.Run("isEmptyCompletionPayload classifies empty data event as empty", func(t *testing.T) {
		if !IsEmptyCompletionPayload([]byte("data:\n\n")) {
			t.Fatal("IsEmptyCompletionPayload() = false for empty data: event")
		}
		if !IsEmptyCompletionPayload([]byte("data:   \n\n")) {
			t.Fatal("IsEmptyCompletionPayload() = false for whitespace empty data: event")
		}
	})
}

func TestStreamBootstrapDetectorOpaqueSSEMetadata(t *testing.T) {
	t.Run("event containing data: substring does not parse suffix as data", func(t *testing.T) {
		var detector StreamBootstrapDetector
		payload := []byte("event: metadata:ping\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		if detector.Observe(payload) {
			t.Fatal("Observe() forwarded empty completion stream with event: metadata:ping")
		}
		if !detector.Finish() {
			t.Fatal("Finish() = false, want empty completion recognized despite event: metadata:ping")
		}
		if detector.state.acc.sawUnknownData {
			t.Fatal("sawUnknownData = true, want event field value to remain opaque")
		}
	})

	t.Run("comment containing data: substring does not parse suffix as data", func(t *testing.T) {
		var detector StreamBootstrapDetector
		payload := []byte(": data: keep-alive\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		if detector.Observe(payload) {
			t.Fatal("Observe() forwarded empty completion stream with : data: keep-alive comment")
		}
		if !detector.Finish() {
			t.Fatal("Finish() = false, want empty completion recognized despite : data: keep-alive comment")
		}
		if detector.state.acc.sawUnknownData {
			t.Fatal("sawUnknownData = true, want comment field value to remain opaque")
		}
	})

	t.Run("isEmptyCompletionPayload classifies payload with metadata containing data: as empty", func(t *testing.T) {
		payload := []byte("event: metadata:ping\n: data: keep-alive\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		if !IsEmptyCompletionPayload(payload) {
			t.Fatal("IsEmptyCompletionPayload() = false for payload with metadata containing data:")
		}
	})

	t.Run("control: real data field with data: inside JSON value parses correctly", func(t *testing.T) {
		var detector StreamBootstrapDetector
		payload := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"data: hello\"},\"finish_reason\":null}]}\n\n")
		if !detector.Observe(payload) {
			t.Fatal("Observe() = false, want content payload to forward")
		}
		if detector.Finish() {
			t.Fatal("Finish() = true, want content stream not classified as empty")
		}
	})

	t.Run("split metadata line across chunk boundary followed by data-like content", func(t *testing.T) {
		var detector StreamBootstrapDetector
		// Chunk 1 has partial metadata line: "event:" without newline
		// Chunk 2 has continuation of event name "data:ping\n" followed by empty completion data line
		chunk1 := []byte("event:")
		chunk2 := []byte("data:ping\n")
		chunk3 := []byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")

		if detector.Observe(chunk1) {
			t.Fatal("Observe(chunk1) forwarded partial event line")
		}
		if detector.Observe(chunk2) {
			t.Fatal("Observe(chunk2) forwarded event line continuation")
		}
		if detector.state.acc.sawUnknownData {
			t.Fatal("sawUnknownData = true after event: continuation, want metadata field value to remain opaque")
		}
		if detector.Observe(chunk3) {
			t.Fatal("Observe(chunk3) forwarded empty completion stream")
		}
		if !detector.Finish() {
			t.Fatal("Finish() = false, want empty completion recognized when metadata line split across chunk boundary")
		}
	})

	t.Run("arbitrary split points of metadata lines do not set sawUnknownData", func(t *testing.T) {
		fullPayload := "event: metadata:ping_data:123\n: data: keep-alive\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
		for split := 1; split < 40; split++ {
			var detector StreamBootstrapDetector
			c1 := []byte(fullPayload[:split])
			c2 := []byte(fullPayload[split:])
			if detector.Observe(c1) {
				t.Fatalf("split %d: Observe(c1) forwarded unexpectedly", split)
			}
			if detector.Observe(c2) {
				t.Fatalf("split %d: Observe(c2) forwarded unexpectedly", split)
			}
			if detector.state.acc.sawUnknownData {
				t.Fatalf("split %d: sawUnknownData = true, want metadata value to remain opaque across split", split)
			}
			if !detector.Finish() {
				t.Fatalf("split %d: Finish() = false, want empty completion recognized", split)
			}
		}
	})
}

func TestStreamBootstrapDetectorMultilineSSE(t *testing.T) {
	t.Run("empty completion split across data fields remains buffered and recognized", func(t *testing.T) {
		var detector StreamBootstrapDetector
		fragments := [][]byte{
			[]byte("data: {\n"),
			[]byte("data:   \"id\": \"chatcmpl-test\",\n"),
			[]byte("data:   \"choices\": [\n"),
			[]byte("data:     {\n"),
			[]byte("data:       \"index\": 0,\n"),
			[]byte("data:       \"delta\": {},\n"),
			[]byte("data:       \"finish_reason\": \"stop\"\n"),
			[]byte("data:     }\n"),
			[]byte("data:   ],\n"),
			[]byte("data:   \"usage\": {\n"),
			[]byte("data:     \"prompt_tokens\": 5,\n"),
			[]byte("data:     \"completion_tokens\": 0,\n"),
			[]byte("data:     \"total_tokens\": 5\n"),
			[]byte("data:   }\n"),
			[]byte("data: }\n\n"),
			[]byte("data: [DONE]\n\n"),
		}
		for i, f := range fragments {
			if detector.Observe(f) {
				t.Fatalf("Observe(fragment %d: %q) forwarded empty completion stream", i, string(f))
			}
		}
		if !detector.Finish() {
			t.Fatal("Finish() = false, want multiline empty completion recognized")
		}
	})

	t.Run("multiline SSE with content forwards at event boundary", func(t *testing.T) {
		var detector StreamBootstrapDetector
		fragments := [][]byte{
			[]byte("data: {\n"),
			[]byte("data:   \"choices\": [\n"),
			[]byte("data:     {\n"),
			[]byte("data:       \"delta\": {\n"),
			[]byte("data:         \"content\": \"Hello\"\n"),
			[]byte("data:       }\n"),
			[]byte("data:     }\n"),
			[]byte("data:   ]\n"),
			[]byte("data: }\n\n"),
		}
		forwarded := false
		for _, f := range fragments {
			if detector.Observe(f) {
				forwarded = true
				break
			}
		}
		if !forwarded {
			t.Fatal("Observe() = false, want multiline content event to forward")
		}
		if detector.Finish() {
			t.Fatal("Finish() = true, want non-empty multiline stream not recognized as empty completion")
		}
	})

	t.Run("isEmptyCompletionPayload handles multiline SSE payloads", func(t *testing.T) {
		openaiEmpty := []byte("data: {\ndata:   \"choices\": [{\"delta\":{},\"finish_reason\":\"stop\"}],\ndata:   \"usage\": {\"completion_tokens\": 0}\ndata: }\n\ndata: [DONE]\n\n")
		if !IsEmptyCompletionPayload(openaiEmpty) {
			t.Fatal("IsEmptyCompletionPayload() = false for multiline OpenAI empty completion")
		}

		geminiEmpty := []byte("data: {\ndata:   \"candidates\": [\ndata:     {\"finishReason\": \"STOP\"}\ndata:   ],\ndata:   \"usageMetadata\": {\"candidatesTokenCount\": 0}\ndata: }\n\n")
		if !IsEmptyCompletionPayload(geminiEmpty) {
			t.Fatal("IsEmptyCompletionPayload() = false for multiline Gemini empty completion")
		}

		malformed := []byte("data: {\ndata:   not valid json\ndata: }\n\n")
		if IsEmptyCompletionPayload(malformed) {
			t.Fatal("IsEmptyCompletionPayload() = true for multiline malformed SSE")
		}
	})

	t.Run("event field between data fragments does not flush partial data prematurely", func(t *testing.T) {
		var detector StreamBootstrapDetector
		fragments := [][]byte{
			[]byte("data: {\"choices\":[\n"),
			[]byte("event: message\n"),
			[]byte("id: evt_999\n"),
			[]byte("data: ]}\n\n"),
			[]byte("data: [DONE]\n\n"),
		}
		for i, f := range fragments {
			if detector.Observe(f) {
				t.Fatalf("Observe(fragment %d: %q) forwarded stream with interleaved event/id fields", i, string(f))
			}
		}
		if !detector.Finish() {
			t.Fatal("Finish() = false, want empty completion recognized when event: field is interleaved between data lines")
		}

		interleavedPayload := []byte("data: {\"choices\":[\nevent: message\nid: evt_999\ndata: ]}\n\ndata: [DONE]\n\n")
		if !IsEmptyCompletionPayload(interleavedPayload) {
			t.Fatal("IsEmptyCompletionPayload() = false for payload with event: field between data: lines")
		}
	})

	t.Run("split event metadata and data without newline", func(t *testing.T) {
		var detector StreamBootstrapDetector
		fragments := [][]byte{
			[]byte("event: response.completed"),
			[]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"output\":[]}}"),
		}
		for _, f := range fragments {
			detector.Observe(f)
		}
		// Without a newline between chunks, "event: response.completeddata: ..." is an event line whose value happens to contain "data: ...".
		// Because SSE metadata is opaque and chunks without newlines are buffered as a single line, Finish() recognizes the stream as metadata-only (empty completion).
		if !detector.Finish() {
			t.Fatal("Finish() = false, want response.completed without newline recognized as metadata-only empty completion")
		}

		// When concatenated without a newline, "event: response.completeddata: ..." is a single event header with value "response.completeddata: ...".
		// Because SSE metadata values are treated as opaque, it must NOT split on the internal "data:" substring.
		singlePayload := []byte("event: response.completeddata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"output\":[]}}\n\n")
		if !IsEmptyCompletionPayload(singlePayload) {
			t.Fatal("IsEmptyCompletionPayload() = false for metadata-only payload without newline between event and data prefix")
		}
	})
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
	if !detector.Observe([]byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"custom_tool_call\",\"name\":\"shell\",\"input\":\"pwd\"}}\n\n")) {
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
			name:     "gemini executableCode whitespace object stays empty",
			payload:  []byte(`data: {"candidates":[{"content":{"role":"model","parts":[{"executableCode":{  }}]},"finishReason":"STOP"}]}`),
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
			name:     "gemini codeExecutionResult whitespace object stays empty",
			payload:  []byte(`data: {"candidates":[{"content":{"role":"model","parts":[{"codeExecutionResult":{  }}]},"finishReason":"STOP"}]}`),
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

func TestClaudeToolBlocksEmptyCompletion(t *testing.T) {
	t.Run("empty tool block without id name or input is empty completion", func(t *testing.T) {
		payload := []byte(`{"type":"message","role":"assistant","content":[{"type":"tool_use","input":null}],"stop_reason":"end_turn"}`)
		if !IsEmptyCompletionPayload(payload) {
			t.Fatal("IsEmptyCompletionPayload() = false for tool_use with null input and no name/id, want true")
		}
	})

	t.Run("empty tool block with empty input object and no id/name is empty completion", func(t *testing.T) {
		payload := []byte(`{"type":"message","role":"assistant","content":[{"type":"tool_use","input":{}}],"stop_reason":"end_turn"}`)
		if !IsEmptyCompletionPayload(payload) {
			t.Fatal("IsEmptyCompletionPayload() = false for tool_use with empty input and no name/id, want true")
		}
	})

	t.Run("tool block with valid name is recognized as tool call", func(t *testing.T) {
		payload := []byte(`{"type":"message","role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{}}],"stop_reason":"tool_use"}`)
		if IsEmptyCompletionPayload(payload) {
			t.Fatal("IsEmptyCompletionPayload() = true for valid tool_use with name/id, want false")
		}
	})

	t.Run("text block with lexical null input does not treat null as tool call", func(t *testing.T) {
		payload := []byte(`{"type":"message","role":"assistant","content":[{"type":"text","text":"","input":null}],"stop_reason":"end_turn"}`)
		if !IsEmptyCompletionPayload(payload) {
			t.Fatal("IsEmptyCompletionPayload() = false for text block with lexical null input, want true")
		}
	})
}

func TestClaudeToolUseStopReasonEmptyCompletion(t *testing.T) {
	t.Run("empty tool_use blocks with stop_reason tool_use is empty completion", func(t *testing.T) {
		payload := []byte(`{"type":"message","role":"assistant","content":[{"type":"tool_use","input":null}],"stop_reason":"tool_use"}`)
		if !IsEmptyCompletionPayload(payload) {
			t.Fatal("IsEmptyCompletionPayload() = false for empty tool_use block with stop_reason tool_use, want true")
		}
	})

	t.Run("empty tool_use blocks in sse stream with stop_reason tool_use is empty completion", func(t *testing.T) {
		payload := []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-3\",\"usage\":{\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"input\":null}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":0}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		if !IsEmptyCompletionPayload(payload) {
			t.Fatal("IsEmptyCompletionPayload() = false for stream with empty tool_use and stop_reason tool_use, want true")
		}
	})

	t.Run("control real tool_use with stop_reason tool_use is not empty", func(t *testing.T) {
		payload := []byte(`{"type":"message","role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"location":"San Francisco"}}],"stop_reason":"tool_use"}`)
		if IsEmptyCompletionPayload(payload) {
			t.Fatal("IsEmptyCompletionPayload() = true for real tool_use with stop_reason tool_use, want false")
		}
	})

	t.Run("claude mcp_tool_use with id and name is not empty completion", func(t *testing.T) {
		payload := []byte(`{"type":"message","role":"assistant","content":[{"type":"mcp_tool_use","id":"mcp_1","name":"server__tool","input":{}}],"stop_reason":"tool_use"}`)
		if IsEmptyCompletionPayload(payload) {
			t.Fatal("IsEmptyCompletionPayload() = true for mcp_tool_use with id and name, want false")
		}
	})

	t.Run("claude mcp_tool_use in sse stream with id and name is not empty completion", func(t *testing.T) {
		payload := []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-3\",\"usage\":{\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"mcp_tool_use\",\"id\":\"mcp_1\",\"name\":\"server__tool\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":0}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		if IsEmptyCompletionPayload(payload) {
			t.Fatal("IsEmptyCompletionPayload() = true for mcp_tool_use stream with id and name, want false")
		}
	})

	t.Run("claude mcp_tool_use missing id is empty completion", func(t *testing.T) {
		payload := []byte(`{"type":"message","role":"assistant","content":[{"type":"mcp_tool_use","id":"","name":"server__tool","input":{}}],"stop_reason":"tool_use"}`)
		if !IsEmptyCompletionPayload(payload) {
			t.Fatal("IsEmptyCompletionPayload() = false for mcp_tool_use missing id, want true")
		}
	})

	t.Run("claude mcp_tool_use missing name is empty completion", func(t *testing.T) {
		payload := []byte(`{"type":"message","role":"assistant","content":[{"type":"mcp_tool_use","id":"mcp_1","name":"","input":{}}],"stop_reason":"tool_use"}`)
		if !IsEmptyCompletionPayload(payload) {
			t.Fatal("IsEmptyCompletionPayload() = false for mcp_tool_use missing name, want true")
		}
	})

	t.Run("control stop_reason max_tokens without content is blocked and not empty completion", func(t *testing.T) {
		payload := []byte(`{"type":"message","role":"assistant","content":[],"stop_reason":"max_tokens"}`)
		if IsEmptyCompletionPayload(payload) {
			t.Fatal("IsEmptyCompletionPayload() = true for stop_reason max_tokens, want false (blocked)")
		}
	})

	t.Run("control stop_reason refusal without content is blocked and not empty completion", func(t *testing.T) {
		payload := []byte(`{"type":"message","role":"assistant","content":[],"stop_reason":"refusal"}`)
		if IsEmptyCompletionPayload(payload) {
			t.Fatal("IsEmptyCompletionPayload() = true for stop_reason refusal, want false (blocked)")
		}
	})
}

func TestPrettyPrintedJSONWithDataSubstringEmptyCompletion(t *testing.T) {
	t.Run("pretty-printed json with data substring is evaluated as empty completion", func(t *testing.T) {
		payload := []byte("{\n  \"id\": \"msg-data:123\",\n  \"choices\": [\n    {\n      \"message\": {\n        \"role\": \"assistant\",\n        \"content\": \"\"\n      },\n      \"finish_reason\": \"stop\"\n    }\n  ]\n}")
		if !IsEmptyCompletionPayload(payload) {
			t.Fatal("IsEmptyCompletionPayload() = false for pretty-printed JSON with data: substring, want true")
		}
	})
}

func TestClaudeInputJSONDeltaEmptyCompletion(t *testing.T) {
	t.Run("empty input_json_delta with empty partial_json and no preceding tool id/name is empty completion", func(t *testing.T) {
		payload := []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-3\",\"usage\":{\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"input\":null}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":0}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		if !IsEmptyCompletionPayload(payload) {
			t.Fatal("IsEmptyCompletionPayload() = false for stream with empty input_json_delta, want true")
		}
	})

	t.Run("meaningful input_json_delta sets tool calls", func(t *testing.T) {
		payload := []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-3\",\"usage\":{\"output_tokens\":0}}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"location\\\":\\\"SF\\\"}\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":0}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		if IsEmptyCompletionPayload(payload) {
			t.Fatal("IsEmptyCompletionPayload() = true for stream with meaningful input_json_delta, want false")
		}
	})
}

func TestClaudeEmptyThinkingBlockStartEmptyCompletion(t *testing.T) {
	t.Run("empty thinking content_block_start followed by message_stop is empty completion", func(t *testing.T) {
		payload := []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-3\",\"usage\":{\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":0}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		if !IsEmptyCompletionPayload(payload) {
			t.Fatal("IsEmptyCompletionPayload() = false for empty thinking block start with message_stop, want true")
		}
	})

	t.Run("thinking content_block_start followed by thinking_delta with text is not empty", func(t *testing.T) {
		payload := []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-3\",\"usage\":{\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"thinking step\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":0}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		if IsEmptyCompletionPayload(payload) {
			t.Fatal("IsEmptyCompletionPayload() = true for thinking block with thinking_delta text, want false")
		}
	})

	t.Run("empty redacted_thinking content_block_start followed by message_stop is empty completion", func(t *testing.T) {
		payload := []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-3\",\"usage\":{\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"redacted_thinking\",\"data\":\"\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":0}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		if !IsEmptyCompletionPayload(payload) {
			t.Fatal("IsEmptyCompletionPayload() = false for empty redacted_thinking block start with message_stop, want true")
		}
	})

	t.Run("non-empty redacted_thinking content_block_start followed by message_stop is not empty", func(t *testing.T) {
		payload := []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-3\",\"usage\":{\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"redacted_thinking\",\"data\":\"abc123encryptedpayload\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":0}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		if IsEmptyCompletionPayload(payload) {
			t.Fatal("IsEmptyCompletionPayload() = true for non-empty redacted_thinking block, want false")
		}
	})
}

func TestRecognizedContentlessEOFEmptyStream(t *testing.T) {
	t.Run("OpenAI role-only delta stream closed at EOF without [DONE] is empty completion", func(t *testing.T) {
		var detector StreamBootstrapDetector
		payload := []byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\n")
		if detector.Observe(payload) {
			t.Fatal("Observe() = true for role-only delta, want false")
		}
		if !detector.Finish() {
			t.Fatal("Finish() = false at EOF for recognized role-only stream without content, want true")
		}
	})

	t.Run("Claude message_start stream closed at EOF without message_stop is empty completion", func(t *testing.T) {
		var detector StreamBootstrapDetector
		payload := []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-3\",\"usage\":{\"output_tokens\":0}}}\n\n")
		if detector.Observe(payload) {
			t.Fatal("Observe() = true for message_start, want false")
		}
		if !detector.Finish() {
			t.Fatal("Finish() = false at EOF for recognized message_start stream without content, want true")
		}
	})

	t.Run("OpenAI delta stream with content closed at EOF is not empty", func(t *testing.T) {
		var detector StreamBootstrapDetector
		payload := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
		if !detector.Observe(payload) {
			t.Fatal("Observe() = false for stream with content, want true")
		}
		if detector.Finish() {
			t.Fatal("Finish() = true for stream with content, want false")
		}
	})

	t.Run("unknown-format stream closed at EOF remains non-empty and forwards", func(t *testing.T) {
		var detector StreamBootstrapDetector
		payload := []byte("data: {\"unknown_payload\":true}\n\n")
		if !detector.Observe(payload) {
			t.Fatal("Observe() = false for unknown-format, want true (force forward)")
		}
		if detector.Finish() {
			t.Fatal("Finish() = true for unknown-format, want false")
		}
	})
}

func TestColonlessSSEFields(t *testing.T) {
	t.Run("stream with colonless event and id fields then empty data is recognized as empty completion", func(t *testing.T) {
		var detector StreamBootstrapDetector
		payload := []byte("event\nid\nretry\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		if detector.Observe(payload) {
			t.Fatal("Observe() = true for stream with colonless metadata lines, want false")
		}
		if !detector.Finish() {
			t.Fatal("Finish() = false, want empty completion recognized for colonless metadata")
		}
		if detector.state.acc.sawUnknownData {
			t.Fatal("sawUnknownData = true for colonless metadata lines, want false")
		}
		if !IsEmptyCompletionPayload(payload) {
			t.Fatal("IsEmptyCompletionPayload() = false for colonless metadata lines")
		}
	})

	t.Run("colonless data field treated as empty data event", func(t *testing.T) {
		var detector StreamBootstrapDetector
		payload := []byte("data\n\ndata: [DONE]\n\n")
		if detector.Observe(payload) {
			t.Fatal("Observe() = true for colonless data event, want false")
		}
		if !detector.Finish() {
			t.Fatal("Finish() = false, want empty completion recognized for colonless data event")
		}
		if detector.state.acc.sawUnknownData {
			t.Fatal("sawUnknownData = true for colonless data, want false")
		}
		if !IsEmptyCompletionPayload(payload) {
			t.Fatal("IsEmptyCompletionPayload() = false for colonless data event payload")
		}
	})

	t.Run("couldBeSSEPrefix recognizes colonless prefixes", func(t *testing.T) {
		for _, prefix := range []string{"data", "event", "id", "retry"} {
			if !couldBeSSEPrefix([]byte(prefix)) {
				t.Fatalf("couldBeSSEPrefix(%q) = false, want true", prefix)
			}
		}
	})
}

func TestStreamBootstrapDetectorMeaningfulOutput(t *testing.T) {
	t.Run("openai role-only delta is not meaningful output", func(t *testing.T) {
		var detector StreamBootstrapDetector
		if detector.Observe([]byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n")) {
			t.Fatal("Observe() = true for role-only delta")
		}
		if detector.HasMeaningfulOutput() {
			t.Fatal("HasMeaningfulOutput() = true for role-only delta")
		}
	})

	t.Run("claude message_start is not meaningful output", func(t *testing.T) {
		var detector StreamBootstrapDetector
		if detector.Observe([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"role\":\"assistant\"}}\n\n")) {
			t.Fatal("Observe() = true for message_start")
		}
		if detector.HasMeaningfulOutput() {
			t.Fatal("HasMeaningfulOutput() = true for message_start")
		}
	})

	t.Run("responses response.created is not meaningful output", func(t *testing.T) {
		var detector StreamBootstrapDetector
		if detector.Observe([]byte(`{"type":"response.created","response":{"id":"r1"}}`)) {
			t.Fatal("Observe() = true for response.created")
		}
		if detector.HasMeaningfulOutput() {
			t.Fatal("HasMeaningfulOutput() = true for response.created")
		}
	})

	t.Run("sse ping comment is not meaningful output", func(t *testing.T) {
		var detector StreamBootstrapDetector
		if detector.Observe([]byte(": ping\n\n")) {
			t.Fatal("Observe() = true for SSE ping comment")
		}
		if detector.HasMeaningfulOutput() {
			t.Fatal("HasMeaningfulOutput() = true for SSE ping comment")
		}
	})

	t.Run("openai content delta is meaningful output", func(t *testing.T) {
		var detector StreamBootstrapDetector
		if !detector.Observe([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")) {
			t.Fatal("Observe() = false for content delta")
		}
		if !detector.HasMeaningfulOutput() {
			t.Fatal("HasMeaningfulOutput() = false for content delta")
		}
	})

	t.Run("openai tool call is meaningful output", func(t *testing.T) {
		var detector StreamBootstrapDetector
		if !detector.Observe([]byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"search\"}}]}}]}\n\n")) {
			t.Fatal("Observe() = false for tool_calls")
		}
		if !detector.HasMeaningfulOutput() {
			t.Fatal("HasMeaningfulOutput() = false for tool_calls")
		}
	})

	t.Run("content filter block is meaningful output", func(t *testing.T) {
		var detector StreamBootstrapDetector
		if !detector.Observe([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"content_filter\"}]}\n\n")) {
			t.Fatal("Observe() = false for content_filter finish_reason")
		}
		if !detector.HasMeaningfulOutput() {
			t.Fatal("HasMeaningfulOutput() = false for content_filter finish_reason")
		}
	})
}

func TestReadStreamBootstrapErrorHandling(t *testing.T) {
	errUpstream := errors.New("upstream failed")

	t.Run("error following openai role delta propagates as failover error", func(t *testing.T) {
		ch := make(chan cliproxyexecutor.StreamChunk, 2)
		ch <- cliproxyexecutor.StreamChunk{Payload: []byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n")}
		ch <- cliproxyexecutor.StreamChunk{Err: errUpstream}
		close(ch)

		buffered, closed, err := readStreamBootstrap(context.Background(), ch)
		if err == nil {
			t.Fatal("readStreamBootstrap error = nil, want errUpstream propagated for failover")
		}
		if !errors.Is(err, errUpstream) {
			t.Fatalf("readStreamBootstrap error = %v, want %v", err, errUpstream)
		}
		if len(buffered) != 0 {
			t.Fatalf("len(buffered) = %d, want 0 when error propagates", len(buffered))
		}
		if closed {
			t.Fatal("closed = true, want false")
		}
	})

	t.Run("error following claude message_start propagates as failover error", func(t *testing.T) {
		ch := make(chan cliproxyexecutor.StreamChunk, 2)
		ch <- cliproxyexecutor.StreamChunk{Payload: []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"role\":\"assistant\"}}\n\n")}
		ch <- cliproxyexecutor.StreamChunk{Err: errUpstream}
		close(ch)

		buffered, _, err := readStreamBootstrap(context.Background(), ch)
		if err == nil {
			t.Fatal("readStreamBootstrap error = nil, want errUpstream propagated")
		}
		if !errors.Is(err, errUpstream) {
			t.Fatalf("readStreamBootstrap error = %v, want %v", err, errUpstream)
		}
		if len(buffered) != 0 {
			t.Fatalf("len(buffered) = %d, want 0", len(buffered))
		}
	})

	t.Run("error following zero payload chunk propagates as failover error", func(t *testing.T) {
		ch := make(chan cliproxyexecutor.StreamChunk, 2)
		ch <- cliproxyexecutor.StreamChunk{Payload: nil}
		ch <- cliproxyexecutor.StreamChunk{Err: errUpstream}
		close(ch)

		buffered, _, err := readStreamBootstrap(context.Background(), ch)
		if err == nil {
			t.Fatal("readStreamBootstrap error = nil, want errUpstream propagated")
		}
		if !errors.Is(err, errUpstream) {
			t.Fatalf("readStreamBootstrap error = %v, want %v", err, errUpstream)
		}
		if len(buffered) != 0 {
			t.Fatalf("len(buffered) = %d, want 0", len(buffered))
		}
	})

	t.Run("error following responses created event propagates as failover error", func(t *testing.T) {
		ch := make(chan cliproxyexecutor.StreamChunk, 2)
		ch <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"type":"response.created","response":{"id":"r1"}}`)}
		ch <- cliproxyexecutor.StreamChunk{Err: errUpstream}
		close(ch)

		buffered, _, err := readStreamBootstrap(context.Background(), ch)
		if err == nil {
			t.Fatal("readStreamBootstrap error = nil, want errUpstream propagated")
		}
		if !errors.Is(err, errUpstream) {
			t.Fatalf("readStreamBootstrap error = %v, want %v", err, errUpstream)
		}
		if len(buffered) != 0 {
			t.Fatalf("len(buffered) = %d, want 0", len(buffered))
		}
	})

	t.Run("meaningful content starts stream immediately", func(t *testing.T) {
		ch := make(chan cliproxyexecutor.StreamChunk, 2)
		ch <- cliproxyexecutor.StreamChunk{Payload: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")}
		ch <- cliproxyexecutor.StreamChunk{Payload: []byte("data: [DONE]\n\n")}
		close(ch)

		buffered, closed, err := readStreamBootstrap(context.Background(), ch)
		if err != nil {
			t.Fatalf("readStreamBootstrap error = %v, want nil", err)
		}
		if len(buffered) != 1 {
			t.Fatalf("len(buffered) = %d, want 1", len(buffered))
		}
		if closed {
			t.Fatal("closed = true, want false (started stream)")
		}
	})
}

func TestExecuteLegacyOpenAICompletionNotRotated(t *testing.T) {
	executor := &emptyCompletionTestExecutor{
		executePayloads: map[string][]byte{},
		executeCalls:    map[string]int{},
	}
	manager, ids, model, _ := newEmptyCompletionTestManager(t, executor)

	legacyPayload := []byte(`{"choices":[{"text":"hello legacy completion","finish_reason":"stop"}]}`)
	executor.executePayloads[ids[0]] = legacyPayload
	executor.executePayloads[ids[1]] = legacyPayload

	resp, err := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(string(resp.Payload), "hello legacy completion") {
		t.Fatalf("resp payload = %q, want legacy completion text", string(resp.Payload))
	}
	if auth, ok := manager.GetByID(ids[0]); ok && auth != nil {
		if auth.Unavailable || !auth.NextRetryAfter.IsZero() {
			t.Fatalf("auth %q was cooled despite returning legacy completion text", ids[0])
		}
	}
}

func TestStreamBootstrapDetectorLegacyOpenAI(t *testing.T) {
	d := &StreamBootstrapDetector{}
	if got := d.Observe([]byte("data: {\"choices\":[{\"text\":\"hello\",\"finish_reason\":null}]}\n\n")); got != true {
		t.Fatalf("Observe(legacy choices.text chunk) = %v, want true (forwarded immediately)", got)
	}
	if !d.state.forward {
		t.Fatal("state.forward = false, want true")
	}
	if d.state.isEmptyCompletion() {
		t.Fatal("isEmptyCompletion = true, want false")
	}
}

func TestClaudeSignatureDeltaEmptyCompletion(t *testing.T) {
	t.Run("thinking content_block_start followed by signature_delta with signature is not empty", func(t *testing.T) {
		payload := []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-3\",\"usage\":{\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"sig_encrypted_carrier_payload\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":0}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		if IsEmptyCompletionPayload(payload) {
			t.Fatal("IsEmptyCompletionPayload() = true for thinking stream with non-empty signature_delta, want false")
		}
	})

	t.Run("thinking content_block_start followed by empty signature_delta is empty completion", func(t *testing.T) {
		payload := []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-3\",\"usage\":{\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":0}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		if !IsEmptyCompletionPayload(payload) {
			t.Fatal("IsEmptyCompletionPayload() = false for thinking stream with empty signature_delta, want true")
		}
	})
}

func TestOpenAIResponsesFunctionCallArgumentsEmptyCompletion(t *testing.T) {
	t.Run("empty function_call_arguments delta without prior call item does not set tool calls", func(t *testing.T) {
		var detector StreamBootstrapDetector
		chunk := []byte("event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"\"}\n\n")
		if detector.Observe(chunk) {
			t.Fatal("Observe() = true for empty function_call_arguments.delta, want false")
		}
		if detector.HasMeaningfulOutput() {
			t.Fatal("HasMeaningfulOutput() = true for empty function_call_arguments.delta, want false")
		}
	})

	t.Run("non-empty function_call_arguments delta sets tool calls", func(t *testing.T) {
		var detector StreamBootstrapDetector
		chunk := []byte("event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"{\\\"q\\\":\\\"search\\\"}\"}\n\n")
		if !detector.Observe(chunk) {
			t.Fatal("Observe() = false for meaningful function_call_arguments.delta, want true")
		}
		if !detector.HasMeaningfulOutput() {
			t.Fatal("HasMeaningfulOutput() = false for meaningful function_call_arguments.delta, want true")
		}
	})

	t.Run("empty function_call_arguments delta with prior established call item retains tool calls", func(t *testing.T) {
		var detector StreamBootstrapDetector
		itemChunk := []byte("event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"name\":\"search\",\"arguments\":\"\"}}\n\n")
		if !detector.Observe(itemChunk) {
			t.Fatal("Observe() = false for output_item.added function_call, want true")
		}
		deltaChunk := []byte("event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"\"}\n\n")
		if !detector.Observe(deltaChunk) {
			t.Fatal("Observe() = false for stream with prior function_call item, want true")
		}
		if !detector.HasMeaningfulOutput() {
			t.Fatal("HasMeaningfulOutput() = false for stream with prior function_call item, want true")
		}
	})

	t.Run("semantically empty function_call_arguments delta ({}) without prior call item does not set tool calls", func(t *testing.T) {
		var detector StreamBootstrapDetector
		chunk := []byte("event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"{}\"}\n\n")
		if detector.Observe(chunk) {
			t.Fatal("Observe() = true for semantically empty function_call_arguments.delta, want false")
		}
		if detector.HasMeaningfulOutput() {
			t.Fatal("HasMeaningfulOutput() = true for semantically empty function_call_arguments.delta, want false")
		}
	})

	t.Run("semantically empty function_call_arguments done ({}) without prior call item does not set tool calls", func(t *testing.T) {
		var detector StreamBootstrapDetector
		chunk := []byte("event: response.function_call_arguments.done\ndata: {\"type\":\"response.function_call_arguments.done\",\"arguments\":\"{}\"}\n\n")
		if detector.Observe(chunk) {
			t.Fatal("Observe() = true for semantically empty function_call_arguments.done, want false")
		}
		if detector.HasMeaningfulOutput() {
			t.Fatal("HasMeaningfulOutput() = true for semantically empty function_call_arguments.done, want false")
		}
	})

	t.Run("semantically empty function_call_arguments done ([]) without prior call item does not set tool calls", func(t *testing.T) {
		var detector StreamBootstrapDetector
		chunk := []byte("event: response.function_call_arguments.done\ndata: {\"type\":\"response.function_call_arguments.done\",\"arguments\":\"[]\"}\n\n")
		if detector.Observe(chunk) {
			t.Fatal("Observe() = true for semantically empty function_call_arguments.done, want false")
		}
		if detector.HasMeaningfulOutput() {
			t.Fatal("HasMeaningfulOutput() = true for semantically empty function_call_arguments.done, want false")
		}
	})

	t.Run("semantically empty function_call_arguments done (null) without prior call item does not set tool calls", func(t *testing.T) {
		var detector StreamBootstrapDetector
		chunk := []byte("event: response.function_call_arguments.done\ndata: {\"type\":\"response.function_call_arguments.done\",\"arguments\":\"null\"}\n\n")
		if detector.Observe(chunk) {
			t.Fatal("Observe() = true for semantically empty function_call_arguments.done, want false")
		}
		if detector.HasMeaningfulOutput() {
			t.Fatal("HasMeaningfulOutput() = true for semantically empty function_call_arguments.done, want false")
		}
	})

	t.Run("meaningful function_call_arguments done with real args sets tool calls", func(t *testing.T) {
		var detector StreamBootstrapDetector
		chunk := []byte("event: response.function_call_arguments.done\ndata: {\"type\":\"response.function_call_arguments.done\",\"arguments\":\"{\\\"location\\\":\\\"Paris\\\"}\"}\n\n")
		if !detector.Observe(chunk) {
			t.Fatal("Observe() = false for meaningful function_call_arguments.done, want true")
		}
		if !detector.HasMeaningfulOutput() {
			t.Fatal("HasMeaningfulOutput() = false for meaningful function_call_arguments.done, want true")
		}
	})
}

func TestClaudeStreamBootstrapShortCircuitsOnMessageStop(t *testing.T) {
	t.Run("empty claude stream message_stop marks terminal empty without waiting for channel close", func(t *testing.T) {
		var detector StreamBootstrapDetector
		startChunk := []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-3-5-sonnet\",\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n")
		stopChunk := []byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")

		if detector.Observe(startChunk) {
			t.Fatal("Observe(message_start) = true, want false")
		}
		if detector.Observe(stopChunk) {
			t.Fatal("Observe(message_stop) = true, want false")
		}
		if !detector.IsTerminalEmpty() {
			t.Fatal("IsTerminalEmpty() = false on message_stop, want true (sawDone equivalent)")
		}
	})
}

func TestMultiValueJSONMixedUnknownEmptyCompletion(t *testing.T) {
	t.Run("multi-value json with recognized empty and unknown object is not empty completion", func(t *testing.T) {
		payload := []byte(`{"choices":[{"message":{"content":""},"finish_reason":"stop"}]}{"custom_provider_event":{"data":"foo"}}`)
		if IsEmptyCompletionPayload(payload) {
			t.Fatal("IsEmptyCompletionPayload() = true for mixed recognized-empty and unknown JSON values, want false")
		}
	})

	t.Run("multi-value json with only recognized empty completions is empty completion", func(t *testing.T) {
		payload := []byte(`{"choices":[{"message":{"content":""},"finish_reason":"stop"}]}{"choices":[{"message":{"content":""},"finish_reason":"stop"}]}`)
		if !IsEmptyCompletionPayload(payload) {
			t.Fatal("IsEmptyCompletionPayload() = false for multiple recognized empty completions, want true")
		}
	})
}

func TestGeminiThoughtSignatureEmptyCompletion(t *testing.T) {
	t.Run("gemini STOP with thoughtSignature and omitted candidatesTokenCount is not empty", func(t *testing.T) {
		payload := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"thoughtSignature":"sig_gemini_thought_123"}]},"finishReason":"STOP"}]}`)
		if IsEmptyCompletionPayload(payload) {
			t.Fatal("IsEmptyCompletionPayload() = true for non-empty thoughtSignature with omitted token count, want false")
		}
	})

	t.Run("gemini STOP with thought_signature and omitted candidatesTokenCount is not empty", func(t *testing.T) {
		payload := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"thought_signature":"sig_gemini_thought_123"}]},"finishReason":"STOP"}]}`)
		if IsEmptyCompletionPayload(payload) {
			t.Fatal("IsEmptyCompletionPayload() = true for non-empty thought_signature with omitted token count, want false")
		}
	})

	t.Run("gemini STOP with thoughtSignature and zero candidatesTokenCount is not empty", func(t *testing.T) {
		payload := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"","thoughtSignature":"sig_gemini_thought_123"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":0}}`)
		if IsEmptyCompletionPayload(payload) {
			t.Fatal("IsEmptyCompletionPayload() = true for non-empty thoughtSignature with zero token count, want false")
		}
	})

	t.Run("gemini STOP with thought_signature and zero candidatesTokenCount is not empty", func(t *testing.T) {
		payload := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"","thought_signature":"sig_gemini_thought_123"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":0}}`)
		if IsEmptyCompletionPayload(payload) {
			t.Fatal("IsEmptyCompletionPayload() = true for non-empty thought_signature with zero token count, want false")
		}
	})

	t.Run("gemini STOP with empty thoughtSignature is empty", func(t *testing.T) {
		payload := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"","thoughtSignature":""}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":0}}`)
		if !IsEmptyCompletionPayload(payload) {
			t.Fatal("IsEmptyCompletionPayload() = false for empty thoughtSignature, want true")
		}
	})

	t.Run("gemini STOP with empty thought_signature is empty", func(t *testing.T) {
		payload := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"","thought_signature":""}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":0}}`)
		if !IsEmptyCompletionPayload(payload) {
			t.Fatal("IsEmptyCompletionPayload() = false for empty thought_signature, want true")
		}
	})
}

func TestReadStreamBootstrapForwardsPositiveUsageTerminalFrameImmediately(t *testing.T) {
	t.Run("positive completion tokens forwards immediately without stream close", func(t *testing.T) {
		ch := make(chan cliproxyexecutor.StreamChunk, 2)
		ch <- cliproxyexecutor.StreamChunk{
			Payload: []byte("data: {\"choices\":[],\"usage\":{\"completion_tokens\":1}}\n\n"),
		}
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()

		buffered, closed, err := readStreamBootstrap(ctx, ch)
		if err != nil {
			t.Fatalf("readStreamBootstrap error = %v, want immediate forward", err)
		}
		if closed {
			t.Fatalf("readStreamBootstrap returned closed = true, want false (channel still open)")
		}
		if len(buffered) != 1 {
			t.Fatalf("buffered chunks count = %d, want 1", len(buffered))
		}
	})

	t.Run("zero completion tokens is withheld and not forwarded while stream open", func(t *testing.T) {
		ch := make(chan cliproxyexecutor.StreamChunk, 2)
		ch <- cliproxyexecutor.StreamChunk{
			Payload: []byte("data: {\"choices\":[],\"usage\":{\"completion_tokens\":0}}\n\n"),
		}
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		_, _, err := readStreamBootstrap(ctx, ch)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("readStreamBootstrap error = %v, want context.DeadlineExceeded (withheld)", err)
		}
	})
}

func TestResponsesReasoningOutputItemBootstrap(t *testing.T) {
	emptyReasoning := []byte("data: {\"type\":\"response.output_item.added\",\"sequence_number\":0,\"output_index\":0,\"item\":{\"id\":\"rs_1\",\"type\":\"reasoning\",\"status\":\"in_progress\",\"encrypted_content\":\"\",\"summary\":[]}}\n\n")
	encryptedReasoning := []byte("data: {\"type\":\"response.output_item.added\",\"sequence_number\":0,\"output_index\":0,\"item\":{\"id\":\"rs_1\",\"type\":\"reasoning\",\"status\":\"in_progress\",\"encrypted_content\":\"gAAAA_signature_123\",\"summary\":[]}}\n\n")
	summaryReasoning := []byte("data: {\"type\":\"response.output_item.added\",\"sequence_number\":0,\"output_index\":0,\"item\":{\"id\":\"rs_1\",\"type\":\"reasoning\",\"status\":\"in_progress\",\"encrypted_content\":\"\",\"summary\":[{\"type\":\"summary_text\",\"text\":\"reasoning step\"}]}}\n\n")

	t.Run("empty reasoning item does not mark meaningful and allows bootstrap error failover", func(t *testing.T) {
		var detector StreamBootstrapDetector
		if detector.Observe(emptyReasoning) {
			t.Fatal("detector.Observe() = true for empty reasoning scaffolding, want false")
		}
		if detector.HasMeaningfulOutput() {
			t.Fatal("detector.HasMeaningfulOutput() = true for empty reasoning scaffolding, want false")
		}

		errUpstream := errors.New("upstream failed immediately after scaffolding")
		ch := make(chan cliproxyexecutor.StreamChunk, 2)
		ch <- cliproxyexecutor.StreamChunk{Payload: emptyReasoning}
		ch <- cliproxyexecutor.StreamChunk{Err: errUpstream}
		close(ch)

		buffered, closed, err := readStreamBootstrap(context.Background(), ch)
		if !errors.Is(err, errUpstream) {
			t.Fatalf("readStreamBootstrap error = %v, want %v for failover", err, errUpstream)
		}
		if len(buffered) != 0 {
			t.Fatalf("readStreamBootstrap buffered = %d, want 0 on failover error", len(buffered))
		}
		if closed {
			t.Fatal("readStreamBootstrap returned closed = true, want false on error")
		}
	})

	t.Run("reasoning item with encrypted_content marks meaningful and forwards", func(t *testing.T) {
		var detector StreamBootstrapDetector
		if !detector.Observe(encryptedReasoning) {
			t.Fatal("detector.Observe() = false for reasoning with encrypted_content, want true")
		}
		if !detector.HasMeaningfulOutput() {
			t.Fatal("detector.HasMeaningfulOutput() = false for reasoning with encrypted_content, want true")
		}
	})

	t.Run("reasoning item with summary marks meaningful and forwards", func(t *testing.T) {
		var detector StreamBootstrapDetector
		if !detector.Observe(summaryReasoning) {
			t.Fatal("detector.Observe() = false for reasoning with summary, want true")
		}
		if !detector.HasMeaningfulOutput() {
			t.Fatal("detector.HasMeaningfulOutput() = false for reasoning with summary, want true")
		}
	})
}

func TestClaudeInputJSONDeltaSemanticallyEmpty(t *testing.T) {
	emptyObjectDelta := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{ }"}}`)
	emptyArrayDelta := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"[]"}}`)
	nullSpaceDelta := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"null "}}`)
	validCompleteDelta := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"test\"}"}}`)
	validIncompleteDelta := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":"}}`)

	t.Run("whitespace empty object does not mark meaningful and allows bootstrap error failover", func(t *testing.T) {
		var detector StreamBootstrapDetector
		if detector.Observe(emptyObjectDelta) {
			t.Fatal("detector.Observe() = true for empty object partial_json, want false")
		}
		if detector.HasMeaningfulOutput() {
			t.Fatal("detector.HasMeaningfulOutput() = true for empty object partial_json, want false")
		}
		errUpstream := errors.New("upstream failed after empty arg delta")
		ch := make(chan cliproxyexecutor.StreamChunk, 2)
		ch <- cliproxyexecutor.StreamChunk{Payload: emptyObjectDelta}
		ch <- cliproxyexecutor.StreamChunk{Err: errUpstream}
		close(ch)
		buffered, closed, err := readStreamBootstrap(context.Background(), ch)
		if !errors.Is(err, errUpstream) {
			t.Fatalf("readStreamBootstrap error = %v, want %v for failover", err, errUpstream)
		}
		if len(buffered) != 0 {
			t.Fatalf("readStreamBootstrap buffered = %d, want 0 on failover error", len(buffered))
		}
		if closed {
			t.Fatal("readStreamBootstrap returned closed = true, want false on error")
		}
	})

	t.Run("empty array does not mark meaningful and allows bootstrap error failover", func(t *testing.T) {
		var detector StreamBootstrapDetector
		if detector.Observe(emptyArrayDelta) {
			t.Fatal("detector.Observe() = true for empty array partial_json, want false")
		}
		if detector.HasMeaningfulOutput() {
			t.Fatal("detector.HasMeaningfulOutput() = true for empty array partial_json, want false")
		}
	})

	t.Run("null with space does not mark meaningful and allows bootstrap error failover", func(t *testing.T) {
		var detector StreamBootstrapDetector
		if detector.Observe(nullSpaceDelta) {
			t.Fatal("detector.Observe() = true for null space partial_json, want false")
		}
		if detector.HasMeaningfulOutput() {
			t.Fatal("detector.HasMeaningfulOutput() = true for null space partial_json, want false")
		}
	})

	t.Run("valid complete partial_json marks meaningful and forwards", func(t *testing.T) {
		var detector StreamBootstrapDetector
		if !detector.Observe(validCompleteDelta) {
			t.Fatal("detector.Observe() = false for valid complete partial_json, want true")
		}
		if !detector.HasMeaningfulOutput() {
			t.Fatal("detector.HasMeaningfulOutput() = false for valid complete partial_json, want true")
		}
	})

	t.Run("valid incomplete partial_json marks meaningful and forwards", func(t *testing.T) {
		var detector StreamBootstrapDetector
		if !detector.Observe(validIncompleteDelta) {
			t.Fatal("detector.Observe() = false for valid incomplete partial_json, want true")
		}
		if !detector.HasMeaningfulOutput() {
			t.Fatal("detector.HasMeaningfulOutput() = false for valid incomplete partial_json, want true")
		}
	})
}

func TestExecuteStream_TerminalDoneWithoutClosingChannelRotatesAuth(t *testing.T) {
	executor := &emptyCompletionTestExecutor{
		streamPayloads:  map[string][][]byte{},
		streamCalls:     map[string]int{},
		leaveStreamOpen: true,
		emptyStreamPayload: [][]byte{
			[]byte(": keep-alive\n\n"),
			[]byte("data: [DONE]\n\n"),
		},
	}
	manager, ids, model, capture := newEmptyCompletionTestManager(t, executor)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	stream, err := manager.ExecuteStream(ctx, []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
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

func TestExecuteStream_MeaningfulContentWithOpenChannelForwardsImmediately(t *testing.T) {
	chunks := make(chan cliproxyexecutor.StreamChunk, 2)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"meaningful_content\"}}]}\n\n")}
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("data: [DONE]\n\n")}
	// Leave channel open

	customExec := &customStreamOpenChannelExecutor{chunks: chunks}

	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(3, 5*time.Second, 3)
	model := "open-channel-meaningful-" + uuid.NewString()

	auth := &Auth{ID: "auth-1", Provider: "claude", Status: StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register error: %v", err)
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	manager.RefreshSchedulerEntry(auth.ID)
	manager.RegisterExecutor(customExec)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	stream, err := manager.ExecuteStream(ctx, []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	if stream == nil {
		t.Fatal("ExecuteStream() returned nil stream")
	}

	firstChunk := <-stream.Chunks
	if !strings.Contains(string(firstChunk.Payload), "meaningful_content") {
		t.Fatalf("first chunk payload = %q, want meaningful_content", string(firstChunk.Payload))
	}
}

type customStreamOpenChannelExecutor struct {
	chunks chan cliproxyexecutor.StreamChunk
}

func (e *customStreamOpenChannelExecutor) Identifier() string { return "claude" }

func (e *customStreamOpenChannelExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (e *customStreamOpenChannelExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (e *customStreamOpenChannelExecutor) Refresh(_ context.Context, a *Auth) (*Auth, error) {
	return a, nil
}

func (e *customStreamOpenChannelExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *customStreamOpenChannelExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return &cliproxyexecutor.StreamResult{Chunks: e.chunks}, nil
}

func TestResponsesEmptyToolCallScaffold(t *testing.T) {
	emptyFuncScaffold := []byte("event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"sequence_number\":0,\"output_index\":0,\"item\":{\"id\":\"\",\"type\":\"function_call\",\"status\":\"in_progress\",\"arguments\":\"\",\"call_id\":\"\",\"name\":\"\"}}\n\n")
	emptyCustomToolScaffold := []byte("event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"sequence_number\":0,\"output_index\":0,\"item\":{\"id\":\"\",\"type\":\"custom_tool_call\",\"status\":\"in_progress\",\"input\":\"\",\"call_id\":\"\",\"name\":\"\"}}\n\n")
	funcWithID := []byte("event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"sequence_number\":0,\"output_index\":0,\"item\":{\"id\":\"fc_123\",\"type\":\"function_call\",\"status\":\"in_progress\",\"arguments\":\"\",\"call_id\":\"\",\"name\":\"\"}}\n\n")
	funcWithCallID := []byte("event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"sequence_number\":0,\"output_index\":0,\"item\":{\"id\":\"\",\"type\":\"function_call\",\"status\":\"in_progress\",\"arguments\":\"\",\"call_id\":\"call_123\",\"name\":\"\"}}\n\n")
	funcWithName := []byte("event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"sequence_number\":0,\"output_index\":0,\"item\":{\"id\":\"\",\"type\":\"function_call\",\"status\":\"in_progress\",\"arguments\":\"\",\"call_id\":\"\",\"name\":\"lookup\"}}\n\n")
	funcWithArgs := []byte("event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"sequence_number\":0,\"output_index\":0,\"item\":{\"id\":\"\",\"type\":\"function_call\",\"status\":\"in_progress\",\"arguments\":\"{\\\"q\\\":\\\"search\\\"}\",\"call_id\":\"\",\"name\":\"\"}}\n\n")
	customToolWithInput := []byte("event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"sequence_number\":0,\"output_index\":0,\"item\":{\"id\":\"\",\"type\":\"custom_tool_call\",\"status\":\"in_progress\",\"input\":\"{\\\"cmd\\\":\\\"run\\\"}\",\"call_id\":\"\",\"name\":\"\"}}\n\n")

	t.Run("empty function_call scaffold does not mark meaningful and allows bootstrap error failover", func(t *testing.T) {
		var detector StreamBootstrapDetector
		if detector.Observe(emptyFuncScaffold) {
			t.Fatal("detector.Observe() = true for empty function_call scaffold, want false")
		}
		if detector.HasMeaningfulOutput() {
			t.Fatal("detector.HasMeaningfulOutput() = true for empty function_call scaffold, want false")
		}

		errUpstream := errors.New("upstream failed immediately after function_call scaffold")
		ch := make(chan cliproxyexecutor.StreamChunk, 2)
		ch <- cliproxyexecutor.StreamChunk{Payload: emptyFuncScaffold}
		ch <- cliproxyexecutor.StreamChunk{Err: errUpstream}
		close(ch)
		buffered, closed, err := readStreamBootstrap(context.Background(), ch)
		if !errors.Is(err, errUpstream) {
			t.Fatalf("readStreamBootstrap error = %v, want %v for failover", err, errUpstream)
		}
		if len(buffered) != 0 {
			t.Fatalf("readStreamBootstrap buffered = %d, want 0 on failover error", len(buffered))
		}
		if closed {
			t.Fatal("readStreamBootstrap returned closed = true, want false on error")
		}
	})

	t.Run("empty custom_tool_call scaffold does not mark meaningful and allows bootstrap error failover", func(t *testing.T) {
		var detector StreamBootstrapDetector
		if detector.Observe(emptyCustomToolScaffold) {
			t.Fatal("detector.Observe() = true for empty custom_tool_call scaffold, want false")
		}
		if detector.HasMeaningfulOutput() {
			t.Fatal("detector.HasMeaningfulOutput() = true for empty custom_tool_call scaffold, want false")
		}
	})

	t.Run("scaffold with non-empty id marks meaningful and forwards", func(t *testing.T) {
		var detector StreamBootstrapDetector
		if !detector.Observe(funcWithID) {
			t.Fatal("detector.Observe() = false for function_call with id, want true")
		}
		if !detector.HasMeaningfulOutput() {
			t.Fatal("detector.HasMeaningfulOutput() = false for function_call with id, want true")
		}
	})

	t.Run("scaffold with non-empty call_id marks meaningful and forwards", func(t *testing.T) {
		var detector StreamBootstrapDetector
		if !detector.Observe(funcWithCallID) {
			t.Fatal("detector.Observe() = false for function_call with call_id, want true")
		}
		if !detector.HasMeaningfulOutput() {
			t.Fatal("detector.HasMeaningfulOutput() = false for function_call with call_id, want true")
		}
	})

	t.Run("scaffold with non-empty name marks meaningful and forwards", func(t *testing.T) {
		var detector StreamBootstrapDetector
		if !detector.Observe(funcWithName) {
			t.Fatal("detector.Observe() = false for function_call with name, want true")
		}
		if !detector.HasMeaningfulOutput() {
			t.Fatal("detector.HasMeaningfulOutput() = false for function_call with name, want true")
		}
	})

	t.Run("scaffold with non-empty arguments marks meaningful and forwards", func(t *testing.T) {
		var detector StreamBootstrapDetector
		if !detector.Observe(funcWithArgs) {
			t.Fatal("detector.Observe() = false for function_call with arguments, want true")
		}
		if !detector.HasMeaningfulOutput() {
			t.Fatal("detector.HasMeaningfulOutput() = false for function_call with arguments, want true")
		}
	})

	t.Run("custom tool with non-empty input marks meaningful and forwards", func(t *testing.T) {
		var detector StreamBootstrapDetector
		if !detector.Observe(customToolWithInput) {
			t.Fatal("detector.Observe() = false for custom_tool_call with input, want true")
		}
		if !detector.HasMeaningfulOutput() {
			t.Fatal("detector.HasMeaningfulOutput() = false for custom_tool_call with input, want true")
		}
	})

	t.Run("output_item.done with empty function_call does not mark meaningful and allows bootstrap error failover", func(t *testing.T) {
		emptyDoneFuncItems := [][]byte{
			[]byte("event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"function_call\"}}\n\n"),
			[]byte("event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"\",\"type\":\"function_call\",\"status\":\"completed\",\"arguments\":\"\",\"call_id\":\"\",\"name\":\"\"}}\n\n"),
			[]byte("event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"output\":{\"type\":\"function_call\"}}\n\n"),
			[]byte("event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"custom_tool_call\"}}\n\n"),
		}
		for i, payload := range emptyDoneFuncItems {
			var detector StreamBootstrapDetector
			if detector.Observe(payload) {
				t.Fatalf("case %d: detector.Observe() = true for empty output_item.done, want false", i)
			}
			if detector.HasMeaningfulOutput() {
				t.Fatalf("case %d: detector.HasMeaningfulOutput() = true for empty output_item.done, want false", i)
			}
		}

		errUpstream := errors.New("upstream failed immediately after output_item.done empty scaffold")
		ch := make(chan cliproxyexecutor.StreamChunk, 2)
		ch <- cliproxyexecutor.StreamChunk{Payload: emptyDoneFuncItems[0]}
		ch <- cliproxyexecutor.StreamChunk{Err: errUpstream}
		close(ch)
		buffered, closed, err := readStreamBootstrap(context.Background(), ch)
		if !errors.Is(err, errUpstream) {
			t.Fatalf("readStreamBootstrap error = %v, want %v for failover", err, errUpstream)
		}
		if len(buffered) != 0 {
			t.Fatalf("readStreamBootstrap buffered = %d, want 0 on failover error", len(buffered))
		}
		if closed {
			t.Fatal("readStreamBootstrap returned closed = true, want false on error")
		}
	})

	t.Run("output_item.done with valid function_call marks meaningful and forwards", func(t *testing.T) {
		validDoneFunc := []byte("event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"fc_123\",\"type\":\"function_call\",\"status\":\"completed\",\"arguments\":\"{\\\"query\\\":\\\"go\\\"}\",\"call_id\":\"call_123\",\"name\":\"search\"}}\n\n")
		var detector StreamBootstrapDetector
		if !detector.Observe(validDoneFunc) {
			t.Fatal("detector.Observe() = false for valid output_item.done function_call, want true")
		}
		if !detector.HasMeaningfulOutput() {
			t.Fatal("detector.HasMeaningfulOutput() = false for valid output_item.done function_call, want true")
		}
	})
}

func TestGeminiStreamBootstrapTerminalEmptyOnSTOP(t *testing.T) {
	t.Run("empty gemini stream STOP marks terminal empty without waiting for channel close", func(t *testing.T) {
		var detector StreamBootstrapDetector
		stopChunk := []byte("data: {\"candidates\":[{\"finishReason\":\"STOP\"}]}\n\n")

		if detector.Observe(stopChunk) {
			t.Fatal("Observe(gemini STOP) = true, want false")
		}
		if !detector.IsTerminalEmpty() {
			t.Fatal("IsTerminalEmpty() = false on gemini STOP, want true")
		}
	})

	t.Run("gemini stream with content then STOP is not terminal empty", func(t *testing.T) {
		var detector StreamBootstrapDetector
		contentChunk := []byte("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hi\"}]}}]}\n\n")
		stopChunk := []byte("data: {\"candidates\":[{\"finishReason\":\"STOP\"}]}\n\n")

		if !detector.Observe(contentChunk) {
			t.Fatal("Observe(gemini content) = false, want true")
		}
		if !detector.Observe(stopChunk) {
			t.Fatal("Observe(gemini STOP after content) = false, want true")
		}
		if detector.IsTerminalEmpty() {
			t.Fatal("IsTerminalEmpty() = true for stream with content, want false")
		}
	})

	t.Run("gemini stream with blocked finishReason is forwarded and not terminal empty", func(t *testing.T) {
		var detector StreamBootstrapDetector
		safetyChunk := []byte("data: {\"candidates\":[{\"finishReason\":\"SAFETY\"}]}\n\n")

		if !detector.Observe(safetyChunk) {
			t.Fatal("Observe(gemini SAFETY) = false, want true (blocked reasons must reach client)")
		}
		if detector.IsTerminalEmpty() {
			t.Fatal("IsTerminalEmpty() = true on gemini SAFETY, want false")
		}
	})

	t.Run("conductor readStreamBootstrap with empty gemini STOP over open channel classifies empty", func(t *testing.T) {
		ch := make(chan cliproxyexecutor.StreamChunk, 2)
		ch <- cliproxyexecutor.StreamChunk{Payload: []byte("data: {\"candidates\":[{\"finishReason\":\"STOP\"}]}\n\n")}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		buffered, terminalEmpty, err := readStreamBootstrap(ctx, ch)
		if err != nil {
			t.Fatalf("readStreamBootstrap error = %v, want nil", err)
		}
		if !terminalEmpty {
			t.Fatal("readStreamBootstrap terminalEmpty = false, want true")
		}
		if len(buffered) != 1 {
			t.Fatalf("len(buffered) = %d, want 1", len(buffered))
		}
	})
}

func TestClaudeDataOnlyMessageStopTerminalEmpty(t *testing.T) {
	t.Run("empty claude data-only message_stop marks terminal empty without waiting for channel close", func(t *testing.T) {
		var detector StreamBootstrapDetector
		stopChunk := []byte("data: {\"type\":\"message_stop\"}\n\n")

		if detector.Observe(stopChunk) {
			t.Fatal("Observe(claude data-only message_stop) = true, want false")
		}
		if !detector.IsTerminalEmpty() {
			t.Fatal("IsTerminalEmpty() = false on claude data-only message_stop, want true")
		}
	})

	t.Run("claude stream with content then data-only message_stop is not terminal empty", func(t *testing.T) {
		var detector StreamBootstrapDetector
		contentChunk := []byte("data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n")
		stopChunk := []byte("data: {\"type\":\"message_stop\"}\n\n")

		if !detector.Observe(contentChunk) {
			t.Fatal("Observe(claude content) = false, want true")
		}
		if !detector.Observe(stopChunk) {
			t.Fatal("Observe(claude message_stop after content) = false, want true")
		}
		if detector.IsTerminalEmpty() {
			t.Fatal("IsTerminalEmpty() = true for stream with content, want false")
		}
	})

	t.Run("conductor readStreamBootstrap with data-only claude message_stop over open channel classifies empty", func(t *testing.T) {
		ch := make(chan cliproxyexecutor.StreamChunk, 2)
		ch <- cliproxyexecutor.StreamChunk{Payload: []byte("data: {\"type\":\"message_stop\"}\n\n")}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		buffered, terminalEmpty, err := readStreamBootstrap(ctx, ch)
		if err != nil {
			t.Fatalf("readStreamBootstrap error = %v, want nil", err)
		}
		if !terminalEmpty {
			t.Fatal("readStreamBootstrap terminalEmpty = false, want true")
		}
		if len(buffered) != 1 {
			t.Fatalf("len(buffered) = %d, want 1", len(buffered))
		}
	})
}

func TestEmptyCompletionImages(t *testing.T) {
	cases := []struct {
		name     string
		payload  []byte
		expected bool
	}{
		{
			name:     "delta images with image_url is not empty",
			payload:  []byte(`data: {"id":"1","choices":[{"index":0,"delta":{"images":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AQID"}}]},"finish_reason":"stop"}]}` + "\n\n" + `data: [DONE]` + "\n\n"),
			expected: false,
		},
		{
			name:     "message images non-stream is not empty",
			payload:  []byte(`{"id":"1","choices":[{"index":0,"message":{"role":"assistant","content":"","images":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AQID"}}]},"finish_reason":"stop"}],"usage":{"completion_tokens":0}}`),
			expected: false,
		},
		{
			name:     "delta images empty array stays empty",
			payload:  []byte(`data: {"id":"1","choices":[{"index":0,"delta":{"images":[]},"finish_reason":"stop"}]}` + "\n\n" + `data: [DONE]` + "\n\n"),
			expected: true,
		},
		{
			name:     "delta images null stays empty",
			payload:  []byte(`data: {"id":"1","choices":[{"index":0,"delta":{"images":null},"finish_reason":"stop"}]}` + "\n\n" + `data: [DONE]` + "\n\n"),
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

func TestEmptyCompletionClaudeCitations(t *testing.T) {
	cases := []struct {
		name     string
		payload  []byte
		expected bool
	}{
		{
			name:     "citations_delta with non-empty citation object is not empty",
			payload:  []byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"citations_delta\",\"citation\":{\"type\":\"char_location\",\"cited_text\":\"some cited text\",\"document_index\":0}}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"),
			expected: false,
		},
		{
			name:     "citations_delta with empty citation object is empty",
			payload:  []byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"citations_delta\",\"citation\":{}}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"),
			expected: true,
		},
		{
			name:     "citations_delta with null citation is empty",
			payload:  []byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"citations_delta\",\"citation\":null}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"),
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

func TestEmptyCompletionResponsesImageGenerationCallResult(t *testing.T) {
	meaningfulPayload := []byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"image_generation_call\",\"status\":\"completed\",\"result\":\"image-data\"}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[],\"usage\":{\"output_tokens\":0}}}\n\ndata: [DONE]\n\n")
	if got := isEmptyCompletionPayload(meaningfulPayload); got != false {
		t.Fatalf("isEmptyCompletionPayload(meaningful image_generation_call result) = %v, want false", got)
	}

	detector := &StreamBootstrapDetector{}
	if got := detector.Observe([]byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"image_generation_call\",\"status\":\"completed\",\"result\":\"image-data\"}}\n\n")); got != true {
		t.Fatalf("StreamBootstrapDetector.Observe(meaningful result) = %v, want true", got)
	}

	emptyDetector := &StreamBootstrapDetector{}
	if got := emptyDetector.Observe([]byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"image_generation_call\",\"status\":\"completed\",\"result\":\"\"}}\n\n")); got != false {
		t.Fatalf("StreamBootstrapDetector.Observe(empty result) = %v, want false", got)
	}

	wsDetector := &StreamBootstrapDetector{}
	if got := wsDetector.Observe([]byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"image_generation_call\",\"status\":\"completed\",\"result\":\"   \"}}\n\n")); got != false {
		t.Fatalf("StreamBootstrapDetector.Observe(whitespace result) = %v, want false", got)
	}
}
