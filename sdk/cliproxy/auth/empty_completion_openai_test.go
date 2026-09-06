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

// openaiEmptyTestExecutor returns an empty OpenAI chat-completion payload from
// the first auth Execute picks and a real completion from every later auth.
type openaiEmptyTestExecutor struct {
	emptyPayload   []byte
	contentPayload []byte
	firstExecute   string
	executeCalls   map[string]int
}

func (*openaiEmptyTestExecutor) Identifier() string { return "claude" }

func (e *openaiEmptyTestExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e.executeCalls == nil {
		e.executeCalls = map[string]int{}
	}
	e.executeCalls[auth.ID]++
	if e.firstExecute == "" {
		e.firstExecute = auth.ID
	}
	if e.firstExecute == auth.ID {
		empty := e.emptyPayload
		if len(empty) == 0 {
			empty = []byte(`{"choices":[{"message":{"content":""},"finish_reason":"stop"}],"usage":{"completion_tokens":0}}`)
		}
		return cliproxyexecutor.Response{Payload: empty}, nil
	}
	content := e.contentPayload
	if len(content) == 0 {
		content = []byte(`{"choices":[{"message":{"content":"real"},"finish_reason":"stop"}]}`)
	}
	return cliproxyexecutor.Response{Payload: content}, nil
}

func (*openaiEmptyTestExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (*openaiEmptyTestExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, errors.New("stream not in this slice")
}

func (*openaiEmptyTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (*openaiEmptyTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func newOpenAIEmptyTestManager(t *testing.T, executor *openaiEmptyTestExecutor) (*Manager, []string, string, *resultCaptureHook) {
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

func assertOpenAIEmptyRotates(t *testing.T, ids []string, emptyFirst, gotPayload, wantSubstr string, capture *resultCaptureHook) {
	t.Helper()
	if emptyFirst == "" {
		t.Fatal("executor never executed any auth")
	}
	if !strings.Contains(gotPayload, wantSubstr) {
		t.Fatalf("payload = %q, want %q from the non-empty auth", gotPayload, wantSubstr)
	}
	other := ids[0]
	if emptyFirst == ids[0] {
		other = ids[1]
	}
	var emptyRecorded bool
	var emptySucceeded bool
	var otherSucceeded bool
	for _, r := range capture.Results() {
		if r.AuthID == emptyFirst && !r.Success {
			emptyRecorded = true
		}
		if r.AuthID == emptyFirst && r.Success {
			emptySucceeded = true
		}
		if r.AuthID == other && r.Success {
			otherSucceeded = true
		}
	}
	if emptySucceeded {
		t.Fatalf("empty auth %q was recorded as success; results=%v", emptyFirst, capture.Results())
	}
	if !emptyRecorded {
		t.Fatalf("empty auth %q was not recorded as a failure; results=%v", emptyFirst, capture.Results())
	}
	if !otherSucceeded {
		t.Fatalf("content auth %q was not recorded as success; results=%v", other, capture.Results())
	}
}

func TestExecuteEmptyCompletionRotatesAuth(t *testing.T) {
	executor := &openaiEmptyTestExecutor{}
	manager, ids, model, capture := newOpenAIEmptyTestManager(t, executor)

	resp, err := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	assertOpenAIEmptyRotates(t, ids, executor.firstExecute, string(resp.Payload), "real", capture)
}

func TestExecuteEmptyChoicesRotatesAuth(t *testing.T) {
	executor := &openaiEmptyTestExecutor{
		emptyPayload: []byte(`{"choices":[],"usage":{"completion_tokens":0}}`),
	}
	manager, ids, model, capture := newOpenAIEmptyTestManager(t, executor)

	resp, err := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	assertOpenAIEmptyRotates(t, ids, executor.firstExecute, string(resp.Payload), "real", capture)
}

func TestExecuteEmptyBodyRotatesAuth(t *testing.T) {
	executor := &openaiEmptyTestExecutor{emptyPayload: []byte("   ")}
	manager, ids, model, capture := newOpenAIEmptyTestManager(t, executor)

	resp, err := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	assertOpenAIEmptyRotates(t, ids, executor.firstExecute, string(resp.Payload), "real", capture)
}

func TestExecuteNonEmptyOpenAINotRotated(t *testing.T) {
	executor := &openaiEmptyTestExecutor{
		emptyPayload: []byte(`{"choices":[{"message":{"content":"hello from first"},"finish_reason":"stop"}]}`),
	}
	manager, _, model, capture := newOpenAIEmptyTestManager(t, executor)

	resp, err := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(string(resp.Payload), "hello from first") {
		t.Fatalf("payload = %q, want first-auth content (no rotation)", resp.Payload)
	}
	if executor.firstExecute == "" {
		t.Fatal("executor never executed any auth")
	}
	results := capture.Results()
	if len(results) != 1 || !results[0].Success || results[0].AuthID != executor.firstExecute {
		t.Fatalf("first auth should succeed without rotation, results=%v", results)
	}
}

func TestExecuteMeaningfulToolCallNotRotated(t *testing.T) {
	executor := &openaiEmptyTestExecutor{
		emptyPayload: []byte(`{"choices":[{"message":{"tool_calls":[{"id":"x","function":{"name":"lookup","arguments":"{\"q\":\"1\"}"}}]},"finish_reason":"tool_calls"}]}`),
	}
	manager, _, model, capture := newOpenAIEmptyTestManager(t, executor)

	resp, err := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(string(resp.Payload), "lookup") {
		t.Fatalf("payload = %q, want tool call from first auth", resp.Payload)
	}
	results := capture.Results()
	if len(results) != 1 || !results[0].Success || results[0].AuthID != executor.firstExecute {
		t.Fatalf("meaningful tool call must not rotate, results=%v", results)
	}
}

func TestIsEmptyCompletionPayloadOpenAI(t *testing.T) {
	cases := []struct {
		name     string
		payload  []byte
		expected bool
	}{
		{name: "whitespace body", payload: []byte("   "), expected: true},
		{name: "json null", payload: []byte("null"), expected: true},
		{name: "empty choices", payload: []byte(`{"choices":[]}`), expected: true},
		{name: "empty content stop", payload: []byte(`{"choices":[{"message":{"content":""},"finish_reason":"stop"}],"usage":{"completion_tokens":0}}`), expected: true},
		{name: "skeleton tool_calls id only", payload: []byte(`{"choices":[{"message":{"tool_calls":[{"id":"x"}]},"finish_reason":"tool_calls"}]}`), expected: true},
		{name: "real content", payload: []byte(`{"choices":[{"message":{"content":"hello"},"finish_reason":"stop"}]}`), expected: false},
		{name: "nonzero tokens", payload: []byte(`{"choices":[{"message":{"content":""},"finish_reason":"stop"}],"usage":{"completion_tokens":5}}`), expected: false},
		{name: "named tool call", payload: []byte(`{"choices":[{"message":{"tool_calls":[{"id":"x","function":{"name":"lookup"}}]},"finish_reason":"tool_calls"}]}`), expected: false},
		{name: "openai sse empty", payload: []byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"), expected: true},
		{name: "openai sse then content", payload: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"), expected: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isEmptyCompletionPayload(tc.payload)
			if got != tc.expected {
				t.Fatalf("isEmptyCompletionPayload(%q) = %v, want %v", tc.payload, got, tc.expected)
			}
		})
	}
}

func TestExecuteSkeletonToolCallRotatesAuth(t *testing.T) {
	executor := &openaiEmptyTestExecutor{
		emptyPayload: []byte(`{"choices":[{"message":{"tool_calls":[{"id":"x"}]},"finish_reason":"tool_calls"}]}`),
	}
	manager, ids, model, capture := newOpenAIEmptyTestManager(t, executor)

	resp, err := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	assertOpenAIEmptyRotates(t, ids, executor.firstExecute, string(resp.Payload), "real", capture)
}
