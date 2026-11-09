package payloadcompress

import (
	"strconv"
	"strings"
	"testing"
)

func TestMCPAccessibilityDefaultsMatchUpstream(t *testing.T) {
	if mcpDefaultMinLengthToProcess != 2000 {
		t.Fatalf("minLengthToProcess = %d, want 2000", mcpDefaultMinLengthToProcess)
	}
	if mcpDefaultCollapseThreshold != 30 {
		t.Fatalf("collapseThreshold = %d, want 30", mcpDefaultCollapseThreshold)
	}
	if mcpDefaultCollapseKeepHead != 10 {
		t.Fatalf("collapseKeepHead = %d, want 10", mcpDefaultCollapseKeepHead)
	}
	if mcpDefaultCollapseKeepTail != 5 {
		t.Fatalf("collapseKeepTail = %d, want 5", mcpDefaultCollapseKeepTail)
	}
	if mcpDefaultMaxTextChars != 50000 {
		t.Fatalf("maxTextChars = %d, want 50000", mcpDefaultMaxTextChars)
	}
	if mcpAccessibilityTailReserve != 300 {
		t.Fatalf("tailReserve = %d, want 300", mcpAccessibilityTailReserve)
	}
}

func TestMCPAccessibilityRemovesNoiseLines(t *testing.T) {
	text := "- generic:\n- text: \"\"\n- button [ref=e1] Save\n" + strings.Repeat("content line\n", 200)
	out, changed := mcpAccessibilityEngine{}.compress(text)
	if !changed {
		t.Fatal("expected noise removal to change the text")
	}
	if strings.Contains(out, "- generic:") || strings.Contains(out, `- text: ""`) {
		t.Fatalf("noise lines survived: %q", out)
	}
	if !strings.Contains(out, "- button [ref=e1] Save") {
		t.Fatalf("meaningful line was dropped: %q", out)
	}
}

func TestMCPAccessibilityCollapsesSiblingRun(t *testing.T) {
	var b strings.Builder
	b.WriteString(strings.Repeat("x", 1500))
	b.WriteString("\n")
	for i := 1; i <= 40; i++ {
		b.WriteString("- button [ref=e")
		b.WriteString(strconv.Itoa(i))
		b.WriteString("] item\n")
	}
	out, changed := mcpAccessibilityEngine{}.compress(strings.TrimSuffix(b.String(), "\n"))
	if !changed {
		t.Fatal("expected collapse to change the text")
	}
	if !strings.Contains(out, `... [25 similar "button" items omitted by OmniRoute MCP filter]`) {
		t.Fatalf("missing collapse notice: %q", out)
	}
	if !strings.Contains(out, "- button [ref=e1] item") {
		t.Fatalf("head kept item missing: %q", out)
	}
	if !strings.Contains(out, "- button [ref=e40] item") {
		t.Fatalf("tail kept item missing: %q", out)
	}
	if strings.Contains(out, "[ref=e11] item\n- button [ref=e12]") {
		t.Fatalf("middle items were not collapsed: %q", out)
	}
	if !strings.Contains(out, "[refs of omitted \"button\" items (clickable):") {
		t.Fatalf("omitted refs notice missing: %q", out)
	}
	for _, ref := range []string{"[ref=e11]", "[ref=e35]"} {
		if !strings.Contains(out, ref) {
			t.Fatalf("omitted %s anchor lost: %q", ref, out)
		}
	}
}

func TestMCPAccessibilityShortTextUntouched(t *testing.T) {
	text := "- generic:\n- button [ref=e1] Save"
	out, changed := mcpAccessibilityEngine{}.compress(text)
	if changed || out != text {
		t.Fatalf("short text must pass through: got %q changed=%v", out, changed)
	}
}

func TestMCPAccessibilityClampsLargeText(t *testing.T) {
	text := strings.Repeat("a", mcpDefaultMaxTextChars+10)
	out, changed := mcpAccessibilityEngine{}.compress(text)
	if !changed {
		t.Fatal("expected clamp to change the text")
	}
	if !strings.Contains(out, "... [truncated 310 chars by OmniRoute MCP filter.") {
		t.Fatalf("missing truncation notice: %q", out)
	}
	head := strings.SplitN(out, "\n\n", 2)[0]
	if got, want := charLen(head), mcpDefaultMaxTextChars-mcpAccessibilityTailReserve; got != want {
		t.Fatalf("head chars = %d, want %d", got, want)
	}
}

func TestMCPAccessibilityCustomOptions(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 6; i++ {
		b.WriteString("- link [ref=e")
		b.WriteString(strconv.Itoa(i))
		b.WriteString("] item\n")
	}
	e := mcpAccessibilityEngine{opts: StepOptions{
		McpMinLength:         1,
		McpCollapseThreshold: 4,
		McpCollapseKeepHead:  1,
		McpCollapseKeepTail:  1,
	}}
	out, changed := e.compress(strings.TrimSuffix(b.String(), "\n"))
	if !changed {
		t.Fatal("expected custom threshold to trigger collapse")
	}
	if !strings.Contains(out, `[4 similar "link" items omitted`) {
		t.Fatalf("custom collapse counts wrong: %q", out)
	}
}
