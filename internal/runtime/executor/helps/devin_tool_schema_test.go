package helps

import (
	"bytes"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestNormalizeDevinToolSchemas_RootOneOf(t *testing.T) {
	original := []byte(`{
		"type":"object",
		"properties":{
			"code":{"type":"string"},
			"completion_summary":{"type":"string"},
			"nested":{"oneOf":[{"type":"string"},{"type":"integer"}]}
		},
		"oneOf":[
			{"type":"object","required":["code"]},
			{"type":"object","required":["completion_summary"]}
		],
		"additionalProperties":false
	}`)
	tools := NormalizeDevinToolSchemas([]DevinTool{{Name: "mcp__cua_repl__js", Parameters: original}})
	if len(tools) != 1 || tools[0].Name != "mcp__cua_repl__js" {
		t.Fatalf("tool identity changed: %+v", tools)
	}
	schema := ojson.ParseBytes(tools[0].Parameters)
	if schema.Get("oneOf").Exists() || schema.Get("anyOf").Exists() || schema.Get("allOf").Exists() {
		t.Fatalf("root union survived: %s", tools[0].Parameters)
	}
	if schema.Get("type").String() != "object" ||
		!schema.Get("properties.code").Exists() ||
		!schema.Get("properties.completion_summary").Exists() ||
		!schema.Get("properties.nested.oneOf").Exists() ||
		schema.Get("required").Exists() ||
		schema.Get("additionalProperties").Bool() {
		t.Fatalf("unexpected folded schema: %s", tools[0].Parameters)
	}
	if !bytes.Contains(original, []byte(`"oneOf"`)) {
		t.Fatal("original schema was mutated")
	}
}

func TestNormalizeDevinToolSchemas_PreservesSchemasWithoutRootUnion(t *testing.T) {
	plain := []byte(`{ "type": "object", "properties": {"query":{"type":"string"}}, "required":["query"] }`)
	nested := []byte(`{"type":"object","properties":{"choice":{"oneOf":[{"type":"string"},{"type":"integer"}]}}}`)
	malformed := []byte(`{"type":`)
	tools := NormalizeDevinToolSchemas([]DevinTool{
		{Name: "plain", Description: "unchanged", Parameters: plain},
		{Name: "nested", Parameters: nested},
		{Name: "malformed", Parameters: malformed},
		{Name: "no-parameters"},
	})
	for i, expected := range [][]byte{plain, nested, malformed, nil} {
		if !bytes.Equal(tools[i].Parameters, expected) {
			t.Fatalf("tool %q changed unexpectedly: %s", tools[i].Name, tools[i].Parameters)
		}
	}
	if tools[0].Description != "unchanged" {
		t.Fatalf("description changed: %q", tools[0].Description)
	}
}

func TestNormalizeDevinToolSchemas_OtherRootUnions(t *testing.T) {
	for _, tc := range []struct {
		name     string
		schema   string
		property string
	}{
		{
			name:     "anyOf",
			schema:   `{"anyOf":[{"type":"object","properties":{"a":{"type":"string"}}},{"type":"object","properties":{"b":{"type":"integer"}}}]}`,
			property: "properties.b",
		},
		{
			name:     "allOf",
			schema:   `{"type":"object","properties":{"a":{"type":"string"}},"allOf":[{"type":"object","properties":{"b":{"type":"integer"}},"required":["b"]}]}`,
			property: "properties.b",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tools := NormalizeDevinToolSchemas([]DevinTool{{Name: "union", Parameters: []byte(tc.schema)}})
			root := ojson.ParseBytes(tools[0].Parameters)
			if root.Get(tc.name).Exists() || root.Get("type").String() != "object" || !root.Get(tc.property).Exists() {
				t.Fatalf("root union not normalized: %s", tools[0].Parameters)
			}
			if tc.name == "allOf" && !root.Get(`required.#(=="b")`).Exists() {
				t.Fatalf("allOf required field lost: %s", tools[0].Parameters)
			}
		})
	}
}
