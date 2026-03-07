package chat_completions

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIRequestToGemini_MapMaxTokensAndStop(t *testing.T) {
	in := []byte(`{
		"model":"gemini-2.5-pro",
		"messages":[{"role":"user","content":"hello"}],
		"max_tokens":123,
		"stop":["END"]
	}`)

	out := ConvertOpenAIRequestToGemini("gemini-2.5-pro", in, false)
	root := gjson.ParseBytes(out)

	if got := root.Get("generationConfig.maxOutputTokens").Int(); got != 123 {
		t.Fatalf("max_tokens not mapped, got=%d output=%s", got, string(out))
	}

	stop := root.Get("generationConfig.stopSequences")
	if !stop.Exists() || !stop.IsArray() {
		t.Fatalf("generationConfig.stopSequences missing, output=%s", string(out))
	}
	if len(stop.Array()) != 1 || stop.Array()[0].String() != "END" {
		t.Fatalf("unexpected stopSequences: %s", stop.Raw)
	}
}

func TestConvertOpenAIRequestToGemini_ImageURLMustNotBeDropped(t *testing.T) {
	in := []byte(`{
		"model":"gemini-2.5-pro",
		"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]}]
	}`)

	out := ConvertOpenAIRequestToGemini("gemini-2.5-pro", in, false)
	root := gjson.ParseBytes(out)
	fileURI := root.Get("contents.0.parts.0.fileData.fileUri").String()

	if fileURI != "https://example.com/a.png" {
		t.Fatalf("image_url should map to fileData.fileUri, got=%q output=%s", fileURI, string(out))
	}
}

func TestConvertOpenAIRequestToGemini_FileDataURIShouldStripPrefix(t *testing.T) {
	in := []byte(`{
		"model":"gemini-2.5-pro",
		"messages":[{"role":"user","content":[{"type":"file","file":{"filename":"a.txt","file_data":"data:text/plain;base64,SGVsbG8="}}]}]
	}`)

	out := ConvertOpenAIRequestToGemini("gemini-2.5-pro", in, false)
	root := gjson.ParseBytes(out)

	if got := root.Get("contents.0.parts.0.inlineData.data").String(); got != "SGVsbG8=" {
		t.Fatalf("file_data base64 payload mismatch: got=%q output=%s", got, string(out))
	}
	if got := root.Get("contents.0.parts.0.inlineData.mime_type").String(); got != "text/plain" {
		t.Fatalf("file_data mime mismatch: got=%q output=%s", got, string(out))
	}
}

func TestConvertOpenAIRequestToGemini_MapToolChoiceRequired(t *testing.T) {
	in := []byte(`{
		"model":"gemini-2.5-pro",
		"messages":[{"role":"user","content":"hello"}],
		"tool_choice":"required",
		"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}]
	}`)

	out := ConvertOpenAIRequestToGemini("gemini-2.5-pro", in, false)
	root := gjson.ParseBytes(out)
	mode := root.Get("toolConfig.functionCallingConfig.mode").String()
	if mode != "ANY" {
		t.Fatalf("tool_choice required should map to ANY, got=%q output=%s", mode, string(out))
	}
}

func TestConvertOpenAIRequestToGemini_KeepToolResultObject(t *testing.T) {
	in := []byte(`{
		"model":"gemini-2.5-pro",
		"messages":[
			{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":{"k":1,"ok":true}}
		]
	}`)

	out := ConvertOpenAIRequestToGemini("gemini-2.5-pro", in, false)
	root := gjson.ParseBytes(out)
	result := root.Get("contents.1.parts.0.functionResponse.response.result")
	if !result.Exists() {
		t.Fatalf("tool result missing, output=%s", string(out))
	}
	if result.Get("k").Int() != 1 || !result.Get("ok").Bool() {
		t.Fatalf("tool result object mismatch, got=%s output=%s", result.Raw, string(out))
	}
}
