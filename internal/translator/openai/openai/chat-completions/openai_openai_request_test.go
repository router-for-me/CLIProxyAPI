package chat_completions

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIRequestToOpenAIReusesMatchingModelPayload(t *testing.T) {
	input := []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"hello"}]}`)

	output := ConvertOpenAIRequestToOpenAI("gpt-test", input, false)

	if &output[0] != &input[0] {
		t.Fatal("matching model caused a payload copy")
	}
}

func TestConvertOpenAIRequestToOpenAIUpdatesDifferentModel(t *testing.T) {
	input := []byte(`{"model":"old-model","messages":[]}`)

	output := ConvertOpenAIRequestToOpenAI("new-model", input, false)

	if model := gjson.GetBytes(output, "model").String(); model != "new-model" {
		t.Fatalf("model = %q, want new-model", model)
	}
}
