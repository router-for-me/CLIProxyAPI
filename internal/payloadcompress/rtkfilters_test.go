package payloadcompress

import (
	"strings"
	"testing"
)

// TestRTKFilterInlineTests replays every expectation shipped with the filter
// data. Upstream authors each pack with its own input/expected pairs, so this
// checks the port against upstream's answers rather than against a reading of
// the rules.
func TestRTKFilterInlineTests(t *testing.T) {
	filters := loadRTKFilters()
	if len(filters) < 50 {
		t.Fatalf("expected at least 50 filters, got %d", len(filters))
	}
	total := 0
	for _, filter := range filters {
		for _, test := range filter.tests {
			total++
			got, _ := filter.apply(test.Input)
			if strings.TrimSpace(got) != strings.TrimSpace(test.Expected) {
				t.Errorf("%s/%s:\n got: %q\nwant: %q", filter.id, test.Name, got, test.Expected)
			}
		}
	}
	if total < 60 {
		t.Fatalf("expected at least 60 inline tests, got %d", total)
	}
}

// TestRTKFilterPacksCompile guards against a pack that silently loses its
// patterns: a filter with no compiled pattern still matches nothing, so a load
// failure would look like "this output simply has no filter".
func TestRTKFilterPacksCompile(t *testing.T) {
	for _, filter := range loadRTKFilters() {
		// Some packs are reachable only through their content patterns: upstream
		// normally keys them by command name, which a chat payload does not carry.
		if len(filter.types) == 0 && len(filter.match) == 0 {
			t.Errorf("%s: neither an output type nor a match pattern", filter.id)
		}
		if len(filter.drop) == 0 && len(filter.keep) == 0 {
			t.Errorf("%s: no line patterns compiled", filter.id)
		}
	}
}

// TestRTKFilterSelection pins the cascade: the detected output type wins, then
// the pack's own content patterns, and a document read gets no filter at all.
func TestRTKFilterSelection(t *testing.T) {
	diff := strings.Join([]string{
		"diff --git a/a.go b/a.go",
		"index 111..222 100644",
		"--- a/a.go",
		"+++ b/a.go",
		"@@ -1,3 +1,3 @@",
		" package a",
		"-old",
		"+new",
	}, "\n") + "\n"
	if got := selectRTKFilter(diff); got == nil || got.id != "git-diff" {
		t.Fatalf("git diff output selected %v", got)
	}

	if got := selectRTKFilter("Error: boom\nException: fake\n"); got == nil || !got.hasType("generic-error") {
		t.Fatalf("error output selected %v", got)
	}

	// A file read: no command shape and no error markers. Filtering it would cut
	// a source file in half, so nothing may be selected.
	var read strings.Builder
	for i := 0; i < 100; i++ {
		read.WriteString("func handler() error { return nil }\n")
	}
	if got := selectRTKFilter(read.String()); got != nil {
		t.Fatalf("a plain file read must not be filtered, selected %s", got.id)
	}
}

// TestRTKEngineAppliesFilters pins the wiring: compress() runs the selected pack,
// so npm's install chatter is whitelisted away and not merely truncated.
func TestRTKEngineAppliesFilters(t *testing.T) {
	in := strings.Join([]string{
		"added 3 packages",
		"npm warn deprecated a@1.0.0: use b instead",
		"npm warn deprecated c@2.0.0: use d instead",
		"npm warn deprecated e@3.0.0: use f instead",
		"packages are looking for funding",
		"run `npm fund` for details",
		"audited 3 packages",
		"found 0 vulnerabilities",
	}, "\n") + "\n"
	out, ok := rtkEngine{}.compress(in)
	if !ok {
		t.Fatal("expected a rewrite")
	}
	for _, keep := range []string{"added 3 packages", "audited 3 packages"} {
		if !strings.Contains(out, keep) {
			t.Fatalf("lost %q:\n%s", keep, out)
		}
	}
	if strings.Contains(out, "npm warn deprecated") {
		t.Fatalf("filter did not drop install warnings:\n%s", out)
	}
}
