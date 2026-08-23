package helps

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestNonThinkingContentParts_RejectsEmptyVisibleParts(t *testing.T) {
	onlyThinking := gjson.Parse(`[{"type":"thinking","thinking":"x","signature":"sig"}]`)
	parts, ok := NonThinkingContentParts(onlyThinking)
	if ok {
		t.Fatalf("content with no visible anchor must fail closed, got %d parts", len(parts))
	}

	redactedOnly := gjson.Parse(`[{"type":"redacted_thinking"}]`)
	parts, ok = NonThinkingContentParts(redactedOnly)
	if ok {
		t.Fatalf("content with only redacted thinking must fail closed, got %d parts", len(parts))
	}
}

func TestNonThinkingContentParts_AcceptsTextOrToolUse(t *testing.T) {
	textOnly := gjson.Parse(`[{"type":"text","text":"hi"}]`)
	parts, ok := NonThinkingContentParts(textOnly)
	if !ok || len(parts) != 1 {
		t.Fatalf("text-only content should produce one visible part, got ok=%v parts=%d", ok, len(parts))
	}

	toolUse := gjson.Parse(`[{"type":"tool_use","id":"t1","name":"x"}]`)
	parts, ok = NonThinkingContentParts(toolUse)
	if !ok || len(parts) != 1 {
		t.Fatalf("tool_use content should produce one visible part, got ok=%v parts=%d", ok, len(parts))
	}
}
