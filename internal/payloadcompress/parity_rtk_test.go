package payloadcompress

import (
	"strconv"
	"strings"
	"testing"
)

// rtkFiller builds n distinct short lines so truncation is driven by the line
// budget, not the char budget.
func rtkFiller(n int) string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = "output row " + strconv.Itoa(i) + " ok"
	}
	return strings.Join(lines, "\n")
}

// TestRTKParityIntensityBudgets checks the behavioural effect: aggressive cuts
// above 60 lines, standard above 120, minimal and empty (the upstream default)
// above 180.
func TestRTKParityIntensityBudgets(t *testing.T) {
	cases := []struct {
		intensity string
		lines     int
		truncated bool
		dropped   int
	}{
		{"minimal", 130, false, 0},
		{"aggressive", 70, true, 70 - 32},
		{"standard", 130, true, 130 - 48},
		{"", 130, false, 0},
		{"minimal", 190, true, 190 - 48},
	}
	for _, tc := range cases {
		out, ok := rtkEngine{opts: StepOptions{Intensity: tc.intensity}}.compressOutput("ls", rtkFiller(tc.lines), true)
		if !ok && tc.truncated {
			t.Fatalf("%q with %d lines: expected truncation", tc.intensity, tc.lines)
		}
		if ok && !tc.truncated {
			t.Fatalf("%q with %d lines: unexpected truncation:\n%s", tc.intensity, tc.lines, out)
		}
		marker := "[rtk:truncated " + strconv.Itoa(tc.dropped) + " lines]"
		if tc.truncated && !strings.Contains(out, marker) {
			t.Fatalf("%q: missing %q in:\n%s", tc.intensity, marker, out)
		}
	}
}

// TestRTKParityCharCapNotScaled keeps the 12000-char cap identical across
// intensities.
func TestRTKParityCharCapNotScaled(t *testing.T) {
	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, "row "+strconv.Itoa(i)+" "+strings.Repeat("x", 700))
	}
	text := strings.Join(lines, "\n")
	if charLen(text) <= 12000 {
		t.Fatalf("fixture too small: %d chars", charLen(text))
	}
	for _, intensity := range []string{"minimal", "standard", "", "aggressive"} {
		out, ok := rtkEngine{opts: StepOptions{Intensity: intensity}}.compressOutput("ls", text, true)
		if !ok || !strings.Contains(out, "[rtk:truncated by chars]") {
			t.Fatalf("%q: char cap not applied (ok=%v):\n%s", intensity, ok, out)
		}
	}
}

// TestRTKParityLineTruncateHeadTail checks marker text and head/tail placement
// match upstream smartTruncate.
func TestRTKParityLineTruncateHeadTail(t *testing.T) {
	out, ok := rtkEngine{opts: StepOptions{Intensity: "standard"}}.compressOutput("ls", rtkFiller(200), true)
	if !ok {
		t.Fatal("expected truncation")
	}
	got := strings.Split(out, "\n")
	if got[rtkEngine{}.limits().preserveHead] != "[rtk:truncated 152 lines]" {
		t.Fatalf("marker not after head block:\n%s", out)
	}
	if !strings.HasSuffix(out, "output row 199 ok") {
		t.Fatalf("tail not preserved:\n%s", out)
	}
}
