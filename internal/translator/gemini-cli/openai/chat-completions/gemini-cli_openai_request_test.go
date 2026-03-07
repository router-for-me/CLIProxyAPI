package chat_completions

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIRequestToGeminiCLI_KeepToolResultObject(t *testing.T) {
	in := []byte(`{
		"model":"gemini-2.5-pro",
		"messages":[
			{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":{"k":1,"ok":true}}
		]
	}`)

	out := ConvertOpenAIRequestToGeminiCLI("gemini-2.5-pro", in, false)
	root := gjson.ParseBytes(out)
	result := root.Get("request.contents.1.parts.0.functionResponse.response.result")
	if !result.Exists() {
		t.Fatalf("tool result missing, output=%s", string(out))
	}
	if result.Get("k").Int() != 1 || !result.Get("ok").Bool() {
		t.Fatalf("tool result object mismatch, got=%s output=%s", result.Raw, string(out))
	}
}

func TestConvertOpenAIRequestToGeminiCLI_MapMaxStopAndToolChoice(t *testing.T) {
	in := []byte(`{
		"model":"gemini-2.5-pro",
		"max_tokens":123,
		"stop":["END","DONE"],
		"tool_choice":"required",
		"messages":[{"role":"user","content":"hello"}],
		"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{}}}}]
	}`)

	out := ConvertOpenAIRequestToGeminiCLI("gemini-2.5-pro", in, false)
	root := gjson.ParseBytes(out)
	if got := root.Get("request.generationConfig.maxOutputTokens").Int(); got != 123 {
		t.Fatalf("maxOutputTokens mismatch, got=%d output=%s", got, string(out))
	}
	if got := root.Get("request.generationConfig.stopSequences.0").String(); got != "END" {
		t.Fatalf("stopSequences[0] mismatch, got=%q output=%s", got, string(out))
	}
	if got := root.Get("request.generationConfig.stopSequences.1").String(); got != "DONE" {
		t.Fatalf("stopSequences[1] mismatch, got=%q output=%s", got, string(out))
	}
	if got := root.Get("request.toolConfig.functionCallingConfig.mode").String(); got != "ANY" {
		t.Fatalf("functionCallingConfig.mode mismatch, got=%q output=%s", got, string(out))
	}
}

func TestConvertOpenAIRequestToGeminiCLI_MaxCompletionTokensPrecedence(t *testing.T) {
	in := []byte(`{
		"model":"gemini-2.5-pro",
		"max_tokens":123,
		"max_completion_tokens":456,
		"messages":[{"role":"user","content":"hello"}]
	}`)

	out := ConvertOpenAIRequestToGeminiCLI("gemini-2.5-pro", in, false)
	root := gjson.ParseBytes(out)
	if got := root.Get("request.generationConfig.maxOutputTokens").Int(); got != 456 {
		t.Fatalf("max_completion_tokens should override max_tokens, got=%d output=%s", got, string(out))
	}
}

func TestConvertOpenAIRequestToGeminiCLI_ToolChoiceFunctionAllowedName(t *testing.T) {
	in := []byte(`{
		"model":"gemini-2.5-pro",
		"tool_choice":{"type":"function","function":{"name":"weather"}},
		"messages":[{"role":"user","content":"hello"}],
		"tools":[{"type":"function","function":{"name":"weather","parameters":{"type":"object","properties":{}}}}]
	}`)

	out := ConvertOpenAIRequestToGeminiCLI("gemini-2.5-pro", in, false)
	root := gjson.ParseBytes(out)
	if got := root.Get("request.toolConfig.functionCallingConfig.mode").String(); got != "ANY" {
		t.Fatalf("mode mismatch, got=%q output=%s", got, string(out))
	}
	if got := root.Get("request.toolConfig.functionCallingConfig.allowedFunctionNames.0").String(); got != "weather" {
		t.Fatalf("allowedFunctionNames mismatch, got=%q output=%s", got, string(out))
	}
}

func TestConvertOpenAIRequestToGeminiCLI_RemoteImageURLToFileData(t *testing.T) {
	in := []byte(`{
		"model":"gemini-2.5-pro",
		"messages":[
			{
				"role":"user",
				"content":[{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]
			}
		]
	}`)

	out := ConvertOpenAIRequestToGeminiCLI("gemini-2.5-pro", in, false)
	root := gjson.ParseBytes(out)
	if got := root.Get("request.contents.0.parts.0.fileData.fileUri").String(); got != "https://example.com/a.png" {
		t.Fatalf("remote image should map to fileData.fileUri, got=%q output=%s", got, string(out))
	}
}

func TestConvertOpenAIRequestToGeminiCLI_ParseJSONStringToolResult(t *testing.T) {
	in := []byte(`{
		"model":"gemini-2.5-pro",
		"messages":[
			{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"{\"k\":1,\"ok\":true}"}
		]
	}`)

	out := ConvertOpenAIRequestToGeminiCLI("gemini-2.5-pro", in, false)
	root := gjson.ParseBytes(out)
	result := root.Get("request.contents.1.parts.0.functionResponse.response.result")
	if !result.Exists() {
		t.Fatalf("tool result missing, output=%s", string(out))
	}
	if result.Get("k").Int() != 1 || !result.Get("ok").Bool() {
		t.Fatalf("json string tool result should parse into object, got=%s output=%s", result.Raw, string(out))
	}
}
