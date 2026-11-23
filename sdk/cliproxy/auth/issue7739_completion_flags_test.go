package auth

import "testing"

func TestIssue7739_JSONObjectCompletionFlags_ResponseCandidatesEnvelope(t *testing.T) {
	tests := []struct {
		name           string
		payload        string
		wantCompleted  bool
		wantIncomplete bool
		wantExecOK     bool
	}{
		{
			name:           "response candidates STOP",
			payload:        `{"response":{"candidates":[{"finishReason":"STOP"}]}}`,
			wantCompleted:  true,
			wantIncomplete: false,
			wantExecOK:     true,
		},
		{
			name:           "response candidates MAX_TOKENS",
			payload:        `{"response":{"candidates":[{"finishReason":"MAX_TOKENS"}]}}`,
			wantCompleted:  false,
			wantIncomplete: true,
			wantExecOK:     false,
		},
		{
			name:           "response candidates SAFETY",
			payload:        `{"response":{"candidates":[{"finishReason":"SAFETY"}]}}`,
			wantCompleted:  false,
			wantIncomplete: true,
			wantExecOK:     false,
		},
		{
			name:           "response candidates without finishReason",
			payload:        `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"partial"}]}}]}}`,
			wantCompleted:  false,
			wantIncomplete: false,
			wantExecOK:     false,
		},
		{
			name:           "response candidates STOP with response error stays incomplete",
			payload:        `{"response":{"error":{"code":500,"message":"boom"},"candidates":[{"finishReason":"STOP"}]}}`,
			wantCompleted:  true,
			wantIncomplete: true,
			wantExecOK:     false,
		},
		{
			name:           "response candidates STOP with incomplete_details stays incomplete",
			payload:        `{"response":{"incomplete_details":{"reason":"max_output_tokens"},"candidates":[{"finishReason":"STOP"}]}}`,
			wantCompleted:  true,
			wantIncomplete: true,
			wantExecOK:     false,
		},
		{
			name:           "response candidates mixed STOP and PROHIBITED_CONTENT",
			payload:        `{"response":{"candidates":[{"finishReason":"STOP"},{"finishReason":"PROHIBITED_CONTENT"}]}}`,
			wantCompleted:  true,
			wantIncomplete: true,
			wantExecOK:     false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(tc.payload)
			completed, incomplete := jsonObjectCompletionFlags(raw)
			if completed != tc.wantCompleted || incomplete != tc.wantIncomplete {
				t.Fatalf("jsonObjectCompletionFlags(%s) = (completed=%v, incomplete=%v), want (%v, %v)", tc.payload, completed, incomplete, tc.wantCompleted, tc.wantIncomplete)
			}
			if gotExec := isCompletedExecutionPayload(raw); gotExec != tc.wantExecOK {
				t.Fatalf("isCompletedExecutionPayload(%s) = %v, want %v", tc.payload, gotExec, tc.wantExecOK)
			}
			sse := []byte("data: " + tc.payload + "\n\n")
			streamCompleted, streamIncomplete := streamPayloadCompletionFlags(sse)
			if streamCompleted != tc.wantCompleted || streamIncomplete != tc.wantIncomplete {
				t.Fatalf("streamPayloadCompletionFlags(%s) = (completed=%v, incomplete=%v), want (%v, %v)", tc.payload, streamCompleted, streamIncomplete, tc.wantCompleted, tc.wantIncomplete)
			}
		})
	}
}

func TestIssue7739_JSONObjectCompletionFlags_GeminiFailureFinishReasons(t *testing.T) {
	reasons := []string{
		"MAX_TOKENS",
		"SAFETY",
		"RECITATION",
		"OTHER",
		"MALFORMED_FUNCTION_CALL",
		"UNEXPECTED_TOOL_CALL",
		"MALFORMED_RESPONSE",
		"PROHIBITED_CONTENT",
		"BLOCKLIST",
		"SPII",
		"IMAGE_SAFETY",
		"LANGUAGE",
		"NO_IMAGE",
	}

	for _, reason := range reasons {
		t.Run("top_level/"+reason, func(t *testing.T) {
			payload := []byte(`{"candidates":[{"finishReason":"` + reason + `"}]}`)
			completed, incomplete := jsonObjectCompletionFlags(payload)
			if completed || !incomplete {
				t.Fatalf("jsonObjectCompletionFlags(%s) = (completed=%v, incomplete=%v), want (false, true)", reason, completed, incomplete)
			}
			if isCompletedExecutionPayload(payload) {
				t.Fatalf("isCompletedExecutionPayload(%s) = true, want false", reason)
			}
		})

		t.Run("response_envelope/"+reason, func(t *testing.T) {
			payload := []byte(`{"response":{"candidates":[{"finishReason":"` + reason + `"}]}}`)
			completed, incomplete := jsonObjectCompletionFlags(payload)
			if completed || !incomplete {
				t.Fatalf("jsonObjectCompletionFlags(response.%s) = (completed=%v, incomplete=%v), want (false, true)", reason, completed, incomplete)
			}
			if isCompletedExecutionPayload(payload) {
				t.Fatalf("isCompletedExecutionPayload(response.%s) = true, want false", reason)
			}
		})
	}
}
