package executor

import (
	"net/http"
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

func TestNormalizeCodexParallelToolCalls_ResponsesLiteMetadataForcesFalse(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-luna","tools":[{"type":"function","name":"lookup"}],"parallel_tool_calls":true,"client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"true"},"input":"hi"}`)

	out := normalizeCodexParallelToolCalls(body, nil)

	parallelToolCalls := gjson.GetBytes(out, "parallel_tool_calls")
	if !parallelToolCalls.Exists() || parallelToolCalls.Bool() {
		t.Fatalf("responses-lite parallel_tool_calls should be false: %s", string(out))
	}
}

func TestNormalizeCodexParallelToolCalls_ResponsesLiteHeaderForcesFalse(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-luna","parallel_tool_calls":true,"input":"hi"}`)
	headers := make(http.Header)
	headers.Set(codexResponsesLiteHeader, "true")

	out := normalizeCodexParallelToolCalls(body, headers)

	parallelToolCalls := gjson.GetBytes(out, "parallel_tool_calls")
	if !parallelToolCalls.Exists() || parallelToolCalls.Bool() {
		t.Fatalf("responses-lite parallel_tool_calls should be false: %s", string(out))
	}
}
