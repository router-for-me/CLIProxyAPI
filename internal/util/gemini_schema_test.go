package util

import (
	"encoding/json"
	"reflect"
	"strconv"
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
				"anyOf": [
					{ "type": "string" },
					{ "type": "integer" }
				]
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

	expected := `{
		"type": "object",
		"properties": {
			"value": {
				"anyOf": [
					{ "type": "string" },
					{ "type": "integer" }
				],
				"nullable": true,
				"description": "(nullable)"
			}
		}
	}`

	result := CleanJSONSchemaForAntigravity(input)
	compareJSON(t, expected, result)
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
	nullableInput := `{
		"type": "object",
		"properties": {
			"config": {
				"description": "Parent desc",
				"anyOf": [
					{ "type": "string", "description": "Child desc" },
					{ "type": "null" }
				]
			}
		}
	}`

	expectedNullable := `{
		"type": "object",
		"properties": {
			"config": {
				"type": "string",
				"nullable": true,
				"description": "Parent desc (Child desc) (Accepts: string | null)"
			}
		}
	}`

	resultNullable := CleanJSONSchemaForAntigravity(nullableInput)
	compareJSON(t, expectedNullable, resultNullable)

	multiBranchInput := `{
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

	resultMulti := CleanJSONSchemaForAntigravity(multiBranchInput)
	compareJSON(t, multiBranchInput, resultMulti)
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
// anyOf with required-only branches should not overwrite the parent object's type and properties,
// and should materialize valid typed object branches from parent properties so the union is preserved.
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
	branches := parsed.Get("anyOf").Array()
	if len(branches) != 2 {
		t.Fatalf("anyOf branch count = %d, want 2; cleaned: %s", len(branches), got)
	}
	if branches[0].Get("type").String() != "object" || !branches[0].Get("properties.left").Exists() || branches[0].Get("required.0").String() != "left" {
		t.Fatalf("anyOf[0] was not materialized into valid object branch: %s", branches[0].Raw)
	}
	if branches[1].Get("type").String() != "object" || !branches[1].Get("properties.right").Exists() || branches[1].Get("required.0").String() != "right" {
		t.Fatalf("anyOf[1] was not materialized into valid object branch: %s", branches[1].Raw)
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

// TestCleanJSONSchema_RemovesDraft04IdAndSchemaIdentifierKeywords covers Issue #5888:
// Draft-04 schema identifier "id" and Draft 2019-09/2020-12 identifier keywords ($anchor, $vocabulary,
// $dynamicRef, $dynamicAnchor) should be stripped from schema nodes while preserving properties legitimately named "id".
func TestCleanJSONSchema_RemovesDraft04IdAndSchemaIdentifierKeywords(t *testing.T) {
	// Repro case from Issue #5888: MCP tool property schema containing "id": "ContentType"
	input := `{
		"id": "http://example.com/root.json",
		"$anchor": "rootAnchor",
		"$vocabulary": {"https://json-schema.org/draft/2020-12/vocab/core": true},
		"type": "object",
		"properties": {
			"kind": {
				"type": "string",
				"enum": ["short", "video"],
				"id": "ContentType",
				"$anchor": "contentTypeAnchor",
				"$dynamicAnchor": "dynAnchor",
				"$dynamicRef": "#dynAnchor",
				"description": "Kind"
			},
			"id": {
				"type": "string",
				"description": "Property legitimately named id should survive"
			}
		},
		"required": ["kind"]
	}`

	for cleaner, clean := range map[string]func(string) string{
		"gemini":              CleanJSONSchemaForGemini,
		"antigravity":         CleanJSONSchemaForAntigravity,
		"antigravityTool":     func(s string) string { return CleanJSONSchemaForAntigravityTool(s, false) },
		"antigravityResponse": CleanJSONSchemaForAntigravityResponse,
	} {
		got := clean(input)
		parsed := gjson.Parse(got)

		// Root keywords should be stripped
		if parsed.Get("id").Exists() {
			t.Errorf("%s: root 'id' was not removed: %s", cleaner, got)
		}
		if parsed.Get("$anchor").Exists() {
			t.Errorf("%s: root '$anchor' was not removed: %s", cleaner, got)
		}
		if parsed.Get("$vocabulary").Exists() {
			t.Errorf("%s: root '$vocabulary' was not removed: %s", cleaner, got)
		}

		// Keywords inside property schema should be stripped
		if parsed.Get("properties.kind.id").Exists() {
			t.Errorf("%s: 'properties.kind.id' was not removed: %s", cleaner, got)
		}
		if parsed.Get("properties.kind.$anchor").Exists() {
			t.Errorf("%s: 'properties.kind.$anchor' was not removed: %s", cleaner, got)
		}
		if parsed.Get("properties.kind.$dynamicAnchor").Exists() {
			t.Errorf("%s: 'properties.kind.$dynamicAnchor' was not removed: %s", cleaner, got)
		}
		if parsed.Get("properties.kind.$dynamicRef").Exists() {
			t.Errorf("%s: 'properties.kind.$dynamicRef' was not removed: %s", cleaner, got)
		}

		// Property named "id" must be preserved
		if !parsed.Get("properties.id").Exists() {
			t.Errorf("%s: property named 'id' was incorrectly removed: %s", cleaner, got)
		}
		if parsed.Get("properties.id.type").String() != "string" {
			t.Errorf("%s: property named 'id' type corrupted: %s", cleaner, got)
		}
	}

	// Real-world MCP repro: definition carrying "id": "ContentType" expanded via $ref
	refInput := `{
		"definitions": {
			"ContentType": {
				"type": "string",
				"enum": ["short", "video"],
				"id": "ContentType",
				"description": "Kind"
			}
		},
		"type": "object",
		"properties": {
			"kind": { "$ref": "#/definitions/ContentType" }
		},
		"required": ["kind"]
	}`
	for cleaner, clean := range map[string]func(string) string{
		"antigravity":         CleanJSONSchemaForAntigravity,
		"antigravityResponse": CleanJSONSchemaForAntigravityResponse,
	} {
		got := clean(refInput)
		parsed := gjson.Parse(got)
		if !parsed.Get("properties.kind").Exists() || parsed.Get("properties.kind.type").String() != "string" {
			t.Errorf("%s: inlined $ref property 'properties.kind' corrupted or missing: %s", cleaner, got)
		}
		if parsed.Get("properties.kind.id").Exists() {
			t.Errorf("%s: inlined $ref 'properties.kind.id' was not removed: %s", cleaner, got)
		}
		if parsed.Get("definitions").Exists() {
			t.Errorf("%s: 'definitions' was not removed: %s", cleaner, got)
		}
	}
}

func TestCleanJSONSchema_TrueBooleanSubschemas(t *testing.T) {
	// Issue #3551: Antigravity rejects OpenAI function tools containing `true` JSON Schema subschemas.
	input := `{
		"type": "object",
		"properties": {
			"screenshot_id": true,
			"filename": true,
			"file_size": true,
			"source_file_checksum": true,
			"disabled_field": false
		},
		"additionalProperties": true
	}`

	for cleaner, clean := range map[string]func(string) string{
		"antigravity":         CleanJSONSchemaForAntigravity,
		"antigravityTool":     func(s string) string { return CleanJSONSchemaForAntigravityTool(s, false) },
		"antigravityResponse": CleanJSONSchemaForAntigravityResponse,
		"gemini":              CleanJSONSchemaForGemini,
	} {
		got := clean(input)
		parsed := gjson.Parse(got)

		// true subschemas under properties must be converted to empty object schemas {}
		for _, prop := range []string{"screenshot_id", "filename", "file_size", "source_file_checksum"} {
			val := parsed.Get("properties." + prop)
			if !val.Exists() {
				t.Fatalf("%s: expected property %q to exist in %s", cleaner, prop, got)
			}
			if val.Type != gjson.JSON || val.Raw != "{}" {
				t.Errorf("%s: property %q should be normalized to {}, got %s (type %v)", cleaner, prop, val.Raw, val.Type)
			}
		}

		// false subschema must NOT be converted to {} to preserve rejection semantics
		disabledVal := parsed.Get("properties.disabled_field")
		if !disabledVal.Exists() {
			t.Fatalf("%s: expected property 'disabled_field' to exist in %s", cleaner, got)
		}
		if disabledVal.Type != gjson.False {
			t.Errorf("%s: property 'disabled_field' should remain false, got %s", cleaner, disabledVal.Raw)
		}
	}
}

func TestCleanJSONSchema_NestedTrueBooleanSubschemas(t *testing.T) {
	// Issue #3551: Verify boolean true subschema normalization in nested schema positions.
	input := `{
		"type": "object",
		"properties": {
			"tags": {
				"type": "array",
				"items": true
			},
			"tuple": {
				"type": "array",
				"items": [true, {"type": "string"}],
				"additionalItems": true
			},
			"union": {
				"anyOf": [true, {"type": "string"}]
			},
			"combination": {
				"allOf": [true, {"type": "object", "properties": {"opt": true}}]
			},
			"metadata": {
				"type": "object",
				"properties": {
					"nested_true": true,
					"nested_false": false
				}
			},
			"large_int": 9007199254740993
		},
		"$defs": {
			"custom_schema": true
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

		// tags.items: true -> {}
		if val := parsed.Get("properties.tags.items"); val.Exists() && val.Type == gjson.True {
			t.Errorf("%s: tags.items should not be boolean true: %s", cleaner, got)
		}

		// nested properties: true -> {}, false preserved
		if val := parsed.Get("properties.metadata.properties.nested_true"); !val.Exists() || val.Type != gjson.JSON || val.Raw != "{}" {
			t.Errorf("%s: nested_true should be {}, got %s", cleaner, val.Raw)
		}
		if val := parsed.Get("properties.metadata.properties.nested_false"); !val.Exists() || val.Type != gjson.False {
			t.Errorf("%s: nested_false should remain false, got %s", cleaner, val.Raw)
		}

		// tuple items: true in list should be normalized to {}
		if val := parsed.Get("properties.tuple.items.0"); val.Exists() && val.Type == gjson.True {
			t.Errorf("%s: tuple items[0] should not be boolean true: %s", cleaner, got)
		}

		// union / combination inner property
		if val := parsed.Get("properties.combination.properties.opt"); val.Exists() {
			if val.Type == gjson.True {
				t.Errorf("%s: combination.properties.opt should not be boolean true: %s", cleaner, got)
			}
		}

		// large integer preservation
		if val := parsed.Get("properties.large_int"); !val.Exists() || val.Raw != "9007199254740993" {
			t.Errorf("%s: large_int corrupted, got %s", cleaner, val.Raw)
		}
	}
}

func TestCleanJSONSchema_RootAndWrappedTrue(t *testing.T) {
	// Verify root boolean true normalization to {}
	for cleaner, clean := range map[string]func(string) string{
		"antigravity":         CleanJSONSchemaForAntigravity,
		"antigravityTool":     func(s string) string { return CleanJSONSchemaForAntigravityTool(s, false) },
		"antigravityResponse": CleanJSONSchemaForAntigravityResponse,
		"gemini":              CleanJSONSchemaForGemini,
	} {
		got := clean("true")
		if got != "{}" {
			t.Errorf("%s: root true should normalize to {}, got %s", cleaner, got)
		}

		// Verify wrapped {"schema": true} normalization
		wrapped := `{"schema": true}`
		gotWrapped := clean(wrapped)
		parsed := gjson.Parse(gotWrapped)
		if parsed.Get("schema").Exists() && parsed.Get("schema").Type == gjson.True {
			t.Errorf("%s: wrapped schema true should normalize to {}, got %s", cleaner, gotWrapped)
		}
	}
}

func TestCleanJSONSchemaForGeminiJSONSchema_PreservesAdditionalPropertiesAndPattern_Issue5959(t *testing.T) {
	input := `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"title": "SubmitTool",
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"recipient": {
				"type": "string",
				"pattern": "^(alice|bob)$",
				"minLength": 3,
				"maxLength": 10
			},
			"amount": {
				"type": "number",
				"minimum": 1
			},
			"nested": {
				"type": "object",
				"additionalProperties": false,
				"properties": {
					"tag": {
						"type": "string",
						"pattern": "^[a-z]+$"
					}
				}
			},
			"items_list": {
				"type": "array",
				"items": {
					"type": "string",
					"pattern": "^[0-9]+$"
				}
			}
		},
		"required": ["recipient", "amount", "non_existent"]
	}`

	cleaned := CleanJSONSchemaForGeminiJSONSchema(input)
	parsed := gjson.Parse(cleaned)

	if got := parsed.Get("additionalProperties"); !got.Exists() || got.Type != gjson.False {
		t.Fatalf("additionalProperties should be preserved as false, got: %v. Cleaned: %s", got, cleaned)
	}
	if got := parsed.Get("properties.recipient.pattern"); !got.Exists() || got.String() != "^(alice|bob)$" {
		t.Fatalf("pattern should be preserved, got: %v. Cleaned: %s", got, cleaned)
	}
	if got := parsed.Get("properties.recipient.minLength").Int(); got != 3 {
		t.Fatalf("minLength should be preserved as 3, got: %v. Cleaned: %s", got, cleaned)
	}
	if got := parsed.Get("properties.recipient.maxLength").Int(); got != 10 {
		t.Fatalf("maxLength should be preserved as 10, got: %v. Cleaned: %s", got, cleaned)
	}
	if got := parsed.Get("properties.amount.minimum").Int(); got != 1 {
		t.Fatalf("minimum should be preserved as 1, got: %v. Cleaned: %s", got, cleaned)
	}
	if got := parsed.Get("properties.nested.additionalProperties"); !got.Exists() || got.Type != gjson.False {
		t.Fatalf("nested additionalProperties should be preserved as false, got: %v. Cleaned: %s", got, cleaned)
	}
	if got := parsed.Get("properties.nested.properties.tag.pattern"); !got.Exists() || got.String() != "^[a-z]+$" {
		t.Fatalf("nested tag pattern should be preserved, got: %v. Cleaned: %s", got, cleaned)
	}
	if got := parsed.Get("properties.items_list.items.pattern"); !got.Exists() || got.String() != "^[0-9]+$" {
		t.Fatalf("array items pattern should be preserved, got: %v. Cleaned: %s", got, cleaned)
	}
	if parsed.Get("title").Exists() {
		t.Fatalf("title should be removed. Cleaned: %s", cleaned)
	}
	if parsed.Get("$schema").Exists() {
		t.Fatalf("$schema should be removed. Cleaned: %s", cleaned)
	}
	if parsed.Get("description").Exists() && parsed.Get("description").String() == "No extra properties allowed" {
		t.Fatalf("additionalProperties: false should not be converted to description hint. Cleaned: %s", cleaned)
	}
	if got := parsed.Get("properties.recipient.description"); got.Exists() && strings.Contains(got.String(), "pattern:") {
		t.Fatalf("pattern should not be converted to description hint. Cleaned: %s", cleaned)
	}
	if got := parsed.Get("properties.nested.properties.tag.description"); got.Exists() && strings.Contains(got.String(), "pattern:") {
		t.Fatalf("nested pattern should not be converted to description hint. Cleaned: %s", cleaned)
	}
	if got := parsed.Get("properties.items_list.items.description"); got.Exists() && strings.Contains(got.String(), "pattern:") {
		t.Fatalf("array items pattern should not be converted to description hint. Cleaned: %s", cleaned)
	}
	// non_existent should be removed from required because it's not in properties
	required := parsed.Get("required").Array()
	if len(required) != 2 {
		t.Fatalf("required length = %d, want 2. Cleaned: %s", len(required), cleaned)
	}

	// Compare with legacy CleanJSONSchemaForGemini to verify backward compatibility
	legacyCleaned := CleanJSONSchemaForGemini(input)
	legacyParsed := gjson.Parse(legacyCleaned)
	if legacyParsed.Get("additionalProperties").Exists() {
		t.Fatalf("legacy cleaner should strip additionalProperties. Cleaned: %s", legacyCleaned)
	}
	if !strings.Contains(legacyParsed.Get("description").String(), "No extra properties allowed") {
		t.Fatalf("legacy cleaner should add description hint for additionalProperties. Cleaned: %s", legacyCleaned)
	}
	if legacyParsed.Get("properties.recipient.pattern").Exists() {
		t.Fatalf("legacy cleaner should strip pattern. Cleaned: %s", legacyCleaned)
	}
}

func TestCleanJSONSchemaForGeminiJSONSchema_PreservesSchemaValuedAdditionalProperties(t *testing.T) {
	input := `{
		"type": "object",
		"additionalProperties": {
			"type": "string",
			"pattern": "^[a-z]+$",
			"minLength": 2
		}
	}`

	cleaned := CleanJSONSchemaForGeminiJSONSchema(input)
	parsed := gjson.Parse(cleaned)

	ap := parsed.Get("additionalProperties")
	if !ap.Exists() || !ap.IsObject() {
		t.Fatalf("additionalProperties object should be preserved, got: %s", cleaned)
	}
	if got := ap.Get("type").String(); got != "string" {
		t.Fatalf("additionalProperties.type = %q, want string", got)
	}
	if got := ap.Get("pattern").String(); got != "^[a-z]+$" {
		t.Fatalf("additionalProperties.pattern = %q, want ^[a-z]+$", got)
	}
	if got := ap.Get("minLength").Int(); got != 2 {
		t.Fatalf("additionalProperties.minLength = %d, want 2", got)
	}
	if ap.Get("description").Exists() && strings.Contains(ap.Get("description").String(), "pattern:") {
		t.Fatalf("additionalProperties pattern should not be converted to description hint: %s", cleaned)
	}

	// Legacy cleaner should strip schema-valued additionalProperties
	legacyCleaned := CleanJSONSchemaForGemini(input)
	if gjson.Get(legacyCleaned, "additionalProperties").Exists() {
		t.Fatalf("legacy CleanJSONSchemaForGemini should strip additionalProperties schema: %s", legacyCleaned)
	}
}

// TestCleanJSONSchema_ArrayItemsRequireArrayType_Issue6011 covers Issue #6011:
// Gemini rejects tool schemas where a property declares "items" but its type is not "array"
// (e.g. type is omitted or flattened to a non-array type), returning:
// "field predicate failed: $type == Type.ARRAY".
func TestCleanJSONSchema_ArrayItemsRequireArrayType_Issue6011(t *testing.T) {
	// Case 1: property declares "items" but omits "type": "array" (relying on JSON Schema inference)
	// Reproduces the error path reported in Issue #6011: properties.revision_reasons.items.properties.evidence_reference
	inputMissingType := `{
		"type": "object",
		"properties": {
			"revision_reasons": {
				"type": "array",
				"items": {
					"type": "object",
					"properties": {
						"evidence_reference": {
							"description": "references to evidence",
							"items": {
								"type": "string"
							}
						}
					}
				}
			}
		}
	}`

	cleaners := map[string]func(string) string{
		"gemini":            CleanJSONSchemaForGemini,
		"geminiJSONSchema":  CleanJSONSchemaForGeminiJSONSchema,
		"antigravity":       CleanJSONSchemaForAntigravity,
		"antigravityLegacy": func(s string) string { return CleanJSONSchemaForAntigravityTool(s, false) },
	}

	for name, clean := range cleaners {
		t.Run(name+"_missing_type", func(t *testing.T) {
			cleaned := clean(inputMissingType)
			parsed := gjson.Parse(cleaned)
			target := parsed.Get("properties.revision_reasons.items.properties.evidence_reference")
			if !target.Exists() {
				t.Fatalf("evidence_reference property missing in %s: %s", name, cleaned)
			}
			if gotType := target.Get("type").String(); gotType != "array" {
				t.Fatalf("%s: evidence_reference.type = %q, want 'array'; cleaned schema: %s", name, gotType, cleaned)
			}
			if gotItemsType := target.Get("items.type").String(); gotItemsType != "string" {
				t.Fatalf("%s: evidence_reference.items.type = %q, want 'string'; cleaned schema: %s", name, gotItemsType, cleaned)
			}
		})
	}

	// Case 2: property declares union type: ["string", "array"] with items
	inputUnionType := `{
		"type": "object",
		"properties": {
			"revision_reasons": {
				"type": "array",
				"items": {
					"type": "object",
					"properties": {
						"evidence_reference": {
							"type": ["string", "array"],
							"items": {
								"type": "string"
							}
						}
					}
				}
			}
		}
	}`

	for name, clean := range cleaners {
		t.Run(name+"_union_type_with_items", func(t *testing.T) {
			cleaned := clean(inputUnionType)
			parsed := gjson.Parse(cleaned)
			target := parsed.Get("properties.revision_reasons.items.properties.evidence_reference")
			if gotType := target.Get("type").String(); gotType != "array" {
				t.Fatalf("%s: evidence_reference.type = %q, want 'array' when items is defined; cleaned schema: %s", name, gotType, cleaned)
			}
		})
	}

	// Case 3: property has explicit non-array type (e.g. "string") with extraneous items
	inputNonArrayWithItems := `{
		"type": "object",
		"properties": {
			"label": {
				"type": "string",
				"items": { "type": "string" }
			},
			"config": {
				"type": "object",
				"properties": { "key": { "type": "string" } },
				"items": { "type": "string" }
			}
		}
	}`

	for name, clean := range cleaners {
		t.Run(name+"_non_array_strips_items", func(t *testing.T) {
			cleaned := clean(inputNonArrayWithItems)
			parsed := gjson.Parse(cleaned)
			if parsed.Get("properties.label.items").Exists() {
				t.Fatalf("%s: properties.label should not retain items on string type: %s", name, cleaned)
			}
			if parsed.Get("properties.config.items").Exists() {
				t.Fatalf("%s: properties.config should not retain items on object type: %s", name, cleaned)
			}
			if parsed.Get("properties.label.type").String() != "string" {
				t.Fatalf("%s: properties.label.type should remain string: %s", name, cleaned)
			}
		})
	}

	// Case 4: property literally named "items" should be preserved and not treated as array items
	inputPropertyNamedItems := `{
		"type": "object",
		"properties": {
			"items": {
				"type": "string",
				"description": "a field named items"
			}
		}
	}`

	for name, clean := range cleaners {
		t.Run(name+"_property_named_items_preserved", func(t *testing.T) {
			cleaned := clean(inputPropertyNamedItems)
			parsed := gjson.Parse(cleaned)
			itemProp := parsed.Get("properties.items")
			if !itemProp.Exists() {
				t.Fatalf("%s: property named 'items' was removed: %s", name, cleaned)
			}
			if itemProp.Get("type").String() != "string" {
				t.Fatalf("%s: property named 'items' type changed: %s", name, cleaned)
			}
		})
	}

	// Case 5: Root array with items missing type
	inputRootArrayMissingType := `{
		"items": {
			"type": "string"
		}
	}`

	for name, clean := range cleaners {
		t.Run(name+"_root_array_missing_type", func(t *testing.T) {
			cleaned := clean(inputRootArrayMissingType)
			parsed := gjson.Parse(cleaned)
			if parsed.Get("type").String() != "array" {
				t.Fatalf("%s: root schema type should be inferred as 'array': %s", name, cleaned)
			}
			if parsed.Get("items.type").String() != "string" {
				t.Fatalf("%s: root schema items.type should remain 'string': %s", name, cleaned)
			}
		})
	}

	// Case 6: Cleaning is idempotent across all cleaners
	for name, clean := range cleaners {
		t.Run(name+"_idempotent", func(t *testing.T) {
			cleaned1 := clean(inputMissingType)
			cleaned2 := clean(cleaned1)
			if cleaned1 != cleaned2 {
				t.Fatalf("%s: cleaner is not idempotent.\nFirst:\n%s\nSecond:\n%s", name, cleaned1, cleaned2)
			}
		})
	}

	// Case 7: Response schema regression check
	t.Run("antigravity_response_schema", func(t *testing.T) {
		// Response schema declaring items without type infers array type
		respInput := `{"type":"object","properties":{"names":{"items":{"type":"string"}}}}`
		cleaned := CleanJSONSchemaForAntigravityResponse(respInput)
		parsed := gjson.Parse(cleaned)
		if parsed.Get("properties.names.type").String() != "array" {
			t.Fatalf("response schema should infer type 'array' when items is defined: %s", cleaned)
		}
		// Response array missing items still remains untouched
		respNoItems := `{"type":"object","properties":{"values":{"type":"array"}}}`
		cleanedNoItems := CleanJSONSchemaForAntigravityResponse(respNoItems)
		parsedNoItems := gjson.Parse(cleanedNoItems)
		if parsedNoItems.Get("properties.values.items").Exists() {
			t.Fatalf("response array missing items should not gain placeholder items: %s", cleanedNoItems)
		}
	})
}

func TestSanitizeArrayItems_PreservesItemsForUppercaseArrayType(t *testing.T) {
	input := `{
		"type": "OBJECT",
		"properties": {
			"summary": {"type": "STRING"},
			"brands": {
				"type": "ARRAY",
				"items": {"type": "STRING"}
			},
			"catalog": {
				"type": "OBJECT",
				"properties": {
					"items": {
						"type": "ARRAY",
						"items": {
							"type": "OBJECT",
							"properties": {"id": {"type": "STRING"}}
						}
					}
				}
			}
		}
	}`

	cleaners := map[string]func(string) string{
		"AntigravityResponse": CleanJSONSchemaForAntigravityResponse,
		"Antigravity":         CleanJSONSchemaForAntigravity,
		"Gemini":              CleanJSONSchemaForGemini,
		"AntigravityTool":     func(s string) string { return CleanJSONSchemaForAntigravityTool(s, false) },
	}

	for name, clean := range cleaners {
		t.Run(name, func(t *testing.T) {
			got := clean(input)
			parsed := gjson.Parse(got)

			brandsItems := parsed.Get("properties.brands.items")
			if !brandsItems.Exists() {
				t.Fatalf("[%s] properties.brands.items was stripped from uppercase ARRAY: %s", name, got)
			}
			typeStr := parsed.Get("properties.brands.type").String()
			if !strings.EqualFold(typeStr, "array") {
				t.Fatalf("[%s] properties.brands.type corrupted: %s", name, got)
			}

			nestedItems := parsed.Get("properties.catalog.properties.items.items")
			if !nestedItems.Exists() {
				t.Fatalf("[%s] properties.catalog.properties.items.items was stripped: %s", name, got)
			}
		})
	}
}

// TestCleanJSONSchema_EnforcesObjectTypeForProperties_Issue6394 tests issue 6394:
// When a property defines nested properties/required (e.g. through a union type like ["string", "object"],
// merged anyOf branches, or missing type), Gemini requires type to be OBJECT.
// Properties or required must never be emitted on a non-OBJECT type node.
func TestCleanJSONSchema_EnforcesObjectTypeForProperties_Issue6394(t *testing.T) {
	cleaners := map[string]func(string) string{
		"Gemini":              CleanJSONSchemaForGemini,
		"GeminiJSONSchema":    CleanJSONSchemaForGeminiJSONSchema,
		"AntigravityTool":     func(s string) string { return CleanJSONSchemaForAntigravityTool(s, false) },
		"AntigravityResponse": CleanJSONSchemaForAntigravityResponse,
	}

	t.Run("TypeArrayWithPropertiesPrefersObject", func(t *testing.T) {
		input := `{"type":"object","properties":{"requestBody":{"type":"object","properties":{"delivery":{"type":"object","properties":{"endpoint":{"type":["string","object"],"properties":{"traces":{"type":"string"}},"required":["traces"]}},"required":["endpoint"]}},"required":["delivery"]}},"required":["requestBody"]}`

		for name, clean := range cleaners {
			t.Run(name, func(t *testing.T) {
				got := clean(input)
				parsed := gjson.Parse(got)

				endpointType := parsed.Get("properties.requestBody.properties.delivery.properties.endpoint.type").String()
				if !strings.EqualFold(endpointType, "object") {
					t.Fatalf("[%s] endpoint type = %q, want object; cleaned: %s", name, endpointType, got)
				}
				if !parsed.Get("properties.requestBody.properties.delivery.properties.endpoint.properties.traces").Exists() {
					t.Fatalf("[%s] endpoint.properties.traces missing; cleaned: %s", name, got)
				}
			})
		}
	})

	t.Run("AnyOfUnionMergedIntoNodeWithoutTypeEnsuresObject", func(t *testing.T) {
		input := `{"type":"object","properties":{"endpoint":{"properties":{"traces":{"type":"string"}},"required":["traces"],"anyOf":[{"type":"string"},{"type":"object","properties":{"other":{"type":"string"}}}]}}}`

		for name, clean := range cleaners {
			t.Run(name, func(t *testing.T) {
				got := clean(input)
				parsed := gjson.Parse(got)

				endpointType := parsed.Get("properties.endpoint.type").String()
				if !strings.EqualFold(endpointType, "object") {
					t.Fatalf("[%s] endpoint type = %q, want object; cleaned: %s", name, endpointType, got)
				}
				if !parsed.Get("properties.endpoint.properties.traces").Exists() {
					t.Fatalf("[%s] endpoint.properties.traces missing; cleaned: %s", name, got)
				}
			})
		}
	})

	t.Run("PrimitiveTypeWithPropertiesPromotedToObject", func(t *testing.T) {
		input := `{"type":"object","properties":{"endpoint":{"type":"string","properties":{"traces":{"type":"string"}},"required":["traces"]}}}`

		for name, clean := range cleaners {
			t.Run(name, func(t *testing.T) {
				got := clean(input)
				parsed := gjson.Parse(got)

				endpointType := parsed.Get("properties.endpoint.type").String()
				if !strings.EqualFold(endpointType, "object") {
					t.Fatalf("[%s] endpoint type = %q, want object; cleaned: %s", name, endpointType, got)
				}
			})
		}
	})
}

// TestCleanJSONSchema_RemovesDependentKeywords_Issue6539 covers Issue #6539:
// JSON Schema keywords dependentRequired, dependentSchemas, and dependencies are rejected by
// the Gemini/Antigravity backend with 400 INVALID_ARGUMENT ("Unknown name \"dependentRequired\" ... Cannot find field")
// and must be stripped while preserving legitimate tool properties with those names.
func TestCleanJSONSchema_RemovesDependentKeywords_Issue6539(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"baseline_url": {"type": "string"},
			"candidate_url": {"type": "string"},
			"options": {
				"type": "object",
				"properties": {
					"threshold": {"type": "number"},
					"diff_color": {"type": "string"}
				},
				"dependentRequired": {
					"threshold": ["diff_color"]
				},
				"dependentSchemas": {
					"threshold": {
						"properties": {
							"diff_color": {"type": "string"}
						}
					}
				},
				"dependencies": {
					"threshold": ["diff_color"]
				}
			},
			"dependentRequired": {
				"type": "string",
				"description": "Legitimate property named dependentRequired"
			},
			"dependentSchemas": {
				"type": "object",
				"properties": {
					"nested": {"type": "string"}
				},
				"dependentRequired": {
					"nested": ["other"]
				}
			},
			"dependencies": {
				"type": "string",
				"description": "Legitimate property named dependencies"
			}
		},
		"dependentRequired": {
			"baseline_url": ["candidate_url"]
		},
		"dependentSchemas": {
			"baseline_url": {
				"required": ["candidate_url"]
			}
		},
		"dependencies": {
			"baseline_url": ["candidate_url"]
		}
	}`

	cleaners := map[string]func(string) string{
		"Antigravity":         CleanJSONSchemaForAntigravity,
		"AntigravityTool":     func(s string) string { return CleanJSONSchemaForAntigravityTool(s, false) },
		"AntigravityResponse": CleanJSONSchemaForAntigravityResponse,
		"Gemini":              CleanJSONSchemaForGemini,
		"GeminiJSONSchema":    CleanJSONSchemaForGeminiJSONSchema,
	}

	for name, clean := range cleaners {
		t.Run(name, func(t *testing.T) {
			got := clean(input)
			parsed := gjson.Parse(got)

			for _, kw := range []string{"dependentRequired", "dependentSchemas", "dependencies"} {
				if parsed.Get(kw).Exists() {
					t.Errorf("[%s] root %q was not removed: %s", name, kw, got)
				}
				if parsed.Get("properties.options." + kw).Exists() {
					t.Errorf("[%s] nested properties.options.%s was not removed: %s", name, kw, got)
				}
			}

			if parsed.Get("properties.dependentSchemas.dependentRequired").Exists() {
				t.Errorf("[%s] properties.dependentSchemas.dependentRequired was not removed: %s", name, got)
			}

			for _, prop := range []string{"dependentRequired", "dependentSchemas", "dependencies"} {
				if !parsed.Get("properties." + prop).Exists() {
					t.Errorf("[%s] legitimate property %q was removed: %s", name, prop, got)
				}
			}
		})
	}
}

// TestCleanJSONSchema_PreservesMultiBranchAnyOfOneOf_Issue3662 covers Issue #3662:
// Multi-branch anyOf/oneOf unions (such as string | image object | audio object) must not be
// flattened to a single selected branch, which previously caused non-first object branches (e.g. audio)
// to disappear from the cleaned schema.
// Multi-branch unions are preserved by every cleaner except the Antigravity Claude
// tool cleaner, which flattens them for the Claude-side upstream that rejects nested
// multi-branch unions (Issue 3662 regression follow-up).
func TestCleanJSONSchema_PreservesMultiBranchAnyOfOneOf_Issue3662(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"item": {
				"anyOf": [
					{ "type": "string" },
					{
						"type": "object",
						"properties": {
							"image": { "type": "string" }
						},
						"required": ["image"]
					},
					{
						"type": "object",
						"properties": {
							"audio": { "type": "string" }
						},
						"required": ["audio"]
					}
				]
			}
		},
		"required": ["item"]
	}`

	cleaners := map[string]func(string) string{
		"Antigravity":         CleanJSONSchemaForAntigravity,
		"AntigravityTool":     func(s string) string { return CleanJSONSchemaForAntigravityTool(s, false) },
		"AntigravityResponse": CleanJSONSchemaForAntigravityResponse,
		"Gemini":              CleanJSONSchemaForGemini,
		"GeminiJSONSchema":    CleanJSONSchemaForGeminiJSONSchema,
	}

	for name, clean := range cleaners {
		t.Run(name, func(t *testing.T) {
			got := clean(input)
			compareJSON(t, input, got)

			// Verify idempotency when cleaned a second time
			gotSecond := clean(got)
			compareJSON(t, got, gotSecond)
		})
	}

	t.Run("NullableMultiBranchUnionStripsNullBranchAndSetsNullable", func(t *testing.T) {
		nullableUnion := `{
			"type": "object",
			"properties": {
				"item": {
					"anyOf": [
						{ "type": "string" },
						{
							"type": "object",
							"properties": { "image": { "type": "string" } },
							"required": ["image"]
						},
						{
							"type": "object",
							"properties": { "audio": { "type": "string" } },
							"required": ["audio"]
						},
						{ "type": "null" }
					]
				}
			},
			"required": ["item"]
		}`

		agCleaned := gjson.Parse(CleanJSONSchemaForAntigravity(nullableUnion))
		branches := agCleaned.Get("properties.item.anyOf").Array()
		if len(branches) != 3 {
			t.Fatalf("Antigravity anyOf branch count = %d, want 3: %s", len(branches), agCleaned.Raw)
		}
		if !agCleaned.Get("properties.item.nullable").Bool() {
			t.Fatalf("Antigravity properties.item.nullable = false, want true: %s", agCleaned.Raw)
		}
		if !agCleaned.Get("properties.item.anyOf.2.properties.audio").Exists() {
			t.Fatalf("Antigravity lost audio branch: %s", agCleaned.Raw)
		}

		geminiCleaned := gjson.Parse(CleanJSONSchemaForGeminiJSONSchema(nullableUnion))
		if len(geminiCleaned.Get("properties.item.anyOf").Array()) != 3 {
			t.Fatalf("GeminiJSONSchema anyOf branch count = %d, want 3: %s", len(geminiCleaned.Get("properties.item.anyOf").Array()), geminiCleaned.Raw)
		}
		if geminiCleaned.Get("properties.item.nullable").Exists() {
			t.Fatalf("GeminiJSONSchema should not keep nullable keyword: %s", geminiCleaned.Raw)
		}
		if !strings.Contains(geminiCleaned.Get("properties.item.description").String(), "(nullable)") {
			t.Fatalf("GeminiJSONSchema missing (nullable) hint: %s", geminiCleaned.Raw)
		}

		// Nested nullable union in both [null, nestedUnion] and [nestedUnion, null] orders
		// must unwrap to the nested union with nullable metadata and remain idempotent.
		for _, nestedNullable := range []string{
			`{"type":"object","properties":{"item":{"anyOf":[{"type":"null"},{"anyOf":[{"type":"string"},{"type":"integer"}]}]}}}`,
			`{"type":"object","properties":{"item":{"anyOf":[{"anyOf":[{"type":"string"},{"type":"integer"}]},{"type":"null"}]}}}`,
		} {
			expectedNested := `{
				"type": "object",
				"properties": {
					"item": {
						"anyOf": [
							{ "type": "string" },
							{ "type": "integer" }
						],
						"nullable": true,
						"description": "(nullable)"
					}
				}
			}`
			cleanedOnce := CleanJSONSchemaForAntigravity(nestedNullable)
			compareJSON(t, expectedNested, cleanedOnce)
			cleanedTwice := CleanJSONSchemaForAntigravity(cleanedOnce)
			compareJSON(t, expectedNested, cleanedTwice)
		}
	})

	t.Run("OneOfNormalizedToAnyOfOnNativeSchemaCarriers", func(t *testing.T) {
		oneOfInput := `{
			"type": "object",
			"properties": {
				"item": {
					"oneOf": [
						{ "type": "string" },
						{
							"type": "object",
							"properties": { "image": { "type": "string" } },
							"required": ["image"]
						},
						{
							"type": "object",
							"properties": { "audio": { "type": "string" } },
							"required": ["audio"]
						}
					]
				}
			},
			"required": ["item"]
		}`

		for name, clean := range map[string]func(string) string{
			"Antigravity":         CleanJSONSchemaForAntigravity,
			"AntigravityTool":     func(s string) string { return CleanJSONSchemaForAntigravityTool(s, false) },
			"AntigravityResponse": CleanJSONSchemaForAntigravityResponse,
			"Gemini":              CleanJSONSchemaForGemini,
		} {
			parsed := gjson.Parse(clean(oneOfInput))
			if parsed.Get("properties.item.oneOf").Exists() {
				t.Fatalf("[%s] oneOf survived on native Schema carrier: %s", name, parsed.Raw)
			}
			if got := len(parsed.Get("properties.item.anyOf").Array()); got != 3 {
				t.Fatalf("[%s] anyOf branch count = %d, want 3: %s", name, got, parsed.Raw)
			}
		}

		// parametersJsonSchema carrier preserves standard JSON Schema oneOf
		jsonSchemaParsed := gjson.Parse(CleanJSONSchemaForGeminiJSONSchema(oneOfInput))
		if got := len(jsonSchemaParsed.Get("properties.item.oneOf").Array()); got != 3 {
			t.Fatalf("[GeminiJSONSchema] oneOf branch count = %d, want 3: %s", got, jsonSchemaParsed.Raw)
		}
	})

	t.Run("PreservesUnionAndBranchRequiredWhenParentHasProperties", func(t *testing.T) {
		parentPropsUnion := `{
			"type": "object",
			"properties": {},
			"anyOf": [
				{
					"type": "object",
					"properties": { "image": { "type": "string" } },
					"required": ["image"]
				},
				{
					"type": "object",
					"properties": { "audio": { "type": "string" } },
					"required": ["audio"]
				}
			]
		}`

		for name, clean := range cleaners {
			t.Run(name, func(t *testing.T) {
				got := clean(parentPropsUnion)
				compareJSON(t, parentPropsUnion, got)
			})
		}

		// Mixed union with a required-only branch materializes the required-only branch from parent properties
		// so that the alternative branch (e.g. text) is preserved in anyOf.
		mixedRequiredOnlyBranch := `{
			"type": "object",
			"properties": {
				"image": { "type": "string" },
				"audio": { "type": "string" },
				"text": { "type": "string" }
			},
			"anyOf": [
				{
					"type": "object",
					"properties": { "image": { "type": "string" } },
					"required": ["image"]
				},
				{
					"type": "object",
					"properties": { "audio": { "type": "string" } },
					"required": ["audio"]
				},
				{
					"required": ["text"]
				}
			]
		}`
		expectedMixedRequiredOnly := `{
			"type": "object",
			"properties": {
				"image": { "type": "string" },
				"audio": { "type": "string" },
				"text": { "type": "string" }
			},
			"anyOf": [
				{
					"type": "object",
					"properties": { "image": { "type": "string" } },
					"required": ["image"]
				},
				{
					"type": "object",
					"properties": { "audio": { "type": "string" } },
					"required": ["audio"]
				},
				{
					"type": "object",
					"properties": { "text": { "type": "string" } },
					"required": ["text"]
				}
			]
		}`
		compareJSON(t, expectedMixedRequiredOnly, CleanJSONSchemaForAntigravity(mixedRequiredOnlyBranch))

		// Pure required-only union branches on an object with parent properties are materialized into valid object branches.
		pureRequiredOnlyUnion := `{
			"type": "object",
			"properties": {
				"image": { "type": "string" },
				"audio": { "type": "string" }
			},
			"anyOf": [
				{ "required": ["image"] },
				{ "required": ["audio"] }
			]
		}`
		expectedPureRequiredOnly := `{
			"type": "object",
			"properties": {
				"image": { "type": "string" },
				"audio": { "type": "string" }
			},
			"anyOf": [
				{
					"type": "object",
					"properties": { "image": { "type": "string" } },
					"required": ["image"]
				},
				{
					"type": "object",
					"properties": { "audio": { "type": "string" } },
					"required": ["audio"]
				}
			]
		}`
		compareJSON(t, expectedPureRequiredOnly, CleanJSONSchemaForAntigravity(pureRequiredOnlyUnion))
	})

	t.Run("BooleanUnionBranchesAndSiblingAnyOfOneOf", func(t *testing.T) {
		// Boolean false subschema branches in anyOf/oneOf are unsatisfiable and must be stripped
		// instead of leaking raw boolean false into protobuf Schema anyOf.
		falseSingleBranch := `{"type":"object","properties":{"value":{"anyOf":[false,{"type":"string"}]}}}`
		expectedFalseSingle := `{"type":"object","properties":{"value":{"type":"string"}}}`
		compareJSON(t, expectedFalseSingle, CleanJSONSchemaForAntigravity(falseSingleBranch))

		falseMultiBranch := `{"type":"object","properties":{"value":{"oneOf":[false,{"type":"string"},{"type":"integer"}]}}}`
		expectedFalseMulti := `{"type":"object","properties":{"value":{"anyOf":[{"type":"string"},{"type":"integer"}]}}}`
		compareJSON(t, expectedFalseMulti, CleanJSONSchemaForAntigravity(falseMultiBranch))

		// Boolean true subschema branch in anyOf/oneOf is normalized to an unconstrained {} branch and preserved.
		trueMultiBranch := `{"type":"object","properties":{"value":{"anyOf":[true,{"type":"string"},{"type":"integer"}]}}}`
		expectedTrueMulti := `{"type":"object","properties":{"value":{"anyOf":[{},{"type":"string"},{"type":"integer"}]}}}`
		compareJSON(t, expectedTrueMulti, CleanJSONSchemaForAntigravityTool(trueMultiBranch, false))

		// When a node declares both anyOf and oneOf, native Schema carriers keep anyOf without concatenating
		// oneOf into it and record oneOf as a description hint, while GeminiJSONSchema preserves both.
		siblingUnions := `{
			"type": "object",
			"properties": {
				"value": {
					"anyOf": [
						{ "type": "string" },
						{ "type": "integer" }
					],
					"oneOf": [
						{ "type": "string" },
						{ "type": "boolean" }
					]
				}
			}
		}`
		for name, clean := range map[string]func(string) string{
			"Antigravity":         CleanJSONSchemaForAntigravity,
			"AntigravityTool":     func(s string) string { return CleanJSONSchemaForAntigravityTool(s, false) },
			"AntigravityResponse": CleanJSONSchemaForAntigravityResponse,
			"Gemini":              CleanJSONSchemaForGemini,
		} {
			parsed := gjson.Parse(clean(siblingUnions))
			if parsed.Get("properties.value.oneOf").Exists() {
				t.Fatalf("[%s] sibling oneOf survived on native Schema carrier: %s", name, parsed.Raw)
			}
			if got := len(parsed.Get("properties.value.anyOf").Array()); got != 2 {
				t.Fatalf("[%s] anyOf branch count = %d, want 2 (must not concatenate oneOf): %s", name, got, parsed.Raw)
			}
			if !strings.Contains(parsed.Get("properties.value.description").String(), "oneOf:") {
				t.Fatalf("[%s] missing oneOf description hint: %s", name, parsed.Raw)
			}
		}

		jsonSchemaSibling := gjson.Parse(CleanJSONSchemaForGeminiJSONSchema(siblingUnions))
		if len(jsonSchemaSibling.Get("properties.value.anyOf").Array()) != 2 || len(jsonSchemaSibling.Get("properties.value.oneOf").Array()) != 2 {
			t.Fatalf("[GeminiJSONSchema] expected both anyOf and oneOf to be preserved: %s", jsonSchemaSibling.Raw)
		}

		// Issue 3662 regression: only the Antigravity Claude tool cleaner flattens the
		// multi-branch unions that every other cleaner above preserves, because only the
		// Antigravity Claude backend is known to reject nested multi-branch unions when
		// it converts function declarations to Claude custom.input_schema.
		t.Run("AntigravityClaudeToolFlattensNestedUnions", func(t *testing.T) {
			flattened := gjson.Parse(CleanJSONSchemaForAntigravityClaudeTool(input, false))
			item := flattened.Get("properties.item")
			if item.Get("anyOf").Exists() || item.Get("oneOf").Exists() {
				t.Fatalf("Antigravity Claude tool union survived cleaning: %s", flattened.Raw)
			}
			if got := item.Get("type").String(); got != "object" {
				t.Fatalf("Antigravity Claude flattened item type = %q, want object (strongest branch): %s", got, flattened.Raw)
			}
			if !item.Get("properties.image").Exists() {
				t.Fatalf("Antigravity Claude flattened item lost the first object branch: %s", flattened.Raw)
			}
			if desc := item.Get("description").String(); !strings.Contains(desc, "Accepts:") || !strings.Contains(desc, "string") {
				t.Fatalf("Antigravity Claude flattened item lost the dropped-branch hint: %s", flattened.Raw)
			}

			// Idempotency matters on the Antigravity path: translators clean the schema
			// once, then the executor cleans it again before sending it upstream.
			flattenedTwice := CleanJSONSchemaForAntigravityClaudeTool(flattened.Raw, false)
			compareJSON(t, flattened.Raw, flattenedTwice)

			// Double-cleaning across the canonical translator -> executor chain must
			// keep the flattened Claude shape unchanged on the second pass.
			recleaned := CleanJSONSchemaForAntigravityClaudeTool(CleanJSONSchemaForAntigravity(input), false)
			compareJSON(t, flattened.Raw, recleaned)
			compareJSON(t, flattened.Raw, CleanJSONSchemaForAntigravity(flattened.Raw))

			// Nullable multi-branch unions must keep nullability through the whole
			// translator -> executor chain: the union-preserving first pass strips the
			// null branch and marks the parent nullable:true, so the flattening second
			// pass must inherit that marker instead of silently dropping it.
			nullableUnionInput := `{"type":"object","properties":{"value":{"anyOf":[{"type":"string"},{"type":"integer"},{"type":"null"}]}}}`
			directFlattened := CleanJSONSchemaForAntigravityClaudeTool(nullableUnionInput, false)
			directValue := gjson.Parse(directFlattened).Get("properties.value")
			if !directValue.Get("nullable").Bool() || directValue.Get("type").String() != "string" {
				t.Fatalf("direct Claude flattening lost nullability: %s", directFlattened)
			}
			chainedFlattened := CleanJSONSchemaForAntigravityClaudeTool(CleanJSONSchemaForAntigravity(nullableUnionInput), false)
			chainedValue := gjson.Parse(chainedFlattened).Get("properties.value")
			if !chainedValue.Get("nullable").Bool() || chainedValue.Get("type").String() != "string" || chainedValue.Get("anyOf").Exists() {
				t.Fatalf("translator -> executor chain lost nullability while flattening: %s", chainedFlattened)
			}
			compareJSON(t, chainedFlattened, CleanJSONSchemaForAntigravityClaudeTool(chainedFlattened, false))

			// Boolean subschema branches must not leak raw booleans or unions into the
			// flattened output: true normalizes away, false is unstisfiable and dropped.
			trueBranch := `{"type":"object","properties":{"value":{"anyOf":[true,{"type":"string"},{"type":"integer"}]}}}`
			compareJSON(t, `{"type":"object","properties":{"value":{"type":"string","description":"Accepts: string | integer"}}}`, CleanJSONSchemaForAntigravityClaudeTool(trueBranch, false))
			falseBranch := `{"type":"object","properties":{"value":{"oneOf":[false,{"type":"string"},{"type":"integer"}]}}}`
			compareJSON(t, `{"type":"object","properties":{"value":{"type":"string","description":"Accepts: string | integer"}}}`, CleanJSONSchemaForAntigravityClaudeTool(falseBranch, false))

			// Sibling anyOf + oneOf on one node: both are flattened and neither raw union
			// JSON leaks into the description (paths are processed oneOf-first, so the
			// oneOf branch set wins the single remaining node).
			siblingClaude := gjson.Parse(CleanJSONSchemaForAntigravityClaudeTool(siblingUnions, false))
			siblingValue := siblingClaude.Get("properties.value")
			if siblingValue.Get("anyOf").Exists() || siblingValue.Get("oneOf").Exists() {
				t.Fatalf("Antigravity Claude sibling union survived cleaning: %s", siblingClaude.Raw)
			}
			compareJSON(t, `{"type":"string","description":"Accepts: string | boolean"}`, siblingValue.Raw)
			compareJSON(t, siblingClaude.Raw, CleanJSONSchemaForAntigravityClaudeTool(siblingClaude.Raw, false))
		})

		// Null-only unions (anyOf:[null], oneOf:[null], anyOf:[false, null]) must not panic across any cleaner.
		nullOnlyCleaners := map[string]func(string) string{
			"Antigravity":           CleanJSONSchemaForAntigravity,
			"AntigravityTool":       func(s string) string { return CleanJSONSchemaForAntigravityTool(s, false) },
			"AntigravityClaudeTool": func(s string) string { return CleanJSONSchemaForAntigravityClaudeTool(s, false) },
			"AntigravityResponse":   CleanJSONSchemaForAntigravityResponse,
			"Gemini":                CleanJSONSchemaForGemini,
			"GeminiJSONSchema":      CleanJSONSchemaForGeminiJSONSchema,
		}
		for _, nullOnlyInput := range []string{
			`{"type":"object","properties":{"value":{"anyOf":[{"type":"null"}]}}}`,
			`{"type":"object","properties":{"value":{"oneOf":[{"type":"null"},{"type":"null"}]}}}`,
			`{"type":"object","properties":{"value":{"anyOf":[false,{"type":"null"}],"description":"Null only"}}}`,
		} {
			for name, clean := range nullOnlyCleaners {
				got := clean(nullOnlyInput)
				if !gjson.Valid(got) {
					t.Fatalf("[%s] invalid JSON for null-only union %s: %s", name, nullOnlyInput, got)
				}
				if gjson.Get(got, "properties.value.type").String() != "null" {
					t.Fatalf("[%s] expected properties.value.type = null, got: %s", name, got)
				}
			}
		}
	})
}

// TestCleanJSONSchema_RemovesDiscriminatorKeyword_Issue6639 covers Issue #6639:
// OpenAPI/JSON Schema keyword "discriminator" alongside preserved anyOf/oneOf unions
// (both directly on a property schema and inside array items) or on object schemas
// is rejected by the Gemini/Antigravity backend with 400 INVALID_ARGUMENT
// ("Unknown name \"discriminator\" ... Cannot find field") and must be stripped while
// preserving legitimate tool properties named "discriminator".
func TestCleanJSONSchema_RemovesDiscriminatorKeyword_Issue6639(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"target": {
				"anyOf": [
					{"type": "object", "properties": {"kind": {"const": "a"}, "url": {"type": "string"}}},
					{"type": "object", "properties": {"kind": {"const": "b"}, "path": {"type": "string"}}}
				],
				"discriminator": {"propertyName": "kind"}
			},
			"deliverables": {
				"type": "array",
				"items": {
					"anyOf": [
						{"type": "object", "properties": {"kind": {"const": "file"}, "path": {"type": "string"}}},
						{"type": "object", "properties": {"kind": {"const": "link"}, "url": {"type": "string"}}}
					],
					"discriminator": {"propertyName": "kind"}
				}
			},
			"polymorphicObject": {
				"type": "object",
				"properties": {
					"kind": {"type": "string"}
				},
				"oneOf": [
					{"properties": {"a": {"type": "string"}}},
					{"properties": {"b": {"type": "number"}}}
				],
				"discriminator": {
					"propertyName": "kind",
					"mapping": {"a": "#/definitions/A", "b": "#/definitions/B"}
				}
			},
			"discriminator": {
				"type": "string",
				"description": "Legitimate property named discriminator"
			},
			"nestedDiscriminatorProp": {
				"type": "object",
				"properties": {
					"discriminator": {
						"type": "object",
						"properties": {
							"kind": {"type": "string"}
						},
						"discriminator": {"propertyName": "kind"}
					}
				}
			}
		},
		"discriminator": {"propertyName": "target"}
	}`

	cleaners := map[string]func(string) string{
		"Antigravity":           CleanJSONSchemaForAntigravity,
		"AntigravityTool":       func(s string) string { return CleanJSONSchemaForAntigravityTool(s, false) },
		"AntigravityClaudeTool": func(s string) string { return CleanJSONSchemaForAntigravityClaudeTool(s, false) },
		"AntigravityResponse":   CleanJSONSchemaForAntigravityResponse,
		"Gemini":                CleanJSONSchemaForGemini,
		"GeminiJSONSchema":      CleanJSONSchemaForGeminiJSONSchema,
	}

	for name, clean := range cleaners {
		t.Run(name, func(t *testing.T) {
			got := clean(input)
			parsed := gjson.Parse(got)

			for _, forbiddenPath := range []string{
				"discriminator",
				"properties.target.discriminator",
				"properties.deliverables.items.discriminator",
				"properties.polymorphicObject.discriminator",
				"properties.nestedDiscriminatorProp.properties.discriminator.discriminator",
			} {
				if parsed.Get(forbiddenPath).Exists() {
					t.Errorf("[%s] %s was not removed: %s", name, forbiddenPath, got)
				}
			}

			if name != "AntigravityClaudeTool" {
				if gotBranches := len(parsed.Get("properties.target.anyOf").Array()); gotBranches != 2 {
					t.Errorf("[%s] properties.target.anyOf branch count = %d, want 2: %s", name, gotBranches, got)
				}
				if gotBranches := len(parsed.Get("properties.deliverables.items.anyOf").Array()); gotBranches != 2 {
					t.Errorf("[%s] properties.deliverables.items.anyOf branch count = %d, want 2: %s", name, gotBranches, got)
				}
			}

			if !parsed.Get("properties.discriminator").Exists() {
				t.Errorf("[%s] legitimate property \"discriminator\" was removed: %s", name, got)
			}
			if !parsed.Get("properties.nestedDiscriminatorProp.properties.discriminator.properties.kind").Exists() {
				t.Errorf("[%s] legitimate nested property \"discriminator\" was removed: %s", name, got)
			}
		})
	}
}

// TestCleanJSONSchema_RemovesContentMediaTypeKeyword_Issue6699 covers Issue #6699:
// JSON Schema Content Vocabulary annotations ("contentMediaType" and "contentEncoding")
// inside nested anyOf/oneOf branches, on property schemas, or at the schema root are
// rejected by the Gemini/Antigravity backend with 400 INVALID_ARGUMENT
// ("Unknown name \"contentMediaType\" ... Cannot find field") and must be stripped
// while preserving the anyOf branches and any legitimate property named
// "contentMediaType" or "contentEncoding".
func TestCleanJSONSchema_RemovesContentMediaTypeKeyword_Issue6699(t *testing.T) {
	input := `{
		"type": "object",
		"contentMediaType": "application/json",
		"contentEncoding": "utf-8",
		"properties": {
			"value": {
				"anyOf": [
					{ "type": "object", "properties": {} },
					{ "type": "string", "contentMediaType": "application/json", "contentEncoding": "base64" }
				]
			},
			"directPayload": {
				"type": "string",
				"contentMediaType": "application/json",
				"contentEncoding": "base64"
			},
			"contentMediaType": {
				"type": "string",
				"contentMediaType": "text/plain",
				"description": "Legitimate property named contentMediaType"
			},
			"contentEncoding": {
				"type": "string",
				"contentEncoding": "base64",
				"description": "Legitimate property named contentEncoding"
			}
		}
	}`

	cleaners := map[string]func(string) string{
		"Antigravity":           CleanJSONSchemaForAntigravity,
		"AntigravityTool":       func(s string) string { return CleanJSONSchemaForAntigravityTool(s, false) },
		"AntigravityClaudeTool": func(s string) string { return CleanJSONSchemaForAntigravityClaudeTool(s, false) },
		"AntigravityResponse":   CleanJSONSchemaForAntigravityResponse,
		"Gemini":                CleanJSONSchemaForGemini,
		"GeminiJSONSchema":      CleanJSONSchemaForGeminiJSONSchema,
	}

	for name, clean := range cleaners {
		t.Run(name, func(t *testing.T) {
			got := clean(input)
			parsed := gjson.Parse(got)

			for _, forbiddenPath := range []string{
				"contentMediaType",
				"contentEncoding",
				"properties.value.anyOf.1.contentMediaType",
				"properties.value.anyOf.1.contentEncoding",
				"properties.directPayload.contentMediaType",
				"properties.directPayload.contentEncoding",
				"properties.contentMediaType.contentMediaType",
				"properties.contentEncoding.contentEncoding",
			} {
				if parsed.Get(forbiddenPath).Exists() {
					t.Errorf("[%s] %s was not removed: %s", name, forbiddenPath, got)
				}
			}

			if name != "AntigravityClaudeTool" {
				if gotBranches := len(parsed.Get("properties.value.anyOf").Array()); gotBranches != 2 {
					t.Errorf("[%s] properties.value.anyOf branch count = %d, want 2: %s", name, gotBranches, got)
				}
				if gotType := parsed.Get("properties.value.anyOf.1.type").String(); gotType != "string" {
					t.Errorf("[%s] properties.value.anyOf.1.type = %q, want \"string\": %s", name, gotType, got)
				}
			}

			if !parsed.Get("properties.contentMediaType").Exists() {
				t.Errorf("[%s] legitimate property \"contentMediaType\" was removed: %s", name, got)
			}
			if !parsed.Get("properties.contentEncoding").Exists() {
				t.Errorf("[%s] legitimate property \"contentEncoding\" was removed: %s", name, got)
			}
		})
	}
}

// TestIssue6732_GeminiCleanersInlineLocalDefs verifies that the Gemini cleaners expand local
// $defs references before the definition container is stripped, so nested fields survive.
func TestIssue6732_GeminiCleanersInlineLocalDefs(t *testing.T) {
	input := `{"type":"object","properties":{"person":{"$ref":"#/$defs/Person"}},"required":["person"],"$defs":{"Person":{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}}}`

	for name, clean := range map[string]func(string) string{
		"gemini":           CleanJSONSchemaForGemini,
		"geminiJSONSchema": CleanJSONSchemaForGeminiJSONSchema,
	} {
		got := clean(input)
		parsed := gjson.Parse(got)
		if gotType := parsed.Get("properties.person.properties.name.type").String(); gotType != "string" {
			t.Errorf("%s: inlined properties.person.properties.name.type = %q, want %q: %s", name, gotType, "string", got)
		}
		if parsed.Get("$defs").Exists() {
			t.Errorf("%s: $defs should be removed after inlining: %s", name, got)
		}
		if strings.Contains(got, "See: Person") {
			t.Errorf("%s: resolvable local ref degraded to a hint: %s", name, got)
		}
	}
}

// TestIssue6732_GeminiCleanersInlinePropertyReuse verifies that a Zod-style #/properties/...
// reference is expanded by the Gemini cleaners instead of becoming a description hint.
func TestIssue6732_GeminiCleanersInlinePropertyReuse(t *testing.T) {
	input := `{"type":"object","properties":{"billing":{"type":"object","properties":{"zip":{"type":"string"}}},"shipping":{"$ref":"#/properties/billing"}}}`

	for name, clean := range map[string]func(string) string{
		"gemini":           CleanJSONSchemaForGemini,
		"geminiJSONSchema": CleanJSONSchemaForGeminiJSONSchema,
	} {
		got := clean(input)
		parsed := gjson.Parse(got)
		if gotType := parsed.Get("properties.shipping.properties.zip.type").String(); gotType != "string" {
			t.Errorf("%s: properties.shipping.properties.zip.type = %q, want %q: %s", name, gotType, "string", got)
		}
	}
}

// TestIssue6732_GeminiCleanersReuseSameDefinitionTwice verifies that two properties referencing
// the same definition both keep the referenced fields; bounding expansion must not degrade
// ordinary reuse of a shared definition to a hint.
func TestIssue6732_GeminiCleanersReuseSameDefinitionTwice(t *testing.T) {
	input := `{"type":"object","properties":{"billing":{"$ref":"#/$defs/Address"},"shipping":{"$ref":"#/$defs/Address"}},"$defs":{"Address":{"type":"object","properties":{"zip":{"type":"string"}}}}}`

	for name, clean := range map[string]func(string) string{
		"gemini":           CleanJSONSchemaForGemini,
		"geminiJSONSchema": CleanJSONSchemaForGeminiJSONSchema,
	} {
		got := clean(input)
		parsed := gjson.Parse(got)
		for _, field := range []string{"billing", "shipping"} {
			if gotType := parsed.Get("properties." + field + ".properties.zip.type").String(); gotType != "string" {
				t.Errorf("%s: properties.%s.properties.zip.type = %q, want %q: %s", name, field, gotType, "string", got)
			}
		}
	}
}

// TestIssue6732_WrappedSchemaResolvesAgainstInnerRoot verifies that a schema wrapped in the
// single-key {"schema": ...} carrier used by the Antigravity executor resolves local refs
// against the inner schema rather than the carrier.
func TestIssue6732_WrappedSchemaResolvesAgainstInnerRoot(t *testing.T) {
	inner := `{"type":"object","properties":{"person":{"$ref":"#/$defs/Person"}},"required":["person"],"$defs":{"Person":{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}}}`
	wrapped := `{"schema":` + inner + `}`

	got := CleanJSONSchemaForAntigravityTool(wrapped, false)
	parsed := gjson.Parse(got)
	if gotType := parsed.Get("schema.properties.person.properties.name.type").String(); gotType != "string" {
		t.Errorf("wrapped schema.properties.person.properties.name.type = %q, want %q: %s", gotType, "string", got)
	}
	if parsed.Get("schema.$defs").Exists() {
		t.Errorf("wrapped $defs should be removed after inlining: %s", got)
	}
	if strings.Contains(got, "See: Person") {
		t.Errorf("wrapped resolvable local ref degraded to a hint: %s", got)
	}
}

// TestIssue6732_DefaultLiteralPreservedOnJSONSchemaRoute verifies that the parametersJsonSchema
// route forwards default values verbatim even when they contain schema-looking keys.
func TestIssue6732_DefaultLiteralPreservedOnJSONSchemaRoute(t *testing.T) {
	input := `{"type":"object","properties":{"report":{"type":"object","default":{"title":"Report","format":"pdf","id":7,"x-owner":"ops"}}}}`

	got := CleanJSONSchemaForGeminiJSONSchema(input)
	def := gjson.Parse(got).Get("properties.report.default")
	if !def.Exists() {
		t.Fatalf("default literal missing: %s", got)
	}
	if def.Get("title").String() != "Report" || def.Get("format").String() != "pdf" || def.Get("id").Int() != 7 || def.Get("x-owner").String() != "ops" {
		t.Errorf("default literal was rewritten, got %s: %s", def.Raw, got)
	}
}

// TestIssue6732_ExampleLiteralsPreserved verifies that example/examples values keep
// schema-looking keys on the parametersJsonSchema route.
func TestIssue6732_ExampleLiteralsPreserved(t *testing.T) {
	input := `{"type":"object","properties":{"a":{"type":"string","example":{"title":"Hi","x-owner":"ops","id":3},"examples":[{"title":"Hi2","x-owner":"ops","id":4}]}}}`

	got := CleanJSONSchemaForGeminiJSONSchema(input)
	parsed := gjson.Parse(got)
	example := parsed.Get("properties.a.example")
	if example.Get("title").String() != "Hi" || example.Get("x-owner").String() != "ops" || example.Get("id").Int() != 3 {
		t.Errorf("example literal was rewritten, got %s: %s", example.Raw, got)
	}
	exampleItem := parsed.Get("properties.a.examples.0")
	if exampleItem.Get("title").String() != "Hi2" || exampleItem.Get("x-owner").String() != "ops" || exampleItem.Get("id").Int() != 4 {
		t.Errorf("examples literal was rewritten, got %s: %s", exampleItem.Raw, got)
	}
}

// TestIssue6732_InlineLocalRefsLeavesLiteralRefs verifies that the inliner does not resolve
// {"$ref": ...} objects that appear as client data inside a default value.
func TestIssue6732_InlineLocalRefsLeavesLiteralRefs(t *testing.T) {
	input := `{"type":"object","properties":{"a":{"type":"object","default":{"$ref":"#/$defs/Person"}}},"$defs":{"Person":{"type":"object","properties":{"name":{"type":"string"}}}}}`

	got := InlineLocalRefs(input)
	parsed := gjson.Parse(got)
	if ref := parsed.Get("properties.a.default.$ref").String(); ref != "#/$defs/Person" {
		t.Errorf("literal $ref inside default = %q, want it preserved verbatim: %s", ref, got)
	}
	if parsed.Get("properties.a.default.properties").Exists() {
		t.Errorf("literal $ref inside default was expanded: %s", got)
	}
	if !parsed.Get("$defs.Person.properties.name").Exists() {
		t.Errorf("InlineLocalRefs must not strip definitions: %s", got)
	}
}

// TestIssue6732_PropertiesNamedDefaultAndTypeRemainSchemas verifies that properties literally
// named "default" and "type" keep their schema semantics: their own keywords are still cleaned
// and their own $ref is still inlined.
func TestIssue6732_PropertiesNamedDefaultAndTypeRemainSchemas(t *testing.T) {
	input := `{"type":"object","properties":{"default":{"$ref":"#/$defs/Person"},"type":{"type":"string","title":"Kind"}},"$defs":{"Person":{"type":"object","properties":{"name":{"type":"string"}}}}}`

	got := CleanJSONSchemaForGeminiJSONSchema(input)
	parsed := gjson.Parse(got)
	if gotType := parsed.Get("properties.default.properties.name.type").String(); gotType != "string" {
		t.Errorf("property named \"default\" ref was not inlined, name type = %q: %s", gotType, got)
	}
	if parsed.Get("properties.type.title").Exists() {
		t.Errorf("property named \"type\" schema title should still be cleaned: %s", got)
	}
}

// TestIssue6732_ExpansionBudgetBoundsSharedDefs verifies that inlining a schema whose shared
// definitions fan out exponentially stays bounded instead of expanding without limit, while the
// affordable outer levels are still expanded.
func TestIssue6732_ExpansionBudgetBoundsSharedDefs(t *testing.T) {
	input := geminiSchemaDiamondInput()
	got := CleanJSONSchemaForGeminiJSONSchema(input)
	if len(got) >= 512*1024 {
		t.Errorf("diamond schema output = %d bytes, want < %d (input %d bytes)", len(got), 512*1024, len(input))
	}
	parsed := gjson.Parse(got)
	if !parsed.Get("properties.leaf").Exists() {
		t.Errorf("diamond schema lost the top-level property: %s", got)
	}
	if !parsed.Get("properties.leaf.properties.a.properties.a").Exists() {
		t.Errorf("diamond schema outer levels should still be expanded: %s", got)
	}
	if parsed.Get("$defs").Exists() {
		t.Errorf("diamond schema should not retain $defs: %s", got)
	}
}

// TestIssue6732_UnusedDefinitionsDoNotConsumeExpansionBudget verifies that definitions the
// cleaners are about to delete anyway neither consume the expansion budget nor make the kept
// fields depend on map iteration order.
func TestIssue6732_UnusedDefinitionsDoNotConsumeExpansionBudget(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{"type":"object","properties":{"person":{"$ref":"#/$defs/Person"}},"required":["person"],"$defs":{"leaf0":{"type":"string","description":"leaf leaf leaf leaf leaf leaf leaf leaf leaf leaf"},"Person":{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`)
	for i := range 13 {
		target := "leaf0"
		if i > 0 {
			target = "D" + strconv.Itoa(i-1)
		}
		sb.WriteString(`,"D` + strconv.Itoa(i) + `":{"type":"object","properties":{"a":{"$ref":"#/$defs/` + target + `"},"b":{"$ref":"#/$defs/` + target + `"}}}`)
	}
	sb.WriteString(`}}`)
	input := sb.String()

	first := CleanJSONSchemaForGeminiJSONSchema(input)
	for run := range 30 {
		got := CleanJSONSchemaForGeminiJSONSchema(input)
		if got != first {
			t.Fatalf("run %d: output is not deterministic:\nfirst: %s\ngot:   %s", run, first, got)
		}
		parsed := gjson.Parse(got)
		if gotType := parsed.Get("properties.person.properties.name.type").String(); gotType != "string" {
			t.Fatalf("run %d: used definition was not inlined, name type = %q: %s", run, gotType, got)
		}
		if parsed.Get("$defs").Exists() {
			t.Fatalf("run %d: $defs should be removed: %s", run, got)
		}
	}
}

// TestIssue6732_BudgetFallbackDoesNotCopyLargeDescriptions verifies that references degraded by
// the expansion budget do not each copy a large target description, which would defeat the bound.
func TestIssue6732_BudgetFallbackDoesNotCopyLargeDescriptions(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{"type":"object","properties":{`)
	for i := range 200 {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(`"p` + strconv.Itoa(i) + `":{"$ref":"#/$defs/Big"}`)
	}
	sb.WriteString(`},"$defs":{"Big":{"type":"object","description":"` + strings.Repeat("x", 32*1024) + `","properties":{"name":{"type":"string"}}}}}`)

	got := CleanJSONSchemaForGeminiJSONSchema(sb.String())
	if len(got) >= 512*1024 {
		t.Errorf("shared large-description output = %d bytes, want < %d", len(got), 512*1024)
	}
	parsed := gjson.Parse(got)
	if !parsed.Get("properties.p0").Exists() || !parsed.Get("properties.p199").Exists() {
		t.Errorf("shared large-description schema lost properties: %s", got)
	}
	if gotType := parsed.Get("properties.p0.properties.name.type").String(); gotType != "string" {
		t.Errorf("first affordable expansion should keep its fields, name type = %q: %s", gotType, got)
	}
	if parsed.Get("$defs").Exists() {
		t.Errorf("$defs should be removed: %s", got)
	}
}

// TestIssue6732_NestedUnusedDefinitionsGraphDoesNotConsumeBudget verifies that a self-contained
// reference graph inside a nested definitions container (which cleaning deletes at every level)
// neither spends the expansion budget nor starves kept fields.
func TestIssue6732_NestedUnusedDefinitionsGraphDoesNotConsumeBudget(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{"type":"object","properties":{"person":{"$ref":"#/$defs/Person"},"a":{"type":"object","$defs":{"Leaf":{"type":"string","description":"` + strings.Repeat("x", 1024) + `"}`)
	for i := range 256 {
		sb.WriteString(`,"U` + strconv.Itoa(i) + `":{"$ref":"#/properties/a/$defs/Leaf"}`)
	}
	sb.WriteString(`}}},"required":["person"],"$defs":{"Person":{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}}}`)

	got := CleanJSONSchemaForGeminiJSONSchema(sb.String())
	parsed := gjson.Parse(got)
	if gotType := parsed.Get("properties.person.properties.name.type").String(); gotType != "string" {
		t.Errorf("nested unused definitions starved the kept field, name type = %q: %s", gotType, got)
	}
	if parsed.Get("properties.a.$defs").Exists() || parsed.Get("$defs").Exists() {
		t.Errorf("nested and root $defs should be removed: %s", got)
	}
}

// TestIssue6732_RefSiblingNameMapReferencesStillExpand verifies that references inside a $ref
// node's own name-map siblings expand when the reference target itself expands.
func TestIssue6732_RefSiblingNameMapReferencesStillExpand(t *testing.T) {
	input := `{"type":"object","properties":{"a":{"$ref":"#/$defs/Base","properties":{"x":{"$ref":"#/$defs/S"}}}},"$defs":{"Base":{"type":"object"},"S":{"type":"string"}}}`

	got := CleanJSONSchemaForGeminiJSONSchema(input)
	parsed := gjson.Parse(got)
	if gotType := parsed.Get("properties.a.properties.x.type").String(); gotType != "string" {
		t.Errorf("ref sibling name-map reference lost its type, got %q: %s", gotType, got)
	}
}

// TestIssue6732_CleanInliningStripsDefinitionsFromIntermediate verifies that the cleaning
// inliner's intermediate output does not serialize definition containers (root or carried inside
// expanded targets), so repeated references to a target holding a huge unused definitions block
// cannot multiply that block in the intermediate JSON.
func TestIssue6732_CleanInliningStripsDefinitionsFromIntermediate(t *testing.T) {
	person := `{"type":"object","properties":{"name":{"type":"string"}},"$defs":{"Unused":{"type":"string","description":"` + strings.Repeat("x", 280*1024) + `"}}}`
	input := `{"type":"object","properties":{"p0":{"$ref":"#/$defs/Person"},"p1":{"$ref":"#/$defs/Person"}},"$defs":{"Person":` + person + `}}`

	got := inlineLocalRefsForClean(input)
	if len(got) >= 64*1024 {
		t.Errorf("clean inlining intermediate = %d bytes, want < %d", len(got), 64*1024)
	}
	parsed := gjson.Parse(got)
	if parsed.Get("$defs").Exists() {
		t.Errorf("intermediate should not carry root $defs: %s", got)
	}
	if parsed.Get("properties.p0.$defs").Exists() {
		t.Errorf("intermediate should not carry target-nested $defs: %s", got)
	}
	if gotType := parsed.Get("properties.p1.properties.name.type").String(); gotType != "string" {
		t.Errorf("second expansion lost its fields, name type = %q: %s", gotType, got)
	}
}

// TestIssue6732_ArrayRefWithExplicitTypeKeepsTargetItems verifies that the placeholder items
// Phase 0 adds to a typed but unresolved $ref node does not clobber the items of the referenced
// definition during inlining.
func TestIssue6732_ArrayRefWithExplicitTypeKeepsTargetItems(t *testing.T) {
	input := `{"type":"object","properties":{"rows":{"type":"array","$ref":"#/$defs/Rows"}},"$defs":{"Rows":{"type":"array","items":{"type":"object","properties":{"name":{"type":"string"}}}}}}`

	got := CleanJSONSchemaForGeminiJSONSchema(input)
	parsed := gjson.Parse(got)
	if gotType := parsed.Get("properties.rows.items.properties.name.type").String(); gotType != "string" {
		t.Errorf("array ref target items were clobbered, name type = %q: %s", gotType, got)
	}
}

// TestIssue6732_BudgetFallbackStillResolvesSiblings verifies that when a reference target is too
// large to expand, sibling subschemas on the same node are still resolved instead of being copied
// verbatim with their own references left dangling.
func TestIssue6732_BudgetFallbackStillResolvesSiblings(t *testing.T) {
	input := `{"type":"object","properties":{"a":{"$ref":"#/$defs/Big","properties":{"x":{"$ref":"#/$defs/S"}}}},"$defs":{"Big":{"type":"object","description":"` + strings.Repeat("x", 280*1024) + `"},"S":{"type":"string"}}}`

	got := CleanJSONSchemaForGeminiJSONSchema(input)
	parsed := gjson.Parse(got)
	if gotType := parsed.Get("properties.a.properties.x.type").String(); gotType != "string" {
		t.Errorf("sibling reference after budget fallback lost its type, got %q: %s", gotType, got)
	}
	if strings.Contains(got, `"$ref"`) {
		t.Errorf("cleaned schema left a dangling reference: %s", got)
	}
}

// TestIssue6732_InlineLocalRefsDefinitionNamesKeepSchemaSemantics verifies that definitions whose
// names look like schema keywords are treated as schemas by the public inliner: a definition named
// "properties" keeps its literal default intact, and a definition named "default" still expands.
func TestIssue6732_InlineLocalRefsDefinitionNamesKeepSchemaSemantics(t *testing.T) {
	input := `{"$defs":{"S":{"type":"string"},"properties":{"type":"object","default":{"$ref":"#/$defs/S"}},"default":{"type":"object","properties":{"x":{"$ref":"#/$defs/S"}}}}}`

	got := InlineLocalRefs(input)
	parsed := gjson.Parse(got)
	if ref := parsed.Get("$defs.properties.default.$ref").String(); ref != "#/$defs/S" {
		t.Errorf("literal default inside definition named \"properties\" was expanded, $ref = %q: %s", ref, got)
	}
	if gotType := parsed.Get("$defs.default.properties.x.type").String(); gotType != "string" {
		t.Errorf("definition named \"default\" was not resolved as a schema, x type = %q: %s", gotType, got)
	}
}

// TestIssue6732_TargetNestedDefinitionsExemptFromBudget verifies that unused definitions nested
// inside a referenced target (which cleaning deletes) do not inflate the target's budget charge.
func TestIssue6732_TargetNestedDefinitionsExemptFromBudget(t *testing.T) {
	person := `{"type":"object","properties":{"name":{"type":"string"}},"$defs":{"Unused":{"type":"string","description":"` + strings.Repeat("x", 280*1024) + `"}}}`
	cases := map[string]string{
		"propertyRef": `{"type":"object","properties":{"person":{"$ref":"#/$defs/Person"}},"$defs":{"Person":` + person + `}}`,
		"rootRef":     `{"$ref":"#/$defs/Person","$defs":{"Person":` + person + `}}`,
	}
	for caseName, input := range cases {
		got := CleanJSONSchemaForGeminiJSONSchema(input)
		parsed := gjson.Parse(got)
		namePath := "properties.person.properties.name.type"
		if caseName == "rootRef" {
			namePath = "properties.name.type"
		}
		if gotType := parsed.Get(namePath).String(); gotType != "string" {
			t.Errorf("%s: %s = %q, want %q: %s", caseName, namePath, gotType, "string", got)
		}
		if parsed.Get("$defs").Exists() {
			t.Errorf("%s: $defs should be removed: %s", caseName, got)
		}
	}
}

// TestIssue6732_ArrayRefWithoutTargetItemsGetsPlaceholder verifies that a typed array whose
// referenced definition carries no items still ends up with the placeholder items Phase 0 would
// have added, and that cleaning twice is stable.
func TestIssue6732_ArrayRefWithoutTargetItemsGetsPlaceholder(t *testing.T) {
	input := `{"type":"object","properties":{"rows":{"type":"array","$ref":"#/$defs/Common"}},"$defs":{"Common":{"description":"Rows"}}}`

	first := CleanJSONSchemaForAntigravityTool(input, false)
	parsed := gjson.Parse(first)
	if gotType := parsed.Get("properties.rows.items.type").String(); gotType != "string" {
		t.Errorf("array ref without target items missing placeholder items, got %q: %s", gotType, first)
	}
	if second := CleanJSONSchemaForAntigravityTool(first, false); second != first {
		t.Errorf("cleaning is not idempotent for array refs:\nfirst:  %s\nsecond: %s", first, second)
	}
}

// TestIssue6732_SmallTargetFanOutStaysBounded verifies that thousands of references to a small
// target with a near-limit description cannot multiply the description without bound through a
// mix of expansions and fallback copies.
func TestIssue6732_SmallTargetFanOutStaysBounded(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{"type":"object","properties":{`)
	for i := range 2000 {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(`"p` + strconv.Itoa(i) + `":{"$ref":"#/$defs/S"}`)
	}
	sb.WriteString(`},"$defs":{"S":{"type":"string","description":"` + strings.Repeat("x", 512) + `"}}}`)

	got := CleanJSONSchemaForGeminiJSONSchema(sb.String())
	if len(got) >= 512*1024 {
		t.Errorf("small-target fan-out output = %d bytes, want < %d", len(got), 512*1024)
	}
}

// TestIssue6732_EscapedDescriptionFanOutStaysBounded verifies that budget accounting covers
// HTML-escaped string growth during JSON encoding.
func TestIssue6732_EscapedDescriptionFanOutStaysBounded(t *testing.T) {
	input := `{"type":"object","properties":{"p0":{"$ref":"#/$defs/Big"},"p1":{"$ref":"#/$defs/Big"},"p2":{"$ref":"#/$defs/Big"},"p3":{"$ref":"#/$defs/Big"},"p4":{"$ref":"#/$defs/Big"},"p5":{"$ref":"#/$defs/Big"},"p6":{"$ref":"#/$defs/Big"},"p7":{"$ref":"#/$defs/Big"}},"$defs":{"Big":{"type":"object","description":"` + strings.Repeat("<", 32*1024) + `","properties":{"name":{"type":"string"}}}}}`

	got := CleanJSONSchemaForGeminiJSONSchema(input)
	if len(got) >= 512*1024 {
		t.Errorf("escaped-description fan-out output = %d bytes, want < %d", len(got), 512*1024)
	}
}

// TestIssue6732_DependenciesNameMapKeepsSchemaSemantics verifies that the Draft-07 dependencies
// container behaves as a name map: a dependency keyed "default" holds a schema whose references
// still expand in the public inliner.
func TestIssue6732_DependenciesNameMapKeepsSchemaSemantics(t *testing.T) {
	input := `{"type":"object","dependencies":{"default":{"properties":{"x":{"$ref":"#/definitions/S"}}}},"definitions":{"S":{"type":"string"}}}`

	got := InlineLocalRefs(input)
	parsed := gjson.Parse(got)
	if gotType := parsed.Get("dependencies.default.properties.x.type").String(); gotType != "string" {
		t.Errorf("dependency schema named \"default\" was not resolved, x type = %q: %s", gotType, got)
	}
	if parsed.Get("dependencies.default.properties.x.$ref").Exists() {
		t.Errorf("dependency schema reference was left dangling: %s", got)
	}
}

// TestIssue6732_OmittedFallbackDescriptionNotCharged verifies that a target description the
// fallback omits (because it exceeds the copy limit) is not charged to the expansion budget, so
// a following small reference still expands.
func TestIssue6732_OmittedFallbackDescriptionNotCharged(t *testing.T) {
	input := `{"type":"object","properties":{"a":{"$ref":"#/$defs/A"},"b":{"$ref":"#/$defs/B"}},"$defs":{"A":{"type":"string","description":"` + strings.Repeat("x", 262142) + `"},"B":{"type":"object","properties":{"name":{"type":"string"}}}}}`

	got := CleanJSONSchemaForGeminiJSONSchema(input)
	parsed := gjson.Parse(got)
	if gotType := parsed.Get("properties.b.properties.name.type").String(); gotType != "string" {
		t.Errorf("small reference after omitted fallback description lost its fields, name type = %q: %s", gotType, got)
	}
}

// TestIssue6732_SynthesizedTypeDoesNotOverrideRefTarget verifies that the type Phase 0 would
// infer from a ref node's sibling items does not clobber the referenced definition's object
// type, and that cleaning the result again is stable.
func TestIssue6732_SynthesizedTypeDoesNotOverrideRefTarget(t *testing.T) {
	input := `{"$ref":"#/$defs/O","items":{"type":"integer"},"$defs":{"O":{"type":"object","properties":{"x":{"type":"string"}}}}}`

	first := CleanJSONSchemaForGeminiJSONSchema(input)
	parsed := gjson.Parse(first)
	if gotType := parsed.Get("type").String(); gotType != "object" {
		t.Errorf("root ref target type = %q, want %q: %s", gotType, "object", first)
	}
	if parsed.Get("items").Exists() {
		t.Errorf("object schema should not carry items: %s", first)
	}
	if gotType := parsed.Get("properties.x.type").String(); gotType != "string" {
		t.Errorf("target property lost its type, got %q: %s", gotType, first)
	}
	if second := CleanJSONSchemaForGeminiJSONSchema(first); second != first {
		t.Errorf("cleaning is not idempotent:\nfirst:  %s\nsecond: %s", first, second)
	}
}

// TestIssue6732_PropertyNamedRefRemainsProperty verifies that a property literally named "$ref"
// is treated as a property, not as a reference keyword, by the cleaner.
func TestIssue6732_PropertyNamedRefRemainsProperty(t *testing.T) {
	input := `{"type":"object","properties":{"$ref":{"type":"string"}}}`

	got := CleanJSONSchemaForGeminiJSONSchema(input)
	parsed := gjson.Parse(got)
	if gotType := parsed.Get(`properties.\$ref.type`).String(); gotType != "string" {
		t.Errorf("property named $ref lost its schema, type = %q: %s", gotType, got)
	}
	if strings.Contains(got, "See:") {
		t.Errorf("property named $ref was converted into a hint: %s", got)
	}
}

// TestIssue6732_PropertyNamedConstRemainsProperty verifies that a property literally named
// "const" holding a schema object is not converted into an enum constraint on its parent.
func TestIssue6732_PropertyNamedConstRemainsProperty(t *testing.T) {
	input := `{"type":"object","properties":{"const":{"type":"string"}}}`

	got := CleanJSONSchemaForGeminiJSONSchema(input)
	parsed := gjson.Parse(got)
	if gotType := parsed.Get(`properties.const.type`).String(); gotType != "string" {
		t.Errorf("property named const lost its schema, type = %q: %s", gotType, got)
	}
	if parsed.Get("enum").Exists() {
		t.Errorf("property named const fabricates a root enum: %s", got)
	}
}

// geminiSchemaDiamondInput builds a schema referencing D12, where each Di is an object with two
// properties referencing Di-1 and D0 references a leaf definition twice. Fully expanding it
// produces 2^13 leaf copies.
func geminiSchemaDiamondInput() string {
	var sb strings.Builder
	sb.WriteString(`{"type":"object","properties":{"leaf":{"$ref":"#/$defs/D12"}},"$defs":{"leaf0":{"type":"string","description":"leaf leaf leaf leaf leaf leaf leaf leaf leaf leaf"}`)
	for i := range 13 {
		target := "leaf0"
		if i > 0 {
			target = "D" + strconv.Itoa(i-1)
		}
		sb.WriteString(`,"D` + strconv.Itoa(i) + `":{"type":"object","properties":{"a":{"$ref":"#/$defs/` + target + `"},"b":{"$ref":"#/$defs/` + target + `"}}}`)
	}
	sb.WriteString(`}}`)
	return sb.String()
}

// TestCleanJSONSchema_RemovesReadOnlyWriteOnlyKeywords_Issue6778 covers Issue #6778:
// JSON Schema annotation keywords "readOnly" and "writeOnly" (common in Google Discovery-derived
// MCP tools such as Firestore) are rejected by the Gemini/Antigravity backend with 400
// INVALID_ARGUMENT ("Unknown name \"readOnly\" ... Cannot find field") and must be stripped
// while preserving legitimate tool properties named "readOnly" or "writeOnly".
func TestCleanJSONSchema_RemovesReadOnlyWriteOnlyKeywords_Issue6778(t *testing.T) {
	input := `{
		"type": "object",
		"$schema": "http://json-schema.org/draft-04/schema#",
		"readOnly": false,
		"writeOnly": false,
		"properties": {
			"parent": { "type": "string", "description": "Parent resource name" },
			"database": {
				"type": "object",
				"properties": {
					"name":       { "type": "string", "description": "Database resource name", "readOnly": true },
					"uid":        { "type": "string", "readOnly": true },
					"createTime": { "type": "string", "readOnly": true },
					"etag":       { "type": "string", "writeOnly": true },
					"locationId": { "type": "string" }
				}
			},
			"itemsList": {
				"type": "array",
				"items": {
					"type": "string",
					"readOnly": true,
					"writeOnly": false
				}
			},
			"unionField": {
				"anyOf": [
					{ "type": "string", "readOnly": true },
					{ "type": "integer", "writeOnly": true }
				]
			},
			"readOnly": {
				"type": "boolean",
				"readOnly": true,
				"description": "Legitimate property named readOnly"
			},
			"writeOnly": {
				"type": "boolean",
				"writeOnly": true,
				"description": "Legitimate property named writeOnly"
			}
		},
		"required": ["parent"]
	}`

	cleaners := map[string]func(string) string{
		"Antigravity":           CleanJSONSchemaForAntigravity,
		"AntigravityTool":       func(s string) string { return CleanJSONSchemaForAntigravityTool(s, false) },
		"AntigravityClaudeTool": func(s string) string { return CleanJSONSchemaForAntigravityClaudeTool(s, false) },
		"AntigravityResponse":   CleanJSONSchemaForAntigravityResponse,
		"Gemini":                CleanJSONSchemaForGemini,
		"GeminiJSONSchema":      CleanJSONSchemaForGeminiJSONSchema,
	}

	for name, clean := range cleaners {
		t.Run(name, func(t *testing.T) {
			got := clean(input)
			parsed := gjson.Parse(got)

			for _, forbiddenPath := range []string{
				"readOnly",
				"writeOnly",
				"properties.database.properties.name.readOnly",
				"properties.database.properties.uid.readOnly",
				"properties.database.properties.createTime.readOnly",
				"properties.database.properties.etag.writeOnly",
				"properties.itemsList.items.readOnly",
				"properties.itemsList.items.writeOnly",
				"properties.unionField.readOnly",
				"properties.unionField.writeOnly",
				"properties.unionField.anyOf.0.readOnly",
				"properties.unionField.anyOf.1.writeOnly",
				"properties.readOnly.readOnly",
				"properties.writeOnly.writeOnly",
			} {
				if parsed.Get(forbiddenPath).Exists() {
					t.Errorf("[%s] %s was not removed: %s", name, forbiddenPath, got)
				}
			}

			for _, keptField := range []string{
				"name",
				"uid",
				"createTime",
				"etag",
				"locationId",
			} {
				if gotType := parsed.Get("properties.database.properties." + keptField + ".type").String(); gotType != "string" {
					t.Errorf("[%s] properties.database.properties.%s.type = %q, want \"string\": %s", name, keptField, gotType, got)
				}
			}
			if gotDesc := parsed.Get("properties.database.properties.name.description").String(); gotDesc != "Database resource name" {
				t.Errorf("[%s] properties.database.properties.name.description = %q, want %q: %s", name, gotDesc, "Database resource name", got)
			}

			if gotType := parsed.Get("properties.readOnly.type").String(); gotType != "boolean" {
				t.Errorf("[%s] legitimate property \"readOnly\" lost its type (%q): %s", name, gotType, got)
			}
			if gotDesc := parsed.Get("properties.readOnly.description").String(); gotDesc != "Legitimate property named readOnly" {
				t.Errorf("[%s] legitimate property \"readOnly\" lost its description (%q): %s", name, gotDesc, got)
			}
			if gotType := parsed.Get("properties.writeOnly.type").String(); gotType != "boolean" {
				t.Errorf("[%s] legitimate property \"writeOnly\" lost its type (%q): %s", name, gotType, got)
			}
			if gotDesc := parsed.Get("properties.writeOnly.description").String(); gotDesc != "Legitimate property named writeOnly" {
				t.Errorf("[%s] legitimate property \"writeOnly\" lost its description (%q): %s", name, gotDesc, got)
			}
		})
	}
}

// TestIssue6950_ArrayItemsFromAllOfPreserved verifies that array schemas whose items come from
// allOf branches (directly, through a $ref, or alongside a base array $ref/branch) retain their
// real items schema instead of having it replaced by Phase 0's {"type":"string"} placeholder.
func TestIssue6950_ArrayItemsFromAllOfPreserved(t *testing.T) {
	cleaners := map[string]struct {
		clean             func(string) string
		preservesNullable bool
	}{
		"Antigravity": {
			clean:             CleanJSONSchemaForAntigravity,
			preservesNullable: true,
		},
		"AntigravityTool": {
			clean:             func(s string) string { return CleanJSONSchemaForAntigravityTool(s, false) },
			preservesNullable: true,
		},
		"AntigravityClaudeTool": {
			clean:             func(s string) string { return CleanJSONSchemaForAntigravityClaudeTool(s, false) },
			preservesNullable: true,
		},
		"Gemini": {
			clean:             CleanJSONSchemaForGemini,
			preservesNullable: false,
		},
	}

	cases := []struct {
		name         string
		input        string
		path         string
		wantItemType string
		wantDesc     string
		wantNullable bool
	}{
		{
			name:         "direct_allof_items",
			input:        `{"type":"object","properties":{"ids":{"type":"array","allOf":[{"items":{"type":"integer"}}]}}}`,
			path:         "properties.ids",
			wantItemType: "integer",
		},
		{
			name:         "ref_in_allof_items",
			input:        `{"type":"object","properties":{"ids":{"type":"array","allOf":[{"$ref":"#/$defs/Ids"}]}},"$defs":{"Ids":{"type":"array","items":{"type":"integer"}}}}`,
			path:         "properties.ids",
			wantItemType: "integer",
		},
		{
			name:         "nullable_type_array_with_ref_in_allof",
			input:        `{"type":"object","properties":{"ids":{"type":["array","null"],"allOf":[{"$ref":"#/$defs/Ids"}]}},"$defs":{"Ids":{"type":"array","items":{"type":"integer"}}}}`,
			path:         "properties.ids",
			wantItemType: "integer",
			wantDesc:     "(nullable)",
			wantNullable: true,
		},
		{
			name:         "split_allof_branches",
			input:        `{"type":"object","properties":{"ids":{"allOf":[{"type":"array","description":"ID list"},{"items":{"type":"integer"}}]}}}`,
			path:         "properties.ids",
			wantItemType: "integer",
			wantDesc:     "ID list",
		},
		{
			name:         "ref_base_array_with_allof_items",
			input:        `{"type":"object","properties":{"ids":{"$ref":"#/$defs/BaseArray","allOf":[{"items":{"type":"integer"}}]}},"$defs":{"BaseArray":{"type":"array","description":"ID list"}}}`,
			path:         "properties.ids",
			wantItemType: "integer",
			wantDesc:     "ID list",
		},
		{
			name:         "root_array_with_allof_items",
			input:        `{"type":"array","allOf":[{"items":{"type":"integer"}}]}`,
			path:         "",
			wantItemType: "integer",
		},
		{
			name:         "allof_without_items_gets_placeholder",
			input:        `{"type":"object","properties":{"ids":{"type":"array","allOf":[{"description":"x"}]}}}`,
			path:         "properties.ids",
			wantItemType: "string",
			wantDesc:     "x",
		},
	}

	for cleanerName, cleaner := range cleaners {
		for _, tc := range cases {
			t.Run(cleanerName+"/"+tc.name, func(t *testing.T) {
				got := cleaner.clean(tc.input)
				node := gjson.Parse(got)
				if tc.path != "" {
					node = node.Get(tc.path)
				}
				if gotType := node.Get("type").String(); gotType != "array" {
					t.Errorf("type = %q, want \"array\": %s", gotType, got)
				}
				if gotItemType := node.Get("items.type").String(); gotItemType != tc.wantItemType {
					t.Errorf("items.type = %q, want %q: %s", gotItemType, tc.wantItemType, got)
				}
				if tc.wantDesc != "" {
					if gotDesc := node.Get("description").String(); gotDesc != tc.wantDesc {
						t.Errorf("description = %q, want %q: %s", gotDesc, tc.wantDesc, got)
					}
				}
				if tc.wantNullable && cleaner.preservesNullable {
					if !node.Get("nullable").Bool() {
						t.Errorf("nullable = false, want true: %s", got)
					}
				}
				compareJSON(t, got, cleaner.clean(got))
			})
		}
	}
}
