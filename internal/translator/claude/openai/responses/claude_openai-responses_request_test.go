package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponsesRequestToClaude_MapInputStringAndParams(t *testing.T) {
	in := []byte(`{
		"model":"claude-sonnet-4-5",
		"input":"hello",
		"max_output_tokens":256,
		"temperature":0.2,
		"stop":["END"]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-sonnet-4-5", in, false)
	root := gjson.ParseBytes(out)

	if got := root.Get("messages.0.role").String(); got != "user" {
		t.Fatalf("input string should map to user message, got role=%q output=%s", got, string(out))
	}
	if got := root.Get("messages.0.content").String(); got != "hello" {
		t.Fatalf("input string should map to user message content, got=%q output=%s", got, string(out))
	}
	if got := root.Get("max_tokens").Int(); got != 256 {
		t.Fatalf("max_output_tokens mapping mismatch: got=%d output=%s", got, string(out))
	}
	if got := root.Get("temperature").Float(); got != 0.2 {
		t.Fatalf("temperature mapping mismatch: got=%v output=%s", got, string(out))
	}
	stop := root.Get("stop_sequences")
	if !stop.Exists() || !stop.IsArray() || len(stop.Array()) != 1 || stop.Array()[0].String() != "END" {
		t.Fatalf("stop mapping mismatch: %s output=%s", stop.Raw, string(out))
	}
}
