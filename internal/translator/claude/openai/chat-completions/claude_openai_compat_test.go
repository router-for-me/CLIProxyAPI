package chat_completions

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestConvertOpenAIRequestToClaudeWithCompatPreservesReasoningContent(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"assistant","content":"answer","reasoning_content":"reason"}]}`)

	withoutCompat, _ := ConvertOpenAIRequestToClaude("deepseek-v4", payload, false)
	if ojson.GetBytesNoCopy(withoutCompat, "messages.0.content.#(type=thinking)").Exists() {
		t.Fatalf("default translation preserved reasoning_content: %s", withoutCompat)
	}

	withCompat, _ := ConvertOpenAIRequestToClaudeWithCompat("deepseek-v4", payload, false)
	part := ojson.GetBytesNoCopy(withCompat, "messages.0.content.#(type=thinking)")
	if part.Get("thinking").String() != "reason" || part.Get("signature").String() != "" {
		t.Fatalf("compat translation missing unsigned thinking block: %s", withCompat)
	}
}
