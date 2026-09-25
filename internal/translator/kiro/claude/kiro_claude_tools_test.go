package claude

import (
	"encoding/json"
	"testing"
)

// TestRepairJSON_QuirkyButComplete verifies that RepairJSON preserves valid
// JSON that contains shell-escape characters, embedded newlines, and
// trailing-comma-free structures — i.e. the cases that historically tripped
// the heuristic and produced "input: {}" downstream.
func TestRepairJSON_QuirkyButComplete(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want map[string]interface{}
	}{
		{
			name: "shell command with embedded quotes",
			in:   `{"command":"echo \"hello world\""}`,
			want: map[string]interface{}{"command": "echo \"hello world\""},
		},
		{
			name: "shell command with literal newline",
			in:   "{\"command\":\"echo a\\necho b\"}",
			want: map[string]interface{}{"command": "echo a\necho b"},
		},
		{
			name: "bash with description and timeout",
			in:   `{"command":"ls -la","description":"list files","timeout":5000}`,
			want: map[string]interface{}{
				"command":     "ls -la",
				"description": "list files",
				"timeout":     float64(5000),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repaired := RepairJSON(tc.in)
			var got map[string]interface{}
			if err := json.Unmarshal([]byte(repaired), &got); err != nil {
				t.Fatalf("RepairJSON output failed to parse: %v\nrepaired: %s", err, repaired)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d keys, want %d (got=%v want=%v)", len(got), len(tc.want), got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("key %q: got %v (%T), want %v (%T)", k, got[k], got[k], v, v)
				}
			}
		})
	}
}

// TestRecoverPartialInput_Empty confirms the fallback helper returns a
// non-nil empty map when nothing can be salvaged, instead of nil (which
// would marshal to JSON null).
func TestRecoverPartialInput_Empty(t *testing.T) {
	got := RecoverPartialInput(nil)
	if got == nil {
		t.Fatal("RecoverPartialInput(nil) returned nil; want empty map")
	}
	if len(got) != 0 {
		t.Fatalf("RecoverPartialInput(nil) returned %d keys; want 0", len(got))
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b) != "{}" {
		t.Fatalf("marshal output = %s, want {}", string(b))
	}
}

// TestRecoverPartialInput_PreservesFields verifies that when extractPartialFields
// finds recoverable fields (e.g. "command" prefix), they survive the type
// conversion into the format BuildClaudeResponse emits as tool_use input.
func TestRecoverPartialInput_PreservesFields(t *testing.T) {
	partial := map[string]string{
		"command":     "echo hello",
		"description": "test command",
	}
	got := RecoverPartialInput(partial)
	if got["command"] != "echo hello" {
		t.Errorf("command lost in conversion: got %v", got["command"])
	}
	if got["description"] != "test command" {
		t.Errorf("description lost in conversion: got %v", got["description"])
	}
}

// TestProcessToolUseEvent_RepairFailureRecoversPartial is the regression test
// for the user-reported bug: kiro streams a tool_use buffer that fails
// RepairJSON+Unmarshal, and the tool_use block is emitted with input: {} —
// causing the downstream SDK to reject the call with
// "Invalid args for tool \"Bash\": must have required property 'command'".
//
// The fix replaces the silent empty-map fallback with extractPartialFields
// recovery. This test feeds a stream whose final buffer is unparseable but
// still contains a recognizable "command" field.
func TestProcessToolUseEvent_RepairFailureRecoversPartial(t *testing.T) {
	processed := map[string]bool{}

	// Start the tool use with a complete first chunk.
	startEvent := map[string]interface{}{
		"toolUseEvent": map[string]interface{}{
			"toolUseId": "tu_test_1",
			"name":      "Bash",
			"stop":      false,
			"input":     `{"command":`,
		},
	}
	_, state := ProcessToolUseEvent(startEvent, nil, processed)
	if state == nil {
		t.Fatal("expected non-nil state from tool use start")
	}
	if state.ToolUseID != "tu_test_1" {
		t.Fatalf("state.ToolUseID = %s, want tu_test_1", state.ToolUseID)
	}

	// Append fragments. The cumulative buffer becomes an unparseable JSON
	// (truncated mid-key), simulating the upstream failure mode that
	// historically led to input: {}.
	state.InputBuffer.WriteString(`"echo a; echo b","descrip`)

	stopEvent := map[string]interface{}{
		"toolUseEvent": map[string]interface{}{
			"toolUseId": "tu_test_1",
			"name":      "Bash",
			"stop":      true,
		},
	}
	results, finalState := ProcessToolUseEvent(stopEvent, state, processed)
	if finalState != nil {
		t.Errorf("expected nil state after stop, got %+v", finalState)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}

	tu := results[0]
	if tu.Name != "Bash" {
		t.Errorf("name = %s, want Bash", tu.Name)
	}
	// The critical assertion: input MUST NOT be empty for a tool with required
	// fields, otherwise the downstream SDK rejects the call.
	if tu.Input == nil {
		t.Fatal("input is nil — should be empty map or partial fields")
	}
	if _, hasCmd := tu.Input["command"]; !hasCmd {
		t.Fatalf("input missing 'command' field — SDK will reject the call. got=%v", tu.Input)
	}
}

// TestProcessToolUseEvent_MergesFragmentsWithCompleteObject is the regression
// test for the 7.5.6 fix: when a Kiro stream sends both accumulated input
// fragments and a complete input object for the same tool use, the object
// must be merged over the repaired fragments (object wins conflicts) instead
// of resetting the buffer — the old reset dropped fragment-only fields and
// tripped "TRUNCATION DETECTED" downstream.
func TestProcessToolUseEvent_MergesFragmentsWithCompleteObject(t *testing.T) {
	processed := map[string]bool{}

	// Accumulate a fragment that dies mid-string-value.
	startEvent := map[string]interface{}{
		"toolUseEvent": map[string]interface{}{
			"toolUseId": "tu_merge_1",
			"name":      "Write",
			"stop":      false,
			"input":     `{"file_path": "/tmp/partial`,
		},
	}
	_, state := ProcessToolUseEvent(startEvent, nil, processed)
	if state == nil {
		t.Fatal("expected non-nil state from tool use start")
	}

	// Complete object arrives on the stop event; it must be merged, not
	// replace the buffer blindly (the pre-7.5.6 behavior that dropped
	// fragment-only fields).
	stopEvent := map[string]interface{}{
		"toolUseEvent": map[string]interface{}{
			"toolUseId": "tu_merge_1",
			"name":      "Write",
			"stop":      true,
			"input": map[string]interface{}{
				"file_path": "/tmp/final",
				"content":   "body",
			},
		},
	}
	results, finalState := ProcessToolUseEvent(stopEvent, state, processed)
	if finalState != nil {
		t.Errorf("expected nil state after stop, got %+v", finalState)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}

	tu := results[0]
	if tu.IsTruncated {
		t.Errorf("merged input must not be flagged truncated; input=%v", tu.Input)
	}
	if tu.Input["file_path"] != "/tmp/final" {
		t.Errorf("object value must win on conflict, got file_path=%v", tu.Input["file_path"])
	}
	if tu.Input["content"] != "body" {
		t.Errorf("object fields must survive the merge, got input=%v", tu.Input)
	}
}

// TestProcessToolUseEvent_IgnoresFragmentAfterCompleteObject guards the
// reverse event order: a fragment arriving AFTER the complete object must
// not be appended raw onto the finalized buffer, or the stop-time parse
// fails and the truncation symptom reappears through the other order.
func TestProcessToolUseEvent_IgnoresFragmentAfterCompleteObject(t *testing.T) {
	processed := map[string]bool{}

	// The complete object arrives first (empty-buffer fast path).
	startEvent := map[string]interface{}{
		"toolUseEvent": map[string]interface{}{
			"toolUseId": "tu_order_1",
			"name":      "Write",
			"stop":      false,
			"input": map[string]interface{}{
				"file_path": "/tmp/final",
				"content":   "body",
			},
		},
	}
	_, state := ProcessToolUseEvent(startEvent, nil, processed)
	if state == nil {
		t.Fatal("expected non-nil state from tool use start")
	}

	// A trailing fragment shows up afterwards (stop-event fragmentation or
	// client redelivery). It must be ignored, not appended onto the
	// finalized JSON.
	trailingEvent := map[string]interface{}{
		"toolUseEvent": map[string]interface{}{
			"toolUseId": "tu_order_1",
			"name":      "Write",
			"stop":      false,
			"input":     `, "extra": "junk`,
		},
	}
	_, state = ProcessToolUseEvent(trailingEvent, state, processed)
	if state == nil {
		t.Fatal("expected state to survive the trailing fragment")
	}

	stopEvent := map[string]interface{}{
		"toolUseEvent": map[string]interface{}{
			"toolUseId": "tu_order_1",
			"name":      "Write",
			"stop":      true,
		},
	}
	results, finalState := ProcessToolUseEvent(stopEvent, state, processed)
	if finalState != nil {
		t.Errorf("expected nil state after stop, got %+v", finalState)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}

	tu := results[0]
	if tu.IsTruncated {
		t.Errorf("input must not be flagged truncated; input=%v", tu.Input)
	}
	if tu.Input["file_path"] != "/tmp/final" {
		t.Errorf("file_path corrupted by trailing fragment, got %v", tu.Input["file_path"])
	}
	if tu.Input["content"] != "body" {
		t.Errorf("content corrupted by trailing fragment, got input=%v", tu.Input)
	}
	if _, hasExtra := tu.Input["extra"]; hasExtra {
		t.Errorf("trailing fragment leaked into input: %v", tu.Input)
	}
}
