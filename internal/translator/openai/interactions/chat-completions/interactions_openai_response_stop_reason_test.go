package chat_completions

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func runInteractionsToOpenAIChat(t *testing.T, model string, chunks ...string) [][]byte {
	t.Helper()
	var param any
	var out [][]byte
	for _, chunk := range chunks {
		out = append(out, ConvertInteractionsResponseToOpenAI(context.Background(), model, nil, nil, []byte(chunk), &param)...)
	}
	return out
}

func lastFinishReason(t *testing.T, out [][]byte) (string, []byte) {
	t.Helper()
	for i := len(out) - 1; i >= 0; i-- {
		if fr := gjson.GetBytes(out[i], "choices.0.finish_reason"); fr.Exists() && fr.Type != gjson.Null {
			return fr.String(), out[i]
		}
	}
	return "", nil
}

// A function_call step that follows a thought step and a model_output step is
// still tool_calls[0] on the OpenAI side. Devin/Gemini step indices count every
// step type, OpenAI tool_calls indices must be 0-based and contiguous.
func TestConvertInteractionsResponseToOpenAIStream_ToolCallIndexIsOrdinalNotStepIndex(t *testing.T) {
	out := runInteractionsToOpenAIChat(t, "devin/swe-2",
		`data: {"event_type":"interaction.created","interaction":{"id":"i1","model":"devin/swe-2"}}`,
		`data: {"event_type":"step.start","index":0,"step":{"type":"thought"}}`,
		`data: {"event_type":"step.delta","index":0,"delta":{"type":"thought_summary","text":"thinking"}}`,
		`data: {"event_type":"step.stop","index":0}`,
		`data: {"event_type":"step.start","index":1,"step":{"type":"model_output"}}`,
		`data: {"event_type":"step.delta","index":1,"delta":{"type":"text","text":"Let me edit."}}`,
		`data: {"event_type":"step.stop","index":1}`,
		`data: {"event_type":"step.start","index":2,"step":{"type":"function_call","id":"call_a","name":"edit","arguments":{}}}`,
		`data: {"event_type":"step.delta","index":2,"delta":{"type":"arguments_delta","arguments":"{\"path\":\"a.go\"}"}}`,
		`data: {"event_type":"step.stop","index":2}`,
		`data: {"event_type":"step.start","index":3,"step":{"type":"function_call","id":"call_b","name":"read","arguments":{}}}`,
		`data: {"event_type":"step.delta","index":3,"delta":{"type":"arguments_delta","arguments":"{\"path\":\"b.go\"}"}}`,
		`data: {"event_type":"step.stop","index":3}`,
		`data: {"event_type":"interaction.completed","interaction":{"id":"i1","status":"completed","stop_reason":"tool_calls","usage":{"total_input_tokens":2,"total_output_tokens":3}}}`,
	)

	var toolChunks [][]byte
	for _, chunk := range out {
		if gjson.GetBytes(chunk, "choices.0.delta.tool_calls").Exists() {
			toolChunks = append(toolChunks, chunk)
		}
	}
	if len(toolChunks) != 4 {
		t.Fatalf("expected 4 tool_calls chunks (2 starts + 2 args), got %d", len(toolChunks))
	}
	wantIndex := []int64{0, 0, 1, 1}
	wantID := []string{"call_a", "", "call_b", ""}
	for i, chunk := range toolChunks {
		if got := gjson.GetBytes(chunk, "choices.0.delta.tool_calls.0.index").Int(); got != wantIndex[i] {
			t.Fatalf("chunk %d tool_calls index = %d, want %d. Payload: %s", i, got, wantIndex[i], chunk)
		}
		if got := gjson.GetBytes(chunk, "choices.0.delta.tool_calls.0.id").String(); got != wantID[i] {
			t.Fatalf("chunk %d tool_calls id = %q, want %q. Payload: %s", i, got, wantID[i], chunk)
		}
	}
	if got, payload := lastFinishReason(t, out); got != "tool_calls" {
		t.Fatalf("finish_reason = %q, want tool_calls. Payload: %s", got, payload)
	}
}

func TestConvertInteractionsResponseToOpenAIStream_FallbackToolIDUsesOrdinal(t *testing.T) {
	out := runInteractionsToOpenAIChat(t, "devin/swe-2",
		`data: {"event_type":"interaction.created","interaction":{"id":"i1","model":"devin/swe-2"}}`,
		`data: {"event_type":"step.start","index":0,"step":{"type":"model_output"}}`,
		`data: {"event_type":"step.delta","index":0,"delta":{"type":"text","text":"hi"}}`,
		`data: {"event_type":"step.start","index":1,"step":{"type":"function_call","name":"edit","arguments":{}}}`,
	)
	toolStart := findOpenAIChatChunk(out, "choices.0.delta.tool_calls.0.function.name")
	if got := gjson.GetBytes(toolStart, "choices.0.delta.tool_calls.0.id").String(); got != "call_0" {
		t.Fatalf("fallback tool id = %q, want call_0. Payload: %s", got, toolStart)
	}
}

func TestConvertInteractionsResponseToOpenAIStream_ExplicitLengthStopReasonWithTruncatedToolCall(t *testing.T) {
	out := runInteractionsToOpenAIChat(t, "devin/swe-2",
		`data: {"event_type":"interaction.created","interaction":{"id":"i1","model":"devin/swe-2"}}`,
		`data: {"event_type":"step.start","index":0,"step":{"type":"function_call","id":"call_a","name":"edit","arguments":{}}}`,
		`data: {"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":"{\"path\":\"a.go\",\"content\":\"partial"}}`,
		`data: {"event_type":"step.stop","index":0}`,
		`data: {"event_type":"interaction.completed","interaction":{"id":"i1","status":"incomplete","stop_reason":"length","stop_reason_code":3,"usage":{"total_input_tokens":2,"total_output_tokens":65536}}}`,
	)
	got, payload := lastFinishReason(t, out)
	if got != "length" {
		t.Fatalf("finish_reason = %q, want length. Payload: %s", got, payload)
	}
	if usage := gjson.GetBytes(payload, "usage.completion_tokens").Int(); usage != 65536 {
		t.Fatalf("completion_tokens = %d, want 65536. Payload: %s", usage, payload)
	}
}

func TestConvertInteractionsResponseToOpenAIStream_IncompleteStatusWithoutToolCallIsLength(t *testing.T) {
	out := runInteractionsToOpenAIChat(t, "devin/swe-2",
		`data: {"event_type":"interaction.created","interaction":{"id":"i1","model":"devin/swe-2"}}`,
		`data: {"event_type":"step.start","index":0,"step":{"type":"model_output"}}`,
		`data: {"event_type":"step.delta","index":0,"delta":{"type":"text","text":"very long te"}}`,
		`data: {"event_type":"interaction.completed","interaction":{"id":"i1","status":"incomplete"}}`,
	)
	if got, payload := lastFinishReason(t, out); got != "length" {
		t.Fatalf("finish_reason = %q, want length. Payload: %s", got, payload)
	}
}

func TestConvertInteractionsResponseToOpenAIStream_TruncatedToolArgumentsHeuristic(t *testing.T) {
	// No explicit stop reason and status=completed, but the tool arguments never
	// closed: the only way that happens is an upstream cut mid-call.
	out := runInteractionsToOpenAIChat(t, "devin/swe-2",
		`data: {"event_type":"interaction.created","interaction":{"id":"i1","model":"devin/swe-2"}}`,
		`data: {"event_type":"step.start","index":0,"step":{"type":"function_call","id":"call_a","name":"edit","arguments":{}}}`,
		`data: {"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":"{\"path\":\"a.go\",\"content\":\"partial"}}`,
		`data: {"event_type":"interaction.completed","interaction":{"id":"i1","status":"completed"}}`,
	)
	if got, payload := lastFinishReason(t, out); got != "length" {
		t.Fatalf("finish_reason = %q, want length. Payload: %s", got, payload)
	}
}

func TestConvertInteractionsResponseToOpenAIStream_CompleteToolArgumentsStayToolCalls(t *testing.T) {
	out := runInteractionsToOpenAIChat(t, "devin/swe-2",
		`data: {"event_type":"interaction.created","interaction":{"id":"i1","model":"devin/swe-2"}}`,
		`data: {"event_type":"step.start","index":0,"step":{"type":"function_call","id":"call_a","name":"edit","arguments":{}}}`,
		`data: {"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":"{\"path\":"}}`,
		`data: {"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":"\"a.go\"}"}}`,
		`data: {"event_type":"interaction.completed","interaction":{"id":"i1","status":"completed","stop_reason":"tool_calls"}}`,
	)
	if got, payload := lastFinishReason(t, out); got != "tool_calls" {
		t.Fatalf("finish_reason = %q, want tool_calls. Payload: %s", got, payload)
	}
}

func TestConvertInteractionsResponseToOpenAIStream_ContentFilterStopReason(t *testing.T) {
	out := runInteractionsToOpenAIChat(t, "devin/swe-2",
		`data: {"event_type":"interaction.created","interaction":{"id":"i1","model":"devin/swe-2"}}`,
		`data: {"event_type":"interaction.completed","interaction":{"id":"i1","status":"completed","stop_reason":"content_filter"}}`,
	)
	if got, payload := lastFinishReason(t, out); got != "content_filter" {
		t.Fatalf("finish_reason = %q, want content_filter. Payload: %s", got, payload)
	}
}

func TestConvertInteractionsResponseToOpenAIStream_ResponseFailedSuppressesSyntheticStop(t *testing.T) {
	out := runInteractionsToOpenAIChat(t, "devin/swe-2",
		`data: {"event_type":"interaction.created","interaction":{"id":"i1","model":"devin/swe-2"}}`,
		`data: {"event_type":"step.start","index":0,"step":{"type":"model_output"}}`,
		`data: {"event_type":"step.delta","index":0,"delta":{"type":"text","text":"partial"}}`,
		`data: {"event_type":"response.failed","error":{"message":"devin stream terminated prematurely before EOS trailer","code":"stream_truncated"}}`,
		`data: {"event_type":"interaction.completed","interaction":{"id":"i1","status":"completed"}}`,
	)
	if got, payload := lastFinishReason(t, out); got != "" {
		t.Fatalf("finish_reason = %q after response.failed, want none. Payload: %s", got, payload)
	}
}

func TestConvertInteractionsResponseToOpenAINonStream_StopReasonLength(t *testing.T) {
	raw := []byte(`{"id":"i1","model":"devin/swe-2","status":"incomplete","stop_reason":"length","stop_reason_code":3,"steps":[{"type":"function_call","id":"call_1","name":"edit","arguments":{}}],"usage":{"total_input_tokens":2,"total_output_tokens":3,"total_tokens":5}}`)
	out := ConvertInteractionsResponseToOpenAINonStream(context.Background(), "devin/swe-2", nil, nil, raw, nil)
	if got := gjson.GetBytes(out, "choices.0.finish_reason").String(); got != "length" {
		t.Fatalf("finish_reason = %q, want length. Output: %s", got, out)
	}
	if got := gjson.GetBytes(out, "choices.0.message.tool_calls.0.id").String(); got != "call_1" {
		t.Fatalf("tool call still expected to be present, got id %q. Output: %s", got, out)
	}
}

func TestConvertInteractionsResponseToOpenAINonStream_DefaultStopUnchanged(t *testing.T) {
	raw := []byte(`{"id":"i1","model":"devin/swe-2","status":"completed","steps":[{"type":"model_output","content":[{"type":"text","text":"hi"}]}]}`)
	out := ConvertInteractionsResponseToOpenAINonStream(context.Background(), "devin/swe-2", nil, nil, raw, nil)
	if got := gjson.GetBytes(out, "choices.0.finish_reason").String(); got != "stop" {
		t.Fatalf("finish_reason = %q, want stop. Output: %s", got, out)
	}
}
