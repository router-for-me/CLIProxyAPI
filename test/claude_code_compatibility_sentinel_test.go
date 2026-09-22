package test

import "testing"

type sentinelPayload = map[string]any

var (
	claudeCodeToolProgressFixture = sentinelPayload{
		"type":                 "tool_progress",
		"tool_use_id":          "toolu_123",
		"tool_name":            "Bash",
		"parent_tool_use_id":   nil,
		"elapsed_time_seconds": 2.5,
		"task_id":              "task_123",
		"uuid":                 "11111111-1111-4111-8111-111111111111",
		"session_id":           "sess_123",
	}
	claudeCodeSessionStateChangedFixture = sentinelPayload{
		"type":       "system",
		"subtype":    "session_state_changed",
		"state":      "requires_action",
		"uuid":       "22222222-2222-4222-8222-222222222222",
		"session_id": "sess_123",
	}
	claudeCodeToolUseSummaryFixture = sentinelPayload{
		"type":                   "tool_use_summary",
		"summary":                "Searched in auth/",
		"preceding_tool_use_ids": []any{"toolu_1", "toolu_2"},
		"uuid":                   "33333333-3333-4333-8333-333333333333",
		"session_id":             "sess_123",
	}
	claudeCodeControlRequestCanUseToolFixture = sentinelPayload{
		"type":       "control_request",
		"request_id": "req_123",
		"request": sentinelPayload{
			"subtype":     "can_use_tool",
			"tool_name":   "Bash",
			"input":       sentinelPayload{"command": "npm test"},
			"tool_use_id": "toolu_123",
			"description": "Running npm test",
		},
	}
)

func requireStringField(t *testing.T, obj sentinelPayload, key string) string {
	t.Helper()
	value, ok := obj[key].(string)
	if !ok || value == "" {
		t.Fatalf("field %q missing or empty: %#v", key, obj[key])
	}
	return value
}

func TestClaudeCodeSentinel_ToolProgressShape(t *testing.T) {
	payload := claudeCodeToolProgressFixture
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
	payload := claudeCodeSessionStateChangedFixture
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
	payload := claudeCodeToolUseSummaryFixture
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
	payload := claudeCodeControlRequestCanUseToolFixture
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
