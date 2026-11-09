package payloadcompress

import (
	"strings"
	"testing"
)

func TestLiteDefaultKeepsToolResults(t *testing.T) {
	text := strings.Repeat("abcdefghij", 30)
	out, _ := liteEngine{opts: StepOptions{MaxToolLength: 10}}.compressKind(kindToolOutput, text)
	if out != text {
		t.Fatalf("default must not truncate tool results, got %q", out)
	}
}

func TestLiteExplicitCompressToolResults(t *testing.T) {
	enabled := true
	text := strings.Repeat("abcdefghij", 40)
	out, changed := liteEngine{opts: StepOptions{CompressToolResults: &enabled, MaxToolLength: 300}}.compressKind(kindToolOutput, text)
	if !changed || !strings.HasSuffix(out, toolTruncationMarker) {
		t.Fatalf("explicit opt-in must truncate, got %q", out)
	}
}

func TestLiteMaxToolLengthHonorsLargerValue(t *testing.T) {
	enabled := true
	text := strings.Repeat("abcdefghij", 250) // 2500 chars, below the 3000 budget
	out, changed := liteEngine{opts: StepOptions{CompressToolResults: &enabled, MaxToolLength: 3000}}.compressKind(kindToolOutput, text)
	if changed || out != text {
		t.Fatalf("configured MaxToolLength above 2000 must be honoured, got %q", out)
	}
}

func TestLiteMaxToolLengthZeroUsesDefault(t *testing.T) {
	enabled := true
	text := strings.Repeat("abcdefghij", 250) // 2500 chars, above the 2000 default
	out, changed := liteEngine{opts: StepOptions{CompressToolResults: &enabled}}.compressKind(kindToolOutput, text)
	if !changed || !strings.HasSuffix(out, toolTruncationMarker) {
		t.Fatalf("zero MaxToolLength must fall back to the 2000 default, got %q", out)
	}
}

func TestLiteNeverTruncatesToolCurrent(t *testing.T) {
	enabled := true
	text := strings.Repeat("abcdefghij", 250)
	out, changed := liteEngine{opts: StepOptions{CompressToolResults: &enabled, MaxToolLength: 10}}.compressKind(kindToolCurrent, text)
	if changed || out != text {
		t.Fatalf("latest tool result must be left alone, got %q", out)
	}
}

func TestLitePreserveToolWhitespaceDefaultTrue(t *testing.T) {
	text := "a\n\n\n\nb"
	out, changed := liteEngine{}.compressKind(kindToolOutput, text)
	if changed || out != text {
		t.Fatalf("nil preserve-tool-whitespace must preserve layout, got %q", out)
	}
}

func TestLitePreserveToolWhitespaceFalseCollapses(t *testing.T) {
	disabled := false
	text := "a\n\n\n\nb"
	out, _ := liteEngine{opts: StepOptions{PreserveToolWhitespace: &disabled}}.compressKind(kindToolOutput, text)
	if out != "a\n\nb" {
		t.Fatalf("preserve-tool-whitespace=false must collapse blank runs, got %q", out)
	}
}
