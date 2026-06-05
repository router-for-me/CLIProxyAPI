package logging

import (
	"strings"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
)

func TestLogFormatterIncludesFailureMetadataFields(t *testing.T) {
	entry := &log.Entry{
		Time:    time.Date(2026, 6, 5, 17, 55, 16, 0, time.UTC),
		Level:   log.WarnLevel,
		Message: "failure_metadata",
		Data: log.Fields{
			"request_id":          "req-test",
			"event":               "failure_metadata",
			"failure_class":       "upstream_api_error",
			"model":               "gpt-5.5",
			"endpoint":            "POST /v1/chat/completions",
			"message_count":       127,
			"tool_count":          49,
			"reasoning_effort":    "minimal",
			"attempt_count":       4,
			"duration_ms":         3025,
			"upstream_status":     500,
			"upstream_error_code": "api_error",
		},
	}

	raw, err := (&LogFormatter{}).Format(entry)
	if err != nil {
		t.Fatalf("format log entry: %v", err)
	}
	line := string(raw)
	for _, want := range []string{
		"[req-test]",
		"failure_metadata",
		"model=gpt-5.5",
		"event=failure_metadata",
		"failure_class=upstream_api_error",
		"endpoint=POST /v1/chat/completions",
		"message_count=127",
		"tool_count=49",
		"reasoning_effort=minimal",
		"attempt_count=4",
		"duration_ms=3025",
		"upstream_status=500",
		"upstream_error_code=api_error",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("formatted log missing %q: %s", want, line)
		}
	}
}

func TestLogFormatterIncludesToolHistoryRepairFields(t *testing.T) {
	entry := &log.Entry{
		Time:    time.Date(2026, 6, 5, 18, 30, 0, 0, time.UTC),
		Level:   log.WarnLevel,
		Message: "repaired Claude tool_use history",
		Data: log.Fields{
			"request_id":                  "req-tool",
			"executor":                    "claude",
			"compat_kind":                 "minimax",
			"repairs":                     2,
			"merged_tool_result_messages": 1,
			"deduped_tool_results":        1,
			"reordered_tool_results":      1,
			"removed_tool_uses":           2,
			"removed_tool_results":        3,
		},
	}

	raw, err := (&LogFormatter{}).Format(entry)
	if err != nil {
		t.Fatalf("format log entry: %v", err)
	}
	line := string(raw)
	for _, want := range []string{
		"[req-tool]",
		"repaired Claude tool_use history",
		"executor=claude",
		"compat_kind=minimax",
		"repairs=2",
		"merged_tool_result_messages=1",
		"deduped_tool_results=1",
		"reordered_tool_results=1",
		"removed_tool_uses=2",
		"removed_tool_results=3",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("formatted log missing %q: %s", want, line)
		}
	}
}
