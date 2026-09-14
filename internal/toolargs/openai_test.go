package toolargs

import (
	"strings"
	"testing"
)

const chatRequest = `{"model":"m","tools":[{"type":"function","function":{"name":"wait","parameters":{"type":"object","properties":{"session_id":{"type":"integer"},"yield_time_ms":{"type":"integer"},"ratio":{"type":"number"}}}}}]}`
const responsesRequest = `{"model":"m","tools":[{"type":"function","name":"wait","parameters":{"type":"object","properties":{"session_id":{"type":"integer"},"yield_time_ms":{"type":"integer"}}}}]}`

func TestCoerceChatCompletionsBody(t *testing.T) {
	body := `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"wait","arguments":"{\"session_id\":1,\"yield_time_ms\":14380.0,\"ratio\":2.0}"}}]},"finish_reason":"tool_calls"}]}`
	got := string(CoerceChatCompletionsBody([]byte(body), []byte(chatRequest), "claude", "m"))
	want := `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"wait","arguments":"{\"session_id\":1,\"yield_time_ms\":14380,\"ratio\":2.0}"}}]},"finish_reason":"tool_calls"}]}`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	// Unknown tool name (schema cannot be found) → untouched.
	other := strings.Replace(body, `"name":"wait"`, `"name":"other"`, 1)
	if got := string(CoerceChatCompletionsBody([]byte(other), []byte(chatRequest), "claude", "m")); got != other {
		t.Fatalf("unknown tool must pass through: %s", got)
	}
	// No tools in request → early-out, byte-identical.
	if got := string(CoerceChatCompletionsBody([]byte(body), []byte(`{"model":"m"}`), "claude", "m")); got != body {
		t.Fatalf("no tools must pass through")
	}
}

func TestCoerceChatCompletionsChunkWholeValueOnly(t *testing.T) {
	// Whole assembled call in one delta (Claude/Gemini style): name + complete object → rewritten,
	// with the rest of the chunk (id, index, type, finish_reason position) untouched.
	whole := `{"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"wait","arguments":"{\"yield_time_ms\":14380.0}"}}]},"finish_reason":null}]}`
	got := string(CoerceChatCompletionsChunk([]byte(whole), []byte(chatRequest), "claude", "m"))
	want := `{"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"wait","arguments":"{\"yield_time_ms\":14380}"}}]},"finish_reason":null}]}`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	// A true fragment (Codex delta style: no name, partial JSON) must be passed through verbatim —
	// bytes already streamed cannot be retracted, and a fragment has no resolvable schema.
	for _, frag := range []string{
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"yield_time_ms\":143"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"80.0}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"14380.0"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"wait","arguments":"{\"yield_time_ms\":143"}}]}}]}`,
	} {
		if got := string(CoerceChatCompletionsChunk([]byte(frag), []byte(chatRequest), "codex", "m")); got != frag {
			t.Fatalf("fragment must pass through verbatim:\n in  %s\n got %s", frag, got)
		}
	}
	// SSE-framed chat chunk keeps its framing byte-for-byte.
	framed := "data: " + whole + "\n\n"
	gotFramed := string(CoerceChatCompletionsChunk([]byte(framed), []byte(chatRequest), "claude", "m"))
	if gotFramed != "data: "+want+"\n\n" {
		t.Fatalf("framing not preserved: %q", gotFramed)
	}
}

func TestCoerceResponsesBody(t *testing.T) {
	body := `{"id":"r","output":[{"type":"message","content":[]},{"type":"function_call","id":"fc_1","call_id":"call_1","name":"wait","arguments":"{\"session_id\":96230.0}","status":"completed"},{"type":"custom_tool_call","id":"ctc_1","call_id":"call_2","name":"wait","input":"{\"session_id\":96230.0}"}]}`
	got := string(CoerceResponsesBody([]byte(body), []byte(responsesRequest), "openai", "m"))
	want := `{"id":"r","output":[{"type":"message","content":[]},{"type":"function_call","id":"fc_1","call_id":"call_1","name":"wait","arguments":"{\"session_id\":96230}","status":"completed"},{"type":"custom_tool_call","id":"ctc_1","call_id":"call_2","name":"wait","input":"{\"session_id\":96230.0}"}]}`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestCoerceResponsesEventStreamSequence(t *testing.T) {
	var param any
	key := &param
	ev := func(name, payload string) []byte { return []byte("event: " + name + "\ndata: " + payload + "\n\n") }

	added := ev("response.output_item.added", `{"type":"response.output_item.added","sequence_number":3,"output_index":0,"item":{"id":"fc_1","type":"function_call","status":"in_progress","arguments":"","call_id":"call_1","name":"wait"}}`)
	if got := CoerceResponsesEvent(added, []byte(responsesRequest), key, "openai", "m"); string(got) != string(added) {
		t.Fatalf("output_item.added must be untouched")
	}
	// Deltas are never rewritten (already-flushed fragments).
	delta := ev("response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","sequence_number":4,"item_id":"fc_1","output_index":0,"delta":"{\"session_id\":14380.0}"}`)
	if got := CoerceResponsesEvent(delta, []byte(responsesRequest), key, "openai", "m"); string(got) != string(delta) {
		t.Fatalf("delta must be untouched")
	}
	// args.done carries only item_id → resolved through the name remembered from .added.
	done := ev("response.function_call_arguments.done", `{"type":"response.function_call_arguments.done","sequence_number":5,"item_id":"fc_1","output_index":0,"arguments":"{\"session_id\":14380.0}"}`)
	wantDone := ev("response.function_call_arguments.done", `{"type":"response.function_call_arguments.done","sequence_number":5,"item_id":"fc_1","output_index":0,"arguments":"{\"session_id\":14380}"}`)
	if got := CoerceResponsesEvent(done, []byte(responsesRequest), key, "openai", "m"); string(got) != string(wantDone) {
		t.Fatalf("args.done:\n got %q\nwant %q", got, wantDone)
	}
	// output_item.done is stateless (item.name present) and must agree with args.done; the
	// sequence_number / ids / call_id / status are untouched.
	itemDone := ev("response.output_item.done", `{"type":"response.output_item.done","sequence_number":6,"output_index":0,"item":{"id":"fc_1","type":"function_call","status":"completed","arguments":"{\"session_id\":14380.0}","call_id":"call_1","name":"wait"}}`)
	wantItemDone := ev("response.output_item.done", `{"type":"response.output_item.done","sequence_number":6,"output_index":0,"item":{"id":"fc_1","type":"function_call","status":"completed","arguments":"{\"session_id\":14380}","call_id":"call_1","name":"wait"}}`)
	if got := CoerceResponsesEvent(itemDone, []byte(responsesRequest), key, "openai", "m"); string(got) != string(wantItemDone) {
		t.Fatalf("output_item.done:\n got %q\nwant %q", got, wantItemDone)
	}
	// Terminal event drops the per-stream memory.
	completed := ev("response.completed", `{"type":"response.completed","sequence_number":7,"response":{"id":"r"}}`)
	if got := CoerceResponsesEvent(completed, []byte(responsesRequest), key, "openai", "m"); string(got) != string(completed) {
		t.Fatalf("completed must be untouched")
	}
	if _, tracked := responsesStreamNames.Load(key); tracked {
		t.Fatalf("stream memory must be released on response.completed")
	}
	// Codex passthrough framing: `data:` with no event line, still rewritten with framing kept.
	bare := []byte("data: " + `{"type":"response.output_item.done","sequence_number":1,"output_index":0,"item":{"id":"fc_9","type":"function_call","status":"completed","arguments":"{\"session_id\":7.0}","call_id":"call_9","name":"wait"}}` + "\n\n")
	wantBare := []byte("data: " + `{"type":"response.output_item.done","sequence_number":1,"output_index":0,"item":{"id":"fc_9","type":"function_call","status":"completed","arguments":"{\"session_id\":7}","call_id":"call_9","name":"wait"}}` + "\n\n")
	if got := CoerceResponsesEvent(bare, []byte(responsesRequest), nil, "codex", "m"); string(got) != string(wantBare) {
		t.Fatalf("bare data framing:\n got %q\nwant %q", got, wantBare)
	}
}

func TestCoerceResponsesEventArgsDoneWithoutStreamKeyLeavesArgsDone(t *testing.T) {
	// With no per-stream key the item_id → name mapping cannot exist, so args.done is left
	// alone (fail closed: no guessing) while output_item.done (stateless) is still fixed.
	done := []byte("event: response.function_call_arguments.done\ndata: " + `{"type":"response.function_call_arguments.done","item_id":"fc_1","arguments":"{\"session_id\":1.0}"}` + "\n\n")
	if got := CoerceResponsesEvent(done, []byte(responsesRequest), nil, "openai", "m"); string(got) != string(done) {
		t.Fatalf("args.done without stream memory must pass through")
	}
}

func TestStatsCountsCoercionsPerProviderModel(t *testing.T) {
	before := Stats()["claude/stat-model"]
	body := `{"choices":[{"message":{"tool_calls":[{"function":{"name":"wait","arguments":"{\"session_id\":1.0,\"yield_time_ms\":2.0}"}}]}}]}`
	CoerceChatCompletionsBody([]byte(body), []byte(chatRequest), "claude", "stat-model")
	if after := Stats()["claude/stat-model"]; after-before != 2 {
		t.Fatalf("stats delta = %d, want 2 (two coerced params)", after-before)
	}
}
