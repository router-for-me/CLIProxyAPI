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
	if got := selectRTKFilter(diff, ""); got == nil || got.id != "git-diff" {
		t.Fatalf("git diff output selected %v", got)
	}

	if got := selectRTKFilter("Error: boom\nException: fake\n", ""); got == nil || !got.hasType("generic-error") {
		t.Fatalf("error output selected %v", got)
	}

	// A file read: no command shape and no error markers. Filtering it would cut
	// a source file in half, so nothing may be selected.
	var read strings.Builder
	for i := 0; i < 100; i++ {
		read.WriteString("func handler() error { return nil }\n")
	}
	if got := selectRTKFilter(read.String(), ""); got != nil {
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

// Upstream picks an rtk filter by the command that produced the output. Without
// that, a build log with no distinctive marker lines reaches no filter at all
// and the payload keeps its ANSI escapes and blank runs.
func TestRTKFilterUsesTheOriginatingCommand(t *testing.T) {
	output := strings.Repeat("\x1b[31mFAIL\x1b[0m TestFoo\n\n\n\n", 30)
	command := "go test ./internal/..."

	byCommand := selectRTKFilter(output, command)
	if byCommand == nil || byCommand.id != "test-go" {
		t.Fatalf("command %q selected %v, want test-go", command, byCommand)
	}
	if byContent := selectRTKFilter(output, ""); byContent != nil && byContent.id == byCommand.id {
		t.Fatal("this output is meant to be unrecognizable without its command")
	}

	stripped, ok := applyRTKFilters(output, command)
	if !ok || strings.Contains(stripped, "\x1b") {
		t.Fatalf("command filter did not strip ANSI:\n%q", stripped)
	}
}

// The command lives in a different place in each protocol, so the pairing is
// pinned for all three: a segment carrying the wrong command runs a filter that
// was meant for a different tool.
func TestRTKCommandPickedFromEveryProtocol(t *testing.T) {
	cases := map[string]string{
		"chat": `{"messages":[{"role":"assistant","tool_calls":[{"id":"c1","function":{"arguments":"{\"command\":\"go test ./...\"}"}}]},` +
			`{"role":"tool","tool_call_id":"c1","content":"out"}]}`,
		"anthropic": `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"bash","input":{"command":"git status"}}]},` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"On branch main"}]}]}`,
		"responses": `{"input":[{"type":"function_call","call_id":"r1","name":"shell","arguments":"{\"command\":\"npm test\"}"},` +
			`{"type":"function_call_output","call_id":"r1","output":"PASS"}]}`,
	}
	want := map[string]string{
		"chat":      "go test ./...",
		"anthropic": "git status",
		"responses": "npm test",
	}
	for name, body := range cases {
		s := &scanner{body: []byte(body)}
		if !s.scan() {
			t.Fatalf("%s: no segments", name)
		}
		found := ""
		for _, seg := range s.segs {
			if seg.kind == kindToolOutput && seg.command != "" {
				found = seg.command
			}
		}
		if found != want[name] {
			t.Fatalf("%s: tool command = %q, want %q", name, found, want[name])
		}
	}
}
