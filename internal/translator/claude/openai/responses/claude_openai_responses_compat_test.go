package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponsesRequestToClaudeWithCompatPreservesEmptyReasoning(t *testing.T) {
	payload := []byte(`{"input":[{"type":"reasoning","summary":[{"type":"summary_text","text":"reason"}],"encrypted_content":""}]}`)

	withoutCompat := ConvertOpenAIResponsesRequestToClaude("deepseek-v4", payload, false)
	if gjson.GetBytes(withoutCompat, "messages.#").Int() != 0 {
		t.Fatalf("default translation preserved empty reasoning: %s", withoutCompat)
	}

	withCompat := ConvertOpenAIResponsesRequestToClaudeWithCompat("deepseek-v4", payload, false)
	part := gjson.GetBytes(withCompat, "messages.0.content.0")
	if part.Get("type").String() != "thinking" || part.Get("signature").String() != "" {
		t.Fatalf("compat translation missing unsigned thinking block: %s", withCompat)
	}

	opaquePayload := []byte(`{"input":[{"type":"reasoning","summary":[{"type":"summary_text","text":"reason"}],"encrypted_content":"opaque-deepseek-id"}]}`)
	opaqueCompat := ConvertOpenAIResponsesRequestToClaudeWithCompat("deepseek-v4", opaquePayload, false)
	opaquePart := gjson.GetBytes(opaqueCompat, "messages.0.content.0")
	if opaquePart.Get("type").String() != "thinking" || opaquePart.Get("thinking").String() != "reason" || opaquePart.Get("signature").String() != "opaque-deepseek-id" {
		t.Fatalf("compat translation dropped invalid-signature thinking block: %s", opaqueCompat)
	}
}
