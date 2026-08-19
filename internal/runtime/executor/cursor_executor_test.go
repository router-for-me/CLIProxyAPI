package executor

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

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

func TestCursorExecuteToolResultUsesColdContinuation(t *testing.T) {
	clientID := normalizeToolCallID("call non-stream")
	processCount := 0
	e := newCursorExecutorHarness(func(_ context.Context, _ cursorStream, _ map[string][]byte, _ anyMCPTools, onText func(string, bool), onMcpExec func(pendingMcpExec), toolResultCh <-chan []toolResultInfo, _ *cursorTokenUsage, _ func([]byte)) error {
		if onMcpExec == nil {
			return errors.New("OpenAI non-stream request did not install an MCP callback")
		}
		if toolResultCh != nil {
			return errors.New("OpenAI non-stream request parked an H2 tool session")
		}
		processCount++
		if processCount == 1 {
			onMcpExec(pendingMcpExec{ToolCallId: clientID, ToolName: "read", Args: `{"path":"README.md"}`})
			return nil
		}
		onText("after tool", false)
		return nil
	})

	first, err := e.Execute(context.Background(), cursorTestAuth(), cursorTestRequest(false), cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("first Execute() error = %v", err)
	}
	if got := gjson.GetBytes(first.Payload, "choices.0.finish_reason").String(); got != "tool_calls" {
		t.Fatalf("first finish_reason = %q, want tool_calls", got)
	}
	if got := gjson.GetBytes(first.Payload, "choices.0.message.tool_calls.0.id").String(); got != clientID {
		t.Fatalf("tool call ID = %q, want %q", got, clientID)
	}
	if got := gjson.GetBytes(first.Payload, "choices.0.message.tool_calls.0.function.name").String(); got != "read" {
		t.Fatalf("tool name = %q, want read", got)
	}
	if got := gjson.GetBytes(first.Payload, "choices.0.message.tool_calls.0.function.arguments").String(); got != `{"path":"README.md"}` {
		t.Fatalf("tool arguments = %q", got)
	}

	secondPayload := []byte(`{"model":"cursor-test-model","messages":[{"role":"user","content":"hello"},{"role":"assistant","tool_calls":[{"id":"` + clientID + `","type":"function","function":{"name":"read","arguments":"{\"path\":\"README.md\"}"}}]},{"role":"tool","tool_call_id":"` + clientID + `","content":"file contents"}]}`)
	second, err := e.Execute(context.Background(), cursorTestAuth(), cliproxyexecutor.Request{Model: "cursor-test-model", Payload: secondPayload}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("second Execute() error = %v", err)
	}
	if got := gjson.GetBytes(second.Payload, "choices.0.finish_reason").String(); got != "stop" {
		t.Fatalf("second finish_reason = %q, want stop", got)
	}
	if got := gjson.GetBytes(second.Payload, "choices.0.message.content").String(); got != "after tool" {
		t.Fatalf("second content = %q, want after tool", got)
	}
	if processCount != 2 {
		t.Fatalf("processor calls = %d, want 2", processCount)
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

func TestFlattenConversationIntoUserTextPreservesToolResult(t *testing.T) {
	parsed := parseOpenAIRequest([]byte(`{
		"messages": [
			{"role":"user","content":"Read the project file."},
			{"role":"assistant","content":"I will read it.","tool_calls":[{"id":"call_read","type":"function","function":{"name":"read","arguments":"{\"path\":\"README.md\"}"}}]},
			{"role":"tool","tool_call_id":"call_read","content":"project contents"}
		]
	}`))

	flattenConversationIntoUserText(parsed)
	for _, want := range []string{
		"USER: Read the project file.",
		"ASSISTANT: I will read it.",
		`ASSISTANT_TOOL_CALL: {"arguments":"{\"path\":\"README.md\"}","id":"call_read","name":"read"}`,
		`TOOL_RESULT: {"content":"project contents","tool_call_id":"call_read"}`,
		"Continue your response based on this context.",
	} {
		if !strings.Contains(parsed.UserText, want) {
			t.Fatalf("flattened transcript is missing %q: %q", want, parsed.UserText)
		}
	}
	if len(parsed.Turns) != 0 || len(parsed.ToolResults) != 0 {
		t.Fatal("flattenConversationIntoUserText() retained structured history")
	}
}

func TestFlattenConversationIntoUserTextPreservesToolCallOrderAndCompleteUTF8Result(t *testing.T) {
	largeResult := strings.Repeat("🙂", 3000)
	payload := []byte(`{"messages":[` +
		`{"role":"user","content":"run both"},` +
		`{"role":"assistant","tool_calls":[` +
		`{"id":"call_first","type":"function","function":{"name":"first","arguments":"{\"n\":1}"}},` +
		`{"id":"call_second","type":"function","function":{"name":"second","arguments":"{\"n\":2}"}}]},` +
		`{"role":"tool","tool_call_id":"call_first","content":` + jsonString(largeResult) + `},` +
		`{"role":"tool","tool_call_id":"call_second","content":"done"}` +
		`]}`)
	parsed := parseOpenAIRequest(payload)

	flattenConversationIntoUserText(parsed)

	first := strings.Index(parsed.UserText, `"id":"call_first"`)
	second := strings.Index(parsed.UserText, `"id":"call_second"`)
	firstResult := strings.Index(parsed.UserText, `"tool_call_id":"call_first"`)
	secondResult := strings.Index(parsed.UserText, `"tool_call_id":"call_second"`)
	if first < 0 || second <= first || firstResult <= second || secondResult <= firstResult {
		t.Fatalf("tool call/result order was not preserved: first=%d second=%d firstResult=%d secondResult=%d", first, second, firstResult, secondResult)
	}
	if !strings.Contains(parsed.UserText, largeResult) || !strings.Contains(parsed.UserText, `"name":"first"`) || !strings.Contains(parsed.UserText, `"arguments":"{\"n\":1}"`) {
		t.Fatal("flattened transcript lost tool metadata or complete UTF-8 result")
	}
	if !utf8.ValidString(parsed.UserText) || strings.Contains(parsed.UserText, "[truncated]") {
		t.Fatal("flattened transcript truncated or corrupted UTF-8")
	}
}

func TestCursorExecuteColdContinuationRetiresAllConversationState(t *testing.T) {
	payload := []byte(`{"model":"cursor-test-model","messages":[{"role":"user","content":"hello"},{"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"read","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call-1","content":"result"}]}`)
	parsed := parseOpenAIRequest(payload)
	conversationID := deriveConversationId("", "", parsed.SystemPrompt)
	firstStream := newFakeCursorStream()
	secondStream := newFakeCursorStream()
	canceled := 0
	e := newCursorExecutorHarness(func(_ context.Context, _ cursorStream, _ map[string][]byte, _ anyMCPTools, onText func(string, bool), _ func(pendingMcpExec), _ <-chan []toolResultInfo, _ *cursorTokenUsage, _ func([]byte)) error {
		onText("continued", false)
		return nil
	})
	e.sessions["old-auth:"+conversationID] = &cursorSession{stream: firstStream, cancel: func() { canceled++ }}
	e.sessions["cursor-test:"+conversationID] = &cursorSession{stream: secondStream, cancel: func() { canceled++ }}
	e.checkpoints[conversationID] = &savedCheckpoint{data: []byte("stale")}

	if _, err := e.Execute(context.Background(), cursorTestAuth(), cliproxyexecutor.Request{Model: "cursor-test-model", Payload: payload}, cliproxyexecutor.Options{}); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	sessions := len(e.sessions)
	_, checkpointExists := e.checkpoints[conversationID]
	e.mu.Unlock()
	if sessions != 0 || checkpointExists || canceled != 2 {
		t.Fatalf("retired state: sessions=%d checkpoint=%v canceled=%d", sessions, checkpointExists, canceled)
	}
	for index, stream := range []*fakeCursorStream{firstStream, secondStream} {
		select {
		case <-stream.dead:
		default:
			t.Fatalf("retired stream %d remained open", index)
		}
	}
}

func TestCursorConversationOwnershipRejectsRetiredCheckpointWriter(t *testing.T) {
	e := NewCursorExecutor(nil)
	conversationID := "conversation"
	retiredOwner := e.beginConversationStream(conversationID)
	currentOwner := e.beginConversationStream(conversationID)
	if e.saveCheckpoint(conversationID, retiredOwner, &savedCheckpoint{data: []byte("stale")}) {
		t.Fatal("retired owner saved a checkpoint")
	}
	if !e.saveCheckpoint(conversationID, currentOwner, &savedCheckpoint{data: []byte("current")}) {
		t.Fatal("current owner could not save checkpoint")
	}
	e.releaseConversationStream(conversationID, retiredOwner)
	e.mu.Lock()
	ownerAfterStaleRelease := e.stateOwners[conversationID]
	e.mu.Unlock()
	if ownerAfterStaleRelease != currentOwner {
		t.Fatal("retired owner released the current owner")
	}
	e.retireConversationState(conversationID)
	if e.saveCheckpoint(conversationID, currentOwner, &savedCheckpoint{data: []byte("late")}) {
		t.Fatal("owner saved a checkpoint after transactional retirement")
	}
}

func TestCursorConversationRetirementWinsAgainstClaimedSessionRestoreAndPublication(t *testing.T) {
	e := NewCursorExecutor(nil)
	conversationID := "conversation"
	sessionKey := "cursor-test:" + conversationID
	stream := newFakeCursorStream()
	canceled := make(chan struct{})
	owner := e.beginConversationStream(conversationID)
	if !e.attachConversationStream(conversationID, owner, func() { close(canceled) }, stream) {
		t.Fatal("could not attach current conversation owner")
	}
	session := &cursorSession{
		stream:         stream,
		cancel:         owner.cancel,
		conversationID: conversationID,
		owner:          owner,
	}

	claimed := make(chan struct{})
	attemptRestore := make(chan struct{})
	restored := make(chan bool, 1)
	go func() {
		close(claimed)
		<-attemptRestore
		restored <- e.publishConversationSession(conversationID, sessionKey, owner, session, false)
	}()
	<-claimed
	e.retireConversationState(conversationID)
	close(attemptRestore)
	if <-restored {
		t.Fatal("claimed session was restored after retirement")
	}
	if e.publishConversationSession(conversationID, sessionKey, owner, session, true) {
		t.Fatal("retired processor published a later tool session")
	}
	if e.saveCheckpoint(conversationID, owner, &savedCheckpoint{data: []byte("late")}) {
		t.Fatal("retired processor published a later checkpoint")
	}
	select {
	case <-canceled:
	default:
		t.Fatal("claimed in-flight session was not canceled by retirement")
	}
	select {
	case <-stream.dead:
	default:
		t.Fatal("claimed in-flight stream was not closed by retirement")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.sessions) != 0 || e.stateOwners[conversationID] != nil || e.checkpoints[conversationID] != nil {
		t.Fatal("conversation state reappeared after retirement")
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

func TestCursorExecuteStreamOpenAICancellationAfterFirstChunkStopsSession(t *testing.T) {
	processorExited := make(chan struct{})
	stream := newFakeCursorStream()
	e := newCursorExecutorHarness(func(ctx context.Context, _ cursorStream, _ map[string][]byte, _ anyMCPTools, onText func(string, bool), _ func(pendingMcpExec), _ <-chan []toolResultInfo, _ *cursorTokenUsage, _ func([]byte)) error {
		onText("first", false)
		<-ctx.Done()
		close(processorExited)
		return ctx.Err()
	})
	e.openStream = func(string) (cursorStream, error) { return stream, nil }
	ctx, cancel := context.WithCancel(context.Background())
	result, err := e.ExecuteStream(ctx, cursorTestAuth(), cursorTestRequest(true), cliproxyexecutor.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-processorExited:
	case <-time.After(time.Second):
		t.Fatal("OpenAI frame processor survived client cancellation after first chunk")
	}
	collectCursorStream(t, result)
	select {
	case <-stream.dead:
	case <-time.After(time.Second):
		t.Fatal("OpenAI upstream stream remained open after client cancellation")
	}
}

func TestCursorExecuteStreamClaudeCancellationRetainsSessionLifetime(t *testing.T) {
	release := make(chan struct{})
	processorExited := make(chan struct{})
	e := newCursorExecutorHarness(func(ctx context.Context, _ cursorStream, _ map[string][]byte, _ anyMCPTools, onText func(string, bool), _ func(pendingMcpExec), _ <-chan []toolResultInfo, _ *cursorTokenUsage, _ func([]byte)) error {
		onText("first", false)
		select {
		case <-ctx.Done():
			return errors.New("native Claude session inherited client cancellation")
		case <-release:
			close(processorExited)
			return nil
		}
	})
	payload := []byte(`{"model":"cursor-test-model","max_tokens":128,"stream":true,"messages":[{"role":"user","content":"hello"}]}`)
	ctx, cancel := context.WithCancel(context.Background())
	result, err := e.ExecuteStream(ctx, cursorTestAuth(), cliproxyexecutor.Request{Model: "cursor-test-model", Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude"), OriginalRequest: payload})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-processorExited:
		t.Fatal("native Claude session stopped with the client context")
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	collectCursorStream(t, result)
	select {
	case <-processorExited:
	case <-time.After(time.Second):
		t.Fatal("native Claude processor did not finish after release")
	}
}

func TestCursorResumeCancellationRestoresSession(t *testing.T) {
	e := NewCursorExecutor(nil)
	sessionKey := "cursor-test:conversation"
	owner := e.beginConversationStream("conversation")
	session := &cursorSession{
		toolResultCh:   make(chan []toolResultInfo, 1),
		resumeOutCh:    make(chan cliproxyexecutor.StreamChunk, 1),
		cancel:         func() {},
		conversationID: "conversation",
		owner:          owner,
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
	owner := e.beginConversationStream("pending-session")
	switched := false
	session := &cursorSession{
		pending:        []pendingMcpExec{{ToolCallId: "call-good"}},
		toolResultCh:   make(chan []toolResultInfo, 1),
		resumeOutCh:    make(chan cliproxyexecutor.StreamChunk, 1),
		cancel:         func() {},
		conversationID: "pending-session",
		owner:          owner,
		switchOutput:   func(chan cliproxyexecutor.StreamChunk, context.Context) { switched = true },
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

func TestCursorOpenAIToolResultUsesColdContinuation(t *testing.T) {
	clientID := normalizeToolCallID("call immediate")
	var processMu sync.Mutex
	processCount := 0
	e := newCursorExecutorHarness(func(_ context.Context, _ cursorStream, _ map[string][]byte, _ anyMCPTools, onText func(string, bool), onMcpExec func(pendingMcpExec), toolResultCh <-chan []toolResultInfo, _ *cursorTokenUsage, _ func([]byte)) error {
		if toolResultCh != nil {
			return errors.New("OpenAI request parked an H2 tool session")
		}
		processMu.Lock()
		processCount++
		current := processCount
		processMu.Unlock()
		if current == 1 {
			onMcpExec(pendingMcpExec{ToolCallId: clientID, ToolName: "read", Args: `{}`})
			return nil
		}
		onText("after tool", false)
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

	openAI := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")}
	first, err := e.ExecuteStream(context.Background(), cursorTestAuth(), cursorTestRequest(true), openAI)
	if err != nil {
		t.Fatal(err)
	}
	firstBody := cursorStreamPayload(collectCursorStream(t, first))
	if !strings.Contains(firstBody, `"finish_reason":"tool_calls"`) || strings.Contains(firstBody, `"finish_reason":"stop"`) {
		t.Fatalf("first OpenAI tool boundary is invalid:\n%s", firstBody)
	}
	e.mu.Lock()
	parkedSessions := len(e.sessions)
	e.mu.Unlock()
	if parkedSessions != 0 {
		t.Fatalf("OpenAI tool call parked %d H2 session(s)", parkedSessions)
	}

	secondPayload := []byte(`{"model":"cursor-test-model","stream":true,"messages":[{"role":"user","content":"hello"},{"role":"assistant","tool_calls":[{"id":"` + clientID + `","type":"function","function":{"name":"read","arguments":"{}"}}]},{"role":"tool","tool_call_id":"` + clientID + `","content":"file contents"}]}`)
	second, err := e.ExecuteStream(context.Background(), cursorTestAuth(), cliproxyexecutor.Request{Model: "cursor-test-model", Payload: secondPayload}, openAI)
	if err != nil {
		t.Fatal(err)
	}
	secondBody := cursorStreamPayload(collectCursorStream(t, second))
	if !strings.Contains(secondBody, `"content":"after tool"`) || !strings.Contains(secondBody, `"finish_reason":"stop"`) {
		t.Fatalf("cold continuation did not complete normally:\n%s", secondBody)
	}

	openMu.Lock()
	gotOpenCount := openCount
	openMu.Unlock()
	processMu.Lock()
	gotProcessCount := processCount
	processMu.Unlock()
	if gotOpenCount != 2 || gotProcessCount != 2 {
		t.Fatalf("cold continuation opens=%d processor calls=%d, want 2 and 2", gotOpenCount, gotProcessCount)
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

// recordingCursorStream captures written request frames for assertions.
type recordingCursorStream struct {
	inner   *fakeCursorStream
	capture *[]byte
}

func (s *recordingCursorStream) ID() string          { return s.inner.ID() }
func (s *recordingCursorStream) Data() <-chan []byte { return s.inner.Data() }
func (s *recordingCursorStream) Done() <-chan struct{} {
	return s.inner.Done()
}
func (s *recordingCursorStream) Err() error { return s.inner.Err() }
func (s *recordingCursorStream) Close()     { s.inner.Close() }
func (s *recordingCursorStream) Write(p []byte) error {
	*s.capture = append(*s.capture, p...)
	return s.inner.Write(p)
}

// TestCursorExecuteNonStreamFlattensConversationTurns guards the fix for the
// deterministic multi-message failure: Execute never attaches a checkpoint, and
// Cursor's Run endpoint rejects structured turns on a checkpoint-less
// conversation with "Connect error internal", so the wire request must carry a
// flattened UserText transcript instead of Turns (#183).
func TestCursorExecuteNonStreamFlattensConversationTurns(t *testing.T) {
	var written []byte
	e := NewCursorExecutor(nil)
	e.openStream = func(string) (cursorStream, error) {
		return &recordingCursorStream{inner: newFakeCursorStream(), capture: &written}, nil
	}
	e.processFrames = func(_ context.Context, _ cursorStream, _ map[string][]byte, _ anyMCPTools, onText func(string, bool), _ func(pendingMcpExec), _ <-chan []toolResultInfo, _ *cursorTokenUsage, _ func([]byte)) error {
		onText("answer", false)
		return nil
	}

	payload := []byte(`{"model":"cursor-test-model","messages":[` +
		`{"role":"user","content":"hi"},` +
		`{"role":"assistant","content":"Hello."},` +
		`{"role":"user","content":"capital of France?"}]}`)
	req := cliproxyexecutor.Request{Model: "cursor-test-model", Payload: payload}

	if _, err := e.Execute(context.Background(), cursorTestAuth(), req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	_, wire, _, ok := cursorproto.ParseConnectFrame(written)
	if !ok || len(wire) == 0 {
		t.Fatalf("no connect frame captured (written=%d bytes)", len(written))
	}

	control := parseOpenAIRequest(payload)
	flattenConversationIntoUserText(control)
	if len(control.Turns) != 0 || control.UserText == "" {
		t.Fatalf("control flatten produced turns=%d userText=%q", len(control.Turns), control.UserText)
	}

	// The wire request must carry the flattened transcript inside UserText and
	// must not byte-match an encoding built from the structured turns.
	transcript := control.UserText
	if !strings.Contains(string(wire), transcript) {
		t.Fatalf("wire request does not contain the flattened transcript %q", transcript)
	}
	plain := parseOpenAIRequest(payload)
	apiKey := apiKeyFromContext(context.Background())
	convID := deriveConversationId(apiKey, extractClaudeCodeSessionId(payload), plain.SystemPrompt)
	withTurns := cursorproto.EncodeRunRequest(buildRunRequestParams(plain, convID, req.Model))
	if bytes.Equal(wire, withTurns) {
		t.Fatalf("wire matches the unflattened turns encoding; flatten fix not applied")
	}
}
