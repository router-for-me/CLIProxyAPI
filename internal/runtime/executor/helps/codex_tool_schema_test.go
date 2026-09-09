package helps

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestNormalizeCodexToolSchemas_ComplexOneOfSimplified_PreservesProperties(t *testing.T) {
	// Minimal repro from issue #5551: 13-branch oneOf inside a property schema
	input := []byte(`{
		"model": "gpt-5.5",
		"tools": [{
			"type": "function",
			"name": "t1",
			"description": "test tool",
			"strict": true,
			"parameters": {
				"type": "object",
				"properties": {
					"action": {
						"type": "string",
						"enum": ["p.list","m.list","s.list","s.create","s.send","s.fork","s.status","s.messages","sch.list","sch.create","sch.run","sch.delete","sch.toggle"],
						"oneOf": [
							{"const": "p.list", "description": "List projects"},
							{"const": "m.list", "description": "List models"},
							{"const": "s.list", "description": "List sessions"},
							{"const": "s.create", "description": "Create session"},
							{"const": "s.send", "description": "Send prompt"},
							{"const": "s.fork", "description": "Fork session"},
							{"const": "s.status", "description": "Session status"},
							{"const": "s.messages", "description": "Session messages"},
							{"const": "sch.list", "description": "List schedule"},
							{"const": "sch.create", "description": "Create schedule"},
							{"const": "sch.run", "description": "Run schedule"},
							{"const": "sch.delete", "description": "Delete schedule"},
							{"const": "sch.toggle", "description": "Toggle schedule"}
						],
						"description": "Action to perform"
					},
					"target": {
						"type": "string",
						"description": "Target ID"
					}
				},
				"required": ["action"]
			}
		}]
	}`)

	out := NormalizeCodexToolSchemas(input)

	tool := gjson.GetBytes(out, "tools.0")
	// Complex oneOf must be removed from action property
	if tool.Get("parameters.properties.action.oneOf").Exists() {
		t.Fatalf("expected oneOf to be removed from action property, got: %s", tool.Get("parameters").Raw)
	}
	// Action type and enum must be PRESERVED
	if tool.Get("parameters.properties.action.type").String() != "string" {
		t.Fatalf("expected action.type = string, got: %s", tool.Get("parameters.properties.action.type").String())
	}
	enumArr := tool.Get("parameters.properties.action.enum").Array()
	if len(enumArr) != 13 {
		t.Fatalf("expected action.enum to retain all 13 items, got %d", len(enumArr))
	}
	// Sibling property 'target' must be PRESERVED
	if tool.Get("parameters.properties.target.type").String() != "string" {
		t.Fatalf("sibling property 'target' was deleted or altered")
	}
	// 'required' array must be PRESERVED
	if tool.Get("parameters.required.0").String() != "action" {
		t.Fatalf("required list was deleted or altered")
	}
	// Tool name and strict mode preserved
	if tool.Get("name").String() != "t1" {
		t.Fatalf("name should be preserved as t1, got: %s", tool.Get("name").String())
	}
	if !tool.Get("strict").Bool() {
		t.Fatalf("strict should remain true since schema is still strictly typed")
	}
}

func TestNormalizeCodexToolSchemas_DottedPropertyName(t *testing.T) {
	// A property whose key contains a dot (e.g. "my.action") must not be split into nested paths
	input := []byte(`{
		"model": "gpt-5.5",
		"tools": [{
			"type": "function",
			"name": "dotted_tool",
			"parameters": {
				"type": "object",
				"properties": {
					"my.action": {
						"type": "string",
						"enum": ["1", "2", "3", "4", "5", "6", "7", "8"],
						"oneOf": [
							{"const": "1"}, {"const": "2"}, {"const": "3"}, {"const": "4"},
							{"const": "5"}, {"const": "6"}, {"const": "7"}, {"const": "8"}
						]
					}
				}
			}
		}]
	}`)

	out := NormalizeCodexToolSchemas(input)

	// Ensure properties contains "my.action", NOT nested object "my": {"action": ...}
	tool := gjson.GetBytes(out, "tools.0")
	dottedProp := tool.Get(`parameters.properties.my\.action`)
	if !dottedProp.Exists() {
		t.Fatalf("expected my.action property to exist with escaped key, got parameters: %s", tool.Get("parameters").Raw)
	}
	if dottedProp.Get("oneOf").Exists() {
		t.Fatalf("oneOf should be removed from my.action")
	}
	if dottedProp.Get("type").String() != "string" {
		t.Fatalf("type should be string")
	}
	// Verify that "my" was NOT created as an object containing "action"
	if tool.Get("parameters.properties.my.action").Exists() {
		t.Fatalf("my.action was incorrectly split into nested path parameters.properties.my.action")
	}
}

func TestNormalizeCodexToolSchemas_ColonPropertyName(t *testing.T) {
	// A property whose key starts with or contains a colon (e.g. ":action") must not be interpreted as control syntax
	input := []byte(`{
		"model": "gpt-5.5",
		"tools": [{
			"type": "function",
			"name": "colon_tool",
			"parameters": {
				"type": "object",
				"properties": {
					":action": {
						"type": "string",
						"enum": ["1", "2", "3", "4", "5", "6", "7", "8"],
						"oneOf": [
							{"const": "1"}, {"const": "2"}, {"const": "3"}, {"const": "4"},
							{"const": "5"}, {"const": "6"}, {"const": "7"}, {"const": "8"}
						]
					}
				}
			}
		}]
	}`)

	out := NormalizeCodexToolSchemas(input)

	tool := gjson.GetBytes(out, "tools.0")
	colonProp := tool.Get(`parameters.properties.\:action`)
	if !colonProp.Exists() {
		t.Fatalf("expected :action property to exist with escaped colon, got parameters: %s", tool.Get("parameters").Raw)
	}
	if colonProp.Get("oneOf").Exists() {
		t.Fatalf("oneOf should be removed from :action")
	}
	if tool.Get("parameters.properties.action").Exists() {
		t.Fatalf(":action was incorrectly written to action without colon")
	}
}

func TestNormalizeCodexToolSchemas_NumericDuplicateConstNotTouched(t *testing.T) {
	// 1 and 1.0 are mathematically equal in JSON Schema. Having both in oneOf violates exclusivity.
	// Because exclusivity cannot be proven, the union must remain completely untouched.
	input := []byte(`{
		"model": "gpt-5.5",
		"tools": [{
			"type": "function",
			"name": "num_dup_tool",
			"parameters": {
				"type": "object",
				"properties": {
					"val": {
						"type": "number",
						"oneOf": [
							{"const": 1}, {"const": 1.0}, {"const": 2}, {"const": 3},
							{"const": 4}, {"const": 5}, {"const": 6}, {"const": 7}
						]
					}
				}
			}
		}]
	}`)

	out := NormalizeCodexToolSchemas(input)

	tool := gjson.GetBytes(out, "tools.0")
	if !tool.Get("parameters.properties.val.oneOf").Exists() {
		t.Fatalf("oneOf with numeric duplicates (1 and 1.0) must NOT be transformed to enum")
	}
}

func TestNormalizeCodexToolSchemas_LargeIntegerPrecisionPreserved(t *testing.T) {
	// Number larger than 2^53 (53 bits of mantissa in float64) must not lose precision
	largeInt := "9007199254740993"
	input := []byte(`{
		"model": "gpt-5.5",
		"tools": [{
			"type": "function",
			"name": "large_int_tool",
			"parameters": {
				"type": "object",
				"properties": {
					"id": {
						"type": "integer",
						"oneOf": [
							{"const": ` + largeInt + `},
							{"const": 1}, {"const": 2}, {"const": 3},
							{"const": 4}, {"const": 5}, {"const": 6}, {"const": 7}
						]
					}
				}
			}
		}]
	}`)

	out := NormalizeCodexToolSchemas(input)

	tool := gjson.GetBytes(out, "tools.0")
	if tool.Get("parameters.properties.id.oneOf").Exists() {
		t.Fatalf("oneOf should be deleted")
	}
	// Raw string of first enum item must preserve exact large integer digits without float rounding
	firstEnumItem := tool.Get("parameters.properties.id.enum.0").Raw
	if firstEnumItem != largeInt {
		t.Fatalf("large integer precision was lost: got %s, want %s", firstEnumItem, largeInt)
	}
}

func TestNormalizeCodexToolSchemas_UnicodeDuplicateConstNotTouched(t *testing.T) {
	// Branch containing "\u0061" and branch containing "a" have the same semantic string value.
	// In oneOf this violates exclusivity; because it's not pure distinct constants, keep untouched.
	input := []byte(`{
		"model": "gpt-5.5",
		"tools": [{
			"type": "function",
			"name": "dup_tool",
			"parameters": {
				"type": "object",
				"properties": {
					"val": {
						"type": "string",
						"oneOf": [
							{"const": "a"}, {"const": "\u0061"}, {"const": "c"}, {"const": "d"},
							{"const": "e"}, {"const": "f"}, {"const": "g"}, {"const": "h"}
						]
					}
				}
			}
		}]
	}`)

	out := NormalizeCodexToolSchemas(input)

	tool := gjson.GetBytes(out, "tools.0")
	if !tool.Get("parameters.properties.val.oneOf").Exists() {
		t.Fatalf("oneOf with duplicate semantic values must NOT be transformed to enum")
	}
}

func TestNormalizeCodexToolSchemas_TypePreservingComparison(t *testing.T) {
	// Existing enum contains strings ["1", ..., "8"], but oneOf contains numbers [1, ..., 8].
	// Because JSON types differ, they must NOT be treated as identical and must remain untouched.
	input := []byte(`{
		"model": "gpt-5.5",
		"tools": [{
			"type": "function",
			"name": "type_tool",
			"parameters": {
				"type": "object",
				"properties": {
					"val": {
						"enum": ["1", "2", "3", "4", "5", "6", "7", "8"],
						"oneOf": [
							{"const": 1}, {"const": 2}, {"const": 3}, {"const": 4},
							{"const": 5}, {"const": 6}, {"const": 7}, {"const": 8}
						]
					}
				}
			}
		}]
	}`)

	out := NormalizeCodexToolSchemas(input)

	tool := gjson.GetBytes(out, "tools.0")
	if !tool.Get("parameters.properties.val.oneOf").Exists() {
		t.Fatalf("oneOf must NOT be deleted when enum strings do not match const numbers")
	}
}

func TestNormalizeCodexToolSchemas_BothOneOfAndAnyOfUntouched(t *testing.T) {
	// A property with BOTH oneOf and anyOf must be left completely untouched
	input := []byte(`{
		"model": "gpt-5.5",
		"tools": [{
			"type": "function",
			"name": "compound_tool",
			"parameters": {
				"type": "object",
				"properties": {
					"val": {
						"oneOf": [
							{"const": "1"}, {"const": "2"}, {"const": "3"}, {"const": "4"},
							{"const": "5"}, {"const": "6"}, {"const": "7"}, {"const": "8"}
						],
						"anyOf": [
							{"const": "5"}, {"const": "6"}, {"const": "7"}, {"const": "8"},
							{"const": "9"}, {"const": "10"}, {"const": "11"}, {"const": "12"}
						]
					}
				}
			}
		}]
	}`)

	out := NormalizeCodexToolSchemas(input)

	tool := gjson.GetBytes(out, "tools.0")
	if !tool.Get("parameters.properties.val.oneOf").Exists() || !tool.Get("parameters.properties.val.anyOf").Exists() {
		t.Fatalf("compound oneOf+anyOf property must not be modified")
	}
}

func TestNormalizeCodexToolSchemas_MigratesConstBranchesToEnum(t *testing.T) {
	// Property has oneOf with 10 const branches, but no enum field
	input := []byte(`{
		"model": "gpt-5.5",
		"tools": [{
			"type": "function",
			"name": "t2",
			"parameters": {
				"type": "object",
				"properties": {
					"mode": {
						"type": "string",
						"oneOf": [
							{"const": "m1"}, {"const": "m2"}, {"const": "m3"}, {"const": "m4"},
							{"const": "m5"}, {"const": "m6"}, {"const": "m7"}, {"const": "m8"},
							{"const": "m9"}, {"const": "m10"}
						]
					}
				}
			}
		}]
	}`)

	out := NormalizeCodexToolSchemas(input)

	tool := gjson.GetBytes(out, "tools.0")
	if tool.Get("parameters.properties.mode.oneOf").Exists() {
		t.Fatalf("oneOf should be deleted")
	}
	enumArr := tool.Get("parameters.properties.mode.enum").Array()
	if len(enumArr) != 10 {
		t.Fatalf("expected 10 enum items migrated from const, got %d", len(enumArr))
	}
}

func TestNormalizeCodexToolSchemas_NonMatchingEnumNotTouched(t *testing.T) {
	// Existing enum has 9 values, while oneOf const branches only cover 8 values.
	// Because they are not proven identical, the union must remain completely untouched.
	input := []byte(`{
		"model": "gpt-5.5",
		"tools": [{
			"type": "function",
			"name": "t3",
			"parameters": {
				"type": "object",
				"properties": {
					"status": {
						"type": "string",
						"enum": ["1", "2", "3", "4", "5", "6", "7", "8", "extra"],
						"oneOf": [
							{"const": "1"}, {"const": "2"}, {"const": "3"}, {"const": "4"},
							{"const": "5"}, {"const": "6"}, {"const": "7"}, {"const": "8"}
						]
					}
				}
			}
		}]
	}`)

	out := NormalizeCodexToolSchemas(input)

	tool := gjson.GetBytes(out, "tools.0")
	if !tool.Get("parameters.properties.status.oneOf").Exists() {
		t.Fatalf("oneOf should NOT be removed when enum set does not match const values set")
	}
}

func TestNormalizeCodexToolSchemas_NonConstUnionNotTouched(t *testing.T) {
	// Union branches have additional constraints (e.g. pattern, type) and are not pure consts.
	// Must remain completely untouched to preserve validation semantics.
	input := []byte(`{
		"model": "gpt-5.5",
		"tools": [{
			"type": "function",
			"name": "t4",
			"parameters": {
				"type": "object",
				"properties": {
					"data": {
						"oneOf": [
							{"type": "string", "pattern": "^[a-z]+$"},
							{"type": "number", "minimum": 0},
							{"type": "boolean"},
							{"type": "null"},
							{"type": "array"},
							{"type": "object"},
							{"type": "integer"},
							{"type": "string", "pattern": "^[0-9]+$"}
						]
					}
				}
			}
		}]
	}`)

	out := NormalizeCodexToolSchemas(input)

	tool := gjson.GetBytes(out, "tools.0")
	if !tool.Get("parameters.properties.data.oneOf").Exists() {
		t.Fatalf("non-const oneOf must remain untouched")
	}
}

func TestNormalizeCodexToolSchemas_SimpleToolPreserved(t *testing.T) {
	input := []byte(`{
		"model": "gpt-5.5",
		"tools": [{
			"type": "function",
			"name": "lookup",
			"strict": true,
			"parameters": {
				"type": "object",
				"properties": {
					"query": {"type": "string"}
				},
				"required": ["query"],
				"additionalProperties": false
			}
		}]
	}`)

	out := NormalizeCodexToolSchemas(input)

	tool := gjson.GetBytes(out, "tools.0")
	if tool.Get("parameters.properties.query.type").String() != "string" {
		t.Fatalf("simple tool should not be modified, got: %s", tool.Get("parameters").Raw)
	}
	if !tool.Get("strict").Bool() {
		t.Fatalf("strict should remain true for simple tool")
	}
}

func TestNormalizeCodexToolSchemas_NamespaceToolSimplified(t *testing.T) {
	input := []byte(`{
		"model": "gpt-5.5",
		"tools": [{
			"type": "namespace",
			"name": "mcp",
			"tools": [{
				"type": "function",
				"name": "complex_tool",
				"parameters": {
					"type": "object",
					"properties": {
						"action": {
							"type": "string",
							"oneOf": [
								{"const": "1"}, {"const": "2"}, {"const": "3"}, {"const": "4"},
								{"const": "5"}, {"const": "6"}, {"const": "7"}, {"const": "8"}
							]
						}
					}
				}
			}]
		}]
	}`)

	out := NormalizeCodexToolSchemas(input)

	nestedTool := gjson.GetBytes(out, "tools.0.tools.0")
	if nestedTool.Get("parameters.properties.action.oneOf").Exists() {
		t.Fatalf("nested tool oneOf should be simplified")
	}
	if len(nestedTool.Get("parameters.properties.action.enum").Array()) != 8 {
		t.Fatalf("expected action.enum with 8 items")
	}
}
