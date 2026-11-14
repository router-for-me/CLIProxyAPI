package helps

import "testing"

func TestProviderErrorPayloadRequiresParsedErrorDetails(t *testing.T) {
	for _, tc := range []struct {
		payload string
		want    bool
	}{
		{`{"error":{"message":"overloaded"}}`, true},
		{`{"type":"error","error":{"type":"overloaded_error"}}`, true},
		{`{"error":{"code":503,"status":"UNAVAILABLE"}}`, true},
		{`{"response":{"error":{"code":"capacity"}}}`, true},
		{`{"code":503,"message":"unavailable"}`, true},
		{`{"error":"refused"}`, true},
		{`{"type":"error","message":"provider overloaded"}`, true},
		{`{"type":"error","code":"overloaded"}`, true},
		{`{"type":"error","error_type":"overloaded"}`, true},
		{`{"message":"ordinary content"}`, false},
		{`{"error":{"message":"truncated"}`, false},
		{`{"type":"error"}`, false},
		{`{"error":{}}`, false},
		{`{"error":{"message":42,"type":7}}`, false},
		{`{"code":"","message":"unclassified"}`, false},
		{`{"error":{"code":{},"message":[]}}`, false},
		{`{"code":{},"message":"bad"}`, false},
		{`{"candidates":[{"finishReason":"STOP"}]}`, false},
		{`Invalid apply_patch tool arguments received from upstream.`, false},
	} {
		if got := IsProviderErrorPayload([]byte(tc.payload)); got != tc.want {
			t.Errorf("payload=%s providerError=%t, want %t", tc.payload, got, tc.want)
		}
	}
}

func TestProviderErrorEventContextIsRequiredForBareDetails(t *testing.T) {
	payload := []byte(`{"message":"provider overloaded"}`)
	if !IsProviderErrorEventPayload("error", payload) || IsProviderErrorEventPayload("message", payload) {
		t.Fatal("top-level message lost its explicit provider event context")
	}
	if IsProviderErrorEventPayload("error", []byte(`{"message":"truncated"`)) {
		t.Fatal("error event name granted replay to malformed JSON")
	}
}
