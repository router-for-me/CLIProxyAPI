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
