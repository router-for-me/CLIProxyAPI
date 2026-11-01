package gemini

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
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

	name := gjson.GetBytes(out, "contents.1.parts.0.functionResponse.name").String()
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

	name0 := gjson.GetBytes(out, "contents.1.parts.0.functionResponse.name").String()
	name1 := gjson.GetBytes(out, "contents.1.parts.1.functionResponse.name").String()
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

	name := gjson.GetBytes(out, "contents.1.parts.0.functionResponse.name").String()
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

	name := gjson.GetBytes(out, "contents.1.parts.0.functionResponse.name").String()
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

	name0 := gjson.GetBytes(out, "contents.1.parts.0.functionResponse.name").String()
	if name0 != "Bash" {
		t.Errorf("Expected first name 'Bash', got '%s'", name0)
	}
	// Second response has no matching call, should remain empty
	name1 := gjson.GetBytes(out, "contents.1.parts.1.functionResponse.name").String()
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

	name0 := gjson.GetBytes(out, "contents.1.parts.0.functionResponse.name").String()
	name1 := gjson.GetBytes(out, "contents.3.parts.0.functionResponse.name").String()
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
	contents := gjson.GetBytes(out, "contents").Array()
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

	decl := gjson.GetBytes(out, "tools.0.function_declarations.0")
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
		if got.Type != gjson.Number || got.Int() != tc.want {
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
		if got.Type != gjson.String || got.String() != tc.want {
			t.Errorf("parametersJsonSchema.%s = %v (type %v), want string %q", tc.path, got.Raw, got.Type, tc.want)
		}
	}
	if got := schema.Get("properties").Map()[`a\b`].Get("minLength"); got.Type != gjson.Number || got.Int() != 11 {
		t.Errorf(`parametersJsonSchema.properties["a\\b"].minLength = %s (type %v), want number 11`, got.Raw, got.Type)
	}
	if _, bogus := schema.Get("properties").Map()["ab"]; bogus {
		t.Errorf(`unexpected fabricated property "ab" in parametersJsonSchema.properties`)
	}
	if got := schema.Get("patternProperties").Map()[`^\d+$`].Get("maxLength"); got.Type != gjson.Number || got.Int() != 12 {
		t.Errorf(`parametersJsonSchema.patternProperties["^\\d+$"].maxLength = %s (type %v), want number 12`, got.Raw, got.Type)
	}

	respSchema := gjson.GetBytes(out, "generationConfig.responseJsonSchema")
	if !respSchema.Exists() {
		t.Fatalf("expected generationConfig.responseJsonSchema to exist, got: %s", out)
	}
	if got := respSchema.Get("minProperties"); got.Type != gjson.Number || got.Int() != 1 {
		t.Errorf("responseJsonSchema.minProperties = %v (type %v), want number 1", got.Raw, got.Type)
	}
	if got := respSchema.Get("properties.Summary.minLength"); got.Type != gjson.Number || got.Int() != 5 {
		t.Errorf("responseJsonSchema.properties.Summary.minLength = %v (type %v), want number 5", got.Raw, got.Type)
	}
}
