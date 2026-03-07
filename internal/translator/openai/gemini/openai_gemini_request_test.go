package gemini

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertGeminiRequestToOpenAI_MapStopString(t *testing.T) {
	in := []byte(`{
		"generationConfig":{
			"stop":"END"
		},
		"contents":[{"role":"user","parts":[{"text":"hello"}]}]
	}`)

	out := ConvertGeminiRequestToOpenAI("gemini-2.5-pro", in, false)
	root := gjson.ParseBytes(out)
	if got := root.Get("stop.0").String(); got != "END" {
		t.Fatalf("stop should map from generationConfig.stop, got=%q output=%s", got, string(out))
	}
}

func TestConvertGeminiRequestToOpenAI_ToolChoiceAllowedFunctionName(t *testing.T) {
	in := []byte(`{
		"toolConfig":{
			"functionCallingConfig":{
				"mode":"ANY",
				"allowedFunctionNames":["weather"]
			}
		},
		"contents":[{"role":"user","parts":[{"text":"hello"}]}]
	}`)

	out := ConvertGeminiRequestToOpenAI("gemini-2.5-pro", in, false)
	root := gjson.ParseBytes(out)
	if got := root.Get("tool_choice.type").String(); got != "function" {
		t.Fatalf("tool_choice.type mismatch, got=%q output=%s", got, string(out))
	}
	if got := root.Get("tool_choice.function.name").String(); got != "weather" {
		t.Fatalf("tool_choice.function.name mismatch, got=%q output=%s", got, string(out))
	}
}
