package responsestools

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func reviewRoutePolicy(search ClientSearchMode, custom CustomToolsMode) RoutePolicy {
	return RoutePolicy{
		ClientSearch:  search,
		CustomTools:   custom,
		CustomGrammar: CustomGrammarReject,
		Schema:        SchemaPolicy{LocalRefs: LocalRefsPreserve},
	}
}

func decodeReviewRequest(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	return root
}

func responseStatus(err error) int {
	var statusErr interface{ StatusCode() int }
	if errors.As(err, &statusErr) {
		return statusErr.StatusCode()
	}
	return 0
}

func TestPrepareKeepsDeferredCustomOutOfInitialActivation(t *testing.T) {
	body := []byte(`{
		"tools": [
			{"type":"tool_search"},
			{"type":"custom","name":"apply_patch","defer_loading":true}
		],
		"input":[{"type":"message","role":"user","content":"find the patch tool"}]
	}`)
	prepared, err := Prepare(body, reviewRoutePolicy(ClientSearchBridge, CustomToolsFunction), DefaultLimits(), NewLimiter(DefaultLimits()))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer prepared.Attempt.Close()

	root := decodeReviewRequest(t, prepared.Body)
	tools, _ := root["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("active tools = %d, want only the search function before discovery: %s", len(tools), prepared.Body)
	}
	search, _ := tools[0].(map[string]any)
	if search["type"] != "function" || search["name"] != ToolSearchName {
		t.Fatalf("active declaration = %v, want bridged search function", search)
	}
}

func TestPrepareBridgesCustomToolsDiscoveredByClientSearch(t *testing.T) {
	body := []byte(`{
		"tools":[{"type":"tool_search"}],
		"input":[
			{"type":"tool_search_call","call_id":"search_1","arguments":{"query":"patch"}},
			{"type":"tool_search_output","call_id":"search_1","tools":[
				{"type":"custom","name":"apply_patch","description":"Patch","format":{"type":"text"}}
			]}
		]
	}`)
	prepared, err := Prepare(body, reviewRoutePolicy(ClientSearchBridge, CustomToolsFunction), DefaultLimits(), NewLimiter(DefaultLimits()))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer prepared.Attempt.Close()

	root := decodeReviewRequest(t, prepared.Body)
	tools, _ := root["tools"].([]any)
	var customAlias string
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		if tool["name"] == ToolSearchName {
			continue
		}
		if tool["type"] != "function" {
			t.Fatalf("discovered custom declaration was not bridged: %v", tool)
		}
		customAlias, _ = tool["name"].(string)
	}
	if customAlias == "" {
		t.Fatalf("discovered custom tool missing from active declarations: %s", prepared.Body)
	}
	identity, ok := prepared.Attempt.bridge.Resolve(customAlias)
	if !ok || identity != (ToolIdentity{Namespace: "", Name: "apply_patch", Kind: ToolKindCustom}) {
		t.Fatalf("custom alias %q resolves to %v, %v", customAlias, identity, ok)
	}

	restored, err := prepared.Attempt.RewriteResponse([]byte(fmt.Sprintf(
		`{"output":[{"type":"function_call","id":"fc_fixture_discovered","name":%q,"call_id":"patch_1","arguments":"{\"input\":\"exact patch\"}"}]}`,
		customAlias,
	)))
	if err != nil {
		t.Fatalf("restore response: %v", err)
	}
	var response map[string]any
	if err := json.Unmarshal(restored, &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	items, _ := response["output"].([]any)
	item, _ := items[0].(map[string]any)
	if item["type"] != "custom_tool_call" || item["name"] != "apply_patch" || item["input"] != "exact patch" {
		t.Fatalf("discovered custom call did not restore: %v", item)
	}
}

func TestPrepareBridgesNamespacedDiscoveredCustomOnce(t *testing.T) {
	body := []byte(`{
		"tools":[{"type":"tool_search"}],
		"input":[
			{"type":"tool_search_call","call_id":"search_ns_1","arguments":{"query":"patch"}},
			{"type":"tool_search_output","call_id":"search_ns_1","tools":[
				{"type":"namespace","name":"fs","tools":[
					{"type":"custom","name":"apply_patch","description":"Patch","format":{"type":"text"}}
				]}
			]}
		]
	}`)
	prepared, err := Prepare(body, reviewRoutePolicy(ClientSearchBridge, CustomToolsFunction), DefaultLimits(), NewLimiter(DefaultLimits()))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer prepared.Attempt.Close()

	alias := CustomAliasFor("fs", "apply_patch")
	root := decodeReviewRequest(t, prepared.Body)
	var found bool
	var visit func(any)
	visit = func(value any) {
		switch current := value.(type) {
		case []any:
			for _, child := range current {
				visit(child)
			}
		case map[string]any:
			if current["type"] == "function" && current["name"] == alias {
				found = true
			}
			visit(current["tools"])
		}
	}
	visit(root["tools"])
	if !found {
		t.Fatalf("namespaced custom declaration must use its one stable alias %q: %s", alias, prepared.Body)
	}
	identity, ok := prepared.Attempt.bridge.ResolveWireAlias(alias)
	if !ok || identity != (ToolIdentity{Namespace: "fs", Name: "apply_patch", Kind: ToolKindCustom}) {
		t.Fatalf("alias %q resolves to %v, %v", alias, identity, ok)
	}
	restored, err := prepared.Attempt.RewriteResponse([]byte(fmt.Sprintf(
		`{"output":[{"type":"function_call","id":"fc_fixture_namespaced","name":%q,"call_id":"patch_ns_1","arguments":"{\"input\":\"exact patch\"}"}]}`,
		alias,
	)))
	if err != nil {
		t.Fatalf("restore namespaced custom response: %v", err)
	}
	var response map[string]any
	if err := json.Unmarshal(restored, &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	items, _ := response["output"].([]any)
	item, _ := items[0].(map[string]any)
	if item["type"] != "custom_tool_call" || item["name"] != "apply_patch" ||
		item["namespace"] != "fs" || item["input"] != "exact patch" {
		t.Fatalf("namespaced custom response identity not restored: %v", item)
	}
}

func TestPrepareConvertsCustomOnlyHistoryWithoutDeclarations(t *testing.T) {
	body := []byte(`{
		"input":[
			{"type":"custom_tool_call","name":"apply_patch","call_id":"patch_1","input":"exact patch"},
			{"type":"custom_tool_call_output","call_id":"patch_1","output":"ok"}
		]
	}`)
	prepared, err := Prepare(body, reviewRoutePolicy(ClientSearchInherit, CustomToolsFunction), DefaultLimits(), NewLimiter(DefaultLimits()))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer prepared.Attempt.Close()
	if prepared.Attempt == nil {
		t.Fatal("history-only custom request did not create an adaptation attempt")
	}
	if string(prepared.Body) == string(body) {
		t.Fatal("history-only custom request was not converted")
	}
	root := decodeReviewRequest(t, prepared.Body)
	input, _ := root["input"].([]any)
	call, _ := input[0].(map[string]any)
	if call["type"] != "function_call" {
		t.Fatalf("history call type = %v, want function_call", call["type"])
	}
}

func TestPrepareRejectsOpaqueCustomHistory(t *testing.T) {
	body := []byte(`{
		"previous_response_id":"resp_remote",
		"tools":[{"type":"custom","name":"apply_patch"}],
		"input":[{"type":"message","role":"user","content":"continue"}]
	}`)
	_, err := Prepare(body, reviewRoutePolicy(ClientSearchInherit, CustomToolsFunction), DefaultLimits(), NewLimiter(DefaultLimits()))
	if err == nil {
		t.Fatal("expected opaque history to be rejected when custom bridging is required")
	}
	if got := responseStatus(err); got != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %v", got, err)
	}
}

func TestPrepareRejectsOpaqueHistoryForSearchBridge(t *testing.T) {
	body := []byte(`{
		"previous_response_id":"resp_remote",
		"tools":[{"type":"tool_search"}],
		"input":[{"type":"message","role":"user","content":"continue"}]
	}`)
	_, err := Prepare(body, reviewRoutePolicy(ClientSearchBridge, CustomToolsInherit), DefaultLimits(), NewLimiter(DefaultLimits()))
	if err == nil || responseStatus(err) != http.StatusUnprocessableEntity {
		t.Fatalf("search bridge accepted opaque previous response: %v", err)
	}
}

func TestPrepareRejectsOpaqueHistoryForCustomStrip(t *testing.T) {
	body := []byte(`{
		"previous_response_id":"resp_remote",
		"tools":[{"type":"custom","name":"apply_patch"}],
		"input":[{"type":"message","role":"user","content":"continue"}]
	}`)
	_, err := Prepare(body, reviewRoutePolicy(ClientSearchInherit, CustomToolsStrip), DefaultLimits(), NewLimiter(DefaultLimits()))
	if err == nil || responseStatus(err) != http.StatusUnprocessableEntity {
		t.Fatalf("custom strip accepted opaque previous response: %v", err)
	}
}

func TestPrepareRejectsCustomHistoryOnlyForRejectPolicy(t *testing.T) {
	body := []byte(`{
		"input":[
			{"type":"custom_tool_call","name":"apply_patch","call_id":"patch_1","input":"exact patch"},
			{"type":"custom_tool_call_output","call_id":"patch_1","output":"ok"}
		]
	}`)
	_, err := Prepare(body, reviewRoutePolicy(ClientSearchInherit, CustomToolsReject), DefaultLimits(), NewLimiter(DefaultLimits()))
	if err == nil || responseStatus(err) != http.StatusUnprocessableEntity {
		t.Fatalf("custom history-only request bypassed reject policy: %v", err)
	}
}

func TestPrepareAllowsOpaqueHistoryOnNativePassthroughRoute(t *testing.T) {
	body := []byte(`{
		"previous_response_id":"resp_remote",
		"tools":[{"type":"custom","name":"apply_patch"}],
		"input":[{"type":"message","role":"user","content":"continue"}]
	}`)
	policy := reviewRoutePolicy(ClientSearchNative, CustomToolsInherit)
	prepared, err := Prepare(body, policy, DefaultLimits(), NewLimiter(DefaultLimits()))
	if err != nil {
		t.Fatalf("native passthrough: %v", err)
	}
	if prepared.Attempt != nil || string(prepared.Body) != string(body) {
		t.Fatal("native opaque history was unexpectedly rewritten")
	}
}

func TestPrepareRejectsUnpairedCustomToolOutput(t *testing.T) {
	body := []byte(`{
		"input":[
			{"type":"custom_tool_call_output","call_id":"missing_call","output":"ok"}
		]
	}`)
	_, err := Prepare(body, reviewRoutePolicy(ClientSearchInherit, CustomToolsFunction), DefaultLimits(), NewLimiter(DefaultLimits()))
	if err == nil {
		t.Fatal("orphaned custom tool output was accepted")
	}
	if got := responseStatus(err); got != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %v", got, err)
	}
}

func TestAttemptRejectsMalformedCustomResponseArguments(t *testing.T) {
	body := []byte(`{"tools":[{"type":"custom","name":"apply_patch"}],"input":[]}`)
	prepared, err := Prepare(body, reviewRoutePolicy(ClientSearchInherit, CustomToolsFunction), DefaultLimits(), NewLimiter(DefaultLimits()))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer prepared.Attempt.Close()
	alias, ok := prepared.Attempt.bridge.Alias(ToolIdentity{Name: "apply_patch", Kind: ToolKindCustom})
	if !ok {
		t.Fatal("custom alias missing")
	}
	response := []byte(fmt.Sprintf(
		`{"output":[{"type":"function_call","name":%q,"call_id":"patch_1","arguments":"{\"input\":\"first\",\"input\":\"second\"}"}]}`,
		alias,
	))
	if _, err := prepared.Attempt.RewriteResponse(response); err == nil {
		t.Fatal("malformed custom arguments were returned as a successful response")
	} else if got := responseStatus(err); got != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502: %v", got, err)
	}
}

func TestCustomBridgeDoesNotStealSameNamedOrdinaryFunction(t *testing.T) {
	body := []byte(`{
		"tools":[
			{"type":"function","name":"patch","parameters":{"type":"object"}},
			{"type":"custom","name":"patch"}
		],
		"input":[]
	}`)
	prepared, err := Prepare(body, reviewRoutePolicy(ClientSearchInherit, CustomToolsFunction), DefaultLimits(), NewLimiter(DefaultLimits()))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer prepared.Attempt.Close()

	ordinaryResponse := []byte(`{"output":[{"type":"function_call","name":"patch","call_id":"function_1","arguments":"{\"input\":\"ordinary payload\"}"}]}`)
	restored, err := prepared.Attempt.RewriteResponse(ordinaryResponse)
	if err != nil {
		t.Fatalf("rewrite response: %v", err)
	}
	var response map[string]any
	if err := json.Unmarshal(restored, &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	items, _ := response["output"].([]any)
	item, _ := items[0].(map[string]any)
	if item["type"] != "function_call" || item["name"] != "patch" {
		t.Fatalf("ordinary function was mistaken for custom: %v", item)
	}
}

func TestCustomOnlyToolSearchNamedFunctionRemainsOrdinaryAcrossResponseAndStream(t *testing.T) {
	body := []byte(`{
		"tools":[
			{"type":"function","name":"tool_search","parameters":{"type":"object"}},
			{"type":"custom","name":"apply_patch","description":"Patch"}
		],
		"input":[]
	}`)
	prepared, err := Prepare(body, reviewRoutePolicy(ClientSearchInherit, CustomToolsFunction), DefaultLimits(), NewLimiter(DefaultLimits()))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer prepared.Attempt.Close()

	ordinaryResponse := []byte(`{"output":[{"type":"function_call","name":"tool_search","call_id":"ordinary_1","arguments":"{}"}]}`)
	restored, err := prepared.Attempt.RewriteResponse(ordinaryResponse)
	if err != nil {
		t.Fatalf("rewrite response: %v", err)
	}
	var response map[string]any
	if err := json.Unmarshal(restored, &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	items, _ := response["output"].([]any)
	item, _ := items[0].(map[string]any)
	if item["type"] != "function_call" || item["name"] != "tool_search" {
		t.Fatalf("ordinary tool_search function was rewritten as built-in search: %v", item)
	}

	feed := NewStreamFeed(prepared.Attempt.contract, prepared.Attempt.bridge, prepared.Attempt.lease, prepared.Attempt.limits)
	frames, err := feed.Feed([]byte(`{"type":"response.output_item.added","item":{"type":"function_call","id":"item_ordinary","name":"tool_search","call_id":"ordinary_1","output_index":0}}`))
	if err != nil {
		t.Fatalf("feed ordinary function event: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("output_item.added frames = %d, want 1", len(frames))
	}
	var event map[string]any
	if err := json.Unmarshal(frames[0], &event); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	eventItem, _ := event["item"].(map[string]any)
	if eventItem["type"] != "function_call" || eventItem["name"] != "tool_search" {
		t.Fatalf("ordinary streaming function was rewritten as built-in search: %v", eventItem)
	}
}

func TestAttemptRestoresOnlySyntheticSearchNulls(t *testing.T) {
	body := []byte(`{
		"tools":[{"type":"tool_search","parameters":{
			"type":"object",
			"properties":{
				"query":{"type":"string"},
				"limit":{"type":"integer"},
				"cursor":{"anyOf":[{"type":"string"},{"type":"null"}]}
			},
			"required":["query"]
		}}],
		"input":[]
	}`)
	policy := reviewRoutePolicy(ClientSearchBridge, CustomToolsInherit)
	policy.Schema.SearchRequired = SearchRequiredComplete
	prepared, err := Prepare(body, policy, DefaultLimits(), NewLimiter(DefaultLimits()))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer prepared.Attempt.Close()
	response, err := prepared.Attempt.RewriteResponse([]byte(`{"output":[{"type":"function_call","id":"fc_fixture_nulls","name":"tool_search","call_id":"search_1","arguments":"{\"query\":\"x\",\"limit\":null,\"cursor\":null}"}]}`))
	if err != nil {
		t.Fatalf("rewrite response: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(response, &decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	items, _ := decoded["output"].([]any)
	item, _ := items[0].(map[string]any)
	arguments, _ := item["arguments"].(map[string]any)
	if _, exists := arguments["limit"]; exists {
		t.Fatalf("synthetic null for optional limit must be removed: %v", arguments)
	}
	if cursor, exists := arguments["cursor"]; !exists || cursor != nil {
		t.Fatalf("originally nullable cursor must remain null: %v", arguments)
	}
}

func TestAttemptRestoresNestedSyntheticSearchNulls(t *testing.T) {
	body := []byte(`{
		"tools":[{"type":"tool_search","parameters":{
			"type":"object",
			"properties":{"filter":{
				"type":"object",
				"properties":{"limit":{"type":"integer"}},
				"required":[]
			}},
			"required":["filter"]
		}}],
		"input":[]
	}`)
	policy := reviewRoutePolicy(ClientSearchBridge, CustomToolsInherit)
	policy.Schema.SearchRequired = SearchRequiredComplete
	prepared, err := Prepare(body, policy, DefaultLimits(), NewLimiter(DefaultLimits()))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer prepared.Attempt.Close()

	response, err := prepared.Attempt.RewriteResponse([]byte(`{"output":[{"type":"function_call","id":"fc_fixture_nested","name":"tool_search","call_id":"search_1","arguments":"{\"filter\":{\"limit\":null}}"}]}`))
	if err != nil {
		t.Fatalf("rewrite response: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(response, &decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	items, _ := decoded["output"].([]any)
	item, _ := items[0].(map[string]any)
	arguments, _ := item["arguments"].(map[string]any)
	filter, _ := arguments["filter"].(map[string]any)
	if _, exists := filter["limit"]; exists {
		t.Fatalf("nested synthetic null must be removed: %v", arguments)
	}
}

func TestAttemptDoesNotRestoreSyntheticNullWithoutSchemaCompletion(t *testing.T) {
	body := []byte(`{"tools":[{"type":"tool_search","parameters":{"type":"object","properties":{"limit":{"type":"integer"}},"required":[]}}],"input":[]}`)
	prepared, err := Prepare(body, reviewRoutePolicy(ClientSearchBridge, CustomToolsInherit), DefaultLimits(), NewLimiter(DefaultLimits()))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer prepared.Attempt.Close()

	response, err := prepared.Attempt.RewriteResponse([]byte(`{"output":[{"type":"function_call","id":"fc_fixture_no_completion","name":"tool_search","call_id":"search_1","arguments":"{\"limit\":null}"}]}`))
	if err != nil {
		t.Fatalf("rewrite response: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(response, &decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	items, _ := decoded["output"].([]any)
	item, _ := items[0].(map[string]any)
	arguments, _ := item["arguments"].(map[string]any)
	if value, exists := arguments["limit"]; !exists || value != nil {
		t.Fatalf("null was removed without request schema completion: %v", arguments)
	}
}

func TestAttemptPreservesNullAcceptedByUnconstrainedSchema(t *testing.T) {
	body := []byte(`{"tools":[{"type":"tool_search","parameters":{"type":"object","properties":{"opaque":{}},"required":[]}}],"input":[]}`)
	policy := reviewRoutePolicy(ClientSearchBridge, CustomToolsInherit)
	policy.Schema.SearchRequired = SearchRequiredComplete
	prepared, err := Prepare(body, policy, DefaultLimits(), NewLimiter(DefaultLimits()))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer prepared.Attempt.Close()

	response, err := prepared.Attempt.RewriteResponse([]byte(`{"output":[{"type":"function_call","id":"fc_fixture_opaque","name":"tool_search","call_id":"search_1","arguments":"{\"opaque\":null}"}]}`))
	if err != nil {
		t.Fatalf("rewrite response: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(response, &decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	items, _ := decoded["output"].([]any)
	item, _ := items[0].(map[string]any)
	arguments, _ := item["arguments"].(map[string]any)
	if value, exists := arguments["opaque"]; !exists || value != nil {
		t.Fatalf("originally accepted null was removed: %v", arguments)
	}
}

func TestPrepareInlinesSearchSchemaRefsBeforeCompletion(t *testing.T) {
	body := []byte(`{
		"tools":[{
			"type":"tool_search",
			"parameters":{
				"type":"object",
				"properties":{"query":{"$ref":"#/$defs/Query"}},
				"required":[],
				"$defs":{"Query":{"type":"string"}}
			}
		}],
		"input":[]
	}`)
	policy := reviewRoutePolicy(ClientSearchBridge, CustomToolsInherit)
	policy.Schema.SearchRequired = SearchRequiredComplete
	policy.Schema.LocalRefs = LocalRefsInline
	prepared, err := Prepare(body, policy, DefaultLimits(), NewLimiter(DefaultLimits()))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer prepared.Attempt.Close()
	root := decodeReviewRequest(t, prepared.Body)
	tools := root["tools"].([]any)
	parameters := tools[0].(map[string]any)["parameters"].(map[string]any)
	if _, exists := parameters["$defs"]; exists {
		t.Fatalf("local definitions were not inlined: %v", parameters)
	}
	required := parameters["required"].([]any)
	if len(required) != 1 || required[0] != "query" {
		t.Fatalf("required = %v, want [query]", required)
	}
	query := parameters["properties"].(map[string]any)["query"].(map[string]any)
	if entries, ok := query["anyOf"].([]any); !ok || len(entries) != 2 {
		t.Fatalf("inlined optional query was not made nullable: %v", query)
	}
}

func TestNativeSearchResponseRestoresSyntheticNulls(t *testing.T) {
	body := []byte(`{"tools":[{"type":"tool_search","parameters":{"type":"object","properties":{"limit":{"type":"integer"}},"required":[]}}],"input":[]}`)
	policy := reviewRoutePolicy(ClientSearchNative, CustomToolsInherit)
	policy.Schema.SearchRequired = SearchRequiredComplete
	prepared, err := Prepare(body, policy, DefaultLimits(), NewLimiter(DefaultLimits()))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer prepared.Attempt.Close()

	response, err := prepared.Attempt.RewriteResponse([]byte(`{"output":[{"type":"tool_search_call","execution":"client","arguments":{"limit":null}}]}`))
	if err != nil {
		t.Fatalf("rewrite response: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(response, &decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	items, _ := decoded["output"].([]any)
	item, _ := items[0].(map[string]any)
	arguments, _ := item["arguments"].(map[string]any)
	if _, exists := arguments["limit"]; exists {
		t.Fatalf("native response kept a synthetic null: %v", arguments)
	}
}

func TestSchemaPolicyAppliesOnNativePassThroughRoute(t *testing.T) {
	body := []byte(`{"tools":[{"type":"tool_search","parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":[]}}],"input":[]}`)
	policy := reviewRoutePolicy(ClientSearchNative, CustomToolsInherit)
	policy.Schema.SearchRequired = SearchRequiredComplete
	prepared, err := Prepare(body, policy, DefaultLimits(), NewLimiter(DefaultLimits()))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if string(prepared.Body) == string(body) {
		t.Fatal("explicit search schema policy was ignored on a native route")
	}
	prepared.Attempt.Close()
}

func TestPrepareAppliesDeclarationBudgetToCustomOnlyRequest(t *testing.T) {
	body := []byte(`{"tools":[{"type":"custom","name":"apply_patch","description":"a long enough description"}],"input":[]}`)
	limits := DefaultLimits()
	limits.MaxActiveToolBytes = 8
	_, err := Prepare(body, reviewRoutePolicy(ClientSearchInherit, CustomToolsFunction), limits, NewLimiter(limits))
	if err == nil {
		t.Fatal("custom-only active declaration exceeded its budget without rejection")
	}
	if got := responseStatus(err); got != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413: %v", got, err)
	}
}

// A tool name that merely starts with its namespace ("fs_read" under
// namespace "fs") is not already qualified. Treating it as qualified made the
// outbound guard require a name no provider translator ever emits.
func TestRawQualifiedToolNameDoesNotTreatPrefixedNameAsQualified(t *testing.T) {
	if got := RawQualifiedToolName("fs", "fs_read"); got != "fs__fs_read" {
		t.Fatalf("RawQualifiedToolName(fs, fs_read) = %q, want fs__fs_read", got)
	}
	if got := RawQualifiedToolName("fs", "fs"); got != "fs" {
		t.Fatalf("RawQualifiedToolName(fs, fs) = %q, want the unchanged name", got)
	}
	if got := RawQualifiedToolName("mcp__acme", "mcp__acme__search"); got != "mcp__acme__search" {
		t.Fatalf("RawQualifiedToolName(mcp__acme, mcp__acme__search) = %q, want the unchanged name", got)
	}
}

// Replayed custom history carries item type "custom_tool_call", not the
// declaration type "custom". Normalizing it as a function identity pointed
// history at the wrong alias whenever a discovery round declared a function
// and a custom tool under the same namespace and name.
func TestPrepareKeepsCustomHistoryOnTheCustomAlias(t *testing.T) {
	body := []byte(`{
		"tools":[{"type":"tool_search"}],
		"input":[
			{"type":"tool_search_call","call_id":"s","arguments":{}},
			{"type":"tool_search_output","call_id":"s","tools":[
				{"type":"function","namespace":"ns","name":"run","parameters":{"type":"object"}},
				{"type":"custom","namespace":"ns","name":"run"}
			]},
			{"type":"custom_tool_call","call_id":"c","namespace":"ns","name":"run","input":"hello"},
			{"type":"custom_tool_call_output","call_id":"c","output":"ok"}
		]
	}`)
	prepared, err := Prepare(body, reviewRoutePolicy(ClientSearchBridge, CustomToolsFunction), DefaultLimits(), NewLimiter(DefaultLimits()))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer prepared.Attempt.Close()

	want := CustomAliasFor("ns", "run")
	root := decodeReviewRequest(t, prepared.Body)
	items, _ := root["input"].([]any)
	if len(items) != 4 {
		t.Fatalf("input items = %d, want 4: %s", len(items), prepared.Body)
	}
	call, _ := items[2].(map[string]any)
	if got, _ := call["name"].(string); got != want {
		t.Fatalf("custom history call name = %q, want the custom bridge alias %q: %s", got, want, prepared.Body)
	}
	declared := false
	for _, rawTool := range root["tools"].([]any) {
		tool, _ := rawTool.(map[string]any)
		if name, _ := tool["name"].(string); name == want {
			declared = true
		}
	}
	if !declared {
		t.Fatalf("custom declaration alias %q missing from the wire body: %s", want, prepared.Body)
	}
}

// A discovery round may legitimately return a tool literally named
// "tool_search". It must receive its own alias instead of being dropped for
// colliding with the bridge's search entry point.
func TestPrepareActivatesDiscoveredToolNamedLikeTheSearchEntryPoint(t *testing.T) {
	body := []byte(`{
		"tools":[{"type":"tool_search"}],
		"input":[
			{"type":"tool_search_call","call_id":"s","arguments":{}},
			{"type":"tool_search_output","call_id":"s","tools":[
				{"type":"function","name":"tool_search","description":"Search local documents","parameters":{"type":"object"}}
			]}
		]
	}`)
	prepared, err := Prepare(body, reviewRoutePolicy(ClientSearchBridge, CustomToolsInherit), DefaultLimits(), NewLimiter(DefaultLimits()))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer prepared.Attempt.Close()

	root := decodeReviewRequest(t, prepared.Body)
	tools, _ := root["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("active tools = %d, want the search entry point plus the discovered tool: %s", len(tools), prepared.Body)
	}
	names := map[string]bool{}
	for _, rawTool := range tools {
		tool, _ := rawTool.(map[string]any)
		name, _ := tool["name"].(string)
		names[name] = true
	}
	if !names[ToolSearchName] {
		t.Fatalf("search entry point missing: %s", prepared.Body)
	}
	if names[ToolSearchName] && len(names) != 2 {
		t.Fatalf("discovered tool collided with the search alias: %s", prepared.Body)
	}
	discovered := ""
	for name := range names {
		if name != ToolSearchName {
			discovered = name
		}
	}
	if discovered == "" {
		t.Fatalf("discovered tool was dropped: %s", prepared.Body)
	}
	output, _ := root["input"].([]any)[1].(map[string]any)
	if got, _ := output["output"].(string); got == "" || !strings.Contains(got, discovered) {
		t.Fatalf("discovery manifest %q does not carry the activated alias %q", got, discovered)
	}
}
