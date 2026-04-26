package claude

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// sseEvent is a decoded "event: <type>\ndata: <payload>" pair.
type sseEvent struct {
	Type    string
	Payload string
}

// runStream feeds the given upstream chunks (without the "data: " prefix)
// followed by a [DONE] marker through ConvertOpenAIResponseToClaude and
// returns every SSE event emitted to the client. originalReq must declare
// "stream":true to take the streaming code path.
func runStream(t *testing.T, originalReq string, chunks ...string) []sseEvent {
	t.Helper()
	var paramAny any
	var emitted [][]byte
	for _, chunk := range chunks {
		emitted = append(emitted, ConvertOpenAIResponseToClaude(
			context.Background(), "",
			[]byte(originalReq), nil,
			[]byte("data: "+chunk), &paramAny,
		)...)
	}
	emitted = append(emitted, ConvertOpenAIResponseToClaude(
		context.Background(), "",
		[]byte(originalReq), nil,
		[]byte("data: [DONE]"), &paramAny,
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

// countByType returns how many emitted events match the given type.
func countByType(events []sseEvent, typ string) int {
	n := 0
	for _, e := range events {
		if e.Type == typ {
			n++
		}
	}
	return n
}

// toolUseStarts returns content_block_start events whose content_block.type is "tool_use".
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

// blockIndices returns the "index" field of every content_block_start event,
// in emission order, regardless of block type.
func blockIndices(events []sseEvent) []int64 {
	var idx []int64
	for _, e := range events {
		if e.Type == "content_block_start" {
			idx = append(idx, gjson.Get(e.Payload, "index").Int())
		}
	}
	return idx
}

// lastStopReason returns the stop_reason field from the final message_delta
// event, or "" if no message_delta was emitted.
func lastStopReason(events []sseEvent) string {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == "message_delta" {
			return gjson.Get(events[i].Payload, "delta.stop_reason").String()
		}
	}
	return ""
}

const streamReq = `{"stream":true}`

// TestStreamingTool_EmptyNameThroughout verifies that a tool_call whose
// function.name is "" in every chunk never produces a content_block_start —
// and therefore must not produce delta or stop events either. The final
// message_delta must also avoid claiming stop_reason=tool_use, since no
// tool_use block was actually announced.
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

// TestStreamingTool_NullName verifies that a JSON null name is treated the
// same as missing/empty: no start, no delta, no stop.
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

// TestStreamingTool_NonStringName verifies that a non-string name (e.g. a
// number) is rejected by the type check and treated as no-name, instead of
// silently coercing into the announced tool name.
func TestStreamingTool_NonStringName(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_a","function":{"name":123,"arguments":""}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	if got := len(toolUseStarts(events)); got != 0 {
		t.Fatalf("non-string name must not produce a tool_use start; got %d", got)
	}
}

// TestStreamingTool_RepeatedName verifies that when the upstream sends the
// same name field across multiple chunks, only one content_block_start is
// emitted and the announced name does not drift on later chunks.
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

// TestStreamingTool_MixedSuppressedAndValid verifies that when one tool
// index is suppressed (empty name) and another later index is valid, only
// the valid one emits start/delta/stop and the announced indices remain
// gap-free starting from 0.
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
		t.Fatalf("first content_block_start index must be 0 (no gap from suppressed tool), got %v", indices)
	}
}

// TestStreamingTool_EmptyIDDeferStart verifies that when name arrives but
// id is still empty, content_block_start is deferred. Once id arrives in a
// later chunk, the start emits exactly once.
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
		t.Fatalf("announced tool id = %q, want %q (must not be a fallback)", id, "call_real")
	}
}

// TestStreamingTool_IDInDeltaWithoutFunction verifies that when an upstream
// splits the tool fields across chunks — function.name in one delta, then
// id alone in a later delta whose tool_call entry has no `function` object
// — content_block_start still emits when id arrives. Without the emit
// re-check living outside the function.Exists() branch, the call would be
// silently dropped.
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

// TestStreamingTool_StopReasonWithEmittedTool verifies the positive path:
// when at least one tool_use block is actually announced, the final
// stop_reason still maps to "tool_use" as Anthropic clients expect.
func TestStreamingTool_StopReasonWithEmittedTool(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_a","function":{"name":"do_it","arguments":"{}"}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"},"usage":{"prompt_tokens":1,"completion_tokens":1}}]}`,
	)
	if got := lastStopReason(events); got != "tool_use" {
		t.Fatalf("stop_reason = %q, want %q", got, "tool_use")
	}
}

// TestStreamingTool_StopReasonMixedSuppressedAndValid verifies that the
// stop_reason is still "tool_use" even when some tool indexes were
// suppressed, as long as at least one valid tool block was emitted.
func TestStreamingTool_StopReasonMixedSuppressedAndValid(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[
			{"index":0,"id":"call_skip","function":{"name":"","arguments":""}},
			{"index":1,"id":"call_real","function":{"name":"do_it","arguments":"{}"}}
		]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	if got := lastStopReason(events); got != "tool_use" {
		t.Fatalf("stop_reason = %q, want %q (one valid tool was emitted)", got, "tool_use")
	}
}
