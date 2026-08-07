package claude

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertClaudeRequestToCodexWithCompatPreservesEmptyThinking(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":""}]}]}`)

	withoutCompat := ConvertClaudeRequestToCodex("deepseek-v4", payload, false)
	if gjson.GetBytes(withoutCompat, "input.#").Int() != 0 {
		t.Fatalf("default translation preserved empty-signature thinking: %s", withoutCompat)
	}

	withCompat := ConvertClaudeRequestToCodexWithCompat("deepseek-v4", payload, false)
	if !gjson.GetBytes(withCompat, "input.0.type").Exists() || gjson.GetBytes(withCompat, "input.0.type").String() != "reasoning" {
		t.Fatalf("compat translation missing reasoning item: %s", withCompat)
	}
	if !gjson.GetBytes(withCompat, "input.0.encrypted_content").Exists() {
		t.Fatalf("compat translation missing empty encrypted_content: %s", withCompat)
	}
}
