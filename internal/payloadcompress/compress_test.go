package payloadcompress

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func testConfig(pipeline ...string) Config {
	steps := make([]Step, 0, len(pipeline))
	for _, name := range pipeline {
		steps = append(steps, Step{Engine: name})
	}
	return Config{
		Enabled:      true,
		MinBytes:     1,
		DefaultCombo: "t",
		Combos:       []Combo{{Name: "t", Models: []string{"*"}, Pipeline: steps}},
	}
}

func TestStepOptionsParseNestedAndInline(t *testing.T) {
	const raw = `
combos:
  - name: c
    pipeline:
      - engine: lite
        max-tool-length: 10
      - engine: caveman
        language: ja
        options:
          min-message-length: 20
        language-packs: [ja]
`
	decoder := yaml.NewDecoder(bytes.NewReader([]byte(raw)))
	decoder.KnownFields(true)
	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	steps := cfg.Combos[0].Pipeline
	if steps[0].Options.MaxToolLength != 10 {
		t.Fatalf("inline option lost: %+v", steps[0].Options)
	}
	if steps[1].Options.Language != "ja" || steps[1].Options.LanguagePacks[0] != "ja" || steps[1].Options.MinMessageLength != 20 {
		t.Fatalf("mixed options lost: %+v", steps[1].Options)
	}
}

func TestAppendJSONStringMatchesStdlib(t *testing.T) {
	cases := []string{
		"",
		"plain",
		`quote " and \ backslash`,
		"tab\tnl\ncr\r",
		"ctrl \x00 \x01 \x1f",
		"unicode héllo 日本語 😀",
		"</script>",
	}
	for _, in := range cases {
		encoded := string(appendJSONString(nil, in))
		var decoded string
		if err := json.Unmarshal([]byte(encoded), &decoded); err != nil {
			t.Fatalf("appendJSONString(%q) = %s: %v", in, encoded, err)
		}
		if decoded != in {
			t.Fatalf("round trip mismatch: %q -> %s -> %q", in, encoded, decoded)
		}
		if !json.Valid([]byte(encoded)) {
			t.Fatalf("invalid JSON for %q: %s", in, encoded)
		}
	}
}

func TestApplyIdempotent(t *testing.T) {
	block := strings.Repeat("line of tool output  \n\n\n", 40)
	body := []byte(`{"model":"m","messages":[` +
		`{"role":"user","content":"hello"},` +
		`{"role":"tool","tool_call_id":"c1","content":` + quote(block) + `},` +
		`{"role":"assistant","content":"ok"},` +
		`{"role":"tool","tool_call_id":"c2","content":` + quote(block) + `}]}`)
	cfg := testConfig(EngineSessionDedup, EngineLite, EngineRTK)

	once, stats := Apply(cfg, "m", body)
	if !stats.Applied {
		t.Fatal("expected compression to apply")
	}
	if len(once) >= len(body) {
		t.Fatalf("expected shrink, got %d >= %d", len(once), len(body))
	}
	twice, stats2 := Apply(cfg, "m", once)
	if string(twice) != string(once) {
		t.Fatalf("not idempotent:\n1: %s\n2: %s", once, twice)
	}
	if stats2.Applied {
		t.Fatal("second pass reported changes")
	}
	if !json.Valid(once) {
		t.Fatalf("output is not valid JSON: %s", once)
	}
}

func TestApplyProtectsCachedPrefix(t *testing.T) {
	cached := strings.Repeat("cached   prefix   with   noise   \n\n\n\n", 8)
	fresh := strings.Repeat("fresh   block   with   noise   \n\n\n\n", 8)
	body := []byte(`{"model":"m","messages":[` +
		`{"role":"user","content":[{"type":"text","text":` + quote(cached) +
		`},{"type":"text","text":"x","cache_control":{"type":"ephemeral"}}]},` +
		`{"role":"user","content":` + quote(fresh) + `}]}`)

	cfg := testConfig(EngineLite)
	cfg.CachePrefix = CachePrefixPreserve
	out, stats := Apply(cfg, "m", body)
	if !stats.Applied {
		t.Fatal("expected compression")
	}
	if !strings.Contains(string(out), quote(cached)) {
		t.Fatalf("cached prefix was rewritten:\n%s", out)
	}
	if strings.Contains(string(out), quote(fresh)) {
		t.Fatalf("unprotected segment was not compressed:\n%s", out)
	}
}

func TestApplySkipsSystemAssistantAndToolArgs(t *testing.T) {
	noisy := "system   prompt   with   noise   \n\n\n\n"
	args := `{"path":"a/b","body":"x   y\n\n\n\nz"}`
	body := []byte(`{"model":"m","messages":[` +
		`{"role":"system","content":` + quote(noisy) + `},` +
		`{"role":"user","content":"hi"},` +
		`{"role":"assistant","content":` + quote(noisy) + `,` +
		`"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":` + quote(args) + `}}]}]}`)

	out, _ := Apply(testConfig(EngineLite), "m", body)
	if string(out) != string(body) {
		t.Fatalf("untouchable fields changed:\n%s", out)
	}
}

func TestApplyNoConversationShape(t *testing.T) {
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"a   b   \n\n\n\nc"}]}]}`)
	out, stats := Apply(testConfig(EngineLite), "m", body)
	if stats.Applied || string(out) != string(body) {
		t.Fatal("gemini-shaped payload must not be touched")
	}
}

func TestApplyBelowMinBytes(t *testing.T) {
	cfg := testConfig(EngineLite)
	cfg.MinBytes = 1 << 20
	body := []byte(`{"messages":[{"role":"user","content":"a   b   \n\n\n\nc"}]}`)
	out, stats := Apply(cfg, "m", body)
	if stats.Applied || string(out) != string(body) {
		t.Fatal("small payload must be left alone")
	}
}

func TestApplyShadowMode(t *testing.T) {
	cfg := testConfig(EngineRTK)
	cfg.Shadow = true
	body := []byte(`{"messages":[{"role":"tool","content":` + quote(shellOutput()) + `}]}`)
	out, stats := Apply(cfg, "m", body)
	if stats.Applied {
		t.Fatal("shadow mode must not report Applied")
	}
	if string(out) != string(body) {
		t.Fatal("shadow mode must return the original body")
	}
	if stats.BytesOut >= stats.BytesIn {
		t.Fatalf("shadow mode must still report the projected size: %+v", stats)
	}
}

func TestModelScopedCombo(t *testing.T) {
	cfg := Config{
		Enabled:      true,
		MinBytes:     1,
		DefaultCombo: "lite",
		Combos: []Combo{
			{Name: "lite", Models: []string{"*"}, Pipeline: []Step{{Engine: EngineLite}}},
			{Name: "dup", Models: []string{"claude-*"}, Pipeline: []Step{{Engine: EngineSessionDedup}}},
		},
	}
	if got := cfg.resolve("claude-sonnet-4"); got == nil || got.Name != "dup" {
		t.Fatalf("claude model resolved to %v", got)
	}
	if got := cfg.resolve("gpt-4o"); got == nil || got.Name != "lite" {
		t.Fatalf("fallback model resolved to %v", got)
	}
}

func TestSanitizeDropsUnknownEngines(t *testing.T) {
	cfg := Config{Combos: []Combo{{
		Name:     "Mixed",
		Pipeline: []Step{{Engine: "LITE"}, {Engine: "does-not-exist"}},
	}}}
	cfg.Sanitize()
	if len(cfg.Combos) != 1 || len(cfg.Combos[0].Pipeline) != 1 {
		t.Fatalf("unexpected pipeline: %+v", cfg.Combos)
	}
	if cfg.Combos[0].Pipeline[0].Engine != EngineLite {
		t.Fatalf("engine not normalized: %+v", cfg.Combos[0].Pipeline)
	}
	if cfg.DefaultCombo != "mixed" || cfg.MinBytes != defaultMinBytes {
		t.Fatalf("defaults not repaired: %+v", cfg)
	}
}

func TestDedupElidesByteIdenticalSegments(t *testing.T) {
	block := strings.Repeat("the same content ", 60)
	body := []byte(`{"messages":[` +
		`{"role":"tool","content":` + quote(block) + `},` +
		`{"role":"tool","content":` + quote(block) + `}]}`)
	out, stats := Apply(testConfig(EngineSessionDedup), "m", body)
	if !stats.Applied {
		t.Fatal("expected dedup")
	}
	if !strings.Contains(string(out), dedupMarker) {
		t.Fatalf("missing marker:\n%s", out)
	}
	if strings.Count(string(out), "the same content") != 60 {
		t.Fatalf("first copy must be preserved:\n%s", out)
	}
}

func TestHeadroomRendersColumnar(t *testing.T) {
	rows := make([]string, 0, 20)
	for i := 0; i < 20; i++ {
		rows = append(rows, `{"id":`+itoa(i)+`,"name":"name with some length ,`+itoa(i)+`","ok":true,"detail":"row detail text"}`)
	}
	list := "[" + strings.Join(rows, ",") + "]"
	out, ok := headroomEngine{}.compress(list)
	if !ok {
		t.Fatal("expected columnar rewrite")
	}
	if !strings.HasPrefix(out, headroomMarker+"id,name,ok") {
		t.Fatalf("bad header:\n%s", out)
	}
	if !strings.Contains(out, "\"name with some length ,3\"") {
		t.Fatalf("comma value must be quoted:\n%s", out)
	}
	if again, hit := (headroomEngine{}).compress(out); hit || again != out {
		t.Fatal("headroom is not idempotent")
	}
}

func TestHeadroomLeavesHeterogeneousAlone(t *testing.T) {
	list := `[{"a":1},{"b":2},{"a":3},{"b":4},{"a":5},{"b":6},{"a":7},{"b":8}]`
	if out, ok := (headroomEngine{}).compress(list); ok {
		t.Fatalf("heterogeneous rows must be left alone:\n%s", out)
	}
}

func TestLitePreservesFencedCode(t *testing.T) {
	text := "intro line with trailing   \n" + strings.Repeat("filler line\n", 8) +
		"```\ncode   with   spaces   \n\n\n\nand blanks\n```\noutro   \n\n\n\ntail\n"
	out, ok := liteEngine{}.compress(text)
	if !ok {
		t.Fatal("expected compression")
	}
	if !strings.Contains(out, "code   with   spaces   \n\n\n\nand blanks") {
		t.Fatalf("fenced code was modified:\n%q", out)
	}
	if strings.Contains(out, "intro line with trailing   \n") {
		t.Fatalf("prose was not cleaned:\n%q", out)
	}
}

func TestLiteStripsANSI(t *testing.T) {
	text := "\x1b[31mred\x1b[0m line\n" + strings.Repeat("\x1b[1mbold\x1b[0m text\n", 12)
	out, ok := liteEngine{}.compress(text)
	if !ok || strings.Contains(out, "\x1b") {
		t.Fatalf("ANSI not stripped: %q", out)
	}
}

// A tool result must be cleaned before it is capped. Upstream orders
// collapseWhitespace ahead of compressToolResults, so blank runs never consume
// the character budget; capping first strands the cleaner's savings.
func TestLiteCollapsesBlankRunsBeforeCappingToolOutput(t *testing.T) {
	kept := "payload line"
	text := strings.Repeat(kept+"   \n\n\n\n", 400)
	e := liteEngine{opts: StepOptions{MaxToolLength: 500}}
	out, ok := e.compressKind(kindToolOutput, text)
	if !ok {
		t.Fatal("expected compression")
	}
	if strings.Contains(out, "\n\n\n") {
		t.Fatalf("blank runs survived the cap:\n%q", out[:120])
	}
	if !strings.Contains(out, kept) {
		t.Fatalf("content was lost: %q", out)
	}
	if got := charLen(out); got > 500 {
		t.Fatalf("capped result is %d chars, want <= 500", got)
	}
}

func TestRTKCollapsesRuns(t *testing.T) {
	out, ok := rtkEngine{}.compress(shellOutput())
	if !ok {
		t.Fatal("expected compaction")
	}
	if !strings.Contains(out, "retrying"+rtkRepeatMark+"5)") {
		t.Fatalf("repeated lines not collapsed:\n%s", out)
	}
	if again, hit := (rtkEngine{}).compress(out); hit || again != out {
		t.Fatal("rtk is not idempotent")
	}
}

func TestRTKTruncatesLongLines(t *testing.T) {
	body := strings.Repeat("x", 4000) + "\n" + strings.Repeat("keep\n", 10)
	out, ok := rtkEngine{}.compress(body)
	if !ok {
		t.Fatal("expected truncation")
	}
	if len(out) >= len(body) {
		t.Fatalf("expected shrink: %d >= %d", len(out), len(body))
	}
	if !strings.Contains(out, "chars omitted") {
		t.Fatalf("missing truncation marker:\n%s", out)
	}
}

func TestApplyNoAllocsWhenTooSmall(t *testing.T) {
	cfg := testConfig(EngineLite, EngineSessionDedup, EngineRTK)
	cfg.MinBytes = 1 << 20
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hello"}]}`)
	allocs := testing.AllocsPerRun(100, func() {
		Apply(cfg, "m", body)
	})
	if allocs != 0 {
		t.Fatalf("expected 0 allocs, got %v", allocs)
	}
}

func TestApplyNoAllocsWithoutSegments(t *testing.T) {
	cfg := testConfig(EngineLite)
	body := []byte(`{"model":"m","stream":true,"tools":[{"type":"function"}]}`)
	allocs := testing.AllocsPerRun(100, func() {
		Apply(cfg, "m", body)
	})
	if allocs != 0 {
		t.Fatalf("expected 0 allocs, got %v", allocs)
	}
}

func BenchmarkApplyNothingToDo(b *testing.B) {
	cfg := testConfig(EngineLite, EngineSessionDedup, EngineRTK)
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"short prompt"}]}`)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		Apply(cfg, "m", body)
	}
}

func BenchmarkApplyLargeChat(b *testing.B) {
	cfg := testConfig(EngineSessionDedup, EngineLite)
	body := []byte(largeBody())
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		Apply(cfg, "m", body)
	}
}

func largeBody() string {
	var b strings.Builder
	b.WriteString(`{"model":"m","messages":[`)
	for i := 0; i < 40; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		text := strings.Repeat("tool output line "+itoa(i)+"   \n\n\n", 40)
		b.WriteString(`{"role":"tool","content":` + quote(text) + `}`)
	}
	b.WriteString(`]}`)
	return b.String()
}

func shellOutput() string {
	return "npm run build\n" +
		strings.Repeat("retrying\n", 5) +
		"added 1 package\n" +
		"done\n"
}

func quote(s string) string { return string(appendJSONString(nil, s)) }

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [8]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}

func TestApplyCoversAllThreeProtocols(t *testing.T) {
	noise := strings.Repeat("output   \n\n\n\n", 40)
	cfg := testConfig(EngineLite)

	cases := map[string]string{
		"chat-completions": `{"model":"m","messages":[{"role":"user","content":` + quote("hi") + `},` +
			`{"role":"tool","tool_call_id":"c1","content":` + quote(noise) + `}]}`,
		"anthropic-messages": `{"model":"m","messages":[{"role":"user","content":[` +
			`{"type":"text","text":` + quote("hi") + `},` +
			`{"type":"tool_result","tool_use_id":"t1","content":` + quote(noise) + `}]}]}`,
		"responses": `{"model":"m","input":[` +
			`{"type":"message","role":"user","content":[{"type":"input_text","text":` + quote(noise) + `}]},` +
			`{"type":"function_call_output","call_id":"c1","output":` + quote(noise) + `}]}`,
	}

	for name, body := range cases {
		out, stats := Apply(cfg, "m", []byte(body))
		if !stats.Applied {
			t.Fatalf("%s: expected compression", name)
		}
		if !json.Valid([]byte(out)) {
			t.Fatalf("%s: output is not valid JSON:\n%s", name, out)
		}
		if len(out) >= len(body) {
			t.Fatalf("%s: expected shrink, got %d >= %d", name, len(out), len(body))
		}
		if strings.Contains(string(out), `\n\n\n\n`) {
			t.Fatalf("%s: blank runs not collapsed:\n%s", name, out)
		}
	}
}

func TestStatsReport(t *testing.T) {
	stats := Stats{
		BytesIn:  1000,
		BytesOut: 320,
		Saved:    map[string]int{"rtk": 300, "lite": 380},
	}
	if got := stats.Engines(); got != "lite=380,rtk=300" {
		t.Fatalf("engines = %q", got)
	}
	if got := stats.SavedPct(); got != 68 {
		t.Fatalf("saved pct = %d", got)
	}
	if got := (Stats{}).Engines(); got != "" {
		t.Fatalf("empty stats reported engines %q", got)
	}
	if got := (Stats{}).SavedPct(); got != 0 {
		t.Fatalf("empty stats reported %d%%", got)
	}
}
