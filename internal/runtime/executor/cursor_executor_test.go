package executor

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	cursorproto "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor/proto"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

type fakeCursorStream struct {
	data chan []byte
	done chan struct{}
	mu   sync.Mutex
	err  error
}

func newFakeCursorStream() *fakeCursorStream {
	return &fakeCursorStream{data: make(chan []byte), done: make(chan struct{})}
}
func (s *fakeCursorStream) ID() string            { return "test-stream" }
func (s *fakeCursorStream) Write([]byte) error    { return nil }
func (s *fakeCursorStream) Data() <-chan []byte   { return s.data }
func (s *fakeCursorStream) Done() <-chan struct{} { return s.done }
func (s *fakeCursorStream) Err() error            { s.mu.Lock(); defer s.mu.Unlock(); return s.err }
func (s *fakeCursorStream) Close()                {}

func newCursorExecutorHarness(process cursorFrameProcessor) *CursorExecutor {
	e := NewCursorExecutor(nil)
	e.openStream = func(string) (cursorStream, error) { return newFakeCursorStream(), nil }
	e.processFrames = process
	return e
}

func cursorTestAuth() *cliproxyauth.Auth {
	return &cliproxyauth.Auth{ID: "cursor-test", Metadata: map[string]any{"access_token": "test-token"}}
}

func cursorTestRequest(stream bool) cliproxyexecutor.Request {
	payload := `{"model":"cursor-test-model","messages":[{"role":"user","content":"hello"}]}`
	if stream {
		payload = `{"model":"cursor-test-model","stream":true,"messages":[{"role":"user","content":"hello"}]}`
	}
	return cliproxyexecutor.Request{Model: "cursor-test-model", Payload: []byte(payload)}
}

func TestCursorExecuteReturnsReasoningAndUsage(t *testing.T) {
	e := newCursorExecutorHarness(func(_ context.Context, _ cursorStream, _ map[string][]byte, _ anyMCPTools, onText func(string, bool), _ func(pendingMcpExec), _ <-chan []toolResultInfo, usage *cursorTokenUsage, _ func([]byte)) error {
		onText("plan", true)
		onText("answer", false)
		usage.addOutput(7)
		return nil
	})

	resp, err := e.Execute(context.Background(), cursorTestAuth(), cursorTestRequest(false), cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := gjson.GetBytes(resp.Payload, "choices.0.message.reasoning_content").String(); got != "plan" {
		t.Fatalf("reasoning_content = %q", got)
	}
	if got := gjson.GetBytes(resp.Payload, "choices.0.message.content").String(); got != "answer" {
		t.Fatalf("content = %q", got)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.completion_tokens").Int(); got != 7 {
		t.Fatalf("completion_tokens = %d", got)
	}
}

func TestCursorExecuteReasoningOnlyErrorIsFailure(t *testing.T) {
	boom := errors.New("upstream reset")
	e := newCursorExecutorHarness(func(_ context.Context, _ cursorStream, _ map[string][]byte, _ anyMCPTools, onText func(string, bool), _ func(pendingMcpExec), _ <-chan []toolResultInfo, _ *cursorTokenUsage, _ func([]byte)) error {
		onText("partial thought", true)
		return boom
	})
	if _, err := e.Execute(context.Background(), cursorTestAuth(), cursorTestRequest(false), cliproxyexecutor.Options{}); !errors.Is(err, boom) {
		t.Fatalf("Execute() error = %v, want wrapped %v", err, boom)
	}
}

func TestCursorExecuteStreamPostChunkErrorIsTerminalError(t *testing.T) {
	boom := errors.New("upstream reset")
	e := newCursorExecutorHarness(func(_ context.Context, _ cursorStream, _ map[string][]byte, _ anyMCPTools, onText func(string, bool), _ func(pendingMcpExec), _ <-chan []toolResultInfo, _ *cursorTokenUsage, _ func([]byte)) error {
		onText("partial", false)
		return boom
	})
	result, err := e.ExecuteStream(context.Background(), cursorTestAuth(), cursorTestRequest(true), cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	var sawPayload, sawError, sawStop bool
	for chunk := range result.Chunks {
		if len(chunk.Payload) > 0 {
			sawPayload = true
			if gjson.GetBytes(chunk.Payload, "choices.0.finish_reason").String() == "stop" {
				sawStop = true
			}
		}
		if chunk.Err != nil {
			sawError = true
		}
	}
	if !sawPayload || !sawError || sawStop {
		t.Fatalf("payload=%v error=%v stop=%v", sawPayload, sawError, sawStop)
	}
}

func TestNormalizeToolCallID(t *testing.T) {
	input := "call-cce860e6-ab07-414d-812c-785db35b17ca-4\nfc_d2335004-a95f-93b4-977b-e9eee6316be7_0"
	want := "call-cce860e6-ab07-414d-812c-785db35b17ca-4_u000a_fc_d2335004-a95f-93b4-977b-e9eee6316be7_0"
	if got := normalizeToolCallID(input); got != want {
		t.Fatalf("normalizeToolCallID() = %q, want %q", got, want)
	}
}

func TestNormalizeToolCallIDCollisionsRemainDistinct(t *testing.T) {
	ids := []string{"call-a b", "call-ab", "call-a\nb", "call-a\tb"}
	seen := map[string]string{}
	for _, id := range ids {
		normalized := normalizeToolCallID(id)
		if prior, ok := seen[normalized]; ok && prior != id {
			t.Fatalf("IDs %q and %q collide as %q", prior, id, normalized)
		}
		seen[normalized] = id
	}
}

func TestParseOpenAIToolCallIDRoundTrip(t *testing.T) {
	id := "call-a b\n"
	parsed := parseOpenAIRequest([]byte(`{"model":"m","messages":[{"role":"tool","tool_call_id":"call-a b\n","content":"ok"}]}`))
	if len(parsed.ToolResults) != 1 {
		t.Fatalf("tool results = %d", len(parsed.ToolResults))
	}
	if got, want := parsed.ToolResults[0].ToolCallId, normalizeToolCallID(id); got != want {
		t.Fatalf("tool_call_id = %q, want %q", got, want)
	}
}

func TestCursorStreamCoalescerBatchesAdjacentDeltas(t *testing.T) {
	type emittedDelta struct {
		text       string
		isThinking bool
	}
	emitted := make(chan emittedDelta, 4)
	coalescer := newCursorStreamCoalescer(context.Background(), time.Hour, func(text string, isThinking bool) { emitted <- emittedDelta{text, isThinking} })
	coalescer.push("first", true)
	coalescer.push(" second", true)
	coalescer.push(" third", true)
	coalescer.push("answer", false)
	coalescer.close()
	close(emitted)
	var got []emittedDelta
	for delta := range emitted {
		got = append(got, delta)
	}
	want := []emittedDelta{{"first", true}, {" second third", true}, {"answer", false}}
	if len(got) != len(want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("delta %d = %#v, want %#v", i, got[i], want[i])
		}
	}
}

func TestCursorStreamCoalescerCancellationDoesNotBlock(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	blocked := make(chan struct{})
	coalescer := newCursorStreamCoalescer(ctx, time.Hour, func(string, bool) { <-blocked })
	done := make(chan struct{})
	go func() { coalescer.push("first", false); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("push blocked after cancellation")
	}
	close(blocked)
}

func TestCursorOpenAIExecutorEmitsNoDoneChunk(t *testing.T) {
	e := newCursorExecutorHarness(func(_ context.Context, _ cursorStream, _ map[string][]byte, _ anyMCPTools, onText func(string, bool), _ func(pendingMcpExec), _ <-chan []toolResultInfo, _ *cursorTokenUsage, _ func([]byte)) error {
		onText("ok", false)
		return nil
	})
	result, err := e.ExecuteStream(context.Background(), cursorTestAuth(), cursorTestRequest(true), cliproxyexecutor.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var payload strings.Builder
	for chunk := range result.Chunks {
		payload.Write(chunk.Payload)
	}
	if strings.Contains(payload.String(), "[DONE]") {
		t.Fatalf("executor emitted DONE: %s", payload.String())
	}
}

// Alias keeps test processor signatures readable without weakening production types.
type anyMCPTools = []cursorproto.McpToolDef
