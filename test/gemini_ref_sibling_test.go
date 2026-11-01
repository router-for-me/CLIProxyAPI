package test

import (
	"testing"

	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestIssue6897_GeminiToolParametersPreserveRefSiblingConstraints(t *testing.T) {
	schema := `{"$schema":"https://json-schema.org/draft/2020-12/schema","$defs":{"A":{"type":"string","minLength":5}},"type":"object","properties":{"x":{"$ref":"#/$defs/A","minLength":1}}}`
	cases := []struct {
		name   string
		format sdktranslator.Format
		body   string
	}{
		{
			name: "chat_completions", format: sdktranslator.FormatOpenAI,
			body: `{"messages":[{"role":"user","content":"test"}],"tools":[{"type":"function","function":{"name":"check","parameters":` + schema + `}}]}`,
		},
		{
			name: "responses", format: sdktranslator.FormatOpenAIResponse,
			body: `{"input":"test","tools":[{"type":"function","name":"check","parameters":` + schema + `}]}`,
		},
		{
			name: "claude", format: sdktranslator.FormatClaude,
			body: `{"messages":[{"role":"user","content":"test"}],"tools":[{"name":"check","input_schema":` + schema + `}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := sdktranslator.TranslateRequest(tc.format, sdktranslator.FormatGemini, "gemini-3-flash", []byte(tc.body), false)
			parameters := gjson.GetBytes(out, "tools.0.functionDeclarations.0.parametersJsonSchema")
			if parameters.Get("properties.x.allOf.0.minLength").Int() != 5 || parameters.Get("properties.x.minLength").Int() != 1 {
				t.Fatalf("translated tool parameters lost the reference conjunction: %s", out)
			}
			if parameters.Get("$defs").Exists() || parameters.Get("properties.x.$ref").Exists() {
				t.Fatalf("local reference was not fully expanded: %s", out)
			}
		})
	}
}

func TestIssue7149_GeminiToolParametersNumericConstraintsNormalizedAcrossTargets(t *testing.T) {
	geminiBody := `{
		"contents":[{"role":"user","parts":[{"text":"hi"}]}],
		"tools":[{
			"functionDeclarations":[{
				"name":"run_command",
				"description":"Run a command",
				"parameters":{
					"type":"OBJECT",
					"minProperties":"1",
					"maxProperties":"10",
					"properties":{
						"Message":{
							"type":"STRING",
							"minLength":"1",
							"maxLength":"200",
							"enum":["1","2"],
							"default":"1"
						},
						"Items":{
							"type":"ARRAY",
							"minItems":"1",
							"maxItems":"5",
							"items":{"type":"STRING","minLength":"2"}
						}
					}
				}
			}]
		}]
	}`

	targets := []struct {
		name       string
		to         sdktranslator.Format
		schemaPath string
	}{
		{"gemini", sdktranslator.FormatGemini, "tools.0.function_declarations.0.parametersJsonSchema"},
		{"antigravity", sdktranslator.FormatAntigravity, "request.tools.0.functionDeclarations.0.parametersJsonSchema"},
		{"openai", sdktranslator.FormatOpenAI, "tools.0.function.parameters"},
		{"claude", sdktranslator.FormatClaude, "tools.0.input_schema"},
		{"codex", sdktranslator.FormatCodex, "tools.0.parameters"},
		{"interactions", sdktranslator.FormatInteractions, "tools.0.parameters"},
	}

	for _, tc := range targets {
		t.Run(tc.name, func(t *testing.T) {
			out := sdktranslator.TranslateRequest(sdktranslator.FormatGemini, tc.to, "gemini-2.5-pro", []byte(geminiBody), false)
			schema := gjson.GetBytes(out, tc.schemaPath)
			if !schema.Exists() {
				t.Fatalf("expected schema at %s, got: %s", tc.schemaPath, out)
			}
			for _, check := range []struct {
				path string
				want int64
			}{
				{"minProperties", 1},
				{"maxProperties", 10},
				{"properties.Message.minLength", 1},
				{"properties.Message.maxLength", 200},
				{"properties.Items.minItems", 1},
				{"properties.Items.maxItems", 5},
				{"properties.Items.items.minLength", 2},
			} {
				got := schema.Get(check.path)
				if got.Type != gjson.Number || got.Int() != check.want {
					t.Errorf("[%s] %s = %s (type %v), want number %d; schema=%s", tc.name, check.path, got.Raw, got.Type, check.want, schema.Raw)
				}
			}
			if got := schema.Get("properties.Message.enum.0"); got.Type != gjson.String || got.String() != "1" {
				t.Errorf("[%s] enum.0 = %s (type %v), want string \"1\"", tc.name, got.Raw, got.Type)
			}
			if got := schema.Get("properties.Message.default"); got.Type != gjson.String || got.String() != "1" {
				t.Errorf("[%s] default = %s (type %v), want string \"1\"", tc.name, got.Raw, got.Type)
			}
		})
	}
}
