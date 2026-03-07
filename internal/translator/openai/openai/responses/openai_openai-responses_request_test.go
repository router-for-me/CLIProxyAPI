package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_PreserveToolChoiceObject(t *testing.T) {
	in := []byte(`{
		"model":"gpt-5",
		"input":"hello",
		"tool_choice":{"type":"function","function":{"name":"my_tool"}}
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5", in, false)
	root := gjson.ParseBytes(out)
	toolChoice := root.Get("tool_choice")

	if !toolChoice.Exists() {
		t.Fatalf("tool_choice missing, output=%s", string(out))
	}
	if !toolChoice.IsObject() {
		t.Fatalf("tool_choice should be object, got=%s output=%s", toolChoice.Raw, string(out))
	}
	if toolChoice.Get("type").String() != "function" {
		t.Fatalf("tool_choice.type mismatch: got=%q", toolChoice.Get("type").String())
	}
	if toolChoice.Get("function.name").String() != "my_tool" {
		t.Fatalf("tool_choice.function.name mismatch: got=%q", toolChoice.Get("function.name").String())
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_KeepBuiltinTools(t *testing.T) {
	in := []byte(`{
		"model":"gpt-5",
		"input":"hello",
		"tools":[
			{"type":"web_search"},
			{"type":"file_search","vector_store_ids":["vs_1"]}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5", in, false)
	root := gjson.ParseBytes(out)
	tools := root.Get("tools")

	if !tools.Exists() || !tools.IsArray() {
		t.Fatalf("tools missing, output=%s", string(out))
	}
	if len(tools.Array()) != 2 {
		t.Fatalf("unexpected tools length: got=%d output=%s", len(tools.Array()), string(out))
	}
	if tools.Get("0.type").String() != "web_search" {
		t.Fatalf("tool[0] mismatch: %s", tools.Get("0").Raw)
	}
	if tools.Get("1.type").String() != "file_search" {
		t.Fatalf("tool[1] mismatch: %s", tools.Get("1").Raw)
	}
}
