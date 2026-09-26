package helps

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestResponsesCompactionStreamPreservesOrderingAndFinalItems(t *testing.T) {
	state := ResponsesCompactionStream{Required: true}
	state.Normalize([]byte(`{"type":"response.output_item.added","output_index":2,"item":{"id":"cmp","type":"compaction","encrypted_content":"partial"}}`))
	finalItem := `{"id":"cmp","type":"compaction","encrypted_content":"final","future":{"nested":true},"summary":[{"text":"kept"}]}`
	state.Normalize([]byte(`{"type":"response.output_item.done","output_index":2,"item":` + finalItem + `}`))
	state.Normalize([]byte(`{"type":"response.output_item.added","output_index":2,"item":{"id":"cmp","type":"compaction","encrypted_content":"stale"}}`))
	state.Normalize([]byte(`{"type":"response.output_item.done","output_index":0,"item":{"id":"msg","type":"message","content":[]}}`))
	result := state.Normalize([]byte(`{"type":"response.done","sequence_number":7,"response":{"output":[],"future":42}}`))
	if gjson.GetBytes(result, "type").String() != "response.completed" || gjson.GetBytes(result, "response.output.0.id").String() != "msg" || gjson.GetBytes(result, "response.output.1").Raw != finalItem {
		t.Fatalf("items lost, duplicated, reordered or replaced: %s", result)
	}
	if gjson.GetBytes(result, "sequence_number").Int() != 7 || gjson.GetBytes(result, "response.future").Int() != 42 {
		t.Fatalf("lost terminal metadata: %s", result)
	}
}

func TestResponsesCompactionStreamRejectsInvalidResults(t *testing.T) {
	for _, output := range []string{
		`[]`,
		`[{"type":"message","content":[]}]`,
		`[{"type":"compaction"}]`,
		`[{"type":"compaction","encrypted_content":12}]`,
		`[{"type":"compaction","encrypted_content":"ok"},{"type":"compaction"}]`,
		`[{"type":"compaction_summary","encrypted_content":"ok"}]`,
	} {
		t.Run(output, func(t *testing.T) {
			state := ResponsesCompactionStream{Required: true}
			result := state.Normalize([]byte(`{"type":"response.completed","response":{"output":` + output + `}}`))
			if gjson.GetBytes(result, "type").String() != "response.failed" || gjson.GetBytes(result, "response.error.code").String() != "unsupported_compaction" {
				t.Fatalf("accepted invalid v2 output: %s", result)
			}
		})
	}
}

func TestResponsesCompactionStreamPreservesAuthoritativeTerminal(t *testing.T) {
	state := ResponsesCompactionStream{Required: true}
	state.Normalize([]byte(`{"type":"response.output_item.done","output_index":0,"item":{"type":"compaction","encrypted_content":"old"}}`))
	data := `{"type":"response.completed","response":{"output":[{"type":"compaction","encrypted_content":"terminal"}]}}`
	if result := state.Normalize([]byte(data)); string(result) != data {
		t.Fatalf("replaced authoritative terminal: %s", result)
	}
}

func TestResponsesCompactionStreamLeavesOrdinaryEventsUnchanged(t *testing.T) {
	state := ResponsesCompactionStream{}
	for _, data := range []string{`{"type":"response.completed","response":{"output":[]}}`, `{"type":"response.failed","response":{"error":{"code":"rate_limit_exceeded"}}}`, `{"type":"response.output_item.done","item":{"type":"compaction"}}`} {
		if result := state.Normalize([]byte(data)); string(result) != data {
			t.Fatalf("changed ordinary stream: %s", result)
		}
	}
}

func TestNeedsNativeResponses(t *testing.T) {
	for _, tc := range []struct {
		payload string
		want    bool
	}{
		{`{"input":[{"type":"compaction_trigger"}]}`, true},
		{`{"input":[{"type":"compaction","encrypted_content":"opaque"}]}`, true},
		{`{"input":[{"role":"user","content":"compaction_trigger"}]}`, false},
		{`{"input":"compaction_trigger"}`, false},
		{`{"input":{"type":"compaction_trigger"}}`, false},
	} {
		if got := NeedsNativeResponses([]byte(tc.payload)); got != tc.want {
			t.Errorf("NeedsNativeResponses(%s) = %v", tc.payload, got)
		}
	}
}
