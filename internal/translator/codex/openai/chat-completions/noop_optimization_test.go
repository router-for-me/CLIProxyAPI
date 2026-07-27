package chat_completions

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertCodexResponseToOpenAINonStreamKeepsAssistantRole(t *testing.T) {
	input := []byte(`{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}]}}`)

	output := ConvertCodexResponseToOpenAINonStream(context.Background(), "", nil, nil, input, nil)

	if role := gjson.GetBytes(output, "choices.0.message.role").String(); role != "assistant" {
		t.Fatalf("role = %q, want assistant", role)
	}
}
