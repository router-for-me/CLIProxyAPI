package executor

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestStripCodexIntermediaryUpdatesFromPayloadTopLevelInstructions(t *testing.T) {
	payload := []byte(`{"instructions":"before\n\n## Intermediary updates \n- noisy updates\n- more noise\n## Final answer instructions\nkeep this","input":[{"role":"user","content":"hello"}]}`)

	out := stripCodexIntermediaryUpdatesFromPayload(payload)
	instructions := gjson.GetBytes(out, "instructions").String()

	if strings.Contains(instructions, "Intermediary updates") || strings.Contains(instructions, "noisy updates") {
		t.Fatalf("intermediary section was not stripped: %q", instructions)
	}
	if !strings.Contains(instructions, "before") || !strings.Contains(instructions, "## Final answer instructions") {
		t.Fatalf("surrounding instructions were not preserved: %q", instructions)
	}
}

func TestStripCodexIntermediaryUpdatesFromPayloadLeavesNestedInputText(t *testing.T) {
	payload := []byte(`{"instructions":"keep","input":[{"content":[{"type":"input_text","text":"alpha\n## Intermediary updates\nremove me"}]}],"reasoning":{"effort":"high"}}`)

	out := stripCodexIntermediaryUpdatesFromPayload(payload)
	text := gjson.GetBytes(out, "input.0.content.0.text").String()

	if !strings.Contains(text, "Intermediary updates") || !strings.Contains(text, "remove me") {
		t.Fatalf("nested user text was stripped unexpectedly: %q", text)
	}
	if !strings.Contains(text, "alpha") {
		t.Fatalf("nested prompt prefix was not preserved: %q", text)
	}
	if got := gjson.GetBytes(out, "reasoning.effort").String(); got != "high" {
		t.Fatalf("unrelated fields changed: reasoning.effort=%q", got)
	}
}

func TestStripCodexIntermediaryUpdatesFromPayloadOnlyTouchesInstructions(t *testing.T) {
	payload := []byte(`{"instructions":"keep\n## Intermediary updates\nremove me\n## Final answer instructions\nanswer","input":[{"content":[{"type":"input_text","text":"alpha\n## Intermediary updates\nkeep this user text"}]}]}`)

	out := stripCodexIntermediaryUpdatesFromPayload(payload)
	instructions := gjson.GetBytes(out, "instructions").String()
	text := gjson.GetBytes(out, "input.0.content.0.text").String()

	if strings.Contains(instructions, "Intermediary updates") || strings.Contains(instructions, "remove me") {
		t.Fatalf("instructions intermediary section was not stripped: %q", instructions)
	}
	if !strings.Contains(instructions, "## Final answer instructions") {
		t.Fatalf("following instruction section was not preserved: %q", instructions)
	}
	if !strings.Contains(text, "Intermediary updates") || !strings.Contains(text, "keep this user text") {
		t.Fatalf("nested user text was stripped unexpectedly: %q", text)
	}
}

func TestStripCodexIntermediaryUpdatesFromPayloadLeavesUnrelatedPayload(t *testing.T) {
	payload := []byte(`{"instructions":"inline mention of ## Intermediary updates should stay","input":[]}`)

	out := stripCodexIntermediaryUpdatesFromPayload(payload)
	if string(out) != string(payload) {
		t.Fatalf("payload changed unexpectedly:\ngot  %s\nwant %s", string(out), string(payload))
	}
}

func TestStripCodexIntermediaryUpdatesTextRemovesLastSection(t *testing.T) {
	out, changed := stripCodexIntermediaryUpdatesText("before\n## Intermediary updates\n- remove me")
	if !changed {
		t.Fatal("expected text to change")
	}
	if strings.Contains(out, "Intermediary updates") || strings.Contains(out, "remove me") {
		t.Fatalf("last section was not stripped: %q", out)
	}
	if !strings.Contains(out, "before") {
		t.Fatalf("prefix was not preserved: %q", out)
	}
}

func TestStripCodexIntermediaryUpdatesTextStopsAtMarkdownHeading(t *testing.T) {
	out, changed := stripCodexIntermediaryUpdatesText("before\n## Intermediary updates\n- remove me\n## Intermediary updates\n- remove me too\n### Keep this\nkeep")
	if !changed {
		t.Fatal("expected text to change")
	}
	if strings.Contains(out, "Intermediary updates") || strings.Contains(out, "remove me") {
		t.Fatalf("intermediary sections were not stripped: %q", out)
	}
	if !strings.Contains(out, "before") || !strings.Contains(out, "### Keep this\nkeep") {
		t.Fatalf("surrounding markdown was not preserved: %q", out)
	}
}
