package logging

import (
	"strings"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
)

func TestLogFormatterIncludesCompactEvidenceFields(t *testing.T) {
	entry := &log.Entry{
		Time:    time.Date(2026, 5, 8, 1, 2, 3, 0, time.UTC),
		Level:   log.InfoLevel,
		Message: "openai responses compact evidence diagnostic",
		Data: log.Fields{
			"compact_input_item_count":               4,
			"compact_input_assistant_count":          1,
			"compact_input_tool_call_count":          1,
			"compact_input_tool_output_count":        1,
			"compact_output_has_evidence":            false,
			"compact_same_turn_evidence_hit":         true,
			"compact_same_turn_evidence_skipped":     true,
			"compact_same_turn_evidence_skip_reason": "tool_output_without_prior_call",
			"compact_response_evidence_augmented":    true,
			"compact_fail_reason":                    "none",
			"compact_output_item_count":              4,
			"compact_output_assistant_count":         1,
			"compact_output_tool_call_count":         1,
			"compact_output_tool_output_count":       1,
			"raw_body":                               "SECRET_PROMPT_TEXT",
		},
	}

	out, err := (&LogFormatter{}).Format(entry)
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	got := string(out)
	for _, want := range []string{
		"compact_input_item_count=4",
		"compact_input_assistant_count=1",
		"compact_input_tool_call_count=1",
		"compact_input_tool_output_count=1",
		"compact_output_has_evidence=false",
		"compact_same_turn_evidence_hit=true",
		"compact_same_turn_evidence_skipped=true",
		"compact_same_turn_evidence_skip_reason=tool_output_without_prior_call",
		"compact_response_evidence_augmented=true",
		"compact_fail_reason=none",
		"compact_output_item_count=4",
		"compact_output_assistant_count=1",
		"compact_output_tool_call_count=1",
		"compact_output_tool_output_count=1",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatted log missing %q: %s", want, got)
		}
	}
	for _, forbidden := range []string{"SECRET_PROMPT_TEXT", "raw_body="} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("formatted log leaked %q: %s", forbidden, got)
		}
	}
}
