package executor

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	_ "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator/builtin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestIssue7000AntigravityClaudeStructuredOutputUsesPromptAndAutoTools(t *testing.T) {
	wantSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"title":       map[string]any{"type": "string"},
			"description": map[string]any{"type": "string"},
		},
		"required":             []any{"title", "description"},
		"additionalProperties": false,
	}

	for _, model := range []string{"claude-opus-5-5-high", "claude-sonnet-5-5-high"} {
		for _, tc := range []struct {
			name    string
			format  sdktranslator.Format
			payload string
			tools   string
		}{
			{
				name:   "responses",
				format: sdktranslator.FormatOpenAIResponse,
				payload: `{
					"input":[{"role":"user","content":[{"type":"input_text","text":"Provide title and description for: fix bug"}]}],
					"text":{"format":{"type":"json_schema","strict":true,"name":"codex_output_schema","schema":{
						"type":"object",
						"properties":{"title":{"type":"string"},"description":{"type":"string"}},
						"required":["title","description"],
						"additionalProperties":false
					}}}
				}`,
				tools: `[{"type":"function","name":"get_time","description":"Get the current time.","parameters":{"type":"object","properties":{"timezone":{"type":"string"}},"required":["timezone"]}}]`,
			},
			{
				name:   "chat",
				format: sdktranslator.FormatOpenAI,
				payload: `{
					"messages":[{"role":"user","content":"Provide title and description for: fix bug"}],
					"response_format":{"type":"json_schema","json_schema":{"strict":true,"name":"codex_output_schema","schema":{
						"type":"object",
						"properties":{"title":{"type":"string"},"description":{"type":"string"}},
						"required":["title","description"],
						"additionalProperties":false
					}}}
				}`,
				tools: `[{"type":"function","function":{"name":"get_time","description":"Get the current time.","parameters":{"type":"object","properties":{"timezone":{"type":"string"}},"required":["timezone"]}}}]`,
			},
		} {
			for _, withTools := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/tools=%t", model, tc.name, withTools), func(t *testing.T) {
					payload, errSet := sjson.SetBytes([]byte(tc.payload), "model", model)
					if errSet != nil {
						t.Fatalf("set model: %v", errSet)
					}
					if withTools {
						payload, errSet = sjson.SetRawBytes(payload, "tools", []byte(tc.tools))
						if errSet != nil {
							t.Fatalf("set tools: %v", errSet)
						}
						payload, errSet = sjson.SetBytes(payload, "tool_choice", "auto")
						if errSet != nil {
							t.Fatalf("set tool_choice: %v", errSet)
						}
					}

					translated := sdktranslator.TranslateRequest(tc.format, sdktranslator.FormatAntigravity, model, payload, false)
					body := buildRequestBodyFromRawPayload(t, model, translated)
					encoded, errMarshal := json.Marshal(body)
					if errMarshal != nil {
						t.Fatalf("marshal outbound request: %v", errMarshal)
					}
					request := gjson.GetBytes(encoded, "request")

					if mode := request.Get("toolConfig.functionCallingConfig.mode").String(); mode != "" && mode != "AUTO" {
						t.Errorf("outbound function calling mode = %q, want AUTO or omitted; Claude 5.5 rejects forced tool use", mode)
					}
					for _, field := range []string{"responseSchema", "responseJsonSchema", "responseMimeType"} {
						if got := request.Get("generationConfig." + field); got.Exists() {
							t.Errorf("outbound generationConfig.%s = %s, want omitted for prompt-based structured output", field, got.Raw)
						}
					}

					var systemText strings.Builder
					for _, part := range request.Get("systemInstruction.parts").Array() {
						systemText.WriteString(part.Get("text").String())
						systemText.WriteByte('\n')
					}
					foundSchema := false
					prompt := systemText.String()
					// Decode embedded JSON without depending on instruction wording or schema key order.
					for offset, char := range prompt {
						if char != '{' {
							continue
						}
						var schema map[string]any
						if errDecode := json.NewDecoder(strings.NewReader(prompt[offset:])).Decode(&schema); errDecode == nil && reflect.DeepEqual(schema, wantSchema) {
							foundSchema = true
							break
						}
					}
					if !foundSchema {
						t.Errorf("system instruction must preserve the title/description schema, required fields, and additionalProperties:false; got %q", prompt)
					}

					if withTools {
						declarations := request.Get("tools.0.functionDeclarations").Array()
						if len(declarations) != 1 {
							t.Fatalf("outbound function declaration count = %d, want 1 real tool; tools = %s", len(declarations), request.Get("tools").Raw)
						}
						for path, want := range map[string]string{
							"name":                                "get_time",
							"description":                         "Get the current time.",
							"parameters.type":                     "object",
							"parameters.properties.timezone.type": "string",
							"parameters.required.0":               "timezone",
						} {
							if got := declarations[0].Get(path).String(); got != want {
								t.Errorf("real tool %s = %q, want %q", path, got, want)
							}
						}
					}
				})
			}
		}
	}
}
