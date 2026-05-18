package util

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// TestNormalizeCodexToolSchema_InvalidInput covers empty, "null", whitespace, and
// malformed JSON inputs. All should fall back to the default empty object schema.
func TestNormalizeCodexToolSchema_InvalidInput(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"empty string", ""},
		{"whitespace only", "   \t\n"},
		{"literal null", "null"},
		{"literal null with whitespace", "  null  "},
		{"malformed json", "{not json}"},
		{"truncated json", `{"type":`},
	}

	const want = `{"type":"object","properties":{}}`
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(NormalizeCodexToolSchema(tt.input))
			if got != want {
				t.Errorf("NormalizeCodexToolSchema(%q) = %q, want %q", tt.input, got, want)
			}
		})
	}
}

// TestNormalizeCodexToolSchema_TopLevelNullArrayFields verifies that all five
// array-typed schema keywords are stripped when null at the top level.
func TestNormalizeCodexToolSchema_TopLevelNullArrayFields(t *testing.T) {
	for _, field := range []string{"required", "enum", "allOf", "anyOf", "oneOf"} {
		t.Run(field, func(t *testing.T) {
			input := `{"type":"object","properties":{"foo":{"type":"string"}},"` + field + `":null}`
			got := NormalizeCodexToolSchema(input)
			if !gjson.ValidBytes(got) {
				t.Fatalf("output is not valid JSON: %s", got)
			}
			if gjson.GetBytes(got, field).Exists() {
				t.Errorf("expected field %q to be removed, output: %s", field, got)
			}
			if gjson.GetBytes(got, "type").String() != "object" {
				t.Errorf("expected type=object preserved, output: %s", got)
			}
			if !gjson.GetBytes(got, "properties.foo").Exists() {
				t.Errorf("expected properties.foo preserved, output: %s", got)
			}
		})
	}
}

// TestNormalizeCodexToolSchema_PreservesNonNullArrayFields ensures legitimate,
// non-null array values are kept intact.
func TestNormalizeCodexToolSchema_PreservesNonNullArrayFields(t *testing.T) {
	input := `{"type":"object","required":["a","b"],"properties":{"x":{"type":"string","enum":["one","two"]}}}`
	got := NormalizeCodexToolSchema(input)
	if !gjson.ValidBytes(got) {
		t.Fatalf("output is not valid JSON: %s", got)
	}
	if !gjson.GetBytes(got, "required").IsArray() {
		t.Errorf("expected required to remain an array, output: %s", got)
	}
	if enum := gjson.GetBytes(got, "properties.x.enum").Array(); len(enum) != 2 {
		t.Errorf("expected enum length 2, got %d, output: %s", len(enum), got)
	}
}

// TestNormalizeCodexToolSchema_NestedNullEnumInProperties covers the case where
// a null array field lives one level deep inside properties.
func TestNormalizeCodexToolSchema_NestedNullEnumInProperties(t *testing.T) {
	input := `{"type":"object","properties":{"someField":{"type":"string","enum":null}}}`
	got := NormalizeCodexToolSchema(input)
	if !gjson.ValidBytes(got) {
		t.Fatalf("output is not valid JSON: %s", got)
	}
	if gjson.GetBytes(got, "properties.someField.enum").Exists() {
		t.Errorf("expected nested null enum to be removed, output: %s", got)
	}
	if gjson.GetBytes(got, "properties.someField.type").String() != "string" {
		t.Errorf("expected sibling type=string preserved, output: %s", got)
	}
}

// TestNormalizeCodexToolSchema_NumericPropertyName verifies that property names
// composed entirely of digits are treated as object keys (not array indices),
// so null array fields nested inside them still get removed.
func TestNormalizeCodexToolSchema_NumericPropertyName(t *testing.T) {
	input := `{"type":"object","properties":{"123":{"type":"object","required":null,"description":"keep me"}}}`
	got := NormalizeCodexToolSchema(input)
	if !gjson.ValidBytes(got) {
		t.Fatalf("output is not valid JSON: %s", got)
	}
	// The numeric-keyed property must still exist.
	if !gjson.GetBytes(got, `properties.123`).Exists() {
		t.Errorf("expected properties.123 preserved, output: %s", got)
	}
	// The null required nested inside should be gone.
	if gjson.GetBytes(got, `properties.123.required`).Exists() {
		t.Errorf("expected properties.123.required removed, output: %s", got)
	}
	if gjson.GetBytes(got, `properties.123.description`).String() != "keep me" {
		t.Errorf("expected sibling description preserved, output: %s", got)
	}
}

// TestNormalizeCodexToolSchema_SpecialCharPropertyNames ensures property names
// containing characters with special meaning in sjson paths (\ . * ? :) are
// escaped correctly so the cleanup still locates and deletes the null fields.
func TestNormalizeCodexToolSchema_SpecialCharPropertyNames(t *testing.T) {
	// Each entry: JSON-encoded property name, gjson-escaped path to that property.
	cases := []struct {
		name            string
		propJSON        string // as it appears inside JSON (already JSON-escaped)
		gjsonPathToProp string // path used to look up the property via gjson
	}{
		{"dot", `foo.bar`, `properties.foo\.bar`},
		{"star", `weird*key`, `properties.weird\*key`},
		{"question", `q?key`, `properties.q\?key`},
		{"colon", `key:val`, `properties.key\:val`},
		{"backslash", `a\\b`, `properties.a\\b`}, // JSON \\ → "a\b" in-memory, sjson path needs \\b
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := `{"type":"object","properties":{"` + tc.propJSON + `":{"type":"string","enum":null,"description":"d"}}}`
			got := NormalizeCodexToolSchema(input)
			if !gjson.ValidBytes(got) {
				t.Fatalf("output is not valid JSON: %s", got)
			}
			// Property must still exist.
			if !gjson.GetBytes(got, tc.gjsonPathToProp).Exists() {
				t.Errorf("expected property at %s to be preserved, output: %s", tc.gjsonPathToProp, got)
			}
			// Null enum inside must be gone.
			if gjson.GetBytes(got, tc.gjsonPathToProp+".enum").Exists() {
				t.Errorf("expected enum under %s to be removed, output: %s", tc.gjsonPathToProp, got)
			}
			// Sibling preserved.
			if gjson.GetBytes(got, tc.gjsonPathToProp+".description").String() != "d" {
				t.Errorf("expected description preserved under %s, output: %s", tc.gjsonPathToProp, got)
			}
		})
	}
}

// TestNormalizeCodexToolSchema_ArrayContainersWithNullFields covers null array
// fields nested inside array containers (allOf/anyOf/oneOf items).
func TestNormalizeCodexToolSchema_ArrayContainersWithNullFields(t *testing.T) {
	input := `{
		"type":"object",
		"allOf":[
			{"type":"object","required":null},
			{"type":"object","properties":{"x":{"type":"string","enum":null}}}
		]
	}`
	got := NormalizeCodexToolSchema(input)
	if !gjson.ValidBytes(got) {
		t.Fatalf("output is not valid JSON: %s", got)
	}
	if gjson.GetBytes(got, "allOf.0.required").Exists() {
		t.Errorf("expected allOf.0.required removed, output: %s", got)
	}
	if gjson.GetBytes(got, "allOf.1.properties.x.enum").Exists() {
		t.Errorf("expected allOf.1.properties.x.enum removed, output: %s", got)
	}
	// The allOf container itself must remain.
	if !gjson.GetBytes(got, "allOf").IsArray() {
		t.Errorf("expected allOf array preserved, output: %s", got)
	}
}

// TestNormalizeCodexToolSchema_DeeplyNested validates that the iterative walker
// handles deep nesting without stack overflow and still removes a null array
// field at the deepest level.
func TestNormalizeCodexToolSchema_DeeplyNested(t *testing.T) {
	const depth = 200
	var b strings.Builder
	for i := 0; i < depth; i++ {
		b.WriteString(`{"type":"object","properties":{"n":`)
	}
	// Innermost object carries the null required field.
	b.WriteString(`{"type":"object","required":null,"description":"leaf"}`)
	for i := 0; i < depth; i++ {
		b.WriteString(`}}`)
	}

	input := b.String()
	got := NormalizeCodexToolSchema(input)
	if !gjson.ValidBytes(got) {
		t.Fatalf("output is not valid JSON for deeply nested input")
	}

	// Build the leaf path: properties.n.properties.n.[...].properties.n
	pathParts := make([]string, 0, depth)
	for i := 0; i < depth; i++ {
		pathParts = append(pathParts, "properties.n")
	}
	leafPath := strings.Join(pathParts, ".")

	if gjson.GetBytes(got, leafPath+".required").Exists() {
		t.Errorf("expected deeply nested null required to be removed at depth %d", depth)
	}
	if gjson.GetBytes(got, leafPath+".description").String() != "leaf" {
		t.Errorf("expected leaf description preserved at depth %d", depth)
	}
}

// TestNormalizeCodexToolSchema_NullInArrayElementContainer ensures that null
// array fields living inside array elements (objects in an array) are removed.
func TestNormalizeCodexToolSchema_NullInArrayElementContainer(t *testing.T) {
	input := `{"type":"object","anyOf":[{"required":null,"type":"object"},{"oneOf":null}]}`
	got := NormalizeCodexToolSchema(input)
	if !gjson.ValidBytes(got) {
		t.Fatalf("output is not valid JSON: %s", got)
	}
	if gjson.GetBytes(got, "anyOf.0.required").Exists() {
		t.Errorf("expected anyOf.0.required removed, output: %s", got)
	}
	if gjson.GetBytes(got, "anyOf.1.oneOf").Exists() {
		t.Errorf("expected anyOf.1.oneOf removed, output: %s", got)
	}
}
