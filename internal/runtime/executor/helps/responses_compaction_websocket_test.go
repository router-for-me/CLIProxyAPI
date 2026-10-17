package helps

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestResponsesCompactionTerminalAppendsItemWithoutReplacingOutput(t *testing.T) {
	event := []byte(`{"type":"response.completed","sequence_number":9,"future":"kept","response":{"id":"resp_original","status":"completed","output":[{"type":"reasoning","id":"reasoning_original"},{"type":"message","id":"msg_original","role":"assistant","content":[{"type":"output_text","text":"Check the build."}]}],"usage":{"input_tokens":7,"output_tokens":3}}}`)
	terminal, items, err := ConvertResponsesCompactionTerminalEvent(event, event, "summary-model", "provider-scope", []string{"test-key"})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || gjson.GetBytes(items[0], "type").String() != "response.output_item.added" || gjson.GetBytes(items[1], "type").String() != "response.output_item.done" {
		t.Fatalf("missing appended item events: %s", items)
	}
	for index, item := range items {
		if gjson.GetBytes(item, "output_index").Int() != 2 || gjson.GetBytes(item, "sequence_number").Int() != int64(9+index) || gjson.GetBytes(item, "response_id").String() != "resp_original" {
			t.Fatalf("incorrect item event index, sequence or response ID: %s", item)
		}
	}
	if gjson.GetBytes(terminal, "sequence_number").Int() != 11 || gjson.GetBytes(terminal, "future").String() != "kept" || gjson.GetBytes(terminal, "response.usage.input_tokens").Int() != 7 {
		t.Fatalf("terminal metadata changed: %s", terminal)
	}
	if gjson.GetBytes(terminal, "response.output.0").Raw != gjson.GetBytes(event, "response.output.0").Raw || gjson.GetBytes(terminal, "response.output.1").Raw != gjson.GetBytes(event, "response.output.1").Raw {
		t.Fatalf("original output changed: %s", terminal)
	}
	if gjson.GetBytes(items[1], "item").Raw != gjson.GetBytes(terminal, "response.output.2").Raw {
		t.Fatalf("done item differs from terminal output: %s", terminal)
	}
}

func TestResponsesCompactionTerminalPreservesNativeItemAndOptionalSequence(t *testing.T) {
	for _, tc := range []struct {
		name      string
		event     string
		wantItems int
	}{
		{"native", `{"type":"response.completed","sequence_number":3,"response":{"status":"completed","output":[{"type":"compaction","encrypted_content":"native-opaque","future":true}]}}`, 0},
		{"no sequence", `{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Check the build."}]}]}}`, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			terminal, items, err := ConvertResponsesCompactionTerminalEvent([]byte(tc.event), []byte(tc.event), "summary-model", "provider-scope", []string{"test-key"})
			if err != nil || len(items) != tc.wantItems {
				t.Fatalf("conversion returned %d events and %v", len(items), err)
			}
			if tc.wantItems == 0 && gjson.GetBytes(terminal, "response.output").Raw != gjson.Get(tc.event, "response.output").Raw {
				t.Fatalf("native output changed: %s", terminal)
			}
			if !gjson.Get(tc.event, "sequence_number").Exists() {
				for _, item := range append(items, terminal) {
					if gjson.GetBytes(item, "sequence_number").Exists() {
						t.Fatalf("invented optional sequence: %s", item)
					}
				}
			}
		})
	}
}

func TestResponsesCompactionTerminalRejectsMalformedOuterEvent(t *testing.T) {
	event := `{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"summary"}]}]}`
	for _, malformed := range []string{event, event + `,"broken":`} {
		terminal, items, err := ConvertResponsesCompactionTerminalEvent([]byte(malformed), []byte(malformed), "summary-model", "provider-scope", []string{"test-key"})
		if err == nil || len(terminal) != 0 || len(items) != 0 {
			t.Fatalf("malformed event produced terminal or capsule events: %v", err)
		}
	}
}

func TestResponsesCompactionTerminalRejectsInvalidOutputBeforeReconstruction(t *testing.T) {
	prepared := []byte(`{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"summary"}]}]}}`)
	for _, output := range []string{`{"bad":"field"}`, `"invalid"`, `null`} {
		t.Run(output, func(t *testing.T) {
			original := []byte(`{"type":"response.completed","response":{"status":"completed","output":` + output + `}}`)
			terminal, items, err := ConvertResponsesCompactionTerminalEvent(prepared, original, "summary-model", "provider-scope", []string{"test-key"})
			if err == nil || len(terminal) != 0 || len(items) != 0 {
				t.Fatalf("invalid upstream output produced a capsule: %s %s, %v", terminal, items, err)
			}
		})
	}
}
