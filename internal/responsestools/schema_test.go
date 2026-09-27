package responsestools

import (
	"strings"
	"testing"
)

func TestCompleteSearchSchemaNullableRoundTrip(t *testing.T) {
	tools := []any{map[string]any{
		"type": "tool_search",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":  map[string]any{"type": "string"},
				"limit":  map[string]any{"type": "integer"},
				"strict": map[string]any{"type": "boolean"},
			},
			"required": []any{"query"},
		},
	}}
	if !CompleteToolSearchSchemas(tools) {
		t.Fatalf("expected change")
	}
	schema := tools[0].(map[string]any)["parameters"].(map[string]any)
	required := schema["required"].([]any)
	if len(required) != 3 {
		t.Fatalf("required not completed: %v", required)
	}
	properties := schema["properties"].(map[string]any)
	limit := properties["limit"].(map[string]any)
	entries, ok := limit["anyOf"].([]any)
	if !ok || len(entries) != 2 {
		t.Fatalf("optional field not widened to nullable: %v", limit)
	}
	if _, exists := schema["additionalProperties"]; exists {
		t.Fatalf("completion changed omitted additionalProperties: %v", schema["additionalProperties"])
	}
}

func TestCompleteSearchSchemaPreservesAdditionalPropertiesAndRequired(t *testing.T) {
	additionalProperties := map[string]any{"type": "string", "minLength": 2}
	tools := []any{map[string]any{
		"type": "tool_search",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":    map[string]any{"type": "string"},
				"optional": map[string]any{"type": "integer"},
			},
			"required":             []any{"legacy_required", "query"},
			"additionalProperties": additionalProperties,
		},
	}}
	if !CompleteToolSearchSchemas(tools) {
		t.Fatal("expected schema completion")
	}
	schema := tools[0].(map[string]any)["parameters"].(map[string]any)
	if got := mustMarshal(schema["additionalProperties"]); string(got) != string(mustMarshal(additionalProperties)) {
		t.Fatalf("additionalProperties = %s, want preserved %s", got, mustMarshal(additionalProperties))
	}
	required, ok := schema["required"].([]any)
	if !ok {
		t.Fatalf("required = %#v, want array", schema["required"])
	}
	want := []any{"legacy_required", "query", "optional"}
	if string(mustMarshal(required)) != string(mustMarshal(want)) {
		t.Fatalf("required = %v, want original entries plus missing property %v", required, want)
	}
}

func TestCompleteSearchSchemaSkipsServerAndOrdinary(t *testing.T) {
	tools := []any{
		map[string]any{"type": "tool_search", "execution": "server", "parameters": map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}}},
		map[string]any{"type": "function", "name": "f", "parameters": map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}}},
	}
	if CompleteToolSearchSchemas(tools) {
		t.Fatalf("server and ordinary tools must not change")
	}
}

func TestInlineLocalRefs(t *testing.T) {
	tools := []any{map[string]any{
		"type": "function", "name": "f",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"item": map[string]any{"$ref": "#/$defs/Item"},
			},
			"$defs": map[string]any{
				"Item": map[string]any{"type": "object", "properties": map[string]any{"x": map[string]any{"type": "string"}}},
			},
		},
	}}
	changed, err := InlineLocalRefs(tools, SchemaBudget{MaxBytes: 65536, MaxNodes: 10000, MaxDepth: 64})
	if err != nil {
		t.Fatalf("inline: %v", err)
	}
	if !changed {
		t.Fatalf("expected change")
	}
	encoded := mustMarshal(tools)
	if strings.Contains(string(encoded), "$ref") || strings.Contains(string(encoded), "$defs") {
		t.Fatalf("refs not inlined: %s", encoded)
	}
}

func TestInlineLocalRefsRejectsRecursive(t *testing.T) {
	tools := []any{map[string]any{
		"type": "function", "name": "f",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"item": map[string]any{"$ref": "#/$defs/Item"},
			},
			"$defs": map[string]any{
				"Item": map[string]any{"type": "object", "properties": map[string]any{"child": map[string]any{"$ref": "#/$defs/Item"}}},
			},
		},
	}}
	if _, err := InlineLocalRefs(tools, SchemaBudget{MaxBytes: 65536, MaxNodes: 10000, MaxDepth: 64}); err == nil {
		t.Fatalf("expected recursive rejection")
	}
}

func TestInlineLocalRefsRejectsExternalAndMissing(t *testing.T) {
	for _, ref := range []string{"https://example.invalid/schema.json", "#/properties/x", "#/$defs/Missing"} {
		tools := []any{map[string]any{
			"type": "function", "name": "f",
			"parameters": map[string]any{"a": map[string]any{"$ref": ref}},
		}}
		if _, err := InlineLocalRefs(tools, SchemaBudget{MaxBytes: 65536, MaxNodes: 10000, MaxDepth: 64}); err == nil {
			t.Fatalf("expected rejection for %q", ref)
		}
	}
}

func TestInlineRefSiblingConflictUsesAllOf(t *testing.T) {
	tools := []any{map[string]any{
		"type": "function", "name": "f",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"item": map[string]any{
					"$ref":        "#/$defs/Item",
					"description": "override",
				},
			},
			"$defs": map[string]any{
				"Item": map[string]any{"type": "object", "description": "original"},
			},
		},
	}}
	changed, err := InlineLocalRefs(tools, SchemaBudget{MaxBytes: 65536, MaxNodes: 10000, MaxDepth: 64})
	if err != nil {
		t.Fatalf("inline: %v", err)
	}
	if !changed {
		t.Fatalf("expected change")
	}
	encoded := mustMarshal(tools)
	if !strings.Contains(string(encoded), "allOf") {
		t.Fatalf("conflicting siblings must use allOf, got %s", encoded)
	}
	if !strings.Contains(string(encoded), "override") || !strings.Contains(string(encoded), "original") {
		t.Fatalf("neither side may be dropped: %s", encoded)
	}
}

func TestJSONPointerEscape(t *testing.T) {
	if decodeJSONPointerToken("a~1b~0c") != "a/b~c" {
		t.Fatalf("pointer escape wrong")
	}
}

func TestValidateToolArrayDepth(t *testing.T) {
	deep := []any{map[string]any{"type": "namespace", "name": "a", "tools": []any{
		map[string]any{"type": "namespace", "name": "b", "tools": []any{}},
	}}}
	if err := ValidateToolArrayDepth(deep, 1, 1); err == nil {
		t.Fatalf("expected depth rejection")
	}
	if err := ValidateToolArrayDepth(deep, 1, 5); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}
