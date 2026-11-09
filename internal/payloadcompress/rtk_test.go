package payloadcompress

import (
	"strconv"
	"strings"
	"testing"
)

// TestRTKIntensityLadder pins the upstream ladder: intensity scales only the
// line budget (minimal 1.5x, standard 1.0x, aggressive 0.5x of base 120). The
// char cap and dedup threshold are intensity-independent.
func TestRTKIntensityLadder(t *testing.T) {
	cases := []struct {
		intensity string
		maxLines  int
		maxChars  int
		head      int
		tail      int
		minRun    int
	}{
		{"minimal", 180, 12000, 24, 24, 3},
		{"standard", 120, 12000, 24, 24, 3},
		{"", 180, 12000, 24, 24, 3},
		{"aggressive", 60, 12000, 16, 16, 3},
	}
	for _, tc := range cases {
		got := rtkEngine{opts: StepOptions{Intensity: tc.intensity}}.limits()
		if got.maxLines != tc.maxLines || got.maxChars != tc.maxChars {
			t.Errorf("%q: caps = %d/%d, want %d/%d", tc.intensity, got.maxLines, got.maxChars, tc.maxLines, tc.maxChars)
		}
		if got.preserveHead != tc.head || got.preserveTail != tc.tail {
			t.Errorf("%q: preserve = %d/%d, want %d/%d", tc.intensity, got.preserveHead, got.preserveTail, tc.head, tc.tail)
		}
		if got.minRun != tc.minRun {
			t.Errorf("%q: minRun = %d, want %d", tc.intensity, got.minRun, tc.minRun)
		}
	}
}

// TestRTKRepeatedLineMarkers pins upstream's three-line renderer.
func TestRTKRepeatedLineMarkers(t *testing.T) {
	lines := []string{"intro"}
	for i := 0; i < 8; i++ {
		lines = append(lines, "filler "+strconv.Itoa(i))
	}
	alpha := "alpha beta"
	lines = append(lines, alpha, alpha, alpha, alpha, "outro")
	text := strings.Join(lines, "\n")

	out, ok := rtkEngine{}.compressOutput("", text, false)
	if !ok {
		t.Fatal("expected compression")
	}
	want := "[line repeated 3x]\n[rtk:dropped 3 repeated lines]"
	if !strings.Contains(out, want) {
		t.Fatalf("missing upstream marker pair %q in:\n%s", want, out)
	}
	if strings.Contains(out, " (×") {
		t.Fatalf("old single-line marker still present:\n%s", out)
	}
}
