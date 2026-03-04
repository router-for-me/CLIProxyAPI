package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponsesRequestToGemini_MapStopFromOpenAIStop(t *testing.T) {
	in := []byte(`{
		"model":"gemini-2.5-pro",
		"input":"hello",
		"stop":["END"]
	}`)

	out := ConvertOpenAIResponsesRequestToGemini("gemini-2.5-pro", in, false)
	root := gjson.ParseBytes(out)
	stop := root.Get("generationConfig.stopSequences")

	if !stop.Exists() || !stop.IsArray() {
		t.Fatalf("generationConfig.stopSequences missing, output=%s", string(out))
	}
	if len(stop.Array()) != 1 || stop.Array()[0].String() != "END" {
		t.Fatalf("unexpected stopSequences: %s", stop.Raw)
	}
}

func TestConvertOpenAIResponsesRequestToGemini_KeepFunctionCallOutputObject(t *testing.T) {
	in := []byte(`{
		"model":"gemini-2.5-pro",
		"input":[
			{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_1","output":{"k":1,"ok":true}}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToGemini("gemini-2.5-pro", in, false)
	root := gjson.ParseBytes(out)
	result := root.Get("contents.1.parts.0.functionResponse.response.result")

	if !result.Exists() {
		t.Fatalf("functionResponse.response.result missing, output=%s", string(out))
	}
	if result.Get("k").Int() != 1 {
		t.Fatalf("function output object lost field k, got=%s output=%s", result.Raw, string(out))
	}
	if !result.Get("ok").Bool() {
		t.Fatalf("function output object lost field ok, got=%s output=%s", result.Raw, string(out))
	}
}

func TestConvertOpenAIResponsesRequestToGemini_MapToolChoiceRequired(t *testing.T) {
	in := []byte(`{
		"model":"gemini-2.5-pro",
		"input":"hello",
		"tool_choice":"required",
		"tools":[{"type":"function","name":"f","parameters":{"type":"object"}}]
	}`)

	out := ConvertOpenAIResponsesRequestToGemini("gemini-2.5-pro", in, false)
	root := gjson.ParseBytes(out)
	mode := root.Get("toolConfig.functionCallingConfig.mode").String()
	if mode != "ANY" {
		t.Fatalf("tool_choice required should map to ANY, got=%q output=%s", mode, string(out))
	}
}
