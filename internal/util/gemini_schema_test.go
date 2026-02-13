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
				"enum": ["InsightVizNode"]
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
				"description": "(nullable)"
			},
			"other": {
				"type": "string"
			}
		},
		"required": ["other"]
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

	// After $ref is converted to placeholder object, empty schema placeholder is also added
	expected := `{
		"type": "object",
		"properties": {
			"customer": {
				"type": "object",
				"description": "See: User",
				"properties": {
					"reason": {
						"type": "string",
						"description": "Brief explanation of why you are calling this tool"
					}
				},
				"required": ["reason"]
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

	// After $ref is converted, empty schema placeholder is also added
	expected := `{
		"type": "object",
		"properties": {
			"customer": {
				"type": "object",
				"description": "He said \"hi\"\\nsecond line (See: User)",
				"properties": {
					"reason": {
						"type": "string",
						"description": "Brief explanation of why you are calling this tool"
					}
				},
				"required": ["reason"]
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

	desc, ok := resMap["description"].(string)
	if !ok || !strings.Contains(desc, "Node") {
		t.Errorf("Expected description hint containing 'Node', got: %v", resMap["description"])
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
				"description": "(nullable)"
			},
			"other": {
				"type": "string"
			}
		},
		"required": ["other"]
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

func TestCleanJSONSchemaForAntigravity_SingleEnumNoHint(t *testing.T) {
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

	if strings.Contains(result, "Allowed:") {
		t.Errorf("Single value enum should not add Allowed hint, got: %s", result)
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

func TestCleanJSONSchemaForAntigravity_NumericEnumToString(t *testing.T) {
	// Gemini API requires enum values to be strings, not numbers
	input := `{
		"type": "object",
		"properties": {
			"priority": {"type": "integer", "enum": [0, 1, 2]},
			"level": {"type": "number", "enum": [1.5, 2.5, 3.5]},
			"status": {"type": "string", "enum": ["active", "inactive"]}
		}
	}`

	result := CleanJSONSchemaForAntigravity(input)

	// Numeric enum values should be converted to strings
	if strings.Contains(result, `"enum":[0,1,2]`) {
		t.Errorf("Integer enum values should be converted to strings, got: %s", result)
	}
	if strings.Contains(result, `"enum":[1.5,2.5,3.5]`) {
		t.Errorf("Float enum values should be converted to strings, got: %s", result)
	}
	// Should contain string versions
	if !strings.Contains(result, `"0"`) || !strings.Contains(result, `"1"`) || !strings.Contains(result, `"2"`) {
		t.Errorf("Integer enum values should be converted to string format, got: %s", result)
	}
	// String enum values should remain unchanged
	if !strings.Contains(result, `"active"`) || !strings.Contains(result, `"inactive"`) {
		t.Errorf("String enum values should remain unchanged, got: %s", result)
	}
}

func TestCleanJSONSchemaForAntigravity_BooleanEnumToString(t *testing.T) {
	// Boolean enum values should also be converted to strings
	input := `{
		"type": "object",
		"properties": {
			"enabled": {"type": "boolean", "enum": [true, false]}
		}
	}`

	result := CleanJSONSchemaForAntigravity(input)

	// Boolean enum values should be converted to strings
	if strings.Contains(result, `"enum":[true,false]`) {
		t.Errorf("Boolean enum values should be converted to strings, got: %s", result)
	}
	// Should contain string versions "true" and "false"
	if !strings.Contains(result, `"true"`) || !strings.Contains(result, `"false"`) {
		t.Errorf("Boolean enum values should be converted to string format, got: %s", result)
	}
}

func TestCleanJSONSchemaForGemini_RemovesGeminiUnsupportedMetadataFields(t *testing.T) {
	input := `{
		"$schema": "http://json-schema.org/draft-07/schema#",
		"$id": "root-schema",
		"type": "object",
		"properties": {
			"payload": {
				"type": "object",
				"prefill": "hello",
				"properties": {
					"mode": {
						"type": "string",
						"enum": ["a", "b"],
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
