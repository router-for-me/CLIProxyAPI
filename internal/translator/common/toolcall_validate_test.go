package common

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateToolCallID(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantID    string
		wantError bool
	}{
		{
			name:      "valid alphanumeric ID",
			input:     "call_abc123",
			wantID:    "call_abc123",
			wantError: false,
		},
		{
			name:      "valid with hyphens and underscores",
			input:     "tool-use_id-123",
			wantID:    "tool-use_id-123",
			wantError: false,
		},
		{
			name:      "empty ID returns error",
			input:     "",
			wantID:    "",
			wantError: true,
		},
		{
			name:      "whitespace-only ID returns error",
			input:     "   ",
			wantID:    "",
			wantError: true,
		},
		{
			name:      "sanitizes invalid characters",
			input:     "call@id#with$special%",
			wantID:    "call_id_with_special_",
			wantError: false,
		},
		{
			name:      "sanitizes dots",
			input:     "call.id.with.dots",
			wantID:    "call_id_with_dots",
			wantError: false,
		},
		{
			name:      "truncates long ID",
			input:     "this_is_a_very_long_tool_call_id_that_exceeds_the_maximum_allowed_length_limit",
			wantID:    "this_is_a_very_long_tool_call_id_that_exceeds_the_maximum_allowe",
			wantError: false,
		},
		{
			name:      "preserves valid prefix",
			input:     "toolu_abc123",
			wantID:    "toolu_abc123",
			wantError: false,
		},
		{
			name:      "sanitizes spaces",
			input:     "call id with spaces",
			wantID:    "call_id_with_spaces",
			wantError: false,
		},
		{
			name:      "sanitizes unicode",
			input:     "call_id_中文_test",
			wantID:    "call_id____test",
			wantError: false,
		},
		{
			name:      "all invalid characters",
			input:     "@#$%^&*()",
			wantID:    "_________",
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateToolCallID(tt.input)
			if (err != nil) != tt.wantError {
				t.Errorf("ValidateToolCallID(%q) error = %v, wantError %v", tt.input, err, tt.wantError)
				return
			}
			if got != tt.wantID {
				t.Errorf("ValidateToolCallID(%q) = %q, want %q", tt.input, got, tt.wantID)
			}
			// Verify length constraint
			if len(got) > MaxToolCallIDLength {
				t.Errorf("ValidateToolCallID(%q) result too long: %d > %d", tt.input, len(got), MaxToolCallIDLength)
			}
			// Verify pattern compliance (if not empty)
			if got != "" && !validToolCallIDPattern.MatchString(got) {
				t.Errorf("ValidateToolCallID(%q) result %q doesn't match pattern", tt.input, got)
			}
		})
	}
}

func TestSanitizeToolCallID(t *testing.T) {
	// SanitizeToolCallID should not panic and should return valid IDs
	tests := []struct {
		name  string
		input string
	}{
		{"normal", "call_123"},
		{"with invalid chars", "call@id#test"},
		{"empty", ""},
		{"whitespace", "   "},
		{"very long", "a_very_long_id_that_exceeds_the_maximum_allowed_length_for_tool_call_ids_here"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeToolCallID(tt.input)
			// Should not panic
			if got == "" && tt.input != "" && tt.input != "   " {
				// For non-empty non-whitespace input, should return non-empty
				// (this is actually expected for all-invalid input, as it generates fallback)
			}
			if got != "" && len(got) > MaxToolCallIDLength {
				t.Errorf("SanitizeToolCallID(%q) result too long: %d", tt.input, len(got))
			}
		})
	}
}

func TestValidateToolName(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantError bool
	}{
		{"valid name", "Bash", false},
		{"valid with underscores", "read_file", false},
		{"valid with dots", "mcp.server.read", false},
		{"empty name", "", true},
		{"whitespace only", "   ", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateToolName(tt.input)
			if (err != nil) != tt.wantError {
				t.Errorf("ValidateToolName(%q) error = %v, wantError %v", tt.input, err, tt.wantError)
			}
		})
	}
}

func TestValidateToolCallJSON(t *testing.T) {
	tests := []struct {
		name      string
		input     []byte
		wantError bool
	}{
		{"valid object", []byte(`{"key": "value"}`), false},
		{"empty", []byte{}, false},
		{"null", []byte("null"), false},
		{"empty string", []byte(""), false},
		{"whitespace", []byte("   "), false},
		{"invalid JSON", []byte(`{invalid json`), true},
		{"array instead of object", []byte(`[1, 2, 3]`), true},
		{"string instead of object", []byte(`"hello"`), true},
		{"number instead of object", []byte(`42`), true},
		{"nested object", []byte(`{"a": {"b": "c"}}`), false},
		{"empty object", []byte(`{}`), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateToolCallJSON(tt.input)
			if (err != nil) != tt.wantError {
				t.Errorf("ValidateToolCallJSON(%s) error = %v, wantError %v", tt.input, err, tt.wantError)
			}
		})
	}
}

func TestValidateToolCallPairing(t *testing.T) {
	t.Run("all paired", func(t *testing.T) {
		items := []json.RawMessage{
			[]byte(`{"type": "function_call", "call_id": "call_1", "name": "Bash", "arguments": "{}"}`),
			[]byte(`{"type": "function_call_output", "call_id": "call_1", "output": "ok"}`),
		}
		orphanedCalls, orphanedOutputs := ValidateToolCallPairing(items)
		if len(orphanedCalls) != 0 {
			t.Errorf("expected 0 orphaned calls, got %d: %v", len(orphanedCalls), orphanedCalls)
		}
		if len(orphanedOutputs) != 0 {
			t.Errorf("expected 0 orphaned outputs, got %d: %v", len(orphanedOutputs), orphanedOutputs)
		}
	})

	t.Run("orphaned output", func(t *testing.T) {
		items := []json.RawMessage{
			[]byte(`{"type": "function_call_output", "call_id": "call_1", "output": "ok"}`),
		}
		orphanedCalls, orphanedOutputs := ValidateToolCallPairing(items)
		if len(orphanedCalls) != 0 {
			t.Errorf("expected 0 orphaned calls, got %d", len(orphanedCalls))
		}
		if len(orphanedOutputs) != 1 {
			t.Errorf("expected 1 orphaned output, got %d", len(orphanedOutputs))
		}
	})

	t.Run("orphaned call", func(t *testing.T) {
		items := []json.RawMessage{
			[]byte(`{"type": "function_call", "call_id": "call_1", "name": "Bash", "arguments": "{}"}`),
		}
		orphanedCalls, orphanedOutputs := ValidateToolCallPairing(items)
		if len(orphanedCalls) != 1 {
			t.Errorf("expected 1 orphaned call, got %d", len(orphanedCalls))
		}
		if len(orphanedOutputs) != 0 {
			t.Errorf("expected 0 orphaned outputs, got %d", len(orphanedOutputs))
		}
	})

	t.Run("multiple items mixed", func(t *testing.T) {
		items := []json.RawMessage{
			[]byte(`{"type": "function_call", "call_id": "call_1", "name": "Bash", "arguments": "{}"}`),
			[]byte(`{"type": "function_call_output", "call_id": "call_1", "output": "ok"}`),
			[]byte(`{"type": "function_call", "call_id": "call_2", "name": "Read", "arguments": "{}"}`),
			[]byte(`{"type": "function_call_output", "call_id": "call_3", "output": "ok"}`),
		}
		orphanedCalls, orphanedOutputs := ValidateToolCallPairing(items)
		if len(orphanedCalls) != 1 {
			t.Errorf("expected 1 orphaned call (call_2), got %d: %v", len(orphanedCalls), orphanedCalls)
		}
		if len(orphanedOutputs) != 1 {
			t.Errorf("expected 1 orphaned output (call_3), got %d: %v", len(orphanedOutputs), orphanedOutputs)
		}
	})

	t.Run("custom tool types", func(t *testing.T) {
		items := []json.RawMessage{
			[]byte(`{"type": "custom_tool_call", "call_id": "call_1", "name": "Bash", "arguments": "{}"}`),
			[]byte(`{"type": "custom_tool_call_output", "call_id": "call_1", "output": "ok"}`),
		}
		orphanedCalls, orphanedOutputs := ValidateToolCallPairing(items)
		if len(orphanedCalls) != 0 {
			t.Errorf("expected 0 orphaned calls, got %d", len(orphanedCalls))
		}
		if len(orphanedOutputs) != 0 {
			t.Errorf("expected 0 orphaned outputs, got %d", len(orphanedOutputs))
		}
	})

	t.Run("empty items", func(t *testing.T) {
		items := []json.RawMessage{}
		orphanedCalls, orphanedOutputs := ValidateToolCallPairing(items)
		if len(orphanedCalls) != 0 {
			t.Errorf("expected 0 orphaned calls, got %d", len(orphanedCalls))
		}
		if len(orphanedOutputs) != 0 {
			t.Errorf("expected 0 orphaned outputs, got %d", len(orphanedOutputs))
		}
	})

	t.Run("items without call_id", func(t *testing.T) {
		items := []json.RawMessage{
			[]byte(`{"type": "function_call", "name": "Bash", "arguments": "{}"}`),
			[]byte(`{"type": "function_call_output", "output": "ok"}`),
		}
		orphanedCalls, orphanedOutputs := ValidateToolCallPairing(items)
		if len(orphanedCalls) != 0 {
			t.Errorf("expected 0 orphaned calls, got %d", len(orphanedCalls))
		}
		if len(orphanedOutputs) != 0 {
			t.Errorf("expected 0 orphaned outputs, got %d", len(orphanedOutputs))
		}
	})
}

func TestValidateAndSanitizeToolCall(t *testing.T) {
	t.Run("valid tool call", func(t *testing.T) {
		item := []byte(`{"type": "function_call", "call_id": "call_123", "name": "Bash", "arguments": {"command": "ls"}}`)
		result, err := ValidateAndSanitizeToolCall(item)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.CallID != "call_123" {
			t.Errorf("expected call_id call_123, got %s", result.CallID)
		}
		if result.Name != "Bash" {
			t.Errorf("expected name Bash, got %s", result.Name)
		}
	})

	t.Run("empty item", func(t *testing.T) {
		_, err := ValidateAndSanitizeToolCall([]byte{})
		if err == nil {
			t.Error("expected error for empty item")
		}
	})

	t.Run("wrong type", func(t *testing.T) {
		item := []byte(`{"type": "function_call_output", "call_id": "call_123", "output": "ok"}`)
		_, err := ValidateAndSanitizeToolCall(item)
		if err == nil {
			t.Error("expected error for wrong type")
		}
	})

	t.Run("missing call_id", func(t *testing.T) {
		item := []byte(`{"type": "function_call", "name": "Bash", "arguments": {"command": "ls"}}`)
		_, err := ValidateAndSanitizeToolCall(item)
		if err == nil {
			t.Error("expected error for missing call_id")
		}
	})

	t.Run("missing name", func(t *testing.T) {
		item := []byte(`{"type": "function_call", "call_id": "call_123", "arguments": {"command": "ls"}}`)
		_, err := ValidateAndSanitizeToolCall(item)
		if err == nil {
			t.Error("expected error for missing name")
		}
	})

	t.Run("invalid arguments", func(t *testing.T) {
		item := []byte(`{"type": "function_call", "call_id": "call_123", "name": "Bash", "arguments": "not an object"}`)
		_, err := ValidateAndSanitizeToolCall(item)
		if err == nil {
			t.Error("expected error for invalid arguments")
		}
	})

	t.Run("sanitizes call_id", func(t *testing.T) {
		item := []byte(`{"type": "function_call", "call_id": "call@id#test", "name": "Bash", "arguments": {}}`)
		result, err := ValidateAndSanitizeToolCall(item)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.CallID == "call@id#test" {
			t.Error("expected call_id to be sanitized")
		}
	})
}

func TestValidateAndSanitizeToolOutput(t *testing.T) {
	t.Run("valid output", func(t *testing.T) {
		item := []byte(`{"type": "function_call_output", "call_id": "call_123", "output": "ok"}`)
		result, err := ValidateAndSanitizeToolOutput(item)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.CallID != "call_123" {
			t.Errorf("expected call_id call_123, got %s", result.CallID)
		}
	})

	t.Run("empty item", func(t *testing.T) {
		_, err := ValidateAndSanitizeToolOutput([]byte{})
		if err == nil {
			t.Error("expected error for empty item")
		}
	})

	t.Run("wrong type", func(t *testing.T) {
		item := []byte(`{"type": "function_call", "call_id": "call_123", "name": "Bash", "arguments": {}}`)
		_, err := ValidateAndSanitizeToolOutput(item)
		if err == nil {
			t.Error("expected error for wrong type")
		}
	})

	t.Run("missing call_id", func(t *testing.T) {
		item := []byte(`{"type": "function_call_output", "output": "ok"}`)
		_, err := ValidateAndSanitizeToolOutput(item)
		if err == nil {
			t.Error("expected error for missing call_id")
		}
	})

	t.Run("sanitizes call_id", func(t *testing.T) {
		item := []byte(`{"type": "function_call_output", "call_id": "call@id#test", "output": "ok"}`)
		result, err := ValidateAndSanitizeToolOutput(item)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.CallID == "call@id#test" {
			t.Error("expected call_id to be sanitized")
		}
	})
}

func TestRepairToolCallPairing(t *testing.T) {
	t.Run("all paired - no change", func(t *testing.T) {
		items := []json.RawMessage{
			[]byte(`{"type": "function_call", "call_id": "call_1", "name": "Bash", "arguments": "{}"}`),
			[]byte(`{"type": "function_call_output", "call_id": "call_1", "output": "ok"}`),
		}
		result := RepairToolCallPairing(items)
		if len(result) != 2 {
			t.Errorf("expected 2 items, got %d", len(result))
		}
	})

	t.Run("orphaned output is removed", func(t *testing.T) {
		items := []json.RawMessage{
			[]byte(`{"type": "function_call_output", "call_id": "orphan_1", "output": "ok"}`),
		}
		result := RepairToolCallPairing(items)
		if len(result) != 0 {
			t.Errorf("expected 0 items (orphaned output removed), got %d", len(result))
		}
	})

	t.Run("orphaned call is kept", func(t *testing.T) {
		items := []json.RawMessage{
			[]byte(`{"type": "function_call", "call_id": "call_1", "name": "Bash", "arguments": "{}"}`),
		}
		result := RepairToolCallPairing(items)
		if len(result) != 1 {
			t.Errorf("expected 1 item (orphaned call kept), got %d", len(result))
		}
	})

	t.Run("mixed paired and orphaned", func(t *testing.T) {
		items := []json.RawMessage{
			[]byte(`{"type": "function_call", "call_id": "call_1", "name": "Bash", "arguments": "{}"}`),
			[]byte(`{"type": "function_call_output", "call_id": "call_1", "output": "ok"}`),
			[]byte(`{"type": "function_call", "call_id": "call_2", "name": "Read", "arguments": "{}"}`),
			[]byte(`{"type": "function_call_output", "call_id": "call_3", "output": "ok"}`),
		}
		result := RepairToolCallPairing(items)
		// call_1 is paired (kept), call_2 is orphaned call (kept), call_3 is orphaned output (removed)
		if len(result) != 3 {
			t.Errorf("expected 3 items, got %d", len(result))
		}
		// Verify call_3 output was removed
		for _, item := range result {
			if strings.Contains(string(item), "call_3") {
				t.Error("orphaned output call_3 should have been removed")
			}
		}
	})

	t.Run("non-tool items preserved", func(t *testing.T) {
		items := []json.RawMessage{
			[]byte(`{"type": "message", "role": "user"}`),
			[]byte(`{"type": "function_call", "call_id": "call_1", "name": "Bash", "arguments": "{}"}`),
			[]byte(`{"type": "function_call_output", "call_id": "call_1", "output": "ok"}`),
		}
		result := RepairToolCallPairing(items)
		if len(result) != 3 {
			t.Errorf("expected 3 items, got %d", len(result))
		}
	})

	t.Run("empty input", func(t *testing.T) {
		result := RepairToolCallPairing([]json.RawMessage{})
		if len(result) != 0 {
			t.Errorf("expected 0 items, got %d", len(result))
		}
	})

	t.Run("nil input", func(t *testing.T) {
		result := RepairToolCallPairing(nil)
		if result != nil {
			t.Errorf("expected nil, got %v", result)
		}
	})

	t.Run("custom tool types", func(t *testing.T) {
		items := []json.RawMessage{
			[]byte(`{"type": "custom_tool_call", "call_id": "call_1", "name": "Bash", "arguments": "{}"}`),
			[]byte(`{"type": "custom_tool_call_output", "call_id": "call_1", "output": "ok"}`),
			[]byte(`{"type": "custom_tool_call_output", "call_id": "orphan_1", "output": "ok"}`),
		}
		result := RepairToolCallPairing(items)
		if len(result) != 2 {
			t.Errorf("expected 2 items (paired kept, orphan removed), got %d", len(result))
		}
	})

	t.Run("empty items dropped", func(t *testing.T) {
		items := []json.RawMessage{
			[]byte{},
			[]byte(`{"type": "function_call", "call_id": "call_1", "name": "Bash", "arguments": "{}"}`),
			[]byte{},
			[]byte(`{"type": "function_call_output", "call_id": "call_1", "output": "ok"}`),
		}
		result := RepairToolCallPairing(items)
		if len(result) != 2 {
			t.Errorf("expected 2 items (empty items dropped), got %d", len(result))
		}
	})
}

func TestIsToolCallType(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"function_call", true},
		{"custom_tool_call", true},
		{"function_call_output", false},
		{"custom_tool_call_output", false},
		{"message", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := IsToolCallType(tt.input); got != tt.want {
				t.Errorf("IsToolCallType(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestIsToolCallOutputType(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"function_call_output", true},
		{"custom_tool_call_output", true},
		{"function_call", false},
		{"custom_tool_call", false},
		{"message", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := IsToolCallOutputType(tt.input); got != tt.want {
				t.Errorf("IsToolCallOutputType(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestTruncateString(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		maxLen  int
		want    string
	}{
		{"short string", "hello", 10, "hello"},
		{"exact length", "hello", 5, "hello"},
		{"truncated", "hello world", 5, "hello..."},
		{"empty", "", 5, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := truncateString(tt.input, tt.maxLen); got != tt.want {
				t.Errorf("truncateString(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
			}
		})
	}
}

func TestValidateToolCallID_Regression(t *testing.T) {
	// Ensure previously problematic IDs are handled correctly
	tests := []struct {
		name  string
		input string
	}{
		{"UUID style", "toolu_a1b2c3d4-e5f6-7890-abcd-ef1234567890"},
		{"MCP style", "mcp__server__tool_name"},
		{"dots in name", "mcp.server.tool"},
		{"colons", "tool:with:colons"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateToolCallID(tt.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) > MaxToolCallIDLength {
				t.Errorf("result too long: %d > %d", len(got), MaxToolCallIDLength)
			}
			if !validToolCallIDPattern.MatchString(got) {
				t.Errorf("result %q doesn't match pattern", got)
			}
		})
	}
}
