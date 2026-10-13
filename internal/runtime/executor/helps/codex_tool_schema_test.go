package helps

import (
	"net/http"
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

func TestNormalizeCodexToolSchemas_StripsUnsupportedUnicodePropertyEscapePatterns(t *testing.T) {
	input := []byte(`{
		"model": "gpt-5.6",
		"tools": [{
			"type": "function",
			"name": "Artifact",
			"parameters": {
				"type": "object",
				"properties": {
					"field": {
						"type": "string",
						"description": "field to edit",
						"pattern": "^(?!__.*__$)[^\\p{Cc}\\p{Cf}\\p{Zl}\\p{Zp}\"\\\\./[\\]]{1,200}$"
					},
					"asset_id": {
						"type": "string",
						"pattern": "^[0-9a-f]{32}$"
					}
				},
				"required": ["field"]
			}
		}]
	}`)

	out := NormalizeCodexToolSchemas(input)

	tool := gjson.GetBytes(out, "tools.0")
	params := tool.Get("parameters")

	// Unsupported pattern must be removed
	if params.Get("properties.field.pattern").Exists() {
		t.Errorf("expected properties.field.pattern to be removed, got: %s", params.Get("properties.field.pattern").Raw)
	}
	if got := params.Get("properties.field.type").String(); got != "string" {
		t.Errorf("expected properties.field.type == 'string', got %q", got)
	}

	// Valid pattern must be preserved
	if got := params.Get("properties.asset_id.pattern").String(); got != "^[0-9a-f]{32}$" {
		t.Errorf("expected properties.asset_id.pattern preserved, got %q", got)
	}

	// Idempotence test
	outAgain := NormalizeCodexToolSchemas(out)
	if string(outAgain) != string(out) {
		t.Errorf("expected NormalizeCodexToolSchemas to be idempotent")
	}
}

func TestNormalizeCodexToolSchemas_StripsOctalNULPatternEscape(t *testing.T) {
	// Claude Code's built-in Artifact tool guards file paths with the octal NUL
	// escape, e.g. "^[^\\0]*$" on the upload_asset file_paths item. Strict upstream
	// validators reject that spelling ("is not a 'regex'") while accepting the
	// equivalent \\x00, so the pattern has to come off before the request goes out.
	input := []byte(`{
		"model": "gpt-5.6",
		"tools": [{
			"type": "function",
			"name": "Artifact",
			"parameters": {
				"type": "object",
				"properties": {
					"file_paths": {
						"type": "array",
						"minItems": 1,
						"items": {
							"type": "string",
							"minLength": 1,
							"maxLength": 1024,
							"pattern": "^[^\\0]*$"
						}
					},
					"asset_id": {
						"type": "string",
						"pattern": "^[0-9a-f]{32}$"
					},
					"hex_nul": {
						"type": "string",
						"pattern": "^[^\\x00]*$"
					}
				},
				"required": ["file_paths"]
			}
		}]
	}`)

	out := NormalizeCodexToolSchemas(input)

	params := gjson.GetBytes(out, "tools.0.parameters")

	// The octal NUL pattern must be removed, while the rest of the item schema stays.
	if params.Get("properties.file_paths.items.pattern").Exists() {
		t.Errorf("expected properties.file_paths.items.pattern to be removed, got: %s",
			params.Get("properties.file_paths.items.pattern").Raw)
	}
	if got := params.Get("properties.file_paths.items.type").String(); got != "string" {
		t.Errorf("expected properties.file_paths.items.type == 'string', got %q", got)
	}
	for path, want := range map[string]string{
		"properties.file_paths.items.minLength": "1",
		"properties.file_paths.items.maxLength": "1024",
		"properties.file_paths.minItems":        "1",
	} {
		if got := params.Get(path).String(); got != want {
			t.Errorf("expected %s == %q, got %q", path, want, got)
		}
	}

	// A plain pattern is valid and must survive.
	if got := params.Get("properties.asset_id.pattern").String(); got != "^[0-9a-f]{32}$" {
		t.Errorf("expected properties.asset_id.pattern preserved, got %q", got)
	}

	// The hex NUL spelling is the one strict validators accept, so it must survive.
	if got := params.Get("properties.hex_nul.pattern").String(); got != `^[^\x00]*$` {
		t.Errorf("expected properties.hex_nul.pattern preserved, got %q", got)
	}

	// Idempotence test
	outAgain := NormalizeCodexToolSchemas(out)
	if string(outAgain) != string(out) {
		t.Errorf("expected NormalizeCodexToolSchemas to be idempotent")
	}
}

func TestNormalizeCodexToolSchemas_PreservesNonSchemaPatternKeys(t *testing.T) {
	// A property whose default, enum, or description metadata contains a nested object
	// with a 'pattern' key must NOT be mutated, because it is user data, not a schema.
	input := []byte(`{
		"model": "gpt-5.6",
		"tools": [{
			"type": "function",
			"name": "config_tool",
			"parameters": {
				"type": "object",
				"properties": {
					"regex_config": {
						"type": "object",
						"default": {
							"pattern": "\\p{L}+"
						},
						"enum": [
							{"pattern": "\\p{N}+"}
						]
					},
					"real_schema": {
						"type": "string",
						"pattern": "\\p{L}+"
					}
				}
			}
		}]
	}`)

	out := NormalizeCodexToolSchemas(input)

	tool := gjson.GetBytes(out, "tools.0")
	params := tool.Get("parameters")

	// Real schema pattern must be removed
	if params.Get("properties.real_schema.pattern").Exists() {
		t.Errorf("expected real_schema.pattern to be removed, got: %s", params.Get("properties.real_schema").Raw)
	}

	// User data under default and enum must be PRESERVED
	if got := params.Get("properties.regex_config.default.pattern").String(); got != `\p{L}+` {
		t.Errorf("expected default.pattern preserved, got %q", got)
	}
	if got := params.Get("properties.regex_config.enum.0.pattern").String(); got != `\p{N}+` {
		t.Errorf("expected enum.0.pattern preserved, got %q", got)
	}
}

func TestNormalizeCodexToolSchemas_CoversAllSchemaKeywordLocations(t *testing.T) {
	input := []byte(`{
		"model": "gpt-5.6",
		"tools": [{
			"type": "function",
			"name": "deep_tool",
			"parameters": {
				"type": "object",
				"$defs": {
					"custom_type": {
						"type": "string",
						"pattern": "\\p{L}+"
					}
				},
				"additionalProperties": {
					"type": "string",
					"pattern": "\\p{N}+"
				},
				"patternProperties": {
					"^s_": {
						"type": "string",
						"pattern": "\\p{M}+"
					}
				},
				"if": {
					"properties": {
						"flag": {
							"type": "string",
							"pattern": "\\p{P}+"
						}
					}
				},
				"then": {
					"properties": {
						"val": {
							"type": "string",
							"pattern": "\\p{S}+"
						}
					}
				},
				"else": {
					"properties": {
						"other": {
							"type": "string",
							"pattern": "\\p{Z}+"
						}
					}
				}
			}
		}]
	}`)

	out := NormalizeCodexToolSchemas(input)

	tool := gjson.GetBytes(out, "tools.0")
	params := tool.Get("parameters")

	// All subschemas in schema-aware locations must have their incompatible patterns stripped
	if params.Get("$defs.custom_type.pattern").Exists() {
		t.Errorf("expected $defs.custom_type.pattern to be removed")
	}
	if params.Get("additionalProperties.pattern").Exists() {
		t.Errorf("expected additionalProperties.pattern to be removed")
	}
	if params.Get("patternProperties.^s_.pattern").Exists() {
		t.Errorf("expected patternProperties.^s_.pattern to be removed")
	}
	if params.Get("if.properties.flag.pattern").Exists() {
		t.Errorf("expected if.properties.flag.pattern to be removed")
	}
	if params.Get("then.properties.val.pattern").Exists() {
		t.Errorf("expected then.properties.val.pattern to be removed")
	}
	if params.Get("else.properties.other.pattern").Exists() {
		t.Errorf("expected else.properties.other.pattern to be removed")
	}

	// Subschema types must be preserved
	if got := params.Get("$defs.custom_type.type").String(); got != "string" {
		t.Errorf("expected $defs.custom_type.type == 'string', got %q", got)
	}
}

func TestNormalizeCodexToolSchemas_MalformedOrEmptyParametersFallback(t *testing.T) {
	// Malformed JSON, non-object parameters, null, and empty payloads must not panic
	cases := [][]byte{
		[]byte(`{"model":"gpt-5.6","tools":[{"type":"function","name":"t","parameters":null}]}`),
		[]byte(`{"model":"gpt-5.6","tools":[{"type":"function","name":"t","parameters":"not_an_object"}]}`),
		[]byte(`{"model":"gpt-5.6","tools":[{"type":"function","name":"t","parameters":{"type":"object"}}]}`),
		[]byte(`{"model":"gpt-5.6","tools":[]}`),
		[]byte(`{"model":"gpt-5.6"}`),
	}

	for i, c := range cases {
		out := NormalizeCodexToolSchemas(c)
		if len(out) == 0 {
			t.Errorf("case %d: unexpected empty output", i)
		}
	}
}

func TestNormalizeCodexToolSchemas_JSONUnicodeEscapeBypassPrevention(t *testing.T) {
	// Patterns encoded using JSON Unicode escapes (e.g. \u005c for '\' or \u0070 for 'p')
	// decode to \p{...} / \P{...} and must not be skipped by the fast-path check.
	input := []byte(`{
		"model": "gpt-5.6",
		"tools": [{
			"type": "function",
			"name": "escape_bypass_tool",
			"parameters": {
				"type": "object",
				"properties": {
					"p1": {
						"type": "string",
						"pattern": "\u005c\u0070{L}+"
					},
					"p2": {
						"type": "string",
						"pattern": "\u005cp{Cc}"
					},
					"p3": {
						"type": "string",
						"pattern": "\u005c\u0050{N}+"
					},
					"valid": {
						"type": "string",
						"pattern": "^[0-9a-f]{32}$"
					}
				}
			}
		}]
	}`)

	out := NormalizeCodexToolSchemas(input)
	tool := gjson.GetBytes(out, "tools.0")
	params := tool.Get("parameters")

	if params.Get("properties.p1.pattern").Exists() {
		t.Errorf("expected properties.p1.pattern (\\u0070) to be removed, got: %s", params.Get("properties.p1.pattern").Raw)
	}
	if params.Get("properties.p2.pattern").Exists() {
		t.Errorf("expected properties.p2.pattern (\\u005c) to be removed, got: %s", params.Get("properties.p2.pattern").Raw)
	}
	if params.Get("properties.p3.pattern").Exists() {
		t.Errorf("expected properties.p3.pattern (\\u0050) to be removed, got: %s", params.Get("properties.p3.pattern").Raw)
	}
	if got := params.Get("properties.valid.pattern").String(); got != "^[0-9a-f]{32}$" {
		t.Errorf("expected valid.pattern to be preserved, got %q", got)
	}
}

func TestNormalizeCodexToolSchemas_PatternPropertiesKeySanitization(t *testing.T) {
	input := []byte(`{
		"model": "gpt-5.6",
		"tools": [{
			"type": "function",
			"name": "pattern_props_tool",
			"parameters": {
				"type": "object",
				"patternProperties": {
					"^\\\\p{L}+$": {
						"type": "string"
					},
					"^[a-z]+$": {
						"type": "number"
					}
				}
			}
		}]
	}`)

	out := NormalizeCodexToolSchemas(input)
	tool := gjson.GetBytes(out, "tools.0")
	params := tool.Get("parameters")

	// Key with \p{L}+ must be removed
	patternProps := params.Get("patternProperties").Map()
	if _, exists := patternProps[`^\p{L}+$`]; exists {
		t.Errorf("expected patternProperties key '^\\\\p{L}+$' to be removed, got: %s", params.Get("patternProperties").Raw)
	}
	// Safe key must be preserved
	if _, exists := patternProps[`^[a-z]+$`]; !exists {
		t.Errorf("expected patternProperties key '^[a-z]+$' to be preserved, got: %s", params.Get("patternProperties").Raw)
	}
}

func TestNormalizeCodexToolIntegerTypes_CodexClientToolFieldTypes(t *testing.T) {
	input := []byte(`{
		"tools": [
			{
				"type": "function",
				"name": "exec_command",
				"parameters": {
					"type": "object",
					"properties": {
						"cmd": {"type": "string"},
						"yield_time_ms": {"type": "number"},
						"max_output_tokens": {"type": "number"},
						"timeout_ms": {"type": "number"}
					}
				}
			},
			{
				"type": "function",
				"name": "write_stdin",
				"parameters": {
					"type": "object",
					"properties": {
						"session_id": {"type": "number"},
						"yield_time_ms": {"type": "number"},
						"max_output_tokens": {"type": "number"}
					}
				}
			},
			{
				"type": "function",
				"name": "sleep",
				"parameters": {
					"type": "object",
					"properties": {
						"duration_ms": {"type": "number"}
					}
				}
			},
			{
				"type": "function",
				"name": "wait_agent",
				"parameters": {
					"type": "object",
					"properties": {
						"timeout_ms": {"type": "number"}
					}
				}
			},
			{
				"type": "function",
				"name": "wait",
				"parameters": {
					"type": "object",
					"properties": {
						"yield_time_ms": {"type": "number"},
						"max_tokens": {"type": "number"}
					}
				}
			},
			{
				"type": "function",
				"name": "tool_search",
				"parameters": {
					"type": "object",
					"properties": {
						"limit": {"type": "number"}
					}
				}
			},
			{
				"type": "function",
				"name": "test_sync_tool",
				"parameters": {
					"type": "object",
					"properties": {
						"sleep_before_ms": {"type": "number"},
						"sleep_after_ms": {"type": "number"},
						"participants": {"type": "number"},
						"timeout_ms": {"type": "number"}
					}
				}
			},
			{
				"type": "function",
				"name": "unrelated_tool",
				"parameters": {
					"type": "object",
					"properties": {
						"timeout_ms": {"type": "number"}
					}
				}
			}
		],
		"input": [
			{
				"type": "additional_tools",
				"tools": [
					{
						"type": "function",
						"name": "functions__exec_command",
						"parameters": {
							"type": "object",
							"properties": {
								"yield_time_ms": {"type": ["number", "null"]}
							}
						}
					}
				]
			}
		]
	}`)

	t.Run("non-codex user agent preserves numbers", func(t *testing.T) {
		headers := http.Header{"User-Agent": []string{"curl/8.7.1"}}
		out := NormalizeCodexToolIntegerTypes(input, headers)
		if got := gjson.GetBytes(out, "tools.0.parameters.properties.yield_time_ms.type").String(); got != "number" {
			t.Fatalf("expected number for non-codex, got: %s", got)
		}
	})

	t.Run("codex user agent normalizes specified fields", func(t *testing.T) {
		headers := http.Header{"User-Agent": []string{"codex-tui/0.154.0 (Mac OS 26.5.2; arm64)"}}
		out := NormalizeCodexToolIntegerTypes(input, headers)

		toolMap := make(map[string]gjson.Result)
		for _, tool := range gjson.GetBytes(out, "tools").Array() {
			toolMap[tool.Get("name").String()] = tool
		}

		expected := map[string][]string{
			"exec_command":   {"yield_time_ms", "max_output_tokens", "timeout_ms"},
			"write_stdin":    {"session_id", "yield_time_ms", "max_output_tokens"},
			"sleep":          {"duration_ms"},
			"wait_agent":     {"timeout_ms"},
			"wait":           {"yield_time_ms", "max_tokens"},
			"tool_search":    {"limit"},
			"test_sync_tool": {"sleep_before_ms", "sleep_after_ms", "participants", "timeout_ms"},
		}

		for toolName, fields := range expected {
			tool, ok := toolMap[toolName]
			if !ok {
				t.Fatalf("missing tool: %s", toolName)
			}
			for _, field := range fields {
				if got := tool.Get("parameters.properties." + field + ".type").String(); got != "integer" {
					t.Errorf("tool %s field %s type = %q, want integer", toolName, field, got)
				}
			}
		}

		// Check non-target field cmd in exec_command
		if got := toolMap["exec_command"].Get("parameters.properties.cmd.type").String(); got != "string" {
			t.Errorf("exec_command cmd type = %q, want string", got)
		}

		// Check unrelated tool retains number
		if got := toolMap["unrelated_tool"].Get("parameters.properties.timeout_ms.type").String(); got != "number" {
			t.Errorf("unrelated_tool timeout_ms type = %q, want number", got)
		}

		// Check additional_tools under input
		addToolType := gjson.GetBytes(out, "input.0.tools.0.parameters.properties.yield_time_ms.type").Array()
		if len(addToolType) != 2 || addToolType[0].String() != "integer" || addToolType[1].String() != "null" {
			t.Errorf("additional_tools yield_time_ms type = %v, want [integer null]", addToolType)
		}
	})

	t.Run("array type with number and integer deduplicates to single integer", func(t *testing.T) {
		dupInput := []byte(`{
			"tools": [
				{
					"type": "function",
					"name": "sleep",
					"parameters": {
						"type": "object",
						"properties": {
							"duration_ms": {"type": ["number", "integer", "null"]}
						}
					}
				}
			]
		}`)
		headers := http.Header{"User-Agent": []string{"codex-tui/0.154.0"}}
		out := NormalizeCodexToolIntegerTypes(dupInput, headers)
		arr := gjson.GetBytes(out, "tools.0.parameters.properties.duration_ms.type").Array()
		if len(arr) != 2 || arr[0].String() != "integer" || arr[1].String() != "null" {
			t.Errorf("deduplicated type = %v, want [integer null]", arr)
		}
	})

	t.Run("claude input_schema format supported", func(t *testing.T) {
		claudeInput := []byte(`{
			"tools": [
				{
					"name": "exec_command",
					"input_schema": {
						"type": "object",
						"properties": {
							"yield_time_ms": {"type": "number"},
							"timeout_ms": {"type": "number"}
						}
					}
				}
			]
		}`)
		headers := http.Header{"User-Agent": []string{"codex-tui/0.154.0"}}
		out := NormalizeCodexToolIntegerTypes(claudeInput, headers)
		if got := gjson.GetBytes(out, "tools.0.input_schema.properties.yield_time_ms.type").String(); got != "integer" {
			t.Errorf("claude tool yield_time_ms type = %q, want integer", got)
		}
		if got := gjson.GetBytes(out, "tools.0.input_schema.properties.timeout_ms.type").String(); got != "integer" {
			t.Errorf("claude tool timeout_ms type = %q, want integer", got)
		}
	})

	t.Run("gemini function_declarations format supported", func(t *testing.T) {
		geminiInput := []byte(`{
			"tools": [
				{
					"function_declarations": [
						{
							"name": "sleep",
							"parameters": {
								"type": "object",
								"properties": {
									"duration_ms": {"type": "number"}
								}
							}
						}
					]
				}
			]
		}`)
		headers := http.Header{"User-Agent": []string{"codex-desktop/0.159.0"}}
		out := NormalizeCodexToolIntegerTypes(geminiInput, headers)
		if got := gjson.GetBytes(out, "tools.0.function_declarations.0.parameters.properties.duration_ms.type").String(); got != "integer" {
			t.Errorf("gemini tool duration_ms type = %q, want integer", got)
		}
	})

	t.Run("preserves pattern and oneOf on unrelated schemas", func(t *testing.T) {
		schemaInput := []byte(`{
			"tools": [
				{
					"type": "function",
					"name": "custom_schema_tool",
					"parameters": {
						"type": "object",
						"properties": {
							"regex_field": {
								"type": "string",
								"pattern": "\\p{L}+"
							},
							"choice_field": {
								"oneOf": [{"const": "a"}, {"const": "b"}]
							}
						}
					}
				}
			]
		}`)
		headers := http.Header{"User-Agent": []string{"codex-tui/0.154.0"}}
		out := NormalizeCodexToolIntegerTypes(schemaInput, headers)
		if !gjson.GetBytes(out, "tools.0.parameters.properties.regex_field.pattern").Exists() {
			t.Errorf("NormalizeCodexToolIntegerTypes should not strip pattern")
		}
		if !gjson.GetBytes(out, "tools.0.parameters.properties.choice_field.oneOf").Exists() {
			t.Errorf("NormalizeCodexToolIntegerTypes should not modify oneOf")
		}
	})

	t.Run("nil or empty headers leaves payload untouched", func(t *testing.T) {
		outNil := NormalizeCodexToolIntegerTypes(input, nil)
		if got := gjson.GetBytes(outNil, "tools.0.parameters.properties.yield_time_ms.type").String(); got != "number" {
			t.Fatalf("expected nil headers to preserve number, got: %s", got)
		}
		outEmpty := NormalizeCodexToolIntegerTypes(input, http.Header{})
		if got := gjson.GetBytes(outEmpty, "tools.0.parameters.properties.yield_time_ms.type").String(); got != "number" {
			t.Fatalf("expected empty headers to preserve number, got: %s", got)
		}
	})

	t.Run("third-party mcp tools are not modified", func(t *testing.T) {
		mcpInput := []byte(`{
			"tools": [
				{
					"type": "function",
					"name": "mcp__server__sleep",
					"parameters": {
						"type": "object",
						"properties": {
							"duration_ms": {"type": "number"}
						}
					}
				},
				{
					"type": "function",
					"name": "mcp__server__exec_command",
					"parameters": {
						"type": "object",
						"properties": {
							"yield_time_ms": {"type": "number"}
						}
					}
				},
				{
					"type": "function",
					"name": "functions__sleep",
					"parameters": {
						"type": "object",
						"properties": {
							"duration_ms": {"type": "number"}
						}
					}
				},
				{
					"type": "function",
					"name": "collab__exec_command",
					"parameters": {
						"type": "object",
						"properties": {
							"yield_time_ms": {"type": "number"}
						}
					}
				}
			]
		}`)
		headers := http.Header{"User-Agent": []string{"codex-tui/0.154.0"}}
		out := NormalizeCodexToolIntegerTypes(mcpInput, headers)
		toolMap := make(map[string]gjson.Result)
		for _, tool := range gjson.GetBytes(out, "tools").Array() {
			toolMap[tool.Get("name").String()] = tool
		}
		// mcp__server__sleep must remain number
		if got := toolMap["mcp__server__sleep"].Get("parameters.properties.duration_ms.type").String(); got != "number" {
			t.Errorf("mcp__server__sleep duration_ms type = %q, want number", got)
		}
		// mcp__server__exec_command must remain number
		if got := toolMap["mcp__server__exec_command"].Get("parameters.properties.yield_time_ms.type").String(); got != "number" {
			t.Errorf("mcp__server__exec_command yield_time_ms type = %q, want number", got)
		}
		// functions__sleep must be normalized to integer
		if got := toolMap["functions__sleep"].Get("parameters.properties.duration_ms.type").String(); got != "integer" {
			t.Errorf("functions__sleep duration_ms type = %q, want integer", got)
		}
		// collab__exec_command must be normalized to integer
		if got := toolMap["collab__exec_command"].Get("parameters.properties.yield_time_ms.type").String(); got != "integer" {
			t.Errorf("collab__exec_command yield_time_ms type = %q, want integer", got)
		}
	})

	t.Run("gemini parametersJsonSchema format supported directly", func(t *testing.T) {
		geminiInput := []byte(`{
			"tools": [
				{
					"functionDeclarations": [
						{
							"name": "exec_command",
							"parametersJsonSchema": {
								"type": "object",
								"properties": {
									"yield_time_ms": {"type": "number"}
								}
							}
						}
					]
				}
			]
		}`)
		headers := http.Header{"User-Agent": []string{"codex-desktop/0.159.0"}}
		out := NormalizeCodexToolIntegerTypes(geminiInput, headers)
		if got := gjson.GetBytes(out, "tools.0.functionDeclarations.0.parametersJsonSchema.properties.yield_time_ms.type").String(); got != "integer" {
			t.Errorf("gemini parametersJsonSchema yield_time_ms type = %q, want integer", got)
		}
	})
}

func TestNormalizeCodexToolSchemas_DoesNotNormalizeIntegerTypes(t *testing.T) {
	input := []byte(`{
		"tools": [
			{
				"type": "function",
				"name": "exec_command",
				"parameters": {
					"type": "object",
					"properties": {
						"yield_time_ms": {"type": "number"}
					}
				}
			}
		]
	}`)
	out := NormalizeCodexToolSchemas(input)
	if got := gjson.GetBytes(out, "tools.0.parameters.properties.yield_time_ms.type").String(); got != "number" {
		t.Fatalf("expected number, got: %s", got)
	}
}
