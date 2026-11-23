package helps

import (
	"strings"
	"testing"
)

func TestSSEJSONAssembler_SingleLineAndMultilineWithControlLines(t *testing.T) {
	asm := NewSSEJSONAssembler(4096)

	single := asm.Push([]byte(`data: {"response":{"ok":true}}`))
	if string(single) != `{"response":{"ok":true}}` {
		t.Fatalf("single line payload = %q, want %q", string(single), `{"response":{"ok":true}}`)
	}

	lines := []string{
		`data: {"response":{"candidates":[{"content":{"role":"model",`,
		": keepalive",
		"   ",
		"\t",
		"event: message",
		"id: 99",
		"retry: 3000",
		"custom: ignored",
		"data:",
		`data: "parts":[{"text":"ok"}]}}]}}`,
	}
	var got []byte
	for i, line := range lines {
		got = asm.Push([]byte(line))
		if i < len(lines)-1 && got != nil {
			t.Fatalf("line %d returned premature payload %q", i, string(got))
		}
	}
	if got == nil || !strings.Contains(string(got), `"text":"ok"`) {
		t.Fatalf("reassembled payload = %q, want valid JSON containing ok", string(got))
	}
}

func TestSSEJSONAssembler_ResetsOnlyOnGenuineEmptyLineAndDataDone(t *testing.T) {
	asm := NewSSEJSONAssembler(4096)

	// Whitespace-only lines (spaces or multiple CRs) must NOT reset an in-progress multiline event.
	if got := asm.Push([]byte(`data: {"error":{"code":429,`)); got != nil {
		t.Fatalf("line 1 returned %q, want nil", string(got))
	}
	if got := asm.Push([]byte("   ")); got != nil {
		t.Fatalf("whitespace-only line returned %q, want nil", string(got))
	}
	if got := asm.Push([]byte("\r\r")); got != nil {
		t.Fatalf("multi-CR line returned %q, want nil", string(got))
	}
	errPayload := asm.Push([]byte(`data: "status":"RESOURCE_EXHAUSTED"}}`))
	if errPayload == nil || !strings.Contains(string(errPayload), `"code":429`) {
		t.Fatalf("multiline payload across whitespace-only line = %q, want valid 429 JSON", string(errPayload))
	}

	// Genuine empty line terminates an SSE event and resets truncated pending state.
	if got := asm.Push([]byte(`data: {"bad"`)); got != nil {
		t.Fatalf("truncated line returned %q, want nil", string(got))
	}
	if got := asm.Push([]byte("")); got != nil {
		t.Fatalf("empty line returned %q, want nil", string(got))
	}
	valid := asm.Push([]byte(`data: {"response":{"ok":1}}`))
	if string(valid) != `{"response":{"ok":1}}` {
		t.Fatalf("post-empty payload = %q, want %q", string(valid), `{"response":{"ok":1}}`)
	}

	// CR-only line also terminates an SSE event.
	if got := asm.Push([]byte(`data: {"bad2"`)); got != nil {
		t.Fatalf("truncated line returned %q, want nil", string(got))
	}
	if got := asm.Push([]byte("\r")); got != nil {
		t.Fatalf("CR-only line returned %q, want nil", string(got))
	}
	validCR := asm.Push([]byte(`data: {"response":{"ok":2}}`))
	if string(validCR) != `{"response":{"ok":2}}` {
		t.Fatalf("post-CR payload = %q, want %q", string(validCR), `{"response":{"ok":2}}`)
	}

	// Truncated frame followed by data: [DONE] must also reset pending state.
	if got := asm.Push([]byte(`data: {"truncated"`)); got != nil {
		t.Fatalf("truncated line returned %q, want nil", string(got))
	}
	if got := asm.Push([]byte("data: [DONE]")); got != nil {
		t.Fatalf("[DONE] line returned %q, want nil", string(got))
	}
	validDone := asm.Push([]byte(`data: {"response":{"ok":3}}`))
	if string(validDone) != `{"response":{"ok":3}}` {
		t.Fatalf("post-DONE payload = %q, want %q", string(validDone), `{"response":{"ok":3}}`)
	}
}

func TestSSEJSONAssembler_EnforcesMaxBytesCapAndDiscardsEventTailUntilEmptyLine(t *testing.T) {
	asm := NewSSEJSONAssembler(32)

	// Single oversized malformed SSE line must discard the remainder of the
	// same SSE event (even if a tail data: line happens to be valid JSON)
	// until the next genuine empty line.
	if got := asm.Push([]byte(`data: {"oversized":"` + strings.Repeat("x", 40))); got != nil {
		t.Fatalf("oversized malformed line returned %q, want nil", string(got))
	}
	if got := asm.Push([]byte(`data: {"error":{"code":503}}`)); got != nil {
		t.Fatalf("tail of oversized event returned %q, want nil (must discard until empty line)", string(got))
	}
	// Whitespace-only lines (spaces or multiple CRs) do not end the discarded event.
	if got := asm.Push([]byte("   ")); got != nil {
		t.Fatalf("whitespace-only line inside discarded event returned %q, want nil", string(got))
	}
	if got := asm.Push([]byte("\r\r")); got != nil {
		t.Fatalf("multi-CR line inside discarded event returned %q, want nil", string(got))
	}
	if got := asm.Push([]byte(`data: {"error":{"code":500}}`)); got != nil {
		t.Fatalf("tail after whitespace-only/multi-CR lines returned %q, want nil", string(got))
	}
	if got := asm.Push([]byte("")); got != nil {
		t.Fatalf("empty boundary line returned %q, want nil", string(got))
	}
	valid := asm.Push([]byte(`data: {"ok":true}`))
	if string(valid) != `{"ok":true}` {
		t.Fatalf("after empty line boundary got %q, want %q", string(valid), `{"ok":true}`)
	}

	// Multi-line SSE growth exceeding maxBytes must also discard the rest of
	// that event until the next genuine empty line.
	if got := asm.Push([]byte(`data: {"a":"` + strings.Repeat("a", 12))); got != nil {
		t.Fatalf("first fragment returned %q, want nil", string(got))
	}
	if got := asm.Push([]byte("data: " + strings.Repeat("b", 30))); got != nil {
		t.Fatalf("overflowing fragment returned %q, want nil", string(got))
	}
	if got := asm.Push([]byte(`data: {"error":{"code":503}}`)); got != nil {
		t.Fatalf("tail after multiline overflow returned %q, want nil", string(got))
	}
	if got := asm.Push([]byte("")); got != nil {
		t.Fatalf("empty boundary line returned %q, want nil", string(got))
	}
	valid2 := asm.Push([]byte(`data: {"ok":2}`))
	if string(valid2) != `{"ok":2}` {
		t.Fatalf("after multiline overflow boundary got %q, want %q", string(valid2), `{"ok":2}`)
	}
}

func TestSSEJSONAssembler_RawMultilineJSONAndNilReceiver(t *testing.T) {
	asm := NewSSEJSONAssembler(4096)

	if got := asm.Push([]byte("{")); got != nil {
		t.Fatalf("line 1 returned %q, want nil", string(got))
	}
	if got := asm.Push([]byte(": keepalive")); got != nil {
		t.Fatalf("comment line returned %q, want nil", string(got))
	}
	if got := asm.Push([]byte(`"error":{"code":500}`)); got != nil {
		t.Fatalf("line 2 returned %q, want nil", string(got))
	}
	got := asm.Push([]byte("}"))
	if got == nil || !strings.Contains(string(got), `"code":500`) {
		t.Fatalf("raw multiline JSON = %q, want complete error JSON", string(got))
	}

	var nilAsm *SSEJSONAssembler
	nilAsm.Reset()
	if p := nilAsm.Push([]byte(`data: {"nil":true}`)); string(p) != `{"nil":true}` {
		t.Fatalf("nil receiver Push = %q, want %q", string(p), `{"nil":true}`)
	}
}
