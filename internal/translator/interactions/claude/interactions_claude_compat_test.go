package claude

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertClaudeRequestToInteractionsWithCompatPreservesEmptyThinking(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":""}]}]}`)

	withoutCompat := ConvertClaudeRequestToInteractions("deepseek-v4", payload, false)
	if gjson.GetBytes(withoutCompat, "input.#").Int() != 0 {
		t.Fatalf("default translation preserved empty thinking: %s", withoutCompat)
	}

	withCompat := ConvertClaudeRequestToInteractionsWithCompat("deepseek-v4", payload, false)
	if gjson.GetBytes(withCompat, "input.0.type").String() != "thought" {
		t.Fatalf("compat translation missing thought step: %s", withCompat)
	}
}
