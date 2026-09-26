package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestProbeDiagnosticsReportCountsBudgetAndStateUsage(t *testing.T) {
	clearRequestStates()
	t.Cleanup(clearRequestStates)
	t.Cleanup(func() { _ = applyPluginConfig(nil) })
	logPath := filepath.Join(t.TempDir(), "structure.jsonl")
	config, errConfig := json.Marshal(map[string]any{
		"config_yaml": []byte(fmt.Sprintf("diagnostics_log_path: %q\n", logPath)),
	})
	if errConfig != nil {
		t.Fatalf("json.Marshal() error = %v", errConfig)
	}
	if _, errConfigure := handleMethod("plugin.reconfigure", config); errConfigure != nil {
		t.Fatalf("handleMethod() error = %v", errConfigure)
	}

	body := []byte(`{
		"tools":[
			{"type":"tool_search","execution":"client","parameters":{"type":"object"}},
			{"type":"namespace","name":"deferred","tools":[
				{"type":"function","name":"hidden","defer_loading":true,"parameters":{"type":"object"}}
			]}
		],
		"input":[{"type":"tool_search_output","call_id":"s1","execution":"client","tools":[
			{"type":"function","name":"discovered","parameters":{"type":"object"}}
		]}]
	}`)
	raw, errIntercept := interceptRequest("request_after", mustJSON(t, map[string]any{
		"RequestID":    "diagnostics-counts",
		"SourceFormat": "openai-response",
		"ToFormat":     "openai",
		"Headers":      http.Header{probeRequestHeader: []string{"1"}},
		"Body":         body,
	}))
	if errIntercept != nil {
		t.Fatalf("interceptRequest() error = %v", errIntercept)
	}
	if response := decodeRequestInterceptResult(t, raw); response.Terminate {
		t.Fatalf("probe request terminated: %#v", response)
	}

	file, errOpen := os.Open(logPath)
	if errOpen != nil {
		t.Fatalf("os.Open() error = %v", errOpen)
	}
	defer func() {
		if errClose := file.Close(); errClose != nil {
			t.Errorf("file.Close() error = %v", errClose)
		}
	}()
	scanner := bufio.NewScanner(file)
	var records []structureLog
	for scanner.Scan() {
		var record structureLog
		if errUnmarshal := json.Unmarshal(scanner.Bytes(), &record); errUnmarshal != nil {
			t.Fatalf("json.Unmarshal() error = %v", errUnmarshal)
		}
		records = append(records, record)
	}
	if errScanner := scanner.Err(); errScanner != nil {
		t.Fatalf("scanner.Err() error = %v", errScanner)
	}
	if len(records) != 2 {
		t.Fatalf("diagnostic records = %d, want 2", len(records))
	}
	if records[0].DeferredTools != 1 || records[0].DiscoveredTools != 1 {
		t.Fatalf("request counts = %#v", records[0])
	}
	if records[1].StateEntries != 1 || records[1].BudgetBytes <= 0 {
		t.Fatalf("upstream state/budget = %#v", records[1])
	}
}
