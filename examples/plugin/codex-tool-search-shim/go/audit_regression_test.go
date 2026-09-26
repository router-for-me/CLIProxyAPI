package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	clauderesponses "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/claude/openai/responses"
	geminiresponses "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/gemini/openai/responses"
	openairesponses "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/openai/openai/responses"
)

func TestAuditAdditionalToolsOnlyPreservesEagerTools(t *testing.T) {
	input := []byte(`{
		"input":[{"type":"additional_tools","tools":[
			{"type":"namespace","name":"functions","tools":[
				{"type":"function","name":"exec_command","defer_loading":false,"parameters":{"type":"object"}}
			]}
		]}]
	}`)

	out, changed, errRewrite := rewriteRequestBody(input)
	if errRewrite != nil {
		t.Fatalf("rewriteRequestBody() error = %v", errRewrite)
	}
	if changed || string(out) != string(input) {
		t.Fatalf("request without a client search contract changed: %s", out)
	}

	for name, translate := range map[string]func(string, []byte, bool) []byte{
		"chat":   openairesponses.ConvertOpenAIResponsesRequestToOpenAIChatCompletions,
		"claude": clauderesponses.ConvertOpenAIResponsesRequestToClaude,
		"gemini": geminiresponses.ConvertOpenAIResponsesRequestToGemini,
	} {
		t.Run(name, func(t *testing.T) {
			upstream := translate("audit-eager", input, false)
			if !strings.Contains(string(upstream), "exec_command") {
				t.Fatalf("%s translator lost eager tool: %s", name, upstream)
			}
		})
	}
}

func TestAuditClientSearchPrunesOnlyExplicitDeferredTools(t *testing.T) {
	input := []byte(`{
		"tools":[
			{"type":"tool_search","execution":"client","parameters":{"type":"object"}},
			{"type":"namespace","name":"functions","tools":[
				{"type":"function","name":"exec_command","defer_loading":false,"parameters":{"type":"object"}},
				{"type":"function","name":"lookup","defer_loading":true,"parameters":{"type":"object","properties":{"q":{"type":"string"}}}}
			]}
		]
	}`)

	out, changed, errRewrite := rewriteRequestBody(input)
	if errRewrite != nil || !changed {
		t.Fatalf("rewriteRequestBody() = (_, %v, %v), want changed", changed, errRewrite)
	}
	var root struct {
		Tools []map[string]any `json:"tools"`
	}
	if errUnmarshal := json.Unmarshal(out, &root); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal() error = %v", errUnmarshal)
	}
	if !hasFunctionNamed(anyTools(root.Tools), toolSearchName) {
		t.Fatalf("search declaration missing: %#v", root.Tools)
	}
	namespace := findNamespace(anyTools(root.Tools), "functions")
	if namespace == nil {
		t.Fatalf("namespace missing: %#v", root.Tools)
	}
	eagerFound := false
	if children, ok := namespace["tools"].([]any); ok {
		for _, rawChild := range children {
			child, okChild := rawChild.(map[string]any)
			if okChild && stringField(child, "name") == "exec_command" {
				eagerFound = true
				break
			}
		}
	}
	if !eagerFound {
		t.Fatalf("explicit eager tool was pruned: %#v", namespace)
	}
	entries, ok := namespace[deferredToolsKey].([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("deferred index = %#v", namespace[deferredToolsKey])
	}
	if entry, okEntry := entries[0].(map[string]any); !okEntry || stringField(entry, "name") != "lookup" {
		t.Fatalf("deferred entry = %#v", entries[0])
	}
}

func TestAuditFlatDiscoveryPromotesSchemaWithoutPrecisionLoss(t *testing.T) {
	input := []byte(`{
		"tools":[{"type":"tool_search","execution":"client","parameters":{"type":"object"}}],
		"input":[{"type":"tool_search_output","call_id":"s1","execution":"client","tools":[
			{"type":"function","name":"get_weather","parameters":{"type":"object","properties":{
				"limit":{"type":"integer","const":9007199254740993}
			}}}
		]}]
	}`)

	out, changed, errRewrite := rewriteRequestBody(input)
	if errRewrite != nil || !changed {
		t.Fatalf("rewriteRequestBody() = (_, %v, %v), want changed", changed, errRewrite)
	}
	var root struct {
		Tools []map[string]any `json:"tools"`
	}
	if errUnmarshal := json.Unmarshal(out, &root); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal() error = %v", errUnmarshal)
	}
	if !hasFunctionNamed(anyTools(root.Tools), "get_weather") {
		t.Fatalf("discovered schema missing: %s", out)
	}
	if !strings.Contains(string(out), "9007199254740993") {
		t.Fatalf("discovered integer precision changed: %s", out)
	}
}

func TestAuditServerSearchIsNotRewritten(t *testing.T) {
	input := []byte(`{"tools":[{"type":"tool_search","execution":"server","parameters":{"type":"object"}}]}`)
	out, changed, errRewrite := rewriteRequestBody(input)
	if errRewrite != nil || changed || string(out) != string(input) {
		t.Fatalf("server search rewrite = (%s, %v, %v)", out, changed, errRewrite)
	}
}

func TestAuditOrdinaryToolSearchResponseIsNotHijacked(t *testing.T) {
	const requestID = "audit-ordinary-tool-search"
	request := []byte(`{"tools":[{"type":"function","name":"tool_search","parameters":{"type":"object"}}]}`)
	if _, errIntercept := interceptRequest("request_after", mustJSON(t, map[string]any{
		"RequestID":    requestID,
		"SourceFormat": "openai-response",
		"ToFormat":     "openai",
		"Body":         request,
	})); errIntercept != nil {
		t.Fatalf("interceptRequest() error = %v", errIntercept)
	}

	raw, errResponse := interceptResponse(mustJSON(t, map[string]any{
		"RequestID":    requestID,
		"SourceFormat": "openai-response",
		"Body":         []byte(`{"type":"function_call","call_id":"c1","name":"tool_search","arguments":"{}"}`),
	}))
	if errResponse != nil {
		t.Fatalf("interceptResponse() error = %v", errResponse)
	}
	if body := decodeResponseBodyResult(t, raw); len(body) != 0 {
		t.Fatalf("ordinary tool_search response was rewritten: %s", body)
	}
}

func TestAuditAmbiguousAliasDoesNotResolveToAnotherTool(t *testing.T) {
	catalog := newToolCatalog()
	longName := strings.Repeat("x", 64)
	catalog.addIdentity("first", longName)
	catalog.addIdentity("second", longName)
	catalog.finalize()

	if _, ok := catalog.resolve(capToolName("first__" + longName)); ok {
		t.Fatal("ambiguous alias resolved to another namespace")
	}
}

func TestAuditPromotionCleanupPreservesLookalikeEagerTool(t *testing.T) {
	body := []byte(`{
		"tools":[{"type":"tool_search","execution":"client","parameters":{"type":"object"}}],
		"input":[{"type":"tool_search_output","call_id":"s1","execution":"client","tools":[
			{"type":"function","name":"discovered","parameters":{"type":"object"}}
		]}]
	}`)
	catalog := extractToolCatalog(body)
	if catalog == nil {
		t.Fatal("extractToolCatalog() = nil")
	}
	root := map[string]any{
		"tools": []any{
			map[string]any{"type": "function", "name": "cts_" + strings.Repeat("0", 48), "parameters": map[string]any{"type": "object"}},
		},
	}
	if _, errInject := injectDiscoveredTools(root, catalog); errInject != nil {
		t.Fatalf("injectDiscoveredTools() error = %v", errInject)
	}
	tools := root["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("tools = %#v, want eager lookalike and discovered alias", tools)
	}
	if name := tools[0].(map[string]any)["name"]; name != "cts_"+strings.Repeat("0", 48) {
		t.Fatalf("lookalike eager tool = %#v", name)
	}
}

func TestAuditStrictAdditionalToolsAndExpansionBudget(t *testing.T) {
	input := []byte(`{"input":[{"type":"additional_tools","tools":[
		{"type":"tool_search","execution":"client","parameters":{
			"type":"object","properties":{"query":{"type":"string"}},"required":[]
		}}
	]}]}`)
	out, changed, errNormalize := normalizeStrictNativeBody(input)
	if errNormalize != nil {
		t.Fatalf("normalizeStrictNativeBody() error = %v", errNormalize)
	}
	if !changed {
		t.Fatalf("strict normalize skipped additional_tools: %s", out)
	}
	if !strings.Contains(string(out), `"anyOf"`) {
		t.Fatalf("optional nullable schema not completed conservatively: %s", out)
	}

	recursive := []byte(fmt.Sprintf(`{"tools":[{"type":"function","name":"recursive","parameters":{
		"type":"object","properties":{"value":{"$ref":"#/$defs/Node"}},
		"$defs":{"Node":{"type":"object","properties":{"child":{"$ref":"#/$defs/Node"}}}}
	}}]}`))
	_, _, errRecursive := normalizeStrictNativeBody(recursive)
	if errRecursive == nil {
		t.Fatal("recursive strict schema was accepted without an error")
	}
}

func anyTools(values []map[string]any) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}
