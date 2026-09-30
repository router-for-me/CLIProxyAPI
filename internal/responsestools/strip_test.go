package responsestools

import (
	"errors"
	"testing"
)

// The strip route drops the custom declarations but keeps the conversation, so
// it converts exactly the same history the function route does. Only the
// function-shaped arguments and outputs differ.
func TestConvertCustomHistoryMigratesCallAndOutputIDs(t *testing.T) {
	value, ok := decodeValue([]byte(`{"input": [
		{"type": "custom_tool_call", "id": "ctc_call_1", "call_id": "c1", "name": "patcher", "input": "do it"},
		{"type": "custom_tool_call_output", "id": "ctco_out_1", "call_id": "c1", "output": "done"}
	]}`))
	if !ok {
		t.Fatalf("invalid test body")
	}
	changed, err := ConvertCustomHistory(value)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if !changed {
		t.Fatalf("expected change")
	}
	input, _ := value.(map[string]any)["input"].([]any)
	call, _ := input[0].(map[string]any)
	if call["type"] != "function_call" || call["id"] != "fc_call_1" {
		t.Fatalf("custom call = %v", call)
	}
	// The strip route wraps the custom text in the function parameter shape and
	// keeps everything else the client sent.
	if arguments, _ := call["arguments"].(string); arguments == "" {
		t.Fatalf("custom call lost its arguments: %v", call)
	}
	if _, exists := call["input"]; exists {
		t.Fatalf("the custom input must move into arguments: %v", call)
	}
	if call["call_id"] != "c1" {
		t.Fatalf("call_id changed: %v", call)
	}
	output, _ := input[1].(map[string]any)
	if output["type"] != "function_call_output" || output["id"] != "fco_out_1" || output["output"] != "done" {
		t.Fatalf("custom output = %v", output)
	}
}

func TestConvertCustomHistoryRejectsAnUnmigratableID(t *testing.T) {
	value, _ := decodeValue([]byte(`{"input": [
		{"type": "custom_tool_call", "id": 7, "call_id": "c1", "name": "patcher", "input": "do it"}
	]}`))
	_, err := ConvertCustomHistory(value)
	var compat *ToolCompatibilityError
	if !errors.As(err, &compat) || compat.StatusCode() != 422 || compat.Reason != ReasonHistoryLink {
		t.Fatalf("want 422 history_link, got %v", err)
	}
	call, _ := value.(map[string]any)["input"].([]any)[0].(map[string]any)
	if call["type"] != "custom_tool_call" {
		t.Fatalf("item was partially rewritten: %v", call)
	}
}
