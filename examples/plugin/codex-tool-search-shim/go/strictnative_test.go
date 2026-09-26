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
	limitType := properties["limit"].(map[string]any)["type"].([]any)
	if len(limitType) != 2 || limitType[0] != "number" || limitType[1] != "null" {
		t.Fatalf("limit type = %#v, want [number null]", limitType)
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
	if !inlineRecursiveSchemaRefsForTools(tools) {
		t.Fatal("inlineRecursiveSchemaRefsForTools() changed = false, want true")
	}
	blob, errMarshal := json.Marshal(tools)
	if errMarshal != nil {
		t.Fatalf("json.Marshal() error = %v", errMarshal)
	}
	text := string(blob)
	for _, unexpected := range []string{`"$defs"`, `"$ref"`} {
		if strings.Contains(text, unexpected) {
			t.Fatalf("inlined schema still contains %s: %s", unexpected, text)
		}
	}
	if !strings.Contains(text, "nested GmailMessagePartRequest; recursion elided") {
		t.Fatalf("inlined schema lost the recursion marker: %s", text)
	}
	if !strings.Contains(text, `"partId"`) {
		t.Fatalf("inlined schema lost the definition body: %s", text)
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
				"definitions":{"Part":{"type":"object","properties":{"next":{"$ref":"#/definitions/Part"}}}}}}]}
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
