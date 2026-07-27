package executor

import (
	"encoding/json"
	"strings"
	"testing"

	antigravitychat "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/antigravity/openai/chat-completions"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
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
			whole := util.CleanJSONSchemaForGemini(doc)
			if useAntigravitySchema {
				whole = util.CleanJSONSchemaForAntigravity(doc)
			}
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
			translated := antigravitychat.ConvertOpenAIRequestToAntigravity("gemini-3.6-flash-high", input, false)
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
