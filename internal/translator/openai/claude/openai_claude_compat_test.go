package claude

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertClaudeRequestToOpenAIWithCompatPreservesEmptySignatureThinking(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":""}]}]}`)

	withoutCompat := ConvertClaudeRequestToOpenAI("deepseek-v4", payload, false)
	if gjson.GetBytes(withoutCompat, "messages.0.reasoning_content").Exists() {
		t.Fatalf("default translation preserved empty-signature reasoning: %s", withoutCompat)
	}

	withCompat := ConvertClaudeRequestToOpenAIWithCompat("deepseek-v4", payload, false)
	if gjson.GetBytes(withCompat, "messages.0.reasoning_content").String() != "reason" {
		t.Fatalf("compat translation missing reasoning_content: %s", withCompat)
	}
}
