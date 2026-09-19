package cliproxy

import "testing"

// The two fragments below are the confirmed wire shapes for the cache-diagnosis
// beta: diagnostics sits beside usage in a non-streaming body, and the identical
// object rides inside the message_start event when streaming.
const nonStreamingDiagnosticsBody = `{
  "id": "msg_01",
  "type": "message",
  "role": "assistant",
  "content": [{"type": "text", "text": "ok"}],
  "usage": {"input_tokens": 3, "cache_read_input_tokens": 0, "cache_creation_input_tokens": 25154},
  "diagnostics": {"cache_miss_reason": {"type": "messages_changed", "cache_missed_input_tokens": 25154}}
}`

const streamingDiagnosticsBody = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"msg_01","type":"message","role":"assistant","content":[],` +
	`"usage":{"input_tokens":3,"cache_read_input_tokens":0,"cache_creation_input_tokens":25154},` +
	`"diagnostics":{"cache_miss_reason":{"type":"messages_changed","cache_missed_input_tokens":25154}}}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}` + "\n\n"

func TestClaudeCacheMissReasonNonStreaming(t *testing.T) {
	reason, tokens := claudeCacheMissReason([]byte(nonStreamingDiagnosticsBody))
	if reason != "messages_changed" {
		t.Fatalf("reason = %q, want messages_changed", reason)
	}
	if tokens != 25154 {
		t.Fatalf("cache_missed_input_tokens = %d, want 25154", tokens)
	}
}

func TestClaudeCacheMissReasonStreaming(t *testing.T) {
	reason, tokens := claudeCacheMissReason([]byte(streamingDiagnosticsBody))
	if reason != "messages_changed" {
		t.Fatalf("reason = %q, want messages_changed", reason)
	}
	if tokens != 25154 {
		t.Fatalf("cache_missed_input_tokens = %d, want 25154", tokens)
	}
}

func TestClaudeCacheMissReasonBareMessageStartObject(t *testing.T) {
	body := `{"type":"message_start","message":{"diagnostics":{"cache_miss_reason":{"type":"tools_changed","cache_missed_input_tokens":42}}}}`
	reason, tokens := claudeCacheMissReason([]byte(body))
	if reason != "tools_changed" || tokens != 42 {
		t.Fatalf("reason = %q tokens = %d", reason, tokens)
	}
}

func TestClaudeCacheMissReasonAbsent(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "a clean hit carries no diagnostics", body: `{"usage":{"cache_read_input_tokens":161937}}`},
		{name: "empty body", body: ``},
		{name: "not json and not sse", body: `garbage`},
		{name: "sse without message_start", body: "data: {\"type\":\"ping\"}\n\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason, tokens := claudeCacheMissReason([]byte(tt.body))
			if reason != "" || tokens != 0 {
				t.Fatalf("reason = %q tokens = %d, want empty", reason, tokens)
			}
		})
	}
}
