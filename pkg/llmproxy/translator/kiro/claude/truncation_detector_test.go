package claude

import (
	"strings"
	"testing"
)

func TestDetectTruncation(t *testing.T) {
	// 1. Empty input
	info1 := DetectTruncation("Write", "c1", "", nil)
	if !info1.IsTruncated || info1.TruncationType != TruncationTypeEmptyInput {
		t.Errorf("expected empty_input truncation, got %v", info1)
	}

	// 2. Invalid JSON (truncated)
	info2 := DetectTruncation("Write", "c1", `{"file_path": "test.txt", "content": "hello`, nil)
	if !info2.IsTruncated || info2.TruncationType != TruncationTypeInvalidJSON {
		t.Errorf("expected invalid_json truncation, got %v", info2)
	}
	if info2.ParsedFields["file_path"] != "test.txt" {
		t.Errorf("expected partial field file_path=test.txt, got %v", info2.ParsedFields)
	}

	// 3. Missing fields
	parsed3 := map[string]interface{}{"file_path": "test.txt"}
	info3 := DetectTruncation("Write", "c1", `{"file_path": "test.txt"}`, parsed3)
	if !info3.IsTruncated || info3.TruncationType != TruncationTypeMissingFields {
		t.Errorf("expected missing_fields truncation, got %v", info3)
	}

	// 4. Incomplete string (write tool)
	parsed4 := map[string]interface{}{"file_path": "test.txt", "content": "```go\nfunc main() {"}
	info4 := DetectTruncation("Write", "c1", `{"file_path": "test.txt", "content": "`+"```"+`go\nfunc main() {"}`, parsed4)
	if !info4.IsTruncated || info4.TruncationType != TruncationTypeIncompleteString {
		t.Errorf("expected incomplete_string truncation, got %v", info4)
	}
	if !strings.Contains(info4.ErrorMessage, "unclosed code fence") {
		t.Errorf("expected unclosed code fence error, got %s", info4.ErrorMessage)
	}

	// 5. Success
	parsed5 := map[string]interface{}{"file_path": "test.txt", "content": "hello"}
	info5 := DetectTruncation("Write", "c1", `{"file_path": "test.txt", "content": "hello"}`, parsed5)
	if info5.IsTruncated {
		t.Errorf("expected no truncation, got %v", info5)
	}

	// 6. Bash cmd alias compatibility (Ampcode)
	parsed6 := map[string]interface{}{"cmd": "echo hello"}
	info6 := DetectTruncation("Bash", "c2", `{"cmd":"echo hello"}`, parsed6)
	if info6.IsTruncated {
		t.Errorf("expected no truncation for Bash cmd alias, got %v", info6)
	}

	// 7. execute cmd alias compatibility
	parsed7 := map[string]interface{}{"cmd": "ls -la"}
	info7 := DetectTruncation("execute", "c3", `{"cmd":"ls -la"}`, parsed7)
	if info7.IsTruncated {
		t.Errorf("expected no truncation for execute cmd alias, got %v", info7)
	}

	// 8. run_command cmd alias compatibility
	parsed8 := map[string]interface{}{"cmd": "pwd"}
	info8 := DetectTruncation("run_command", "c4", `{"cmd":"pwd"}`, parsed8)
	if info8.IsTruncated {
		t.Errorf("expected no truncation for run_command cmd alias, got %v", info8)
	}

	// 9. command tool still truncates when both command aliases are missing
	parsed9 := map[string]interface{}{"path": "/tmp"}
	info9 := DetectTruncation("execute", "c5", `{"path":"/tmp"}`, parsed9)
	if !info9.IsTruncated || info9.TruncationType != TruncationTypeMissingFields {
		t.Errorf("expected missing_fields truncation when command aliases are absent, got %v", info9)
	}
}

func TestBuildSoftFailureToolResult(t *testing.T) {
	info := TruncationInfo{
		IsTruncated:    true,
		TruncationType: TruncationTypeInvalidJSON,
		ToolName:       "Write",
		ToolUseID:      "c1",
		RawInput:       `{"file_path": "test.txt", "content": "abc`,
		ParsedFields:   map[string]string{"file_path": "test.txt"},
	}
	got := BuildSoftFailureToolResult(info)
	if !strings.Contains(got, "TOOL_CALL_INCOMPLETE") {
		t.Error("expected TOOL_CALL_INCOMPLETE header")
	}
	if !strings.Contains(got, "file_path=test.txt") {
		t.Error("expected partial context in message")
	}
	if !strings.Contains(got, "Split your output into smaller chunks") {
		t.Error("expected retry guidance")
	}
}
