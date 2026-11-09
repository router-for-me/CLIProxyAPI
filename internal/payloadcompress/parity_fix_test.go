package payloadcompress

import (
	"regexp"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func toolResultArrayJSON() string {
	var b strings.Builder
	b.WriteString("[\n")
	for i := 0; i < 100; i++ {
		b.WriteString("  {\"id\": 1, \"name\": \"fixture\"},\n")
	}
	b.WriteString("  {\"id\": 1, \"name\": \"fixture\"}\n]")
	return b.String()
}

// A marker must be resolvable through the exported retrieve API.
func TestCCRMarkerRetrievable(t *testing.T) {
	ccrResetStore()
	defer ccrResetStore()
	text := strings.Repeat("long user text ", 60)
	body := ccrTestBody(text, true)
	out := string(newCCREngine().applyBody(body))
	m := regexp.MustCompile(`hash=([0-9a-f]{24})`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no marker emitted: %q", out)
	}
	got, ok := RetrieveCCRBlock(m[1])
	if !ok || got != text {
		t.Fatalf("retrieve(%s) = %q, %v; want original", m[1], got, ok)
	}
	if _, ok := RetrieveCCRBlock("000000000000000000000000"); ok {
		t.Fatal("unknown hash resolved")
	}
}

// Ultra must not prune a pipe-less markdown table separator row.
func TestPreservationPipeslessTableSeparator(t *testing.T) {
	text := "name | count\n--- | ---\nfoo | 1"
	rest, blocks := extractPreservedBlocks(text, nil)
	contents := make([]string, len(blocks))
	for i, b := range blocks {
		contents[i] = b.content
	}
	if len(blocks) == 0 || !strings.Contains(strings.Join(contents, "\n"), "--- | ---") {
		t.Fatalf("separator not preserved: %#v", blocks)
	}
	if got := restorePreservedBlocks(rest, blocks); got != text {
		t.Fatalf("round trip = %q, want %q", got, text)
	}
}

// The codex engine must read and write the same output field.
func TestCodexOutputFieldReadWriteMatch(t *testing.T) {
	output := toolResultArrayJSON()
	content := strings.Replace(output, `"fixture"`, `"other"`, -1)
	call := `{"type":"function_call","call_id":"c1","name":"run"}`
	item := `{"type":"function_call_output","call_id":"c1","output":` +
		string(appendJSONString(nil, output)) + `,"content":` +
		string(appendJSONString(nil, content)) + `}`

	got := string(newCodexResponsesEngine().applyBody(codexTestBody(call, item)))
	if gotContent := ojson.Get(got, "input.1.content").String(); gotContent != content {
		t.Fatalf("content field rewritten: %q", gotContent)
	}

	ref := string(newCodexResponsesEngine().applyBody(codexTestBody(call,
		`{"type":"function_call_output","call_id":"c1","output":`+
			string(appendJSONString(nil, output))+`}`)))
	want := ojson.Get(ref, "input.1.output").String()
	if want == output {
		t.Fatal("test fixture not compressible")
	}
	if gotOutput := ojson.Get(got, "input.1.output").String(); gotOutput != want {
		t.Fatalf("output = %q, want compression of output (%q)", gotOutput, want)
	}
}

// Aggressive must compress Anthropic tool_result content blocks.
func TestAggressiveCompressesAnthropicToolResult(t *testing.T) {
	inner := toolResultArrayJSON()
	body := []byte(`{"model":"m","messages":[{"role":"user","content":[` +
		`{"type":"tool_result","tool_use_id":"t1","content":` +
		string(appendJSONString(nil, inner)) + `}]}]}`)
	out := string(newAggressiveEngine().applyBody(body))
	part := ojson.Parse(out).Get("messages").Array()[0].Get("content").Array()[0]
	if part.Get("type").String() != "tool_result" || part.Get("tool_use_id").String() != "t1" {
		t.Fatalf("block shape changed: %s", part.Raw)
	}
	got := part.Get("content").String()
	if got == inner || charLen(got) >= charLen(inner) {
		t.Fatalf("tool_result not compressed: %q", got)
	}
}
