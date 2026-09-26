package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestEagerToolBudgetRejectsInsteadOfDroppingTools(t *testing.T) {
	t.Cleanup(func() { _ = applyPluginConfig(nil) })
	if errConfigure := applyPluginConfig([]byte("max_active_tool_bytes: 160\n")); errConfigure != nil {
		t.Fatalf("applyPluginConfig() error = %v", errConfigure)
	}
	body, errBody := json.Marshal(map[string]any{
		"tools": []any{map[string]any{
			"type": "namespace", "name": "large",
			"tools": []any{map[string]any{
				"type": "function", "name": "eager",
				"parameters": map[string]any{"type": "object", "properties": map[string]any{
					"value": map[string]any{"type": "string"},
				}},
				"description": strings.Repeat("x", 500),
			}},
		}},
	})
	if errBody != nil {
		t.Fatalf("json.Marshal() error = %v", errBody)
	}
	raw, errIntercept := interceptRequest("request_after", mustJSON(t, map[string]any{
		"RequestID":    "budget-eager",
		"SourceFormat": "openai-response",
		"ToFormat":     "openai",
		"Body":         body,
	}))
	if errIntercept != nil {
		t.Fatalf("interceptRequest() error = %v", errIntercept)
	}
	response := decodeRequestInterceptResult(t, raw)
	if !response.Terminate || response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("budget response = %#v, want terminate 413", response)
	}
}

func TestLatestDiscoveryMustFitWithoutSilentPartialActivation(t *testing.T) {
	t.Cleanup(func() { _ = applyPluginConfig(nil) })
	if errConfigure := applyPluginConfig([]byte("max_active_tool_bytes: 360\n")); errConfigure != nil {
		t.Fatalf("applyPluginConfig() error = %v", errConfigure)
	}
	body := []byte(`{
		"tools":[{"type":"tool_search","execution":"client","parameters":{"type":"object"}}],
		"input":[{"type":"tool_search_output","call_id":"latest","execution":"client","tools":[
			{"type":"namespace","name":"large","tools":[
				{"type":"function","name":"result","parameters":{"type":"object","properties":{"value":{"type":"string"}}},"description":"` + strings.Repeat("y", 500) + `"}
			]}
		]}]
	}`)
	raw, errIntercept := interceptRequest("request_after", mustJSON(t, map[string]any{
		"RequestID":    "budget-latest",
		"SourceFormat": "openai-response",
		"ToFormat":     "openai",
		"Body":         body,
	}))
	if errIntercept != nil {
		t.Fatalf("interceptRequest() error = %v", errIntercept)
	}
	response := decodeRequestInterceptResult(t, raw)
	if !response.Terminate || response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("latest discovery response = %#v, want terminate 413", response)
	}
}

func TestHistoricalDiscoveryActivationIsBoundedButLatestRoundSurvives(t *testing.T) {
	t.Cleanup(func() { _ = applyPluginConfig(nil) })
	if errConfigure := applyPluginConfig([]byte("max_active_tool_bytes: 760\n")); errConfigure != nil {
		t.Fatalf("applyPluginConfig() error = %v", errConfigure)
	}
	outputs := make([]any, 0, 50)
	for index := 0; index < 50; index++ {
		outputs = append(outputs, map[string]any{
			"type":      "tool_search_output",
			"call_id":   fmt.Sprintf("search-%d", index),
			"execution": "client",
			"tools": []any{map[string]any{
				"type": "namespace", "name": fmt.Sprintf("server-%d", index),
				"tools": []any{map[string]any{
					"type": "function", "name": "lookup",
					"parameters": map[string]any{"type": "object", "properties": map[string]any{
						"query": map[string]any{"type": "string"},
					}},
				}},
			}},
		})
	}
	body, errMarshal := json.Marshal(map[string]any{
		"tools": []any{map[string]any{"type": "tool_search", "execution": "client", "parameters": map[string]any{"type": "object"}}},
		"input": outputs,
	})
	if errMarshal != nil {
		t.Fatalf("json.Marshal() error = %v", errMarshal)
	}
	raw, errIntercept := interceptRequest("request_after", mustJSON(t, map[string]any{
		"RequestID":    "budget-history",
		"SourceFormat": "openai-response",
		"ToFormat":     "openai",
		"Body":         body,
	}))
	if errIntercept != nil {
		t.Fatalf("interceptRequest() error = %v", errIntercept)
	}
	response := decodeRequestInterceptResult(t, raw)
	if response.Terminate {
		t.Fatalf("historical discovery terminated: %#v", response)
	}
	out := response.Body
	catalog := extractToolCatalog(body)
	latestAlias := catalog.aliasByID[toolIdentity{Namespace: "server-49", Name: "lookup", Kind: "function"}]
	oldestAlias := catalog.aliasByID[toolIdentity{Namespace: "server-0", Name: "lookup", Kind: "function"}]
	var root struct {
		Tools []any `json:"tools"`
	}
	if errUnmarshal := json.Unmarshal(out, &root); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal() error = %v", errUnmarshal)
	}
	encodedTools, errMarshalTools := json.Marshal(root.Tools)
	if errMarshalTools != nil {
		t.Fatalf("json.Marshal() error = %v", errMarshalTools)
	}
	if latestAlias == "" || !strings.Contains(string(encodedTools), latestAlias) {
		t.Fatalf("latest discovery was dropped: %s", out)
	}
	if oldestAlias == "" || strings.Contains(string(encodedTools), oldestAlias) {
		t.Fatalf("oldest discovery remained active: %s", out)
	}
	if len(encodedTools) >= 760 {
		t.Fatalf("activated tools exceeded budget: %d bytes", len(encodedTools))
	}
}
