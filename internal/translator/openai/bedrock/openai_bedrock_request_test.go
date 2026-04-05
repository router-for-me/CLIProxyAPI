package bedrock

import (
	"testing"

	. "github.com/router-for-me/CLIProxyAPI/v6/internal/constant"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestParseDataURIImage(t *testing.T) {
	format, data, ok := parseDataURIImage("data:image/png;base64,QUJD")
	if !ok {
		t.Fatal("expected valid data URI image")
	}
	if format != "png" {
		t.Fatalf("format = %q, want %q", format, "png")
	}
	if data != "QUJD" {
		t.Fatalf("data = %q, want %q", data, "QUJD")
	}
}

func TestParseDataURIImage_InvalidMediaType(t *testing.T) {
	if _, _, ok := parseDataURIImage("data:invalid;base64,QUJD"); ok {
		t.Fatal("expected invalid media type to be rejected")
	}
}

func TestConvertOpenAIRequestToBedrock_IgnoresInvalidImageDataURI(t *testing.T) {
	input := []byte(`{
		"messages":[
			{
				"role":"user",
				"content":[
					{"type":"text","text":"hello"},
					{"type":"image_url","image_url":{"url":"data:invalid;base64,QUJD"}}
				]
			}
		]
	}`)

	out := ConvertOpenAIRequestToBedrock("deepseek.r1-v1:0", input, false)
	if !gjson.ValidBytes(out) {
		t.Fatalf("output is not valid json: %s", string(out))
	}
	if gjson.GetBytes(out, "messages.0.content.1.image").Exists() {
		t.Fatalf("invalid image data URI should not emit image block: %s", string(out))
	}
}

func TestConvertOpenAIRequestToBedrock_ToolChoiceMapping(t *testing.T) {
	tests := []struct {
		name       string
		toolChoice string
		validate   func(t *testing.T, out []byte)
	}{
		{
			name:       "none disables tool config",
			toolChoice: `"none"`,
			validate: func(t *testing.T, out []byte) {
				t.Helper()
				if gjson.GetBytes(out, "toolConfig").Exists() {
					t.Fatalf("toolConfig should be omitted for tool_choice none: %s", string(out))
				}
			},
		},
		{
			name:       "auto maps to toolChoice.auto",
			toolChoice: `"auto"`,
			validate: func(t *testing.T, out []byte) {
				t.Helper()
				if !gjson.GetBytes(out, "toolConfig.tools.0.toolSpec.name").Exists() {
					t.Fatalf("tools should be preserved: %s", string(out))
				}
				if !gjson.GetBytes(out, "toolConfig.toolChoice.auto").Exists() {
					t.Fatalf("expected toolChoice.auto: %s", string(out))
				}
			},
		},
		{
			name:       "required maps to toolChoice.any",
			toolChoice: `"required"`,
			validate: func(t *testing.T, out []byte) {
				t.Helper()
				if !gjson.GetBytes(out, "toolConfig.toolChoice.any").Exists() {
					t.Fatalf("expected toolChoice.any: %s", string(out))
				}
			},
		},
		{
			name:       "function maps to named tool choice",
			toolChoice: `{"type":"function","function":{"name":"ReadFile"}}`,
			validate: func(t *testing.T, out []byte) {
				t.Helper()
				if got := gjson.GetBytes(out, "toolConfig.toolChoice.tool.name").String(); got != "ReadFile" {
					t.Fatalf("toolChoice.tool.name = %q, want %q; body=%s", got, "ReadFile", string(out))
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := []byte(`{
				"messages":[{"role":"user","content":"hi"}],
				"tools":[{"type":"function","function":{"name":"ReadFile","parameters":{"type":"object"}}}],
				"tool_choice":` + tt.toolChoice + `
			}`)
			out := ConvertOpenAIRequestToBedrock("us.deepseek.r1-v1:0", input, false)
			if !gjson.ValidBytes(out) {
				t.Fatalf("output is not valid json: %s", string(out))
			}
			tt.validate(t, out)
		})
	}
}

func TestConvertOpenAIRequestToBedrock_ThinkingModelFamilyRouting(t *testing.T) {
	input := []byte(`{
		"thinking":{"type":"enabled","budget_tokens":5000},
		"messages":[{"role":"user","content":"hi"}]
	}`)

	claudeOut := ConvertOpenAIRequestToBedrock("claude", input, false)
	if !gjson.GetBytes(claudeOut, "additionalModelRequestFields.thinking").Exists() {
		t.Fatalf("expected claude thinking payload, body=%s", string(claudeOut))
	}

	glmOut := ConvertOpenAIRequestToBedrock("glm", input, false)
	if !gjson.GetBytes(glmOut, "additionalModelRequestFields.reasoningConfig").Exists() {
		t.Fatalf("expected glm reasoningConfig payload, body=%s", string(glmOut))
	}

	deepSeekOut := ConvertOpenAIRequestToBedrock("deepseek", input, false)
	if gjson.GetBytes(deepSeekOut, "additionalModelRequestFields.thinking").Exists() ||
		gjson.GetBytes(deepSeekOut, "additionalModelRequestFields.reasoningConfig").Exists() {
		t.Fatalf("expected deepseek to omit model-specific thinking fields, body=%s", string(deepSeekOut))
	}
}

func TestConvertOpenAIRequestToBedrock_ExternalImageURLFallsBackToTextBlock(t *testing.T) {
	input := []byte(`{
		"messages":[
			{
				"role":"user",
				"content":[
					{"type":"image_url","image_url":{"url":"https://example.com/cat.png"}}
				]
			}
		]
	}`)

	out := ConvertOpenAIRequestToBedrock("deepseek-v3.2", input, false)
	if !gjson.ValidBytes(out) {
		t.Fatalf("output is not valid json: %s", string(out))
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.text").String(); got != "[Image: https://example.com/cat.png]" {
		t.Fatalf("fallback text = %q, want %q; body=%s", got, "[Image: https://example.com/cat.png]", string(out))
	}
}

func TestConvertOpenAIResponsesRequestToBedrock_ConvertsResponsesSchema(t *testing.T) {
	input := []byte(`{
		"model":"deepseek-v3.2",
		"instructions":"You are helpful",
		"input":"hello from responses"
	}`)

	out := ConvertOpenAIResponsesRequestToBedrock("deepseek-v3.2", input, false)
	if !gjson.ValidBytes(out) {
		t.Fatalf("output is not valid json: %s", string(out))
	}
	if got := gjson.GetBytes(out, "system.0.text").String(); got != "You are helpful" {
		t.Fatalf("system text = %q, want %q; body=%s", got, "You are helpful", string(out))
	}
	if got := gjson.GetBytes(out, "messages.0.role").String(); got != "user" {
		t.Fatalf("role = %q, want %q; body=%s", got, "user", string(out))
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.text").String(); got != "hello from responses" {
		t.Fatalf("content text = %q, want %q; body=%s", got, "hello from responses", string(out))
	}
}

func TestBedrockInitRegistersOpenAIResponsesTransformer(t *testing.T) {
	reqPayload := []byte(`{"model":"deepseek-v3.2","input":"hello"}`)
	got := sdktranslator.TranslateRequest(OpenaiResponse, BedrockConverse, "deepseek-v3.2", reqPayload, false)
	if !gjson.GetBytes(got, "messages.0.content.0.text").Exists() {
		t.Fatalf("expected OpenAI Responses request to be translated to Bedrock messages; got: %s", string(got))
	}
	if !sdktranslator.HasResponseTransformer(OpenaiResponse, BedrockConverse) {
		t.Fatalf("expected response transformer %q -> %q to be registered", OpenaiResponse, BedrockConverse)
	}
}
