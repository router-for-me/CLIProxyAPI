package logging

import (
	"strings"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
)

func TestLogFormatterIncludesCodexDirectHTTPCompactDiagnosticFields(t *testing.T) {
	entry := &log.Entry{
		Time:    time.Date(2026, 5, 7, 14, 30, 0, 0, time.UTC),
		Level:   log.InfoLevel,
		Message: "codex direct http compact evidence diagnostic",
		Data: log.Fields{
			"route_kind":                          "compact",
			"compact_request":                     true,
			"has_previous_response_id":            false,
			"scope_present":                       true,
			"binding_result":                      "none",
			"repair_result":                       "none",
			"input_item_count":                    6,
			"assistant_message_count":             1,
			"function_call_count":                 2,
			"function_call_output_count":          2,
			"compact_output_has_evidence":         false,
			"same_turn_evidence_hit":              true,
			"compact_response_evidence_augmented": true,
			"recent_evidence_hit":                 false,
			"compact_evidence_augmented":          true,
			"fail_reason":                         "none",
			"bound_output_item_count":             6,
			"bound_output_assistant_count":        1,
			"bound_output_tool_call_count":        2,
			"bound_output_tool_output_count":      2,
			"raw_prompt":                          "do not print prompt",
			"response_id":                         "resp_do_not_print",
			"auth_id":                             "auth_do_not_print",
			"tool_arguments":                      "{}",
			"message_text":                        "do not print message",
		},
	}

	formatted, errFormat := (&LogFormatter{}).Format(entry)
	if errFormat != nil {
		t.Fatalf("Format returned error: %v", errFormat)
	}
	line := string(formatted)
	for _, want := range []string{
		"route_kind=compact",
		"compact_request=true",
		"has_previous_response_id=false",
		"scope_present=true",
		"binding_result=none",
		"repair_result=none",
		"input_item_count=6",
		"assistant_message_count=1",
		"function_call_count=2",
		"function_call_output_count=2",
		"compact_output_has_evidence=false",
		"same_turn_evidence_hit=true",
		"compact_response_evidence_augmented=true",
		"recent_evidence_hit=false",
		"compact_evidence_augmented=true",
		"fail_reason=none",
		"bound_output_item_count=6",
		"bound_output_assistant_count=1",
		"bound_output_tool_call_count=2",
		"bound_output_tool_output_count=2",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("formatted log missing %q: %s", want, line)
		}
	}
	for _, forbidden := range []string{
		"do not print prompt",
		"resp_do_not_print",
		"auth_do_not_print",
		"tool_arguments",
		"message_text",
	} {
		if strings.Contains(line, forbidden) {
			t.Fatalf("formatted log leaked %q: %s", forbidden, line)
		}
	}
}
