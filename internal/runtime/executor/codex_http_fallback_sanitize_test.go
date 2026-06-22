package executor

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestSanitizeCodexHTTPFallbackPayloadDropsOrphanToolSearchOutput(t *testing.T) {
	payload := []byte(`{"type":"response.create","model":"gpt-5-codex","generate":false,"input":[{"type":"message","id":"msg-1"},{"type":"tool_search_output","call_id":"call-1","status":"completed","execution":"client","tools":[]},{"type":"message","id":"msg-2"}]}`)

	sanitized := sanitizeCodexHTTPFallbackPayload(payload)

	if gjson.GetBytes(sanitized, "type").Exists() {
		t.Fatalf("websocket request type leaked into HTTP fallback: %s", sanitized)
	}
	if gjson.GetBytes(sanitized, "generate").Exists() {
		t.Fatalf("generate leaked into HTTP fallback: %s", sanitized)
	}
	input := gjson.GetBytes(sanitized, "input").Array()
	if len(input) != 2 {
		t.Fatalf("input len = %d, want 2 after dropping orphan output: %s", len(input), sanitized)
	}
	if input[0].Get("id").String() != "msg-1" || input[1].Get("id").String() != "msg-2" {
		t.Fatalf("unexpected remaining input: %s", sanitized)
	}
}

func TestSanitizeCodexHTTPFallbackPayloadKeepsMatchedToolSearchPair(t *testing.T) {
	payload := []byte(`{"type":"response.create","model":"gpt-5-codex","input":[{"type":"tool_search_call","id":"tsc-1","call_id":"call-1","status":"completed","execution":"client","arguments":{"query":"tools"},"action":{"type":"unexpected"}},{"type":"tool_search_output","id":"tso-1","call_id":"call-1","status":"completed","execution":"client","tools":[],"action":{"type":"unexpected"}}]}`)

	sanitized := sanitizeCodexHTTPFallbackPayload(payload)

	input := gjson.GetBytes(sanitized, "input").Array()
	if len(input) != 2 {
		t.Fatalf("input len = %d, want 2: %s", len(input), sanitized)
	}
	if input[0].Get("type").String() != "tool_search_call" || input[1].Get("type").String() != "tool_search_output" {
		t.Fatalf("matched tool search pair was not preserved: %s", sanitized)
	}
	if input[0].Get("action").Exists() || input[1].Get("action").Exists() {
		t.Fatalf("action leaked through HTTP fallback sanitizer: %s", sanitized)
	}
}

func TestSanitizeCodexHTTPFallbackPayloadKeepsServerToolSearchOutput(t *testing.T) {
	payload := []byte(`{"type":"response.create","model":"gpt-5-codex","input":[{"type":"tool_search_output","id":"tso-1","call_id":"call-1","status":"completed","execution":"server","tools":[]},{"type":"message","id":"msg-1"}]}`)

	sanitized := sanitizeCodexHTTPFallbackPayload(payload)

	input := gjson.GetBytes(sanitized, "input").Array()
	if len(input) != 2 {
		t.Fatalf("input len = %d, want 2: %s", len(input), sanitized)
	}
	if input[0].Get("type").String() != "tool_search_output" || input[0].Get("execution").String() != "server" {
		t.Fatalf("server tool_search_output should be preserved: %s", sanitized)
	}
}
