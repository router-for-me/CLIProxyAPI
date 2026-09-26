package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStrictCompleteToolSearchSchemasCompletesOptionalProperties(t *testing.T) {
	request := []byte(`{
		"tools": [
			{"type":"tool_search","execution":"client","description":"search","parameters":{
				"type":"object",
				"properties":{
					"limit":{"type":"number","description":"Maximum number of tools to return."},
					"query":{"type":"string","description":"Search query."}
				},
				"required":["query"],
				"additionalProperties":false
			}},
			{"type":"function","name":"exec_command","parameters":{"type":"object","properties":{"cmd":{"type":"string"}},"required":[]}}
		]
	}`)
	tools := decodeTools(t, request)
	if !strictCompleteToolSearchSchemas(tools) {
		t.Fatal("strictCompleteToolSearchSchemas() changed = false, want true")
	}
	tool := tools[0].(map[string]any)
	if tool["type"] != "tool_search" || tool["execution"] != "client" {
		t.Fatalf("tool identity changed: %#v", tool)
	}
	schema := tool["parameters"].(map[string]any)
	required := schema["required"].([]any)
	if len(required) != 2 || required[0] != "limit" || required[1] != "query" {
		t.Fatalf("required = %#v, want [limit query]", required)
	}
	properties := schema["properties"].(map[string]any)
	limitSchema := properties["limit"].(map[string]any)
	limitAnyOf, ok := limitSchema["anyOf"].([]any)
	if !ok || len(limitAnyOf) != 2 {
		t.Fatalf("limit anyOf = %#v, want original and null", limitSchema["anyOf"])
	}
	if _, okNullable := limitAnyOf[1].(map[string]any); !okNullable {
		t.Fatalf("nullable branch = %#v", limitAnyOf[1])
	}
	queryType := properties["query"].(map[string]any)["type"]
	if queryType != "string" {
		t.Fatalf("required property type = %#v, want unchanged string", queryType)
	}
	if schema["additionalProperties"] != false {
		t.Fatalf("additionalProperties = %#v, want false", schema["additionalProperties"])
	}

	functionTool := tools[1].(map[string]any)
	functionSchema := functionTool["parameters"].(map[string]any)
	if _, hasRequired := functionSchema["required"].([]any); !hasRequired {
		t.Fatalf("function tool schema was rewritten: %#v", functionSchema)
	}
	if functionSchema["properties"].(map[string]any)["cmd"].(map[string]any)["type"] != "string" {
		t.Fatalf("function tool property changed: %#v", functionSchema)
	}
}

func TestStrictCompleteToolSearchSchemasIsIdempotent(t *testing.T) {
	request := []byte(`{
		"tools":[{"type":"tool_search","execution":"client","parameters":{
			"type":"object",
			"properties":{"limit":{"type":["number","null"]},"query":{"type":"string"}},
			"required":["limit","query"],
			"additionalProperties":false
		}}]
	}`)
	tools := decodeTools(t, request)
	if strictCompleteToolSearchSchemas(tools) {
		t.Fatal("strictCompleteToolSearchSchemas() changed = true for an already strict schema")
	}
}

func TestStrictCompleteToolSearchSchemasWidensNonObjectOptionalProperty(t *testing.T) {
	request := []byte(`{
		"tools":[{"type":"tool_search","execution":"client","parameters":{
			"type":"object",
			"properties":{"enabled":true,"query":{"type":"string"}},
			"required":["query"],
			"additionalProperties":false
		}}]
	}`)
	tools := decodeTools(t, request)
	if !strictCompleteToolSearchSchemas(tools) {
		t.Fatal("strictCompleteToolSearchSchemas() changed = false, want true")
	}
	schema := tools[0].(map[string]any)["parameters"].(map[string]any)
	properties := schema["properties"].(map[string]any)
	enabledSchema, ok := properties["enabled"].(map[string]any)
	if !ok {
		t.Fatalf("enabled schema = %#v, want nullable anyOf object", properties["enabled"])
	}
	anyOf, ok := enabledSchema["anyOf"].([]any)
	if !ok || len(anyOf) != 2 || anyOf[0] != true {
		t.Fatalf("enabled anyOf = %#v, want original true and null", enabledSchema["anyOf"])
	}
	nullable, ok := anyOf[1].(map[string]any)
	if !ok || nullable["type"] != "null" {
		t.Fatalf("nullable branch = %#v", anyOf[1])
	}
}

func TestInlineRecursiveSchemaRefsElidesReentrantRefs(t *testing.T) {
	request := []byte(`{
		"tools":[{"type":"namespace","name":"gmail","tools":[{"type":"function","name":"_create_draft","parameters":{
			"type":"object",
			"properties":{"message":{"$ref":"#/$defs/GmailMessagePartRequest"}},
			"required":["message"],
			"$defs":{"GmailMessagePartRequest":{
				"type":"object",
				"properties":{"partId":{"type":"string"},"child":{"$ref":"#/$defs/GmailMessagePartRequest"}},
				"required":["partId"]
			}}
		}}]}]
	}`)
	tools := decodeTools(t, request)
	_, errInline := inlineToolSchemaRefsForTools(tools)
	if errInline == nil || !strings.Contains(errInline.Error(), "recursive_schema_reference") {
		t.Fatalf("inlineToolSchemaRefsForTools() error = %v, want recursive reference rejection", errInline)
	}
}

func TestStrictSchemaReferencePolicyHandlesEscapeAndMissingTargets(t *testing.T) {
	escaped := []byte(`{"tools":[{"type":"function","name":"escaped","parameters":{
		"type":"object",
		"properties":{"value":{"$ref":"#/$defs/foo~1bar"}},
		"$defs":{"foo/bar":{"type":"string","minLength":1}}
	}}]}`)
	out, changed, errEscape := normalizeStrictNativeBody(escaped)
	if errEscape != nil {
		t.Fatalf("normalizeStrictNativeBody() escaped pointer error = %v", errEscape)
	}
	if !changed || strings.Contains(string(out), `"$ref"`) || !strings.Contains(string(out), `"minLength":1`) {
		t.Fatalf("escaped pointer was not resolved: (%v, %s)", changed, out)
	}

	for name, body := range map[string]string{
		"missing local target": `{"tools":[{"type":"function","name":"missing","parameters":{
			"properties":{"value":{"$ref":"#/$defs/Missing"}}
		}}]}`,
		"unsupported external target": `{"tools":[{"type":"function","name":"external","parameters":{
			"properties":{"value":{"$ref":"https://example.com/schema.json"}}
		}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, _, errNormalize := normalizeStrictNativeBody([]byte(body))
			if errNormalize == nil {
				t.Fatal("normalizeStrictNativeBody() error = nil")
			}
		})
	}
}

func TestStrictSchemaExpansionBudgetsRejectBeforeUnboundedCopy(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"value": map[string]any{"$ref": "#/$defs/Node"},
			"next":  map[string]any{"$ref": "#/$defs/Node"},
		},
		"$defs": map[string]any{
			"Node": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"child": map[string]any{"$ref": "#/$defs/Leaf"},
				},
			},
			"Leaf": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"value": map[string]any{"type": "string"},
				},
			},
		},
	}
	cases := []struct {
		name          string
		config        string
		expectedError string
	}{
		{"bytes", "max_schema_expansion_bytes: 8\n", "schema_bytes_exceeded"},
		{"nodes", "max_schema_expansion_nodes: 1\n", "schema_nodes_exceeded"},
		{"depth", "max_schema_depth: 1\n", "schema_depth_exceeded"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Cleanup(func() { _ = applyPluginConfig(nil) })
			if errConfigure := applyPluginConfig([]byte(testCase.config)); errConfigure != nil {
				t.Fatalf("applyPluginConfig() error = %v", errConfigure)
			}
			_, _, errExpand := inlineRecursiveSchemaRefsWithPolicy(schema, 1)
			if errExpand == nil || !strings.Contains(errExpand.Error(), testCase.expectedError) {
				t.Fatalf("inlineRecursiveSchemaRefsWithPolicy() error = %v, want %s", errExpand, testCase.expectedError)
			}
		})
	}
}

func TestStrictCompletesNestedObjectSchemaWithoutLosingConstraints(t *testing.T) {
	request := []byte(`{
		"tools":[{"type":"tool_search","execution":"client","parameters":{
			"type":"object",
			"properties":{"filter":{
				"type":"object",
				"properties":{"mode":{"enum":["a","b"]},"limit":{"type":"integer","const":7}},
				"required":[]
			}},
			"required":["filter"]
		}}]
	}`)
	tools := decodeTools(t, request)
	if !strictCompleteToolSearchSchemas(tools) {
		t.Fatal("strictCompleteToolSearchSchemas() changed = false, want true")
	}
	schema := tools[0].(map[string]any)["parameters"].(map[string]any)
	filter := schema["properties"].(map[string]any)["filter"].(map[string]any)
	nestedRequired := filter["required"].([]any)
	if len(nestedRequired) != 2 || nestedRequired[0] != "limit" || nestedRequired[1] != "mode" {
		t.Fatalf("nested required = %#v", nestedRequired)
	}
	nestedProperties := filter["properties"].(map[string]any)
	modeSchema := nestedProperties["mode"].(map[string]any)
	modeAnyOf := modeSchema["anyOf"].([]any)
	if original, ok := modeAnyOf[0].(map[string]any); !ok || original["enum"] == nil {
		t.Fatalf("nested enum constraint lost: %#v", modeSchema)
	}
	limitSchema := nestedProperties["limit"].(map[string]any)
	limitAnyOf := limitSchema["anyOf"].([]any)
	if original, ok := limitAnyOf[0].(map[string]any); !ok || original["const"] != float64(7) {
		t.Fatalf("nested const constraint lost: %#v", limitSchema)
	}
}

func TestNormalizeStrictNativeBodyAppliesBothWorkarounds(t *testing.T) {
	body := []byte(`{
		"model":"opencode-go-muse-spark-1.3",
		"tools":[
			{"type":"tool_search","execution":"client","parameters":{
				"type":"object","properties":{"limit":{"type":"number"},"query":{"type":"string"}},
				"required":["query"],"additionalProperties":false}},
			{"type":"namespace","name":"clock","tools":[{"type":"function","name":"now","parameters":{
				"type":"object","properties":{"part":{"$ref":"#/definitions/Part"}},
		"definitions":{"Part":{"type":"object","properties":{"label":{"type":"string"}}}}}}]}
		]
	}`)
	out, changed, errNormalize := normalizeStrictNativeBody(body)
	if errNormalize != nil {
		t.Fatalf("normalizeStrictNativeBody() error = %v", errNormalize)
	}
	if !changed {
		t.Fatal("normalizeStrictNativeBody() changed = false, want true")
	}
	text := string(out)
	for _, unexpected := range []string{`"definitions"`, `"$ref"`} {
		if strings.Contains(text, unexpected) {
			t.Fatalf("normalized body still contains %s: %s", unexpected, text)
		}
	}
	if !strings.Contains(text, `"required":["limit","query"]`) {
		t.Fatalf("normalized body did not strict-complete tool_search: %s", text)
	}
}

func TestNormalizeStrictNativeBodyStripsCustomTools(t *testing.T) {
	t.Cleanup(func() { _ = applyPluginConfig(nil) })
	if errConfigure := applyPluginConfig([]byte("strip_custom_tools: true\n")); errConfigure != nil {
		t.Fatalf("applyPluginConfig() error = %v", errConfigure)
	}
	body := []byte(`{
		"model":"opencode-go-muse-spark-1.3",
		"tools":[
			{"type":"custom","name":"apply_patch","description":"freeform","format":{"type":"grammar"}},
			{"type":"function","name":"exec_command","parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}},
			{"type":"namespace","name":"plug","tools":[
				{"type":"custom","name":"freeform_child","description":"freeform"},
				{"type":"function","name":"kept_child","parameters":{"type":"object"}}
			]}
		]
	}`)
	out, changed, errNormalize := normalizeStrictNativeBody(body)
	if errNormalize != nil {
		t.Fatalf("normalizeStrictNativeBody() error = %v", errNormalize)
	}
	if !changed {
		t.Fatal("normalizeStrictNativeBody() changed = false, want true")
	}
	var root map[string]any
	if errUnmarshal := json.Unmarshal(out, &root); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal() error = %v", errUnmarshal)
	}
	tools := root["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("top-level tools = %d, want 2 after stripping custom: %s", len(tools), out)
	}
	if tools[0].(map[string]any)["name"] != "exec_command" {
		t.Fatalf("first remaining tool = %#v", tools[0])
	}
	namespace := tools[1].(map[string]any)
	children := namespace["tools"].([]any)
	if len(children) != 1 || children[0].(map[string]any)["name"] != "kept_child" {
		t.Fatalf("namespace children = %#v, want only kept_child", children)
	}
}

func TestStrictCustomToolRemovalRejectsForcedChoice(t *testing.T) {
	t.Cleanup(func() { _ = applyPluginConfig(nil) })
	if errConfigure := applyPluginConfig([]byte("strip_custom_tools: true\n")); errConfigure != nil {
		t.Fatalf("applyPluginConfig() error = %v", errConfigure)
	}
	body := []byte(`{"tools":[{"type":"custom","name":"apply_patch"}],"tool_choice":{"type":"function","name":"apply_patch"}}`)
	_, _, errNormalize := normalizeStrictNativeBody(body)
	if errNormalize == nil || !strings.Contains(errNormalize.Error(), "custom_tool_removal_forced_choice") {
		t.Fatalf("normalizeStrictNativeBody() error = %v, want forced choice rejection", errNormalize)
	}
}

func TestStrictCustomToolRemovalConvertsHistoricalCalls(t *testing.T) {
	t.Cleanup(func() { _ = applyPluginConfig(nil) })
	if errConfigure := applyPluginConfig([]byte("strip_custom_tools: true\n")); errConfigure != nil {
		t.Fatalf("applyPluginConfig() error = %v", errConfigure)
	}
	const patch = "*** Begin Patch\n*** End Patch"
	body := []byte(`{
		"tools":[{"type":"custom","name":"apply_patch"}],
		"input":[
			{"type":"custom_tool_call","id":"item-1","call_id":"c1","name":"apply_patch","input":"*** Begin Patch\n*** End Patch"},
			{"type":"custom_tool_call_output","id":"item-2","call_id":"c1","output":"Patch applied."}
		]
	}`)
	out, changed, errNormalize := normalizeStrictNativeBody(body)
	if errNormalize != nil {
		t.Fatalf("normalizeStrictNativeBody() error = %v", errNormalize)
	}
	if !changed {
		t.Fatal("normalizeStrictNativeBody() changed = false, want true")
	}
	var root map[string]any
	if errUnmarshal := json.Unmarshal(out, &root); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal() error = %v", errUnmarshal)
	}
	if len(root["tools"].([]any)) != 0 {
		t.Fatalf("custom tool declaration was retained: %s", out)
	}
	input := root["input"].([]any)
	call := input[0].(map[string]any)
	if call["type"] != "function_call" || call["name"] != "apply_patch" || call["call_id"] != "c1" || call["id"] != "item-1" {
		t.Fatalf("historical call identity changed: %#v", call)
	}
	if _, exists := call["input"]; exists {
		t.Fatalf("historical call still has custom input: %#v", call)
	}
	var arguments map[string]any
	if errUnmarshal := json.Unmarshal([]byte(call["arguments"].(string)), &arguments); errUnmarshal != nil {
		t.Fatalf("historical call arguments are not JSON: %v", errUnmarshal)
	}
	if arguments["input"] != patch {
		t.Fatalf("historical call input = %#v, want patch", arguments["input"])
	}
	output := input[1].(map[string]any)
	if output["type"] != "function_call_output" || output["call_id"] != "c1" || output["id"] != "item-2" || output["output"] != "Patch applied." {
		t.Fatalf("historical output changed: %#v", output)
	}
}

func TestStrictCustomToolRemovalRejectsNonStringHistoricalInput(t *testing.T) {
	t.Cleanup(func() { _ = applyPluginConfig(nil) })
	if errConfigure := applyPluginConfig([]byte("strip_custom_tools: true\n")); errConfigure != nil {
		t.Fatalf("applyPluginConfig() error = %v", errConfigure)
	}
	body := []byte(`{"tools":[{"type":"custom","name":"apply_patch"}],"input":[{"type":"custom_tool_call","call_id":"c1","name":"apply_patch","input":{"patch":"diff"}}]}`)
	_, _, errNormalize := normalizeStrictNativeBody(body)
	if errNormalize == nil || !strings.Contains(errNormalize.Error(), "custom_tool_history_invalid_input") {
		t.Fatalf("normalizeStrictNativeBody() error = %v, want invalid history input rejection", errNormalize)
	}
}

func TestStrictCustomToolRemovalConvertsNamespacedHistoricalCalls(t *testing.T) {
	t.Cleanup(func() { _ = applyPluginConfig(nil) })
	if errConfigure := applyPluginConfig([]byte("strip_custom_tools: true\n")); errConfigure != nil {
		t.Fatalf("applyPluginConfig() error = %v", errConfigure)
	}
	body := []byte(`{
		"tools":[{"type":"namespace","name":"patcher","tools":[{"type":"custom","name":"apply_patch"}]}],
		"input":[{"type":"custom_tool_call","call_id":"c1","name":"apply_patch","namespace":"patcher","input":"diff"}]
	}`)
	out, changed, errNormalize := normalizeStrictNativeBody(body)
	if errNormalize != nil {
		t.Fatalf("normalizeStrictNativeBody() error = %v", errNormalize)
	}
	if !changed {
		t.Fatal("normalizeStrictNativeBody() changed = false, want true")
	}
	var root map[string]any
	if errUnmarshal := json.Unmarshal(out, &root); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal() error = %v", errUnmarshal)
	}
	call := root["input"].([]any)[0].(map[string]any)
	if call["type"] != "function_call" || call["name"] != "patcher__apply_patch" {
		t.Fatalf("namespaced historical call = %#v, want qualified function_call", call)
	}
	if _, exists := call["namespace"]; exists {
		t.Fatalf("namespaced historical call still has namespace: %#v", call)
	}
}

func TestStrictCustomToolRemovalPreservesUnrelatedFunctionHistory(t *testing.T) {
	t.Cleanup(func() { _ = applyPluginConfig(nil) })
	if errConfigure := applyPluginConfig([]byte("strip_custom_tools: true\n")); errConfigure != nil {
		t.Fatalf("applyPluginConfig() error = %v", errConfigure)
	}
	body := []byte(`{
		"tools":[{"type":"function","name":"exec_command","parameters":{"type":"object"}}],
		"input":[{"type":"function_call","id":"item-1","call_id":"c1","name":"exec_command","arguments":"{}"}]
	}`)
	out, changed, errNormalize := normalizeStrictNativeBody(body)
	if errNormalize != nil {
		t.Fatalf("normalizeStrictNativeBody() error = %v", errNormalize)
	}
	if changed {
		t.Fatalf("normalizeStrictNativeBody() changed = true, want unrelated history untouched: %s", out)
	}
	var root map[string]any
	if errUnmarshal := json.Unmarshal(out, &root); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal() error = %v", errUnmarshal)
	}
	call := root["input"].([]any)[0].(map[string]any)
	if call["type"] != "function_call" || call["name"] != "exec_command" {
		t.Fatalf("unrelated function history changed: %#v", call)
	}
}

func TestStrictNativeBodyPreservesCustomToolsByDefault(t *testing.T) {
	t.Cleanup(func() { _ = applyPluginConfig(nil) })
	body := []byte(`{"tools":[{"type":"custom","name":"apply_patch","description":"freeform"}]}`)
	out, changed, errNormalize := normalizeStrictNativeBody(body)
	if errNormalize != nil {
		t.Fatalf("normalizeStrictNativeBody() error = %v", errNormalize)
	}
	if changed || string(out) != string(body) {
		t.Fatalf("default custom tool changed: %s", out)
	}
}

func TestNormalizeStrictNativeBodyLeavesOtherBodiesAlone(t *testing.T) {
	for _, body := range []string{
		``,
		`{"model":"muse","echo":"unchanged"}`,
		`{"model":"muse","tools":[{"type":"function","name":"exec_command","parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}}]}`,
		`not-json`,
	} {
		out, changed, errNormalize := normalizeStrictNativeBody([]byte(body))
		if errNormalize != nil {
			t.Fatalf("normalizeStrictNativeBody(%q) error = %v", body, errNormalize)
		}
		if changed {
			t.Fatalf("normalizeStrictNativeBody(%q) changed = true, want false", body)
		}
		if string(out) != body {
			t.Fatalf("normalizeStrictNativeBody(%q) = %q, want unchanged", body, out)
		}
	}
}

func TestStrictNativeModelMatches(t *testing.T) {
	t.Cleanup(func() { _ = applyPluginConfig(nil) })
	config := "strict_responses_models:\n  - exact-model\n  - prefix-*\n  - '*muse-spark*'\n  - OpenCode-Exact\n"
	if errConfigure := applyPluginConfig([]byte(config)); errConfigure != nil {
		t.Fatalf("applyPluginConfig() error = %v", errConfigure)
	}
	cases := []struct {
		name       string
		candidates []string
		want       bool
	}{
		{"exact match", []string{"exact-model"}, true},
		{"prefix wildcard", []string{"prefix-anything"}, true},
		{"substring wildcard", []string{"muse-spark-1.3-contributor"}, true},
		{"alias via substring wildcard", []string{"", "opencode-go-muse-spark-1.3"}, true},
		{"case insensitive", []string{"OPENCODE-EXACT"}, true},
		{"unrelated model", []string{"gpt-6-astra"}, false},
		{"prefix wildcard needs prefix", []string{"go-prefix-anything"}, false},
		{"empty candidates", []string{"", ""}, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := strictNativeModelMatches(testCase.candidates...); got != testCase.want {
				t.Fatalf("strictNativeModelMatches(%v) = %v, want %v", testCase.candidates, got, testCase.want)
			}
		})
	}
}

func TestConfigurePluginAppliesStrictModels(t *testing.T) {
	t.Cleanup(func() { _ = applyPluginConfig(nil) })
	envelope, errMarshal := json.Marshal(map[string]any{
		"config_yaml": []byte("enabled: true\nstrict_responses_models:\n  - '*muse-spark*'\n"),
	})
	if errMarshal != nil {
		t.Fatalf("json.Marshal() error = %v", errMarshal)
	}
	if errConfigure := configurePlugin(envelope); errConfigure != nil {
		t.Fatalf("configurePlugin() error = %v", errConfigure)
	}
	if !strictNativeModelMatches("muse-spark-1.3-contributor") {
		t.Fatal("configured model did not match after configurePlugin()")
	}
	if !strictNativeModelMatches("opencode-go-muse-spark-1.3") {
		t.Fatal("configured alias did not match after configurePlugin()")
	}
	if strictNativeModelMatches("gpt-6-astra") {
		t.Fatal("unconfigured model matched after configurePlugin()")
	}
	if errConfigure := configurePlugin([]byte("not-json")); errConfigure == nil {
		t.Fatal("configurePlugin() error = nil for malformed envelope")
	}
}

func decodeTools(t *testing.T, body []byte) []any {
	t.Helper()
	var root map[string]any
	if errUnmarshal := json.Unmarshal(body, &root); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal() error = %v", errUnmarshal)
	}
	tools, ok := root["tools"].([]any)
	if !ok {
		t.Fatalf("tools missing in %s", body)
	}
	return tools
}
