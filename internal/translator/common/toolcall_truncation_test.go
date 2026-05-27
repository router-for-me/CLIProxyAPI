package common

import (
	"strings"
	"testing"
)

func TestDetectTruncation(t *testing.T) {
	requiredFields := map[string][][]string{
		"Write": {{"file_path"}, {"content"}},
		"Bash":  {{"cmd", "command"}},
	}
	writeTools := map[string]bool{
		"Write": true,
	}

	t.Run("no truncation for valid input", func(t *testing.T) {
		parsed := map[string]interface{}{
			"file_path": "/tmp/test.go",
			"content":   "package main\nfunc main() {}",
		}
		info := DetectTruncation("Write", "id1", `{"file_path":"/tmp/test.go","content":"package main\nfunc main() {}"}`, parsed, requiredFields, writeTools)
		if info.IsTruncated {
			t.Errorf("expected no truncation, got %s: %s", info.TruncationType, info.ErrorMessage)
		}
	})

	t.Run("empty input for tool with required fields", func(t *testing.T) {
		info := DetectTruncation("Write", "id1", "", nil, requiredFields, writeTools)
		if !info.IsTruncated {
			t.Error("expected truncation for empty input")
		}
		if info.TruncationType != TruncationTypeEmptyInput {
			t.Errorf("expected type %s, got %s", TruncationTypeEmptyInput, info.TruncationType)
		}
	})

	t.Run("empty input for tool without required fields", func(t *testing.T) {
		info := DetectTruncation("TaskList", "id1", "", nil, requiredFields, writeTools)
		if info.IsTruncated {
			t.Errorf("expected no truncation for tool without required fields, got %s", info.TruncationType)
		}
	})

	t.Run("empty input with nil required fields", func(t *testing.T) {
		info := DetectTruncation("Write", "id1", "", nil, nil, writeTools)
		if info.IsTruncated {
			t.Errorf("expected no truncation with nil requiredFields, got %s", info.TruncationType)
		}
	})

	t.Run("invalid JSON that looks truncated", func(t *testing.T) {
		info := DetectTruncation("Write", "id1", `{"file_path":"/tmp/test","content":"hello`, nil, requiredFields, writeTools)
		if !info.IsTruncated {
			t.Error("expected truncation for truncated JSON")
		}
		if info.TruncationType != TruncationTypeInvalidJSON {
			t.Errorf("expected type %s, got %s", TruncationTypeInvalidJSON, info.TruncationType)
		}
	})

	t.Run("invalid JSON that does not look truncated", func(t *testing.T) {
		info := DetectTruncation("Write", "id1", "not json at all", nil, requiredFields, writeTools)
		if info.IsTruncated {
			t.Errorf("expected no truncation for non-JSON input, got %s", info.TruncationType)
		}
	})

	t.Run("missing required fields", func(t *testing.T) {
		parsed := map[string]interface{}{
			"file_path": "/tmp/test.go",
			// missing "content"
		}
		info := DetectTruncation("Write", "id1", `{"file_path":"/tmp/test.go"}`, parsed, requiredFields, writeTools)
		if !info.IsTruncated {
			t.Error("expected truncation for missing required fields")
		}
		if info.TruncationType != TruncationTypeMissingFields {
			t.Errorf("expected type %s, got %s", TruncationTypeMissingFields, info.TruncationType)
		}
	})

	t.Run("alternative required fields satisfied", func(t *testing.T) {
		parsed := map[string]interface{}{
			"command": "ls -la",
		}
		info := DetectTruncation("Bash", "id1", `{"command":"ls -la"}`, parsed, requiredFields, writeTools)
		if info.IsTruncated {
			t.Errorf("expected no truncation when alternative field present, got %s: %s", info.TruncationType, info.ErrorMessage)
		}
	})

	t.Run("content truncation for write tool", func(t *testing.T) {
		// Large raw input but very short content
		rawInput := `{"file_path":"/tmp/test.go","content":"x"}` + strings.Repeat(" ", 1000)
		parsed := map[string]interface{}{
			"file_path": "/tmp/test.go",
			"content":   "x",
		}
		info := DetectTruncation("Write", "id1", rawInput, parsed, requiredFields, writeTools)
		if !info.IsTruncated {
			t.Error("expected truncation for suspiciously short content")
		}
		if info.TruncationType != TruncationTypeIncompleteString {
			t.Errorf("expected type %s, got %s", TruncationTypeIncompleteString, info.TruncationType)
		}
	})

	t.Run("unclosed code fence in content", func(t *testing.T) {
		parsed := map[string]interface{}{
			"file_path": "/tmp/test.go",
			"content":   "```go\nfunc main() {",
		}
		info := DetectTruncation("Write", "id1", `{"file_path":"/tmp/test.go","content":"`+"```go\\nfunc main() {"+`"}`, parsed, requiredFields, writeTools)
		if !info.IsTruncated {
			t.Error("expected truncation for unclosed code fence")
		}
	})

	t.Run("non-write tool skips content check", func(t *testing.T) {
		parsed := map[string]interface{}{
			"cmd": "echo hello",
		}
		info := DetectTruncation("Bash", "id1", `{"cmd":"echo hello"}`, parsed, requiredFields, writeTools)
		if info.IsTruncated {
			t.Errorf("expected no truncation for non-write tool, got %s: %s", info.TruncationType, info.ErrorMessage)
		}
	})
}

func TestLooksLikeTruncatedJSON(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"empty", "", false},
		{"not JSON", "hello world", false},
		{"valid JSON", `{"key": "value"}`, false},
		{"trailing quote", `{"key": "val`, true},
		{"trailing colon", `{"key":`, true},
		{"trailing comma", `{"key": "value",`, true},
		{"unbalanced braces", `{"key": "value"`, true},
		{"unbalanced brackets", `{"arr": [1, 2`, true},
		{"unclosed string", `{"key": "value`, true},
		{"valid nested", `{"a": {"b": "c"}}`, false},
		{"valid with array", `{"a": [1, 2, 3]}`, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := LooksLikeTruncatedJSON(tt.input); got != tt.want {
				t.Errorf("LooksLikeTruncatedJSON(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestExtractPartialFields(t *testing.T) {
	t.Run("valid partial JSON", func(t *testing.T) {
		fields := ExtractPartialFields(`{"key1": "value1", "key2": "value2"`)
		// Note: rough parsing preserves JSON quotes around values
		if fields["key1"] != `"value1"` {
			t.Errorf("expected key1=%q, got %q", `"value1"`, fields["key1"])
		}
		if fields["key2"] != `"value2"` {
			t.Errorf("expected key2=%q, got %q", `"value2"`, fields["key2"])
		}
	})

	t.Run("not JSON", func(t *testing.T) {
		fields := ExtractPartialFields("not json")
		if len(fields) != 0 {
			t.Errorf("expected 0 fields, got %d", len(fields))
		}
	})

	t.Run("empty", func(t *testing.T) {
		fields := ExtractPartialFields("")
		if len(fields) != 0 {
			t.Errorf("expected 0 fields, got %d", len(fields))
		}
	})

	t.Run("long value truncated", func(t *testing.T) {
		longVal := strings.Repeat("x", 100)
		fields := ExtractPartialFields(`{"key": "` + longVal + `"`)
		if !strings.HasSuffix(fields["key"], "...") {
			t.Errorf("expected long value to be truncated with ..., got %q", fields["key"])
		}
	})
}

func TestExtractParsedFieldNames(t *testing.T) {
	t.Run("string values", func(t *testing.T) {
		parsed := map[string]interface{}{
			"name": "test",
			"id":   "123",
		}
		fields := ExtractParsedFieldNames(parsed)
		if fields["name"] != "test" {
			t.Errorf("expected name=test, got %q", fields["name"])
		}
	})

	t.Run("nil value", func(t *testing.T) {
		parsed := map[string]interface{}{
			"key": nil,
		}
		fields := ExtractParsedFieldNames(parsed)
		if fields["key"] != "<null>" {
			t.Errorf("expected <null>, got %q", fields["key"])
		}
	})

	t.Run("non-string value", func(t *testing.T) {
		parsed := map[string]interface{}{
			"count": 42,
		}
		fields := ExtractParsedFieldNames(parsed)
		if fields["count"] != "<present>" {
			t.Errorf("expected <present>, got %q", fields["count"])
		}
	})

	t.Run("long string truncated", func(t *testing.T) {
		parsed := map[string]interface{}{
			"content": strings.Repeat("x", 100),
		}
		fields := ExtractParsedFieldNames(parsed)
		if !strings.HasSuffix(fields["content"], "...") {
			t.Errorf("expected truncation, got %q", fields["content"])
		}
	})
}

func TestFindMissingRequiredFields(t *testing.T) {
	t.Run("all present", func(t *testing.T) {
		parsed := map[string]interface{}{
			"file_path": "/tmp/test",
			"content":   "hello",
		}
		groups := [][]string{{"file_path"}, {"content"}}
		missing := FindMissingRequiredFields(parsed, groups)
		if len(missing) != 0 {
			t.Errorf("expected 0 missing, got %d: %v", len(missing), missing)
		}
	})

	t.Run("one missing", func(t *testing.T) {
		parsed := map[string]interface{}{
			"file_path": "/tmp/test",
		}
		groups := [][]string{{"file_path"}, {"content"}}
		missing := FindMissingRequiredFields(parsed, groups)
		if len(missing) != 1 {
			t.Errorf("expected 1 missing, got %d", len(missing))
		}
	})

	t.Run("alternative satisfied", func(t *testing.T) {
		parsed := map[string]interface{}{
			"command": "ls",
		}
		groups := [][]string{{"cmd", "command"}}
		missing := FindMissingRequiredFields(parsed, groups)
		if len(missing) != 0 {
			t.Errorf("expected 0 missing (alternative satisfied), got %d: %v", len(missing), missing)
		}
	})

	t.Run("alternative not satisfied", func(t *testing.T) {
		parsed := map[string]interface{}{}
		groups := [][]string{{"cmd", "command"}}
		missing := FindMissingRequiredFields(parsed, groups)
		if len(missing) != 1 {
			t.Errorf("expected 1 missing, got %d", len(missing))
		}
		if missing[0] != "cmd/command" {
			t.Errorf("expected 'cmd/command', got %q", missing[0])
		}
	})
}

func TestDetectContentTruncation(t *testing.T) {
	t.Run("no content field", func(t *testing.T) {
		result := DetectContentTruncation(map[string]interface{}{"key": "val"}, `{"key":"val"}`)
		if result != "" {
			t.Errorf("expected empty, got %q", result)
		}
	})

	t.Run("content not string", func(t *testing.T) {
		result := DetectContentTruncation(map[string]interface{}{"content": 42}, `{"content":42}`)
		if result != "" {
			t.Errorf("expected empty, got %q", result)
		}
	})

	t.Run("suspiciously short content", func(t *testing.T) {
		rawInput := `{"content":"x"}` + strings.Repeat(" ", 1000)
		result := DetectContentTruncation(map[string]interface{}{"content": "x"}, rawInput)
		if result == "" {
			t.Error("expected truncation for short content with large raw input")
		}
	})

	t.Run("unclosed code fence", func(t *testing.T) {
		result := DetectContentTruncation(map[string]interface{}{
			"content": "```go\nfunc main() {",
		}, `{"content":"`+"```go\\nfunc main() {"+`"}`)
		if result == "" {
			t.Error("expected truncation for unclosed code fence")
		}
	})

	t.Run("closed code fence", func(t *testing.T) {
		result := DetectContentTruncation(map[string]interface{}{
			"content": "```go\nfunc main() {}\n```",
		}, `{"content":"normal"}`)
		if result != "" {
			t.Errorf("expected no truncation, got %q", result)
		}
	})
}

func TestIsTruncated(t *testing.T) {
	requiredFields := map[string][][]string{
		"Write": {{"file_path"}, {"content"}},
	}

	t.Run("not truncated", func(t *testing.T) {
		if IsTruncated("Write", `{"file_path":"/tmp/t","content":"hello"}`, map[string]interface{}{"file_path": "/tmp/t", "content": "hello"}, requiredFields, nil) {
			t.Error("expected false")
		}
	})

	t.Run("truncated", func(t *testing.T) {
		if !IsTruncated("Write", "", nil, requiredFields, nil) {
			t.Error("expected true for empty input")
		}
	})
}

func TestGetTruncationSummary(t *testing.T) {
	t.Run("not truncated returns empty", func(t *testing.T) {
		info := TruncationInfo{IsTruncated: false}
		if summary := GetTruncationSummary(info); summary != "" {
			t.Errorf("expected empty, got %q", summary)
		}
	})

	t.Run("truncated returns JSON", func(t *testing.T) {
		info := TruncationInfo{
			IsTruncated:    true,
			TruncationType: TruncationTypeEmptyInput,
			ToolName:       "Write",
			ParsedFields:   map[string]string{},
			RawInput:       "",
		}
		summary := GetTruncationSummary(info)
		if summary == "" {
			t.Error("expected non-empty summary")
		}
		if !strings.Contains(summary, "Write") {
			t.Error("expected summary to contain tool name")
		}
	})
}
