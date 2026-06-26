package claude

import (
<<<<<<< HEAD:pkg/llmproxy/translator/openai/claude/openai_claude_response_test.go
=======
	"bytes"
>>>>>>> upstream/main:internal/translator/openai/claude/openai_claude_response_test.go
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

<<<<<<< HEAD:pkg/llmproxy/translator/openai/claude/openai_claude_response_test.go
func TestConvertOpenAIResponseToClaude(t *testing.T) {
	ctx := context.Background()
	originalRequest := []byte(`{"stream": true}`)
	request := []byte(`{}`)

	// Test streaming chunk with content
	chunk := []byte(`data: {"id": "chatcmpl-123", "model": "gpt-4o", "created": 1677652288, "choices": [{"index": 0, "delta": {"content": "Hello"}, "finish_reason": null}]}`)
	var param any
	got := ConvertOpenAIResponseToClaude(ctx, "claude-3-sonnet", originalRequest, request, chunk, &param)

	if len(got) != 3 { // message_start + content_block_start + content_block_delta
		t.Errorf("expected 3 events, got %d", len(got))
	}

	// Test [DONE]
	doneChunk := []byte(`data: [DONE]`)
	gotDone := ConvertOpenAIResponseToClaude(ctx, "claude-3-sonnet", originalRequest, request, doneChunk, &param)
	if len(gotDone) == 0 {
		t.Errorf("expected events for [DONE], got 0")
	}
}

func TestConvertOpenAIResponseToClaude_DoneWithoutDataPrefix(t *testing.T) {
	ctx := context.Background()
	originalRequest := []byte(`{"stream": true}`)
	request := []byte(`{}`)
	var param any

	chunk := []byte(`data: {"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"hello"}}]}`)
	_ = ConvertOpenAIResponseToClaude(ctx, "claude-3-sonnet", originalRequest, request, chunk, &param)

	doneChunk := []byte(`[DONE]`)
	got := ConvertOpenAIResponseToClaude(ctx, "claude-3-sonnet", originalRequest, request, doneChunk, &param)
	if len(got) == 0 {
		t.Fatalf("expected terminal events for bare [DONE], got 0")
	}

	last := got[len(got)-1]
	if !strings.Contains(last, `"type":"message_stop"`) {
		t.Fatalf("expected final message_stop event, got %q", last)
	}
}

func TestConvertOpenAIResponseToClaude_DoneWithoutDataPrefixEmitsMessageDeltaAfterFinishReason(t *testing.T) {
	ctx := context.Background()
	originalRequest := []byte(`{"stream": true}`)
	request := []byte(`{}`)
	var param any

	chunk := []byte(`data: {"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
	gotFinish := ConvertOpenAIResponseToClaude(ctx, "claude-3-sonnet", originalRequest, request, chunk, &param)
	if len(gotFinish) == 0 {
		t.Fatalf("expected finish chunk events, got 0")
	}

	doneChunk := []byte(`[DONE]`)
	gotDone := ConvertOpenAIResponseToClaude(ctx, "claude-3-sonnet", originalRequest, request, doneChunk, &param)
	if len(gotDone) < 2 {
		t.Fatalf("expected message_delta and message_stop on bare [DONE], got %d events", len(gotDone))
	}
	if !strings.Contains(gotDone[0], `"type":"message_delta"`) {
		t.Fatalf("expected first event message_delta, got %q", gotDone[0])
	}
	if !strings.Contains(gotDone[len(gotDone)-1], `"type":"message_stop"`) {
		t.Fatalf("expected last event message_stop, got %q", gotDone[len(gotDone)-1])
	}
}

func TestConvertOpenAIResponseToClaude_StreamingReasoning(t *testing.T) {
	ctx := context.Background()
	originalRequest := []byte(`{"stream": true}`)
	request := []byte(`{}`)
	var param any

	// 1. Reasoning content chunk
	chunk1 := []byte(`data: {"id": "chatcmpl-1", "choices": [{"index": 0, "delta": {"reasoning_content": "Thinking..."}}]}`)
	got1 := ConvertOpenAIResponseToClaude(ctx, "claude-3-sonnet", originalRequest, request, chunk1, &param)
	// message_start + content_block_start(thinking) + content_block_delta(thinking)
	if len(got1) != 3 {
		t.Errorf("expected 3 events, got %d", len(got1))
	}

	// 2. Transition to content
	chunk2 := []byte(`data: {"id": "chatcmpl-1", "choices": [{"index": 0, "delta": {"content": "Hello"}}]}`)
	got2 := ConvertOpenAIResponseToClaude(ctx, "claude-3-sonnet", originalRequest, request, chunk2, &param)
	_ = got2
	// content_block_stop(thinking) + content_block_start(text) + content_block_delta(text)
	if len(got2) != 3 {
		t.Errorf("expected 3 events for transition, got %d", len(got2))
	}
}

func TestConvertOpenAIResponseToClaude_StreamingToolCalls(t *testing.T) {
	ctx := context.Background()
	originalRequest := []byte(`{"stream": true}`)
	request := []byte(`{}`)
	var param any

	// 1. Tool call chunk (start)
	chunk1 := []byte(`data: {"id": "chatcmpl-1", "choices": [{"index": 0, "delta": {"tool_calls": [{"index": 0, "id": "call_1", "function": {"name": "my_tool", "arguments": ""}}]}}]}`)
	got1 := ConvertOpenAIResponseToClaude(ctx, "claude-3-sonnet", originalRequest, request, chunk1, &param)
	// message_start + content_block_start(tool_use)
	if len(got1) != 2 {
		t.Errorf("expected 2 events, got %d", len(got1))
	}

	// 2. Tool call chunk (arguments)
	chunk2 := []byte(`data: {"id": "chatcmpl-1", "choices": [{"index": 0, "delta": {"tool_calls": [{"index": 0, "function": {"arguments": "{\"a\":1}"}}]}}]}`)
	got2 := ConvertOpenAIResponseToClaude(ctx, "claude-3-sonnet", originalRequest, request, chunk2, &param)
	_ = got2
	// No events emitted during argument accumulation usually, wait until stop or [DONE]
	// Actually, the current implementation emits nothing for arguments during accumulation.

	// 3. Finish reason tool_calls
	chunk3 := []byte(`data: {"id": "chatcmpl-1", "choices": [{"index": 0, "delta": {}, "finish_reason": "tool_calls"}]}`)
	got3 := ConvertOpenAIResponseToClaude(ctx, "claude-3-sonnet", originalRequest, request, chunk3, &param)
	// content_block_delta(input_json_delta) + content_block_stop
	if len(got3) != 2 {
		t.Errorf("expected 2 events for finish, got %d", len(got3))
	}
}

func TestConvertOpenAIResponseToClaudeNonStream(t *testing.T) {
	ctx := context.Background()
	originalRequest := []byte(`{"stream": false}`)
	request := []byte(`{}`)

	// Test non-streaming response with reasoning and content
	response := []byte(`{
		"id": "chatcmpl-123",
		"model": "gpt-4o",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": "Hello",
				"reasoning_content": "Thinking..."
			},
			"finish_reason": "stop"
		}],
		"usage": {
			"prompt_tokens": 10,
			"completion_tokens": 20
		}
	}`)

	got := ConvertOpenAIResponseToClaudeNonStream(ctx, "claude-3-sonnet", originalRequest, request, response, nil)
	res := gjson.Parse(got)

	if res.Get("id").String() != "chatcmpl-123" {
		t.Errorf("expected id chatcmpl-123, got %s", res.Get("id").String())
	}

	content := res.Get("content").Array()
	if len(content) != 2 {
		t.Errorf("expected 2 content blocks, got %d", len(content))
	}

	if content[0].Get("type").String() != "thinking" {
		t.Errorf("expected first block type thinking, got %s", content[0].Get("type").String())
	}

	if content[1].Get("type").String() != "text" {
		t.Errorf("expected second block type text, got %s", content[1].Get("type").String())
	}
}

func TestConvertOpenAIResponseToClaude_ToolCalls(t *testing.T) {
	ctx := context.Background()
	originalRequest := []byte(`{"stream": false}`)
	request := []byte(`{}`)

	response := []byte(`{
		"id": "chatcmpl-123",
		"choices": [{
			"message": {
				"role": "assistant",
				"tool_calls": [{
					"id": "call_123",
					"type": "function",
					"function": {
						"name": "my_tool",
						"arguments": "{\"arg\": 1}"
					}
				}]
			},
			"finish_reason": "tool_calls"
		}]
	}`)

	got := ConvertOpenAIResponseToClaudeNonStream(ctx, "claude-3-sonnet", originalRequest, request, response, nil)
	res := gjson.Parse(got)

	content := res.Get("content").Array()
	if len(content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(content))
	}

	if content[0].Get("type").String() != "tool_use" {
		t.Errorf("expected tool_use block, got %s", content[0].Get("type").String())
	}

	if content[0].Get("name").String() != "my_tool" {
		t.Errorf("expected tool name my_tool, got %s", content[0].Get("name").String())
=======
type sseEvent struct {
	Type    string
	Payload string
}

func runStream(t *testing.T, originalReq string, chunks ...string) []sseEvent {
	t.Helper()

	var paramAny any
	var emitted [][]byte
	for _, chunk := range chunks {
		emitted = append(emitted, ConvertOpenAIResponseToClaude(
			context.Background(),
			"",
			[]byte(originalReq),
			nil,
			[]byte("data: "+chunk),
			&paramAny,
		)...)
	}
	emitted = append(emitted, ConvertOpenAIResponseToClaude(
		context.Background(),
		"",
		[]byte(originalReq),
		nil,
		[]byte("data: [DONE]"),
		&paramAny,
	)...)

	var events []sseEvent
	for _, raw := range emitted {
		s := string(raw)
		if !strings.HasPrefix(s, "event: ") {
			continue
		}
		nl := strings.Index(s, "\n")
		if nl < 0 {
			continue
		}
		typ := strings.TrimPrefix(s[:nl], "event: ")
		rest := s[nl+1:]
		if !strings.HasPrefix(rest, "data: ") {
			continue
		}
		payload := strings.TrimRight(strings.TrimPrefix(rest, "data: "), "\n")
		events = append(events, sseEvent{Type: typ, Payload: payload})
	}
	return events
}

func countByType(events []sseEvent, typ string) int {
	n := 0
	for _, e := range events {
		if e.Type == typ {
			n++
		}
	}
	return n
}

func toolUseStarts(events []sseEvent) []sseEvent {
	var out []sseEvent
	for _, e := range events {
		if e.Type != "content_block_start" {
			continue
		}
		if gjson.Get(e.Payload, "content_block.type").String() == "tool_use" {
			out = append(out, e)
		}
	}
	return out
}

func blockIndices(events []sseEvent) []int64 {
	var idx []int64
	for _, e := range events {
		if e.Type == "content_block_start" {
			idx = append(idx, gjson.Get(e.Payload, "index").Int())
		}
	}
	return idx
}

func lastStopReason(events []sseEvent) string {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == "message_delta" {
			return gjson.Get(events[i].Payload, "delta.stop_reason").String()
		}
	}
	return ""
}

const streamReq = `{"stream":true}`

func TestConvertOpenAIResponseToClaude_StreamIgnoresNullToolNameDelta(t *testing.T) {
	originalRequest := []byte(streamReq)
	var param any

	firstChunks := ConvertOpenAIResponseToClaude(
		context.Background(),
		"test-model",
		originalRequest,
		nil,
		[]byte(`data: {"id":"chatcmpl_1","model":"test-model","created":1,"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":""}}]},"finish_reason":null}]}`),
		&param,
	)
	firstOutput := bytes.Join(firstChunks, nil)
	if !bytes.Contains(firstOutput, []byte(`"name":"read_file"`)) {
		t.Fatalf("expected first chunk to start read_file tool block, got %s", string(firstOutput))
	}

	secondChunks := ConvertOpenAIResponseToClaude(
		context.Background(),
		"test-model",
		originalRequest,
		nil,
		[]byte(`data: {"id":"chatcmpl_1","model":"test-model","created":1,"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":null,"arguments":"{\"path\":\"/tmp/a\"}"}}]},"finish_reason":null}]}`),
		&param,
	)
	secondOutput := bytes.Join(secondChunks, nil)
	if bytes.Contains(secondOutput, []byte(`content_block_start`)) {
		t.Fatalf("did not expect null tool name delta to start a new content block, got %s", string(secondOutput))
	}
	if bytes.Contains(secondOutput, []byte(`"name":""`)) {
		t.Fatalf("did not expect null tool name delta to emit an empty tool name, got %s", string(secondOutput))
	}
}

func TestStreamingTool_EmptyNameThroughout(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_a","function":{"name":"","arguments":""}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"","arguments":"{\"x\":1}"}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)

	if got := len(toolUseStarts(events)); got != 0 {
		t.Fatalf("expected zero tool_use content_block_start, got %d (events=%+v)", got, events)
	}
	if got := countByType(events, "content_block_delta"); got != 0 {
		t.Fatalf("expected zero content_block_delta when start was suppressed, got %d", got)
	}
	if got := countByType(events, "content_block_stop"); got != 0 {
		t.Fatalf("expected zero content_block_stop when start was suppressed, got %d", got)
	}
	if got := lastStopReason(events); got == "tool_use" {
		t.Fatalf("stop_reason must not be tool_use when zero tool_use blocks were emitted; got %q", got)
	}
}

func TestStreamingTool_NullName(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_a","function":{"name":null,"arguments":""}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	if got := len(toolUseStarts(events)); got != 0 {
		t.Fatalf("null name must not produce a tool_use start; got %d", got)
	}
	if got := countByType(events, "content_block_stop"); got != 0 {
		t.Fatalf("null name must not produce content_block_stop; got %d", got)
	}
}

func TestStreamingTool_NonStringName(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_a","function":{"name":123,"arguments":""}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	if got := len(toolUseStarts(events)); got != 0 {
		t.Fatalf("non-string name must not produce a tool_use start; got %d", got)
	}
}

func TestStreamingTool_RepeatedName(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_a","function":{"name":"do_it","arguments":""}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"do_it","arguments":"{\"x\""}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"do_it","arguments":":1}"}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)

	starts := toolUseStarts(events)
	if len(starts) != 1 {
		t.Fatalf("expected exactly one tool_use start, got %d", len(starts))
	}
	if name := gjson.Get(starts[0].Payload, "content_block.name").String(); name != "do_it" {
		t.Fatalf("announced tool name = %q, want %q", name, "do_it")
	}
	if got := countByType(events, "content_block_stop"); got != 1 {
		t.Fatalf("expected exactly one content_block_stop, got %d", got)
	}
}

func TestStreamingTool_MixedSuppressedAndValid(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[
			{"index":0,"id":"call_skip","function":{"name":"","arguments":""}},
			{"index":1,"id":"call_real","function":{"name":"do_it","arguments":""}}
		]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[
			{"index":1,"function":{"arguments":"{}"}}
		]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)

	starts := toolUseStarts(events)
	if len(starts) != 1 {
		t.Fatalf("expected exactly one tool_use start, got %d", len(starts))
	}
	if got := countByType(events, "content_block_stop"); got != 1 {
		t.Fatalf("expected exactly one content_block_stop, got %d", got)
	}

	indices := blockIndices(events)
	if len(indices) == 0 || indices[0] != 0 {
		t.Fatalf("first content_block_start index must be 0, got %v", indices)
	}
}

func TestStreamingTool_EmptyIDDeferStart(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"","function":{"name":"do_it","arguments":""}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_real","function":{"arguments":"{}"}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)

	starts := toolUseStarts(events)
	if len(starts) != 1 {
		t.Fatalf("expected exactly one tool_use start once id arrived, got %d", len(starts))
	}
	if id := gjson.Get(starts[0].Payload, "content_block.id").String(); id != "call_real" {
		t.Fatalf("announced tool id = %q, want %q", id, "call_real")
	}
}

func TestStreamingTool_IDInDeltaWithoutFunction(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"function":{"name":"do_it"}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_real"}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)

	starts := toolUseStarts(events)
	if len(starts) != 1 {
		t.Fatalf("expected exactly one tool_use start when id arrives in a function-less delta, got %d", len(starts))
	}
	if id := gjson.Get(starts[0].Payload, "content_block.id").String(); id != "call_real" {
		t.Fatalf("announced tool id = %q, want %q", id, "call_real")
	}
	if name := gjson.Get(starts[0].Payload, "content_block.name").String(); name != "do_it" {
		t.Fatalf("announced tool name = %q, want %q", name, "do_it")
	}
	if got := countByType(events, "content_block_stop"); got != 1 {
		t.Fatalf("expected exactly one content_block_stop, got %d", got)
	}
}

func TestStreamingTool_StopReasonWithEmittedTool(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_a","function":{"name":"do_it","arguments":"{}"}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
	)
	if got := lastStopReason(events); got != "tool_use" {
		t.Fatalf("stop_reason = %q, want %q", got, "tool_use")
	}
}

func TestStreamingTool_StopReasonWhenIDNeverArrives(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"function":{"name":"do_it","arguments":""}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)

	starts := toolUseStarts(events)
	if len(starts) != 1 {
		t.Fatalf("expected one belated tool_use start with synthetic id, got %d", len(starts))
	}
	id := gjson.Get(starts[0].Payload, "content_block.id").String()
	if !strings.HasPrefix(id, "toolu_") {
		t.Fatalf("synthetic id should match toolu_<nanos>_<n>, got %q", id)
	}
	if name := gjson.Get(starts[0].Payload, "content_block.name").String(); name != "do_it" {
		t.Fatalf("announced tool name = %q, want %q", name, "do_it")
	}
	if got := lastStopReason(events); got != "tool_use" {
		t.Fatalf("stop_reason = %q, want %q", got, "tool_use")
	}
}

func TestStreamingTool_BelatedStartsUseOpenAIToolIndexOrder(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[
			{"index":2,"function":{"name":"third_tool","arguments":"{}"}},
			{"index":0,"function":{"name":"first_tool","arguments":"{}"}},
			{"index":1,"function":{"name":"second_tool","arguments":"{}"}}
		]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)

	starts := toolUseStarts(events)
	if len(starts) != 3 {
		t.Fatalf("expected three belated tool_use starts, got %d", len(starts))
	}

	wantNames := []string{"first_tool", "second_tool", "third_tool"}
	for i, wantName := range wantNames {
		if name := gjson.Get(starts[i].Payload, "content_block.name").String(); name != wantName {
			t.Fatalf("tool_use start %d name = %q, want %q (starts=%+v)", i, name, wantName, starts)
		}
		if blockIndex := gjson.Get(starts[i].Payload, "index").Int(); blockIndex != int64(i) {
			t.Fatalf("tool_use start %d block index = %d, want %d", i, blockIndex, i)
		}
	}
}

func TestStreamingTool_LateIDAfterFinalization(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"function":{"name":"do_it"}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_late"}]}}]}`,
	)

	starts := toolUseStarts(events)
	if len(starts) != 1 {
		t.Fatalf("expected one belated tool_use start, got %d", len(starts))
	}

	var sawMessageStop bool
	for _, e := range events {
		if e.Type == "message_stop" {
			sawMessageStop = true
			continue
		}
		if sawMessageStop {
			switch e.Type {
			case "content_block_start", "content_block_delta", "content_block_stop":
				t.Fatalf("event %q emitted after message_stop (events=%+v)", e.Type, events)
			}
		}
	}
}

func TestStreamingTool_StopReasonMixedSuppressedAndValid(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[
			{"index":0,"id":"call_skip","function":{"name":"","arguments":""}},
			{"index":1,"id":"call_real","function":{"name":"do_it","arguments":"{}"}}
		]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	if got := lastStopReason(events); got != "tool_use" {
		t.Fatalf("stop_reason = %q, want %q", got, "tool_use")
>>>>>>> upstream/main:internal/translator/openai/claude/openai_claude_response_test.go
	}
}
