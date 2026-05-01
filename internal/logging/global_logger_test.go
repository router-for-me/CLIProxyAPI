package logging

import (
	"strings"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
)

func TestLogFormatterIncludesClaudePayloadDiagnosticsFields(t *testing.T) {
	entry := &log.Entry{
		Time:    time.Date(2026, 4, 30, 22, 40, 0, 0, time.UTC),
		Level:   log.WarnLevel,
		Message: "claude payload diagnostics: large or image-containing upstream request",
		Data: log.Fields{
			"request_id":            "abc12345",
			"model":                 "claude-opus-4-7",
			"endpoint":              "messages",
			"body_bytes":            2836981,
			"json_valid":            true,
			"messages":              4,
			"tools":                 12,
			"tool_schema_bytes":     98765,
			"max_tool_schema_bytes": 45678,
			"text_bytes":            123456,
			"max_text_bytes":        100000,
			"image_blocks":          2,
			"base64_image_blocks":   2,
			"image_data_bytes":      2000000,
			"data_image_refs":       0,
			"cache_control_nodes":   4,
		},
	}

	formatted, err := (&LogFormatter{}).Format(entry)
	if err != nil {
		t.Fatalf("Format() error = %v", err)
	}
	line := string(formatted)

	for _, want := range []string{
		"model=claude-opus-4-7",
		"endpoint=messages",
		"body_bytes=2836981",
		"json_valid=true",
		"messages=4",
		"tools=12",
		"tool_schema_bytes=98765",
		"max_tool_schema_bytes=45678",
		"text_bytes=123456",
		"max_text_bytes=100000",
		"image_blocks=2",
		"base64_image_blocks=2",
		"image_data_bytes=2000000",
		"data_image_refs=0",
		"cache_control_nodes=4",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("formatted log missing %q:\n%s", want, line)
		}
	}
}
