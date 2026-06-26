package responses

import (
	"testing"
)

func TestConvertOpenAIResponsesRequestToAntigravity(t *testing.T) {
	input := []byte(`{
		"model": "gpt-4o",
		"instructions": "Be helpful.",
		"input": [
			{
				"role": "user",
				"content": [
					{"type": "input_text", "text": "hello"}
				]
			}
		]
	}`)

	got := ConvertOpenAIResponsesRequestToAntigravity("gpt-4o", input, false)
	if len(got) == 0 {
		t.Errorf("got empty result")
	}
}
