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
