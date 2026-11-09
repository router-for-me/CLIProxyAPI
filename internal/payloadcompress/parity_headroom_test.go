package payloadcompress

import (
	"strconv"
	"strings"
	"testing"
)

func headroomArrayJSON(n int) string {
	var b strings.Builder
	b.WriteByte('[')
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"id":`)
		b.WriteString(strconv.Itoa(i))
		b.WriteString(`,"name":"row-`)
		b.WriteString(strconv.Itoa(i))
		b.WriteString(`"}`)
	}
	b.WriteByte(']')
	return b.String()
}

func TestParityHeadroomEmbeddedInProse(t *testing.T) {
	// Upstream crushText leaves a bare array inside prose alone: splicing a
	// columnar table into arbitrary text would corrupt it.
	in := "Here are the records:\n" + headroomArrayJSON(8) + "\nThat is all."
	out, ok := (headroomEngine{opts: StepOptions{MinRows: 8}}).compress(in)
	if ok || out != in {
		t.Fatalf("bare in-prose array was rewritten:\n%q", out)
	}
}

func TestParityHeadroomJSONFence(t *testing.T) {
	prefix := "Result:\n"
	suffix := "\nDone."
	in := prefix + "```json\n" + headroomArrayJSON(8) + "\n```" + suffix
	out, ok := (headroomEngine{opts: StepOptions{MinRows: 8}}).compress(in)
	if !ok {
		t.Fatal("expected fenced array compaction")
	}
	if !strings.HasPrefix(out, prefix) || !strings.HasSuffix(out, suffix) {
		t.Fatalf("prose was not preserved:\n%q", out)
	}
	if strings.Contains(out, "```json") {
		t.Fatalf("fence was not replaced:\n%s", out)
	}
	if !strings.Contains(out, "# columns=id,name\n") {
		t.Fatalf("missing column header:\n%s", out)
	}
}

func TestParityHeadroomEscapedQuotesAndBrackets(t *testing.T) {
	in := "data:\n```json\n[" +
		`{"id":1,"text":"a]b"},` +
		`{"id":2,"text":"c\"d"},` +
		`{"id":3,"text":"e\\f"},` +
		`{"id":4,"text":"g"},` +
		`{"id":5,"text":"h"},` +
		`{"id":6,"text":"i"},` +
		`{"id":7,"text":"j"},` +
		`{"id":8,"text":"k"}` +
		`]` + "\n``` tail"
	out, ok := (headroomEngine{opts: StepOptions{MinRows: 8}}).compress(in)
	if !ok {
		t.Fatal("expected compaction despite brackets in strings")
	}
	if !strings.HasPrefix(out, "data:\n") || !strings.HasSuffix(out, " tail") {
		t.Fatalf("prose was not preserved:\n%q", out)
	}
	if !strings.Contains(out, `"c\"d"`) {
		t.Fatalf("escaped quote was not preserved:\n%s", out)
	}
	if !strings.Contains(out, "a]b") {
		t.Fatalf("bracket inside string was not preserved:\n%s", out)
	}
}

func TestParityHeadroomBelowMinRowsUntouched(t *testing.T) {
	in := "data: " + headroomArrayJSON(7) + " tail"
	out, ok := (headroomEngine{opts: StepOptions{MinRows: 8}}).compress(in)
	if ok {
		t.Fatalf("array below min rows was compacted:\n%s", out)
	}
	if out != in {
		t.Fatalf("input changed:\n%q", out)
	}
}

func TestParityHeadroomMultipleEmbeddedArrays(t *testing.T) {
	arr := headroomArrayJSON(8)
	in := "first\n```json\n" + arr + "\n```\nmiddle\n```json\n" + arr + "\n```\nlast"
	out, ok := (headroomEngine{opts: StepOptions{MinRows: 8}}).compress(in)
	if !ok {
		t.Fatal("expected both fenced arrays to compact")
	}
	if n := strings.Count(out, "# columns=id,name\n"); n != 2 {
		t.Fatalf("expected 2 compacted arrays, got %d:\n%s", n, out)
	}
	if !strings.HasPrefix(out, "first\n# columns=") || !strings.HasSuffix(out, "\nlast") {
		t.Fatalf("prose was not preserved:\n%q", out)
	}
}

func TestParityHeadroomInvalidJSONUntouched(t *testing.T) {
	in := "start [not, json] end"
	out, ok := (headroomEngine{opts: StepOptions{MinRows: 8}}).compress(in)
	if ok || out != in {
		t.Fatalf("invalid JSON was modified:\n%q", out)
	}
}

func TestParityHeadroomNestedArrayNotCorrupted(t *testing.T) {
	in := "[" + headroomArrayJSON(8) + "]"
	out, ok := (headroomEngine{opts: StepOptions{MinRows: 8}}).compress(in)
	if ok || out != in {
		t.Fatalf("nested array inside a larger array was compacted:\n%q", out)
	}
}

func TestParityHeadroomNonJSONFenceSkipped(t *testing.T) {
	in := "```python\n" + headroomArrayJSON(8) + "\n```\n"
	out, ok := (headroomEngine{opts: StepOptions{MinRows: 8}}).compress(in)
	if ok || out != in {
		t.Fatalf("array inside non-JSON fence was compacted:\n%q", out)
	}
}
