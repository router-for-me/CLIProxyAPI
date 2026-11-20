package auth

import "testing"

func TestIssue7722_JSONObjectCompletionFlagsRejectsCamelCaseMaxTokens(t *testing.T) {
	payload := []byte(`{"type":"message_delta","delta":{"stop_reason":"StopSequenceMaxTokens","stop_sequence":null},"usage":{"output_tokens":4}}`)
	completed, incomplete := jsonObjectCompletionFlags(payload)
	if completed || !incomplete {
		t.Fatalf("jsonObjectCompletionFlags(%s) = (completed=%v, incomplete=%v), want (false, true)", payload, completed, incomplete)
	}
	if isCompletedExecutionPayload(payload) {
		t.Fatalf("isCompletedExecutionPayload(%s) = true, want false", payload)
	}
}

func TestIssue7722_IsCompletedExecutionPayloadClassifiesAnthropicStopReason(t *testing.T) {
	tests := []struct {
		name       string
		stopReason string
		want       bool
	}{
		{name: "max_tokens", stopReason: "max_tokens", want: false},
		{name: "StopSequenceMaxTokens", stopReason: "StopSequenceMaxTokens", want: false},
		{name: "refusal", stopReason: "refusal", want: false},
		{name: "model_context_window_exceeded", stopReason: "model_context_window_exceeded", want: false},
		{name: "end_turn", stopReason: "end_turn", want: true},
		{name: "stop_sequence", stopReason: "stop_sequence", want: true},
		{name: "tool_use", stopReason: "tool_use", want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			payload := []byte(`{"type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],"stop_reason":"` + tc.stopReason + `"}`)
			if got := isCompletedExecutionPayload(payload); got != tc.want {
				t.Fatalf("isCompletedExecutionPayload(%s) = %v, want %v", payload, got, tc.want)
			}
		})
	}
}

func TestIssue7722_MessageDeltaStopReasonSemantics(t *testing.T) {
	tests := []struct {
		name           string
		stopReason     string
		wantCompleted  bool
		wantIncomplete bool
	}{
		{name: "max_tokens", stopReason: "max_tokens", wantCompleted: false, wantIncomplete: true},
		{name: "refusal", stopReason: "refusal", wantCompleted: false, wantIncomplete: true},
		{name: "model_context_window_exceeded", stopReason: "model_context_window_exceeded", wantCompleted: false, wantIncomplete: true},
		{name: "end_turn", stopReason: "end_turn", wantCompleted: true, wantIncomplete: false},
		{name: "stop_sequence", stopReason: "stop_sequence", wantCompleted: true, wantIncomplete: false},
		{name: "tool_use", stopReason: "tool_use", wantCompleted: true, wantIncomplete: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			payload := []byte(`{"type":"message_delta","delta":{"stop_reason":"` + tc.stopReason + `","stop_sequence":null}}`)
			completed, incomplete := jsonObjectCompletionFlags(payload)
			if completed != tc.wantCompleted || incomplete != tc.wantIncomplete {
				t.Fatalf("jsonObjectCompletionFlags(%s) = (completed=%v, incomplete=%v), want (%v, %v)", payload, completed, incomplete, tc.wantCompleted, tc.wantIncomplete)
			}
		})
	}
}
