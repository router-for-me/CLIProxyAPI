package claude

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

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

func TestStreaming_LateUsageOnlyDoesNotEmitAfterMessageStop(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
		`{"id":"c1","model":"m","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
	)

	if got := countByType(events, "message_delta"); got != 1 {
		t.Fatalf("expected exactly one message_delta, got %d (events=%+v)", got, events)
	}
	if got := countByType(events, "message_stop"); got != 1 {
		t.Fatalf("expected exactly one message_stop, got %d (events=%+v)", got, events)
	}
	if len(events) == 0 || events[len(events)-1].Type != "message_stop" {
		t.Fatalf("message_stop must be the last semantic event (events=%+v)", events)
	}
}

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

	starts := toolUseStarts(events)
	if len(starts) != 1 {
		t.Fatalf("expected one tool_use content_block_start with synthetic name, got %d (events=%+v)", len(starts), events)
	}
	if name := gjson.Get(starts[0].Payload, "content_block.name").String(); name != "tool_0" {
		t.Fatalf("announced tool name = %q, want %q", name, "tool_0")
	}
	if id := gjson.Get(starts[0].Payload, "content_block.id").String(); id != "call_a" {
		t.Fatalf("announced tool id = %q, want %q", id, "call_a")
	}
	if got := countByType(events, "content_block_delta"); got != 1 {
		t.Fatalf("expected one content_block_delta for accumulated args, got %d", got)
	}
	if got := countByType(events, "content_block_stop"); got != 1 {
		t.Fatalf("expected one content_block_stop, got %d", got)
	}
	if got := lastStopReason(events); got != "tool_use" {
		t.Fatalf("stop_reason = %q, want %q", got, "tool_use")
	}
}

func TestStreamingTool_NullName(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_a","function":{"name":null,"arguments":""}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	starts := toolUseStarts(events)
	if len(starts) != 1 {
		t.Fatalf("null name with id should belated-emit synthetic tool name; got %d", len(starts))
	}
	if name := gjson.Get(starts[0].Payload, "content_block.name").String(); name != "tool_0" {
		t.Fatalf("announced tool name = %q, want %q", name, "tool_0")
	}
	if id := gjson.Get(starts[0].Payload, "content_block.id").String(); id != "call_a" {
		t.Fatalf("announced tool id = %q, want %q", id, "call_a")
	}
	if got := countByType(events, "content_block_stop"); got != 1 {
		t.Fatalf("expected one content_block_stop, got %d", got)
	}
	if got := lastStopReason(events); got != "tool_use" {
		t.Fatalf("stop_reason = %q, want %q", got, "tool_use")
	}
}

func TestStreamingTool_NonStringName(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_a","function":{"name":123,"arguments":""}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	starts := toolUseStarts(events)
	if len(starts) != 1 {
		t.Fatalf("non-string name with id should belated-emit synthetic tool name; got %d", len(starts))
	}
	if name := gjson.Get(starts[0].Payload, "content_block.name").String(); name != "tool_0" {
		t.Fatalf("announced tool name = %q, want %q", name, "tool_0")
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

func TestStreamingTool_MixedEmptyNameAndValid(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[
			{"index":0,"id":"call_empty","function":{"name":"","arguments":""}},
			{"index":1,"id":"call_real","function":{"name":"do_it","arguments":""}}
		]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[
			{"index":1,"function":{"arguments":"{}"}}
		]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)

	starts := toolUseStarts(events)
	if len(starts) != 2 {
		t.Fatalf("expected two tool_use starts (valid mid-stream + synthetic empty-name), got %d", len(starts))
	}
	// Valid name+id is emitted mid-stream first; empty-name is belated at finish.
	if name := gjson.Get(starts[0].Payload, "content_block.name").String(); name != "do_it" {
		t.Fatalf("first tool name = %q, want %q", name, "do_it")
	}
	if name := gjson.Get(starts[1].Payload, "content_block.name").String(); name != "tool_0" {
		t.Fatalf("second tool name = %q, want %q", name, "tool_0")
	}
	if got := countByType(events, "content_block_stop"); got != 2 {
		t.Fatalf("expected two content_block_stop events, got %d", got)
	}

	indices := blockIndices(events)
	if len(indices) < 2 || indices[0] != 0 || indices[1] != 1 {
		t.Fatalf("content_block_start indices must be [0,1], got %v", indices)
	}
}

func TestStreamingTool_EmptyNameWithoutSignalIsSuppressed(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"function":{"name":"","arguments":""}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	if got := len(toolUseStarts(events)); got != 0 {
		t.Fatalf("empty name without id/args must stay suppressed; got %d", got)
	}
	if got := lastStopReason(events); got == "tool_use" {
		t.Fatalf("stop_reason must not be tool_use when zero tool_use blocks were emitted; got %q", got)
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

func TestStreamingTool_StopReasonMixedEmptyNameAndValid(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[
			{"index":0,"id":"call_empty","function":{"name":"","arguments":""}},
			{"index":1,"id":"call_real","function":{"name":"do_it","arguments":"{}"}}
		]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	if got := lastStopReason(events); got != "tool_use" {
		t.Fatalf("stop_reason = %q, want %q", got, "tool_use")
	}
	if got := len(toolUseStarts(events)); got != 2 {
		t.Fatalf("expected two tool_use starts, got %d", got)
	}
}

func TestStreamingTool_EmptyNameArgsOnlyNoID(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"function":{"name":"","arguments":"{\"q\":\"x\"}"}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	starts := toolUseStarts(events)
	if len(starts) != 1 {
		t.Fatalf("expected one belated tool_use start for empty-name args-only call, got %d", len(starts))
	}
	if name := gjson.Get(starts[0].Payload, "content_block.name").String(); name != "tool_0" {
		t.Fatalf("announced tool name = %q, want %q", name, "tool_0")
	}
	id := gjson.Get(starts[0].Payload, "content_block.id").String()
	if !strings.HasPrefix(id, "toolu_") {
		t.Fatalf("synthetic id should match toolu_<nanos>_<n>, got %q", id)
	}
	if got := lastStopReason(events); got != "tool_use" {
		t.Fatalf("stop_reason = %q, want %q", got, "tool_use")
	}
}

func TestStreamingTool_OmittedFinishReasonEmitsMessageDeltaOnDone(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","function":{"name":"get_weather","arguments":"{\"loc\":\"Paris\"}"}}]},"finish_reason":null}]}`,
	)

	if got := countByType(events, "message_delta"); got != 1 {
		t.Fatalf("expected exactly one message_delta, got %d (events=%+v)", got, events)
	}
	if got := lastStopReason(events); got != "tool_use" {
		t.Fatalf("stop_reason = %q, want %q", got, "tool_use")
	}
	if got := countByType(events, "message_stop"); got != 1 {
		t.Fatalf("expected exactly one message_stop, got %d (events=%+v)", got, events)
	}
	if len(events) < 2 || events[len(events)-2].Type != "message_delta" || events[len(events)-1].Type != "message_stop" {
		t.Fatalf("expected message_delta followed by message_stop at end (events=%+v)", events)
	}
}

func TestStreamingText_OmittedFinishReasonEmitsEndTurnOnDone(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"hello world"},"finish_reason":null}]}`,
	)

	if got := countByType(events, "message_delta"); got != 1 {
		t.Fatalf("expected exactly one message_delta, got %d (events=%+v)", got, events)
	}
	if got := lastStopReason(events); got != "end_turn" {
		t.Fatalf("stop_reason = %q, want %q", got, "end_turn")
	}
	if got := countByType(events, "message_stop"); got != 1 {
		t.Fatalf("expected exactly one message_stop, got %d (events=%+v)", got, events)
	}
}

func TestStreamingTool_UsageWithoutFinishReasonEmitsMessageDelta(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","function":{"name":"get_weather","arguments":"{\"loc\":\"Paris\"}"}}]},"finish_reason":null}]}`,
		`{"id":"c1","model":"m","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
	)

	if got := countByType(events, "message_delta"); got != 1 {
		t.Fatalf("expected exactly one message_delta, got %d (events=%+v)", got, events)
	}
	if got := lastStopReason(events); got != "tool_use" {
		t.Fatalf("stop_reason = %q, want %q", got, "tool_use")
	}
	var deltaEvent *sseEvent
	for _, e := range events {
		if e.Type == "message_delta" {
			deltaEvent = &e
			break
		}
	}
	if deltaEvent == nil {
		t.Fatalf("missing message_delta event")
	}
	if input := gjson.Get(deltaEvent.Payload, "usage.input_tokens").Int(); input != 10 {
		t.Fatalf("input_tokens = %d, want 10", input)
	}
	if output := gjson.Get(deltaEvent.Payload, "usage.output_tokens").Int(); output != 5 {
		t.Fatalf("output_tokens = %d, want 5", output)
	}
	if got := countByType(events, "message_stop"); got != 1 {
		t.Fatalf("expected exactly one message_stop, got %d (events=%+v)", got, events)
	}
}

func TestStreamingTool_OmittedToolCallIndexPreservesParallelCalls(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[
			{"id":"call_weather","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}},
			{"id":"call_time","type":"function","function":{"name":"get_time","arguments":"{\"tz\":\"UTC\"}"}}
		]},"finish_reason":"tool_calls"}]}`,
	)

	starts := toolUseStarts(events)
	if len(starts) != 2 {
		t.Fatalf("expected two tool_use starts, got %d (starts=%+v)", len(starts), starts)
	}

	if id := gjson.Get(starts[0].Payload, "content_block.id").String(); id != "call_weather" {
		t.Fatalf("first tool id = %q, want %q", id, "call_weather")
	}
	if name := gjson.Get(starts[0].Payload, "content_block.name").String(); name != "get_weather" {
		t.Fatalf("first tool name = %q, want %q", name, "get_weather")
	}
	if id := gjson.Get(starts[1].Payload, "content_block.id").String(); id != "call_time" {
		t.Fatalf("second tool id = %q, want %q", id, "call_time")
	}
	if name := gjson.Get(starts[1].Payload, "content_block.name").String(); name != "get_time" {
		t.Fatalf("second tool name = %q, want %q", name, "get_time")
	}

	var deltas []sseEvent
	for _, e := range events {
		if e.Type == "content_block_delta" && gjson.Get(e.Payload, "delta.type").String() == "input_json_delta" {
			deltas = append(deltas, e)
		}
	}
	if len(deltas) != 2 {
		t.Fatalf("expected two input_json_delta events, got %d (deltas=%+v)", len(deltas), deltas)
	}

	firstJSON := gjson.Get(deltas[0].Payload, "delta.partial_json").String()
	secondJSON := gjson.Get(deltas[1].Payload, "delta.partial_json").String()

	if !gjson.Valid(firstJSON) {
		t.Fatalf("first input_json_delta is not valid JSON: %q", firstJSON)
	}
	if !gjson.Valid(secondJSON) {
		t.Fatalf("second input_json_delta is not valid JSON: %q", secondJSON)
	}

	if gotCity := gjson.Get(firstJSON, "city").String(); gotCity != "Paris" {
		t.Fatalf("first tool args city = %q, want %q", gotCity, "Paris")
	}
	if gotTz := gjson.Get(secondJSON, "tz").String(); gotTz != "UTC" {
		t.Fatalf("second tool args tz = %q, want %q", gotTz, "UTC")
	}

	if got := countByType(events, "content_block_stop"); got != 2 {
		t.Fatalf("expected two content_block_stop events, got %d", got)
	}
	if got := lastStopReason(events); got != "tool_use" {
		t.Fatalf("stop_reason = %q, want %q", got, "tool_use")
	}
}
