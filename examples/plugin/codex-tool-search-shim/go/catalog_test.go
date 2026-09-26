package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRewriteRequestBodyPrunesConnectorSchemas(t *testing.T) {
	input := []byte(`{
		"tools": [
			{"type":"function","name":"exec_command","parameters":{"type":"object"}},
			{"type":"namespace","name":"mcp__codex_apps__github","tools":[
				{"type":"function","name":"_get_user_login","parameters":{"type":"object","properties":{"username":{"type":"string"}}},"description":"connector schema marker"},
				{"type":"function","name":"_get_repo","parameters":{"type":"object"}}
			]}
		],
		"input": [
			{"type":"additional_tools","tools":[
				{"type":"namespace","name":"mcp__codex_apps__slack","tools":[
					{"type":"function","name":"_slack_send_message","parameters":{"type":"object"}}
				]}
			]}
		]
	}`)

	out, changed, errRewrite := rewriteRequestBody(input)
	if errRewrite != nil || !changed {
		t.Fatalf("rewriteRequestBody() = (_, %v, %v), want changed", changed, errRewrite)
	}
	if strings.Contains(string(out), "connector schema marker") ||
		strings.Contains(string(out), `"username":{"type":"string"}`) {
		t.Fatalf("connector schema leaked into request: %s", out)
	}

	var request struct {
		Tools []any            `json:"tools"`
		Input []map[string]any `json:"input"`
	}
	if errUnmarshal := json.Unmarshal(out, &request); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal() error = %v", errUnmarshal)
	}
	if !hasFunctionNamed(request.Tools, toolSearchName) {
		t.Fatalf("tool_search declaration missing: %#v", request.Tools)
	}
	github := findNamespace(request.Tools, "mcp__codex_apps__github")
	if github == nil {
		t.Fatal("github namespace missing")
	}
	if _, exists := github["tools"]; exists {
		t.Fatalf("github children were not pruned: %#v", github)
	}
	if len(deferredEntries(t, github)) != 2 {
		t.Fatalf("github deferred metadata = %#v", github[deferredToolsKey])
	}
	additionalNamespace := findNamespace(request.Input[0]["tools"], "mcp__codex_apps__slack")
	if additionalNamespace == nil {
		t.Fatal("additional slack namespace missing")
	}
	if _, exists := additionalNamespace["tools"]; exists {
		t.Fatalf("slack children were not pruned: %#v", additionalNamespace)
	}

	second, changedSecond, errSecond := rewriteRequestBody(out)
	if errSecond != nil || changedSecond {
		t.Fatalf("second rewrite = (_, %v, %v), want unchanged", changedSecond, errSecond)
	}
	if string(second) != string(out) {
		t.Fatalf("second rewrite changed bytes: %s", second)
	}
}

func TestFirstRequestKeepsDeferredSchemasOutOfUpstreamTools(t *testing.T) {
	input := []byte(`{
		"tools":[
			{"type":"function","name":"exec_command","parameters":{}},
			{"type":"namespace","name":"mcp__codex_apps__github","tools":[
				{"type":"function","name":"_get_user_login","description":"schema marker","parameters":{"type":"object","properties":{"username":{"type":"string"}}}},
				{"type":"function","name":"_get_repo","parameters":{"type":"object"}}
			]}
		]
	}`)
	out, changed, errRewrite := rewriteRequestBody(input)
	if errRewrite != nil || !changed {
		t.Fatalf("rewriteRequestBody() = (_, %v, %v), want changed", changed, errRewrite)
	}
	if strings.Contains(string(out), "schema marker") {
		t.Fatalf("deferred schema leaked into request: %s", out)
	}
	var root struct {
		Tools []map[string]any `json:"tools"`
	}
	if errUnmarshal := json.Unmarshal(out, &root); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal() error = %v", errUnmarshal)
	}
	for _, tool := range root.Tools {
		if tool["name"] == "mcp__codex_apps__github___get_user_login" ||
			tool["name"] == "mcp__codex_apps__github___get_repo" {
			t.Fatalf("undiscovered child was promoted: %#v", tool)
		}
	}
}

func TestSearchOutputPromotesOnlyDiscoveredChild(t *testing.T) {
	input := []byte(`{
		"tools":[
			{"type":"namespace","name":"mcp__codex_apps__github","tools":[
				{"type":"function","name":"_get_user_login","description":"get login","parameters":{"type":"object","properties":{"username":{"type":"string"}}}},
				{"type":"function","name":"_get_repo","parameters":{"type":"object"}}
			]},
			{"type":"namespace","name":"mcp__codex_apps__slack","tools":[
				{"type":"function","name":"_slack_send_message","parameters":{"type":"object"}}
			]}
		],
		"input":[
			{"type":"tool_search_output","call_id":"search-1","execution":"client","status":"completed","tools":[
				{"type":"namespace","name":"mcp__codex_apps__github","tools":[
					{"type":"function","name":"_get_user_login","description":"get login","parameters":{"type":"object","properties":{"username":{"type":"string"}}}}
				]}
			]}
		]
	}`)
	out, changed, errRewrite := rewriteRequestBody(input)
	if errRewrite != nil || !changed {
		t.Fatalf("rewriteRequestBody() = (_, %v, %v), want changed", changed, errRewrite)
	}
	var root struct {
		Tools []map[string]any `json:"tools"`
		Input []map[string]any `json:"input"`
	}
	if errUnmarshal := json.Unmarshal(out, &root); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal() error = %v", errUnmarshal)
	}
	if countTopLevelTool(root.Tools, "mcp__codex_apps__github___get_user_login") != 1 {
		t.Fatalf("discovered child promotion = %#v", root.Tools)
	}
	if countTopLevelTool(root.Tools, "mcp__codex_apps__github___get_repo") != 0 ||
		countTopLevelTool(root.Tools, "mcp__codex_apps__slack___slack_send_message") != 0 {
		t.Fatalf("undiscovered child was promoted: %#v", root.Tools)
	}
	if len(root.Input) != 1 || root.Input[0]["type"] != "function_call_output" {
		t.Fatalf("search output history = %#v", root.Input)
	}
	manifest, ok := root.Input[0]["output"].(string)
	if !ok || !strings.Contains(manifest, "mcp__codex_apps__github___get_user_login") {
		t.Fatalf("search-output manifest = %#v", root.Input[0]["output"])
	}
	if strings.Contains(manifest, "username") || strings.Contains(manifest, `"parameters"`) {
		t.Fatalf("search-output manifest repeated schemas: %s", manifest)
	}
}

func TestDiscoveredPromotionDeduplicatesAcrossSearchRounds(t *testing.T) {
	input := []byte(`{
		"tools":[
			{"type":"namespace","name":"mcp__github","tools":[
				{"type":"function","name":"get_me","parameters":{"type":"object"}}
			]}
		],
		"input":[
			{"type":"tool_search_output","call_id":"search-1","execution":"client","tools":[
				{"type":"namespace","name":"mcp__github","tools":[
					{"type":"function","name":"get_me","parameters":{"type":"object"}}
				]}
			]},
			{"type":"tool_search_output","call_id":"search-2","execution":"client","tools":[
				{"type":"namespace","name":"mcp__github","tools":[
					{"type":"function","name":"get_me","parameters":{"type":"object"}}
				]}
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
	if countTopLevelTool(root.Tools, "mcp__github__get_me") != 1 {
		t.Fatalf("discovered child was duplicated: %#v", root.Tools)
	}
}

func TestDiscoveredPromotionDoesNotShadowExistingTopLevelTool(t *testing.T) {
	input := []byte(`{
		"tools":[
			{"type":"function","name":"mcp__github__get_me","description":"top-level wins","parameters":{"type":"object"}},
			{"type":"namespace","name":"mcp__github","tools":[
				{"type":"function","name":"get_me","description":"deferred copy","parameters":{"type":"object"}}
			]}
		],
		"input":[
			{"type":"tool_search_output","call_id":"search-1","execution":"client","tools":[
				{"type":"namespace","name":"mcp__github","tools":[
					{"type":"function","name":"get_me","description":"deferred copy","parameters":{"type":"object"}}
				]}
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
	if countTopLevelTool(root.Tools, "mcp__github__get_me") != 1 {
		t.Fatalf("top-level tool was duplicated: %#v", root.Tools)
	}
	for _, tool := range root.Tools {
		if tool["name"] == "mcp__github__get_me" && tool["description"] != "top-level wins" {
			t.Fatalf("top-level tool was shadowed: %#v", tool)
		}
	}
}

func TestNamespaceFallbackRequiresSingleChild(t *testing.T) {
	body := []byte(`{"tools":[{"type":"namespace","name":"mcp__multi","tools":[
		{"type":"function","name":"one"},
		{"type":"function","name":"two"}
	]}]}`)
	catalog := extractToolCatalog(body)
	if _, ok := catalog.resolve("mcp__multi"); ok {
		t.Fatal("multi-child namespace was guessed")
	}
	if _, ok := catalog.resolve("mcp__single"); ok {
		t.Fatal("unknown namespace resolved")
	}
}

func TestResponseRestoresPrunedConnectorIdentity(t *testing.T) {
	requestBody := []byte(`{
		"tools":[
			{"type":"function","name":"exec_command","parameters":{}},
			{"type":"namespace","name":"mcp__codex_apps__github","tools":[
				{"type":"function","name":"_get_user_login","parameters":{"type":"object"}}
			]}
		]
	}`)
	pruned, _, errRewrite := rewriteRequestBody(requestBody)
	if errRewrite != nil {
		t.Fatalf("rewriteRequestBody() error = %v", errRewrite)
	}
	catalog := extractToolCatalog(pruned)
	if catalog == nil {
		t.Fatal("extractToolCatalog() = nil")
	}

	for _, emittedName := range []string{
		"mcp__codex_apps__github___get_user_login",
		"mcp__codex_apps__github.get_user_login",
		"mcp__codex_apps__github",
		"_get_user_login",
	} {
		body := []byte(`{"output":[
			{"type":"function_call","call_id":"call-1","name":"` + emittedName + `","arguments":"{}"},
			{"type":"function_call","call_id":"call-2","name":"exec_command","arguments":"{}"}
		]}`)
		rewritten, changed, errResponse := rewriteResponseBodyWithPolicy(body, catalog, true)
		if errResponse != nil || !changed {
			t.Fatalf("rewriteResponseBodyWithPolicy(%q) = (_, %v, %v), want changed", emittedName, changed, errResponse)
		}
		var response struct {
			Output []map[string]any `json:"output"`
		}
		if errUnmarshal := json.Unmarshal(rewritten, &response); errUnmarshal != nil {
			t.Fatalf("json.Unmarshal() error = %v", errUnmarshal)
		}
		connector := response.Output[0]
		if connector["name"] != "_get_user_login" ||
			connector["namespace"] != "mcp__codex_apps__github" {
			t.Fatalf("%q restored as %#v", emittedName, connector)
		}
		ordinary := response.Output[1]
		if ordinary["name"] != "exec_command" || ordinary["namespace"] != nil {
			t.Fatalf("ordinary call changed: %#v", ordinary)
		}
	}
}

func TestResponseDoesNotHijackTopLevelLocalName(t *testing.T) {
	requestBody := []byte(`{
		"tools":[
			{"type":"function","name":"_get_user_login","parameters":{}},
			{"type":"namespace","name":"mcp__codex_apps__github","tools":[
				{"type":"function","name":"_get_user_login","parameters":{}}
			]}
		]
	}`)
	pruned, _, errRewrite := rewriteRequestBody(requestBody)
	if errRewrite != nil {
		t.Fatalf("rewriteRequestBody() error = %v", errRewrite)
	}
	catalog := extractToolCatalog(pruned)
	body := []byte(`{"type":"function_call","call_id":"call-1","name":"_get_user_login","arguments":"{}"}`)
	out, changed, errResponse := rewriteResponseBodyWithPolicy(body, catalog, true)
	if errResponse != nil {
		t.Fatalf("rewriteResponseBodyWithPolicy() error = %v", errResponse)
	}
	var item map[string]any
	if errUnmarshal := json.Unmarshal(out, &item); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal() error = %v", errUnmarshal)
	}
	if changed || string(out) != string(body) {
		t.Fatalf("top-level call changed: (%v, %s)", changed, out)
	}
	if _, exists := item["namespace"]; exists {
		t.Fatalf("top-level identity was hijacked: %#v", item)
	}
}

func TestNativeResponsesRouteIsNotRewritten(t *testing.T) {
	if policy := requestPolicyFor("request_before", "openai-response", ""); policy.bridge || policy.bridgeKnown {
		t.Fatalf("before-auth policy = %#v, want deferred decision", policy)
	}
	if policy := requestPolicyFor("request_after", "openai-response", "openai-response"); policy.bridge || !policy.native {
		t.Fatalf("native policy = %#v, want native and no bridge", policy)
	}
	if policy := requestPolicyFor("request_after", "openai-response", "openai"); !policy.bridge || !policy.prune || policy.native {
		t.Fatalf("chat policy = %#v, want bridge and prune", policy)
	}
	if policy := requestPolicyFor("request_after", "openai", "openai"); policy.bridge || policy.native {
		t.Fatalf("chat-completions client policy = %#v, want untouched", policy)
	}
	if policy := requestPolicyFor("request_after", "claude", "openai-response"); policy.bridge || policy.native {
		t.Fatalf("claude client policy = %#v, want untouched", policy)
	}

	input := []byte(`{
		"tools":[
			{"type":"tool_search","execution":"client","parameters":{}},
			{"type":"namespace","name":"mcp__github","tools":[{"type":"function","name":"get_me","parameters":{}}]}
		]
	}`)
	out, changed, errRewrite := rewriteRequestBodyWithPolicy(input, requestPolicyFor("request_after", "openai-response", "openai-response"), extractToolCatalog(input))
	if errRewrite != nil || changed {
		t.Fatalf("native rewrite = (_, %v, %v), want unchanged", changed, errRewrite)
	}
	if string(out) != string(input) {
		t.Fatalf("native body changed: %s", out)
	}
}

func TestRequestStateRestoresIdentityAcrossInterceptors(t *testing.T) {
	const requestID = "catalog-state-test"
	fullBody := []byte(`{"tools":[
		{"type":"namespace","name":"mcp__codex_apps__github","tools":[
			{"type":"function","name":"_get_user_login","parameters":{}}
		]}
	]}`)
	if _, errRequest := interceptRequest("request_after", mustJSON(t, map[string]any{
		"RequestID":    requestID,
		"SourceFormat": "openai-response",
		"ToFormat":     "openai",
		"Body":         fullBody,
	})); errRequest != nil {
		t.Fatalf("interceptRequest() error = %v", errRequest)
	}
	raw, errResponse := interceptResponse(mustJSON(t, map[string]any{
		"RequestID":    requestID,
		"SourceFormat": "openai-response",
		"Body": []byte(`{"type":"function_call","call_id":"call-1",
			"name":"mcp__codex_apps__github___get_user_login","arguments":"{}"}`),
	}))
	if errResponse != nil {
		t.Fatalf("interceptResponse() error = %v", errResponse)
	}
	result := decodeResponseBodyResult(t, raw)
	var item map[string]any
	if errUnmarshal := json.Unmarshal(result, &item); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal() error = %v", errUnmarshal)
	}
	if item["name"] != "_get_user_login" || item["namespace"] != "mcp__codex_apps__github" {
		t.Fatalf("restored response = %#v", item)
	}
}

func TestStreamHeaderCatalogRestoresLaterPayload(t *testing.T) {
	const requestID = "catalog-stream-test"
	headerRaw, errHeader := interceptStreamChunk(mustJSON(t, map[string]any{
		"RequestID":    requestID,
		"SourceFormat": "openai-response",
		"ChunkIndex":   -1,
		"OriginalRequest": []byte(`{"tools":[{"type":"namespace","name":"mcp__github","tools":[
			{"type":"function","name":"get_me","parameters":{}}
		]}]}`),
		"Body": []byte{},
	}))
	if errHeader != nil {
		t.Fatalf("interceptStreamChunk(header) error = %v", errHeader)
	}
	if body := decodeStreamChunkResult(t, headerRaw); len(body) != 0 {
		t.Fatalf("header body = %s", body)
	}

	payloadRaw, errPayload := interceptStreamChunk(mustJSON(t, map[string]any{
		"RequestID":    requestID,
		"SourceFormat": "openai-response",
		"ChunkIndex":   0,
		"Body": []byte(`{"type":"response.output_item.done","item":{
			"type":"function_call","call_id":"call-1","name":"mcp__github__get_me","arguments":"{}"
		}}`),
	}))
	if errPayload != nil {
		t.Fatalf("interceptStreamChunk(payload) error = %v", errPayload)
	}
	payload := decodeStreamChunkResult(t, payloadRaw)
	if !strings.Contains(string(payload), `"name":"get_me"`) ||
		!strings.Contains(string(payload), `"namespace":"mcp__github"`) {
		t.Fatalf("stream identity not restored: %s", payload)
	}
}

func hasFunctionNamed(tools []any, name string) bool {
	for _, tool := range tools {
		item, ok := tool.(map[string]any)
		if !ok {
			continue
		}
		if stringField(item, "type") == "function" && stringField(item, "name") == name {
			return true
		}
	}
	return false
}

func findNamespace(value any, name string) map[string]any {
	tools, ok := value.([]any)
	if !ok {
		return nil
	}
	for _, rawTool := range tools {
		tool, ok := rawTool.(map[string]any)
		if !ok {
			continue
		}
		if stringField(tool, "type") == "namespace" && stringField(tool, "name") == name {
			return tool
		}
	}
	return nil
}

func deferredEntries(t *testing.T, namespace map[string]any) []any {
	t.Helper()
	entries, ok := namespace[deferredToolsKey].([]any)
	if !ok {
		t.Fatalf("deferred metadata = %#v", namespace[deferredToolsKey])
	}
	return entries
}

func countTopLevelTool(tools []map[string]any, name string) int {
	count := 0
	for _, tool := range tools {
		if tool["name"] == name {
			count++
		}
	}
	return count
}

func decodeResponseBodyResult(t *testing.T, raw []byte) []byte {
	t.Helper()
	var envelope struct {
		Result struct {
			Body []byte `json:"Body"`
		} `json:"result"`
	}
	if errUnmarshal := json.Unmarshal(raw, &envelope); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal() error = %v", errUnmarshal)
	}
	return envelope.Result.Body
}

func decodeStreamChunkResult(t *testing.T, raw []byte) []byte {
	t.Helper()
	var envelope struct {
		Result struct {
			Body []byte `json:"Body"`
		} `json:"result"`
	}
	if errUnmarshal := json.Unmarshal(raw, &envelope); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal() error = %v", errUnmarshal)
	}
	return envelope.Result.Body
}
