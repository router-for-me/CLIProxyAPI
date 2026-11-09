package payloadcompress

import (
	"strings"
	"testing"
)

// Parity with upstream lite.ts + lossyRequestPolicy.ts safe defaults.

func TestParityLiteDefaultNoTruncation(t *testing.T) {
	text := strings.Repeat("line of tool output\n", 200)
	out, _ := liteEngine{}.compressKind(kindToolOutput, text)
	if strings.Contains(out, toolTruncationMarker) {
		t.Fatalf("lossless default must not truncate: %q", out)
	}
}

func TestParityLiteExplicitTruncation(t *testing.T) {
	enabled := true
	text := strings.Repeat("chatter ", 400)
	out, _ := liteEngine{opts: StepOptions{CompressToolResults: &enabled, MaxToolLength: 300}}.compressKind(kindToolOutput, text)
	if !strings.HasSuffix(out, toolTruncationMarker) {
		t.Fatalf("explicit truncation expected marker, got %q", out)
	}
	if charLen(out) > 300+charLen(toolTruncationMarker) {
		t.Fatalf("truncated result too long: %d", charLen(out))
	}
}

func TestParityLitePreserveToolWhitespaceNil(t *testing.T) {
	text := "keep\n\n\n    indented\n\ttabbed"
	out, _ := liteEngine{}.compressKind(kindToolBlocks, text)
	if out != text {
		t.Fatalf("nil preserve must keep tool layout, got %q", out)
	}
}

func TestParityLitePreserveToolWhitespaceFalse(t *testing.T) {
	disabled := false
	text := "keep\n\n\n    indented"
	out, _ := liteEngine{opts: StepOptions{PreserveToolWhitespace: &disabled}}.compressKind(kindToolBlocks, text)
	if out != "keep\n\n    indented" {
		t.Fatalf("false must collapse blank runs, got %q", out)
	}
}

func TestParityLiteLeavesImageTextAlone(t *testing.T) {
	text := "before data:image/png;base64,iVBORw0KGgo= after"
	out, changed := liteEngine{}.compressKind(kindUser, text)
	if changed || out != text {
		t.Fatalf("string content must stay byte-identical, got %q", out)
	}
}

func TestParityLiteLeavesRepeatedLinesAlone(t *testing.T) {
	text := "alpha\nbeta\nbeta\ngamma"
	out, changed := liteEngine{}.compressKind(kindUser, text)
	if changed || out != text {
		t.Fatalf("duplicate lines inside a message must be kept, got %q", out)
	}
}
