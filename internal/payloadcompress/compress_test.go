package payloadcompress

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"gopkg.in/yaml.v3"
)

func testConfig(pipeline ...string) Config {
	on, off := true, false
	opts := StepOptions{
		CompressToolResults:    &on,
		PreserveToolWhitespace: &off,
		// Tests exercise the lossy lite body passes, now opt-in.
		LiteRemoveRedundant: &on,
		LiteReplaceImages:   &on,
	}
	steps := make([]Step, 0, len(pipeline))
	for _, name := range pipeline {
		steps = append(steps, Step{Engine: name, Options: opts})
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

func TestApplySkipsSystemAndToolArgs(t *testing.T) {
	noisy := "system   prompt   with   noise   \n\n\n\n"
	args := `{"path":"a/b","body":"x   y\n\n\n\nz"}`
	body := []byte(`{"model":"m","messages":[` +
		`{"role":"system","content":` + quote(noisy) + `},` +
		`{"role":"user","content":"hi"},` +
		`{"role":"assistant","content":` + quote(noisy) + `,` +
		`"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":` + quote(args) + `}}]}]}`)

	out, stats := Apply(testConfig(EngineLite), "m", body)
	if !stats.Applied {
		t.Fatal("expected assistant prose to be compressed")
	}
	// System text stays intact; only the assistant's copy is collapsed.
	if strings.Count(string(out), quote(noisy)) != 1 {
		t.Fatalf("system prompt changed or assistant not compressed:\n%s", out)
	}
	if !strings.Contains(string(out), quote(args)) {
		t.Fatalf("tool-call arguments changed:\n%s", out)
	}
}

func TestApplyNoConversationShape(t *testing.T) {
	body := []byte(`{"prompt":"a   b   \n\n\n\nc"}`)
	out, stats := Apply(testConfig(EngineLite), "m", body)
	if stats.Applied || string(out) != string(body) {
		t.Fatal("payload without a conversation shape must not be touched")
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
	// A long enough run keeps the dedup markers smaller than the lines they
	// replace, so the projected size is genuinely lower.
	toolOutput := "npm run build\n" + strings.Repeat("retrying\n", 12) + "added 1 package\ndone\n"
	body := []byte(`{"messages":[{"role":"tool","content":` + quote(toolOutput) + `}]}`)
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
	block := strings.Repeat("the same content line\n", 20)
	body := []byte(`{"messages":[` +
		`{"role":"tool","content":` + quote(block) + `},` +
		`{"role":"tool","content":` + quote(block) + `},` +
		`{"role":"assistant","content":"ok"}]}`)
	out, stats := Apply(testConfig(EngineSessionDedup), "m", body)
	if !stats.Applied {
		t.Fatal("expected dedup")
	}
	if !strings.Contains(string(out), dedupRefPrefix) {
		t.Fatalf("missing marker:\n%s", out)
	}
	if strings.Count(string(out), "the same content line") != 20 {
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
	e := lossyLite(500)
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
	if !strings.Contains(out, "[line repeated 4x]") {
		t.Fatalf("repeated lines not collapsed:\n%s", out)
	}
	if again, hit := (rtkEngine{}).compress(out); hit || again != out {
		t.Fatal("rtk is not idempotent")
	}
}

// Upstream has no per-line shortening: a long line is kept whole or dropped by
// the total cap, so minified JSON stays parseable.
func TestRTKKeepsLongLinesWhole(t *testing.T) {
	long := strings.Repeat("x", 4000)
	body := "Error: boom\n" + long + "\n" + strings.Repeat("keep\n", 200)
	out, ok := rtkEngine{}.compress(body)
	if !ok {
		t.Fatal("expected the total-size cap to fire")
	}
	if strings.Contains(out, "chars omitted") {
		t.Fatal("rtk elided the middle of a line")
	}
	for _, line := range strings.Split(out, "\n") {
		if line != long && strings.HasPrefix(line, long[:64]) {
			t.Fatalf("line rewritten in place: %.80s", line)
		}
	}
}

// #4559: a file read is not a build log. Neither the generic fallback filter nor
// the line/char cap may touch it.
func TestRTKLeavesDocumentReadsWhole(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 400; i++ {
		b.WriteString("the quick brown fox jumps over the lazy dog " + itoaDecimal(i) + "\n")
	}
	body := b.String()
	out, ok := rtkEngine{}.compress(body)
	if ok || out != body {
		t.Fatal("rtk rewrote a document-like read")
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
	noise := strings.Repeat("output   \n\n\n\n", 200)
	cfg := testConfig(EngineLite)
	yes, no := true, false
	opts := &cfg.Combos[0].Pipeline[0].Options
	opts.CompressToolResults = &yes
	opts.PreserveToolWhitespace = &no

	cases := map[string]string{
		"chat-completions": `{"model":"m","messages":[{"role":"user","content":` + quote("hi") + `},` +
			`{"role":"tool","tool_call_id":"c1","content":` + quote(noise) + `},` +
			`{"role":"user","content":"next"}]}`,
		"anthropic-messages": `{"model":"m","messages":[{"role":"user","content":[` +
			`{"type":"text","text":` + quote("hi") + `},` +
			`{"type":"tool_result","tool_use_id":"t1","content":` + quote(noise) + `}]},` +
			`{"role":"user","content":[{"type":"text","text":"next"}]}]}`,
		"responses": `{"model":"m","input":[` +
			`{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},` +
			`{"type":"function_call_output","call_id":"c1","output":` + quote(noise) + `},` +
			`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]},` +
			`{"type":"message","role":"user","content":[{"type":"input_text","text":"next"}]}]}`,
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
		// Content-array text is string-content-only for lite upstream; only the
		// plain string body collapses blank runs. Arrays shrink via truncation.
		if name == "chat-completions" && strings.Contains(string(out), `\n\n\n\n`) {
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

// appendStabilityBody builds a conversation of n turns: prose, a shell call, a
// noisy tool result and an assistant reply. Each turn only appends, so turn n is
// a strict prefix-extension of turn n-1.
func appendStabilityBody(n int) []byte {
	noisy := strings.Repeat("build output line with noise   \n\n\n\n", 40)
	var b strings.Builder
	b.WriteString(`{"model":"m","messages":[`)
	b.WriteString(`{"role":"system","content":` + quote("You are a coding agent. Keep diffs small.") + `},`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"role":"user","content":` + quote("turn "+itoa(i)+" please fix the failing test in pkg"+itoa(i)) + `},`)
		b.WriteString(`{"role":"assistant","content":null,"tool_calls":[{"id":"c` + itoa(i) +
			`","type":"function","function":{"name":"bash","arguments":"{\"command\":\"go test ./...\"}"}}]},`)
		b.WriteString(`{"role":"tool","tool_call_id":"c` + itoa(i) + `","content":` + quote(noisy) + `},`)
		b.WriteString(`{"role":"assistant","content":` + quote("I will patch pkg"+itoa(i)+" now. The fix touches one function.") + `}`)
	}
	b.WriteString(`]}`)
	return []byte(b.String())
}

// Appending a turn must leave the compressed bytes of every earlier message
// identical. A provider prompt cache is keyed on that prefix, so any drift here
// makes every turn a full-price prompt instead of a cache read.
func TestApplyIsAppendStable(t *testing.T) {
	cfg := testConfig(EngineSessionDedup, EngineLite, EngineRTK, EngineHeadroom, EngineCaveman)
	cfg.Combos[0].Pipeline[4].Level = "full"
	cfg.Sanitize()

	var prev []string
	for turns := 1; turns <= 5; turns++ {
		out, stats := Apply(cfg, "m", appendStabilityBody(turns))
		if !stats.Applied {
			t.Fatalf("turn %d: expected compression", turns)
		}
		var parsed struct {
			Messages []struct {
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(out, &parsed); err != nil {
			t.Fatalf("turn %d: output is not valid JSON: %v", turns, err)
		}
		cur := make([]string, len(parsed.Messages))
		for i, message := range parsed.Messages {
			cur[i] = string(message.Content)
		}
		for i, before := range prev {
			if i >= len(cur) || cur[i] != before {
				t.Fatalf("turn %d: message %d changed after appending a turn\nbefore: %q\nafter:  %q",
					turns, i, before, cur[i])
			}
		}
		prev = cur
	}
}

// Upstream lite never caps the tool results of the current turn: cutting what
// the model just asked for only makes it re-read the same file next turn.
func TestCurrentTurnToolResultsAreNotCapped(t *testing.T) {
	text := strings.Repeat("tool output line that would be cut\n", 100)
	toolTurn := func(id string) string {
		return `{"role":"assistant","content":null,"tool_calls":[{"id":"` + id +
			`","type":"function","function":{"name":"read","arguments":"{\"path\":\"/proj/big.txt\"}"}}]},` +
			`{"role":"tool","tool_call_id":"` + id + `","content":` + quote(text) + `}`
	}
	body := []byte(`{"model":"m","messages":[` + toolTurn("t1") + `,` + toolTurn("t2") + `]}`)

	cfg := testConfig(EngineLite)
	on := true
	cfg.Combos[0].Pipeline[0].Options.CompressToolResults = &on
	out, stats := Apply(cfg, "m", body)
	if !stats.Applied {
		t.Fatal("expected compression to apply")
	}
	var parsed struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if got := parsed.Messages[1].Content; !strings.Contains(got, "...[truncated]") {
		t.Fatalf("history tool result should be capped, got %d chars", len(got))
	}
	if got := parsed.Messages[3].Content; got != text {
		t.Fatalf("current-turn tool result was rewritten (%d of %d chars)", len(got), len(text))
	}
}

// Upstream never rewrites the current turn: a marker where the model expects the
// result it just asked for reads as a lost read, so the duplicate is elided one
// turn later instead. That deferral is the one place dedup trades append
// stability (warm provider cache) for not making the model re-fetch content.
func TestDedupDefersCurrentTurn(t *testing.T) {
	block := strings.Repeat("same tool output line\n", 10)
	turn := func(id string) string {
		return `{"role":"assistant","content":null,"tool_calls":[{"id":"` + id +
			`","type":"function","function":{"name":"bash","arguments":"{}"}}]},` +
			`{"role":"tool","tool_call_id":"` + id + `","content":` + quote(block) + `}`
	}
	// Each turn ends on its tool result, so the last one is the current turn.
	pair := []byte(`{"model":"m","messages":[` + turn("c1") + `,` + turn("c2") + `]}`)
	if out, _ := Apply(testConfig(EngineSessionDedup), "m", pair); strings.Contains(string(out), dedupRefPrefix) {
		t.Fatal("current turn must not be rewritten")
	}
	triple := []byte(`{"model":"m","messages":[` + turn("c1") + `,` + turn("c2") + `,` + turn("c3") + `]}`)
	out, stats := Apply(testConfig(EngineSessionDedup), "m", triple)
	if !stats.Applied || !strings.Contains(string(out), dedupRefPrefix) {
		t.Fatal("a duplicate that left the current turn should be elided")
	}
	if strings.Count(string(out), "same tool output line") != 20 {
		t.Fatalf("the owner and the current-turn copy must both stay intact:\n%s", out)
	}
}

// Upstream lite caps only string tool content: a content array on a role:"tool"
// message is not a tool_result block, so truncateToolResultBlocks never sees it.
func TestLiteLeavesToolMessageBlocksUncapped(t *testing.T) {
	long := strings.Repeat("y", 3000)
	body := []byte(`{"model":"m","messages":[` +
		`{"role":"user","content":"hi"},` +
		`{"role":"assistant","content":"a"},` +
		`{"role":"tool","content":[{"type":"text","text":` + quote(long) + `}]},` +
		`{"role":"assistant","content":"b"}]}`)
	out, _ := Apply(testConfig(EngineLite), "m", body)
	if got := gjson.GetBytes(out, "messages.2.content.0.text").String(); got != long {
		t.Fatal("lite capped text in a tool content array")
	}
}

// Upstream counts apply_patch_call_output as a Responses tool output. Missing it
// ended the trailing current-turn window before the function_call_output in front
// of it, so that output was capped as if it were history.
func TestApplyPatchCallOutputStaysInCurrentTurn(t *testing.T) {
	long := strings.Repeat("z", 3000)
	body := []byte(`{"model":"m","input":[` +
		`{"type":"function_call_output","call_id":"c1","output":` + quote(long) + `},` +
		`{"type":"apply_patch_call_output","call_id":"c2","output":"ok"}]}`)
	out, _ := Apply(testConfig(EngineLite), "m", body)
	if got := gjson.GetBytes(out, "input.0.output").String(); got != long {
		t.Fatal("a trailing Responses tool output was capped")
	}
}

// Upstream leaves the newest user turn as written when earlier turns exist:
// clients append memory and quoted history to it, which would push it past the
// minimum length and let the rules delete the user's own words.
func TestCavemanDefersLatestUserTurn(t *testing.T) {
	text := "I would like you to please basically just check the thing, thanks in advance. " +
		"Memory: the user prefers short answers and always writes Go."
	body := []byte(`{"model":"m","messages":[` +
		`{"role":"user","content":"first question"},` +
		`{"role":"assistant","content":"first answer"},` +
		`{"role":"user","content":` + quote(text) + `}]}`)
	out, _ := Apply(testConfig(EngineCaveman), "m", body)
	if got := gjson.GetBytes(out, "messages.2.content").String(); got != text {
		t.Fatalf("latest user turn was rewritten: %q", got)
	}
	// A one-shot body has no earlier turn, so it still compresses.
	one := []byte(`{"model":"m","messages":[{"role":"user","content":` + quote(text) + `}]}`)
	out, _ = Apply(testConfig(EngineCaveman), "m", one)
	if gjson.GetBytes(out, "messages.0.content").String() == text {
		t.Fatal("a one-shot body must still compress")
	}
}

func useTestEngine(t *testing.T, fn func(Step) engine) {
	t.Helper()
	testEngineHook = fn
	t.Cleanup(func() { testEngineHook = nil })
}

type probeEngine struct{ ran *bool }

func (probeEngine) name() string { return "probe" }
func (e probeEngine) compress(string) (string, bool) {
	*e.ran = true
	return "x", true
}

type inflateEngine struct{}

func (inflateEngine) name() string { return "inflate" }
func (inflateEngine) compress(text string) (string, bool) {
	return text + " growth growth growth", true
}

type panicEngine struct{}

func (panicEngine) name() string                   { return "panic" }
func (panicEngine) compress(string) (string, bool) { panic("boom") }

func probeBody() []byte {
	return []byte(`{"model":"m","messages":[{"role":"user","content":"hello hello hello"}]}`)
}

func TestApplyExclusionsSkipWholePipeline(t *testing.T) {
	var ran bool
	useTestEngine(t, func(step Step) engine {
		if step.Engine == "probe" {
			return probeEngine{ran: &ran}
		}
		return nil
	})

	cfg := testConfig("probe")
	cfg.Exclusions = []string{"Claude-*"}
	out, stats := Apply(cfg, "claude-sonnet", probeBody())
	if ran {
		t.Fatal("engine must not run for an excluded model")
	}
	if stats.Applied || !bytes.Equal(out, probeBody()) {
		t.Fatal("excluded model must be returned byte-identical")
	}

	ran = false
	cfg.Exclusions = nil
	if _, stats := Apply(cfg, "claude-sonnet", probeBody()); !stats.Applied || !ran {
		t.Fatal("a non-excluded model must still compress")
	}
}

func TestApplyRevertsInflatingStep(t *testing.T) {
	useTestEngine(t, func(step Step) engine {
		if step.Engine == "inflate" {
			return inflateEngine{}
		}
		return nil
	})
	out, stats := Apply(testConfig("inflate"), "m", probeBody())
	if stats.Applied || !bytes.Equal(out, probeBody()) {
		t.Fatal("an inflating step must be reverted")
	}
	if len(stats.Saved) != 0 {
		t.Fatalf("reverted step must not report savings: %v", stats.Saved)
	}
}

func TestApplyRecoversFromEnginePanic(t *testing.T) {
	useTestEngine(t, func(step Step) engine {
		if step.Engine == "panic" {
			return panicEngine{}
		}
		return nil
	})
	out, stats := Apply(testConfig("panic"), "m", probeBody())
	if stats.Applied || !bytes.Equal(out, probeBody()) {
		t.Fatal("a panicking engine must fail open to the original body")
	}
}

func TestApplyInvalidJSONFallsBack(t *testing.T) {
	body := []byte(`{"model":"m","messages":[`) // truncated, not valid JSON
	out, stats := Apply(testConfig(EngineLite), "m", body)
	if stats.Applied || !bytes.Equal(out, body) {
		t.Fatal("invalid JSON must be returned unchanged")
	}
}

func TestGatesOffByDefault(t *testing.T) {
	def := DefaultConfig()
	if (Config{}).HardBudgetTokens != 0 || def.HardBudgetTokens != 0 {
		t.Fatal("hard budget must default off")
	}
	for _, step := range def.Combos[0].Pipeline {
		if step.Options.FidelityGate != nil || step.Options.RiskGate != nil {
			t.Fatalf("per-step gates must default off for %s", step.Engine)
		}
	}
	var ran bool
	useTestEngine(t, func(step Step) engine { return probeEngine{ran: &ran} })
	if _, stats := Apply(testConfig("probe"), "m", probeBody()); !stats.Applied || !ran {
		t.Fatal("default options must not block compression")
	}
}

func TestDefaultConfigPreservesToolResults(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 400; i++ {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("tool line ")
		b.WriteString(itoa(i))
		b.WriteString(" value")
	}
	content := b.String()
	body := []byte(`{"model":"m","messages":[` +
		`{"role":"user","content":"keep this"},` +
		`{"role":"tool","tool_call_id":"c1","content":` + quote(content) + `}]}`)
	cfg := DefaultConfig()
	cfg.Enabled = true
	out, _ := Apply(cfg, "m", body)
	if got := gjson.GetBytes(out, "messages.1.content").String(); got != content {
		t.Fatalf("default pipeline altered a tool result: %d -> %d bytes", len(content), len(got))
	}
}
