package util

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestCleanJSONSchemaForAntigravity_ConstToEnum(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"kind": {
				"type": "string",
				"const": "InsightVizNode"
			}
		}
	}`

	expected := `{
		"type": "object",
		"properties": {
			"kind": {
				"type": "string",
				"description": "Allowed: InsightVizNode"
			}
		}
	}`

	result := CleanJSONSchemaForAntigravity(input)
	compareJSON(t, expected, result)
}

func TestCleanJSONSchemaForAntigravity_TypeFlattening_Nullable(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"name": {
				"type": ["string", "null"]
			},
			"other": {
				"type": "string"
			}
		},
		"required": ["name", "other"]
	}`

	expected := `{
		"type": "object",
		"properties": {
			"name": {
				"type": "string",
				"nullable": true,
				"description": "(nullable)"
			},
			"other": {
				"type": "string"
			}
		},
		"required": ["name", "other"]
	}`

	result := CleanJSONSchemaForAntigravity(input)
	compareJSON(t, expected, result)
}

func TestCleanJSONSchemaForAntigravity_ConstraintsToDescription(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"tags": {
				"type": "array",
				"description": "List of tags",
				"minItems": 1
			},
			"name": {
				"type": "string",
				"description": "User name",
				"minLength": 3
			}
		}
	}`

	result := CleanJSONSchemaForAntigravity(input)

	// minItems should be REMOVED and moved to description
	if strings.Contains(result, `"minItems"`) {
		t.Errorf("minItems keyword should be removed")
	}
	if !strings.Contains(result, "minItems: 1") {
		t.Errorf("minItems hint missing in description")
	}

	// minLength should be moved to description
	if !strings.Contains(result, "minLength: 3") {
		t.Errorf("minLength hint missing in description")
	}
	if strings.Contains(result, `"minLength":`) || strings.Contains(result, `"minLength" :`) {
		t.Errorf("minLength keyword should be removed")
	}
}

func TestCleanJSONSchemaForAntigravity_AnyOfFlattening_SmartSelection(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"query": {
				"anyOf": [
					{ "type": "null" },
					{
						"type": "object",
						"properties": {
							"kind": { "type": "string" }
						}
					}
				]
			}
		}
	}`

	expected := `{
		"type": "object",
		"properties": {
			"query": {
				"type": "object",
				"nullable": true,
				"description": "Accepts: null | object",
				"properties": {
					"_": { "type": "boolean" },
					"kind": { "type": "string" }
				},
				"required": ["_"]
			}
		}
	}`

	result := CleanJSONSchemaForAntigravity(input)
	compareJSON(t, expected, result)
}

func TestCleanJSONSchemaForAntigravity_OneOfFlattening(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"config": {
				"oneOf": [
					{ "type": "string" },
					{ "type": "integer" }
				]
			}
		}
	}`

	expected := `{
		"type": "object",
		"properties": {
			"config": {
				"type": "string",
				"description": "Accepts: string | integer"
			}
		}
	}`

	result := CleanJSONSchemaForAntigravity(input)
	compareJSON(t, expected, result)
}

func TestCleanJSONSchemaForAntigravity_AllOfMerging(t *testing.T) {
	input := `{
		"type": "object",
		"allOf": [
			{
				"properties": {
					"a": { "type": "string" }
				},
				"required": ["a"]
			},
			{
				"properties": {
					"b": { "type": "integer" }
				},
				"required": ["b"]
			}
		]
	}`

	expected := `{
		"type": "object",
		"properties": {
			"a": { "type": "string" },
			"b": { "type": "integer" }
		},
		"required": ["a", "b"]
	}`

	result := CleanJSONSchemaForAntigravity(input)
	compareJSON(t, expected, result)
}

func TestCleanJSONSchemaForAntigravity_RefHandling(t *testing.T) {
	input := `{
		"definitions": {
			"User": {
				"type": "object",
				"properties": {
					"name": { "type": "string" }
				}
			}
		},
		"type": "object",
		"properties": {
			"customer": { "$ref": "#/definitions/User" }
		}
	}`

	// The local reference is expanded before definitions are removed. Claude VALIDATED mode adds
	// only its optional-object placeholder; the referenced property definition remains intact.
	expected := `{
		"type": "object",
		"properties": {
			"customer": {
				"type": "object",
				"properties": {
					"name": { "type": "string" },
					"_": { "type": "boolean" }
				},
				"required": ["_"]
			}
		}
	}`

	result := CleanJSONSchemaForAntigravity(input)
	compareJSON(t, expected, result)
}

func TestCleanJSONSchemaForAntigravity_RefHandling_DescriptionEscaping(t *testing.T) {
	input := `{
		"definitions": {
			"User": {
				"type": "object",
				"properties": {
					"name": { "type": "string" }
				}
			}
		},
		"type": "object",
		"properties": {
			"customer": {
				"description": "He said \"hi\"\\nsecond line",
				"$ref": "#/definitions/User"
			}
		}
	}`

	expected := `{
		"type": "object",
		"properties": {
			"customer": {
				"type": "object",
				"description": "He said \"hi\"\\nsecond line",
				"properties": {
					"name": { "type": "string" },
					"_": { "type": "boolean" }
				},
				"required": ["_"]
			}
		}
	}`

	result := CleanJSONSchemaForAntigravity(input)
	compareJSON(t, expected, result)
}

func TestCleanJSONSchemaForAntigravity_CyclicRefDefaults(t *testing.T) {
	input := `{
		"definitions": {
			"Node": {
				"type": "object",
				"properties": {
					"child": { "$ref": "#/definitions/Node" }
				}
			}
		},
		"$ref": "#/definitions/Node"
	}`

	result := CleanJSONSchemaForAntigravity(input)

	var resMap map[string]interface{}
	json.Unmarshal([]byte(result), &resMap)

	if resMap["type"] != "object" {
		t.Errorf("Expected type: object, got: %v", resMap["type"])
	}

	child := gjson.Get(result, "properties.child")
	if child.Get("type").String() != "object" || !strings.Contains(child.Get("description").String(), "Node") {
		t.Errorf("Expected typed cycle hint containing Node, got: %s", result)
	}
}

func TestCleanJSONSchemaForAntigravity_RequiredCleanup(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"a": {"type": "string"},
			"b": {"type": "string"}
		},
		"required": ["a", "b", "c"]
	}`

	expected := `{
		"type": "object",
		"properties": {
			"a": {"type": "string"},
			"b": {"type": "string"}
		},
		"required": ["a", "b"]
	}`

	result := CleanJSONSchemaForAntigravity(input)
	compareJSON(t, expected, result)
}

func TestCleanJSONSchemaForAntigravity_AllOfMerging_DotKeys(t *testing.T) {
	input := `{
		"type": "object",
		"allOf": [
			{
				"properties": {
					"my.param": { "type": "string" }
				},
				"required": ["my.param"]
			},
			{
				"properties": {
					"b": { "type": "integer" }
				},
				"required": ["b"]
			}
		]
	}`

	expected := `{
		"type": "object",
		"properties": {
			"my.param": { "type": "string" },
			"b": { "type": "integer" }
		},
		"required": ["my.param", "b"]
	}`

	result := CleanJSONSchemaForAntigravity(input)
	compareJSON(t, expected, result)
}

func TestCleanJSONSchemaForAntigravity_PropertyNameCollision(t *testing.T) {
	// A tool has an argument named "pattern" - should NOT be treated as a constraint
	input := `{
		"type": "object",
		"properties": {
			"pattern": {
				"type": "string",
				"description": "The regex pattern"
			}
		},
		"required": ["pattern"]
	}`

	expected := `{
		"type": "object",
		"properties": {
			"pattern": {
				"type": "string",
				"description": "The regex pattern"
			}
		},
		"required": ["pattern"]
	}`

	result := CleanJSONSchemaForAntigravity(input)
	compareJSON(t, expected, result)

	var resMap map[string]interface{}
	json.Unmarshal([]byte(result), &resMap)
	props, _ := resMap["properties"].(map[string]interface{})
	if _, ok := props["description"]; ok {
		t.Errorf("Invalid 'description' property injected into properties map")
	}
}

func TestCleanJSONSchemaForAntigravity_DotKeys(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"my.param": {
				"type": "string",
				"$ref": "#/definitions/MyType"
			}
		},
		"definitions": {
			"MyType": { "type": "string" }
		}
	}`

	result := CleanJSONSchemaForAntigravity(input)

	var resMap map[string]interface{}
	if err := json.Unmarshal([]byte(result), &resMap); err != nil {
		t.Fatalf("Failed to unmarshal result: %v", err)
	}

	props, ok := resMap["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("properties missing")
	}

	if val, ok := props["my.param"]; !ok {
		t.Fatalf("Key 'my.param' is missing. Result: %s", result)
	} else {
		valMap, _ := val.(map[string]interface{})
		if _, hasRef := valMap["$ref"]; hasRef {
			t.Errorf("Key 'my.param' still contains $ref")
		}
		if _, ok := props["my"]; ok {
			t.Errorf("Artifact key 'my' created by sjson splitting")
		}
	}
}

func TestCleanJSONSchemaForAntigravity_AnyOfAlternativeHints(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"value": {
				"anyOf": [
					{ "type": "string" },
					{ "type": "integer" },
					{ "type": "null" }
				]
			}
		}
	}`

	result := CleanJSONSchemaForAntigravity(input)

	if !strings.Contains(result, "Accepts:") {
		t.Errorf("Expected alternative types hint, got: %s", result)
	}
	if !strings.Contains(result, "string") || !strings.Contains(result, "integer") {
		t.Errorf("Expected all alternative types in hint, got: %s", result)
	}
}

func TestCleanJSONSchemaForAntigravity_NullableHint(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"name": {
				"type": ["string", "null"],
				"description": "User name"
			}
		},
		"required": ["name"]
	}`

	result := CleanJSONSchemaForAntigravity(input)

	if !strings.Contains(result, "(nullable)") {
		t.Errorf("Expected nullable hint, got: %s", result)
	}
	if !strings.Contains(result, "User name") {
		t.Errorf("Expected original description to be preserved, got: %s", result)
	}
}

func TestCleanJSONSchemaForAntigravity_TypeFlattening_Nullable_DotKey(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"my.param": {
				"type": ["string", "null"]
			},
			"other": {
				"type": "string"
			}
		},
		"required": ["my.param", "other"]
	}`

	expected := `{
		"type": "object",
		"properties": {
			"my.param": {
				"type": "string",
				"nullable": true,
				"description": "(nullable)"
			},
			"other": {
				"type": "string"
			}
		},
		"required": ["my.param", "other"]
	}`

	result := CleanJSONSchemaForAntigravity(input)
	compareJSON(t, expected, result)
}

func TestCleanJSONSchemaForAntigravity_EnumHint(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"status": {
				"type": "string",
				"enum": ["active", "inactive", "pending"],
				"description": "Current status"
			}
		}
	}`

	result := CleanJSONSchemaForAntigravity(input)

	if !strings.Contains(result, "Allowed:") {
		t.Errorf("Expected enum values hint, got: %s", result)
	}
	if !strings.Contains(result, "active") || !strings.Contains(result, "inactive") {
		t.Errorf("Expected enum values in hint, got: %s", result)
	}
}

func TestCleanJSONSchemaForAntigravity_AdditionalPropertiesHint(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"name": { "type": "string" }
		},
		"additionalProperties": false
	}`

	result := CleanJSONSchemaForAntigravity(input)

	if !strings.Contains(result, "No extra properties allowed") {
		t.Errorf("Expected additionalProperties hint, got: %s", result)
	}
}

func TestCleanJSONSchemaForAntigravity_AnyOfFlattening_PreservesDescription(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"config": {
				"description": "Parent desc",
				"anyOf": [
					{ "type": "string", "description": "Child desc" },
					{ "type": "integer" }
				]
			}
		}
	}`

	expected := `{
		"type": "object",
		"properties": {
			"config": {
				"type": "string",
				"description": "Parent desc (Child desc) (Accepts: string | integer)"
			}
		}
	}`

	result := CleanJSONSchemaForAntigravity(input)
	compareJSON(t, expected, result)
}

func TestCleanJSONSchemaForAntigravity_SingleEnumBecomesHint(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"kind": {
				"type": "string",
				"enum": ["fixed"]
			}
		}
	}`

	result := CleanJSONSchemaForAntigravity(input)

	if !strings.Contains(result, "Allowed: fixed") || gjson.Get(result, "properties.kind.enum").Exists() {
		t.Errorf("Ignored tool enum should become a hint, got: %s", result)
	}
}

func TestCleanJSONSchemaForAntigravity_MultipleNonNullTypes(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"value": {
				"type": ["string", "integer", "boolean"]
			}
		}
	}`

	result := CleanJSONSchemaForAntigravity(input)

	if !strings.Contains(result, "Accepts:") {
		t.Errorf("Expected multiple types hint, got: %s", result)
	}
	if !strings.Contains(result, "string") || !strings.Contains(result, "integer") || !strings.Contains(result, "boolean") {
		t.Errorf("Expected all types in hint, got: %s", result)
	}
}

func compareJSON(t *testing.T, expectedJSON, actualJSON string) {
	var expMap, actMap map[string]interface{}
	errExp := json.Unmarshal([]byte(expectedJSON), &expMap)
	errAct := json.Unmarshal([]byte(actualJSON), &actMap)

	if errExp != nil || errAct != nil {
		t.Fatalf("JSON Unmarshal error. Exp: %v, Act: %v", errExp, errAct)
	}

	if !reflect.DeepEqual(expMap, actMap) {
		expBytes, _ := json.MarshalIndent(expMap, "", "  ")
		actBytes, _ := json.MarshalIndent(actMap, "", "  ")
		t.Errorf("JSON mismatch:\nExpected:\n%s\n\nActual:\n%s", string(expBytes), string(actBytes))
	}
}

// ============================================================================
// Empty Schema Placeholder Tests
// ============================================================================

func TestCleanJSONSchemaForAntigravity_EmptySchemaPlaceholder(t *testing.T) {
	// Empty object schema with no properties should get a placeholder
	input := `{
		"type": "object"
	}`

	result := CleanJSONSchemaForAntigravity(input)

	// Should have placeholder property added
	if !strings.Contains(result, `"reason"`) {
		t.Errorf("Empty schema should have 'reason' placeholder property, got: %s", result)
	}
	if !strings.Contains(result, `"required"`) {
		t.Errorf("Empty schema should have 'required' with 'reason', got: %s", result)
	}
}

func TestCleanJSONSchemaForAntigravity_EmptyPropertiesPlaceholder(t *testing.T) {
	// Object with empty properties object
	input := `{
		"type": "object",
		"properties": {}
	}`

	result := CleanJSONSchemaForAntigravity(input)

	// Should have placeholder property added
	if !strings.Contains(result, `"reason"`) {
		t.Errorf("Empty properties should have 'reason' placeholder, got: %s", result)
	}
}

func TestCleanJSONSchemaForAntigravity_NonEmptySchemaUnchanged(t *testing.T) {
	// Schema with properties should NOT get placeholder
	input := `{
		"type": "object",
		"properties": {
			"name": {"type": "string"}
		},
		"required": ["name"]
	}`

	result := CleanJSONSchemaForAntigravity(input)

	// Should NOT have placeholder property
	if strings.Contains(result, `"reason"`) {
		t.Errorf("Non-empty schema should NOT have 'reason' placeholder, got: %s", result)
	}
	// Original properties should be preserved
	if !strings.Contains(result, `"name"`) {
		t.Errorf("Original property 'name' should be preserved, got: %s", result)
	}
}

func TestCleanJSONSchemaForAntigravity_NestedEmptySchema(t *testing.T) {
	// Nested empty object in items should also get placeholder
	input := `{
		"type": "object",
		"properties": {
			"items": {
				"type": "array",
				"items": {
					"type": "object"
				}
			}
		}
	}`

	result := CleanJSONSchemaForAntigravity(input)

	// Nested empty object should also get placeholder
	// Check that the nested object has a reason property
	parsed := gjson.Parse(result)
	nestedProps := parsed.Get("properties.items.items.properties")
	if !nestedProps.Exists() || !nestedProps.Get("reason").Exists() {
		t.Errorf("Nested empty object should have 'reason' placeholder, got: %s", result)
	}
}

func TestCleanJSONSchemaForAntigravity_EmptySchemaWithDescription(t *testing.T) {
	// Empty schema with description should preserve description and add placeholder
	input := `{
		"type": "object",
		"description": "An empty object"
	}`

	result := CleanJSONSchemaForAntigravity(input)

	// Should have both description and placeholder
	if !strings.Contains(result, `"An empty object"`) {
		t.Errorf("Description should be preserved, got: %s", result)
	}
	if !strings.Contains(result, `"reason"`) {
		t.Errorf("Empty schema should have 'reason' placeholder, got: %s", result)
	}
}

func TestCleanJSONSchemaForAntigravityResponseDoesNotAddToolPlaceholders(t *testing.T) {
	bare := gjson.Parse(CleanJSONSchemaForAntigravityResponse(`{"type":"object"}`))
	if bare.Get("properties.reason").Exists() || bare.Get("required").Exists() {
		t.Fatalf("bare response schema gained tool placeholders: %s", bare.Raw)
	}

	input := `{
		"type":"object",
		"title":"Response",
		"nullable":true,
		"properties":{
			"empty":{"type":"object"},
			"optional":{"type":"object","properties":{"value":{"type":"string"}}}
		}
	}`
	result := gjson.Parse(CleanJSONSchemaForAntigravityResponse(input))
	for _, path := range []string{
		"properties.empty.properties.reason",
		"properties.empty.required",
		"properties.optional.properties._",
		"properties.optional.required",
	} {
		if result.Get(path).Exists() {
			t.Errorf("response schema gained tool-only field %s: %s", path, result.Raw)
		}
	}
	if result.Get("title").String() != "Response" || !result.Get("nullable").Bool() {
		t.Errorf("Antigravity response metadata was removed: %s", result.Raw)
	}
}

func TestCleanJSONSchemaForAntigravityResponseProjectsIgnoredUnions(t *testing.T) {
	input := `{
		"type":"object",
		"properties":{
			"action":{"anyOf":[
				{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]},
				{"type":"null"}
			]},
			"label":{"oneOf":[{"type":"string"},{"type":"null"}]}
		}
	}`

	result := gjson.Parse(CleanJSONSchemaForAntigravityResponse(input))
	for _, path := range []string{"properties.action.anyOf", "properties.label.oneOf"} {
		if result.Get(path).Exists() {
			t.Errorf("ignored response union %s survived: %s", path, result.Raw)
		}
	}
	for _, testCase := range []struct{ path, wantType string }{
		{path: "properties.action", wantType: "object"},
		{path: "properties.label", wantType: "string"},
	} {
		schema := result.Get(testCase.path)
		if schema.Get("type").String() != testCase.wantType || !schema.Get("nullable").Bool() {
			t.Errorf("%s was not projected to nullable %s: %s", testCase.path, testCase.wantType, result.Raw)
		}
	}
}

func TestCleanJSONSchemaForAntigravityResponsePreservesAdditionalPropertiesFalse(t *testing.T) {
	input := `{
		"type":"object",
		"properties":{
			"name":{"type":"string"},
			"nested":{
				"type":"object",
				"properties":{
					"age":{"type":"integer"}
				},
				"additionalProperties":false
			}
		},
		"additionalProperties":false
	}`

	result := gjson.Parse(CleanJSONSchemaForAntigravityResponse(input))

	// Root additionalProperties should be preserved as false
	rootAP := result.Get("additionalProperties")
	if !rootAP.Exists() || rootAP.Type != gjson.False {
		t.Errorf("root additionalProperties = %v, want false; cleaned: %s", rootAP, result.Raw)
	}

	// Nested additionalProperties should be preserved as false
	nestedAP := result.Get("properties.nested.additionalProperties")
	if !nestedAP.Exists() || nestedAP.Type != gjson.False {
		t.Errorf("nested additionalProperties = %v, want false; cleaned: %s", nestedAP, result.Raw)
	}

	// Should not have converted additionalProperties into description hints
	if strings.Contains(result.Raw, "No extra properties allowed") {
		t.Errorf("expected no description hint for additionalProperties:false, got: %s", result.Raw)
	}

	// But CleanJSONSchemaForAntigravity (tool path) must still strip it and add hint
	toolResult := CleanJSONSchemaForAntigravity(input)
	if strings.Contains(toolResult, `"additionalProperties"`) {
		t.Errorf("tool schema should not have additionalProperties: %s", toolResult)
	}
	if !strings.Contains(toolResult, "No extra properties allowed") {
		t.Errorf("tool schema should have description hint: %s", toolResult)
	}

	// Non-false additionalProperties (e.g. true or schema-valued) should still be stripped in response schemas
	nonFalseInput := `{
		"type":"object",
		"properties":{
			"map":{"type":"object","additionalProperties":{"type":"string"}}
		},
		"additionalProperties":true
	}`
	nonFalseResult := CleanJSONSchemaForAntigravityResponse(nonFalseInput)
	if strings.Contains(nonFalseResult, `"additionalProperties"`) {
		t.Errorf("non-false additionalProperties should be stripped in response schema: %s", nonFalseResult)
	}
}

func TestCleanJSONSchemaForAntigravityResponsePreservesEnumType(t *testing.T) {
	input := `{
		"type":"object",
		"properties":{
			"conviction":{"type":"number","enum":[0.25,0.5,1]},
			"count":{"type":"integer","enum":[1,2]}
		}
	}`

	result := gjson.Parse(CleanJSONSchemaForAntigravityResponse(input))
	for _, testCase := range []struct {
		path       string
		wantType   string
		wantValues []string
	}{
		{path: "properties.conviction", wantType: "number", wantValues: []string{"0.25", "0.5", "1"}},
		{path: "properties.count", wantType: "integer", wantValues: []string{"1", "2"}},
	} {
		schema := result.Get(testCase.path)
		if gotType := schema.Get("type").String(); gotType != testCase.wantType {
			t.Errorf("%s type = %q, want %q: %s", testCase.path, gotType, testCase.wantType, result.Raw)
		}
		var gotValues []string
		for _, enumValue := range schema.Get("enum").Array() {
			if enumValue.Type != gjson.String {
				t.Errorf("%s enum value is not a string: %s", testCase.path, enumValue.Raw)
			}
			gotValues = append(gotValues, enumValue.String())
		}
		if !reflect.DeepEqual(gotValues, testCase.wantValues) {
			t.Errorf("%s enum values = %v, want %v: %s", testCase.path, gotValues, testCase.wantValues, result.Raw)
		}
	}
}

// ============================================================================
// Format field handling (ad-hoc patch removal)
// ============================================================================

func TestCleanJSONSchemaForAntigravity_FormatFieldRemoval(t *testing.T) {
	// format:"uri" should be removed and added as hint
	input := `{
		"type": "object",
		"properties": {
			"url": {
				"type": "string",
				"format": "uri",
				"description": "A URL"
			}
		}
	}`

	result := CleanJSONSchemaForAntigravity(input)

	// format should be removed
	if strings.Contains(result, `"format"`) {
		t.Errorf("format field should be removed, got: %s", result)
	}
	// hint should be added to description
	if !strings.Contains(result, "format: uri") {
		t.Errorf("format hint should be added to description, got: %s", result)
	}
	// original description should be preserved
	if !strings.Contains(result, "A URL") {
		t.Errorf("Original description should be preserved, got: %s", result)
	}
}

func TestCleanJSONSchemaForAntigravity_FormatFieldNoDescription(t *testing.T) {
	// format without description should create description with hint
	input := `{
		"type": "object",
		"properties": {
			"email": {
				"type": "string",
				"format": "email"
			}
		}
	}`

	result := CleanJSONSchemaForAntigravity(input)

	// format should be removed
	if strings.Contains(result, `"format"`) {
		t.Errorf("format field should be removed, got: %s", result)
	}
	// hint should be added
	if !strings.Contains(result, "format: email") {
		t.Errorf("format hint should be added, got: %s", result)
	}
}

func TestCleanJSONSchemaForAntigravity_MultipleFormats(t *testing.T) {
	// Multiple format fields should all be handled
	input := `{
		"type": "object",
		"properties": {
			"url": {"type": "string", "format": "uri"},
			"email": {"type": "string", "format": "email"},
			"date": {"type": "string", "format": "date-time"}
		}
	}`

	result := CleanJSONSchemaForAntigravity(input)

	// All format fields should be removed
	if strings.Contains(result, `"format"`) {
		t.Errorf("All format fields should be removed, got: %s", result)
	}
	// All hints should be added
	if !strings.Contains(result, "format: uri") {
		t.Errorf("uri format hint should be added, got: %s", result)
	}
	if !strings.Contains(result, "format: email") {
		t.Errorf("email format hint should be added, got: %s", result)
	}
	if !strings.Contains(result, "format: date-time") {
		t.Errorf("date-time format hint should be added, got: %s", result)
	}
}

func TestCleanJSONSchemaForAntigravity_ToolEnumsBecomeHints(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"priority": {"type": "integer", "enum": [0, 1, 2]},
			"level": {"type": "number", "enum": [1.5, 2.5, 3.5]},
			"status": {"type": "string", "enum": ["active", "inactive"]}
		}
	}`

	result := CleanJSONSchemaForAntigravity(input)
	parsed := gjson.Parse(result)

	// Antigravity ignores function-argument enum but still uses the declared type to choose the
	// emitted JSON type. Preserve types and convert enum values to advisory hints.
	for path, wantType := range map[string]string{
		"properties.priority": "integer",
		"properties.level":    "number",
		"properties.status":   "string",
	} {
		if gotType := parsed.Get(path + ".type").String(); gotType != wantType {
			t.Errorf("Tool enum type at %s = %q, want %s: %s", path, gotType, wantType, result)
		}
		if parsed.Get(path+".enum").Exists() || !strings.Contains(parsed.Get(path+".description").String(), "Allowed:") {
			t.Errorf("Tool enum at %s was not projected to a hint: %s", path, result)
		}
	}
}

func TestCleanJSONSchemaForAntigravity_BooleanToolEnumBecomesHint(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"enabled": {"type": "boolean", "enum": [true, false]}
		}
	}`

	result := CleanJSONSchemaForAntigravity(input)

	value := gjson.Get(result, "properties.enabled")
	if value.Get("enum").Exists() || value.Get("type").String() != "boolean" || !strings.Contains(value.Get("description").String(), "Allowed: true, false") {
		t.Errorf("Boolean tool enum should become a typed hint, got: %s", result)
	}
}

func TestCleanJSONSchemaForGemini_RemovesGeminiUnsupportedMetadataFields(t *testing.T) {
	input := `{
		"$schema": "http://json-schema.org/draft-07/schema#",
		"$id": "root-schema",
		"$comment": "root comment should be removed",
		"type": "object",
		"properties": {
			"payload": {
				"type": "object",
				"$comment": "nested comment should be removed",
				"prefill": "hello",
				"properties": {
					"mode": {
						"type": "string",
						"enum": ["a", "b"],
						"enumDescriptions": ["Alpha", "Beta"],
						"enumTitles": ["A", "B"]
					}
				},
				"patternProperties": {
					"^x-": {"type": "string"}
				}
			},
			"$id": {
				"type": "string",
				"description": "property name should not be removed"
			},
			"$comment": {
				"type": "string",
				"description": "property name should not be removed"
			},
			"enumDescriptions": {
				"type": "array",
				"description": "property name should not be removed"
			}
		}
	}`

	expected := `{
		"type": "object",
		"properties": {
			"payload": {
				"type": "object",
				"properties": {
					"mode": {
						"type": "string",
						"enum": ["a", "b"],
						"description": "Allowed: a, b"
					}
				}
			},
			"$id": {
				"type": "string",
				"description": "property name should not be removed"
			},
			"$comment": {
				"type": "string",
				"description": "property name should not be removed"
			},
			"enumDescriptions": {
				"type": "array",
				"items": {"type": "string"},
				"description": "property name should not be removed"
			}
		}
	}`

	result := CleanJSONSchemaForGemini(input)
	compareJSON(t, expected, result)
}

func TestRemoveExtensionFields(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name: "removes x- fields at root",
			input: `{
				"type": "object",
				"x-custom-meta": "value",
				"properties": {
					"foo": { "type": "string" }
				}
			}`,
			expected: `{
				"type": "object",
				"properties": {
					"foo": { "type": "string" }
				}
			}`,
		},
		{
			name: "removes x- fields in nested properties",
			input: `{
				"type": "object",
				"properties": {
					"foo": {
						"type": "string",
						"x-internal-id": 123
					}
				}
			}`,
			expected: `{
				"type": "object",
				"properties": {
					"foo": {
						"type": "string"
					}
				}
			}`,
		},
		{
			name: "does NOT remove properties named x-",
			input: `{
				"type": "object",
				"properties": {
					"x-data": { "type": "string" },
					"normal": { "type": "number", "x-meta": "remove" }
				},
				"required": ["x-data"]
			}`,
			expected: `{
				"type": "object",
				"properties": {
					"x-data": { "type": "string" },
					"normal": { "type": "number" }
				},
				"required": ["x-data"]
			}`,
		},
		{
			name: "does NOT remove $schema and other meta fields (as requested)",
			input: `{
				"$schema": "http://json-schema.org/draft-07/schema#",
				"$id": "test",
				"type": "object",
				"properties": {
					"foo": { "type": "string" }
				}
			}`,
			expected: `{
				"$schema": "http://json-schema.org/draft-07/schema#",
				"$id": "test",
				"type": "object",
				"properties": {
					"foo": { "type": "string" }
				}
			}`,
		},
		{
			name: "handles properties named $schema",
			input: `{
				"type": "object",
				"properties": {
					"$schema": { "type": "string" }
				}
			}`,
			expected: `{
				"type": "object",
				"properties": {
					"$schema": { "type": "string" }
				}
			}`,
		},
		{
			name: "handles escaping in paths",
			input: `{
				"type": "object",
				"properties": {
					"foo.bar": {
						"type": "string",
						"x-meta": "remove"
					}
				},
				"x-root.meta": "remove"
			}`,
			expected: `{
				"type": "object",
				"properties": {
					"foo.bar": {
						"type": "string"
					}
				}
			}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := removeExtensionFields(tt.input)
			compareJSON(t, tt.expected, actual)
		})
	}
}

// uniqueItems should be stripped and moved to description hint (#2123).
func TestCleanJSONSchemaForAntigravity_UniqueItemsStripped(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"ids": {
				"type": "array",
				"description": "Unique identifiers",
				"items": {"type": "string"},
				"uniqueItems": true
			}
		}
	}`

	result := CleanJSONSchemaForAntigravity(input)

	if strings.Contains(result, `"uniqueItems"`) {
		t.Errorf("uniqueItems should be removed from schema")
	}
	if !strings.Contains(result, "uniqueItems: true") {
		t.Errorf("uniqueItems hint missing in description")
	}
}

// TestIsPropertyDefinitionDistinguishesPropertyNamedProperties covers the classification that
// decides whether a key spelled like a schema keyword is a keyword or an author-chosen name.
// Matching a trailing ".properties" alone mistook the schema of a property named "properties" for
// a property map, which disabled cleaning inside it.
func TestIsPropertyDefinitionDistinguishesPropertyNamedProperties(t *testing.T) {
	for path, want := range map[string]bool{
		"":                                    false,
		"properties":                          true,
		"properties.properties":               false,
		"properties.properties.properties":    true,
		"properties.records.items.properties": true,
		"properties.records.items":            false,
		// Any prefix the caller nests the schema under must not change the answer.
		"schema.properties": true,
		"request.tools.0.functionDeclarations.0.parameters":                       false,
		"request.tools.0.functionDeclarations.0.parameters.properties":            true,
		"request.tools.0.functionDeclarations.0.parameters.properties.properties": false,
		// $defs and patternProperties are name maps for the same reason as properties.
		"$defs":                          true,
		"$defs.properties":               false,
		"properties.$defs":               false,
		"properties.a.patternProperties": true,
		"properties.patternProperties":   false,
	} {
		if got := isPropertyDefinition(path); got != want {
			t.Errorf("isPropertyDefinition(%q) = %v, want %v", path, got, want)
		}
	}
}

// TestCleanJSONSchemaStripsPropertyNamesUnderPropertyNamedProperties covers the reported failure:
// the private Gemini backend rejects "propertyNames" with an unknown-field 400, and MCP tool
// schemas place it inside a property that is itself named "properties".
func TestCleanJSONSchemaStripsPropertyNamesUnderPropertyNamedProperties(t *testing.T) {
	shapes := map[string]string{
		// Nested in an array item, alongside the item's own properties map.
		"arrayItem": `{"type":"object","properties":{"records":{"type":"array","items":{"type":"object",` +
			`"properties":{"name":{"type":"string"}},"propertyNames":{"type":"string"}}}}}`,
		// A dynamic map declared by a property named "properties".
		"propertyNamedProperties": `{"type":"object","properties":{"properties":{"type":"object",` +
			`"propertyNames":{"type":"string"}}}}`,
		// Both shapes combined, as the reported tool schemas did.
		"combined": `{"type":"object","properties":{"pages":{"type":"array","items":{"type":"object",` +
			`"properties":{"properties":{"type":"object","propertyNames":{"type":"string"},` +
			`"additionalProperties":true}},"propertyNames":{"type":"string"}}}}}`,
	}

	for name, schema := range shapes {
		for cleaner, clean := range map[string]func(string) string{
			"antigravity":         CleanJSONSchemaForAntigravity,
			"gemini":              CleanJSONSchemaForGemini,
			"antigravityResponse": CleanJSONSchemaForAntigravityResponse,
		} {
			got := clean(schema)
			if strings.Contains(got, `"propertyNames"`) {
				t.Errorf("%s/%s: propertyNames survived cleaning: %s", name, cleaner, got)
			}
			if strings.Contains(got, `"additionalProperties"`) {
				t.Errorf("%s/%s: additionalProperties survived cleaning: %s", name, cleaner, got)
			}
		}
	}
}

// TestCleanJSONSchemaKeepsPropertiesNamedLikeKeywords guards the other half of the rule: a schema
// may legitimately declare properties named after schema keywords, and those must survive.
func TestCleanJSONSchemaKeepsPropertiesNamedLikeKeywords(t *testing.T) {
	input := `{"type":"object","properties":{
		"propertyNames":{"type":"string"},
		"patternProperties":{"type":"string"},
		"properties":{"type":"object","properties":{"propertyNames":{"type":"string"}}}
	}}`

	for cleaner, clean := range map[string]func(string) string{
		"antigravity": CleanJSONSchemaForAntigravity,
		"gemini":      CleanJSONSchemaForGemini,
	} {
		got := gjson.Parse(clean(input))
		for _, path := range []string{
			"properties.propertyNames",
			"properties.patternProperties",
			"properties.properties.properties.propertyNames",
		} {
			if !got.Get(path).Exists() {
				t.Errorf("%s: property %s was removed: %s", cleaner, path, got.Raw)
			}
		}
	}
}

func TestCleanJSONSchema_ConditionalKeywords(t *testing.T) {
	// 1. Root-level if/then/else
	rootInput := `{
		"type": "object",
		"properties": { "kind": { "type": "string", "enum": ["buy", "sell"] } },
		"required": ["kind"],
		"if":   { "properties": { "kind": { "const": "sell" } } },
		"then": { "properties": { "sell_reason": { "type": "string", "description": "why the position is being sold" } }, "required": ["sell_reason"] },
		"else": { "properties": { "buy_reason": { "type": "string" } } }
	}`

	for name, clean := range map[string]func(string) string{
		"AntigravityResponse": CleanJSONSchemaForAntigravityResponse,
		"Antigravity":         CleanJSONSchemaForAntigravity,
		"Gemini":              CleanJSONSchemaForGemini,
	} {
		res := gjson.Parse(clean(rootInput))
		if res.Get("if").Exists() {
			t.Errorf("[%s] root 'if' was not removed: %s", name, res.Raw)
		}
		if res.Get("then").Exists() {
			t.Errorf("[%s] root 'then' was not removed: %s", name, res.Raw)
		}
		if res.Get("else").Exists() {
			t.Errorf("[%s] root 'else' was not removed: %s", name, res.Raw)
		}
		if !res.Get("properties.sell_reason").Exists() {
			t.Errorf("[%s] then.properties.sell_reason was lost: %s", name, res.Raw)
		}
		if !res.Get("properties.buy_reason").Exists() {
			t.Errorf("[%s] else.properties.buy_reason was lost: %s", name, res.Raw)
		}
		if res.Get("properties.sell_reason.description").String() != "why the position is being sold" {
			t.Errorf("[%s] sell_reason description mismatch: %s", name, res.Raw)
		}
	}

	// 2. allOf with if/then
	allOfInput := `{
		"type": "object",
		"properties": { "kind": { "type": "string", "enum": ["buy", "sell"] } },
		"required": ["kind"],
		"allOf": [
			{
				"if":   { "properties": { "kind": { "const": "sell" } } },
				"then": {
					"properties": { "sell_reason": { "type": "string", "description": "why the position is being sold" } },
					"required": ["sell_reason"]
				}
			}
		]
	}`

	for name, clean := range map[string]func(string) string{
		"AntigravityResponse": CleanJSONSchemaForAntigravityResponse,
		"Antigravity":         CleanJSONSchemaForAntigravity,
		"Gemini":              CleanJSONSchemaForGemini,
	} {
		res := gjson.Parse(clean(allOfInput))
		if res.Get("allOf").Exists() {
			t.Errorf("[%s] 'allOf' was not removed: %s", name, res.Raw)
		}
		if res.Get("if").Exists() || strings.Contains(res.Raw, `"if":`) {
			t.Errorf("[%s] 'if' keyword present: %s", name, res.Raw)
		}
		if !res.Get("properties.sell_reason").Exists() {
			t.Errorf("[%s] allOf.then.properties.sell_reason was lost: %s", name, res.Raw)
		}
		if res.Get("properties.sell_reason.description").String() != "why the position is being sold" {
			t.Errorf("[%s] sell_reason description mismatch: %s", name, res.Raw)
		}
	}

	// 3. Nested property with if/then
	nestedInput := `{
		"type": "object",
		"properties": {
			"trade": {
				"type": "object",
				"properties": { "kind": { "type": "string" } },
				"if":   { "properties": { "kind": { "const": "sell" } } },
				"then": { "properties": { "sell_reason": { "type": "string" } } }
			}
		}
	}`

	for name, clean := range map[string]func(string) string{
		"AntigravityResponse": CleanJSONSchemaForAntigravityResponse,
		"Antigravity":         CleanJSONSchemaForAntigravity,
		"Gemini":              CleanJSONSchemaForGemini,
	} {
		res := gjson.Parse(clean(nestedInput))
		if res.Get("properties.trade.if").Exists() {
			t.Errorf("[%s] nested 'if' was not removed: %s", name, res.Raw)
		}
		if res.Get("properties.trade.then").Exists() {
			t.Errorf("[%s] nested 'then' was not removed: %s", name, res.Raw)
		}
		if !res.Get("properties.trade.properties.sell_reason").Exists() {
			t.Errorf("[%s] nested then.properties.sell_reason was lost: %s", name, res.Raw)
		}
	}
}

func TestCleanJSONSchemaForAntigravityResponseConditionalCannotOverwriteParent(t *testing.T) {
	input := `{
		"type":"object",
		"properties":{
			"kind":{"type":"string"},
			"action":{"type":"object","properties":{"full":{"type":"string"}},"required":["full"]}
		},
		"required":["kind","action"],
		"allOf":[{
			"if":{"properties":{"kind":{"const":"skip"}}},
			"then":{"properties":{
				"action":{"type":"null"},
				"branch_only":{"type":"integer"}
			}}
		}]
	}`

	result := gjson.Parse(CleanJSONSchemaForAntigravityResponse(input))
	action := result.Get("properties.action")
	if action.Get("type").String() != "object" || !action.Get("properties.full").Exists() {
		t.Fatalf("conditional branch replaced canonical action: %s", result.Raw)
	}
	if action.Get("required.0").String() != "full" || !result.Get("properties.branch_only").Exists() {
		t.Fatalf("conditional merge lost parent or branch-only information: %s", result.Raw)
	}
	if result.Get("allOf").Exists() || strings.Contains(result.Raw, `"if"`) || strings.Contains(result.Raw, `"then"`) {
		t.Fatalf("unsupported conditional keywords survived: %s", result.Raw)
	}
}

func TestCleanJSONSchemaForAntigravityResponseInlinesLocalRef(t *testing.T) {
	input := `{
		"$defs":{"Payload":{"type":"object","properties":{"id":{"type":"integer"}},"required":["id"]}},
		"type":"object",
		"properties":{"payload":{"$ref":"#/$defs/Payload"}},
		"required":["payload"]
	}`

	result := gjson.Parse(CleanJSONSchemaForAntigravityResponse(input))
	if result.Get(`\$defs`).Exists() || strings.Contains(result.Raw, `"$ref"`) {
		t.Fatalf("local reference metadata survived: %s", result.Raw)
	}
	payload := result.Get("properties.payload")
	if payload.Get("type").String() != "object" || payload.Get("properties.id.type").String() != "integer" || payload.Get("required.0").String() != "id" {
		t.Fatalf("local reference definition was not inlined: %s", result.Raw)
	}
}

func TestCleanJSONSchemaForAntigravityResponseTypeArrayUsesNativeNullable(t *testing.T) {
	input := `{"type":"object","properties":{"value":{"type":["number","null"]}},"required":["value"]}`
	result := gjson.Parse(CleanJSONSchemaForAntigravityResponse(input))
	value := result.Get("properties.value")
	if value.Get("type").String() != "number" || !value.Get("nullable").Bool() {
		t.Fatalf("type array was not projected to native nullable: %s", result.Raw)
	}
	if result.Get("required.0").String() != "value" {
		t.Fatalf("nullable required property became optional: %s", result.Raw)
	}
}

func TestCleanJSONSchemaForAntigravityToolKeepsNumericEnumType(t *testing.T) {
	input := `{"type":"object","properties":{"value":{"type":"number","enum":[1,2]}},"required":["value"]}`
	result := gjson.Parse(CleanJSONSchemaForAntigravityTool(input, false))
	value := result.Get("properties.value")
	if value.Get("type").String() != "number" {
		t.Fatalf("numeric tool enum changed argument JSON type: %s", result.Raw)
	}
	if value.Get("enum").Exists() || !strings.Contains(value.Get("description").String(), "Allowed: 1, 2") {
		t.Fatalf("ignored tool enum was not projected to a hint: %s", result.Raw)
	}
}

func TestCleanJSONSchemaForAntigravityResponseDropsIgnoredBooleanEnum(t *testing.T) {
	input := `{"type":"object","properties":{"value":{"type":"boolean","enum":["true"]}},"required":["value"]}`
	result := gjson.Parse(CleanJSONSchemaForAntigravityResponse(input))
	value := result.Get("properties.value")
	if value.Get("enum").Exists() || value.Get("type").String() != "boolean" || !strings.Contains(value.Get("description").String(), "Allowed: true") {
		t.Fatalf("ignored boolean response enum was not projected to a hint: %s", result.Raw)
	}
}

func TestCleanJSONSchemaForAntigravityResponseHintsIgnoredConstraints(t *testing.T) {
	input := `{"type":"object","properties":{"value":{"type":"number","minimum":1,"maximum":2,"not":{"enum":[1.5]}}}}`
	result := gjson.Parse(CleanJSONSchemaForAntigravityResponse(input))
	value := result.Get("properties.value")
	for _, keyword := range []string{"minimum", "maximum", "not"} {
		if value.Get(keyword).Exists() {
			t.Fatalf("ignored constraint %s survived: %s", keyword, result.Raw)
		}
		if !strings.Contains(value.Get("description").String(), keyword+":") {
			t.Fatalf("ignored constraint %s lost its hint: %s", keyword, result.Raw)
		}
	}
}

func TestSortByDepthUsesSegmentsAndIsStable(t *testing.T) {
	paths := []string{"root.verylong", "root.x.y", "first.same", "later.same"}
	sortByDepth(paths)
	want := []string{"root.x.y", "root.verylong", "first.same", "later.same"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("sortByDepth() = %v, want %v", paths, want)
	}
}

// TestCleanJSONSchemaStripsEncryptedMetadata covers Codex client tool definitions where
// properties carry the Responses-only "encrypted" marker (e.g. "encrypted": true or "encrypted": false).
// The Gemini backend strictly rejects unknown schema fields with an INVALID_ARGUMENT 400.
func TestCleanJSONSchemaStripsEncryptedMetadata(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"api_key": {
				"type": "string",
				"description": "API credential",
				"encrypted": true
			},
			"timeout": {
				"type": "integer",
				"encrypted": false
			},
			"nested": {
				"type": "object",
				"properties": {
					"secret": {
						"type": "string",
						"encrypted": true
					}
				}
			}
		},
		"required": ["api_key"]
	}`

	for cleaner, clean := range map[string]func(string) string{
		"antigravity":         CleanJSONSchemaForAntigravity,
		"gemini":              CleanJSONSchemaForGemini,
		"antigravityTool":     func(s string) string { return CleanJSONSchemaForAntigravityTool(s, false) },
		"antigravityResponse": CleanJSONSchemaForAntigravityResponse,
	} {
		got := clean(input)
		if strings.Contains(got, `"encrypted"`) {
			t.Errorf("%s: 'encrypted' marker survived cleaning: %s", cleaner, got)
		}
		parsed := gjson.Parse(got)
		if !parsed.Get("properties.api_key.type").Exists() || parsed.Get("properties.api_key.description").String() != "API credential" {
			t.Errorf("%s: api_key schema was corrupted: %s", cleaner, got)
		}
		if !parsed.Get("properties.nested.properties.secret.type").Exists() {
			t.Errorf("%s: nested property secret was corrupted: %s", cleaner, got)
		}
	}
}

// TestCleanJSONSchemaKeepsPropertyNamedEncrypted guards the legitimate case where a tool
// parameter itself is named "encrypted" (e.g. properties.encrypted: {"type": "boolean"}).
func TestCleanJSONSchemaKeepsPropertyNamedEncrypted(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"encrypted": {
				"type": "boolean",
				"description": "Whether the payload is encrypted",
				"encrypted": true
			},
			"data": {
				"type": "string"
			}
		},
		"required": ["encrypted"]
	}`

	for cleaner, clean := range map[string]func(string) string{
		"antigravity": CleanJSONSchemaForAntigravity,
		"gemini":      CleanJSONSchemaForGemini,
	} {
		got := clean(input)
		parsed := gjson.Parse(got)
		if !parsed.Get("properties.encrypted").Exists() {
			t.Errorf("%s: property named 'encrypted' was removed: %s", cleaner, got)
		}
		if parsed.Get("properties.encrypted.type").String() != "boolean" {
			t.Errorf("%s: property named 'encrypted' type corrupted: %s", cleaner, got)
		}
		// The inner attribute "encrypted": true must be stripped
		if parsed.Get("properties.encrypted.encrypted").Exists() {
			t.Errorf("%s: inner 'encrypted' attribute survived: %s", cleaner, got)
		}
	}
}

// TestCleanJSONSchema_BarePropertyMapNormalized covers Issue #5178:
// MCP tools (e.g. Asana) emit bare property maps missing type:object and properties wrappers,
// plus boolean required: true on child properties.
func TestCleanJSONSchema_BarePropertyMapNormalized(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"data": {
				"parent": { "type": "string", "required": true },
				"insert_after": { "type": "string" },
				"insert_before": { "type": "string" }
			},
			"opts": {
				"opt_fields": { "type": "string" }
			}
		}
	}`

	for cleaner, clean := range map[string]func(string) string{
		"antigravity":         CleanJSONSchemaForAntigravity,
		"antigravityTool":     func(s string) string { return CleanJSONSchemaForAntigravityTool(s, false) },
		"antigravityResponse": CleanJSONSchemaForAntigravityResponse,
		"gemini":              CleanJSONSchemaForGemini,
	} {
		got := clean(input)
		parsed := gjson.Parse(got)

		// data must be normalized into an object schema with properties
		if parsed.Get("properties.data.type").String() != "object" {
			t.Errorf("%s: properties.data.type = %q, want object; got schema: %s", cleaner, parsed.Get("properties.data.type").String(), got)
		}
		if parsed.Get("properties.data.properties.parent.type").String() != "string" {
			t.Errorf("%s: properties.data.properties.parent.type = %q, want string; got schema: %s", cleaner, parsed.Get("properties.data.properties.parent.type").String(), got)
		}
		if parsed.Get("properties.data.properties.insert_after.type").String() != "string" {
			t.Errorf("%s: properties.data.properties.insert_after.type = %q, want string; got schema: %s", cleaner, parsed.Get("properties.data.properties.insert_after.type").String(), got)
		}
		if parsed.Get("properties.data.properties.insert_before.type").String() != "string" {
			t.Errorf("%s: properties.data.properties.insert_before.type = %q, want string; got schema: %s", cleaner, parsed.Get("properties.data.properties.insert_before.type").String(), got)
		}
		// parent required: true must be promoted to data.required array
		var dataReq []string
		for _, r := range parsed.Get("properties.data.required").Array() {
			dataReq = append(dataReq, r.String())
		}
		if !contains(dataReq, "parent") {
			t.Errorf("%s: properties.data.required = %v, want 'parent' included; got schema: %s", cleaner, dataReq, got)
		}
		// boolean required on parent node must be stripped
		if parsed.Get("properties.data.properties.parent.required").Exists() {
			t.Errorf("%s: properties.data.properties.parent.required survived; got schema: %s", cleaner, got)
		}

		// opts must also be normalized into an object schema
		if parsed.Get("properties.opts.type").String() != "object" {
			t.Errorf("%s: properties.opts.type = %q, want object; got schema: %s", cleaner, parsed.Get("properties.opts.type").String(), got)
		}
		if parsed.Get("properties.opts.properties.opt_fields.type").String() != "string" {
			t.Errorf("%s: properties.opts.properties.opt_fields.type = %q, want string; got schema: %s", cleaner, parsed.Get("properties.opts.properties.opt_fields.type").String(), got)
		}
	}
}

// TestCleanJSONSchema_NestedBarePropertyMap tests recursive normalization of multi-level bare property maps.
func TestCleanJSONSchema_NestedBarePropertyMap(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"data": {
				"workspace": { "type": "string", "required": true },
				"task": {
					"name": { "type": "string", "required": true },
					"notes": { "type": "string" }
				}
			}
		}
	}`

	for cleaner, clean := range map[string]func(string) string{
		"antigravity":         CleanJSONSchemaForAntigravity,
		"gemini":              CleanJSONSchemaForGemini,
		"antigravityResponse": CleanJSONSchemaForAntigravityResponse,
	} {
		got := clean(input)
		parsed := gjson.Parse(got)

		if parsed.Get("properties.data.type").String() != "object" {
			t.Errorf("%s: properties.data.type = %q, want object; got schema: %s", cleaner, parsed.Get("properties.data.type").String(), got)
		}
		if parsed.Get("properties.data.properties.workspace.type").String() != "string" {
			t.Errorf("%s: properties.data.properties.workspace.type = %q, want string; got schema: %s", cleaner, parsed.Get("properties.data.properties.workspace.type").String(), got)
		}

		// Nested task should also be normalized to an object
		if parsed.Get("properties.data.properties.task.type").String() != "object" {
			t.Errorf("%s: properties.data.properties.task.type = %q, want object; got schema: %s", cleaner, parsed.Get("properties.data.properties.task.type").String(), got)
		}
		if parsed.Get("properties.data.properties.task.properties.name.type").String() != "string" {
			t.Errorf("%s: properties.data.properties.task.properties.name.type = %q, want string; got schema: %s", cleaner, parsed.Get("properties.data.properties.task.properties.name.type").String(), got)
		}

		// Required promotion at both levels
		var dataReq []string
		for _, r := range parsed.Get("properties.data.required").Array() {
			dataReq = append(dataReq, r.String())
		}
		if !contains(dataReq, "workspace") {
			t.Errorf("%s: properties.data.required = %v, want 'workspace'; got schema: %s", cleaner, dataReq, got)
		}

		var taskReq []string
		for _, r := range parsed.Get("properties.data.properties.task.required").Array() {
			taskReq = append(taskReq, r.String())
		}
		if !contains(taskReq, "name") {
			t.Errorf("%s: properties.data.properties.task.required = %v, want 'name'; got schema: %s", cleaner, taskReq, got)
		}
	}
}

// TestCleanJSONSchema_BarePropertyMapWithKeywordNames tests that bare property maps with fields
// named like schema keywords (title, description, format, type) are correctly normalized.
func TestCleanJSONSchema_BarePropertyMapWithKeywordNames(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"data": {
				"title": { "type": "string", "required": true },
				"description": { "type": "string" },
				"format": { "type": "string" },
				"type": { "type": "string" }
			}
		}
	}`

	for cleaner, clean := range map[string]func(string) string{
		"antigravity":         CleanJSONSchemaForAntigravity,
		"gemini":              CleanJSONSchemaForGemini,
		"antigravityResponse": CleanJSONSchemaForAntigravityResponse,
	} {
		got := clean(input)
		parsed := gjson.Parse(got)

		if parsed.Get("properties.data.type").String() != "object" {
			t.Errorf("%s: properties.data.type = %q, want object; got schema: %s", cleaner, parsed.Get("properties.data.type").String(), got)
		}
		if parsed.Get("properties.data.properties.title.type").String() != "string" {
			t.Errorf("%s: properties.data.properties.title.type = %q, want string; got schema: %s", cleaner, parsed.Get("properties.data.properties.title.type").String(), got)
		}
		if parsed.Get("properties.data.properties.description.type").String() != "string" {
			t.Errorf("%s: properties.data.properties.description.type = %q, want string; got schema: %s", cleaner, parsed.Get("properties.data.properties.description.type").String(), got)
		}
		if parsed.Get("properties.data.properties.type.type").String() != "string" {
			t.Errorf("%s: properties.data.properties.type.type = %q, want string; got schema: %s", cleaner, parsed.Get("properties.data.properties.type.type").String(), got)
		}

		var dataReq []string
		for _, r := range parsed.Get("properties.data.required").Array() {
			dataReq = append(dataReq, r.String())
		}
		if !contains(dataReq, "title") {
			t.Errorf("%s: properties.data.required = %v, want 'title'; got schema: %s", cleaner, dataReq, got)
		}
	}
}

// TestCleanJSONSchema_ArrayItemsBarePropertyMap tests bare property map normalization inside array items.
func TestCleanJSONSchema_ArrayItemsBarePropertyMap(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"tasks": {
				"type": "array",
				"items": {
					"id": { "type": "string", "required": true },
					"label": { "type": "string" }
				}
			}
		}
	}`

	for cleaner, clean := range map[string]func(string) string{
		"antigravity":         CleanJSONSchemaForAntigravity,
		"gemini":              CleanJSONSchemaForGemini,
		"antigravityResponse": CleanJSONSchemaForAntigravityResponse,
	} {
		got := clean(input)
		parsed := gjson.Parse(got)

		if parsed.Get("properties.tasks.items.type").String() != "object" {
			t.Errorf("%s: properties.tasks.items.type = %q, want object; got schema: %s", cleaner, parsed.Get("properties.tasks.items.type").String(), got)
		}
		if parsed.Get("properties.tasks.items.properties.id.type").String() != "string" {
			t.Errorf("%s: properties.tasks.items.properties.id.type = %q, want string; got schema: %s", cleaner, parsed.Get("properties.tasks.items.properties.id.type").String(), got)
		}
		var itemsReq []string
		for _, r := range parsed.Get("properties.tasks.items.required").Array() {
			itemsReq = append(itemsReq, r.String())
		}
		if !contains(itemsReq, "id") {
			t.Errorf("%s: properties.tasks.items.required = %v, want 'id'; got schema: %s", cleaner, itemsReq, got)
		}
	}
}

// TestCleanJSONSchema_ToolArraysMissingItems covers Issue #5292: Gemini and Antigravity
// reject tool array schemas that do not declare an items schema.
func TestCleanJSONSchema_ToolArraysMissingItems(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"params": { "type": "array" },
			"values": { "type": ["array", "null"], "description": "no items" },
			"existing": { "type": "array", "items": { "type": "number" } }
		}
	}`

	for cleaner, clean := range map[string]func(string) string{
		"antigravity":       CleanJSONSchemaForAntigravity,
		"antigravityLegacy": func(s string) string { return CleanJSONSchemaForAntigravityTool(s, false) },
		"gemini":            CleanJSONSchemaForGemini,
	} {
		t.Run(cleaner, func(t *testing.T) {
			got := gjson.Parse(clean(input))

			for _, path := range []string{"properties.params.items.type", "properties.values.items.type"} {
				if itemType := got.Get(path).String(); itemType != "string" {
					t.Errorf("%s = %q, want string; got schema: %s", path, itemType, got.Raw)
				}
			}
			if itemType := got.Get("properties.existing.items.type").String(); itemType != "number" {
				t.Errorf("existing items type = %q, want number; got schema: %s", itemType, got.Raw)
			}

			rootArray := gjson.Parse(clean(`{"type":"array"}`))
			if itemType := rootArray.Get("items.type").String(); itemType != "string" {
				t.Errorf("root items type = %q, want string; got schema: %s", itemType, rootArray.Raw)
			}
		})
	}
}

func TestCleanJSONSchema_ResponseArrayMissingItemsUnchanged(t *testing.T) {
	input := `{"type":"object","properties":{"values":{"type":"array"}}}`
	got := gjson.Parse(CleanJSONSchemaForAntigravityResponse(input))
	if got.Get("properties.values.items").Exists() {
		t.Fatalf("response schema gained tool-only items placeholder: %s", got.Raw)
	}
}

// TestCleanJSONSchema_BooleanRequiredPromoted tests that boolean required: true is promoted
// and boolean required: false is stripped without being added to the required array.
func TestCleanJSONSchema_BooleanRequiredPromoted(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"existing": { "type": "string" },
			"name": { "type": "string", "required": true },
			"age": { "type": "integer", "required": false },
			"tag": { "type": "string" }
		},
		"required": ["existing"]
	}`

	for cleaner, clean := range map[string]func(string) string{
		"antigravity":         CleanJSONSchemaForAntigravity,
		"gemini":              CleanJSONSchemaForGemini,
		"antigravityResponse": CleanJSONSchemaForAntigravityResponse,
	} {
		got := clean(input)
		parsed := gjson.Parse(got)

		var req []string
		for _, r := range parsed.Get("required").Array() {
			req = append(req, r.String())
		}

		if !contains(req, "existing") || !contains(req, "name") {
			t.Errorf("%s: required = %v, want both 'existing' and 'name'; got schema: %s", cleaner, req, got)
		}
		if contains(req, "age") || contains(req, "tag") {
			t.Errorf("%s: required = %v, should not contain 'age' or 'tag'; got schema: %s", cleaner, req, got)
		}

		if parsed.Get("properties.name.required").Exists() {
			t.Errorf("%s: properties.name.required survived; got schema: %s", cleaner, got)
		}
		if parsed.Get("properties.age.required").Exists() {
			t.Errorf("%s: properties.age.required survived; got schema: %s", cleaner, got)
		}
	}
}

// TestCleanJSONSchema_PreservesLargeNumberPrecision tests that numbers are not corrupted by float64 precision loss.
func TestCleanJSONSchema_PreservesLargeNumberPrecision(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"big_int": {
				"type": "integer",
				"minimum": 9007199254740993
			},
			"bare_child": {
				"sub": { "type": "string" }
			}
		}
	}`

	result := CleanJSONSchemaForAntigravityResponse(input)
	// minimum is moved to description hint
	if !strings.Contains(result, "9007199254740993") {
		t.Errorf("large integer precision was lost: %s", result)
	}
}

// TestCleanJSONSchema_BarePropertyMapWithRequestAndToolsNames tests that property names like
// "request", "tools", "headers", "messages" inside bare property maps are correctly normalized.
func TestCleanJSONSchema_BarePropertyMapWithRequestAndToolsNames(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"data": {
				"request": {
					"method": { "type": "string", "required": true },
					"url": { "type": "string" }
				},
				"headers": {
					"authorization": { "type": "string" }
				},
				"tools": {
					"name": { "type": "string" }
				}
			}
		}
	}`

	for cleaner, clean := range map[string]func(string) string{
		"antigravity":         CleanJSONSchemaForAntigravity,
		"gemini":              CleanJSONSchemaForGemini,
		"antigravityResponse": CleanJSONSchemaForAntigravityResponse,
	} {
		got := clean(input)
		parsed := gjson.Parse(got)

		if parsed.Get("properties.data.type").String() != "object" {
			t.Errorf("%s: properties.data.type = %q, want object; got schema: %s", cleaner, parsed.Get("properties.data.type").String(), got)
		}
		if parsed.Get("properties.data.properties.headers.type").String() != "object" {
			t.Errorf("%s: properties.data.properties.headers.type = %q, want object; got schema: %s", cleaner, parsed.Get("properties.data.properties.headers.type").String(), got)
		}
		if parsed.Get("properties.data.properties.tools.type").String() != "object" {
			t.Errorf("%s: properties.data.properties.tools.type = %q, want object; got schema: %s", cleaner, parsed.Get("properties.data.properties.tools.type").String(), got)
		}
		if parsed.Get("properties.data.properties.request.type").String() != "object" {
			t.Errorf("%s: properties.data.properties.request.type = %q, want object; got schema: %s", cleaner, parsed.Get("properties.data.properties.request.type").String(), got)
		}
		if parsed.Get("properties.data.properties.request.properties.method.type").String() != "string" {
			t.Errorf("%s: properties.data.properties.request.properties.method.type = %q, want string; got schema: %s", cleaner, parsed.Get("properties.data.properties.request.properties.method.type").String(), got)
		}
		var reqReq []string
		for _, r := range parsed.Get("properties.data.properties.request.required").Array() {
			reqReq = append(reqReq, r.String())
		}
		if !contains(reqReq, "method") {
			t.Errorf("%s: request.required = %v, want 'method'; got schema: %s", cleaner, reqReq, got)
		}
	}
}

// TestCleanJSONSchema_BarePropertyMapWithSiblingDescription tests bare property maps with sibling
// annotations (e.g. description, title, required) alongside child property definitions.
func TestCleanJSONSchema_BarePropertyMapWithSiblingDescription(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"data": {
				"description": "Task payload",
				"parent": { "type": "string", "required": true },
				"insert_after": { "type": "string" }
			}
		}
	}`

	for cleaner, clean := range map[string]func(string) string{
		"antigravity":         CleanJSONSchemaForAntigravity,
		"gemini":              CleanJSONSchemaForGemini,
		"antigravityResponse": CleanJSONSchemaForAntigravityResponse,
	} {
		got := clean(input)
		parsed := gjson.Parse(got)

		if parsed.Get("properties.data.type").String() != "object" {
			t.Errorf("%s: properties.data.type = %q, want object; got schema: %s", cleaner, parsed.Get("properties.data.type").String(), got)
		}
		if parsed.Get("properties.data.description").String() != "Task payload" {
			t.Errorf("%s: properties.data.description = %q, want 'Task payload'; got schema: %s", cleaner, parsed.Get("properties.data.description").String(), got)
		}
		if parsed.Get("properties.data.properties.parent.type").String() != "string" {
			t.Errorf("%s: properties.data.properties.parent.type = %q, want string; got schema: %s", cleaner, parsed.Get("properties.data.properties.parent.type").String(), got)
		}
		if parsed.Get("properties.data.properties.insert_after.type").String() != "string" {
			t.Errorf("%s: properties.data.properties.insert_after.type = %q, want string; got schema: %s", cleaner, parsed.Get("properties.data.properties.insert_after.type").String(), got)
		}
		var dataReq []string
		for _, r := range parsed.Get("properties.data.required").Array() {
			dataReq = append(dataReq, r.String())
		}
		if !contains(dataReq, "parent") {
			t.Errorf("%s: properties.data.required = %v, want 'parent'; got schema: %s", cleaner, dataReq, got)
		}
	}
}

// TestCleanJSONSchema_SingleKeySchemaWrapper tests that cleanNestedSchema wrapper {"schema": ...}
// is unwrapped, normalized, and placeholder is properly placed without root pollution.
func TestCleanJSONSchema_SingleKeySchemaWrapper(t *testing.T) {
	inner := `{
		"type": "object",
		"properties": {
			"data": {
				"parent": { "type": "string", "required": true }
			}
		}
	}`
	wrapped := `{"schema": ` + inner + `}`

	result := CleanJSONSchemaForAntigravityTool(wrapped, true)
	parsed := gjson.Parse(result)

	if !parsed.Get("schema").Exists() {
		t.Fatalf("wrapper key 'schema' was lost: %s", result)
	}
	if parsed.Get("schema.properties.data.type").String() != "object" {
		t.Errorf("schema.properties.data.type = %q, want object; got: %s", parsed.Get("schema.properties.data.type").String(), result)
	}
	if parsed.Get("schema.properties.data.properties.parent.type").String() != "string" {
		t.Errorf("schema.properties.data.properties.parent.type = %q, want string; got: %s", parsed.Get("schema.properties.data.properties.parent.type").String(), result)
	}
	var dataReq []string
	for _, r := range parsed.Get("schema.properties.data.required").Array() {
		dataReq = append(dataReq, r.String())
	}
	if !contains(dataReq, "parent") {
		t.Errorf("schema.properties.data.required = %v, want 'parent'; got: %s", dataReq, result)
	}
}

// TestCleanJSONSchema_BarePropertyMapWithExplicitTypeObject tests that nodes declaring
// type: "object" but omitting properties wrapper are correctly normalized.
func TestCleanJSONSchema_BarePropertyMapWithExplicitTypeObject(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"data": {
				"type": "object",
				"parent": { "type": "string", "required": true },
				"insert_after": { "type": "string" }
			}
		}
	}`

	for cleaner, clean := range map[string]func(string) string{
		"antigravity":         CleanJSONSchemaForAntigravity,
		"gemini":              CleanJSONSchemaForGemini,
		"antigravityResponse": CleanJSONSchemaForAntigravityResponse,
	} {
		got := clean(input)
		parsed := gjson.Parse(got)

		if parsed.Get("properties.data.type").String() != "object" {
			t.Errorf("%s: properties.data.type = %q, want object; got schema: %s", cleaner, parsed.Get("properties.data.type").String(), got)
		}
		if parsed.Get("properties.data.properties.parent.type").String() != "string" {
			t.Errorf("%s: properties.data.properties.parent.type = %q, want string; got schema: %s", cleaner, parsed.Get("properties.data.properties.parent.type").String(), got)
		}
		if parsed.Get("properties.data.properties.insert_after.type").String() != "string" {
			t.Errorf("%s: properties.data.properties.insert_after.type = %q, want string; got schema: %s", cleaner, parsed.Get("properties.data.properties.insert_after.type").String(), got)
		}
		var dataReq []string
		for _, r := range parsed.Get("properties.data.required").Array() {
			dataReq = append(dataReq, r.String())
		}
		if !contains(dataReq, "parent") {
			t.Errorf("%s: properties.data.required = %v, want 'parent'; got schema: %s", cleaner, dataReq, got)
		}
	}
}

// TestCleanJSONSchema_BarePropertyMapWithNullable tests bare property maps with nullable: true.
func TestCleanJSONSchema_BarePropertyMapWithNullable(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"data": {
				"nullable": true,
				"description": "Task payload",
				"parent": { "type": "string" }
			}
		}
	}`

	for cleaner, clean := range map[string]func(string) string{
		"antigravityResponse": CleanJSONSchemaForAntigravityResponse,
		"gemini":              CleanJSONSchemaForGemini,
	} {
		got := clean(input)
		parsed := gjson.Parse(got)

		if parsed.Get("properties.data.type").String() != "object" {
			t.Errorf("%s: properties.data.type = %q, want object; got schema: %s", cleaner, parsed.Get("properties.data.type").String(), got)
		}
		if parsed.Get("properties.data.properties.parent.type").String() != "string" {
			t.Errorf("%s: properties.data.properties.parent.type = %q, want string; got schema: %s", cleaner, parsed.Get("properties.data.properties.parent.type").String(), got)
		}
	}
}

// TestCleanJSONSchema_PreservesHTMLCharactersWithoutEscaping tests that < > & in descriptions
// are not converted into HTML entities (\u003c, \u003e, \u0026).
func TestCleanJSONSchema_PreservesHTMLCharactersWithoutEscaping(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"data": {
				"description": "Uses <tag> & symbols > threshold",
				"parent": { "type": "string" }
			}
		}
	}`

	result := CleanJSONSchemaForAntigravityResponse(input)
	if strings.Contains(result, `\u003c`) || strings.Contains(result, `\u003e`) || strings.Contains(result, `\u0026`) {
		t.Errorf("HTML characters were escaped: %s", result)
	}
	if !strings.Contains(result, "<tag>") || !strings.Contains(result, "& symbols >") {
		t.Errorf("Original description with HTML characters was corrupted: %s", result)
	}
}

// TestCleanJSONSchema_VendorExtensionOnEnumNotWrappedIntoProperties tests that vendor extensions
// on non-object types (e.g. x-google-enum-descriptions on a string enum) are not wrapped into properties.
func TestCleanJSONSchema_VendorExtensionOnEnumNotWrappedIntoProperties(t *testing.T) {
	input := `{
		"type": "string",
		"enum": ["FOO", "BAR"],
		"x-google-enum-descriptions": {
			"FOO": "Foo option",
			"BAR": "Bar option"
		}
	}`

	for cleaner, clean := range map[string]func(string) string{
		"antigravity":         CleanJSONSchemaForAntigravity,
		"gemini":              CleanJSONSchemaForGemini,
		"antigravityResponse": CleanJSONSchemaForAntigravityResponse,
	} {
		got := clean(input)
		parsed := gjson.Parse(got)

		if parsed.Get("properties").Exists() {
			t.Errorf("%s: string enum gained unexpected properties: %s", cleaner, got)
		}
		if parsed.Get("type").String() != "string" {
			t.Errorf("%s: string type corrupted: %s", cleaner, got)
		}
	}
}

// TestCleanJSONSchema_ObjectDefaultNotWrappedIntoProperties tests that object-typed default
// is not wrapped into properties as an orphan bare property.
func TestCleanJSONSchema_ObjectDefaultNotWrappedIntoProperties(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"settings": {
				"type": "object",
				"default": { "theme": "dark", "lang": "en" }
			}
		}
	}`

	for cleaner, clean := range map[string]func(string) string{
		"antigravity":         CleanJSONSchemaForAntigravity,
		"gemini":              CleanJSONSchemaForGemini,
		"antigravityResponse": CleanJSONSchemaForAntigravityResponse,
	} {
		got := clean(input)
		parsed := gjson.Parse(got)

		// settings must not gain properties.default.properties.theme
		if parsed.Get("properties.settings.properties.default").Exists() {
			t.Errorf("%s: default was converted to property: %s", cleaner, got)
		}
	}
}

// TestCleanJSONSchema_MixedPropertiesAndOrphanBareProperty tests that orphan bare property maps
// alongside an existing properties object are collected into properties.
func TestCleanJSONSchema_MixedPropertiesAndOrphanBareProperty(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"foo": { "type": "string" }
		},
		"bar": {
			"type": "integer",
			"required": true
		}
	}`

	for cleaner, clean := range map[string]func(string) string{
		"antigravity":         CleanJSONSchemaForAntigravity,
		"gemini":              CleanJSONSchemaForGemini,
		"antigravityResponse": CleanJSONSchemaForAntigravityResponse,
	} {
		got := clean(input)
		parsed := gjson.Parse(got)

		if parsed.Get("properties.foo.type").String() != "string" {
			t.Errorf("%s: foo corrupted: %s", cleaner, got)
		}
		if parsed.Get("properties.bar.type").String() != "integer" {
			t.Errorf("%s: orphan bar was not moved to properties: %s", cleaner, got)
		}
		var req []string
		for _, r := range parsed.Get("required").Array() {
			req = append(req, r.String())
		}
		if !contains(req, "bar") {
			t.Errorf("%s: bar required not promoted: %s", cleaner, got)
		}
		if parsed.Get("bar").Exists() {
			t.Errorf("%s: top-level bar survived: %s", cleaner, got)
		}
	}
}

// TestCleanJSONSchema_PreservesAdditionalPropertiesObjectSchema tests that a standalone
// additionalProperties schema is recognized as a structural keyword and not wrapped as a property.
func TestCleanJSONSchema_PreservesAdditionalPropertiesObjectSchema(t *testing.T) {
	input := `{
		"additionalProperties": {
			"type": "string"
		}
	}`

	for cleaner, clean := range map[string]func(string) string{
		"antigravityResponse": CleanJSONSchemaForAntigravityResponse,
		"gemini":              CleanJSONSchemaForGemini,
	} {
		got := clean(input)
		parsed := gjson.Parse(got)
		// Should not be wrapped as properties.additionalProperties
		if parsed.Get("properties.additionalProperties").Exists() {
			t.Errorf("%s: additionalProperties was wrapped into properties: %s", cleaner, got)
		}
	}
}

// TestCleanJSONSchemaForAntigravityResponse_AnyOfRequiredOnlyBranches tests issue 5219 #2:
// anyOf with required-only branches should not overwrite the parent object's type and properties.
func TestCleanJSONSchemaForAntigravityResponse_AnyOfRequiredOnlyBranches(t *testing.T) {
	input := `{
		"type": "object",
		"anyOf": [
			{"required": ["left"]},
			{"required": ["right"]}
		],
		"properties": {
			"left": {"type": "integer"},
			"right": {"type": "integer"}
		},
		"additionalProperties": false
	}`

	got := CleanJSONSchemaForAntigravityResponse(input)
	parsed := gjson.Parse(got)

	if parsed.Get("type").String() != "object" {
		t.Fatalf("type = %q, want object; cleaned: %s", parsed.Get("type").String(), got)
	}
	if !parsed.Get("properties.left").Exists() || !parsed.Get("properties.right").Exists() {
		t.Fatalf("properties were wiped out; cleaned: %s", got)
	}
	if parsed.Get("anyOf").Exists() {
		t.Fatalf("anyOf was not removed; cleaned: %s", got)
	}
}

// TestCleanJSONSchemaForAntigravityResponse_ContainsKeywordStripped tests issue 5219 #3:
// contains keyword in array schemas should be stripped and moved to description hint.
func TestCleanJSONSchemaForAntigravityResponse_ContainsKeywordStripped(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"tags": {
				"type": "array",
				"items": {"type": "string"},
				"contains": {"enum": ["x"]}
			}
		},
		"required": ["tags"],
		"additionalProperties": false
	}`

	for cleaner, clean := range map[string]func(string) string{
		"antigravityResponse": CleanJSONSchemaForAntigravityResponse,
		"antigravity":         CleanJSONSchemaForAntigravity,
		"gemini":              CleanJSONSchemaForGemini,
	} {
		got := clean(input)
		parsed := gjson.Parse(got)
		if parsed.Get("properties.tags.contains").Exists() {
			t.Errorf("%s: contains keyword was not removed: %s", cleaner, got)
		}
		desc := parsed.Get("properties.tags.description").String()
		if !strings.Contains(desc, "contains") {
			t.Errorf("%s: contains description hint missing: %s", cleaner, got)
		}
	}
}
