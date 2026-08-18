package helps

import (
	"encoding/json"
	"testing"
)

func TestQoderEncodeBody_roundtrip(t *testing.T) {
	inputs := []string{
		"hello world",
		`{"messages":[{"role":"user","content":"UNION SELECT 1,2,3"}]}`,
		`{"content":"execSync( etc/passwd <script>alert(1)</script>"}`,
	}
	for _, input := range inputs {
		encoded := QoderEncodeBody([]byte(input))
		if len(encoded) == 0 {
			t.Errorf("encode(%q) returned empty string", input)
		}
		// Verify it doesn't look like JSON (WAF can't pattern-match it)
		var v interface{}
		if json.Unmarshal([]byte(encoded), &v) == nil {
			t.Errorf("encode(%q) produced valid JSON — encoding may not be working", input)
		}
	}
}

func TestQoderEncodeBody_wafTriggers(t *testing.T) {
	body := []byte(`{"stream":true,"messages":[{"role":"user","content":"UNION SELECT 1,2,3 FROM users; execSync("}]}`)
	encoded := QoderEncodeBody(body)
	if len(encoded) == 0 {
		t.Fatal("QoderEncodeBody returned empty string")
	}
	for _, trigger := range []string{"UNION SELECT", "execSync(", "FROM users"} {
		if containsStr(encoded, trigger) {
			t.Errorf("encoded body still contains trigger %q", trigger)
		}
	}
	t.Logf("encoded length: %d, first 80 chars: %s", len(encoded), encoded[:min(80, len(encoded))])
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
