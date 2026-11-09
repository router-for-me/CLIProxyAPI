package payloadcompress

import (
	"strings"
	"testing"
)

func TestUltraDefaultsMatchUpstream(t *testing.T) {
	e := newUltraEngine()
	if e.compressionRate != 0.5 {
		t.Fatalf("compressionRate = %v, want 0.5", e.compressionRate)
	}
	if e.minScoreThreshold != 0.3 {
		t.Fatalf("minScoreThreshold = %v, want 0.3", e.minScoreThreshold)
	}
	if e.maxTokensPerMessage != 0 {
		t.Fatalf("maxTokensPerMessage = %d, want 0", e.maxTokensPerMessage)
	}
	if e.name() != "ultra" {
		t.Fatalf("name = %q", e.name())
	}
}

func TestUltraApplyBodyPreservesSystemPrompt(t *testing.T) {
	body := []byte(`{"model":"x","messages":[` +
		`{"role":"system","content":"The quick brown fox is very really quite good"},` +
		`{"role":"user","content":"The quick brown fox is very really quite good"}]}`)

	out := string(newUltraEngine().applyBody(body))
	if !strings.Contains(out, `"content":"The quick brown fox is very really quite good"`) {
		t.Fatalf("system prompt was modified: %s", out)
	}
}

func TestUltraApplyBodyPreservesStructuredContent(t *testing.T) {
	structured := []string{
		"```go\nfunc main() { println(\"hi\") }\n```",
		"```diff\n-func old() {}\n+func new() {}\n```",
		"```json\n{\"key\":\"value\"}\n```",
		"https://example.com/a/b?c=d",
		"/tmp/some/file.go",
		"process.env.HOME",
		"API_TOKEN",
	}
	content := "Please refactor this file /tmp/some/file.go quickly because it is very really quite slow.\n" +
		"See https://example.com/a/b?c=d and process.env.HOME and API_TOKEN.\n" +
		"```go\nfunc main() { println(\"hi\") }\n```\n" +
		"```diff\n-func old() {}\n+func new() {}\n```\n" +
		"```json\n{\"key\":\"value\"}\n```\n" +
		"Thanks so much for doing this work."
	body := append([]byte(`{"messages":[{"role":"user","content":`), appendJSONString(nil, content)...)
	body = append(body, []byte(`}]}`)...)

	out := string(newUltraEngine().applyBody(body))
	for _, want := range structured {
		quoted := string(appendJSONString(nil, want))
		if !strings.Contains(out, quoted[1:len(quoted)-1]) {
			t.Fatalf("structured content %q was modified: %s", want, out)
		}
	}
}

func TestUltraApplyBodyFailsOpenOnUnexpectedShape(t *testing.T) {
	engine := newUltraEngine()
	for _, body := range [][]byte{
		[]byte(`{"foo":"bar"}`),
		[]byte(`not json`),
		[]byte(``),
		[]byte(`{"messages":"not-an-array"}`),
	} {
		if got := string(engine.applyBody(body)); got != string(body) {
			t.Fatalf("applyBody(%q) = %q, want unchanged", body, got)
		}
	}
}

func TestPruneByScoreKeepsPolarityAndStructuredTokens(t *testing.T) {
	text := "You must never remove https://example.com/a or /tmp/file.go or Error: boom"
	got := pruneByScore(text, 0.5, 0.3)
	for _, want := range []string{"must", "never", "https://example.com/a", "/tmp/file.go", "Error:"} {
		if !strings.Contains(got, want) {
			t.Fatalf("pruneByScore dropped %q: %s", want, got)
		}
	}
}

func TestPruneByScoreDropsStopwords(t *testing.T) {
	got := pruneByScore("The quick brown fox is very really quite good", 0.5, 0.3)
	if got == "The quick brown fox is very really quite good" {
		t.Fatalf("nothing pruned: %s", got)
	}
	if strings.Contains(got, "really") && strings.Contains(got, "quite") && strings.Contains(got, "very") {
		t.Fatalf("expected filler words pruned: %s", got)
	}
}

func TestPreservationRoundTrip(t *testing.T) {
	text := "Intro https://example.com/a and /tmp/file.go plus\n```go\nx := 1\n```\nThe end"
	withPlaceholders, blocks := extractPreservedBlocks(text, nil)
	if len(blocks) == 0 {
		t.Fatal("expected preserved blocks")
	}
	if got := restorePreservedBlocks(withPlaceholders, blocks); got != text {
		t.Fatalf("round trip = %q, want %q", got, text)
	}
	if fences := findFencedCodeBlocks(text); len(fences) != 1 || !strings.Contains(fences[0], "x := 1") {
		t.Fatalf("findFencedCodeBlocks = %#v", fences)
	}
}

func TestPreserveSpansRoundTrip(t *testing.T) {
	text := "abcdef"
	withPlaceholders, blocks := preserveSpans(text, []span{{start: 1, end: 3, kind: "custom"}, {start: 4, end: 6, kind: "custom"}})
	if got := restorePreservedBlocks(withPlaceholders, blocks); got != text {
		t.Fatalf("round trip = %q, want %q", got, text)
	}
}

func TestUltraCompressKindSkipsToolOutput(t *testing.T) {
	e := newUltraEngine()
	text := "The quick brown fox is very really quite good"
	if got, changed := e.compressKind(kindToolOutput, text); changed || got != text {
		t.Fatalf("tool output was compressed: %q", got)
	}
}
