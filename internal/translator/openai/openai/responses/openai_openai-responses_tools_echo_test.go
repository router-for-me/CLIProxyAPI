package responses

import (
	"encoding/json"
	"testing"

	"github.com/tidwall/gjson"
)

func TestToolsForOpenAIResponsesEcho_FlattensNestedChatFunction(t *testing.T) {
	t.Parallel()

	tools := gjson.Parse(`[{
		"type":"function",
		"function":{
			"name":"search",
			"description":"find things",
			"parameters":{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}
		}
	}]`)

	out := toolsForOpenAIResponsesEcho(tools)
	raw, ok := encodeEchoTools(t, out)
	if !ok {
		return
	}
	tool0 := gjson.Parse(raw).Get("0")
	if tool0.Get("function").Exists() {
		t.Fatalf("still nested: %s", tool0.Raw)
	}
	if got := tool0.Get("name").String(); got != "search" {
		t.Fatalf("name=%q, want search; tool=%s", got, tool0.Raw)
	}
	if got := tool0.Get("description").String(); got != "find things" {
		t.Fatalf("description=%q; tool=%s", got, tool0.Raw)
	}
	if !tool0.Get("parameters.properties.q").Exists() {
		t.Fatalf("parameters missing: %s", tool0.Raw)
	}
}

func TestToolsForOpenAIResponsesEcho_UsesParametersJSONSchemaFallback(t *testing.T) {
	t.Parallel()

	tools := gjson.Parse(`[{
		"type":"function",
		"function":{
			"name":"legacy",
			"parametersJsonSchema":{"type":"object","properties":{"x":{"type":"number"}}}
		}
	}]`)

	out := toolsForOpenAIResponsesEcho(tools)
	raw, ok := encodeEchoTools(t, out)
	if !ok {
		return
	}
	tool0 := gjson.Parse(raw).Get("0")
	if tool0.Get("function").Exists() {
		t.Fatalf("still nested: %s", tool0.Raw)
	}
	if got := tool0.Get("name").String(); got != "legacy" {
		t.Fatalf("name=%q; tool=%s", got, tool0.Raw)
	}
	if !tool0.Get("parameters.properties.x").Exists() {
		t.Fatalf("parametersJsonSchema not lifted: %s", tool0.Raw)
	}
}

func TestToolsForOpenAIResponsesEcho_PreservesAlreadyFlatFunction(t *testing.T) {
	t.Parallel()

	tools := gjson.Parse(`[{
		"type":"function",
		"name":"flat_already",
		"description":"ok",
		"parameters":{"type":"object","properties":{}}
	}]`)

	out := toolsForOpenAIResponsesEcho(tools)
	raw, ok := encodeEchoTools(t, out)
	if !ok {
		return
	}
	tool0 := gjson.Parse(raw).Get("0")
	if tool0.Get("function").Exists() {
		t.Fatalf("unexpected nested function: %s", tool0.Raw)
	}
	if got := tool0.Get("name").String(); got != "flat_already" {
		t.Fatalf("name=%q; tool=%s", got, tool0.Raw)
	}
	if got := tool0.Get("description").String(); got != "ok" {
		t.Fatalf("description=%q; tool=%s", got, tool0.Raw)
	}
}

func TestToolsForOpenAIResponsesEcho_LeavesNonFunctionToolsUntouched(t *testing.T) {
	t.Parallel()

	tools := gjson.Parse(`[{
		"type":"web_search",
		"search_context_size":"medium"
	}]`)

	out := toolsForOpenAIResponsesEcho(tools)
	raw, ok := encodeEchoTools(t, out)
	if !ok {
		return
	}
	tool0 := gjson.Parse(raw).Get("0")
	if got := tool0.Get("type").String(); got != "web_search" {
		t.Fatalf("type=%q; tool=%s", got, tool0.Raw)
	}
	if got := tool0.Get("search_context_size").String(); got != "medium" {
		t.Fatalf("search_context_size=%q; tool=%s", got, tool0.Raw)
	}
	if tool0.Get("name").Exists() {
		t.Fatalf("unexpected name on non-function tool: %s", tool0.Raw)
	}
}

func TestToolsForOpenAIResponsesEcho_MixedArray(t *testing.T) {
	t.Parallel()

	tools := gjson.Parse(`[
		{"type":"function","function":{"name":"nested_one","parameters":{"type":"object","properties":{}}}},
		{"type":"function","name":"flat_two","parameters":{"type":"object","properties":{}}},
		{"type":"file_search","vector_store_ids":["vs_1"]}
	]`)

	out := toolsForOpenAIResponsesEcho(tools)
	raw, ok := encodeEchoTools(t, out)
	if !ok {
		return
	}
	arr := gjson.Parse(raw)
	if n := len(arr.Array()); n != 3 {
		t.Fatalf("len=%d, want 3; %s", n, raw)
	}
	if arr.Get("0.function").Exists() || arr.Get("0.name").String() != "nested_one" {
		t.Fatalf("tools[0] not flattened: %s", arr.Get("0").Raw)
	}
	if arr.Get("1.name").String() != "flat_two" {
		t.Fatalf("tools[1] broken: %s", arr.Get("1").Raw)
	}
	if arr.Get("2.type").String() != "file_search" {
		t.Fatalf("tools[2] broken: %s", arr.Get("2").Raw)
	}
}

func encodeEchoTools(t *testing.T, out interface{}) (string, bool) {
	t.Helper()
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal echo tools: %v (%T)", err, out)
		return "", false
	}
	return string(b), true
}
