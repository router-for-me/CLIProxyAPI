package test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type jsonObject = map[string]any

func loadClaudeCodeSentinelFixture(t *testing.T, name string) jsonObject {
	t.Helper()
	path := filepath.Join("testdata", "claude_code_sentinels", name)
	data := mustReadFile(t, path)
	var payload jsonObject
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("unmarshal %s: %v", name, err)
	}
	return payload
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func requireStringField(t *testing.T, obj jsonObject, key string) string {
	t.Helper()
	value, ok := obj[key].(string)
	if !ok || value == "" {
		t.Fatalf("field %q missing or empty: %#v", key, obj[key])
	}
	return value
}

func TestClaudeCodeSentinel_ToolProgressShape(t *testing.T) {
	payload := loadClaudeCodeSentinelFixture(t, "tool_progress.json")
	if got := requireStringField(t, payload, "type"); got != "tool_progress" {
		t.Fatalf("type = %q, want tool_progress", got)
	}
	requireStringField(t, payload, "tool_use_id")
	requireStringField(t, payload, "tool_name")
	requireStringField(t, payload, "session_id")
	if _, ok := payload["elapsed_time_seconds"].(float64); !ok {
		t.Fatalf("elapsed_time_seconds missing or non-number: %#v", payload["elapsed_time_seconds"])
	}
}

func TestClaudeCodeSentinel_SessionStateShape(t *testing.T) {
	payload := loadClaudeCodeSentinelFixture(t, "session_state_changed.json")
	if got := requireStringField(t, payload, "type"); got != "system" {
		t.Fatalf("type = %q, want system", got)
	}
	if got := requireStringField(t, payload, "subtype"); got != "session_state_changed" {
		t.Fatalf("subtype = %q, want session_state_changed", got)
	}
	state := requireStringField(t, payload, "state")
	switch state {
	case "idle", "running", "requires_action":
	default:
		t.Fatalf("unexpected session state %q", state)
	}
	requireStringField(t, payload, "session_id")
}

func TestClaudeCodeSentinel_ToolUseSummaryShape(t *testing.T) {
	payload := loadClaudeCodeSentinelFixture(t, "tool_use_summary.json")
	if got := requireStringField(t, payload, "type"); got != "tool_use_summary" {
		t.Fatalf("type = %q, want tool_use_summary", got)
	}
	requireStringField(t, payload, "summary")
	rawIDs, ok := payload["preceding_tool_use_ids"].([]any)
	if !ok || len(rawIDs) == 0 {
		t.Fatalf("preceding_tool_use_ids missing or empty: %#v", payload["preceding_tool_use_ids"])
	}
	for i, raw := range rawIDs {
		if id, ok := raw.(string); !ok || id == "" {
			t.Fatalf("preceding_tool_use_ids[%d] invalid: %#v", i, raw)
		}
	}
}

func TestClaudeCodeSentinel_ControlRequestCanUseToolShape(t *testing.T) {
	payload := loadClaudeCodeSentinelFixture(t, "control_request_can_use_tool.json")
	if got := requireStringField(t, payload, "type"); got != "control_request" {
		t.Fatalf("type = %q, want control_request", got)
	}
	requireStringField(t, payload, "request_id")
	request, ok := payload["request"].(map[string]any)
	if !ok {
		t.Fatalf("request missing or invalid: %#v", payload["request"])
	}
	if got := requireStringField(t, request, "subtype"); got != "can_use_tool" {
		t.Fatalf("request.subtype = %q, want can_use_tool", got)
	}
	requireStringField(t, request, "tool_name")
	requireStringField(t, request, "tool_use_id")
	if input, ok := request["input"].(map[string]any); !ok || len(input) == 0 {
		t.Fatalf("request.input missing or empty: %#v", request["input"])
	}
}
