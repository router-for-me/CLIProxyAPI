package executor

import (
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"

	antigravityclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/antigravity/claude"
	antigravitychat "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/antigravity/openai/chat-completions"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/tidwall/gjson"
)

const sanitizeTestPayload = `{
  "request": {
    "contents": [
      {"role": "model", "parts": [{"functionCall": {"name": "manage_todo_list", "args": {
        "operation": "write",
        "todoList": [
          {"id": 1, "title": "output 1", "description": "d1", "status": "not-started"},
          {"id": 2, "title": "output 2", "description": "d2", "status": "not-started"}
        ]}}}]},
      {"role": "model", "parts": [{"functionCall": {"name": "write_file", "args": {
        "path": "a.md", "format": "markdown", "default": "x", "pattern": "p",
        "const": "c", "deprecated": false, "nullable": "n", "examples": "e",
        "additionalProperties": "ap", "x-custom": "keepme"
      }}}]}
    ],
    "tools": [{"functionDeclarations": [{
      "name": "manage_todo_list",
      "parametersJsonSchema": {
        "type": "object",
        "required": ["todoList"],
        "properties": {"todoList": {"type": "array", "items": {
          "type": "object",
          "required": ["id", "title"],
          "title": "TodoItem",
          "properties": {"id": {"type": "number"}, "title": {"type": "string", "minLength": 3}}
        }}}
      }
    }]}]
  }
}`

// TestSanitizeAntigravityRequestSchemasPreservesHistory guards against the schema cleaner being
// applied to the whole payload, which silently stripped keys such as "title" from functionCall
// arguments replayed from conversation history.
func TestSanitizeAntigravityRequestSchemasPreservesHistory(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		useAntigravitySchema bool
	}{
		{"gemini", false},
		{"antigravity", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeAntigravityRequestSchemas(sanitizeTestPayload, tc.useAntigravitySchema)

			before := gjson.Get(sanitizeTestPayload, "request.contents")
			after := gjson.Get(got, "request.contents")
			if before.Raw != after.Raw {
				t.Errorf("conversation history was mutated.\nbefore: %s\nafter:  %s", before.Raw, after.Raw)
			}

			todo := gjson.Get(got, `request.contents.0.parts.0.functionCall.args.todoList`)
			for i, item := range todo.Array() {
				if !item.Get("title").Exists() {
					t.Errorf("todoList[%d] lost its title: %s", i, item.Raw)
				}
			}

			args := gjson.Get(got, `request.contents.1.parts.0.functionCall.args`)
			for _, key := range []string{"format", "default", "pattern", "const", "deprecated", "examples", "additionalProperties", "x-custom"} {
				if !args.Get(gjson.Escape(key)).Exists() {
					t.Errorf("argument key %q was stripped from history: %s", key, args.Raw)
				}
			}
			if args.Get("enum").Exists() {
				t.Errorf("cleaner fabricated an enum key in history args: %s", args.Raw)
			}
		})
	}
}

// TestSanitizeAntigravityRequestSchemasStillCleansSchemas verifies the schema itself is still
// renamed and cleaned, so scoping the cleaner did not disable it.
func TestSanitizeAntigravityRequestSchemasStillCleansSchemas(t *testing.T) {
	got := sanitizeAntigravityRequestSchemas(sanitizeTestPayload, false)

	decl := "request.tools.0.functionDeclarations.0"
	if gjson.Get(got, decl+".parametersJsonSchema").Exists() {
		t.Errorf("parametersJsonSchema was not renamed: %s", gjson.Get(got, decl).Raw)
	}
	schema := gjson.Get(got, decl+".parameters")
	if !schema.Exists() {
		t.Fatalf("parameters missing after sanitization: %s", gjson.Get(got, decl).Raw)
	}

	items := schema.Get("properties.todoList.items")
	if items.Get("title").Exists() {
		t.Errorf("schema keyword title was not removed: %s", items.Raw)
	}
	if items.Get("properties.title.minLength").Exists() {
		t.Errorf("unsupported keyword minLength was not removed: %s", items.Raw)
	}
	if !items.Get("properties.title").Exists() {
		t.Errorf("schema property named title must be preserved: %s", items.Raw)
	}
	if req := items.Get("required").Array(); len(req) != 2 {
		t.Errorf("required list should keep id and title, got: %s", items.Get("required").Raw)
	}
}

// TestSanitizeAntigravityRequestSchemasCleansResultSchemas covers the schemas a function
// declaration can carry besides its parameters. Missing one sends it upstream uncleaned.
func TestSanitizeAntigravityRequestSchemasCleansResultSchemas(t *testing.T) {
	payload := `{"request": {"tools": [{"functionDeclarations": [{
	  "name": "t",
	  "parameters": {"type": "object", "$id": "drop-a", "properties": {"a": {"type": "string"}}},
	  "response": {"type": "object", "$comment": "drop-b", "properties": {"b": {"type": "string"}}},
	  "responseJsonSchema": {"type": "object", "$id": "drop-c", "properties": {"c": {"type": "string"}}}
	}]}]}}`

	got := sanitizeAntigravityRequestSchemas(payload, false)
	decl := gjson.Get(got, "request.tools.0.functionDeclarations.0")

	for _, unsupported := range []string{`parameters.\$id`, `response.\$comment`, `responseJsonSchema.\$id`} {
		if decl.Get(unsupported).Exists() {
			t.Errorf("unsupported keyword %s survived cleaning: %s", unsupported, decl.Raw)
		}
	}
	for _, kept := range []string{"parameters.properties.a", "response.properties.b", "responseJsonSchema.properties.c"} {
		if !decl.Get(kept).Exists() {
			t.Errorf("%s should be preserved: %s", kept, decl.Raw)
		}
	}
}

// TestAntigravitySchemaPathsCoverEverySchemaLocation pins the set of payload locations that get
// cleaned. Scoping the cleaner traded "clean everything" for an explicit list, so a schema at a
// location missing from that list now reaches upstream uncleaned and is rejected — four such gaps
// were found this way, one per location that had been overlooked.
//
// The declaration keys must stay in step with allowedToolKeys in
// internal/translator/antigravity/claude/antigravity_claude_request.go, which is the authoritative
// list of what a function declaration may carry. Add a schema-bearing key there and it must be
// added here too; this test only fails once the key is listed below, so treat the pairing as
// something to check whenever that list changes.
func TestAntigravitySchemaPathsCoverEverySchemaLocation(t *testing.T) {
	const schema = `{"type":"object","$id":"drop","properties":{"a":{"type":"string"}}}`

	// Both spellings of the declarations container are exercised: the Gemini translator forwards
	// snake_case untouched, so covering only camelCase leaves those requests uncleaned.
	for _, declContainer := range []string{"functionDeclarations", "function_declarations"} {
		for _, genContainer := range antigravityGenerationConfigContainers {
			t.Run(declContainer+"_"+strings.TrimPrefix(genContainer, "request."), func(t *testing.T) {
				decl := `"name":"t"`
				for _, k := range antigravityDeclarationSchemaKeys {
					decl += `,"` + k + `":` + schema
				}
				gen := ""
				for i, k := range antigravityGenerationSchemaKeys {
					if i > 0 {
						gen += ","
					}
					gen += `"` + k + `":` + schema
				}
				payload := `{"request":{"tools":[{"` + declContainer + `":[{` + decl + `}]}],"` +
					strings.TrimPrefix(genContainer, "request.") + `":{` + gen + `}}}`

				if !antigravityRequestNeedsSchemaSanitization([]byte(payload)) {
					t.Fatal("sanitization must trigger for a payload carrying schemas")
				}
				got := sanitizeAntigravityRequestSchemas(payload, false)

				check := func(path string) {
					t.Helper()
					node := gjson.Get(got, path)
					if !node.Exists() {
						t.Errorf("%s disappeared: %s", path, got)
						return
					}
					if node.Get(`\$id`).Exists() {
						t.Errorf("%s was never cleaned, $id reaches upstream: %s", path, node.Raw)
					}
				}
				base := "request.tools.0." + declContainer + ".0."
				for _, k := range antigravityDeclarationSchemaKeys {
					// Only the camelCase alias is renamed onto parameters, matching whole-payload
					// cleaning. Every other spelling is cleaned where the client put it.
					if k == "parametersJsonSchema" {
						if gjson.Get(got, base+k).Exists() {
							t.Errorf("%s should have been renamed onto parameters: %s", k, got)
						}
						continue
					}
					check(base + k)
				}
				for _, k := range antigravityGenerationSchemaKeys {
					check(genContainer + "." + k)
				}
			})
		}
	}
}

// TestSanitizeAntigravityRequestSchemasMatchesWholePayloadCleaning pins the emitted schema to what
// whole-payload cleaning produced. Narrowing the scope must change which nodes are cleaned, never
// the result for a schema node — in particular the Claude VALIDATED placeholder, which the cleaner
// only adds when the schema is not top-level.
func TestSanitizeAntigravityRequestSchemasMatchesWholePayloadCleaning(t *testing.T) {
	shapes := map[string]string{
		"optionalOnly": `{"type":"object","properties":{"flag":{"type":"string"}}}`,
		"emptyProps":   `{"type":"object","properties":{}}`,
		"noProps":      `{"type":"object"}`,
		"withRequired": `{"type":"object","required":["a"],"properties":{"a":{"type":"string","minLength":2}}}`,
		"nestedArray":  `{"type":"object","properties":{"list":{"type":"array","items":{"type":"object","title":"X","required":["id","title"],"properties":{"id":{"type":"number"},"title":{"type":"string"}}}}}}`,
		"enumAndRemoved": `{"type":"object","$comment":"c","properties":{"m":{"type":"string","enum":["a","b"],` +
			`"deprecated":true}}}`,
	}
	const schemaPath = "request.tools.0.functionDeclarations.0.parameters"

	for _, useAntigravitySchema := range []bool{false, true} {
		for name, schema := range shapes {
			doc := `{"request":{"tools":[{"functionDeclarations":[{"name":"t","parameters":` + schema + `}]}]}}`
			whole := util.CleanJSONSchemaForAntigravityTool(doc, useAntigravitySchema)
			want := gjson.Get(whole, schemaPath).Raw
			got := gjson.Get(sanitizeAntigravityRequestSchemas(doc, useAntigravitySchema), schemaPath).Raw
			if want != got {
				t.Errorf("%s (antigravity=%v) diverged from whole-payload cleaning.\nwant: %s\ngot:  %s",
					name, useAntigravitySchema, want, got)
			}
		}
	}

	// Explicitly pin the placeholder, so the equivalence above cannot pass by both sides dropping it.
	doc := `{"request":{"tools":[{"functionDeclarations":[{"name":"t","parameters":` + shapes["optionalOnly"] + `}]}]}}`
	got := gjson.Get(sanitizeAntigravityRequestSchemas(doc, true), schemaPath)
	if req := got.Get("required").Array(); len(req) != 1 || req[0].String() != "_" {
		t.Errorf("Claude VALIDATED placeholder missing for an optional-only schema: %s", got.Raw)
	}
}

func TestSanitizeAntigravityRequestSchemasKeepsResponseSchemasPlaceholderFree(t *testing.T) {
	payload := `{"request":{
		"tools":[{"functionDeclarations":[{"name":"tool","parameters":{"type":"object","properties":{"value":{"type":"string"}}}}]}],
		"generationConfig":{"responseSchema":{"type":"object","properties":{
			"empty":{"type":"object"},
			"optional":{"type":"object","properties":{"value":{"type":"string"}}}
		}}}
	}}`

	got := sanitizeAntigravityRequestSchemas(payload, true)
	toolSchema := gjson.Get(got, "request.tools.0.functionDeclarations.0.parameters")
	if required := toolSchema.Get("required.0").String(); required != "_" {
		t.Fatalf("tool schema lost VALIDATED placeholder, required[0] = %q: %s", required, got)
	}

	responseSchema := gjson.Get(got, "request.generationConfig.responseSchema")
	for _, path := range []string{
		"required",
		"properties._",
		"properties.reason",
		"properties.empty.required",
		"properties.empty.properties.reason",
		"properties.optional.required",
		"properties.optional.properties._",
	} {
		if responseSchema.Get(path).Exists() {
			t.Errorf("response schema gained tool-only field %s: %s", path, responseSchema.Raw)
		}
	}
}

func TestSanitizeAntigravityRequestSchemasProjectsUnionsAndPreservesEnumTypes(t *testing.T) {
	payload := `{"request":{
		"tools":[{"functionDeclarations":[{"name":"tool","parameters":{"type":"object","properties":{
			"choice":{"anyOf":[{"type":"string"},{"type":"null"}]},
			"item":{"anyOf":[
				{"type":"string"},
				{"type":"object","properties":{"image":{"type":"string"}},"required":["image"]},
				{"type":"object","properties":{"audio":{"type":"string"}},"required":["audio"]}
			]},
			"variant":{"oneOf":[
				{"type":"object","properties":{"image":{"type":"string"}},"required":["image"]},
				{"type":"object","properties":{"audio":{"type":"string"}},"required":["audio"]}
			]},
			"level":{"type":"number","enum":[1,2]}
		}}}]}],
		"generationConfig":{"responseSchema":{"type":"object","properties":{
			"action":{"anyOf":[
				{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]},
				{"type":"null"}
			]},
			"conviction":{"type":"number","enum":[0.25,0.5,1]}
		}}}
	}}`

	got := sanitizeAntigravityRequestSchemas(payload, true)
	responseSchema := gjson.Get(got, "request.generationConfig.responseSchema")
	action := responseSchema.Get("properties.action")
	if action.Get("anyOf").Exists() || action.Get("type").String() != "object" || !action.Get("nullable").Bool() {
		t.Errorf("response anyOf was not projected to nullable object: %s", responseSchema.Raw)
	}
	conviction := responseSchema.Get("properties.conviction")
	if gotType := conviction.Get("type").String(); gotType != "number" {
		t.Errorf("response enum type = %q, want number: %s", gotType, responseSchema.Raw)
	}
	for _, enumValue := range conviction.Get("enum").Array() {
		if enumValue.Type != gjson.String {
			t.Errorf("response enum value is not a string: %s", conviction.Raw)
		}
	}

	toolSchema := gjson.Get(got, "request.tools.0.functionDeclarations.0.parameters")
	if toolSchema.Get("properties.choice.anyOf").Exists() {
		t.Errorf("tool nullable single-branch anyOf union was not flattened: %s", toolSchema.Raw)
	}
	if gotBranches := len(toolSchema.Get("properties.item.anyOf").Array()); gotBranches != 3 {
		t.Errorf("tool multi-branch anyOf count = %d, want 3: %s", gotBranches, toolSchema.Raw)
	}
	if !toolSchema.Get("properties.item.anyOf.2.properties.audio").Exists() {
		t.Errorf("tool multi-branch anyOf lost audio branch: %s", toolSchema.Raw)
	}
	if toolSchema.Get("properties.variant.oneOf").Exists() {
		t.Errorf("tool multi-branch oneOf should be converted to anyOf for parameters Schema carrier: %s", toolSchema.Raw)
	}
	if gotVariantBranches := len(toolSchema.Get("properties.variant.anyOf").Array()); gotVariantBranches != 2 {
		t.Errorf("tool multi-branch variant.anyOf count = %d, want 2: %s", gotVariantBranches, toolSchema.Raw)
	}
	if !toolSchema.Get("properties.variant.anyOf.1.properties.audio").Exists() {
		t.Errorf("tool multi-branch variant.anyOf lost audio branch: %s", toolSchema.Raw)
	}
	if gotType := toolSchema.Get("properties.level.type").String(); gotType != "number" {
		t.Errorf("tool enum type = %q, want number: %s", gotType, toolSchema.Raw)
	}
}

func TestSanitizeAntigravityToolSchemasKeepNativeTypeAndNullableOnBothPaths(t *testing.T) {
	payload := `{"request":{"tools":[{"functionDeclarations":[{"name":"tool","parameters":{
		"type":"object",
		"properties":{
			"level":{"type":"number","enum":[1,2]},
			"note":{"type":["string","null"]}
		},
		"required":["level","note"]
	}}]}]}}`

	for _, requirePlaceholder := range []bool{false, true} {
		got := sanitizeAntigravityRequestSchemas(payload, requirePlaceholder)
		schema := gjson.Get(got, "request.tools.0.functionDeclarations.0.parameters")
		if schema.Get("properties.level.type").String() != "number" {
			t.Fatalf("placeholder=%v changed numeric tool argument type: %s", requirePlaceholder, schema.Raw)
		}
		for _, member := range schema.Get("properties.level.enum").Array() {
			if member.Type != gjson.String {
				t.Fatalf("placeholder=%v left non-string proto enum: %s", requirePlaceholder, schema.Raw)
			}
		}
		if !schema.Get("properties.note.nullable").Bool() || schema.Get("required.1").String() != "note" {
			t.Fatalf("placeholder=%v lost native nullable/required semantics: %s", requirePlaceholder, schema.Raw)
		}
	}
}

func TestAntigravityBuildRequestKeepsJSONObjectMimeOnly(t *testing.T) {
	input := []byte(`{"model":"gemini-3.1-pro-low","messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_object"}}`)
	translated, _ := antigravitychat.ConvertOpenAIRequestToAntigravity("gemini-3.1-pro-low", input, false)
	body := buildRequestBodyFromRawPayload(t, "gemini-3.1-pro-low", translated)
	encoded, errMarshal := json.Marshal(body)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}

	generationConfig := gjson.GetBytes(encoded, "request.generationConfig")
	if got := generationConfig.Get("responseMimeType").String(); got != "application/json" {
		t.Fatalf("responseMimeType = %q, want application/json: %s", got, encoded)
	}
	if generationConfig.Get("responseSchema").Exists() {
		t.Fatalf("responseSchema should not be set for json_object: %s", encoded)
	}
}

func TestAntigravityBuildRequestPreservesGenerationResponseSchemaMetadata(t *testing.T) {
	payload := []byte(`{"request":{"generationConfig":{"responseSchema":{
		"type":"object",
		"nullable":true,
		"properties":{"_":{"type":"string","nullable":true}},
		"required":["_"]
	}}}}`)

	for _, modelName := range []string{"gemini-3.6-flash-high", "gemini-3.1-pro-low"} {
		t.Run(modelName, func(t *testing.T) {
			body := buildRequestBodyFromRawPayload(t, modelName, payload)
			encoded, errMarshal := json.Marshal(body)
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}

			schema := gjson.GetBytes(encoded, "request.generationConfig.responseSchema")
			if !schema.Get("nullable").Bool() || !schema.Get("properties._.nullable").Bool() {
				t.Fatalf("response schema nullable metadata was removed: %s", schema.Raw)
			}
			if !schema.Get("properties._").Exists() {
				t.Fatalf("legitimate underscore property was removed: %s", schema.Raw)
			}
			if required := schema.Get("required.0").String(); required != "_" {
				t.Fatalf("required[0] = %q, want underscore: %s", required, schema.Raw)
			}
		})
	}
}

func TestAntigravityBuildRequestSanitizesSnakeCaseGenerationResponseSchemas(t *testing.T) {
	for _, testCase := range []struct {
		alias     string
		canonical string
	}{
		{alias: "response_schema", canonical: "responseSchema"},
		{alias: "response_json_schema", canonical: "responseJsonSchema"},
	} {
		t.Run(testCase.alias, func(t *testing.T) {
			input := []byte(`{"model":"gemini-3.6-flash-high","messages":[{"role":"user","content":"hi"}],"generation_config":{"` + testCase.alias + `":{"type":"object","$id":"drop-me","properties":{"title":{"type":"string"}}}}}`)
			translated, _ := antigravitychat.ConvertOpenAIRequestToAntigravity("gemini-3.6-flash-high", input, false)
			body := buildRequestBodyFromRawPayload(t, "gemini-3.6-flash-high", translated)
			encoded, errMarshal := json.Marshal(body)
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}

			base := "request.generationConfig."
			// The upstream API accepts either spelling, so the field must stay where the client put
			// it. Only the unsupported keywords inside it are what upstream rejects.
			schema := gjson.GetBytes(encoded, base+testCase.alias)
			if !schema.Exists() {
				t.Fatalf("snake_case response schema was renamed or dropped: %s", encoded)
			}
			if gjson.GetBytes(encoded, base+testCase.canonical).Exists() {
				t.Fatalf("cleaning must not add a second spelling: %s", encoded)
			}
			if schema.Get(`\$id`).Exists() {
				t.Fatalf("unsupported $id survived cleaning: %s", schema.Raw)
			}
			if !schema.Get("properties.title").Exists() {
				t.Fatalf("schema property named title was removed: %s", schema.Raw)
			}
		})
	}
}

// TestSanitizeAntigravityRequestSchemasCleansBothSpellingsInPlace covers a payload carrying both
// spellings: each is cleaned where it sits, and neither is silently dropped.
func TestSanitizeAntigravityRequestSchemasCleansBothSpellingsInPlace(t *testing.T) {
	payload := `{"request":{"generationConfig":{` +
		`"responseSchema":{"type":"object","$id":"drop-a","properties":{"canonical":{"type":"string"}}},` +
		`"response_schema":{"type":"object","$id":"drop-b","properties":{"alias":{"type":"string"}}}}}}`

	got := sanitizeAntigravityRequestSchemas(payload, false)

	for path, prop := range map[string]string{
		"request.generationConfig.responseSchema":  "canonical",
		"request.generationConfig.response_schema": "alias",
	} {
		schema := gjson.Get(got, path)
		if !schema.Exists() {
			t.Errorf("%s was dropped: %s", path, got)
			continue
		}
		if schema.Get(`\$id`).Exists() {
			t.Errorf("%s kept unsupported $id: %s", path, schema.Raw)
		}
		if !schema.Get("properties." + prop).Exists() {
			t.Errorf("%s lost its property: %s", path, schema.Raw)
		}
	}
}

// TestSanitizeAntigravityRequestSchemasIsIdempotent guards the hint duplication seen in
// production, where a schema cleaned by a translator was cleaned again by this executor.
func TestSanitizeAntigravityRequestSchemasIsIdempotent(t *testing.T) {
	// "withDesc" already has a description, so the hint is parenthesised; "bare" has none, so the
	// hint is stored on its own. Both spellings must survive a second cleaning pass unchanged.
	// "compound" has no description and two hints, so the first pass stores the enum hint bare and
	// appends the constraint after it — the second pass must recognise that leading bare form.
	payload := `{"request": {"tools": [{"functionDeclarations": [{
	  "name": "manage_todo_list",
	  "parameters": {"type": "object", "properties": {
	    "withDesc": {"type": "string", "enum": ["write", "read"], "description": "pick one"},
	    "bare": {"type": "string", "enum": ["not-started", "in-progress", "completed"]},
	    "compound": {"type": "string", "enum": ["a", "b"], "minLength": 1}}}
	}]}]}}`

	once := sanitizeAntigravityRequestSchemas(payload, false)
	twice := sanitizeAntigravityRequestSchemas(once, false)

	base := "request.tools.0.functionDeclarations.0.parameters.properties."
	for _, prop := range []string{"withDesc", "bare", "compound"} {
		descPath := base + prop + ".description"
		first, second := gjson.Get(once, descPath).String(), gjson.Get(twice, descPath).String()
		if first != second {
			t.Errorf("%s: cleaning is not idempotent.\nonce:  %s\ntwice: %s", prop, first, second)
		}
		if strings.Count(second, "Allowed:") != 1 {
			t.Errorf("%s: hint duplicated: %s", prop, second)
		}
	}
}

// propertyNamesShapes are the two nestings reported against the private Gemini backend, which
// rejects the standard JSON Schema keyword "propertyNames" with an unknown-field 400.
var propertyNamesShapes = map[string]string{
	// An object nested in an array item.
	"arrayItem": `{"type":"object","properties":{"records":{"type":"array","items":{"type":"object",` +
		`"properties":{"name":{"type":"string"}},"propertyNames":{"type":"string"}}}}}`,
	// A dynamic map declared by a property that is itself named "properties".
	"propertyNamedProperties": `{"type":"object","properties":{"properties":{"type":"object",` +
		`"propertyNames":{"type":"string"}}}}`,
}

// TestSanitizeAntigravityRequestSchemasStripsPropertyNamesEverywhere covers every payload location
// that can carry a schema, in both spellings of the declarations container. A location that keeps
// "propertyNames" sends a request the backend rejects before inference.
func TestSanitizeAntigravityRequestSchemasStripsPropertyNamesEverywhere(t *testing.T) {
	for shapeName, schema := range propertyNamesShapes {
		for _, declContainer := range []string{"functionDeclarations", "function_declarations"} {
			for _, genContainer := range antigravityGenerationConfigContainers {
				name := shapeName + "_" + declContainer + "_" + strings.TrimPrefix(genContainer, "request.")
				t.Run(name, func(t *testing.T) {
					decl := `"name":"t"`
					for _, k := range antigravityDeclarationSchemaKeys {
						decl += `,"` + k + `":` + schema
					}
					gen := ""
					for i, k := range antigravityGenerationSchemaKeys {
						if i > 0 {
							gen += ","
						}
						gen += `"` + k + `":` + schema
					}
					payload := `{"request":{"tools":[{"` + declContainer + `":[{` + decl + `}]}],"` +
						strings.TrimPrefix(genContainer, "request.") + `":{` + gen + `}}}`

					for _, useAntigravitySchema := range []bool{false, true} {
						got := sanitizeAntigravityRequestSchemas(payload, useAntigravitySchema)
						if strings.Contains(got, `"propertyNames"`) {
							t.Errorf("antigravity=%v: propertyNames reaches upstream: %s", useAntigravitySchema, got)
						}
					}
				})
			}
		}
	}
}

// TestSanitizeAntigravityRequestSchemasKeepsPropertyNamesInHistory pins the boundary of the fix:
// only schema locations may be rewritten. A functionCall argument or a property named
// "propertyNames" is data and must survive untouched.
func TestSanitizeAntigravityRequestSchemasKeepsPropertyNamesInHistory(t *testing.T) {
	payload := `{"request":{
		"contents":[{"role":"model","parts":[{"functionCall":{"name":"t","args":{
			"propertyNames":"keep-me",
			"properties":{"propertyNames":"keep-me-too"}
		}}}]}],
		"tools":[{"functionDeclarations":[{"name":"t","parameters":{"type":"object","properties":{
			"propertyNames":{"type":"string"},
			"properties":{"type":"object","propertyNames":{"type":"string"}}
		}}}]}]
	}}`

	for _, useAntigravitySchema := range []bool{false, true} {
		got := sanitizeAntigravityRequestSchemas(payload, useAntigravitySchema)

		before := gjson.Get(payload, "request.contents")
		after := gjson.Get(got, "request.contents")
		if before.Raw != after.Raw {
			t.Errorf("antigravity=%v: history was mutated.\nbefore: %s\nafter:  %s", useAntigravitySchema, before.Raw, after.Raw)
		}

		schema := gjson.Get(got, "request.tools.0.functionDeclarations.0.parameters")
		if !schema.Get("properties.propertyNames").Exists() {
			t.Errorf("antigravity=%v: property named propertyNames was removed: %s", useAntigravitySchema, schema.Raw)
		}
		if schema.Get("properties.properties.propertyNames").Exists() {
			t.Errorf("antigravity=%v: propertyNames keyword survived inside a property named properties: %s", useAntigravitySchema, schema.Raw)
		}
	}
}

// TestAntigravityBuildRequestStripsPropertyNamesFromOutboundBody asserts on the body that actually
// leaves the executor, so a later transformation cannot reintroduce the keyword unnoticed.
func TestAntigravityBuildRequestStripsPropertyNamesFromOutboundBody(t *testing.T) {
	for shapeName, schema := range propertyNamesShapes {
		for _, modelName := range []string{"gemini-3.1-pro", "claude-opus-4-6"} {
			t.Run(shapeName+"_"+modelName, func(t *testing.T) {
				payload := []byte(`{"request":{
					"contents":[{"role":"model","parts":[{"functionCall":{"name":"t","args":{"propertyNames":"keep-me"}}}]}],
					"tools":[{"function_declarations":[{"name":"t","parametersJsonSchema":` + schema + `}]}],
					"generationConfig":{"responseSchema":` + schema + `}
				}}`)

				body := buildRequestBodyFromRawPayload(t, modelName, payload)
				encoded, errMarshal := json.Marshal(body)
				if errMarshal != nil {
					t.Fatal(errMarshal)
				}

				for _, path := range []string{"request.tools", "request.generationConfig"} {
					if node := gjson.GetBytes(encoded, path); strings.Contains(node.Raw, `"propertyNames"`) {
						t.Errorf("%s still carries propertyNames: %s", path, node.Raw)
					}
				}
				args := gjson.GetBytes(encoded, "request.contents.0.parts.0.functionCall.args")
				if args.Get("propertyNames").String() != "keep-me" {
					t.Errorf("functionCall argument named propertyNames was rewritten: %s", args.Raw)
				}
			})
		}
	}
}

// TestSanitizeAntigravityRequestSchemasStripsEncryptedMetadata covers Codex client tool parameters
// that carry "encrypted": true or "encrypted": false markers.
func TestSanitizeAntigravityRequestSchemasStripsEncryptedMetadata(t *testing.T) {
	encryptedSchema := `{"type":"object","properties":{"key":{"type":"string","encrypted":true},"timeout":{"type":"integer","encrypted":false}},"required":["key"]}`

	for _, declContainer := range []string{"functionDeclarations", "function_declarations"} {
		payload := `{"request":{"tools":[{"` + declContainer + `":[{"name":"test_tool","parameters":` + encryptedSchema + `}]}]}}`

		for _, useAntigravitySchema := range []bool{false, true} {
			got := sanitizeAntigravityRequestSchemas(payload, useAntigravitySchema)
			if strings.Contains(got, `"encrypted"`) {
				t.Errorf("declContainer=%s antigravity=%v: 'encrypted' marker survived sanitization: %s", declContainer, useAntigravitySchema, got)
			}
			schema := gjson.Get(got, "request.tools.0."+declContainer+".0.parameters")
			if !schema.Get("properties.key.type").Exists() || schema.Get("properties.key.type").String() != "string" {
				t.Errorf("declContainer=%s antigravity=%v: key property was corrupted: %s", declContainer, useAntigravitySchema, schema.Raw)
			}
		}
	}
}

func TestSanitizeAntigravityRequestSchemasNormalizesTrueBooleanSubschemas(t *testing.T) {
	// Issue #3551: RevenueCat MCP and other MCP tools declare boolean `true` subschemas in properties.
	// Upstream Antigravity rejects boolean `true` in Schema objects with HTTP 400.
	payload := `{"request":{
		"tools":[{"functionDeclarations":[{
			"name":"mcp__revenuecat_create_product_store_state_plan",
			"parametersJsonSchema":{
				"type":"object",
				"properties":{
					"screenshot_id":true,
					"filename":true,
					"file_size":true,
					"source_file_checksum":true,
					"disabled":false,
					"metadata":{
						"type":"object",
						"properties":{
							"nested_flag":true
						}
					},
					"tags":{
						"type":"array",
						"items":true
					},
					"variants":{
						"anyOf":[true,{"type":"string"}]
					},
					"large_int":9007199254740993
				},
				"additionalProperties":true
			}
		}]}]
	}}`

	for _, useAntigravitySchema := range []bool{false, true} {
		got := sanitizeAntigravityRequestSchemas(payload, useAntigravitySchema)
		decl := gjson.Get(got, "request.tools.0.functionDeclarations.0")
		params := decl.Get("parameters")
		if !params.Exists() {
			t.Fatalf("parameters missing in sanitized declaration: %s", decl.Raw)
		}

		for _, prop := range []string{"screenshot_id", "filename", "file_size", "source_file_checksum"} {
			val := params.Get("properties." + prop)
			if !val.Exists() {
				t.Errorf("useAntigravitySchema=%v: property %s missing: %s", useAntigravitySchema, prop, params.Raw)
			}
			if val.Type != gjson.JSON || val.Raw != "{}" {
				t.Errorf("useAntigravitySchema=%v: property %s should be {}, got %s", useAntigravitySchema, prop, val.Raw)
			}
		}

		// Check false is preserved
		if val := params.Get("properties.disabled"); val.Type != gjson.False {
			t.Errorf("useAntigravitySchema=%v: disabled should be false, got %s", useAntigravitySchema, val.Raw)
		}

		// Check nested property
		if val := params.Get("properties.metadata.properties.nested_flag"); val.Type != gjson.JSON || val.Raw != "{}" {
			t.Errorf("useAntigravitySchema=%v: nested_flag should be {}, got %s", useAntigravitySchema, val.Raw)
		}

		// Check items: true -> items: {}
		if val := params.Get("properties.tags.items"); val.Type != gjson.JSON || val.Raw != "{}" {
			t.Errorf("useAntigravitySchema=%v: tags.items should be {}, got %s", useAntigravitySchema, val.Raw)
		}

		// Check large int preserved without float conversion
		if val := params.Get("properties.large_int"); val.Raw != "9007199254740993" {
			t.Errorf("useAntigravitySchema=%v: large_int corrupted, got %s", useAntigravitySchema, val.Raw)
		}
	}
}

// Issue 3662 regression: the original fix preserved multi-branch tool unions for
// every Antigravity model, but the Antigravity Claude backend converts Gemini
// function declarations into Claude custom.input_schema and rejects nested
// multi-branch anyOf/oneOf unions with HTTP 400 ("JSON schema is invalid").
// Tool schemas for Claude models must therefore have nested multi-branch unions
// flattened to a single strongest branch before they are sent upstream, while
// Gemini models keep the preserved-union behavior from the original fix.
func TestAntigravityBuildRequestFlattensToolUnionsForClaude_Issue3662Regression(t *testing.T) {
	// Reduced reproduction from the regression report: a nested boolean/object/array
	// union under properties.value, plus a two-string-branch nested union. Merely
	// declaring these tools must not poison the whole Claude request.
	payload := []byte(`{"request":{"tools":[{"functionDeclarations":[{"name":"diagnostic_union","parametersJsonSchema":{` +
		`"type":"object",` +
		`"properties":{` +
		`"value":{"anyOf":[{"type":"boolean"},{"type":"object","properties":{"enabled":{"type":"boolean"}},"required":["enabled"]},{"type":"array","items":{"type":"string"}}]},` +
		`"duo":{"anyOf":[{"type":"string"},{"type":"string"}]}` +
		`},` +
		`"required":["value"]` +
		`}}]}]}}`)

	declParams := func(t *testing.T, modelName string) gjson.Result {
		t.Helper()
		body := buildRequestBodyFromRawPayload(t, modelName, payload)
		encoded, errMarshal := json.Marshal(body)
		if errMarshal != nil {
			t.Fatal(errMarshal)
		}
		decl := gjson.GetBytes(encoded, "request.tools.0.functionDeclarations.0")
		if !decl.Exists() {
			t.Fatalf("model %s: function declaration missing: %s", modelName, encoded)
		}
		if decl.Get("parametersJsonSchema").Exists() {
			t.Fatalf("model %s: parametersJsonSchema was not renamed for Antigravity: %s", modelName, decl.Raw)
		}
		params := decl.Get("parameters")
		if !params.Exists() {
			t.Fatalf("model %s: parameters missing after sanitization: %s", modelName, decl.Raw)
		}
		return params
	}

	t.Run("claude flattens nested tool unions", func(t *testing.T) {
		params := declParams(t, "claude-opus-5-5-high")

		value := params.Get("properties.value")
		if value.Get("anyOf").Exists() || value.Get("oneOf").Exists() {
			t.Fatalf("claude tool union survived sanitization; Antigravity Claude rejects nested multi-branch unions: %s", params.Raw)
		}
		// The pre-regression behavior selected the strongest (object) branch.
		if got := value.Get("type").String(); got != "object" {
			t.Fatalf("claude flattened value type = %q, want object (strongest branch): %s", got, params.Raw)
		}
		if !value.Get("properties.enabled").Exists() {
			t.Fatalf("claude flattened value lost the object branch properties: %s", params.Raw)
		}
		if desc := value.Get("description").String(); !strings.Contains(desc, "Accepts:") || !strings.Contains(desc, "boolean") || !strings.Contains(desc, "array") {
			t.Fatalf("claude flattened value lost the dropped-branch hint: %s", params.Raw)
		}

		duo := params.Get("properties.duo")
		if duo.Get("anyOf").Exists() || duo.Get("oneOf").Exists() {
			t.Fatalf("claude two-string-branch union survived sanitization: %s", params.Raw)
		}
		if got := duo.Get("type").String(); got != "string" {
			t.Fatalf("claude flattened duo type = %q, want string: %s", got, params.Raw)
		}
	})

	// The Claude-request translator serves whichever Antigravity model the client
	// selected, Gemini included: its union handling must stay model-unaware and
	// preserve multi-branch unions, because only the executor knows the target
	// model. Flattening in the translator would lose non-first branches (audio)
	// before the executor's Gemini path could keep them.
	// Function return schemas are not the rejected custom.input_schema: even for
	// Claude models their multi-branch unions must survive cleaning, like any other
	// response-side schema. Only declaration parameter schemas are flattened.
	t.Run("claude keeps unions in function return schemas", func(t *testing.T) {
		unionSchema := `{"type":"object","properties":{"item":{"anyOf":[{"type":"string"},{"type":"object","properties":{"image":{"type":"string"}},"required":["image"]},{"type":"object","properties":{"audio":{"type":"string"}},"required":["audio"]}]}},"required":["item"]}`
		decl := `{"name":"diagnostic_union"` +
			`,"parametersJsonSchema":` + unionSchema +
			`,"response":` + unionSchema +
			`,"responseJsonSchema":` + unionSchema +
			`,"response_json_schema":` + unionSchema + `}`
		returnPayload := []byte(`{"request":{"tools":[{"functionDeclarations":[` + decl + `]}]}}`)

		body := buildRequestBodyFromRawPayload(t, "claude-opus-5-5-high", returnPayload)
		encoded, errMarshal := json.Marshal(body)
		if errMarshal != nil {
			t.Fatal(errMarshal)
		}
		gotDecl := gjson.GetBytes(encoded, "request.tools.0.functionDeclarations.0")
		params := gotDecl.Get("parameters")
		if params.Get("properties.item.anyOf").Exists() || params.Get("properties.item.type").String() != "object" || !params.Get("properties.item.properties.image").Exists() {
			t.Fatalf("claude parameter schema union was not flattened: %s", params.Raw)
		}
		for _, key := range []string{"response", "responseJsonSchema", "response_json_schema"} {
			ret := gotDecl.Get(key)
			if !ret.Exists() {
				t.Fatalf("claude return schema %q missing after cleaning: %s", key, gotDecl.Raw)
			}
			if got := len(ret.Get("properties.item.anyOf").Array()); got != 3 {
				t.Fatalf("claude return schema %q anyOf branch count = %d, want 3 preserved: %s", key, got, ret.Raw)
			}
			if !ret.Get("properties.item.anyOf.2.properties.audio").Exists() {
				t.Fatalf("claude return schema %q lost the audio branch: %s", key, ret.Raw)
			}
		}
	})

	// A nullable multi-branch union cleaned by the Claude-request translator first
	// (which strips the null branch and sets nullable:true on the parent) must keep
	// that nullability when the executor flattens it for a Claude model, matching
	// what direct Claude cleaning produces.
	t.Run("claude chain keeps nullable marker after flattening", func(t *testing.T) {
		input := []byte(`{"model":"claude-opus-5-5-high","max_tokens":1024,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"diagnostic_union","input_schema":{"type":"object","properties":{"value":{"anyOf":[{"type":"string"},{"type":"integer"},{"type":"null"}]}},"required":["value"]}}]}`)
		translated, errConv := antigravityclaude.ConvertClaudeRequestToAntigravity("claude-opus-5-5-high", input, false)
		if errConv != nil {
			t.Fatalf("convert error: %v", errConv)
		}
		translatedValue := gjson.GetBytes(translated, "request.tools.0.functionDeclarations.0.parametersJsonSchema.properties.value")
		if len(translatedValue.Get("anyOf").Array()) != 2 || !translatedValue.Get("nullable").Bool() {
			t.Fatalf("translator must preserve the union and set nullable:true: %s", translatedValue.Raw)
		}

		body := buildRequestBodyFromRawPayload(t, "claude-opus-5-5-high", translated)
		encoded, errMarshal := json.Marshal(body)
		if errMarshal != nil {
			t.Fatal(errMarshal)
		}
		finalValue := gjson.GetBytes(encoded, "request.tools.0.functionDeclarations.0.parameters.properties.value")
		if finalValue.Get("anyOf").Exists() || finalValue.Get("type").String() != "string" {
			t.Fatalf("executor must flatten the union for Claude: %s", finalValue.Raw)
		}
		if !finalValue.Get("nullable").Bool() {
			t.Fatalf("executor dropped nullable:true while flattening: %s", finalValue.Raw)
		}
	})

	t.Run("claude translator then executor honors the selected model", func(t *testing.T) {
		for _, modelName := range []string{"gemini-3.6-flash-high", "claude-opus-5-5-high"} {
			input := []byte(`{"model":"` + modelName + `","max_tokens":1024,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"diagnostic_union","input_schema":{"type":"object","properties":{"item":{"anyOf":[{"type":"string"},{"type":"object","properties":{"image":{"type":"string"}},"required":["image"]},{"type":"object","properties":{"audio":{"type":"string"}},"required":["audio"]}]}},"required":["item"]}}]}`)
			translated, errConv := antigravityclaude.ConvertClaudeRequestToAntigravity(modelName, input, false)
			if errConv != nil {
				t.Fatalf("model %s: convert error: %v", modelName, errConv)
			}
			translatedParams := gjson.GetBytes(translated, "request.tools.0.functionDeclarations.0.parametersJsonSchema")
			if got := len(translatedParams.Get("properties.item.anyOf").Array()); got != 3 {
				t.Fatalf("model %s: translator anyOf branch count = %d, want 3 (flattening belongs to the executor, not the translator): %s", modelName, got, translatedParams.Raw)
			}
			if !translatedParams.Get("properties.item.anyOf.2.properties.audio").Exists() {
				t.Fatalf("model %s: translator lost the audio branch: %s", modelName, translatedParams.Raw)
			}

			body := buildRequestBodyFromRawPayload(t, modelName, translated)
			encoded, errMarshal := json.Marshal(body)
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			finalParams := gjson.GetBytes(encoded, "request.tools.0.functionDeclarations.0.parameters")
			if !finalParams.Exists() {
				t.Fatalf("model %s: final parameters missing: %s", modelName, encoded)
			}
			if strings.Contains(modelName, "claude") {
				if finalParams.Get("properties.item.anyOf").Exists() || finalParams.Get("properties.item.type").String() != "object" || !finalParams.Get("properties.item.properties.image").Exists() {
					t.Fatalf("model %s: executor must flatten the union for Claude: %s", modelName, finalParams.Raw)
				}
			} else if got := len(finalParams.Get("properties.item.anyOf").Array()); got != 3 || !finalParams.Get("properties.item.anyOf.2.properties.audio").Exists() {
				t.Fatalf("model %s: executor must keep 3 preserved branches incl. audio for Gemini, got %d: %s", modelName, got, finalParams.Raw)
			}
		}
	})

	t.Run("gemini keeps preserved tool unions", func(t *testing.T) {
		// One model per existing useAntigravitySchema group: gemini-3.1-pro shares the
		// Claude VALIDATED placeholder group, gemini-3.6-flash does not. Both are Gemini
		// and must keep the preserved unions from the original Issue 3662 fix.
		for _, modelName := range []string{"gemini-3.1-pro-low", "gemini-3.6-flash-high"} {
			params := declParams(t, modelName)

			if got := len(params.Get("properties.value.anyOf").Array()); got != 3 {
				t.Fatalf("model %s: preserved anyOf branch count = %d, want 3: %s", modelName, got, params.Raw)
			}
			if !params.Get("properties.value.anyOf.1.properties.enabled").Exists() {
				t.Fatalf("model %s: preserved anyOf lost the object branch: %s", modelName, params.Raw)
			}
			if got := len(params.Get("properties.duo.anyOf").Array()); got != 2 {
				t.Fatalf("model %s: preserved two-branch anyOf count = %d, want 2: %s", modelName, got, params.Raw)
			}
		}
	})
}

// TestSanitizeAntigravityRequestSchemasScalesLinearlyWithDeclarations is the
// regression test for the quadratic tool sanitizing reported in Issue 6714:
// sanitizing must not rewrite the whole tools document once per function
// declaration. The allocation bound is asserted directly in the test (not via
// a benchmark) so a return to per-declaration document rewrites fails loudly.
func TestSanitizeAntigravityRequestSchemasScalesLinearlyWithDeclarations(t *testing.T) {
	const declCount = 256

	var input, want strings.Builder
	input.WriteString(`{"request":{"tools":[{"functionDeclarations":[`)
	want.WriteString(`{"request":{"tools":[{"functionDeclarations":[`)
	for i := 0; i < declCount; i++ {
		if i > 0 {
			input.WriteByte(',')
			want.WriteByte(',')
		}
		// parametersJsonSchema forces the rename onto parameters and $id forces
		// a cleaner rewrite of the schema. The padded description sits on the
		// declaration, outside the schema, so whole-document copies must pay for
		// it while per-declaration edits do not; the bound below then measures
		// document-rewrite cost only, not the cleaner's per-schema cost (Issue 6713).
		fmt.Fprintf(&input, `{"name":"tool_%d","description":"%s","parametersJsonSchema":{"type":"object","$id":"drop","properties":{"q":{"type":"string"}}}}`, i, strings.Repeat("x", 4096))
		// The schema has no required properties, so the Claude VALIDATED
		// placeholder is inserted even on the Gemini path (antigravity=true).
		fmt.Fprintf(&want, `{"name":"tool_%d","description":"%s","parameters":{"type":"object","properties":{"q":{"type":"string"},"_":{"type":"boolean"}},"required":["_"]}}`, i, strings.Repeat("x", 4096))
	}
	input.WriteString(`]}]}}`)
	want.WriteString(`]}]}}`)

	payload := input.String()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	got := sanitizeAntigravityRequestSchemasForModel(payload, true, false)
	runtime.ReadMemStats(&after)

	if got != want.String() {
		t.Fatalf("sanitized tools diverged from the expected bytes\nfirst declaration want: %s\nfirst declaration got:  %s",
			gjson.Get(want.String(), "request.tools.0.functionDeclarations.0").Raw,
			gjson.Get(got, "request.tools.0.functionDeclarations.0").Raw)
	}

	// Whole-document work is bounded by a constant number of passes: extracting
	// request.tools, rebuilding it, and writing it back. Everything beyond a
	// small multiple of the payload size means per-declaration document rewrites
	// are back; the pre-fix implementation allocated O(declCount x payload).
	const allowedCopies = 32
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > uint64(allowedCopies*len(payload)) {
		t.Fatalf("sanitizing %d declarations allocated %d bytes for a %d byte payload, want at most %d (cost must be linear in the tools size, not per declaration)",
			declCount, allocated, len(payload), allowedCopies*len(payload))
	}
}

// TestSanitizeAntigravityRequestSchemasPreservesToolArrayWhitespace pins the
// byte-equivalence claim of the per-declaration rewrite: rebuilding the tools
// and declarations arrays must keep the whitespace around array elements, so
// a pretty-printed payload only changes inside the cleaned declarations. The
// expected value is the input with only the affected schema bytes replaced,
// exactly what the previous whole-document sjson edits produced.
func TestSanitizeAntigravityRequestSchemasPreservesToolArrayWhitespace(t *testing.T) {
	input := "{\n" +
		"  \"request\": {\n" +
		"    \"tools\": [\n" +
		"      {\"functionDeclarations\": [\n" +
		"        {\"name\": \"a\", \"parametersJsonSchema\": {\"type\": \"object\", \"$id\": \"drop\", \"properties\": {\"q\": {\"type\": \"string\"}}}} ,\n" +
		"        {\"name\": \"b\", \"parameters\": {\"type\": \"object\", \"required\": [\"q\"], \"properties\": {\"q\": {\"type\": \"string\"}}}} ,\n" +
		"        {\"name\": \"c\"}\n" +
		"      ]} ,\n" +
		"      {\"googleSearch\": {}}\n" +
		"    ]\n" +
		"  }\n" +
		"}\n"

	// Declaration a is renamed and cleaned in place (sjson compacts the
	// replacement gaps inside the edited node, as the whole-document edits
	// did). Declaration b is already clean, and declarations b and c and
	// the googleSearch tool are untouched, so every other byte (including the
	// whitespace around commas and brackets) must survive verbatim. The odd
	// "}} ," separator is the declaration's own closing bytes, one space of
	// inter-element whitespace, the comma, and the newline.
	want := strings.Replace(input,
		`{"name": "a", "parametersJsonSchema": {"type": "object", "$id": "drop", "properties": {"q": {"type": "string"}}}}`,
		`{"name": "a","parameters":{"type": "object", "properties": {"q": {"type": "string"},"_":{"type":"boolean"}},"required":["_"]}}`, 1)
	if want == input {
		t.Fatal("test fixture is broken: declaration a was not replaced in the expected value")
	}

	if got := sanitizeAntigravityRequestSchemasForModel(input, true, false); got != want {
		t.Fatalf("pretty-printed tools lost whitespace outside the cleaned declaration\nwant:\n%s\ngot:\n%s", want, got)
	}
}
