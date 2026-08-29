package helps

import (
	"context"
	"testing"
)

func TestIsResponsesTokenEvent_Classification(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{
			name:    "empty payload",
			payload: "",
			want:    false,
		},
		{
			name:    "whitespace only",
			payload: "   \n\t  ",
			want:    false,
		},
		{
			name:    "codex rate limits metadata",
			payload: `{"type":"codex.rate_limits","rate_limits":{"plan_type":"pro"}}`,
			want:    false,
		},
		{
			name:    "codex response metadata",
			payload: `{"type":"codex.response.metadata","etag":"W/\"123\""}`,
			want:    false,
		},
		{
			name:    "responsesapi websocket timing",
			payload: `{"type":"responsesapi.websocket_timing","timing":{"duration_ms":100}}`,
			want:    false,
		},
		{
			name:    "response created",
			payload: `{"type":"response.created","response":{"id":"resp_123","status":"in_progress"}}`,
			want:    false,
		},
		{
			name:    "response in progress",
			payload: `{"type":"response.in_progress","response":{"id":"resp_123","tools":[{"type":"function"}]}}`,
			want:    false,
		},
		{
			name:    "response output item added with encrypted content only",
			payload: `{"type":"response.output_item.added","item":{"type":"reasoning","encrypted_content":"gAAAAAB..."}}`,
			want:    false,
		},
		{
			name:    "response content part added",
			payload: `{"type":"response.content_part.added","part":{"type":"text","text":""}}`,
			want:    false,
		},
		{
			name:    "response reasoning summary part added",
			payload: `{"type":"response.reasoning_summary_part.added","part":{"type":"summary_text","text":""}}`,
			want:    false,
		},
		{
			name:    "response reasoning summary text delta empty",
			payload: `{"type":"response.reasoning_summary_text.delta","delta":""}`,
			want:    false,
		},
		{
			name:    "response reasoning summary text delta non-empty",
			payload: `{"type":"response.reasoning_summary_text.delta","delta":"**Inspecting**"}`,
			want:    true,
		},
		{
			name:    "response reasoning delta non-empty",
			payload: `{"type":"response.reasoning.delta","delta":"Analyzing requirements..."}`,
			want:    true,
		},
		{
			name:    "response reasoning text delta non-empty",
			payload: `{"type":"response.reasoning_text.delta","delta":"Step 1: Check code"}`,
			want:    true,
		},
		{
			name:    "response output text delta non-empty",
			payload: `{"type":"response.output_text.delta","delta":"Hello world"}`,
			want:    true,
		},
		{
			name:    "response text delta non-empty",
			payload: `{"type":"response.text.delta","delta":"Direct text chunk"}`,
			want:    true,
		},
		{
			name:    "response function call arguments delta non-empty",
			payload: `{"type":"response.function_call_arguments.delta","delta":"{\"query\":\"test\"}"}`,
			want:    true,
		},
		{
			name:    "response custom_tool_call_input delta non-empty",
			payload: `{"type":"response.custom_tool_call_input.delta","delta":"{\"param\":1}"}`,
			want:    true,
		},
		{
			name:    "response code interpreter call code delta non-empty",
			payload: `{"type":"response.code_interpreter_call_code.delta","delta":"import math\n"}`,
			want:    true,
		},
		{
			name:    "response mcp call arguments delta non-empty",
			payload: `{"type":"response.mcp_call_arguments.delta","delta":"{\"tool\":\"lookup\"}"}`,
			want:    true,
		},
		{
			name:    "response shell call command added with non-empty command",
			payload: `{"type":"response.shell_call_command.added","command":"ls -la"}`,
			want:    true,
		},
		{
			name:    "response shell call command added with empty command",
			payload: `{"type":"response.shell_call_command.added","command":""}`,
			want:    false,
		},
		{
			name:    "response shell call command delta non-empty",
			payload: `{"type":"response.shell_call_command.delta","delta":"ls -la\n"}`,
			want:    true,
		},
		{
			name:    "response shell call output content delta is tool execution output, not model token",
			payload: `{"type":"response.shell_call_output_content.delta","delta":{"stdout":"output text\n","stderr":""}}`,
			want:    false,
		},
		{
			name:    "response shell call output content done is tool execution output, not model token",
			payload: `{"type":"response.shell_call_output_content.done","output":[]}`,
			want:    false,
		},
		{
			name:    "response refusal delta non-empty",
			payload: `{"type":"response.refusal.delta","delta":"I cannot fulfill this request"}`,
			want:    true,
		},
		{
			name:    "response audio transcript delta non-empty",
			payload: `{"type":"response.audio.transcript.delta","delta":"Spoken text"}`,
			want:    true,
		},
		{
			name:    "response audio delta non-empty",
			payload: `{"type":"response.audio.delta","delta":"UklGRi..."}`,
			want:    true,
		},
		{
			name:    "response image generation call partial image non-empty",
			payload: `{"type":"response.image_generation_call.partial_image","partial_image_b64":"iVBORw0KGgo..."}`,
			want:    true,
		},
		{
			name:    "response web search call in progress",
			payload: `{"type":"response.web_search_call.in_progress"}`,
			want:    false,
		},
		{
			name:    "response file search call searching",
			payload: `{"type":"response.file_search_call.searching"}`,
			want:    false,
		},
		{
			name:    "response code interpreter call interpreting",
			payload: `{"type":"response.code_interpreter_call.interpreting"}`,
			want:    false,
		},
		{
			name:    "response mcp call in progress",
			payload: `{"type":"response.mcp_call.in_progress"}`,
			want:    false,
		},
		{
			name:    "response output item done function call empty args",
			payload: `{"type":"response.output_item.done","item":{"type":"function_call","name":"lookup","arguments":""}}`,
			want:    false,
		},
		{
			name:    "response output item done function call non-empty args",
			payload: `{"type":"response.output_item.done","item":{"type":"function_call","name":"lookup","arguments":"{\"q\":1}"}}`,
			want:    true,
		},
		{
			name:    "response output item done message empty",
			payload: `{"type":"response.output_item.done","item":{"type":"message","content":[]}}`,
			want:    false,
		},
		{
			name:    "response output item done message with text",
			payload: `{"type":"response.output_item.done","item":{"type":"message","content":[{"type":"text","text":"hello"}]}}`,
			want:    true,
		},
		{
			name:    "response completed fallback",
			payload: `{"type":"response.completed","response":{"id":"resp_123","status":"completed"}}`,
			want:    true,
		},
		{
			name:    "response done fallback",
			payload: `{"type":"response.done","response":{"id":"resp_123"}}`,
			want:    true,
		},
		{
			name:    "response incomplete fallback",
			payload: `{"type":"response.incomplete","response":{"id":"resp_123","status":"incomplete"}}`,
			want:    true,
		},
		{
			name:    "response failed fallback",
			payload: `{"type":"response.failed","response":{"id":"resp_123","status":"failed"}}`,
			want:    true,
		},
		{
			name:    "generic error fallback",
			payload: `{"type":"error","error":{"message":"overloaded","code":"rate_limit_exceeded"}}`,
			want:    true,
		},
		// SSE line format verification
		{
			name:    "SSE data line with output text delta",
			payload: `data: {"type":"response.output_text.delta","delta":"Hello SSE"}`,
			want:    true,
		},
		{
			name:    "SSE data line with response created",
			payload: `data: {"type":"response.created","response":{"id":"resp_sse"}}`,
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsResponsesTokenEvent([]byte(tt.payload)); got != tt.want {
				t.Errorf("IsResponsesTokenEvent(%s) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestObserveResponsesTokenEvent_Behavior(t *testing.T) {
	ctx := context.Background()
	reporter := NewUsageReporter(ctx, "codex", "gpt-5.6-luna", nil)
	reporter.StartResponseTTFT()

	// 1. Initial state: TTFT not set
	if reporter.IsTTFTSet() {
		t.Fatalf("expected IsTTFTSet() to be false initially")
	}

	// 2. Metadata event records first packet fallback, but does not mark effective TTFT
	ObserveResponsesTokenEvent(reporter, []byte(`{"type":"codex.rate_limits","rate_limits":{"plan_type":"pro"}}`))
	if reporter.IsTTFTSet() {
		t.Fatalf("metadata event must not trigger IsTTFTSet()")
	}
	if !reporter.IsFirstPacketSet() {
		t.Fatalf("expected IsFirstPacketSet() == true")
	}

	// 3. Response created event does not mark effective TTFT
	ObserveResponsesTokenEvent(reporter, []byte(`{"type":"response.created","response":{"id":"resp_1"}}`))
	if reporter.IsTTFTSet() {
		t.Fatalf("response.created must not trigger IsTTFTSet()")
	}

	// 4. Meaningful token delta triggers effective TTFT
	ObserveResponsesTokenEvent(reporter, []byte(`{"type":"response.output_text.delta","delta":"First word"}`))
	if !reporter.IsTTFTSet() {
		t.Fatalf("response.output_text.delta must trigger IsTTFTSet()")
	}
	tokenTTFT := reporter.ttftDuration()
	if tokenTTFT <= 0 {
		t.Fatalf("expected token TTFT > 0, got %v", tokenTTFT)
	}

	// 5. Subsequent calls should be fast-path no-ops and not alter TTFT
	ObserveResponsesTokenEvent(reporter, []byte(`{"type":"response.output_text.delta","delta":"Second word"}`))
	if reporter.ttftDuration() != tokenTTFT {
		t.Fatalf("subsequent ObserveResponsesTokenEvent must not modify already set TTFT")
	}
}

func TestObserveResponsesTokenEvent_FirstPacketFallback(t *testing.T) {
	ctx := context.Background()
	reporter := NewUsageReporter(ctx, "codex", "gpt-5.6-luna", nil)
	reporter.StartResponseTTFT()

	// Only send metadata events, no token deltas
	ObserveResponsesTokenEvent(reporter, []byte(`{"type":"codex.rate_limits","rate_limits":{"plan_type":"pro"}}`))
	ObserveResponsesTokenEvent(reporter, []byte(`{"type":"response.created","response":{"id":"resp_1"}}`))

	if reporter.IsTTFTSet() {
		t.Fatalf("expected IsTTFTSet() to be false when no tokens were received")
	}

	// First packet fallback must be returned
	if !reporter.IsFirstPacketSet() {
		t.Fatalf("expected IsFirstPacketSet() == true")
	}
}
