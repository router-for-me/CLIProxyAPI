package claude

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestConvertClaudeRequestToInteractionsWithCompatPreservesEmptyThinking(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":""}]}]}`)

	withoutCompat, _ := ConvertClaudeRequestToInteractions("deepseek-v4", payload, false)
	if ojson.GetBytesNoCopy(withoutCompat, "input.#").Int() != 0 {
		t.Fatalf("default translation preserved empty thinking: %s", withoutCompat)
	}

	withCompat, _ := ConvertClaudeRequestToInteractionsWithCompat("deepseek-v4", payload, false)
	if ojson.GetBytesNoCopy(withCompat, "input.0.type").String() != "thought" {
		t.Fatalf("compat translation missing thought step: %s", withCompat)
	}
}
