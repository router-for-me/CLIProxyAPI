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
