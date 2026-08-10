package chat_completions

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIRequestToClaudeWithCompatPreservesReasoningContent(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"assistant","content":"answer","reasoning_content":"reason"}]}`)

	withoutCompat := ConvertOpenAIRequestToClaude("deepseek-v4", payload, false)
	if gjson.GetBytes(withoutCompat, "messages.0.content.#(type=thinking)").Exists() {
		t.Fatalf("default translation preserved reasoning_content: %s", withoutCompat)
	}

	withCompat := ConvertOpenAIRequestToClaudeWithCompat("deepseek-v4", payload, false)
	part := gjson.GetBytes(withCompat, "messages.0.content.#(type=thinking)")
	if part.Get("thinking").String() != "reason" || part.Get("signature").String() != "" {
		t.Fatalf("compat translation missing unsigned thinking block: %s", withCompat)
	}
}
