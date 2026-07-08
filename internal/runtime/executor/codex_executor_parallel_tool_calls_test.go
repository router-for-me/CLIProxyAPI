package executor

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestNormalizeCodexParallelToolCallsForTools_DropsWhenToolsMissing(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","parallel_tool_calls":true,"input":"hi"}`)

	out := normalizeCodexParallelToolCallsForTools(body)

	if gjson.GetBytes(out, "parallel_tool_calls").Exists() {
		t.Fatalf("parallel_tool_calls should be removed when tools are missing: %s", string(out))
	}
}

func TestNormalizeCodexParallelToolCallsForTools_DropsWhenToolsEmpty(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","tools":[],"parallel_tool_calls":false,"input":"hi"}`)

	out := normalizeCodexParallelToolCallsForTools(body)

	if gjson.GetBytes(out, "parallel_tool_calls").Exists() {
		t.Fatalf("parallel_tool_calls should be removed when tools are empty: %s", string(out))
	}
	if !gjson.GetBytes(out, "tools").Exists() {
		t.Fatalf("tools should be preserved: %s", string(out))
	}
}

func TestNormalizeCodexParallelToolCallsForTools_PreservesWhenToolsPresent(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","tools":[{"type":"function","name":"lookup"}],"parallel_tool_calls":true,"input":"hi"}`)

	out := normalizeCodexParallelToolCallsForTools(body)

	if !gjson.GetBytes(out, "parallel_tool_calls").Bool() {
		t.Fatalf("parallel_tool_calls should be preserved when tools are present: %s", string(out))
	}
}
