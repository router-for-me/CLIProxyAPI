package diagnosticcapture

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSummarizeRequestCapturesOnlyAllowedContinuationFields(t *testing.T) {
	resetForTest()
	raw := []byte(`{
		"model":"gpt-5.5",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"SECRET PROMPT"}]},
			{"type":"function_call","call_id":"opaque-call-123","name":"getMetricMetadata","arguments":"{\"metric\":\"betriebe\"}"},
			{"type":"function_call_output","call_id":"opaque-call-123","output":"SECRET TOOL OUTPUT"}
		],
		"tools":[
			{"type":"function","name":"getMetricMetadata","parameters":{"type":"object"}},
			{"type":"function","name":"queryMetricTimeseries","parameters":{"type":"object"}}
		],
		"tool_choice":{"type":"function","name":"queryMetricTimeseries"},
		"previous_response_id":"opaque-response-456",
		"authorization":"SECRET TOKEN"
	}`)

	record := summarizeRequest(2, "inbound_client", raw)
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)

	for _, forbidden := range []string{"SECRET PROMPT", "SECRET TOOL OUTPUT", "SECRET TOKEN", "opaque-call-123", "opaque-response-456", "arguments", "parameters", "content"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("sanitized record leaked %q: %s", forbidden, text)
		}
	}
	if record.RequestOrdinal != 2 || record.Stage != "inbound_client" {
		t.Fatalf("unexpected identity fields: %+v", record)
	}
	if record.ToolsCount != 2 || strings.Join(record.ToolNames, ",") != "getMetricMetadata,queryMetricTimeseries" {
		t.Fatalf("unexpected tools summary: %+v", record)
	}
	if record.ToolChoice.Kind != "function" || record.ToolChoice.Name != "queryMetricTimeseries" {
		t.Fatalf("unexpected tool choice: %+v", record.ToolChoice)
	}
	if len(record.FunctionCalls) != 1 || record.FunctionCalls[0].CallID != "CALL_A" || record.FunctionCalls[0].Name != "getMetricMetadata" {
		t.Fatalf("unexpected calls: %+v", record.FunctionCalls)
	}
	if len(record.FunctionCallOutputs) != 1 || record.FunctionCallOutputs[0].CallID != "CALL_A" || !record.FunctionCallOutputs[0].MatchedCall {
		t.Fatalf("unexpected outputs: %+v", record.FunctionCallOutputs)
	}
	if record.PreviousResponseID != "RESP_A" {
		t.Fatalf("unexpected previous response placeholder: %q", record.PreviousResponseID)
	}
}

func TestPlaceholderMappingIsStableAcrossStages(t *testing.T) {
	resetForTest()
	inbound := summarizeRequest(1, "inbound_client", []byte(`{"input":[{"type":"function_call","call_id":"same-call","name":"first"}]}`))
	forwarded := summarizeRequest(1, "forwarded_upstream", []byte(`{"input":[{"type":"function_call","call_id":"same-call","name":"first"},{"type":"function_call_output","call_id":"same-call","output":"ok"}]}`))
	response := summarizeUpstreamResponse(1, 200, []byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"same-response\"}}\n\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"call_id\":\"same-call\",\"name\":\"second\"}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"same-response\",\"output\":[{\"type\":\"function_call\",\"call_id\":\"same-call\",\"name\":\"second\"}]}}\n\n"))

	if inbound.FunctionCalls[0].CallID != "CALL_A" || forwarded.FunctionCalls[0].CallID != "CALL_A" || response.FunctionCalls[0].CallID != "CALL_A" {
		t.Fatalf("call placeholder changed: inbound=%+v forwarded=%+v response=%+v", inbound, forwarded, response)
	}
	if response.ResponseID != "RESP_A" {
		t.Fatalf("unexpected response placeholder: %+v", response)
	}
	if strings.Join(response.ResponseItemTypes, ",") != "function_call" {
		t.Fatalf("unexpected response item types: %+v", response.ResponseItemTypes)
	}
}

func TestWriteRecordIsOffByDefaultAndCreatesMode0600WhenEnabled(t *testing.T) {
	resetForTest()
	t.Setenv(capturePathEnv, "")
	if Enabled() {
		t.Fatal("capture should be disabled without an explicit path")
	}

	path := filepath.Join(t.TempDir(), "trace.jsonl")
	t.Setenv(capturePathEnv, path)
	if !Enabled() {
		t.Fatal("capture should be enabled with an explicit path")
	}
	if err := WriteRecord(Record{RequestOrdinal: 1, Stage: "inbound_client"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("capture permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestNormalizeErrorClass(t *testing.T) {
	tests := []struct {
		status int
		body   string
		want   string
	}{
		{400, `{"error":{"message":"No tool call found for function call output with call_id x"}}`, "tool_correlation"},
		{404, `{"error":{"code":"previous_response_not_found"}}`, "previous_response_not_found"},
		{429, `{}`, "rate_limit"},
		{500, `{}`, "upstream_5xx"},
		{200, `{}`, "none"},
	}
	for _, tt := range tests {
		if got := normalizeErrorClass(tt.status, []byte(tt.body)); got != tt.want {
			t.Errorf("normalizeErrorClass(%d, %s) = %q, want %q", tt.status, tt.body, got, tt.want)
		}
	}
}
