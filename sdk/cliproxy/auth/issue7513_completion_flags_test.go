package auth

import "testing"

func TestIssue7513_JSONObjectCompletionFlagsRejectsGeminiAbnormalFinishes(t *testing.T) {
	for _, reason := range []string{"MALFORMED_FUNCTION_CALL", "UNEXPECTED_TOOL_CALL", "MALFORMED_RESPONSE"} {
		t.Run(reason, func(t *testing.T) {
			payload := []byte(`{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"` + reason + `"}]}`)
			completed, incomplete := jsonObjectCompletionFlags(payload)
			if completed || !incomplete {
				t.Fatalf("jsonObjectCompletionFlags(%s) = (completed=%v, incomplete=%v), want (false, true)", reason, completed, incomplete)
			}
			if isCompletedExecutionPayload(payload) {
				t.Fatalf("isCompletedExecutionPayload(%s) = true, want false", reason)
			}
		})
	}
}
