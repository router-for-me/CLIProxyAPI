package executor

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	cursorproto "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor/proto"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

type fakeCursorStream struct {
	data chan []byte
	done chan struct{}
	mu   sync.Mutex
	err  error
	once sync.Once
	dead chan struct{}
}

func newFakeCursorStream() *fakeCursorStream {
	return &fakeCursorStream{data: make(chan []byte), done: make(chan struct{}), dead: make(chan struct{})}
}
func (s *fakeCursorStream) ID() string            { return "test-stream" }
func (s *fakeCursorStream) Write([]byte) error    { return nil }
func (s *fakeCursorStream) Data() <-chan []byte   { return s.data }
func (s *fakeCursorStream) Done() <-chan struct{} { return s.done }
func (s *fakeCursorStream) Err() error            { s.mu.Lock(); defer s.mu.Unlock(); return s.err }
func (s *fakeCursorStream) Close()                { s.once.Do(func() { close(s.dead) }) }

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

func collectCursorStream(t *testing.T, result *cliproxyexecutor.StreamResult) []cliproxyexecutor.StreamChunk {
	t.Helper()
	done := make(chan []cliproxyexecutor.StreamChunk, 1)
	go func() {
		var chunks []cliproxyexecutor.StreamChunk
		for chunk := range result.Chunks {
			chunks = append(chunks, chunk)
		}
		done <- chunks
	}()
	select {
	case chunks := <-done:
		return chunks
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Cursor stream to close")
		return nil
	}
}

func cursorStreamPayload(chunks []cliproxyexecutor.StreamChunk) string {
	var payload strings.Builder
	for _, chunk := range chunks {
		payload.Write(chunk.Payload)
		payload.WriteByte('\n')
	}
	return payload.String()
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
	promptTokens := gjson.GetBytes(resp.Payload, "usage.prompt_tokens").Int()
	if promptTokens < 1 {
		t.Fatalf("prompt_tokens = %d, want positive estimate", promptTokens)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.total_tokens").Int(); got != promptTokens+7 {
		t.Fatalf("total_tokens = %d, want %d", got, promptTokens+7)
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

func TestCursorExecuteStreamClaudeThinkingToolBoundaryAndResume(t *testing.T) {
	rawToolCallID := "call-a b\n\t"
	clientToolCallID := normalizeToolCallID(rawToolCallID)
	processorResult := make(chan error, 1)
	e := newCursorExecutorHarness(func(ctx context.Context, _ cursorStream, _ map[string][]byte, _ anyMCPTools, onText func(string, bool), onMcpExec func(pendingMcpExec), toolResultCh <-chan []toolResultInfo, usage *cursorTokenUsage, _ func([]byte)) error {
		onText("plan", true)
		onText("answer", false)
		onMcpExec(pendingMcpExec{
			ExecMsgId:  1,
			ExecId:     "exec-1",
			ToolCallId: clientToolCallID,
			ToolName:   "read",
			Args:       `{"path":"README.md"}`,
		})
		select {
		case results := <-toolResultCh:
			if len(results) != 1 || results[0].ToolCallId != clientToolCallID || results[0].Content != "file contents" {
				err := errors.New("resumed tool result did not preserve the emitted ID and content")
				processorResult <- err
				return err
			}
		case <-ctx.Done():
			processorResult <- ctx.Err()
			return ctx.Err()
		}
		onText("after tool", false)
		usage.addOutput(9)
		processorResult <- nil
		return nil
	})

	firstPayload := []byte(`{"model":"cursor-test-model","max_tokens":128,"stream":true,"messages":[{"role":"user","content":"hello"}]}`)
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude"), OriginalRequest: firstPayload}
	first, err := e.ExecuteStream(context.Background(), cursorTestAuth(), cliproxyexecutor.Request{Model: "cursor-test-model", Payload: firstPayload}, opts)
	if err != nil {
		t.Fatalf("first ExecuteStream() error = %v", err)
	}
	firstBody := cursorStreamPayload(collectCursorStream(t, first))
	thinkingAt := strings.Index(firstBody, `"type":"thinking"`)
	thinkingTextAt := strings.Index(firstBody, `"thinking":"plan"`)
	answerAt := strings.Index(firstBody, `"text":"answer"`)
	toolAt := strings.Index(firstBody, `"type":"tool_use"`)
	toolIDAt := strings.Index(firstBody, `"id":"`+clientToolCallID+`"`)
	toolStopAt := strings.Index(firstBody, `"stop_reason":"tool_use"`)
	if thinkingAt < 0 || thinkingTextAt < thinkingAt || answerAt < thinkingTextAt || toolAt < answerAt || toolIDAt < toolAt || toolStopAt < toolIDAt {
		t.Fatalf("Claude thinking/text/tool boundary order invalid:\n%s", firstBody)
	}
	e.mu.Lock()
	publishedSessions := len(e.sessions)
	var publishedPending []pendingMcpExec
	for _, session := range e.sessions {
		publishedPending = append(publishedPending, session.pending...)
	}
	e.mu.Unlock()
	if publishedSessions != 1 || len(publishedPending) != 1 || publishedPending[0].ToolCallId != clientToolCallID {
		t.Fatalf("tool boundary closed before resumable session publication: sessions=%d pending=%#v", publishedSessions, publishedPending)
	}

	secondPayload := []byte(`{"model":"cursor-test-model","max_tokens":128,"stream":true,"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":[{"type":"tool_use","id":"` + clientToolCallID + `","name":"read","input":{"path":"README.md"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + clientToolCallID + `","content":"file contents"}]}]}`)
	second, err := e.ExecuteStream(context.Background(), cursorTestAuth(), cliproxyexecutor.Request{Model: "cursor-test-model", Payload: secondPayload}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("claude"),
		OriginalRequest: secondPayload,
	})
	if err != nil {
		t.Fatalf("resumed ExecuteStream() error = %v", err)
	}
	secondBody := cursorStreamPayload(collectCursorStream(t, second))
	if strings.Contains(secondBody, `"type":"tool_use"`) || !strings.Contains(secondBody, `"text":"after tool"`) || !strings.Contains(secondBody, `"stop_reason":"end_turn"`) {
		t.Fatalf("resumed Claude stream has invalid boundary/order:\n%s", secondBody)
	}
	if err := <-processorResult; err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeToolCallID(t *testing.T) {
	input := "call-cce860e6-ab07-414d-812c-785db35b17ca-4\nfc_d2335004-a95f-93b4-977b-e9eee6316be7_0"
	want := "cursor_call_Y2FsbC1jY2U4NjBlNi1hYjA3LTQxNGQtODEyYy03ODVkYjM1YjE3Y2EtNApmY19kMjMzNTAwNC1hOTVmLTkzYjQtOTc3Yi1lOWVlZTYzMTZiZTdfMA"
	if got := normalizeToolCallID(input); got != want {
		t.Fatalf("normalizeToolCallID() = %q, want %q", got, want)
	}
}

func TestNormalizeToolCallIDCollisionsRemainDistinct(t *testing.T) {
	ids := []string{
		"call-a b",
		"call-a_u0020_b",
		"call-a%20b",
		"call-a\\u0020b",
		"call-ab",
		"call-a\nb",
		"call-a\tb",
		"call-a\x00b",
	}
	seen := map[string]string{}
	for _, id := range ids {
		normalized := normalizeToolCallID(id)
		if prior, ok := seen[normalized]; ok && prior != id {
			t.Fatalf("IDs %q and %q collide as %q", prior, id, normalized)
		}
		seen[normalized] = id
		encoded := strings.TrimPrefix(normalized, "cursor_call_")
		decoded, err := base64.RawURLEncoding.DecodeString(encoded)
		if !strings.HasPrefix(normalized, "cursor_call_") || err != nil || string(decoded) != id {
			t.Fatalf("normalized ID %q does not reversibly encode %q: decoded=%q err=%v", normalized, id, decoded, err)
		}
	}
}

func TestParseOpenAIToolCallIDRoundTrip(t *testing.T) {
	id := "call-a b\n"
	parsed := parseOpenAIRequest([]byte(`{"model":"m","messages":[{"role":"tool","tool_call_id":"call-a b\n","content":"ok"}]}`))
	if len(parsed.ToolResults) != 1 {
		t.Fatalf("tool results = %d", len(parsed.ToolResults))
	}
	if got, want := parsed.ToolResults[0].ToolCallId, id; got != want {
		t.Fatalf("tool_call_id = %q, want %q", got, want)
	}
}

func TestParseOpenAIToolCallIDDoesNotDoubleNormalize(t *testing.T) {
	rawID := "call-a b\n\t"
	clientID := normalizeToolCallID(rawID)
	payload := []byte(`{"model":"m","messages":[{"role":"tool","tool_call_id":` + jsonString(clientID) + `,"content":"ok"}]}`)
	parsed := parseOpenAIRequest(payload)
	if len(parsed.ToolResults) != 1 {
		t.Fatalf("tool results = %d", len(parsed.ToolResults))
	}
	if got := parsed.ToolResults[0].ToolCallId; got != clientID {
		t.Fatalf("tool_call_id = %q, want exact client ID %q", got, clientID)
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

func TestCursorStreamCoalescerFlushesOnCadence(t *testing.T) {
	type emittedDelta struct {
		text string
	}
	emitted := make(chan emittedDelta, 2)
	coalescer := newCursorStreamCoalescer(context.Background(), 5*time.Millisecond, func(text string, _ bool) {
		emitted <- emittedDelta{text: text}
	})
	coalescer.push("first", false)
	if got := (<-emitted).text; got != "first" {
		t.Fatalf("first delta = %q", got)
	}
	coalescer.push("batched", false)
	select {
	case got := <-emitted:
		if got.text != "batched" {
			t.Fatalf("cadence delta = %q", got.text)
		}
	case <-time.After(time.Second):
		t.Fatal("pending delta did not flush on cadence")
	}
	coalescer.close()
}

func TestCursorExecuteStreamCancellationBeforeFirstChunkReturns(t *testing.T) {
	processorExited := make(chan struct{})
	e := newCursorExecutorHarness(func(ctx context.Context, _ cursorStream, _ map[string][]byte, _ anyMCPTools, _ func(string, bool), _ func(pendingMcpExec), _ <-chan []toolResultInfo, _ *cursorTokenUsage, _ func([]byte)) error {
		<-ctx.Done()
		close(processorExited)
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := e.ExecuteStream(ctx, cursorTestAuth(), cursorTestRequest(true), cliproxyexecutor.Options{})
	if !errors.Is(err, context.Canceled) || result != nil {
		t.Fatalf("ExecuteStream() = %#v, %v; want nil, context.Canceled", result, err)
	}
	select {
	case <-processorExited:
	case <-time.After(time.Second):
		t.Fatal("frame processor remained detached after request cancellation")
	}
}

func TestCursorResumeCancellationRestoresSession(t *testing.T) {
	e := NewCursorExecutor(nil)
	sessionKey := "cursor-test:conversation"
	session := &cursorSession{
		toolResultCh: make(chan []toolResultInfo, 1),
		resumeOutCh:  make(chan cliproxyexecutor.StreamChunk, 1),
		cancel:       func() {},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := e.resumeWithToolResults(
		ctx,
		sessionKey,
		session,
		&parsedOpenAIRequest{ToolResults: []toolResultInfo{{ToolCallId: "call-1", Content: "ok"}}},
		sdktranslator.FromString("openai"),
		sdktranslator.FromString("openai"),
		cursorTestRequest(true),
		nil,
		nil,
		false,
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("resumeWithToolResults() error = %v, want context.Canceled", err)
	}
	e.mu.Lock()
	restored := e.sessions[sessionKey]
	e.mu.Unlock()
	if restored != session {
		t.Fatal("canceled resume did not restore owned session")
	}
	select {
	case results := <-session.toolResultCh:
		t.Fatalf("canceled resume injected tool results: %#v", results)
	default:
	}
}

func TestCursorResumeInvalidSessionIsDiscarded(t *testing.T) {
	e := NewCursorExecutor(nil)
	sessionKey := "cursor-test:invalid-session"
	stream := newFakeCursorStream()
	canceled := false
	session := &cursorSession{
		stream:      stream,
		resumeOutCh: make(chan cliproxyexecutor.StreamChunk, 1),
		cancel:      func() { canceled = true },
	}
	_, err := e.resumeWithToolResults(
		context.Background(),
		sessionKey,
		session,
		&parsedOpenAIRequest{ToolResults: []toolResultInfo{{ToolCallId: "call-1", Content: "ok"}}},
		sdktranslator.FromString("openai"),
		sdktranslator.FromString("openai"),
		cursorTestRequest(true),
		nil,
		nil,
		false,
	)
	if err == nil || !strings.Contains(err.Error(), "no toolResultCh") {
		t.Fatalf("resumeWithToolResults() error = %v", err)
	}
	e.mu.Lock()
	restored := e.sessions[sessionKey]
	e.mu.Unlock()
	if restored != nil || !canceled {
		t.Fatalf("invalid session was retained: restored=%v canceled=%v", restored != nil, canceled)
	}
	select {
	case <-stream.dead:
	default:
		t.Fatal("invalid session stream was not closed")
	}
}

func TestCursorResumeRejectsUnmatchedToolResultAndRestoresSession(t *testing.T) {
	e := NewCursorExecutor(nil)
	sessionKey := "cursor-test:pending-session"
	switched := false
	session := &cursorSession{
		pending:      []pendingMcpExec{{ToolCallId: "call-good"}},
		toolResultCh: make(chan []toolResultInfo, 1),
		resumeOutCh:  make(chan cliproxyexecutor.StreamChunk, 1),
		cancel:       func() {},
		switchOutput: func(chan cliproxyexecutor.StreamChunk, context.Context) { switched = true },
	}
	_, err := e.resumeWithToolResults(
		context.Background(),
		sessionKey,
		session,
		&parsedOpenAIRequest{ToolResults: []toolResultInfo{{ToolCallId: "call-wrong", Content: "ok"}}},
		sdktranslator.FromString("openai"),
		sdktranslator.FromString("openai"),
		cursorTestRequest(true),
		nil,
		nil,
		false,
	)
	if err == nil || !strings.Contains(err.Error(), "do not match") {
		t.Fatalf("resumeWithToolResults() error = %v", err)
	}
	e.mu.Lock()
	restored := e.sessions[sessionKey]
	e.mu.Unlock()
	if restored != session || switched {
		t.Fatalf("unmatched result lost session ownership: restored=%v switched=%v", restored == session, switched)
	}
	select {
	case results := <-session.toolResultCh:
		t.Fatalf("unmatched result was injected: %#v", results)
	default:
	}
}

func TestCursorToolBoundaryImmediateResumeUsesPublishedSession(t *testing.T) {
	clientID := normalizeToolCallID("call immediate")
	e := newCursorExecutorHarness(func(ctx context.Context, _ cursorStream, _ map[string][]byte, _ anyMCPTools, _ func(string, bool), onMcpExec func(pendingMcpExec), toolResultCh <-chan []toolResultInfo, _ *cursorTokenUsage, _ func([]byte)) error {
		onMcpExec(pendingMcpExec{ToolCallId: clientID, ToolName: "read", Args: `{}`})
		select {
		case results := <-toolResultCh:
			if len(results) != 1 || results[0].ToolCallId != clientID {
				return errors.New("immediate resume delivered wrong tool result")
			}
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	})
	var openMu sync.Mutex
	openCount := 0
	e.openStream = func(string) (cursorStream, error) {
		openMu.Lock()
		openCount++
		openMu.Unlock()
		return newFakeCursorStream(), nil
	}
	first, err := e.ExecuteStream(context.Background(), cursorTestAuth(), cursorTestRequest(true), cliproxyexecutor.Options{})
	if err != nil {
		t.Fatal(err)
	}
	secondPayload := []byte(`{"model":"cursor-test-model","stream":true,"messages":[{"role":"user","content":"hello"},{"role":"assistant","tool_calls":[{"id":"` + clientID + `","type":"function","function":{"name":"read","arguments":"{}"}}]},{"role":"tool","tool_call_id":"` + clientID + `","content":"ok"}]}`)
	type outcome struct {
		body string
		err  error
	}
	resumed := make(chan outcome, 1)
	go func() {
		for range first.Chunks {
		}
		second, errResume := e.ExecuteStream(context.Background(), cursorTestAuth(), cliproxyexecutor.Request{Model: "cursor-test-model", Payload: secondPayload}, cliproxyexecutor.Options{})
		if errResume != nil {
			resumed <- outcome{err: errResume}
			return
		}
		var chunks []cliproxyexecutor.StreamChunk
		for chunk := range second.Chunks {
			chunks = append(chunks, chunk)
		}
		resumed <- outcome{body: cursorStreamPayload(chunks)}
	}()
	select {
	case got := <-resumed:
		if got.err != nil {
			t.Fatal(got.err)
		}
		openMu.Lock()
		gotOpenCount := openCount
		openMu.Unlock()
		if gotOpenCount != 1 {
			t.Fatalf("immediate resume cold-started a second Cursor stream: opens=%d body=%s", gotOpenCount, got.body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("immediate close-triggered resume hung")
	}
}

func TestCursorExecuteStreamConcurrentCancelAndFirstEmit(t *testing.T) {
	for i := 0; i < 50; i++ {
		gate := make(chan struct{})
		e := newCursorExecutorHarness(func(_ context.Context, _ cursorStream, _ map[string][]byte, _ anyMCPTools, onText func(string, bool), _ func(pendingMcpExec), _ <-chan []toolResultInfo, _ *cursorTokenUsage, _ func([]byte)) error {
			<-gate
			onText("first", false)
			return nil
		})
		ctx, cancel := context.WithCancel(context.Background())
		type outcome struct {
			result *cliproxyexecutor.StreamResult
			err    error
		}
		finished := make(chan outcome, 1)
		go func() {
			result, err := e.ExecuteStream(ctx, cursorTestAuth(), cursorTestRequest(true), cliproxyexecutor.Options{})
			finished <- outcome{result: result, err: err}
		}()
		close(gate)
		cancel()
		select {
		case got := <-finished:
			if got.err != nil && !errors.Is(got.err, context.Canceled) {
				t.Fatalf("iteration %d: ExecuteStream() error = %v", i, got.err)
			}
			if got.result != nil {
				collectCursorStream(t, got.result)
			}
		case <-time.After(time.Second):
			t.Fatalf("iteration %d: cancel/first-emission race hung", i)
		}
	}
}

func TestCursorExecuteStreamBackpressureCancellationUnblocks(t *testing.T) {
	processorExited := make(chan struct{})
	e := newCursorExecutorHarness(func(_ context.Context, _ cursorStream, _ map[string][]byte, _ anyMCPTools, onText func(string, bool), _ func(pendingMcpExec), _ <-chan []toolResultInfo, _ *cursorTokenUsage, _ func([]byte)) error {
		defer close(processorExited)
		for i := 0; i < 256; i++ {
			onText("x", i%2 == 0)
		}
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	result, err := e.ExecuteStream(ctx, cursorTestAuth(), cursorTestRequest(true), cliproxyexecutor.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-processorExited:
	case <-time.After(time.Second):
		t.Fatal("frame processor blocked behind a full output buffer")
	}
	collectCursorStream(t, result)
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
