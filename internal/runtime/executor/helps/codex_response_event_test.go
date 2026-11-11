package helps

import "testing"

func TestCodexResponseEventType(t *testing.T) {
	for _, tc := range []struct{ payload, want string }{
		{`{"error":{"code":"bio_policy"}}`, "error"},
		{`{"body":{"error":"blocked"}}`, "error"},
		{`{"response":{"error":{"code":"bio_policy"}}}`, "error"},
		{`{"code":"bio_policy","message":"blocked"}`, "error"},
		{`{"error":null}`, ""},
		{`{"error":" "}`, ""},
		{`{"body":{"error":null}}`, ""},
		{`{"response":{"error":null,"status":"completed"}}`, ""},
		{`{"message":"This content was flagged for possible biological risk."}`, ""},
		{`{"type":"response.output_text.delta","delta":"blocked","error":{"code":"bio_policy"}}`, "response.output_text.delta"},
		{`{"type":"response.metadata","body":{"error":{"code":"bio_policy"}}}`, "response.metadata"},
		{`{"error":{"code":"bio_policy"}}` + "trailing data", ""},
	} {
		t.Run(tc.payload, func(t *testing.T) {
			if got := CodexResponseEventType([]byte(tc.payload)); got != tc.want {
				t.Fatalf("type = %q, want %q", got, tc.want)
			}
		})
	}
}
