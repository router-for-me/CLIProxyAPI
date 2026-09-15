package responses

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func runInteractionsToResponses(t *testing.T, chunks ...string) [][]byte {
	t.Helper()
	var param any
	var out [][]byte
	for _, chunk := range chunks {
		out = append(out, ConvertInteractionsResponseToOpenAIResponses(context.Background(), "devin/swe-2", nil, nil, []byte(chunk), &param)...)
	}
	return out
}

// The AI SDK's Responses parser validates response.output_item.done strictly:
// a function_call item without status:"completed" fails the schema, the frame is
// degraded to an unknown chunk, and the client never emits the tool call.
func TestConvertInteractionsResponseToOpenAIResponsesStream_FunctionCallItemsCarryStatus(t *testing.T) {
	out := runInteractionsToResponses(t,
		`data: {"event_type":"interaction.created","interaction":{"id":"i1","model":"devin/swe-2"}}`,
		`data: {"event_type":"step.start","index":0,"step":{"type":"thought"}}`,
		`data: {"event_type":"step.stop","index":0}`,
		`data: {"event_type":"step.start","index":1,"step":{"type":"function_call","id":"call_a","name":"write_file","arguments":{}}}`,
		`data: {"event_type":"step.delta","index":1,"delta":{"type":"arguments_delta","arguments":"{\"path\":\"a.html\"}"}}`,
		`data: {"event_type":"step.stop","index":1}`,
		`data: {"event_type":"interaction.completed","interaction":{"id":"i1","status":"completed","stop_reason":"tool_calls","usage":{"total_input_tokens":10,"total_output_tokens":20,"total_tokens":30}}}`,
	)
	var added, done []byte
	for _, event := range out {
		payload := ssePayload(event)
		if gjson.GetBytes(payload, "item.type").String() != "function_call" {
			continue
		}
		switch gjson.GetBytes(payload, "type").String() {
		case "response.output_item.added":
			added = payload
		case "response.output_item.done":
			done = payload
		}
	}
	if added == nil || done == nil {
		t.Fatalf("missing function_call added/done events: %v", responsesEventNames(out))
	}
	if got := gjson.GetBytes(added, "item.status").String(); got != "in_progress" {
		t.Fatalf("added item.status = %q, want in_progress. Payload: %s", got, added)
	}
	if got := gjson.GetBytes(done, "item.status").String(); got != "completed" {
		t.Fatalf("done item.status = %q, want completed. Payload: %s", got, done)
	}
	if got := gjson.GetBytes(done, "item.arguments").String(); got != `{"path":"a.html"}` {
		t.Fatalf("done item.arguments = %q. Payload: %s", got, done)
	}
	completed := findResponsesEventPayload(out, "response.completed")
	if completed == nil {
		t.Fatalf("response.completed missing: %v", responsesEventNames(out))
	}
	if got := gjson.GetBytes(completed, "response.output.1.status").String(); got != "completed" {
		t.Fatalf("completed output function_call status = %q, want completed. Payload: %s", got, completed)
	}
	if got := gjson.GetBytes(completed, "response.status").String(); got != "completed" {
		t.Fatalf("response.status = %q, want completed", got)
	}
	if got := gjson.GetBytes(completed, "response.usage.input_tokens").Int(); got != 10 {
		t.Fatalf("usage.input_tokens = %d, want 10. Payload: %s", got, completed)
	}
}

func TestConvertInteractionsResponseToOpenAIResponsesStream_LengthStopReasonEmitsIncomplete(t *testing.T) {
	out := runInteractionsToResponses(t,
		`data: {"event_type":"interaction.created","interaction":{"id":"i1","model":"devin/swe-2"}}`,
		`data: {"event_type":"step.start","index":0,"step":{"type":"function_call","id":"call_a","name":"write_file","arguments":{}}}`,
		`data: {"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":"{\"path\":\"a.html\",\"content\":\"partial"}}`,
		`data: {"event_type":"step.stop","index":0}`,
		`data: {"event_type":"interaction.completed","interaction":{"id":"i1","status":"incomplete","stop_reason":"length","stop_reason_code":3,"usage":{"total_input_tokens":10,"total_output_tokens":65536}}}`,
	)
	names := responsesEventNames(out)
	if strings.Contains(strings.Join(names, ","), "response.completed") {
		t.Fatalf("response.completed emitted for a truncated interaction: %v", names)
	}
	incomplete := findResponsesEventPayload(out, "response.incomplete")
	if incomplete == nil {
		t.Fatalf("response.incomplete missing: %v", names)
	}
	if got := gjson.GetBytes(incomplete, "response.status").String(); got != "incomplete" {
		t.Fatalf("response.status = %q, want incomplete. Payload: %s", got, incomplete)
	}
	if got := gjson.GetBytes(incomplete, "response.incomplete_details.reason").String(); got != "max_output_tokens" {
		t.Fatalf("incomplete_details.reason = %q, want max_output_tokens. Payload: %s", got, incomplete)
	}
	if got := gjson.GetBytes(incomplete, "response.usage.output_tokens").Int(); got != 65536 {
		t.Fatalf("usage.output_tokens = %d, want 65536", got)
	}
}

// If the upstream never closed the function_call step, the terminal event must
// still be preceded by function_call_arguments.done + output_item.done so the
// client receives the (possibly partial) tool call instead of nothing.
func TestConvertInteractionsResponseToOpenAIResponsesStream_ClosesDanglingFunctionCallBeforeCompletion(t *testing.T) {
	out := runInteractionsToResponses(t,
		`data: {"event_type":"interaction.created","interaction":{"id":"i1","model":"devin/swe-2"}}`,
		`data: {"event_type":"step.start","index":0,"step":{"type":"function_call","id":"call_a","name":"write_file","arguments":{}}}`,
		`data: {"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":"{\"path\":\"a.html\"}"}}`,
		`data: {"event_type":"interaction.completed","interaction":{"id":"i1","status":"completed"}}`,
	)
	got := strings.Join(responsesEventNames(out), ",")
	want := "response.created,response.output_item.added,response.function_call_arguments.delta,response.function_call_arguments.done,response.output_item.done,response.completed"
	if got != want {
		t.Fatalf("events = %s, want %s", got, want)
	}
	done := findResponsesEventPayload(out, "response.output_item.done")
	if s := gjson.GetBytes(done, "item.status").String(); s != "completed" {
		t.Fatalf("dangling done item.status = %q, want completed", s)
	}
	if id := gjson.GetBytes(done, "item.call_id").String(); id != "call_a" {
		t.Fatalf("dangling done call_id = %q, want call_a", id)
	}
}

func TestConvertInteractionsResponseToOpenAIResponsesStream_CompletedUsageAlwaysNumeric(t *testing.T) {
	out := runInteractionsToResponses(t,
		`data: {"event_type":"interaction.created","interaction":{"id":"i1","model":"devin/swe-2"}}`,
		`data: {"event_type":"interaction.completed","interaction":{"id":"i1","status":"completed"}}`,
	)
	completed := findResponsesEventPayload(out, "response.completed")
	if completed == nil {
		t.Fatalf("response.completed missing")
	}
	for _, path := range []string{"response.usage.input_tokens", "response.usage.output_tokens"} {
		v := gjson.GetBytes(completed, path)
		if !v.Exists() || v.Type != gjson.Number {
			t.Fatalf("%s must be a number, got %s. Payload: %s", path, v.Raw, completed)
		}
	}
	if !gjson.GetBytes(completed, "response.incomplete_details").Exists() {
		t.Fatalf("incomplete_details should be present (null) for schema stability. Payload: %s", completed)
	}
}

func TestConvertInteractionsResponseToOpenAIResponsesStream_CompletionIsIdempotent(t *testing.T) {
	out := runInteractionsToResponses(t,
		`data: {"event_type":"interaction.created","interaction":{"id":"i1","model":"devin/swe-2"}}`,
		`data: {"event_type":"interaction.completed","interaction":{"id":"i1","status":"completed"}}`,
		`data: {"event_type":"finish","metadata":{"total_usage":{"total_input_tokens":1,"total_output_tokens":1}}}`,
	)
	count := 0
	for _, name := range responsesEventNames(out) {
		if name == "response.completed" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("response.completed emitted %d times, want 1: %v", count, responsesEventNames(out))
	}
}

func TestConvertInteractionsResponseToOpenAIResponsesNonStream_FunctionCallStatusAndIncomplete(t *testing.T) {
	raw := []byte(`{"id":"i1","model":"devin/swe-2","status":"incomplete","stop_reason":"length","steps":[{"type":"function_call","id":"call_1","name":"write_file","arguments":{"path":"a.html"}}],"usage":{"total_input_tokens":2,"total_output_tokens":3}}`)
	out := ConvertInteractionsResponseToOpenAIResponsesNonStream(context.Background(), "devin/swe-2", nil, nil, raw, nil)
	if got := gjson.GetBytes(out, "output.0.status").String(); got != "completed" {
		t.Fatalf("output.0.status = %q, want completed. Output: %s", got, out)
	}
	if got := gjson.GetBytes(out, "output.0.id").String(); got != "call_1" {
		t.Fatalf("output.0.id = %q, want call_1. Output: %s", got, out)
	}
	if got := gjson.GetBytes(out, "status").String(); got != "incomplete" {
		t.Fatalf("status = %q, want incomplete. Output: %s", got, out)
	}
	if got := gjson.GetBytes(out, "incomplete_details.reason").String(); got != "max_output_tokens" {
		t.Fatalf("incomplete_details.reason = %q, want max_output_tokens. Output: %s", got, out)
	}
	if got := gjson.GetBytes(out, "usage.output_tokens").Int(); got != 3 {
		t.Fatalf("usage.output_tokens = %d, want 3", got)
	}
}
