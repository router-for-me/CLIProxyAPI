package executor

import (
	"encoding/json"
	"testing"

	"github.com/tidwall/gjson"
)

func TestNormalizeCodexToolSchemas_UnionTypeArrayAddsItems(t *testing.T) {
	t.Parallel()

	body := []byte(`{"tools":[{"name":"tool_object","parameters":{"type":["object","array"]}},{"name":"tool_string","parameters":"{\"type\":[\"null\",\"array\"]}"}]}`)
	got := normalizeCodexToolSchemas(body)

	if !gjson.GetBytes(got, "tools.0.parameters.items").Exists() {
		t.Fatalf("expected items for object parameters union array type")
	}

	paramsString := gjson.GetBytes(got, "tools.1.parameters").String()
	if paramsString == "" {
		t.Fatal("expected parameters string for second tool")
	}
	var schema map[string]any
	if err := json.Unmarshal([]byte(paramsString), &schema); err != nil {
		t.Fatalf("failed to parse parameters string: %v", err)
	}
	if _, ok := schema["items"]; !ok {
		t.Fatal("expected items in string parameters union array type")
	}
}

func TestNormalizeCodexToolSchemas_NestedCompositeArrayAddsItems(t *testing.T) {
	t.Parallel()

	body := []byte(`{
  "tools":[
    {
      "name":"nested",
      "parameters":{
        "type":"object",
        "properties":{
          "payload":{
            "anyOf":[
              {"type":"array"},
              {"type":"object","properties":{"nested":{"type":["array","null"]}}}
            ]
          }
        }
      }
    }
  ]
}`)

	got := normalizeCodexToolSchemas(body)
	if !gjson.GetBytes(got, "tools.0.parameters.properties.payload.anyOf.0.items").Exists() {
		t.Fatal("expected items added for anyOf array schema")
	}
	if !gjson.GetBytes(got, "tools.0.parameters.properties.payload.anyOf.1.properties.nested.items").Exists() {
		t.Fatal("expected items added for nested union array schema")
	}
}

func TestNormalizeCodexToolSchemas_ExistingItemsUnchanged(t *testing.T) {
	t.Parallel()

	body := []byte("{\n  \"tools\": [\n    {\n      \"name\": \"already_ok\",\n      \"parameters\": {\n        \"type\": \"array\",\n        \"items\": {\"type\": \"string\"}\n      }\n    }\n  ]\n}\n")
	got := normalizeCodexToolSchemas(body)

	if string(got) != string(body) {
		t.Fatal("expected original body when schema already has items")
	}
	if gjson.GetBytes(got, "tools.0.parameters.items.type").String() != "string" {
		t.Fatal("expected existing items schema to remain unchanged")
	}
}
