package loguploader

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---- SSE parsing tests ----

func TestParseSSEPayloadCompleted(t *testing.T) {
	t.Parallel()

	payload := "event: response.output_item.added\n" +
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","content":[]}}` + "\n\n" +
		"event: response.content_part.added\n" +
		`data: {"type":"response.content_part.added","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}` + "\n\n" +
		"event: response.output_text.delta\n" +
		`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"Hello "}` + "\n\n" +
		"event: response.output_text.delta\n" +
		`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"world"}` + "\n\n" +
		"event: response.output_text.done\n" +
		`data: {"type":"response.output_text.done","output_index":0,"content_index":0,"text":"Hello world"}` + "\n\n" +
		"event: response.output_item.done\n" +
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","content":[{"type":"output_text","text":"Hello world"}]}}` + "\n\n" +
		"event: response.completed\n" +
		`data: {"type":"response.completed","response":{"id":"resp-1","output":[{"type":"message","content":[{"type":"output_text","text":"Hello world"}]}]}}` + "\n\n"

	body, stats := parseSSEPayload(payload)
	if stats.EventCount != 7 {
		t.Errorf("event count = %d, want 7", stats.EventCount)
	}
	if stats.TerminalType != "response.completed" {
		t.Errorf("terminal type = %q, want response.completed", stats.TerminalType)
	}
	outputs, _ := body["output"].([]any)
	if len(outputs) == 0 {
		// Try from reconstructed items.
		outputs, _ = body["outputs"].([]any)
	}
	if len(outputs) == 0 {
		t.Fatal("no outputs in parsed body")
	}
}

func TestParseSSEPayloadFunctionCall(t *testing.T) {
	t.Parallel()

	payload := "event: response.output_item.added\n" +
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","name":"get_weather","call_id":"call-1","arguments":""}}` + "\n\n" +
		"event: response.function_call_arguments.delta\n" +
		`data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"city\":"}` + "\n\n" +
		"event: response.function_call_arguments.delta\n" +
		`data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"\"Tokyo\"}"}` + "\n\n" +
		"event: response.function_call_arguments.done\n" +
		`data: {"type":"response.function_call_arguments.done","output_index":0,"arguments":"{\"city\":\"Tokyo\"}"}` + "\n\n" +
		"event: response.output_item.done\n" +
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","name":"get_weather","call_id":"call-1","arguments":"{\"city\":\"Tokyo\"}"}}` + "\n\n" +
		"event: response.completed\n" +
		`data: {"type":"response.completed","response":{"id":"resp-2","output":[{"type":"function_call","name":"get_weather","call_id":"call-1","arguments":"{\"city\":\"Tokyo\"}"}]}}` + "\n\n"

	body, stats := parseSSEPayload(payload)
	if stats.TerminalType != "response.completed" {
		t.Errorf("terminal type = %q, want response.completed", stats.TerminalType)
	}
	outputs, _ := body["output"].([]any)
	if len(outputs) == 0 {
		outputs, _ = body["outputs"].([]any)
	}
	if len(outputs) == 0 {
		t.Fatal("no outputs in parsed body for function call")
	}
}

func TestParseSSEPayloadReasoningSummary(t *testing.T) {
	t.Parallel()

	payload := "event: response.output_item.added\n" +
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","content":[]}}` + "\n\n" +
		"event: response.reasoning_summary_part.added\n" +
		`data: {"type":"response.reasoning_summary_part.added","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}}` + "\n\n" +
		"event: response.reasoning_summary_text.delta\n" +
		`data: {"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":0,"delta":"Thinking..."}` + "\n\n" +
		"event: response.reasoning_summary_text.done\n" +
		`data: {"type":"response.reasoning_summary_text.done","output_index":0,"summary_index":0,"text":"Thinking..."}` + "\n\n" +
		"event: response.output_item.done\n" +
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","content":[{"type":"summary_text","text":"Thinking..."}]}}` + "\n\n" +
		"event: response.completed\n" +
		`data: {"type":"response.completed","response":{"id":"resp-3","output":[{"type":"reasoning","content":[{"type":"summary_text","text":"Thinking..."}]}]}}` + "\n\n"

	_, stats := parseSSEPayload(payload)
	if stats.TerminalType != "response.completed" {
		t.Errorf("terminal type = %q, want response.completed", stats.TerminalType)
	}
	if stats.EventCount != 6 {
		t.Errorf("event count = %d, want 6", stats.EventCount)
	}
}

func TestParseSSEPayloadFailed(t *testing.T) {
	t.Parallel()

	payload := "event: response.failed\n" +
		`data: {"type":"response.failed","response":{"id":"resp-fail","status":"failed"}}` + "\n\n"

	_, stats := parseSSEPayload(payload)
	if stats.TerminalType != "response.failed" {
		t.Errorf("terminal type = %q, want response.failed", stats.TerminalType)
	}
}

// ---- Helper function tests ----

func TestTimestampToUTC(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    any
		want     string
		passthru bool // true = expect original input returned as-is
	}{
		{"UTC input", "2026-07-15T01:23:45Z", "2026-07-15T01:23:45Z", false},
		{"positive offset", "2026-07-15T09:23:45+08:00", "2026-07-15T01:23:45Z", false},
		{"negative offset", "2026-07-14T20:23:45-05:00", "2026-07-15T01:23:45Z", false},
		{"with fraction", "2026-07-15T01:23:45.123+08:00", "2026-07-14T17:23:45.123Z", false},
		{"no timezone", "2026-07-15T01:23:45", "2026-07-15T01:23:45Z", false},
		{"non-string", 12345, "", true},
		{"empty string", "", "", true},
		{"invalid format", "not-a-timestamp", "", true},
	}
	for _, test := range tests {
		got := timestampToUTC(test.input)
		if test.passthru {
			// Passthrough: the function should return the original value.
			if got != test.input {
				t.Errorf("%s: timestampToUTC(%v) = %v, want passthrough of input", test.name, test.input, got)
			}
			continue
		}
		gotStr, ok := got.(string)
		if !ok || gotStr != test.want {
			t.Errorf("%s: timestampToUTC(%v) = %v (%T), want %q", test.name, test.input, got, got, test.want)
		}
	}
}

func TestExtractToolsAndInputs(t *testing.T) {
	t.Parallel()

	body := map[string]any{
		"model": "gpt-4o",
		"input": []any{
			map[string]any{"type": "message", "content": "hello"},
			map[string]any{"type": "additional_tools", "tools": []any{
				map[string]any{"type": "function", "name": "search"},
			}},
		},
		"tools": []any{
			map[string]any{"type": "function", "name": "get_weather"},
		},
	}
	tools, inputs := extractToolsAndInputs(body)
	if len(tools) == 0 {
		t.Fatal("no tools extracted")
	}
	// additional_tools items should be extracted as tools.
	found := false
	for _, tool := range tools {
		if m, ok := tool.(map[string]any); ok && m["name"] == "search" {
			found = true
			break
		}
	}
	if !found {
		t.Error("additional_tools 'search' not found in tools")
	}
	// Input list should have additional_tools items filtered out.
	inputList, ok := inputs.([]any)
	if !ok {
		t.Fatalf("inputs is not a list: %T", inputs)
	}
	if len(inputList) != 1 {
		t.Errorf("filtered inputs count = %d, want 1", len(inputList))
	}
}

func TestPopOutputsAndTools(t *testing.T) {
	t.Parallel()

	body := map[string]any{
		"output": []any{
			map[string]any{"type": "message", "content": "hello"},
		},
		"tools": []any{
			map[string]any{"type": "function", "name": "search"},
		},
	}
	outputs, tools := popOutputsAndTools(body)
	if len(outputs) != 1 {
		t.Errorf("outputs count = %d, want 1", len(outputs))
	}
	if len(tools) != 1 {
		t.Errorf("tools count = %d, want 1", len(tools))
	}
}

func TestPopOutputsAndToolsUsesOutputsKey(t *testing.T) {
	t.Parallel()

	body := map[string]any{
		"outputs": []any{
			map[string]any{"type": "message"},
		},
	}
	outputs, _ := popOutputsAndTools(body)
	if len(outputs) != 1 {
		t.Errorf("outputs count = %d, want 1", len(outputs))
	}
}

func TestPopOutputsAndToolsEmptyBody(t *testing.T) {
	t.Parallel()

	outputs, tools := popOutputsAndTools(map[string]any{})
	if len(outputs) != 0 {
		t.Errorf("expected empty outputs, got %d", len(outputs))
	}
	if len(tools) != 0 {
		t.Errorf("expected empty tools, got %d", len(tools))
	}
}

func TestJSONStringField(t *testing.T) {
	t.Parallel()

	value := map[string]any{"key": "value"}
	result := jsonStringField(value)
	var decoded map[string]any
	if err := json.Unmarshal([]byte(result), &decoded); err != nil {
		t.Fatalf("jsonStringField output is not valid JSON: %v", err)
	}
	if decoded["key"] != "value" {
		t.Errorf("decoded value = %v, want 'value'", decoded["key"])
	}
}

func TestJSONStringFieldNil(t *testing.T) {
	t.Parallel()

	if got := jsonStringField(nil); got != "null" {
		t.Errorf("jsonStringField(nil) = %q, want null", got)
	}
}

// ---- Normalization integration tests ----

func writeCodexTestLog(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "test.log")
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		t.Fatalf("write test log: %v", err)
	}
	return path
}

func makeTestSourceLog(t *testing.T, path, keyName, model string, timestamp time.Time) sourceLog {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat test log: %v", err)
	}
	return sourceLog{
		Path:      path,
		Relative:  filepath.Base(path),
		KeyName:   keyName,
		Model:     model,
		Provider:  providerCodex,
		Timestamp: timestamp,
		Size:      info.Size(),
		ModTime:   info.ModTime(),
	}
}

func TestNormalizeCodexRecordBasic(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	timestamp := time.Date(2026, time.July, 15, 1, 23, 45, 0, time.UTC)
	content := "Timestamp: " + timestamp.Format(time.RFC3339Nano) + "\n" +
		"=== REQUEST INFO ===\n" +
		"Timestamp: 2026-07-15T01:23:45+08:00\n\n" +
		"=== HEADERS ===\n" +
		"Content-Type: application/json\n" +
		"x-client-request-id: req-123\n\n" +
		"=== REQUEST BODY ===\n" +
		`{"model":"gpt-5.6-sol","input":"hello","client_metadata":{"turn_id":"turn-1","thread_id":"thread-1","session_id":"session-1"}}` + "\n\n" +
		"=== RESPONSE ===\n" +
		`{"output":[{"type":"message","content":[{"type":"output_text","text":"Hi there"}]}]}` + "\n"

	path := writeCodexTestLog(t, dir, content)
	source := makeTestSourceLog(t, path, "panda", "gpt-5.6-sol", timestamp)

	record, hash, err := normalizeCodexRecord(source)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if record == nil {
		t.Fatal("expected non-nil record")
	}
	if hash == "" {
		t.Error("expected non-empty hash")
	}
	if record.UserID != "panda" {
		t.Errorf("user_id = %q, want panda", record.UserID)
	}
	if record.ModelName != "gpt-5.6-sol" {
		t.Errorf("model_name = %q, want gpt-5.6-sol", record.ModelName)
	}
	if record.MessageID != "turn-1" {
		t.Errorf("message_id = %q, want turn-1", record.MessageID)
	}
	if record.ConversationID != "thread-1" {
		t.Errorf("conversation_id = %q, want thread-1", record.ConversationID)
	}
	if record.SessionID != "session-1" {
		t.Errorf("session_id = %q, want session-1", record.SessionID)
	}
	// Timestamp from REQUEST INFO should be converted to UTC.
	if !strings.HasSuffix(record.Timestamp, "Z") {
		t.Errorf("timestamp %q not in UTC", record.Timestamp)
	}
	// Verify response is a JSON string (double-encoded).
	var responseOutputs []any
	if err := json.Unmarshal([]byte(record.Response), &responseOutputs); err != nil {
		t.Fatalf("response is not valid JSON string: %v (value=%q)", err, record.Response)
	}
	if len(responseOutputs) != 1 {
		t.Errorf("response outputs count = %d, want 1", len(responseOutputs))
	}
}

func TestNormalizeCodexRecordFiltersEmptyResponse(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	timestamp := time.Date(2026, time.July, 15, 1, 0, 0, 0, time.UTC)
	content := "Timestamp: " + timestamp.Format(time.RFC3339Nano) + "\n" +
		"=== REQUEST BODY ===\n" +
		`{"model":"gpt-5.6-sol","input":"hello"}` + "\n" +
		"=== RESPONSE ===\n" +
		`{"ok":true}` + "\n"

	path := writeCodexTestLog(t, dir, content)
	source := makeTestSourceLog(t, path, "panda", "gpt-5.6-sol", timestamp)

	record, hash, err := normalizeCodexRecord(source)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if record != nil {
		t.Error("expected nil record for empty response outputs")
	}
	if hash == "" {
		t.Error("expected non-empty hash even for filtered record")
	}
}

func TestNormalizeCodexRecordJSONDoubleEncoding(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	timestamp := time.Date(2026, time.July, 15, 1, 0, 0, 0, time.UTC)
	content := "Timestamp: " + timestamp.Format(time.RFC3339Nano) + "\n" +
		"=== REQUEST BODY ===\n" +
		`{"model":"gpt-5.6-sol","input":"hello","tools":[{"type":"function","name":"search"}]}` + "\n" +
		"=== RESPONSE ===\n" +
		`{"output":[{"type":"message","content":[{"type":"output_text","text":"result"}]}]}` + "\n"

	path := writeCodexTestLog(t, dir, content)
	source := makeTestSourceLog(t, path, "panda", "gpt-5.6-sol", timestamp)

	record, _, err := normalizeCodexRecord(source)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if record == nil {
		t.Fatal("expected non-nil record")
	}

	// All six JSON string fields should be valid JSON when non-empty.
	for _, field := range []struct {
		name  string
		value string
	}{
		{"extra_info", record.ExtraInfo},
		{"tools", record.Tools},
		{"inputs", record.Inputs},
		{"response", record.Response},
		{"tool_result", record.ToolResult},
		{"metadata", record.Metadata},
	} {
		if field.value == "" || field.value == "null" {
			continue
		}
		if !json.Valid([]byte(field.value)) {
			t.Errorf("%s is not valid JSON: %q", field.name, field.value)
		}
	}

	// Verify tools field specifically.
	var tools []any
	if err := json.Unmarshal([]byte(record.Tools), &tools); err != nil {
		t.Fatalf("tools is not valid JSON: %v", err)
	}
	if len(tools) != 1 {
		t.Errorf("tools count = %d, want 1", len(tools))
	}
}

func TestNormalizeCodexRecordIdentityFallbacks(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	timestamp := time.Date(2026, time.July, 15, 1, 0, 0, 0, time.UTC)
	// No client_metadata, no headers — should fall back to response.id and source model.
	content := "Timestamp: " + timestamp.Format(time.RFC3339Nano) + "\n" +
		"=== REQUEST BODY ===\n" +
		`{"input":"hello"}` + "\n" +
		"=== RESPONSE ===\n" +
		`{"id":"resp-42","output":[{"type":"message","content":[{"type":"output_text","text":"hi"}]}]}` + "\n"

	path := writeCodexTestLog(t, dir, content)
	source := makeTestSourceLog(t, path, "alice", "unknown-model", timestamp)

	record, _, err := normalizeCodexRecord(source)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if record == nil {
		t.Fatal("expected non-nil record")
	}
	// message_id should fall back to response.id.
	if record.MessageID != "resp-42" {
		t.Errorf("message_id = %q, want resp-42", record.MessageID)
	}
	// model_name should fall back to source model.
	if record.ModelName != "unknown-model" {
		t.Errorf("model_name = %q, want unknown-model", record.ModelName)
	}
	if record.UserID != "alice" {
		t.Errorf("user_id = %q, want alice", record.UserID)
	}
}

func TestNormalizeCodexRecordThinkType(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	timestamp := time.Date(2026, time.July, 15, 1, 0, 0, 0, time.UTC)
	content := "Timestamp: " + timestamp.Format(time.RFC3339Nano) + "\n" +
		"=== REQUEST BODY ===\n" +
		`{"model":"o3","input":"solve","reasoning":{"effort":"high"}}` + "\n" +
		"=== RESPONSE ===\n" +
		`{"output":[{"type":"message","content":[{"type":"output_text","text":"42"}]}]}` + "\n"

	path := writeCodexTestLog(t, dir, content)
	source := makeTestSourceLog(t, path, "panda", "o3", timestamp)

	record, _, err := normalizeCodexRecord(source)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if record == nil {
		t.Fatal("expected non-nil record")
	}
	if record.ThinkType != "high" {
		t.Errorf("think_type = %v, want high", record.ThinkType)
	}
}

// ---- Write normalized record tests ----

func TestWriteCodexNormalizedRecord(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	timestamp := time.Date(2026, time.July, 15, 1, 0, 0, 0, time.UTC)
	content := "Timestamp: " + timestamp.Format(time.RFC3339Nano) + "\n" +
		"=== REQUEST BODY ===\n" +
		`{"model":"gpt-5.6-sol","input":"hello"}` + "\n" +
		"=== RESPONSE ===\n" +
		`{"output":[{"type":"message","content":[{"type":"output_text","text":"world"}]}]}` + "\n"

	path := writeCodexTestLog(t, dir, content)
	source := makeTestSourceLog(t, path, "panda", "gpt-5.6-sol", timestamp)

	var buf strings.Builder
	written, hash, err := writeCodexNormalizedRecord(&buf, source)
	if err != nil {
		t.Fatalf("write normalized record: %v", err)
	}
	if written == 0 {
		t.Error("expected non-zero written bytes")
	}
	if hash == "" {
		t.Error("expected non-empty hash")
	}

	// Verify it is valid JSONL (single line ending with newline).
	line := buf.String()
	if !strings.HasSuffix(line, "\n") {
		t.Error("output does not end with newline")
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}

	// Verify expected top-level keys.
	expectedKeys := []string{
		"message_id", "conversation_id", "session_id", "think_type",
		"extra_info", "tools", "inputs", "response", "timestamp",
		"model_name", "user_id", "tool_result", "metadata",
	}
	for _, key := range expectedKeys {
		if _, ok := decoded[key]; !ok {
			t.Errorf("missing key %q in normalized record", key)
		}
	}

	// Verify written byte count matches actual output.
	if int64(buf.Len()) != written {
		t.Errorf("written count = %d, actual bytes = %d", written, buf.Len())
	}
}

func TestWriteCodexNormalizedRecordFiltered(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	timestamp := time.Date(2026, time.July, 15, 1, 0, 0, 0, time.UTC)
	content := "Timestamp: " + timestamp.Format(time.RFC3339Nano) + "\n" +
		"=== REQUEST BODY ===\n" +
		`{"model":"gpt-5.6-sol","input":"hello"}` + "\n" +
		"=== RESPONSE ===\n" +
		`{"ok":true}` + "\n"

	path := writeCodexTestLog(t, dir, content)
	source := makeTestSourceLog(t, path, "panda", "gpt-5.6-sol", timestamp)

	var buf strings.Builder
	written, hash, err := writeCodexNormalizedRecord(&buf, source)
	if err != nil {
		t.Fatalf("write normalized record: %v", err)
	}
	if written != 0 {
		t.Errorf("filtered record should write 0 bytes, got %d", written)
	}
	if hash == "" {
		t.Error("expected non-empty hash for filtered record")
	}
	if buf.Len() != 0 {
		t.Errorf("filtered record should produce no output, got %d bytes", buf.Len())
	}
}

func TestNormalizeCodexRecordSSE(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	timestamp := time.Date(2026, time.July, 15, 1, 0, 0, 0, time.UTC)
	ssePayload := "event: response.output_item.added\n" +
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","content":[]}}` + "\n\n" +
		"event: response.content_part.added\n" +
		`data: {"type":"response.content_part.added","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}` + "\n\n" +
		"event: response.output_text.delta\n" +
		`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"SSE response"}` + "\n\n" +
		"event: response.output_text.done\n" +
		`data: {"type":"response.output_text.done","output_index":0,"content_index":0,"text":"SSE response"}` + "\n\n" +
		"event: response.output_item.done\n" +
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","content":[{"type":"output_text","text":"SSE response"}]}}` + "\n\n" +
		"event: response.completed\n" +
		`data: {"type":"response.completed","response":{"id":"resp-sse","output":[{"type":"message","content":[{"type":"output_text","text":"SSE response"}]}]}}` + "\n\n"

	content := "Timestamp: " + timestamp.Format(time.RFC3339Nano) + "\n" +
		"=== REQUEST BODY ===\n" +
		`{"model":"gpt-5.6-sol","input":"test sse"}` + "\n" +
		"=== RESPONSE ===\n" +
		ssePayload

	path := writeCodexTestLog(t, dir, content)
	source := makeTestSourceLog(t, path, "panda", "gpt-5.6-sol", timestamp)

	record, _, err := normalizeCodexRecord(source)
	if err != nil {
		t.Fatalf("normalize SSE: %v", err)
	}
	if record == nil {
		t.Fatal("expected non-nil record for SSE response")
	}
	if record.MessageID != "resp-sse" {
		t.Errorf("message_id = %q, want resp-sse", record.MessageID)
	}
}
