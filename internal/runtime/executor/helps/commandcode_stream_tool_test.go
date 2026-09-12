package helps

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// collectToolArguments simulates a real OpenAI-compatible client: it walks
// every SSE chunk payload, groups tool_calls deltas by index, and concatenates
// the arguments strings exactly as the client would accumulate them.
// It returns per-index accumulated arguments plus per-index names.
func collectToolArguments(t *testing.T, chunks [][]byte) (map[int]string, map[int]string) {
	t.Helper()
	argsByIndex := make(map[int]string)
	nameByIndex := make(map[int]string)
	for _, chunk := range chunks {
		if !gjson.ValidBytes(chunk) {
			t.Fatalf("chunk is not valid JSON: %s", string(chunk))
		}
		root := gjson.ParseBytes(chunk)
		root.Get("choices.0.delta.tool_calls").ForEach(func(_, tc gjson.Result) bool {
			idx := int(tc.Get("index").Int())
			if name := tc.Get("function.name").String(); name != "" {
				nameByIndex[idx] = name
			}
			if args := tc.Get("function.arguments").String(); args != "" {
				argsByIndex[idx] += args
			}
			return true
		})
	}
	return argsByIndex, nameByIndex
}

// feedLines feeds raw NDJSON lines into the stream converter and returns all
// emitted OpenAI chunks (the converter keeps state via the param pointer).
func feedLines(t *testing.T, lines ...string) [][]byte {
	t.Helper()
	var state any
	var all [][]byte
	ctx := context.Background()
	for _, line := range lines {
		chunks := ConvertCommandCodeStreamToOpenAI(ctx, "deepseek/deepseek-v4-flash", nil, nil, []byte(line), &state)
		all = append(all, chunks...)
	}
	return all
}

// Case A: the real duplicate scenario.
// tool-input-start -> deltas forming {"path":"/"} -> tool-input-end with full
// input -> tool-call with full input. The client-side concatenation must be
// exactly `{"path":"/"}` and never `{"path":"/"}{"path":"/"}`.
func TestCommandCodeStream_ToolArgumentsNotDuplicatedOnTerminalEvents(t *testing.T) {
	lines := []string{
		`{"type":"tool-input-start","id":"call_a","toolName":"bash"}`,
		`{"type":"tool-input-delta","id":"call_a","delta":"{\"path\":"}`,
		`{"type":"tool-input-delta","id":"call_a","delta":"\"/\"}"}`,
		`{"type":"tool-input-end","id":"call_a","input":{"path":"/"}}`,
		`{"type":"tool-call","id":"call_a","input":{"path":"/"}}`,
	}
	chunks := feedLines(t, lines...)
	argsByIndex, nameByIndex := collectToolArguments(t, chunks)

	got := argsByIndex[0]
	if got != `{"path":"/"}` {
		t.Fatalf("accumulated arguments = %q, want %q (duplicated JSON would be %q)",
			got, `{"path":"/"}`, `{"path":"/"}{"path":"/"}`)
	}
	if nameByIndex[0] != "bash" {
		t.Errorf("tool name = %q, want bash", nameByIndex[0])
	}
}

// Case B: complete tool-call with no prior deltas. The full arguments must be
// emitted exactly once (fallback path).
func TestCommandCodeStream_ToolCallWithoutDeltasEmitsFullArgsOnce(t *testing.T) {
	lines := []string{
		`{"type":"tool-call","id":"call_b","toolName":"read","input":{"path":"README.md","limit":20}}`,
	}
	chunks := feedLines(t, lines...)
	argsByIndex, nameByIndex := collectToolArguments(t, chunks)

	got := argsByIndex[0]
	if got != `{"path":"README.md","limit":20}` {
		t.Fatalf("accumulated arguments = %q, want full JSON once", got)
	}
	if nameByIndex[0] != "read" {
		t.Errorf("tool name = %q, want read", nameByIndex[0])
	}
}

// Case C: two interleaved parallel tool calls with deltas. Each call's
// accumulated arguments must be correct and must not bleed into each other.
func TestCommandCodeStream_ParallelToolCallsDoNotCrossTalk(t *testing.T) {
	lines := []string{
		`{"type":"tool-input-start","id":"call_1","toolName":"bash"}`,
		`{"type":"tool-input-delta","id":"call_1","delta":"{\"command\":"}`,
		`{"type":"tool-input-start","id":"call_2","toolName":"read"}`,
		`{"type":"tool-input-delta","id":"call_2","delta":"{\"path\":"}`,
		`{"type":"tool-input-delta","id":"call_1","delta":"\"ls\"}"}`,
		`{"type":"tool-input-delta","id":"call_2","delta":"\"go.mod\"}"}`,
		`{"type":"tool-input-end","id":"call_1","input":{"command":"ls"}}`,
		`{"type":"tool-input-end","id":"call_2","input":{"path":"go.mod"}}`,
		`{"type":"tool-call","id":"call_1","input":{"command":"ls"}}`,
		`{"type":"tool-call","id":"call_2","input":{"path":"go.mod"}}`,
	}
	chunks := feedLines(t, lines...)
	argsByIndex, nameByIndex := collectToolArguments(t, chunks)

	if got := argsByIndex[0]; got != `{"command":"ls"}` {
		t.Errorf("call_1 arguments = %q, want {\"command\":\"ls\"}", got)
	}
	if got := argsByIndex[1]; got != `{"path":"go.mod"}` {
		t.Errorf("call_2 arguments = %q, want {\"path\":\"go.mod\"}", got)
	}
	if nameByIndex[0] != "bash" {
		t.Errorf("call_1 name = %q, want bash", nameByIndex[0])
	}
	if nameByIndex[1] != "read" {
		t.Errorf("call_2 name = %q, want read", nameByIndex[1])
	}
	// Guard against accidental order swap: index 0 must be bash/call_1.
	if strings.Contains(argsByIndex[0], "go.mod") {
		t.Errorf("cross-talk detected: call_1 got call_2 arguments")
	}
}

// Case D: delta arrives before any start event (missing-start fallback). The
// opening metadata chunk must still be synthesized and arguments must not
// duplicate.
func TestCommandCodeStream_DeltaBeforeStartSynthesizesInit(t *testing.T) {
	lines := []string{
		`{"type":"tool-input-delta","id":"call_d","delta":"{\"x\":"}`,
		`{"type":"tool-input-delta","id":"call_d","delta":"1}"}`,
		`{"type":"tool-call","id":"call_d","toolName":"exec","input":{"x":1}}`,
	}
	chunks := feedLines(t, lines...)
	argsByIndex, nameByIndex := collectToolArguments(t, chunks)

	if got := argsByIndex[0]; got != `{"x":1}` {
		t.Fatalf("accumulated arguments = %q, want {\"x\":1}", got)
	}
	// Name arrives only on the terminal event in this scenario.
	if nameByIndex[0] != "exec" {
		t.Errorf("tool name = %q, want exec (name-only follow-up)", nameByIndex[0])
	}
}

// Case E: malformed/empty arguments on terminal events must fail closed
// (no panic, no bogus chunk; accumulated arguments stay what deltas produced).
func TestCommandCodeStream_EmptyTerminalInputFailsClosed(t *testing.T) {
	lines := []string{
		`{"type":"tool-input-start","id":"call_e","toolName":"foo"}`,
		`{"type":"tool-input-delta","id":"call_e","delta":"{}"}`,
		`{"type":"tool-input-end","id":"call_e"}`,
	}
	chunks := feedLines(t, lines...)
	argsByIndex, _ := collectToolArguments(t, chunks)
	if got := argsByIndex[0]; got != "{}" {
		t.Errorf("accumulated arguments = %q, want {}", got)
	}
}

// Case F: JSON validity of every emitted chunk (fail-closed guarantee).
func TestCommandCodeStream_AllChunksAreValidJSON(t *testing.T) {
	lines := []string{
		`{"type":"tool-input-start","id":"c1","toolName":"bash"}`,
		`{"type":"tool-input-delta","id":"c1","delta":"{\"a\":"}`,
		`{"type":"tool-input-delta","id":"c1","delta":"1}"}`,
		`{"type":"tool-input-end","id":"c1","input":{"a":1}}`,
		`{"type":"finish","finishReason":"tool-calls"}`,
	}
	chunks := feedLines(t, lines...)
	for i, chunk := range chunks {
		var obj map[string]any
		if err := json.Unmarshal(chunk, &obj); err != nil {
			t.Fatalf("chunk %d invalid JSON: %v (%s)", i, err, string(chunk))
		}
	}
	if len(chunks) < 4 {
		t.Fatalf("expected at least 4 chunks (init, 2 delta, finish), got %d", len(chunks))
	}
}

// TestNonStream_FailClosedOnMalformedNDJSON verifies the non-stream translator
// fails closed: malformed NDJSON yields a well-formed OpenAI error object, not
// raw NDJSON leaking to the client.
func TestNonStream_FailClosedOnMalformedNDJSON(t *testing.T) {
	raw := []byte(`{"type":"text-delta","id":"txt-0","text":"partial"}` + "\n" + `not-json`)
	out := ConvertCommandCodeNonStreamToOpenAI(context.Background(), "m", nil, nil, raw, nil)

	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("output must be valid JSON, got: %q (err=%v)", string(out), err)
	}
	msg, hasErr := parsed["error"].(map[string]any)
	if !hasErr {
		t.Fatalf("output must contain an error object, got: %q", string(out))
	}
	if msg["type"] != "server_error" {
		t.Errorf("error.type = %v, want server_error", msg["type"])
	}
}

// TestNonStream_PassThroughAlreadyOpenAIJSON verifies that when the input is
// already an aggregated OpenAI response (the real post-executor input), the
// translator passes it through unchanged on aggregation failure.
func TestNonStream_PassThroughAlreadyOpenAIJSON(t *testing.T) {
	openAI := []byte(`{"id":"chatcmpl-x","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`)
	out := ConvertCommandCodeNonStreamToOpenAI(context.Background(), "m", nil, nil, openAI, nil)
	if string(out) != string(openAI) {
		t.Fatalf("already-aggregated OpenAI JSON must pass through unchanged; got: %q", string(out))
	}
}

// TestCommandCodeStream_ReverseInterleavedToolEvents verifies that when a
// later call's delta arrives before an earlier call's start (call_B delta,
// then call_A start, then call_A delta), indices are assigned by first-observed
// order and remain stable — the OpenAI stream contract only requires each
// tool_calls delta to carry a consistent index per call id.
func TestCommandCodeStream_ReverseInterleavedToolEvents(t *testing.T) {
	lines := []string{
		`{"type":"tool-input-start","id":"call_B","toolName":"second_fn"}`,
		`{"type":"tool-input-delta","id":"call_B","delta":"{\"b\":1}"}`,
		`{"type":"tool-input-start","id":"call_A","toolName":"first_fn"}`,
		`{"type":"tool-input-delta","id":"call_A","delta":"{\"a\":1}"}`,
		`{"type":"tool-input-end","id":"call_A","input":{"a":1}}`,
		`{"type":"tool-input-end","id":"call_B","input":{"b":1}}`,
		`{"type":"finish-step","finishReason":"tool-calls","usage":{"inputTokens":5,"outputTokens":3,"totalTokens":8}}`,
	}
	chunks := feedLines(t, lines...)
	_, nameByIndex := collectToolArguments(t, chunks)

	// first-observed order: call_B seen first => index 0, call_A => index 1.
	if nameByIndex[0] != "second_fn" || nameByIndex[1] != "first_fn" {
		t.Fatalf("expected first-observed order second_fn=0 first_fn=1, got %v", nameByIndex)
	}
}
