//go:build skip
// +build skip

package util

import (
	"encoding/json"
	"testing"
)

func TestDeleteKeysByName_RemovesRefAndDefsRecursively(t *testing.T) {
	input := `{
		"root": {
			"$defs": {
				"Address": {"type": "object", "properties": {"city": {"type": "string"}}
				},
			"tool": {
				"$ref": "#/definitions/Address",
				"properties": {
					"address": {
						"$ref": "#/$defs/Address",
						"$defs": {"Nested": {"type": "string"}}
					}
				}
			}
			},
			"items": [
				{"name": "leaf", "$defs": {"x": 1}},
				{"name": "leaf2", "kind": {"$ref": "#/tool"}}
			]
		}
	}
	`

	got := DeleteKeysByName(input, "$ref", "$defs")

	var payload map[string]any
	if err := json.Unmarshal([]byte(got), &payload); err != nil {
		t.Fatalf("DeleteKeysByName returned invalid json: %v", err)
	}

	r, ok := payload["root"].(map[string]any)
	if !ok {
		t.Fatal("root missing or invalid")
	}

	if _, ok := r["$defs"]; ok {
		t.Fatalf("root $defs should be removed")
	}

	items, ok := r["items"].([]any)
	if !ok {
		t.Fatal("items missing or invalid")
	}
	for i, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("items[%d] invalid type", i)
		}
		if _, ok := obj["$defs"]; ok {
			t.Fatalf("items[%d].$defs should be removed", i)
		}
	}
}

func TestDeleteKeysByName_IgnoresMissingKeys(t *testing.T) {
	input := `{"model":"claude-opus","tools":[{"name":"ok"}]}`
	if got := DeleteKeysByName(input, "$ref", "$defs"); got != input {
		t.Fatalf("DeleteKeysByName should keep payload unchanged when no keys match: got %s", got)
	}
}

func TestDeleteKeysByName_RemovesMultipleKeyNames(t *testing.T) {
	input := `{
		"node": {
			"one": {"target":1},
			"two": {"target":2}
		},
		"target": {"value": 99}
	}`

	got := DeleteKeysByName(input, "one", "target", "missing")

	var payload map[string]any
	if err := json.Unmarshal([]byte(got), &payload); err != nil {
		t.Fatalf("DeleteKeysByName returned invalid json: %v", err)
	}

	node, ok := payload["node"].(map[string]any)
	if !ok {
		t.Fatal("node missing or invalid")
	}
	if _, ok := node["one"]; ok {
		t.Fatalf("node.one should be removed")
	}
	if _, ok := node["two"]; !ok {
		t.Fatalf("node.two should remain")
	}
	if _, ok := payload["target"]; ok {
		t.Fatalf("top-level target should be removed")
	}
}

func TestDeleteKeysByName_UsesStableDeletionPathSorting(t *testing.T) {
	input := `{
		"tool": {
			"parameters": {
				"$defs": {
					"nested": {"$ref": "#/tool/parameters/$defs/nested"}
				},
				"properties": {
					"value": {"type": "string", "$ref": "#/tool/parameters/$defs/nested"}
				}
			}
		}
	}`

	got := DeleteKeysByName(input, "$defs", "$ref")

	var payload map[string]any
	if err := json.Unmarshal([]byte(got), &payload); err != nil {
		t.Fatalf("DeleteKeysByName returned invalid json: %v", err)
	}

	tool, ok := payload["tool"].(map[string]any)
	if !ok {
		t.Fatal("tool missing or invalid")
	}

	parameters, ok := tool["parameters"].(map[string]any)
	if !ok {
		t.Fatal("parameters missing or invalid")
	}
	if _, ok := parameters["$defs"]; ok {
		t.Fatalf("parameters.$defs should be removed")
	}

	properties, ok := parameters["properties"].(map[string]any)
	if !ok {
		t.Fatal("properties missing or invalid")
	}
	value, ok := properties["value"].(map[string]any)
	if !ok {
		t.Fatal("value missing or invalid")
	}
	if _, ok := value["$ref"]; ok {
		t.Fatalf("nested $ref should be removed")
	}
	if _, ok := value["type"]; !ok {
		t.Fatalf("value.type should remain")
	}
}
