package payloadcompress

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

const prose = "Hi there! I was wondering if you could please basically fix the bug in the parser. " +
	"It seems like the tokenizer is probably broken when it receives an empty input buffer."

func TestCavemanLiteDropsFiller(t *testing.T) {
	out, ok := (cavemanEngine{level: "lite"}).compressKind(kindUser, prose)
	if !ok {
		t.Fatal("expected compression")
	}
	for _, gone := range []string{"Hi there", "I was wondering if you could", "please", "basically", "probably", "It seems like"} {
		if strings.Contains(strings.ToLower(out), strings.ToLower(gone)) {
			t.Fatalf("%q survived:\n%s", gone, out)
		}
	}
	for _, kept := range []string{"fix", "bug", "parser", "tokenizer", "empty", "input buffer"} {
		if !strings.Contains(strings.ToLower(out), kept) {
			t.Fatalf("%q was lost:\n%s", kept, out)
		}
	}
}

func TestCavemanIntensityLadder(t *testing.T) {
	text := "I will make sure to query the database for the configuration of the function."
	lite, ok := (cavemanEngine{level: "lite"}).compressKind(kindUser, text)
	if !ok {
		t.Fatal("lite: expected compression")
	}
	if strings.Contains(lite, "database") && strings.Contains(lite, "make sure to") {
		t.Fatalf("lite did not rewrite:\n%s", lite)
	}
	full, ok := (cavemanEngine{level: "full"}).compressKind(kindUser, text)
	if !ok {
		t.Fatal("full: expected compression")
	}
	if len(full) >= len(lite) {
		t.Fatalf("full must compress further than lite: %d >= %d\n%q\n%q", len(full), len(lite), full, lite)
	}
	ultra, ok := (cavemanEngine{level: "ultra"}).compressKind(kindUser, text)
	if !ok {
		t.Fatal("ultra: expected compression")
	}
	if !strings.Contains(ultra, "DB") || strings.Contains(strings.ToLower(ultra), "database") {
		t.Fatalf("ultra abbreviations missing:\n%s", ultra)
	}
}

func TestCavemanPreservesCodeAndURLs(t *testing.T) {
	text := "Please take a look at https://example.com/a/b?c=d and the snippet " +
		"`db.Query(ctx, \"SELECT 1\")` because it basically fails.\n" +
		"```go\nfunc very() { return database }\n```\nThanks in advance."
	out, ok := (cavemanEngine{level: "ultra"}).compressKind(kindUser, text)
	if !ok {
		t.Fatal("expected compression")
	}
	if !strings.Contains(out, "https://example.com/a/b?c=d") {
		t.Fatalf("URL was rewritten:\n%s", out)
	}
	if !strings.Contains(out, "`db.Query(ctx, \"SELECT 1\")`") {
		t.Fatalf("inline code was rewritten:\n%s", out)
	}
	if !strings.Contains(out, "```go\nfunc very() { return database }\n```") {
		t.Fatalf("fenced block was rewritten:\n%s", out)
	}
	if strings.Contains(out, "basically") || strings.Contains(strings.ToLower(out), "thanks in advance") {
		t.Fatalf("filler survived:\n%s", out)
	}
}

func TestCavemanSkipsToolOutputAndCode(t *testing.T) {
	if out, ok := (cavemanEngine{level: "ultra"}).compressKind(kindToolOutput, prose); ok || out != prose {
		t.Fatal("tool output must never be rewritten by caveman")
	}
	code := "const a = 1;\nconst b = 2;\nfunction run() {\n  return a + b;\n}"
	if out, ok := (cavemanEngine{level: "lite"}).compressKind(kindUser, code); ok || out != code {
		t.Fatalf("code-dominant text must be left alone:\n%s", out)
	}
}

func TestCavemanIdempotent(t *testing.T) {
	engine := cavemanEngine{level: "full"}
	once, ok := engine.compressKind(kindUser, prose)
	if !ok {
		t.Fatal("expected compression")
	}
	twice, _ := engine.compressKind(kindUser, once)
	if twice != once {
		t.Fatalf("not idempotent:\n1: %q\n2: %q", once, twice)
	}
}

func TestCavemanThroughApply(t *testing.T) {
	body := []byte(`{"model":"m","messages":[` +
		`{"role":"user","content":` + quote(prose) + `},` +
		`{"role":"tool","content":` + quote(prose) + `}]}`)

	out, stats := Apply(testConfig(EngineCaveman), "m", body)
	if !stats.Applied {
		t.Fatal("expected compression")
	}
	user := gjson.GetBytes(out, "messages.0.content").String()
	if strings.Contains(strings.ToLower(user), "please") || strings.Contains(strings.ToLower(user), "basically") {
		t.Fatalf("user prose not compressed:\n%s", user)
	}
	if got := gjson.GetBytes(out, "messages.1.content").String(); got != prose {
		t.Fatalf("tool output was rewritten:\n%s", got)
	}
	if !json.Valid(out) {
		t.Fatalf("invalid JSON: %s", out)
	}
}

func TestAntislopStylesInCatalog(t *testing.T) {
	for _, id := range []string{"antislop-ui", "antislop-copy"} {
		for _, level := range []string{"lite", "full", "ultra"} {
			text := styleInstruction([]OutputStyle{{ID: id, Level: level}})
			if !strings.Contains(text, "["+id+"]") {
				t.Fatalf("%s/%s missing from instruction", id, level)
			}
			if !strings.Contains(text, antislopBoundaries) {
				t.Fatalf("%s/%s missing antislop boundaries", id, level)
			}
		}
	}
	ui := styleInstruction([]OutputStyle{{ID: "antislop-ui", Level: "full"}})
	if !strings.Contains(ui, "purpose test") || !strings.Contains(ui, "blue-purple gradient wash") {
		t.Fatalf("antislop-ui full text wrong:\n%s", ui)
	}
	copyStyle := styleInstruction([]OutputStyle{{ID: "antislop-copy", Level: "full"}})
	if !strings.Contains(copyStyle, "WCAG AA") || !strings.Contains(copyStyle, "em dash") {
		t.Fatalf("antislop-copy full text wrong:\n%s", copyStyle)
	}
}

func TestSharedBoundariesEmittedOnce(t *testing.T) {
	text := styleInstruction([]OutputStyle{
		{ID: "antislop-ui", Level: "full"},
		{ID: "antislop-copy", Level: "full"},
		{ID: "terse-prose", Level: "lite"},
	})
	if got := strings.Count(text, antislopBoundaries); got != 1 {
		t.Fatalf("antislop boundaries emitted %d times", got)
	}
	if got := strings.Count(text, sharedBoundaries); got != 1 {
		t.Fatalf("shared boundaries emitted %d times", got)
	}
}
