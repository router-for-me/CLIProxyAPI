package responses

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// customToolRequest is a minimal Responses-API request declaring apply_patch as
// a freeform `custom` tool alongside a regular `function` tool (shell). The
// response side uses the request's tools list to decide which tool calls must
// be re-emitted as custom_tool_call (bare input) vs function_call (JSON args).
const customToolRequest = `{"model":"gpt-5","tools":[` +
	`{"type":"custom","name":"apply_patch","description":"Use the apply_patch tool to edit files."},` +
	`{"type":"function","name":"shell","parameters":{"type":"object","properties":{"command":{"type":"string"}}}}` +
	`]}`

// barePatch is the literal text the Codex host expects inside custom_tool_call.input.
const barePatch = "*** Begin Patch\n*** Add File: /tmp/x.txt\n+hi\n*** End Patch"

// Streaming: a Claude tool_use for apply_patch (declared custom) must come back
// as a custom_tool_call carrying bare `input` text (no JSON wrapper, no
// arguments field).
func TestConvertClaudeResponse_Stream_CustomToolEmitsCustomToolCall(t *testing.T) {
	// The model was told to wrap the payload into the JSON `input` argument;
	// emit it as two input_json_delta fragments to mimic real streaming.
	lines := []string{
		`data: {"type":"message_start","message":{"id":"msg_cust","usage":{"input_tokens":5}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"id":"toolu_patch","type":"tool_use","name":"apply_patch"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"input\":\"*** Begin Patch\\n*** Add File: /tmp/x.txt\\n"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"+hi\\n*** End Patch\"}"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_stop"}`,
	}

	req := []byte(customToolRequest)
	var state any
	var addedItem, inputDone, doneItem, completed string
	for _, ln := range lines {
		evs := ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude", req, req, []byte(ln), &state)
		for _, e := range evs {
			s := string(e)
			if strings.Contains(s, "response.output_item.added") && strings.Contains(s, `"custom_tool_call"`) {
				addedItem = s
			}
			if strings.Contains(s, "response.custom_tool_call_input.done") {
				inputDone = s
			}
			if strings.Contains(s, "response.output_item.done") && strings.Contains(s, `"custom_tool_call"`) {
				doneItem = s
			}
			if strings.Contains(s, "response.completed") {
				completed = s
			}
		}
	}

	if addedItem == "" {
		t.Fatal("expected an output_item.added event with type custom_tool_call")
	}
	addedPayload := addedItem[strings.Index(addedItem, "{"):]
	if got := gjson.Get(addedPayload, "item.type").String(); got != "custom_tool_call" {
		t.Fatalf("added item.type = %q, want custom_tool_call: %s", got, addedPayload)
	}
	if got := gjson.Get(addedPayload, "item.name").String(); got != "apply_patch" {
		t.Fatalf("added item.name = %q, want apply_patch", got)
	}
	if gjson.Get(addedPayload, "item.arguments").Exists() {
		t.Fatalf("added item must not carry arguments field: %s", addedPayload)
	}

	if inputDone == "" {
		t.Fatal("expected a response.custom_tool_call_input.done event")
	}
	inDonePayload := inputDone[strings.Index(inputDone, "{"):]
	if got := gjson.Get(inDonePayload, "input").String(); got != barePatch {
		t.Fatalf("custom_tool_call_input.done input = %q, want bare patch %q", got, barePatch)
	}

	if doneItem == "" {
		t.Fatal("expected an output_item.done event with type custom_tool_call")
	}
	donePayload := doneItem[strings.Index(doneItem, "{"):]
	if got := gjson.Get(donePayload, "item.type").String(); got != "custom_tool_call" {
		t.Fatalf("done item.type = %q, want custom_tool_call: %s", got, donePayload)
	}
	if got := gjson.Get(donePayload, "item.input").String(); got != barePatch {
		t.Fatalf("done item.input = %q, want bare patch (no JSON wrapper): %s", got, donePayload)
	}
	if gjson.Get(donePayload, "item.arguments").Exists() {
		t.Fatalf("done item must not carry arguments field: %s", donePayload)
	}
	if got := gjson.Get(donePayload, "item.call_id").String(); got != "toolu_patch" {
		t.Fatalf("done item.call_id = %q, want toolu_patch", got)
	}

	// The custom_tool_call must also surface in the final aggregated output.
	if completed == "" {
		t.Fatal("expected a response.completed event")
	}
	cp := completed[strings.Index(completed, "{"):]
	found := false
	gjson.Get(cp, "response.output").ForEach(func(_, v gjson.Result) bool {
		if v.Get("type").String() == "custom_tool_call" {
			found = true
			if got := v.Get("input").String(); got != barePatch {
				t.Fatalf("completed custom_tool_call input = %q, want bare patch", got)
			}
			if v.Get("arguments").Exists() {
				t.Fatalf("completed custom_tool_call must not carry arguments: %s", v.Raw)
			}
		}
		return true
	})
	if !found {
		t.Fatalf("expected custom_tool_call in response.completed output: %s", cp)
	}
}

// Non-stream: same scenario via the aggregating non-stream converter.
func TestConvertClaudeResponse_Stream_CustomToolDoesNotLeakWrapperDelta(t *testing.T) {
	// For downgraded custom tools the streamed input_json_delta fragments are
	// pieces of the JSON wrapper ({"input":"...). Emitting them verbatim as
	// response.custom_tool_call_input.delta corrupts the bare-input stream the
	// host concatenates. We must NOT emit custom_tool_call_input.delta events;
	// the bare input is delivered once at .done.
	lines := []string{
		`data: {"type":"message_start","message":{"id":"msg_cust","usage":{"input_tokens":5}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"id":"toolu_patch","type":"tool_use","name":"apply_patch"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"input\":\"*** Begin Patch\\n*** Add File: /tmp/x.txt\\n"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"+hi\\n*** End Patch\"}"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_stop"}`,
	}
	req := []byte(customToolRequest)
	var state any
	var inputDone string
	deltaCount := 0
	for _, ln := range lines {
		evs := ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude", req, req, []byte(ln), &state)
		for _, e := range evs {
			s := string(e)
			if strings.Contains(s, "response.custom_tool_call_input.delta") {
				deltaCount++
			}
			if strings.Contains(s, "response.custom_tool_call_input.done") {
				inputDone = s
			}
		}
	}
	if deltaCount != 0 {
		t.Fatalf("custom tools must NOT emit custom_tool_call_input.delta (raw JSON wrapper would corrupt the stream); got %d delta events", deltaCount)
	}
	if inputDone == "" {
		t.Fatal("expected the bare input to still arrive via custom_tool_call_input.done")
	}
	inDonePayload := inputDone[strings.Index(inputDone, "{"):]
	if got := gjson.Get(inDonePayload, "input").String(); got != barePatch {
		t.Fatalf("input.done must carry the bare patch, got %q", got)
	}
}

func TestConvertClaudeResponse_NonStream_CustomToolEmitsCustomToolCall(t *testing.T) {
	raw := []byte(strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_cust2"}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"id":"toolu_patch2","type":"tool_use","name":"apply_patch"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"input\":\"*** Begin Patch\\n*** Add File: /tmp/x.txt\\n"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"+hi\\n*** End Patch\"}"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_stop"}`,
	}, "\n"))

	req := []byte(customToolRequest)
	var state any
	out := ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "claude", req, req, raw, &state)

	item := gjson.GetBytes(out, "output.0")
	if got := item.Get("type").String(); got != "custom_tool_call" {
		t.Fatalf("output[0].type = %q, want custom_tool_call: %s", got, string(out))
	}
	if got := item.Get("name").String(); got != "apply_patch" {
		t.Fatalf("output[0].name = %q, want apply_patch", got)
	}
	if got := item.Get("input").String(); got != barePatch {
		t.Fatalf("output[0].input = %q, want bare patch (no JSON wrapper): %s", got, string(out))
	}
	if item.Get("arguments").Exists() {
		t.Fatalf("output[0] must not carry arguments field: %s", string(out))
	}
	if got := item.Get("call_id").String(); got != "toolu_patch2" {
		t.Fatalf("output[0].call_id = %q, want toolu_patch2", got)
	}
}

// Regression: a regular `function` tool (shell) must keep emitting
// function_call + JSON arguments and must NOT be turned into custom_tool_call.
func TestConvertClaudeResponse_NonStream_RegularFunctionUnaffected(t *testing.T) {
	raw := []byte(strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_fn"}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"id":"toolu_sh","type":"tool_use","name":"shell"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"ls\"}"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_stop"}`,
	}, "\n"))

	req := []byte(customToolRequest)
	var state any
	out := ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "claude", req, req, raw, &state)

	item := gjson.GetBytes(out, "output.0")
	if got := item.Get("type").String(); got != "function_call" {
		t.Fatalf("output[0].type = %q, want function_call: %s", got, string(out))
	}
	if got := item.Get("name").String(); got != "shell" {
		t.Fatalf("output[0].name = %q, want shell", got)
	}
	if got := item.Get("arguments").String(); got != `{"command":"ls"}` {
		t.Fatalf("output[0].arguments = %q, want JSON arguments", got)
	}
	if item.Get("input").Exists() {
		t.Fatalf("regular function_call must not carry input field: %s", string(out))
	}
}

// Regression (streaming): a regular function tool must keep emitting
// function_call output items with JSON arguments.
func TestConvertClaudeResponse_Stream_RegularFunctionUnaffected(t *testing.T) {
	lines := []string{
		`data: {"type":"message_start","message":{"id":"msg_fn2"}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"id":"toolu_sh2","type":"tool_use","name":"shell"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"ls\"}"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_stop"}`,
	}

	req := []byte(customToolRequest)
	var state any
	var doneItem string
	for _, ln := range lines {
		evs := ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude", req, req, []byte(ln), &state)
		for _, e := range evs {
			s := string(e)
			if strings.Contains(s, "response.output_item.done") && strings.Contains(s, `"function_call"`) {
				doneItem = s
			}
			if strings.Contains(s, `"custom_tool_call"`) {
				t.Fatalf("regular function tool must not emit custom_tool_call: %s", s)
			}
		}
	}
	if doneItem == "" {
		t.Fatal("expected an output_item.done event with type function_call")
	}
	donePayload := doneItem[strings.Index(doneItem, "{"):]
	if got := gjson.Get(donePayload, "item.arguments").String(); got != `{"command":"ls"}` {
		t.Fatalf("done item.arguments = %q, want JSON arguments: %s", got, donePayload)
	}
	if got := gjson.Get(donePayload, "item.name").String(); got != "shell" {
		t.Fatalf("done item.name = %q, want shell", got)
	}
}
