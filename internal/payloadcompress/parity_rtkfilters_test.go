package payloadcompress

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// find names the shell-ls pack in its match.commands, but upstream's command
// detector claims only "ls" for that type and "find" for shell-find. Detection
// must follow the detector, not the pack.
func TestRTKFindSelectsShellFind(t *testing.T) {
	cases := []struct {
		name    string
		text    string
		command string
	}{
		{"command", "./src/a.ts\n./src/b.ts", "find . -name '*.ts'"},
		{"output only", "./src/a.ts\n./src/b.ts", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			filter := selectRTKFilter(tc.text, tc.command)
			if filter == nil {
				t.Fatal("no filter selected")
			}
			if filter.id != "shell-find" {
				t.Fatalf("selected %q, want shell-find", filter.id)
			}
		})
	}
}

func TestRTKCustomFilterSources(t *testing.T) {
	project := t.TempDir()
	dataDir := t.TempDir()
	t.Chdir(project)
	t.Setenv("DATA_DIR", dataDir)
	t.Setenv("OMNIROUTE_RTK_TRUST_PROJECT_FILTERS", "1")

	projectFile := filepath.Join(project, ".rtk", "filters.json")
	if err := os.MkdirAll(filepath.Dir(projectFile), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path, body string) {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	projectPack := `{"id":"custom-project","priority":999,"match":{"patterns":["^custom-project"]}}`
	write(projectFile, projectPack)

	globalFile := filepath.Join(dataDir, "rtk", "filters.json")
	if err := os.MkdirAll(filepath.Dir(globalFile), 0o755); err != nil {
		t.Fatal(err)
	}
	write(globalFile, `[{"id":"custom-global","priority":50,"match":{"patterns":["^custom-global"]}}]`)

	filters := loadCustomRTKFilters()
	byID := map[string]*rtkFilter{}
	for _, filter := range filters {
		byID[filter.id] = filter
	}
	projectFilter, ok := byID["custom-project"]
	if !ok {
		t.Fatalf("project pack not loaded: %v", byID)
	}
	globalFilter, ok := byID["custom-global"]
	if !ok {
		t.Fatalf("global pack not loaded: %v", byID)
	}
	if projectFilter.sourceRank <= globalFilter.sourceRank {
		t.Fatalf("project rank %d must outrank global %d", projectFilter.sourceRank, globalFilter.sourceRank)
	}
}

func TestRTKCustomFilterUntrusted(t *testing.T) {
	project := t.TempDir()
	t.Chdir(project)
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("OMNIROUTE_RTK_TRUST_PROJECT_FILTERS", "")

	path := filepath.Join(project, ".rtk", "filters.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"id":"custom-project"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, filter := range loadCustomRTKFilters() {
		if filter.id == "custom-project" {
			t.Fatal("untrusted project pack must not load")
		}
	}
}

func TestRTKParseCustomPackShapes(t *testing.T) {
	array, err := parseRTKFilterPacks([]byte(`[{"id":"a"},{"id":"b"}]`))
	if err != nil || len(array) != 2 {
		t.Fatalf("array: %v, %d", err, len(array))
	}
	one, err := parseRTKFilterPacks([]byte(`{"id":"only"}`))
	if err != nil || len(one) != 1 || one[0].ID != "only" {
		t.Fatalf("object: %v, %+v", err, one)
	}
}

func TestRTKScaleMaxLines(t *testing.T) {
	cases := []struct {
		base      int
		intensity string
		want      int
	}{
		{100, "", 150},
		{100, "standard", 100},
		{100, "minimal", 150},
		{100, "aggressive", 50},
		{1, "aggressive", 1},
		{0, "minimal", 0},
	}
	for _, tc := range cases {
		if got := scaleRTKMaxLines(tc.base, tc.intensity); got != tc.want {
			t.Errorf("scaleRTKMaxLines(%d, %q) = %d, want %d", tc.base, tc.intensity, got, tc.want)
		}
	}
}

func TestRTKFilterIntensityScalesOwnCap(t *testing.T) {
	lines := []string{"l1", "l2", "l3", "l4", "l5", "l6", "l7", "l8", "l9", "l10"}
	text := strings.Join(lines, "\n")
	// maxLines 10 is exactly the input length: standard and minimal leave it
	// untouched, aggressive halves the budget and truncates.
	filter := &rtkFilter{id: "synthetic", maxLines: 10, headLines: 2, tailLines: 2}

	if out, changed := filter.applyWithIntensity(text, ""); changed || out != text {
		t.Fatalf("standard changed output: %q", out)
	}
	if out, changed := filter.applyWithIntensity(text, "minimal"); changed || out != text {
		t.Fatalf("minimal changed output: %q", out)
	}
	out, changed := filter.applyWithIntensity(text, "aggressive")
	if !changed {
		t.Fatal("aggressive did not truncate")
	}
	if !strings.Contains(out, "rtk:truncated") {
		t.Fatalf("aggressive output not truncated: %q", out)
	}
}

// A pack's head/tail truncation must not drop a diagnostic line.
func TestRTKFilterSeverityRescue(t *testing.T) {
	text := "a\nb\nFATAL: boom\nc"
	filter := &rtkFilter{id: "synthetic", maxLines: 2}
	out, changed := filter.apply(text)
	if !changed {
		t.Fatal("expected truncation")
	}
	if !strings.Contains(out, "FATAL: boom") {
		t.Fatalf("severity line dropped: %q", out)
	}
}

// An explicit engine-level MaxLines goes through the same intensity ladder as
// the built-in default; only the char cap stays unscaled.
func TestRTKIntensityScalesExplicitMaxLines(t *testing.T) {
	cases := []struct {
		intensity string
		want      int
	}{
		{"", 150},
		{"minimal", 150},
		{"standard", 100},
		{"aggressive", 50},
	}
	for _, tc := range cases {
		e := rtkEngine{opts: StepOptions{MaxLines: 100, Intensity: tc.intensity}}
		if got := e.limits().maxLines; got != tc.want {
			t.Errorf("intensity %q: maxLines = %d, want %d", tc.intensity, got, tc.want)
		}
	}
	e := rtkEngine{opts: StepOptions{MaxChars: 4000, Intensity: "aggressive"}}
	if got := e.limits().maxChars; got != 4000 {
		t.Errorf("maxChars = %d, want 4000", got)
	}
}

func TestCompileRTKFilterDefaultsHeadTail(t *testing.T) {
	packs, err := parseRTKFilterPacks([]byte(`[{"id":"mine","match":{"patterns":["^X"]},"rules":{"maxLines":100}}]`))
	if err != nil {
		t.Fatal(err)
	}
	f := compileRTKFilter(packs[0])
	if f.headLines != 20 || f.tailLines != 20 {
		t.Fatalf("headLines/tailLines = %d/%d, want upstream defaults 20/20", f.headLines, f.tailLines)
	}
}
