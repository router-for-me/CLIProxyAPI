package util

import (
	"encoding/json"
	"testing"

	"github.com/tidwall/gjson"
)

func TestIssue6897_RefSiblingConjunctions(t *testing.T) {
	cases := []struct {
		name    string
		target  string
		sibling string
		checks  map[string]string
	}{
		{
			name: "target_minimum", target: `{"type":"string","minLength":5}`, sibling: `"minLength":1`,
			checks: map[string]string{"allOf.0.minLength": `5`, "minLength": `1`},
		},
		{
			name: "sibling_minimum", target: `{"type":"string","minLength":1}`, sibling: `"minLength":5`,
			checks: map[string]string{"allOf.0.minLength": `1`, "minLength": `5`},
		},
		{
			name: "maximum", target: `{"type":"string","maxLength":5}`, sibling: `"maxLength":10`,
			checks: map[string]string{"allOf.0.maxLength": `5`, "maxLength": `10`},
		},
		{
			name: "patterns", target: `{"type":"string","pattern":"^a"}`, sibling: `"pattern":"z$"`,
			checks: map[string]string{"allOf.0.pattern": `"^a"`, "pattern": `"z$"`},
		},
		{
			name: "disjoint_types", target: `{"type":"string"}`, sibling: `"type":"number"`,
			checks: map[string]string{"allOf.0.type": `"string"`, "type": `"number"`},
		},
		{
			name: "overlapping_types", target: `{"type":["string","number"]}`, sibling: `"type":"number"`,
			checks: map[string]string{"allOf.0.type": `["string","number"]`, "type": `"number"`},
		},
		{
			name: "overlapping_nullable_types", target: `{"type":["string","null"]}`, sibling: `"type":"null"`,
			checks: map[string]string{"allOf.0.type": `["string","null"]`, "type": `"null"`},
		},
		{
			name: "overlapping_array_types", target: `{"type":["array","null"],"items":{"type":"integer"}}`, sibling: `"type":"array"`,
			checks: map[string]string{"allOf.0.type": `["array","null"]`, "allOf.0.items.type": `"integer"`, "type": `"array"`},
		},
		{
			name: "overlapping_object_types", target: `{"type":["object","string"],"properties":{"a":{"type":"integer"}}}`, sibling: `"type":"string"`,
			checks: map[string]string{"allOf.0.type": `["object","string"]`, "allOf.0.properties.a.type": `"integer"`, "type": `"string"`},
		},
		{
			name: "enums", target: `{"type":"string","enum":["a","b"]}`, sibling: `"enum":["b","c"]`,
			checks: map[string]string{"allOf.0.enum": `["a","b"]`, "enum": `["b","c"]`},
		},
		{
			name: "required_without_local_properties", target: `{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"string"}},"required":["a"]}`, sibling: `"required":["b"]`,
			checks: map[string]string{"allOf.0.required": `["a"]`, "required": `["b"]`, "allOf.0.properties.b.type": `"string"`},
		},
		{
			name: "closed_object_scopes", target: `{"type":"object","properties":{"a":{"type":"string"}},"additionalProperties":false}`, sibling: `"properties":{"b":{"type":"string"}},"additionalProperties":false`,
			checks: map[string]string{"allOf.0.properties": `{"a":{"type":"string"}}`, "properties": `{"b":{"type":"string"}}`, "allOf.0.additionalProperties": `false`, "additionalProperties": `false`},
		},
		{
			name: "equal_closed_object_constraints", target: `{"type":"object","properties":{"a":{"type":"string"}},"additionalProperties":false}`, sibling: `"additionalProperties":false`,
			checks: map[string]string{"allOf.0.properties.a.type": `"string"`, "allOf.0.additionalProperties": `false`, "additionalProperties": `false`},
		},
		{
			name: "sibling_closed_object_scope", target: `{"type":"object","properties":{"a":{"type":"string"}}}`, sibling: `"additionalProperties":false`,
			checks: map[string]string{"allOf.0.properties.a.type": `"string"`, "additionalProperties": `false`},
		},
		{
			name: "target_closed_object_scope", target: `{"type":"object","additionalProperties":false}`, sibling: `"properties":{"a":{"type":"string"}}`,
			checks: map[string]string{"allOf.0.additionalProperties": `false`, "properties.a.type": `"string"`},
		},
		{
			name: "equal_contains_constraints", target: `{"type":"array","items":{},"contains":{"type":"integer"},"minContains":0}`, sibling: `"contains":{"type":"integer"}`,
			checks: map[string]string{"allOf.0.minContains": `0`, "allOf.0.contains.type": `"integer"`, "contains.type": `"integer"`},
		},
		{
			name: "contains_count_scope", target: `{"type":"array","items":{},"contains":{"type":"integer"}}`, sibling: `"minContains":0`,
			checks: map[string]string{"allOf.0.contains.type": `"integer"`, "minContains": `0`},
		},
		{
			name: "equal_items_with_different_prefix_scope", target: `{"type":"array","prefixItems":[{"type":"string"}],"items":{"type":"integer"}}`, sibling: `"items":{"type":"integer"}`,
			checks: map[string]string{"allOf.0.prefixItems.0.type": `"string"`, "allOf.0.items.type": `"integer"`, "items.type": `"integer"`},
		},
		{
			name: "existing_allof", target: `{"type":"string","allOf":[{"minLength":5}]}`, sibling: `"allOf":[{"maxLength":8}]`,
			checks: map[string]string{"allOf.0.allOf.0.minLength": `5`, "allOf.1.maxLength": `8`},
		},
	}
	for _, tc := range cases {
		for _, rootRef := range []bool{false, true} {
			name := tc.name + "/nested"
			input := `{"$schema":"https://json-schema.org/draft/2020-12/schema","$defs":{"A":` + tc.target + `},"type":"object","properties":{"x":{"$ref":"#/$defs/A",` + tc.sibling + `}}}`
			path := "properties.x"
			if rootRef {
				name = tc.name + "/root"
				input = `{"$schema":"https://json-schema.org/draft/2020-12/schema","$defs":{"A":` + tc.target + `},"$ref":"#/$defs/A",` + tc.sibling + `}`
				path = ""
			}
			t.Run(name, func(t *testing.T) {
				for cleaner, clean := range map[string]func(string) string{
					"inline": InlineLocalRefs,
					"gemini": CleanJSONSchemaForGeminiJSONSchema,
				} {
					t.Run(cleaner, func(t *testing.T) {
						got := clean(input)
						node := gjson.Parse(got)
						if path != "" {
							node = node.Get(path)
						}
						for key, want := range tc.checks {
							actual := node.Get(key)
							if !actual.Exists() {
								t.Errorf("lost constraint %s = %s: %s", key, want, got)
								continue
							}
							compareJSON(t, `{"value":`+want+`}`, `{"value":`+actual.Raw+`}`)
						}
						compareJSON(t, got, clean(got))
					})
				}
			})
		}
	}
}

func TestIssue6897_RefSiblingAnnotationsAndIdenticalTypes(t *testing.T) {
	input := `{"$defs":{"A":{"type":"string","minLength":5,"description":"base","default":"base","examples":["base"]}},"properties":{"x":{"$ref":"#/$defs/A","type":"string","description":"local","default":"local","examples":[{"$ref":"literal"}]}}}`
	for name, clean := range map[string]func(string) string{
		"inline": InlineLocalRefs,
		"gemini": CleanJSONSchemaForGeminiJSONSchema,
	} {
		t.Run(name, func(t *testing.T) {
			got := clean(input)
			compareJSON(t, `{"type":"string","minLength":5,"description":"local","default":"local","examples":[{"$ref":"literal"}]}`, gjson.Get(got, "properties.x").Raw)
		})
	}
}

func TestIssue6897_WrappedRefConjunction(t *testing.T) {
	input := `{"schema":{"$defs":{"A":{"type":"string","minLength":5}},"$ref":"#/$defs/A","minLength":1}}`
	got := CleanJSONSchemaForGeminiJSONSchema(input)
	compareJSON(t, `{"schema":{"minLength":1,"allOf":[{"type":"string","minLength":5}]}}`, got)
}

func TestIssue6897_RootRefKeepsDocumentDeclarations(t *testing.T) {
	input := `{"$schema":"https://json-schema.org/draft/2020-12/schema","$defs":{"A":{"type":"string","minLength":5}},"$ref":"#/$defs/A","minLength":1}`
	got := InlineLocalRefs(input)
	compareJSON(t, `{"$schema":"https://json-schema.org/draft/2020-12/schema","$defs":{"A":{"type":"string","minLength":5}},"minLength":1,"allOf":[{"type":"string","minLength":5}]}`, got)
}

func TestIssue6897_RequiredOutsideAllOf(t *testing.T) {
	input := `{"type":"object","allOf":[{"properties":{"x":{"type":"string"}}}],"required":["x"]}`
	got := CleanJSONSchemaForGeminiJSONSchema(input)
	if gjson.Get(got, "required.0").String() != "x" {
		t.Fatalf("required property declared in allOf was dropped: %s", got)
	}
}

func TestIssue6897_ArrayConjunctionDoesNotInventItems(t *testing.T) {
	for _, rootRef := range []bool{false, true} {
		name := "nested"
		input := `{"$defs":{"A":{"type":"array","items":{"type":"integer"},"minItems":2}},"type":"object","properties":{"x":{"$ref":"#/$defs/A","type":"array","minItems":1}}}`
		path := "properties.x"
		if rootRef {
			name = "root"
			input = `{"$defs":{"A":{"type":"array","items":{"type":"integer"},"minItems":2}},"$ref":"#/$defs/A","type":"array","minItems":1}`
			path = ""
		}
		t.Run(name, func(t *testing.T) {
			got := CleanJSONSchemaForGeminiJSONSchema(input)
			node := gjson.Parse(got)
			if path != "" {
				node = node.Get(path)
			}
			// [1, 2] is valid: the sibling only constrains array length, not element type.
			compareJSON(t, `{"type":"array","minItems":1,"allOf":[{"type":"array","items":{"type":"integer"},"minItems":2}]}`, node.Raw)
			compareJSON(t, got, CleanJSONSchemaForGeminiJSONSchema(got))
		})
	}
}

func TestIssue6897_ArrayTargetDoesNotInventItemsBeforeInlining(t *testing.T) {
	for _, rootRef := range []bool{false, true} {
		name := "nested"
		input := `{"$defs":{"A":{"type":"array","minItems":2}},"type":"object","properties":{"x":{"$ref":"#/$defs/A","minItems":1,"items":{"type":"integer"}}}}`
		path := "properties.x"
		if rootRef {
			name = "root"
			input = `{"$defs":{"A":{"type":"array","minItems":2}},"$ref":"#/$defs/A","minItems":1,"items":{"type":"integer"}}`
			path = ""
		}
		t.Run(name, func(t *testing.T) {
			got := CleanJSONSchemaForGeminiJSONSchema(input)
			node := gjson.Parse(got)
			if path != "" {
				node = node.Get(path)
			}
			// Only the sibling constrains the elements; [1, 2] must remain valid.
			compareJSON(t, `{"minItems":1,"items":{"type":"integer"},"allOf":[{"type":"array","minItems":2}]}`, node.Raw)
			compareJSON(t, got, CleanJSONSchemaForGeminiJSONSchema(got))
		})
	}
}

func TestIssue6897_ArrayItemsInsideAllOf(t *testing.T) {
	input := `{"type":"array","allOf":[{"items":{"type":"integer"}}]}`
	got := CleanJSONSchemaForGeminiJSONSchema(input)
	if gjson.Get(got, "items").Exists() {
		t.Fatalf("array conjunction gained an unrelated element constraint: %s", got)
	}
	if gjson.Get(got, "allOf.0.items.type").String() != "integer" {
		t.Fatalf("integer element constraint was lost: %s", got)
	}
}

func TestIssue6897_UnionCannotReplaceConjunction(t *testing.T) {
	input := `{"type":"object","allOf":[{"properties":{"x":{"type":"string"}},"required":["x"]}],"anyOf":[{"type":"object"}]}`
	got := CleanJSONSchemaForGeminiJSONSchema(input)
	// {} must remain invalid, even though it satisfies the anyOf branch.
	if gjson.Get(got, "allOf.0.properties.x.type").String() != "string" || gjson.Get(got, "allOf.0.required.0").String() != "x" {
		t.Fatalf("union cleanup removed the required-property conjunction: %s", got)
	}
	compareJSON(t, got, CleanJSONSchemaForGeminiJSONSchema(got))
}

func TestIssue6897_ConjunctionPreservesRequiredOnlyUnion(t *testing.T) {
	input := `{"type":"object","allOf":[{"properties":{"a":{"type":"string"},"b":{"type":"string"}}},{"anyOf":[{"required":["a"]},{"required":["b"]}]}]}`
	got := CleanJSONSchemaForGeminiJSONSchema(input)
	// {} must fail both alternatives; either declared key can satisfy the union.
	if gjson.Get(got, "allOf.1.anyOf.0.required.0").String() != "a" || gjson.Get(got, "allOf.1.anyOf.1.required.0").String() != "b" {
		t.Fatalf("required-only union lost constraints declared in another conjunction branch: %s", got)
	}
	compareJSON(t, got, CleanJSONSchemaForGeminiJSONSchema(got))
}

func TestIssue6897_RefSiblingRequiredOnlyUnion(t *testing.T) {
	input := `{"$defs":{"A":{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"string"}},"minProperties":1}},"type":"object","properties":{"x":{"$ref":"#/$defs/A","minProperties":0,"anyOf":[{"required":["a"]},{"required":["b"]}]}}}`
	got := CleanJSONSchemaForGeminiJSONSchema(input)
	// {"x":{"z":1}} must remain invalid: at least a or b is required.
	if gjson.Get(got, "properties.x.anyOf.0.required.0").String() != "a" || gjson.Get(got, "properties.x.anyOf.1.required.0").String() != "b" {
		t.Fatalf("sibling union required constraints were removed: %s", got)
	}
	compareJSON(t, got, CleanJSONSchemaForGeminiJSONSchema(got))
}

func TestIssue6897_ConjunctionDoesNotInferContainerType(t *testing.T) {
	for name, sibling := range map[string]string{
		"items":      `"items":{"type":"integer"}`,
		"properties": `"properties":{"ignored":{"type":"integer"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			input := `{"$defs":{"A":{"type":"number","minimum":5}},"type":"object","properties":{"x":{"$ref":"#/$defs/A","minimum":1,` + sibling + `}}}`
			got := CleanJSONSchemaForGeminiJSONSchema(input)
			node := gjson.Get(got, "properties.x")
			// 5 is valid; items/properties do not constrain non-container instances.
			if node.Get("type").Exists() || node.Get("allOf.0.type").String() != "number" {
				t.Fatalf("container inference made a valid numeric conjunction impossible: %s", got)
			}
			compareJSON(t, got, CleanJSONSchemaForGeminiJSONSchema(got))
		})
	}
}

func TestIssue6897_OuterUnionPreservesConjunction(t *testing.T) {
	cases := []struct {
		name  string
		input string
		path  string
		want  string
	}{
		{
			name:  "existing_conjunction",
			input: `{"type":"object","properties":{"x":{"type":"string"}},"anyOf":[{"allOf":[{"properties":{"x":{"minLength":5}}}]}]}`,
			path:  "anyOf.0.allOf.0.properties.x.minLength", want: "5",
		},
		{
			name:  "reference_conjunction",
			input: `{"$defs":{"A":{"type":"object","properties":{"x":{"minLength":5}},"minProperties":1}},"type":"object","properties":{"x":{"type":"string"}},"anyOf":[{"$ref":"#/$defs/A","minProperties":0}]}`,
			path:  "anyOf.0.allOf.0.properties.x.minLength", want: "5",
		},
		{
			name:  "ancestor_required",
			input: `{"required":["x"],"anyOf":[{"allOf":[{"properties":{"x":{"type":"string"}}}]}]}`,
			path:  "required.0", want: "x",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CleanJSONSchemaForGeminiJSONSchema(tc.input)
			if gjson.Get(got, tc.path).String() != tc.want {
				t.Fatalf("outer union lost %s = %s: %s", tc.path, tc.want, got)
			}
			compareJSON(t, got, CleanJSONSchemaForGeminiJSONSchema(got))
		})
	}
}

func TestIssue6897_RefSiblingUnevaluatedScope(t *testing.T) {
	cases := []struct {
		name    string
		target  string
		sibling string
		want    string
	}{
		{
			name:    "target_properties_scope",
			target:  `{"type":"object","unevaluatedProperties":false}`,
			sibling: `"properties":{"x":{"type":"string"}}`,
			want:    `{"properties":{"x":{"type":"string"}},"allOf":[{"type":"object","unevaluatedProperties":false}]}`,
		},
		{
			name:    "target_items_scope",
			target:  `{"type":"array","unevaluatedItems":false}`,
			sibling: `"prefixItems":[{"type":"integer"}]`,
			want:    `{"prefixItems":[{"type":"integer"}],"allOf":[{"type":"array","unevaluatedItems":false}]}`,
		},
		{
			name:    "properties",
			target:  `{"type":"object","properties":{"x":{"type":"string"}},"minProperties":1}`,
			sibling: `"minProperties":0,"unevaluatedProperties":false`,
			want:    `{"minProperties":0,"unevaluatedProperties":false,"allOf":[{"type":"object","properties":{"x":{"type":"string"}},"minProperties":1}]}`,
		},
		{
			name:    "items",
			target:  `{"type":"array","prefixItems":[{"type":"integer"}],"minItems":1}`,
			sibling: `"minItems":0,"unevaluatedItems":false`,
			want:    `{"minItems":0,"unevaluatedItems":false,"allOf":[{"type":"array","prefixItems":[{"type":"integer"}],"minItems":1}]}`,
		},
	}
	for _, tc := range cases {
		for _, rootRef := range []bool{false, true} {
			name := tc.name + "/nested"
			input := `{"$defs":{"A":` + tc.target + `},"properties":{"value":{"$ref":"#/$defs/A",` + tc.sibling + `}}}`
			path := "properties.value"
			if rootRef {
				name = tc.name + "/root"
				input = `{"$defs":{"A":` + tc.target + `},"$ref":"#/$defs/A",` + tc.sibling + `}`
				path = ""
			}
			t.Run(name, func(t *testing.T) {
				got := InlineLocalRefs(input)
				node := gjson.Parse(got)
				if path != "" {
					node = node.Get(path)
				} else {
					// Root declarations are not part of the validation assertions under test.
					root := node.Value().(map[string]any)
					delete(root, "$defs")
					encoded, errMarshal := json.Marshal(root)
					if errMarshal != nil {
						t.Fatal(errMarshal)
					}
					node = gjson.ParseBytes(encoded)
				}
				// Unevaluated keywords must stay adjacent to the applicator so they see its
				// annotations: {"x":"ok"} and [1] remain valid, unlike extra keys or items.
				compareJSON(t, tc.want, node.Raw)
			})
		}
	}
}

func TestIssue6897_RefSiblingMinLength(t *testing.T) {
	input := `{"$schema":"https://json-schema.org/draft/2020-12/schema","$defs":{"A":{"type":"string","minLength":5}},"type":"object","properties":{"x":{"$ref":"#/$defs/A","minLength":1}}}`
	got := CleanJSONSchemaForGeminiJSONSchema(input)
	t.Logf("cleaned=%s", got)

	// Both constraints apply: "a" must remain invalid and "abcde" must remain valid.
	var minLength func(gjson.Result) int64
	minLength = func(schema gjson.Result) int64 {
		minimum := schema.Get("minLength").Int()
		for _, branch := range schema.Get("allOf").Array() {
			minimum = max(minimum, minLength(branch))
		}
		return minimum
	}
	if minimum := minLength(gjson.Get(got, "properties.x")); minimum != 5 {
		t.Fatalf("effective minLength = %d, want 5; reference target constraint was lost", minimum)
	}
}
