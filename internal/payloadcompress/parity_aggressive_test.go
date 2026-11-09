package payloadcompress

import (
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func aggCodeFile(lines int) string {
	var b strings.Builder
	for i := 0; i < lines; i++ {
		b.WriteString("const value")
		b.WriteString(strings.Repeat("x", i%3))
		b.WriteString(" = ")
		b.WriteString("1;\n")
	}
	return b.String()
}

func TestAggressiveToolResultCategories(t *testing.T) {
	file := aggCodeFile(30)
	result := compressToolResult(file)
	if result.strategy != "fileContent" || !strings.Contains(result.compressed, "lines elided") {
		t.Fatalf("file content: strategy=%q out=%q", result.strategy, result.compressed)
	}

	grep := "src/a.go:10:first\nsrc/b.go:2:second"
	result = compressToolResult(grep)
	if result.strategy != "grepSearch" || !strings.Contains(result.compressed, "Files: src/a.go, src/b.go") {
		t.Fatalf("grep: strategy=%q out=%q", result.strategy, result.compressed)
	}

	shell := "\x1b[31mred\x1b[0m\n$ ls"
	result = compressToolResult(shell)
	if result.strategy != "shellOutput" || strings.Contains(result.compressed, "\x1b[") {
		t.Fatalf("shell: strategy=%q out=%q", result.strategy, result.compressed)
	}

	jsonBody := `{"a":"` + strings.Repeat("x", 2100) + `"}`
	result = compressToolResult(jsonBody)
	if result.strategy != "json" || !strings.Contains(result.compressed, `"a": "xxx`) {
		t.Fatalf("json: strategy=%q out=%q", result.strategy, result.compressed)
	}

	var stack strings.Builder
	stack.WriteString("Error: boom\n")
	for i := 0; i < 20; i++ {
		stack.WriteString("  at frame\n")
	}
	result = compressToolResult(stack.String())
	if result.strategy != "errorMessage" || !strings.Contains(result.compressed, "frames elided") {
		t.Fatalf("error: strategy=%q out=%q", result.strategy, result.compressed)
	}

	result = compressToolResult("just some prose")
	if result.strategy != toolStrategyNone || result.saved != 0 {
		t.Fatalf("none: %+v", result)
	}
}

func TestAggressiveApplyBody(t *testing.T) {
	toolContent := aggCodeFile(30)
	body := []byte(`{"model":"m","messages":[` +
		`{"role":"system","content":"SYSTEM PROMPT"},` +
		`{"role":"user","content":"do it"},` +
		`{"role":"tool","content":` + string(appendJSONString(nil, toolContent)) + `},` +
		`{"role":"assistant","content":"noted"},` +
		`{"role":"user","content":"current question"}]}`)

	out := string(newAggressiveEngine().applyBody(body))
	if !strings.Contains(out, "lines elided") {
		t.Fatalf("tool result not compressed: %s", out)
	}
	if !strings.Contains(out, `"content":"SYSTEM PROMPT"`) {
		t.Fatalf("system prompt changed: %s", out)
	}
	if !strings.Contains(out, `"content":"current question"`) {
		t.Fatalf("current user turn changed: %s", out)
	}
}

func TestAggressiveApplyBodyFailsOpen(t *testing.T) {
	engine := newAggressiveEngine()
	for _, body := range [][]byte{
		[]byte(`not json`),
		[]byte(`{"messages":5}`),
		[]byte(`{"messages":[]}`),
		[]byte(`{}`),
		[]byte(`{"messages":[{"role":"user","content":"hi"}]}`),
	} {
		if got := engine.applyBody(body); string(got) != string(body) {
			t.Fatalf("expected fail open for %q, got %q", body, got)
		}
	}
}

func TestAggressiveAgingProtections(t *testing.T) {
	jsonMsg := `{"role":"assistant","content":"{\"a\":1}"}`
	fenced := "```\nline1\nline2\nline3\nline4\nline5\n```"
	fencedMsg := `{"role":"assistant","content":` + string(appendJSONString(nil, fenced)) + `}`
	oldProse := `{"role":"assistant","content":` + string(appendJSONString(nil, "this is a long prose answer with many words that could be compressed")) + `}`
	compressedMsg := `{"role":"assistant","content":"[COMPRESSED:aging:light] already"}`
	spared := `{"role":"assistant","content":"spare me"}`

	raws := []string{
		jsonMsg,
		fencedMsg,
		compressedMsg,
		oldProse,
		spared,
		`{"role":"user","content":"latest question"}`,
	}
	out, _ := applyAging(raws, agingDefaultThresholds, true, -1)

	if out[0] != jsonMsg {
		t.Fatalf("json message must stay verbatim: %q", out[0])
	}
	if !strings.Contains(out[1], "[COMPRESSED:aging:") || !strings.Contains(out[1], "line1") {
		t.Fatalf("fenced block must be tagged and preserved: %q", out[1])
	}
	if out[2] != compressedMsg {
		t.Fatalf("recursion guard failed: %q", out[2])
	}
	if out[5] != raws[5] {
		t.Fatalf("latest user turn must be spared: %q", out[5])
	}

	// An explicit spared index is left byte-identical even when old.
	out, _ = applyAging(raws, agingDefaultThresholds, true, 1)
	if out[1] != fencedMsg {
		t.Fatalf("explicit spare index ignored: %q", out[1])
	}
}

func TestAggressiveSummarizerRuleBased(t *testing.T) {
	messages := []ojson.Result{
		ojson.Parse(`{"role":"user","content":"Fix: broken thing\nmore detail"}`),
		ojson.Parse(`{"role":"assistant","content":"edited src/main.go and internal/api/server.go"}`),
		ojson.Parse(`{"role":"user","content":"TypeError: boom happened"}`),
		ojson.Parse(`{"role":"assistant","content":"We chose the simpler option."}`),
	}
	summary := summarizeRuleBased(messages, 2000, true)
	for _, want := range []string{
		"[COMPRESSED:summary]",
		"Intents: Fix: broken thing.",
		"Files touched: src/main.go, internal/api/server.go.",
		"Errors: TypeError: boom happened.",
		"Last decision: We chose the simpler option.",
	} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary missing %q: %s", want, summary)
		}
	}

	capped := summarizeRuleBased(messages, 40, true)
	if charLen(capped) > 40 || !strings.HasSuffix(capped, "...") {
		t.Fatalf("maxLen cap failed (%d chars): %q", charLen(capped), capped)
	}
}

func TestAggressiveTagAged(t *testing.T) {
	if got := tagAged("light", `{"a":1}`, "x"); got != `{"a":1}` {
		t.Fatalf("json must remain untagged: %q", got)
	}
	if got := tagAged("light", "```code```", "x"); got != "[COMPRESSED:aging:light]\n```code```" {
		t.Fatalf("fenced tag placement: %q", got)
	}
	if got := tagAged("moderate", "prose here", "shorter"); got != "[COMPRESSED:aging:moderate] shorter" {
		t.Fatalf("inline tag: %q", got)
	}
}
