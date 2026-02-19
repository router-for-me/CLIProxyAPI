package chat_completions

import (
	"bytes"
	"testing"
)

func TestConvertOpenAIRequestToOpenAI(t *testing.T) {
	input := []byte(`{"model": "gpt-3.5-turbo", "messages": [{"role": "user", "content": "hello"}]}`)
	modelName := "gpt-4o"
	got := ConvertOpenAIRequestToOpenAI(modelName, input, false)

	if !bytes.Contains(got, []byte(`"model": "gpt-4o"`)) && !bytes.Contains(got, []byte(`"model":"gpt-4o"`)) {
		t.Errorf("expected model gpt-4o, got %s", string(got))
	}
}
