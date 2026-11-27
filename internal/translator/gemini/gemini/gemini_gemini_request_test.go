package gemini

import (
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
)

const largeInlineDataSize = 20 << 20

var largeInlineDataBenchmarkOutput []byte

func TestConvertGeminiRequestToGeminiReusesLargeNormalizedPayload(t *testing.T) {
	input := largeInlineDataGeminiRequest(true)

	// Assert the reuse invariant with t.Fatal rather than inside testing.Benchmark:
	// a failing benchmark aborts before any iteration completes and yields a zero
	// BenchmarkResult, so AllocedBytesPerOp would report 0 and silently satisfy the
	// allocation check below exactly when the payload is being copied.
	output, _ := ConvertGeminiRequestToGemini("gemini-test", input, false)
	if &output[0] != &input[0] {
		t.Fatal("normalized request should reuse the input payload")
	}
	largeInlineDataBenchmarkOutput = output

	result := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			largeInlineDataBenchmarkOutput, _ = ConvertGeminiRequestToGemini("gemini-test", input, false)
		}
	})

	if result.N == 0 {
		t.Fatal("allocation benchmark did not complete an iteration")
	}
	if allocated := result.AllocedBytesPerOp(); allocated >= 1<<20 {
		t.Fatalf("normalized 20 MiB inlineData request allocated %d bytes/op, want less than 1 MiB", allocated)
	}
}

func BenchmarkConvertGeminiRequestToGeminiLargeInlineData(b *testing.B) {
	for _, test := range []struct {
		name                  string
		includeSafetySettings bool
	}{
		{name: "normalized_passthrough", includeSafetySettings: true},
		{name: "attach_default_safety", includeSafetySettings: false},
	} {
		b.Run(test.name, func(b *testing.B) {
			input := largeInlineDataGeminiRequest(test.includeSafetySettings)
			b.ReportAllocs()
			b.SetBytes(int64(len(input)))
			b.ResetTimer()
			for b.Loop() {
				largeInlineDataBenchmarkOutput, _ = ConvertGeminiRequestToGemini("gemini-test", input, false)
			}
		})
	}
}

func largeInlineDataGeminiRequest(includeSafetySettings bool) []byte {
	prefix := `{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"video/mp4","data":"`
	suffix := `"}}]}]`
	if includeSafetySettings {
		suffix += `,"safetySettings":[]`
	}
	return []byte(prefix + strings.Repeat("A", largeInlineDataSize) + suffix + `}`)
}

func TestBackfillEmptyFunctionResponseNames_Single(t *testing.T) {
	input := []byte(`{
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "Bash", "args": {"cmd": "ls"}}}
				]
			},
			{
				"role": "user",
				"parts": [
					{"functionResponse": {"name": "", "response": {"output": "file1.txt"}}}
				]
			}
		]
	}`)

	out := backfillEmptyFunctionResponseNames(input)

	name := ojson.GetBytesNoCopy(out, "contents.1.parts.0.functionResponse.name").String()
	if name != "Bash" {
		t.Errorf("Expected backfilled name 'Bash', got '%s'", name)
	}
}

func TestBackfillEmptyFunctionResponseNames_Parallel(t *testing.T) {
	input := []byte(`{
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "Read", "args": {"path": "/a"}}},
					{"functionCall": {"name": "Grep", "args": {"pattern": "x"}}}
				]
			},
			{
				"role": "user",
				"parts": [
					{"functionResponse": {"name": "", "response": {"result": "content a"}}},
					{"functionResponse": {"name": "", "response": {"result": "match x"}}}
				]
			}
		]
	}`)

	out := backfillEmptyFunctionResponseNames(input)

	name0 := ojson.GetBytesNoCopy(out, "contents.1.parts.0.functionResponse.name").String()
	name1 := ojson.GetBytesNoCopy(out, "contents.1.parts.1.functionResponse.name").String()
	if name0 != "Read" {
		t.Errorf("Expected first name 'Read', got '%s'", name0)
	}
	if name1 != "Grep" {
		t.Errorf("Expected second name 'Grep', got '%s'", name1)
	}
}

func TestBackfillEmptyFunctionResponseNames_PreservesExisting(t *testing.T) {
	input := []byte(`{
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "Bash", "args": {}}}
				]
			},
			{
				"role": "user",
				"parts": [
					{"functionResponse": {"name": "Bash", "response": {"result": "ok"}}}
				]
			}
		]
	}`)

	out := backfillEmptyFunctionResponseNames(input)

	name := ojson.GetBytesNoCopy(out, "contents.1.parts.0.functionResponse.name").String()
	if name != "Bash" {
		t.Errorf("Expected preserved name 'Bash', got '%s'", name)
	}
}

func TestConvertGeminiRequestToGemini_BackfillsEmptyName(t *testing.T) {
	input := []byte(`{
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "Bash", "args": {"cmd": "ls"}}}
				]
			},
			{
				"role": "user",
				"parts": [
					{"functionResponse": {"name": "", "response": {"output": "file1.txt"}}}
				]
			}
		]
	}`)

	out, _ := ConvertGeminiRequestToGemini("", input, false)

	name := ojson.GetBytesNoCopy(out, "contents.1.parts.0.functionResponse.name").String()
	if name != "Bash" {
		t.Errorf("Expected backfilled name 'Bash', got '%s'", name)
	}
}

func TestBackfillEmptyFunctionResponseNames_MoreResponsesThanCalls(t *testing.T) {
	// Extra responses beyond the call count should not panic and should be left unchanged.
	input := []byte(`{
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "Bash", "args": {}}}
				]
			},
			{
				"role": "user",
				"parts": [
					{"functionResponse": {"name": "", "response": {"result": "ok"}}},
					{"functionResponse": {"name": "", "response": {"result": "extra"}}}
				]
			}
		]
	}`)

	out := backfillEmptyFunctionResponseNames(input)

	name0 := ojson.GetBytesNoCopy(out, "contents.1.parts.0.functionResponse.name").String()
	if name0 != "Bash" {
		t.Errorf("Expected first name 'Bash', got '%s'", name0)
	}
	// Second response has no matching call, should remain empty
	name1 := ojson.GetBytesNoCopy(out, "contents.1.parts.1.functionResponse.name").String()
	if name1 != "" {
		t.Errorf("Expected second name to remain empty, got '%s'", name1)
	}
}

func TestBackfillEmptyFunctionResponseNames_MultipleGroups(t *testing.T) {
	// Two sequential call/response groups should each get correct names.
	input := []byte(`{
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "Read", "args": {}}}
				]
			},
			{
				"role": "user",
				"parts": [
					{"functionResponse": {"name": "", "response": {"result": "content"}}}
				]
			},
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "Grep", "args": {}}}
				]
			},
			{
				"role": "user",
				"parts": [
					{"functionResponse": {"name": "", "response": {"result": "match"}}}
				]
			}
		]
	}`)

	out := backfillEmptyFunctionResponseNames(input)

	name0 := ojson.GetBytesNoCopy(out, "contents.1.parts.0.functionResponse.name").String()
	name1 := ojson.GetBytesNoCopy(out, "contents.3.parts.0.functionResponse.name").String()
	if name0 != "Read" {
		t.Errorf("Expected first group name 'Read', got '%s'", name0)
	}
	if name1 != "Grep" {
		t.Errorf("Expected second group name 'Grep', got '%s'", name1)
	}
}

func TestConvertGeminiRequestToGemini_FunctionResponseWithInvalidRoleNormalizesToUser(t *testing.T) {
	inputJSON := []byte(`{
		"contents": [
			{"role": "user", "parts": [{"text": "reminder"}]},
			{"role": "invalid", "parts": [{"functionResponse": {"name": "lookup", "response": {}}}]}
		]
	}`)
	out, _ := ConvertGeminiRequestToGemini("gemini-3-flash", inputJSON, false)
	contents := ojson.GetBytesNoCopy(out, "contents").Array()
	if len(contents) != 2 {
		t.Fatalf("expected 2 contents, got %d", len(contents))
	}
	if got := contents[1].Get("role").String(); got != "user" {
		t.Fatalf("functionResponse invalid role should normalize to user, got %q", got)
	}
}

func TestConvertGeminiRequestToGemini_NormalizesParametersNumericConstraints_Issue7149(t *testing.T) {
	inputJSON := []byte(`{
		"contents": [
			{"role": "user", "parts": [{"text": "run command"}]}
		],
		"tools": [
			{
				"functionDeclarations": [
					{
						"name": "run_command",
						"parameters": {
							"type": "OBJECT",
							"minProperties": "1",
							"maxProperties": "10",
							"properties": {
								"Message": {
									"type": "STRING",
									"minLength": "1",
									"maxLength": "200",
									"enum": ["1", "2"],
									"default": "1",
									"example": "1",
									"examples": ["1", {"minLength": "1"}]
								},
								"Args": {
									"type": "ARRAY",
									"minItems": "1",
									"maxItems": "5",
									"items": {
										"type": "STRING",
										"minLength": "2",
										"maxLength": "50"
									}
								},
								"Filter": {
									"anyOf": [
										{"type": "STRING", "minLength": "3"},
										{"type": "ARRAY", "minItems": "4", "items": {"type": "STRING"}}
									]
								},
								"minLength": {
									"type": "STRING",
									"minLength": "6",
									"default": "6"
								},
								"properties": {
									"type": "STRING",
									"minLength": "7"
								},
								"default": {
									"type": "STRING",
									"minLength": "8",
									"default": "8"
								},
								"a\\b": {
									"type": "STRING",
									"minLength": "11"
								}
							},
							"patternProperties": {
								"^\\d+$": {
									"type": "STRING",
									"maxLength": "12"
								}
							},
							"x-metadata": {
								"minLength": "2",
								"maxItems": "9"
							},
							"schema": {
								"minLength": "10"
							}
						}
					}
				]
			}
		],
		"generationConfig": {
			"responseSchema": {
				"type": "OBJECT",
				"minProperties": "1",
				"properties": {
					"Summary": {
						"type": "STRING",
						"minLength": "5"
					}
				}
			}
		}
	}`)

	out, err := ConvertGeminiRequestToGemini("gemini-2.5-pro", inputJSON, false)
	if err != nil {
		t.Fatalf("ConvertGeminiRequestToGemini returned error: %v", err)
	}

	decl := ojson.GetBytesNoCopy(out, "tools.0.function_declarations.0")
	if decl.Get("parameters").Exists() {
		t.Fatalf("expected parameters to be converted to parametersJsonSchema, got: %s", decl.Raw)
	}
	schema := decl.Get("parametersJsonSchema")
	if !schema.Exists() {
		t.Fatalf("expected parametersJsonSchema to exist, got: %s", decl.Raw)
	}

	numericChecks := []struct {
		path string
		want int64
	}{
		{"minProperties", 1},
		{"maxProperties", 10},
		{"properties.Message.minLength", 1},
		{"properties.Message.maxLength", 200},
		{"properties.Args.minItems", 1},
		{"properties.Args.maxItems", 5},
		{"properties.Args.items.minLength", 2},
		{"properties.Args.items.maxLength", 50},
		{"properties.Filter.anyOf.0.minLength", 3},
		{"properties.Filter.anyOf.1.minItems", 4},
		{"properties.minLength.minLength", 6},
		{"properties.properties.minLength", 7},
		{"properties.default.minLength", 8},
	}
	for _, tc := range numericChecks {
		got := schema.Get(tc.path)
		if got.Type != ojson.Number || got.Int() != tc.want {
			t.Errorf("parametersJsonSchema.%s = %v (type %v), want number %d; schema=%s", tc.path, got.Raw, got.Type, tc.want, schema.Raw)
		}
	}

	stringChecks := []struct {
		path string
		want string
	}{
		{"properties.Message.enum.0", "1"},
		{"properties.Message.enum.1", "2"},
		{"properties.Message.default", "1"},
		{"properties.Message.example", "1"},
		{"properties.Message.examples.0", "1"},
		{"properties.Message.examples.1.minLength", "1"},
		{"properties.minLength.default", "6"},
		{"properties.default.default", "8"},
		{"x-metadata.minLength", "2"},
		{"x-metadata.maxItems", "9"},
		{"schema.minLength", "10"},
	}
	for _, tc := range stringChecks {
		got := schema.Get(tc.path)
		if got.Type != ojson.String || got.String() != tc.want {
			t.Errorf("parametersJsonSchema.%s = %v (type %v), want string %q", tc.path, got.Raw, got.Type, tc.want)
		}
	}
	if got := schema.Get("properties").Map()[`a\b`].Get("minLength"); got.Type != ojson.Number || got.Int() != 11 {
		t.Errorf(`parametersJsonSchema.properties["a\\b"].minLength = %s (type %v), want number 11`, got.Raw, got.Type)
	}
	if _, bogus := schema.Get("properties").Map()["ab"]; bogus {
		t.Errorf(`unexpected fabricated property "ab" in parametersJsonSchema.properties`)
	}
	if got := schema.Get("patternProperties").Map()[`^\d+$`].Get("maxLength"); got.Type != ojson.Number || got.Int() != 12 {
		t.Errorf(`parametersJsonSchema.patternProperties["^\\d+$"].maxLength = %s (type %v), want number 12`, got.Raw, got.Type)
	}

	respSchema := ojson.GetBytesNoCopy(out, "generationConfig.responseJsonSchema")
	if !respSchema.Exists() {
		t.Fatalf("expected generationConfig.responseJsonSchema to exist, got: %s", out)
	}
	if got := respSchema.Get("minProperties"); got.Type != ojson.Number || got.Int() != 1 {
		t.Errorf("responseJsonSchema.minProperties = %v (type %v), want number 1", got.Raw, got.Type)
	}
	if got := respSchema.Get("properties.Summary.minLength"); got.Type != ojson.Number || got.Int() != 5 {
		t.Errorf("responseJsonSchema.properties.Summary.minLength = %v (type %v), want number 5", got.Raw, got.Type)
	}
}

func TestConvertGeminiRequestToGemini_NormalizesNumericProtoSchemaTypes_Issue8032(t *testing.T) {
	inputJSON := []byte(`{
		"contents": [{"role": "user", "parts": [{"text": "hello"}]}],
		"tools": [{
			"functionDeclarations": [{
				"name": "search",
				"parameters": {
					"type": 6,
					"properties": {
						"query": {"type": 1},
						"tags": {"type": 5, "items": {"type": 1}},
						"limit": {"type": 3},
						"verbose": {"type": 4}
					},
					"required": ["query"]
				}
			}]
		}],
		"generationConfig": {
			"responseSchema": {
				"type": 6,
				"properties": {
					"score": {"type": 2},
					"empty": {"type": 7}
				}
			}
		}
	}`)

	out, err := ConvertGeminiRequestToGemini("gemini-3-flash", inputJSON, false)
	if err != nil {
		t.Fatalf("ConvertGeminiRequestToGemini returned error: %v", err)
	}

	params := ojson.GetBytesNoCopy(out, "tools.0.function_declarations.0.parametersJsonSchema")
	for _, tc := range []struct {
		path string
		want string
	}{
		{"type", "object"},
		{"properties.query.type", "string"},
		{"properties.tags.type", "array"},
		{"properties.tags.items.type", "string"},
		{"properties.limit.type", "integer"},
		{"properties.verbose.type", "boolean"},
	} {
		got := params.Get(tc.path)
		if got.Type != ojson.String || got.String() != tc.want {
			t.Errorf("parametersJsonSchema.%s = %s (type %v), want string %q", tc.path, got.Raw, got.Type, tc.want)
		}
	}

	respSchema := ojson.GetBytesNoCopy(out, "generationConfig.responseJsonSchema")
	for _, tc := range []struct {
		path string
		want string
	}{
		{"type", "object"},
		{"properties.score.type", "number"},
		{"properties.empty.type", "null"},
	} {
		got := respSchema.Get(tc.path)
		if got.Type != ojson.String || got.String() != tc.want {
			t.Errorf("responseJsonSchema.%s = %s (type %v), want string %q", tc.path, got.Raw, got.Type, tc.want)
		}
	}

	t.Run("snake_case parameters_json_schema and generation_config response_json_schema", func(t *testing.T) {
		snakeInput := []byte(`{
			"contents": [{"role": "user", "parts": [{"text": "hello"}]}],
			"tools": [{
				"function_declarations": [{
					"name": "search",
					"parameters_json_schema": {
						"type": 6,
						"properties": {
							"tags": {"type": 5, "items": {"type": 1}}
						}
					}
				}]
			}],
			"generation_config": {
				"response_json_schema": {
					"type": 6,
					"properties": {
						"score": {"type": 2}
					}
				}
			}
		}`)
		snakeOut, errSnake := ConvertGeminiRequestToGemini("gemini-3-flash", snakeInput, false)
		if errSnake != nil {
			t.Fatalf("ConvertGeminiRequestToGemini returned error: %v", errSnake)
		}
		snakeParams := ojson.GetBytesNoCopy(snakeOut, "tools.0.function_declarations.0.parameters_json_schema")
		for _, tc := range []struct {
			path string
			want string
		}{
			{"type", "object"},
			{"properties.tags.type", "array"},
			{"properties.tags.items.type", "string"},
		} {
			got := snakeParams.Get(tc.path)
			if got.Type != ojson.String || got.String() != tc.want {
				t.Errorf("parameters_json_schema.%s = %s (type %v), want string %q", tc.path, got.Raw, got.Type, tc.want)
			}
		}
		snakeResp := ojson.GetBytesNoCopy(snakeOut, "generation_config.response_json_schema")
		for _, tc := range []struct {
			path string
			want string
		}{
			{"type", "object"},
			{"properties.score.type", "number"},
		} {
			got := snakeResp.Get(tc.path)
			if got.Type != ojson.String || got.String() != tc.want {
				t.Errorf("generation_config.response_json_schema.%s = %s (type %v), want string %q", tc.path, got.Raw, got.Type, tc.want)
			}
		}
	})

	t.Run("snake_case generation_config response_schema converted to response_json_schema", func(t *testing.T) {
		snakeInput := []byte(`{
			"contents": [{"role": "user", "parts": [{"text": "hello"}]}],
			"generation_config": {
				"response_schema": {
					"type": 6,
					"properties": {
						"title": {"type": 1}
					}
				}
			}
		}`)
		snakeOut, errSnake := ConvertGeminiRequestToGemini("gemini-3-flash", snakeInput, false)
		if errSnake != nil {
			t.Fatalf("ConvertGeminiRequestToGemini returned error: %v", errSnake)
		}
		if ojson.GetBytesNoCopy(snakeOut, "generation_config.response_schema").Exists() {
			t.Fatalf("expected generation_config.response_schema to be replaced by response_json_schema: %s", snakeOut)
		}
		snakeResp := ojson.GetBytesNoCopy(snakeOut, "generation_config.response_json_schema")
		if got := snakeResp.Get("type"); got.Type != ojson.String || got.String() != "object" {
			t.Errorf("generation_config.response_json_schema.type = %s (type %v), want string object", got.Raw, got.Type)
		}
		if got := snakeResp.Get("properties.title.type"); got.Type != ojson.String || got.String() != "string" {
			t.Errorf("generation_config.response_json_schema.properties.title.type = %s (type %v), want string", got.Raw, got.Type)
		}
	})
}

func TestConvertGeminiRequestToGemini_GroupsSplitFunctionResponses(t *testing.T) {
	input := []byte(`{
		"contents": [
			{"role": "user", "parts": [{"text": "call echo 3 times"}]},
			{"role": "model", "parts": [
				{"functionCall": {"id": "call_1", "name": "echo", "args": {"x": "a"}}},
				{"functionCall": {"id": "call_2", "name": "echo", "args": {"x": "b"}}},
				{"functionCall": {"id": "call_3", "name": "echo", "args": {"x": "c"}}}
			]},
			{"role": "user", "parts": [{"functionResponse": {"id": "call_1", "name": "echo", "response": {"result": "res_a"}}}]},
			{"role": "user", "parts": [{"functionResponse": {"id": "call_2", "name": "echo", "response": {"result": "res_b"}}}]},
			{"role": "user", "parts": [{"functionResponse": {"id": "call_3", "name": "echo", "response": {"result": "res_c"}}}]}
		]
	}`)

	out, err := ConvertGeminiRequestToGemini("gemini-4-hydrogen", input, false)
	if err != nil {
		t.Fatalf("ConvertGeminiRequestToGemini returned error: %v", err)
	}
	if errPair := signature.ValidateGeminiFunctionCallPairing(out); errPair != nil {
		t.Fatalf("expected valid pairing after grouping split functionResponses, got: %v\nout=%s", errPair, out)
	}
	contents := ojson.GetBytesNoCopy(out, "contents")
	if got := contents.Get("#").Int(); got != 3 {
		t.Fatalf("contents length = %d, want 3; out=%s", got, out)
	}
	if got := contents.Get("2.parts.#").Int(); got != 3 {
		t.Fatalf("contents[2].parts length = %d, want 3; out=%s", got, out)
	}
	for i, wantID := range []string{"call_1", "call_2", "call_3"} {
		gotID := contents.Get("2.parts").Array()[i].Get("functionResponse.id").String()
		if gotID != wantID {
			t.Fatalf("contents[2].parts[%d].functionResponse.id = %q, want %q", i, gotID, wantID)
		}
	}
}

func TestConvertGeminiRequestToGemini_ReordersOutOfOrderSplitFunctionResponsesAndBackfillsNames(t *testing.T) {
	input := []byte(`{
		"contents": [
			{"role": "user", "parts": [{"text": "run tools"}]},
			{"role": "model", "parts": [
				{"functionCall": {"id": "call_1", "name": "read", "args": {"path": "a"}}},
				{"functionCall": {"id": "call_2", "name": "grep", "args": {"pattern": "b"}}},
				{"functionCall": {"id": "call_3", "name": "bash", "args": {"cmd": "c"}}}
			]},
			{"role": "user", "parts": [{"functionResponse": {"id": "call_3", "name": "", "response": {"result": "r3"}}}]},
			{"role": "user", "parts": [{"text": "intervening reminder"}]},
			{"role": "user", "parts": [
				{"functionResponse": {"id": "call_1", "name": "", "response": {"result": "r1"}}},
				{"text": "sibling user note"}
			]},
			{"role": "function", "parts": [{"functionResponse": {"id": "call_2", "name": "", "response": {"result": "r2"}}}]}
		]
	}`)

	out, err := ConvertGeminiRequestToGemini("gemini-4-neon", input, false)
	if err != nil {
		t.Fatalf("ConvertGeminiRequestToGemini returned error: %v", err)
	}
	if errPair := signature.ValidateGeminiFunctionCallPairing(out); errPair != nil {
		t.Fatalf("expected valid pairing after reordering and backfilling, got: %v\nout=%s", errPair, out)
	}
	contents := ojson.GetBytesNoCopy(out, "contents")
	if got := contents.Get("#").Int(); got != 5 {
		t.Fatalf("contents length = %d, want 5 (user, model, merged responses, reminder, sibling note); out=%s", got, out)
	}
	wantPairs := []struct{ id, name string }{
		{"call_1", "read"},
		{"call_2", "grep"},
		{"call_3", "bash"},
	}
	respParts := contents.Get("2.parts").Array()
	if len(respParts) != 3 {
		t.Fatalf("contents[2].parts length = %d, want 3", len(respParts))
	}
	for i, want := range wantPairs {
		gotID := respParts[i].Get("functionResponse.id").String()
		gotName := respParts[i].Get("functionResponse.name").String()
		if gotID != want.id || gotName != want.name {
			t.Fatalf("contents[2].parts[%d] = (%q, %q), want (%q, %q)", i, gotID, gotName, want.id, want.name)
		}
	}
	if got := contents.Get("3.parts.0.text").String(); got != "intervening reminder" {
		t.Fatalf("contents[3] text = %q, want %q", got, "intervening reminder")
	}
	if got := contents.Get("4.parts.0.text").String(); got != "sibling user note" {
		t.Fatalf("contents[4] text = %q, want %q", got, "sibling user note")
	}
}

func TestConvertGeminiRequestToGemini_PreservesIncompleteFunctionResponsesForValidation(t *testing.T) {
	input := []byte(`{
		"contents": [
			{"role": "user", "parts": [{"text": "call echo 3 times"}]},
			{"role": "model", "parts": [
				{"functionCall": {"id": "call_1", "name": "echo", "args": {"x": "a"}}},
				{"functionCall": {"id": "call_2", "name": "echo", "args": {"x": "b"}}},
				{"functionCall": {"id": "call_3", "name": "echo", "args": {"x": "c"}}}
			]},
			{"role": "user", "parts": [{"functionResponse": {"id": "call_1", "name": "echo", "response": {"result": "res_a"}}}]}
		]
	}`)

	out, err := ConvertGeminiRequestToGemini("gemini-4-neon", input, false)
	if err != nil {
		t.Fatalf("ConvertGeminiRequestToGemini returned error: %v", err)
	}
	errPair := signature.ValidateGeminiFunctionCallPairing(out)
	if errPair == nil || !strings.Contains(errPair.Error(), "functionResponse count 1 does not match pending functionCall count 3") {
		t.Fatalf("expected honest count mismatch error for incomplete history, got: %v\nout=%s", errPair, out)
	}
}

func TestConvertGeminiRequestToGemini_DefersInterveningReminderBeforeSingleFunctionResponseTurn(t *testing.T) {
	input := []byte(`{
		"contents": [
			{"role": "user", "parts": [{"text": "run tools"}]},
			{"role": "model", "parts": [
				{"functionCall": {"id": "call_1", "name": "read", "args": {"path": "a"}}},
				{"functionCall": {"id": "call_2", "name": "grep", "args": {"pattern": "b"}}}
			]},
			{"role": "user", "parts": [{"text": "intervening reminder"}]},
			{"role": "user", "parts": [
				{"functionResponse": {"id": "call_1", "name": "", "response": {"result": "r1"}}},
				{"functionResponse": {"id": "call_2", "name": "   ", "response": {"result": "r2"}}}
			]}
		]
	}`)

	out, err := ConvertGeminiRequestToGemini("gemini-4-neon", input, false)
	if err != nil {
		t.Fatalf("ConvertGeminiRequestToGemini returned error: %v", err)
	}
	if errPair := signature.ValidateGeminiFunctionCallPairing(out); errPair != nil {
		t.Fatalf("expected valid pairing after deferring reminder and backfilling names, got: %v\nout=%s", errPair, out)
	}
	contents := ojson.GetBytesNoCopy(out, "contents")
	if got := contents.Get("#").Int(); got != 4 {
		t.Fatalf("contents length = %d, want 4; out=%s", got, out)
	}
	if got := contents.Get("2.parts.0.functionResponse.name").String(); got != "read" {
		t.Fatalf("contents[2].parts[0].functionResponse.name = %q, want %q", got, "read")
	}
	if got := contents.Get("2.parts.1.functionResponse.name").String(); got != "grep" {
		t.Fatalf("contents[2].parts[1].functionResponse.name = %q, want %q", got, "grep")
	}
	if got := contents.Get("3.parts.0.text").String(); got != "intervening reminder" {
		t.Fatalf("contents[3] text = %q, want %q", got, "intervening reminder")
	}
}

func TestConvertGeminiRequestToGemini_ReordersSingleTurnMixedIDAndNamelessFunctionResponses(t *testing.T) {
	input := []byte(`{
		"contents": [
			{"role": "user", "parts": [{"text": "run tools"}]},
			{"role": "model", "parts": [
				{"functionCall": {"id": "call_1", "name": "read", "args": {"path": "a"}}},
				{"functionCall": {"name": "grep", "args": {"pattern": "b"}}},
				{"functionCall": {"name": "bash", "args": {"cmd": "c"}}}
			]},
			{"role": "user", "parts": [
				{"functionResponse": {"id": "call_1", "name": "read", "response": {"result": "r1"}}},
				{"functionResponse": {"name": "bash", "response": {"result": "r3"}}},
				{"functionResponse": {"name": "grep", "response": {"result": "r2"}}}
			]}
		]
	}`)

	out, err := ConvertGeminiRequestToGemini("gemini-4-neon", input, false)
	if err != nil {
		t.Fatalf("ConvertGeminiRequestToGemini returned error: %v", err)
	}
	if errPair := signature.ValidateGeminiFunctionCallPairing(out); errPair != nil {
		t.Fatalf("expected valid pairing after reordering mixed-ID responses, got: %v\nout=%s", errPair, out)
	}
	respParts := ojson.GetBytesNoCopy(out, "contents.2.parts").Array()
	if len(respParts) != 3 {
		t.Fatalf("contents[2].parts length = %d, want 3", len(respParts))
	}
	for i, wantName := range []string{"read", "grep", "bash"} {
		if gotName := respParts[i].Get("functionResponse.name").String(); gotName != wantName {
			t.Fatalf("contents[2].parts[%d].functionResponse.name = %q, want %q", i, gotName, wantName)
		}
	}
}

func TestConvertGeminiRequestToGemini_ModelWithoutPartsResetsPendingGrouping(t *testing.T) {
	input := []byte(`{
		"contents": [
			{"role": "model", "parts": [
				{"functionCall": {"id": "call_1", "name": "echo", "args": {"x": "a"}}},
				{"functionCall": {"id": "call_2", "name": "echo", "args": {"x": "b"}}}
			]},
			{"role": "user", "parts": [{"functionResponse": {"id": "call_1", "name": "echo", "response": {"result": "res_a"}}}]},
			{"role": "model"},
			{"role": "user", "parts": [{"functionResponse": {"id": "call_2", "name": "echo", "response": {"result": "res_b"}}}]}
		]
	}`)

	if geminiFunctionResponsesNeedGrouping(ojson.GetBytesNoCopy(input, "contents")) {
		t.Fatalf("expected model turn without parts to reset pending state in fast-path check")
	}
}

func TestConvertGeminiRequestToGemini_PreservesExcessSplitResponsesForCountMismatchValidation(t *testing.T) {
	input := []byte(`{
		"contents": [
			{"role": "user", "parts": [{"text": "call echo twice"}]},
			{"role": "model", "parts": [
				{"functionCall": {"id": "call_1", "name": "echo", "args": {"x": "a"}}},
				{"functionCall": {"id": "call_2", "name": "echo", "args": {"x": "b"}}}
			]},
			{"role": "user", "parts": [{"functionResponse": {"id": "call_1", "name": "echo", "response": {"result": "res_a"}}}]},
			{"role": "user", "parts": [
				{"functionResponse": {"id": "call_2", "name": "echo", "response": {"result": "res_b"}}},
				{"functionResponse": {"id": "call_3", "name": "echo", "response": {"result": "res_extra"}}}
			]}
		]
	}`)

	out, err := ConvertGeminiRequestToGemini("gemini-4-neon", input, false)
	if err != nil {
		t.Fatalf("ConvertGeminiRequestToGemini returned error: %v", err)
	}
	errPair := signature.ValidateGeminiFunctionCallPairing(out)
	if errPair == nil || !strings.Contains(errPair.Error(), "functionResponse count 3 does not match pending functionCall count 2") {
		t.Fatalf("expected honest count mismatch error for excess split responses, got: %v\nout=%s", errPair, out)
	}
}

func TestConvertGeminiRequestToGemini_GroupsSnakeCaseSplitFunctionResponses(t *testing.T) {
	input := []byte(`{
		"contents": [
			{"role": "user", "parts": [{"text": "call echo twice"}]},
			{"role": "model", "parts": [
				{"function_call": {"id": "call_1", "name": "echo", "args": {"x": "a"}}},
				{"function_call": {"id": "call_2", "name": "echo", "args": {"x": "b"}}}
			]},
			{"role": "user", "parts": [{"function_response": {"id": "call_2", "name": "echo", "response": {"result": "res_b"}}}]},
			{"role": "user", "parts": [{"function_response": {"id": "call_1", "name": "echo", "response": {"result": "res_a"}}}]}
		]
	}`)

	out, err := ConvertGeminiRequestToGemini("gemini-4-neon", input, false)
	if err != nil {
		t.Fatalf("ConvertGeminiRequestToGemini returned error: %v", err)
	}
	contents := ojson.GetBytesNoCopy(out, "contents")
	if got := contents.Get("#").Int(); got != 3 {
		t.Fatalf("contents length = %d, want 3; out=%s", got, out)
	}
	respParts := contents.Get("2.parts").Array()
	if len(respParts) != 2 {
		t.Fatalf("contents[2].parts length = %d, want 2; out=%s", len(respParts), out)
	}
	if respParts[0].Get("function_response.id").String() != "call_1" || respParts[1].Get("function_response.id").String() != "call_2" {
		t.Fatalf("expected snake_case responses reordered to [call_1, call_2], got %s", contents.Get("2.parts").Raw)
	}
}

func TestConvertGeminiRequestToGemini_BackfillsEmptySnakeCaseFunctionResponseNames(t *testing.T) {
	input := []byte(`{
		"contents": [
			{"role": "user", "parts": [{"text": "run tools"}]},
			{"role": "model", "parts": [
				{"function_call": {"id": "call_1", "name": "read", "args": {"path": "a"}}},
				{"function_call": {"id": "call_2", "name": "grep", "args": {"pattern": "b"}}}
			]},
			{"role": "user", "parts": [{"function_response": {"id": "call_2", "name": "", "response": {"result": "r2"}}}]},
			{"role": "user", "parts": [{"function_response": {"id": "call_1", "name": "   ", "response": {"result": "r1"}}}]}
		]
	}`)

	out, err := ConvertGeminiRequestToGemini("gemini-4-neon", input, false)
	if err != nil {
		t.Fatalf("ConvertGeminiRequestToGemini returned error: %v", err)
	}
	if errPair := signature.ValidateGeminiFunctionCallPairing(out); errPair != nil {
		t.Fatalf("expected valid pairing after grouping and backfilling snake_case responses, got: %v\nout=%s", errPair, out)
	}
	respParts := ojson.GetBytesNoCopy(out, "contents.2.parts").Array()
	if len(respParts) != 2 {
		t.Fatalf("contents[2].parts length = %d, want 2; out=%s", len(respParts), out)
	}
	want := []struct{ id, name string }{
		{"call_1", "read"},
		{"call_2", "grep"},
	}
	for i, tc := range want {
		if respParts[i].Get("functionResponse").Exists() {
			t.Fatalf("contents[2].parts[%d] unexpectedly created camelCase functionResponse: %s", i, respParts[i].Raw)
		}
		gotID := respParts[i].Get("function_response.id").String()
		gotName := respParts[i].Get("function_response.name").String()
		if gotID != tc.id || gotName != tc.name {
			t.Fatalf("contents[2].parts[%d] = (%q, %q), want (%q, %q)", i, gotID, gotName, tc.id, tc.name)
		}
	}

	t.Run("legacy_non_array_parts_backfills_snake_case", func(t *testing.T) {
		legacy := []byte(`{
			"contents": [
				{"role": "model", "parts": {"0": {"function_call": {"id": "call_1", "name": "read", "args": {}}}}},
				{"role": "user", "parts": {"0": {"function_response": {"id": "call_1", "name": "  ", "response": {"result": "ok"}}}}}
			]
		}`)
		got := backfillEmptyFunctionResponseNames(legacy)
		if name := ojson.GetBytesNoCopy(got, "contents.1.parts.0.function_response.name").String(); name != "read" {
			t.Fatalf("legacy snake_case backfill name = %q, want %q; out=%s", name, "read", got)
		}
	})
}

func TestConvertGeminiRequestToGemini_ReordersDuplicateIDParallelFunctionResponsesByName(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{
			name: "single_turn",
			input: `{
				"contents": [
					{"role": "user", "parts": [{"text": "run tools"}]},
					{"role": "model", "parts": [
						{"functionCall": {"id": "1", "name": "read", "args": {"path": "a"}}},
						{"functionCall": {"id": "1", "name": "grep", "args": {"pattern": "b"}}}
					]},
					{"role": "user", "parts": [
						{"functionResponse": {"id": "1", "name": "grep", "response": {"result": "r2"}}},
						{"functionResponse": {"id": "1", "name": "read", "response": {"result": "r1"}}}
					]}
				]
			}`,
		},
		{
			name: "split_turns",
			input: `{
				"contents": [
					{"role": "user", "parts": [{"text": "run tools"}]},
					{"role": "model", "parts": [
						{"functionCall": {"id": "1", "name": "read", "args": {"path": "a"}}},
						{"functionCall": {"id": "1", "name": "grep", "args": {"pattern": "b"}}}
					]},
					{"role": "user", "parts": [{"functionResponse": {"id": "1", "name": "grep", "response": {"result": "r2"}}}]},
					{"role": "user", "parts": [{"functionResponse": {"id": "1", "name": "read", "response": {"result": "r1"}}}]}
				]
			}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := ConvertGeminiRequestToGemini("gemini-4-neon", []byte(tc.input), false)
			if err != nil {
				t.Fatalf("ConvertGeminiRequestToGemini returned error: %v", err)
			}
			if errPair := signature.ValidateGeminiFunctionCallPairing(out); errPair != nil {
				t.Fatalf("expected valid pairing after reordering duplicate-ID responses by name, got: %v\nout=%s", errPair, out)
			}
			respParts := ojson.GetBytesNoCopy(out, "contents.2.parts").Array()
			if len(respParts) != 2 {
				t.Fatalf("contents[2].parts length = %d, want 2; out=%s", len(respParts), out)
			}
			if respParts[0].Get("functionResponse.name").String() != "read" || respParts[1].Get("functionResponse.name").String() != "grep" {
				t.Fatalf("expected responses ordered [read, grep], got %s", ojson.GetBytesNoCopy(out, "contents.2.parts").Raw)
			}
		})
	}
}

func TestConvertGeminiRequestToGemini_GroupsExcessSplitFunctionResponsesAcrossSeparateTurns(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{
			name: "trailing_extra_turn",
			input: `{
				"contents": [
					{"role": "user", "parts": [{"text": "call echo twice"}]},
					{"role": "model", "parts": [
						{"functionCall": {"id": "call_1", "name": "echo", "args": {"x": "a"}}},
						{"functionCall": {"id": "call_2", "name": "echo", "args": {"x": "b"}}}
					]},
					{"role": "user", "parts": [{"functionResponse": {"id": "call_1", "name": "echo", "response": {"result": "res_a"}}}]},
					{"role": "user", "parts": [{"functionResponse": {"id": "call_2", "name": "echo", "response": {"result": "res_b"}}}]},
					{"role": "user", "parts": [{"functionResponse": {"id": "call_3", "name": "echo", "response": {"result": "res_extra"}}}]}
				]
			}`,
		},
		{
			name: "leading_extra_turn_with_out_of_order_matches",
			input: `{
				"contents": [
					{"role": "user", "parts": [{"text": "call echo twice"}]},
					{"role": "model", "parts": [
						{"functionCall": {"id": "call_1", "name": "echo", "args": {"x": "a"}}},
						{"functionCall": {"id": "call_2", "name": "echo", "args": {"x": "b"}}}
					]},
					{"role": "user", "parts": [{"functionResponse": {"id": "call_extra", "name": "echo", "response": {"result": "extra"}}}]},
					{"role": "user", "parts": [{"functionResponse": {"id": "call_2", "name": "echo", "response": {"result": "res_b"}}}]},
					{"role": "user", "parts": [{"functionResponse": {"id": "call_1", "name": "echo", "response": {"result": "res_a"}}}]}
				]
			}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := ConvertGeminiRequestToGemini("gemini-4-neon", []byte(tc.input), false)
			if err != nil {
				t.Fatalf("ConvertGeminiRequestToGemini returned error: %v", err)
			}
			contents := ojson.GetBytesNoCopy(out, "contents")
			if got := contents.Get("#").Int(); got != 3 {
				t.Fatalf("contents length = %d, want 3; out=%s", got, out)
			}
			if got := contents.Get("2.parts.#").Int(); got != 3 {
				t.Fatalf("contents[2].parts length = %d, want 3; out=%s", got, out)
			}
			errPair := signature.ValidateGeminiFunctionCallPairing(out)
			if errPair == nil || !strings.Contains(errPair.Error(), "functionResponse count 3 does not match pending functionCall count 2") {
				t.Fatalf("expected count mismatch error 3 vs 2, got: %v\nout=%s", errPair, out)
			}
		})
	}
}

func TestConvertGeminiRequestToGemini_PreservesEmptyContentBoundaryBeforeFunctionResponse(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{
			name: "empty_parts_array",
			input: `{
				"contents": [
					{"role": "user", "parts": [{"text": "call echo"}]},
					{"role": "model", "parts": [{"functionCall": {"id": "call_1", "name": "echo", "args": {"x": "a"}}}]},
					{"role": "user", "parts": []},
					{"role": "user", "parts": [{"functionResponse": {"id": "call_1", "name": "echo", "response": {"result": "res_a"}}}]}
				]
			}`,
		},
		{
			name: "missing_parts_field",
			input: `{
				"contents": [
					{"role": "user", "parts": [{"text": "call echo"}]},
					{"role": "model", "parts": [{"functionCall": {"id": "call_1", "name": "echo", "args": {"x": "a"}}}]},
					{"role": "user"},
					{"role": "user", "parts": [{"functionResponse": {"id": "call_1", "name": "echo", "response": {"result": "res_a"}}}]}
				]
			}`,
		},
		{
			name: "empty_parts_after_prior_grouped_turn_slow_path",
			input: `{
				"contents": [
					{"role": "user", "parts": [{"text": "first turn"}]},
					{"role": "model", "parts": [
						{"functionCall": {"id": "c1", "name": "echo", "args": {}}},
						{"functionCall": {"id": "c2", "name": "echo", "args": {}}}
					]},
					{"role": "user", "parts": [{"functionResponse": {"id": "c1", "name": "echo", "response": {"ok": true}}}]},
					{"role": "user", "parts": [{"functionResponse": {"id": "c2", "name": "echo", "response": {"ok": true}}}]},
					{"role": "model", "parts": [{"functionCall": {"id": "c3", "name": "echo", "args": {}}}]},
					{"role": "user", "parts": []},
					{"role": "user", "parts": [{"functionResponse": {"id": "c3", "name": "echo", "response": {"ok": true}}}]}
				]
			}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := ConvertGeminiRequestToGemini("gemini-4-neon", []byte(tc.input), false)
			if err != nil {
				t.Fatalf("ConvertGeminiRequestToGemini returned error: %v", err)
			}
			errPair := signature.ValidateGeminiFunctionCallPairing(out)
			if errPair == nil || !strings.Contains(errPair.Error(), "content appears before 1 pending functionResponse part(s)") {
				t.Fatalf("expected empty content boundary error to be preserved, got: %v\nout=%s", errPair, out)
			}
		})
	}
}

func TestConvertGeminiRequestToGemini_PreservesInvalidFunctionCallInNonModelTurn(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr string
	}{
		{
			name: "interleaved_function_call_and_response_in_user_turn",
			input: `{
				"contents": [
					{"role": "user", "parts": [{"text": "call echo"}]},
					{"role": "model", "parts": [{"functionCall": {"id": "call_1", "name": "echo", "args": {"x": "a"}}}]},
					{"role": "user", "parts": [
						{"functionResponse": {"id": "call_1", "name": "echo", "response": {"result": "res_a"}}},
						{"functionCall": {"id": "call_2", "name": "echo", "args": {"x": "b"}}}
					]}
				]
			}`,
			wantErr: "functionCall and functionResponse parts must not be interleaved in the same content",
		},
		{
			name: "user_turn_with_function_call_before_pending_response",
			input: `{
				"contents": [
					{"role": "user", "parts": [{"text": "call echo"}]},
					{"role": "model", "parts": [{"functionCall": {"id": "call_1", "name": "echo", "args": {"x": "a"}}}]},
					{"role": "user", "parts": [{"functionCall": {"id": "call_2", "name": "echo", "args": {"x": "b"}}}]},
					{"role": "user", "parts": [{"functionResponse": {"id": "call_1", "name": "echo", "response": {"result": "res_a"}}}]}
				]
			}`,
			wantErr: "functionCall appears before 1 pending functionResponse part(s)",
		},
		{
			name: "interleaved_user_turn_after_prior_grouped_turn_slow_path",
			input: `{
				"contents": [
					{"role": "user", "parts": [{"text": "first turn"}]},
					{"role": "model", "parts": [
						{"functionCall": {"id": "c1", "name": "echo", "args": {}}},
						{"functionCall": {"id": "c2", "name": "echo", "args": {}}}
					]},
					{"role": "user", "parts": [{"functionResponse": {"id": "c1", "name": "echo", "response": {"ok": 1}}}]},
					{"role": "user", "parts": [{"functionResponse": {"id": "c2", "name": "echo", "response": {"ok": 2}}}]},
					{"role": "model", "parts": [{"functionCall": {"id": "c3", "name": "echo", "args": {}}}]},
					{"role": "user", "parts": [
						{"functionResponse": {"id": "c3", "name": "echo", "response": {"ok": 3}}},
						{"functionCall": {"id": "c4", "name": "echo", "args": {}}}
					]}
				]
			}`,
			wantErr: "functionCall and functionResponse parts must not be interleaved in the same content",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := ConvertGeminiRequestToGemini("gemini-4-neon", []byte(tc.input), false)
			if err != nil {
				t.Fatalf("ConvertGeminiRequestToGemini returned error: %v", err)
			}
			errPair := signature.ValidateGeminiFunctionCallPairing(out)
			if errPair == nil || !strings.Contains(errPair.Error(), tc.wantErr) {
				t.Fatalf("expected pairing error containing %q, got: %v\nout=%s", tc.wantErr, errPair, out)
			}
		})
	}
}
