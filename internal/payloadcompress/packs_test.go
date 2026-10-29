package payloadcompress

import (
	"strconv"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

func TestLanguagePacksLoad(t *testing.T) {
	tags := languageTags()
	if len(tags) < 11 {
		t.Fatalf("expected at least 11 packs, got %v", tags)
	}
	for _, tag := range tags {
		rules := packRulesFor(tag)
		if len(rules) < 10 {
			t.Fatalf("%s: only %d rules", tag, len(rules))
		}
		// Every pack must fall back to English for the categories it omits.
		if len(filterRulesByLevel(rules, "lite")) == 0 {
			t.Fatalf("%s: no lite rules", tag)
		}
	}
}

func TestPackRuleOrderingPutsPhrasingFirst(t *testing.T) {
	rules := packRulesFor("en")
	if len(rules) == 0 {
		t.Fatal("no english rules")
	}
	// structural runs before filler so phrase rules consume text before single
	// words do; redundant_phrasing must precede pleasantries.
	phrasing, pleasantries := -1, -1
	for i, rule := range rules {
		switch rule.name {
		case "redundant_phrasing":
			phrasing = i
		case "pleasantries":
			pleasantries = i
		}
	}
	if phrasing < 0 || pleasantries < 0 {
		t.Fatalf("rules missing: phrasing=%d pleasantries=%d", phrasing, pleasantries)
	}
	if phrasing > pleasantries {
		t.Fatalf("redundant_phrasing (%d) must run before pleasantries (%d)", phrasing, pleasantries)
	}
}

func TestUnsupportedPatternsAreSkipped(t *testing.T) {
	// Backreferences cannot be expressed in RE2 and must be dropped, not guessed.
	if _, ok := compilePackRule(packRule{
		Name:        "backref",
		Pattern:     `([\p{L}]+)\s+\1`,
		Replacement: strPtr("$1"),
	}); ok {
		t.Fatal("backreference rule must be rejected")
	}
	if _, ok := compilePackRule(packRule{
		Name:    "lookahead",
		Pattern: `foo(?=bar)`,
	}); ok {
		t.Fatal("unsupported lookahead must be rejected")
	}
}

func TestLookbehindBecomesGuard(t *testing.T) {
	rule, ok := compilePackRule(packRule{
		Name:        "guard",
		Pattern:     `(?<!make\s)(?<!be\s)\b(?:sure)\b\s*`,
		Replacement: strPtr(""),
	})
	if !ok {
		t.Fatal("rule should compile")
	}
	if len(rule.notAfter) != 2 {
		t.Fatalf("guards = %v", rule.notAfter)
	}
	if got := rule.apply("make sure to run"); got != "make sure to run" {
		t.Fatalf("guard did not protect: %q", got)
	}
	if got := rule.apply("I am sure about it"); got != "I am about it" {
		t.Fatalf("guard blocked a valid match: %q", got)
	}
}

func TestDetectLanguage(t *testing.T) {
	cases := map[string]string{
		"请解释这个函数的错误":                                     "zh",
		"この関数のエラーを説明してください":                              "ja",
		"preciso corrigir o arquivo porque falhou":       "pt-BR",
		"necesito corregir el archivo porque fallo":      "es",
		"bitte korrigiere die datei weil der fehler kam": "de",
		"please fix the parser bug in this function":     "en",
	}
	for text, want := range cases {
		if got := detectLanguage(text); got != want {
			t.Fatalf("%q -> %s, want %s", text, got, want)
		}
	}
}

func TestCavemanUsesPortuguesePack(t *testing.T) {
	engine := cavemanEngine{level: "lite", opts: StepOptions{Language: "pt-BR"}}
	text := "Por favor, obrigado, preciso basicamente corrigir o arquivo porque falhou. " +
		"Basicamente obrigado por favor."
	out, ok := engine.compressKind(kindUser, text)
	if !ok {
		t.Fatal("expected compression with pt-BR pack")
	}
	if strings.Contains(strings.ToLower(out), "por favor") || strings.Contains(strings.ToLower(out), "obrigado") {
		t.Fatalf("pt-BR fillers survived:\n%s", out)
	}
	if !strings.Contains(strings.ToLower(out), "arquivo") {
		t.Fatalf("meaning lost:\n%s", out)
	}
}

func TestCavemanAutoDetect(t *testing.T) {
	text := "Preciso corrigir o arquivo porque falhou obrigado, por favor basicamente."
	if got := detectLanguage(text); got != "pt-BR" {
		t.Fatalf("detected %s", got)
	}
	// Detection only picks among the enabled packs, which default to English.
	strict := cavemanEngine{level: "lite"}
	if got := strict.pack(text); got != "en" {
		t.Fatalf("unexpected pack %s without enabled packs", got)
	}
	loose := cavemanEngine{level: "lite", opts: StepOptions{LanguagePacks: []string{"en", "pt-BR"}}}
	if got := loose.pack(text); got != "pt-BR" {
		t.Fatalf("auto-detect picked %s, want pt-BR", got)
	}
	off := false
	pinned := cavemanEngine{level: "lite", opts: StepOptions{LanguagePacks: []string{"en", "pt-BR"}, AutoDetect: &off}}
	if got := pinned.pack(text); got != "en" {
		t.Fatalf("auto-detect=false still detected %s", got)
	}
}

func TestCavemanSkipRules(t *testing.T) {
	engine := cavemanEngine{level: "lite", opts: StepOptions{SkipRules: []string{"filler_adverbs"}}}
	text := "Please basically fix the parser, it is very broken in this function."
	out, _ := engine.compressKind(kindUser, text)
	if !strings.Contains(strings.ToLower(out), "basically") {
		t.Fatalf("skipped rule still applied:\n%s", out)
	}
	if strings.Contains(strings.ToLower(out), "please") {
		t.Fatalf("other rules stopped working:\n%s", out)
	}
}

func TestCavemanPreservesErrorLines(t *testing.T) {
	engine := cavemanEngine{level: "ultra"}
	text := "Please basically look at this failure:\n" +
		"TypeError: Cannot read properties of undefined (reading 'x')\n" +
		"    at veryLongFunctionName (/srv/app/src/index.js:10:5)\n" +
		"Thanks, it is probably a database configuration problem."
	out, ok := engine.compressKind(kindUser, text)
	if !ok {
		t.Fatal("expected compression")
	}
	if !strings.Contains(out, "TypeError: Cannot read properties of undefined (reading 'x')") {
		t.Fatalf("error line was rewritten:\n%s", out)
	}
	if !strings.Contains(out, "at veryLongFunctionName (/srv/app/src/index.js:10:5)") {
		t.Fatalf("stack frame was rewritten:\n%s", out)
	}
}

func TestEngineOptionOverrides(t *testing.T) {
	rows := `[{"a":1,"description":"padding so the size gate does not reject this array, adding more characters to be safe"},` +
		`{"a":2,"description":"padding so the size gate does not reject this array, adding more characters to be safe"},` +
		`{"a":3,"description":"padding so the size gate does not reject this array, adding more characters to be safe"},` +
		`{"a":4,"description":"padding so the size gate does not reject this array, adding more characters to be safe"},` +
		`{"a":5,"description":"padding so the size gate does not reject this array, adding more characters to be safe"}]`
	if _, ok := (headroomEngine{}).compress(rows); ok {
		t.Fatal("default min-rows must reject 5 rows")
	}
	if _, ok := (headroomEngine{opts: StepOptions{MinRows: 5}}).compress(rows); !ok {
		t.Fatal("min-rows override was ignored")
	}

	block := strings.Repeat("repeat me ", 40)
	texts := []string{block, block}
	if got := (&dedupEngine{}).rewrite(texts); got == nil {
		t.Fatal("dedup should elide an 400-byte duplicate")
	}
	if got := (&dedupEngine{opts: StepOptions{MinBlockChars: 1 << 20}}).rewrite(texts); got != nil {
		t.Fatal("min-block-chars override was ignored")
	}
}

func TestRTKIntensityLadder(t *testing.T) {
	// A pair of identical lines: the upstream default threshold (3) keeps them,
	// aggressive (2) collapses them.
	text := "build start\nretrying\nretrying\n" +
		strings.Join([]string{"a", "b", "c", "d", "e", "f", "g", "h"}, "\n") + "\nbuild done\n"

	if out, ok := (rtkEngine{}).compress(text); ok {
		t.Fatalf("default threshold must not collapse a run of 2:\n%s", out)
	}
	aggOut, okAgg := (rtkEngine{opts: StepOptions{Intensity: "aggressive"}}).compress(text)
	if !okAgg {
		t.Fatal("aggressive should collapse the run")
	}
	if !strings.Contains(aggOut, "retrying"+rtkRepeatMark+"2)") {
		t.Fatalf("aggressive did not collapse:\n%s", aggOut)
	}

	// An explicit deduplicate-threshold override wins over the ladder, and the
	// upstream default is 3.
	thresh := rtkEngine{opts: StepOptions{Intensity: "minimal", DeduplicateThreshold: 2}}
	threshOut, okThresh := thresh.compress(text)
	if !okThresh || !strings.Contains(threshOut, "retrying"+rtkRepeatMark+"2)") {
		t.Fatalf("deduplicate-threshold override ignored:\n%s", threshOut)
	}
}

func TestRTKDefaultCapsMatchUpstream(t *testing.T) {
	limits := rtkEngine{}.limits()
	if limits.minRun != 3 || limits.maxLines != 120 || limits.maxChars != 12000 {
		t.Fatalf("default limits = %+v", limits)
	}
	if limits.minSavings != 0 {
		t.Fatalf("default savings gate must be off, got %d", limits.minSavings)
	}
}

// TestRTKMaxLinesCap pins the line pass: head and tail survive and the middle is
// reported as dropped lines. Upstream keeps preserveHead/preserveTail lines
// whatever max-lines says, so a tiny cap still yields 48 body lines.
func TestRTKMaxLinesCap(t *testing.T) {
	var lines []string
	for i := 0; i < 500; i++ {
		lines = append(lines, "distinct output line number "+itoaDecimal(i))
	}
	out, ok := rtkEngine{opts: StepOptions{MaxLines: 40}}.compress(strings.Join(lines, "\n"))
	if !ok {
		t.Fatal("expected the line cap to fire")
	}
	if !strings.Contains(out, "[rtk:truncated 452 lines]") {
		t.Fatalf("missing dropped-line marker: %q", out)
	}
	if !strings.Contains(out, "line number 0") || !strings.Contains(out, "line number 499") {
		t.Fatal("head and tail must survive")
	}
	if strings.Contains(out, "line number 250") {
		t.Fatal("the middle must be dropped")
	}
}

// TestRTKKeepsMiddleDiagnostics pins the reason this port exists: an error line
// buried in the middle of dropped output must survive the cap.
func TestRTKKeepsMiddleDiagnostics(t *testing.T) {
	var lines []string
	for i := 0; i < 500; i++ {
		if i == 250 {
			lines = append(lines, "FATAL: rollback required")
			continue
		}
		lines = append(lines, "chatter "+itoaDecimal(i))
	}
	out, ok := rtkEngine{opts: StepOptions{MaxLines: 40}}.compress(strings.Join(lines, "\n"))
	if !ok {
		t.Fatal("expected the line cap to fire")
	}
	if !strings.Contains(out, "FATAL: rollback required") {
		t.Fatal("a diagnostic line from the middle must survive")
	}
}

func TestLiteToolResultCap(t *testing.T) {
	long := strings.Repeat("x", 2000)
	engine := liteEngine{opts: StepOptions{MaxToolLength: 500}}
	out, ok := engine.compressKind(kindToolOutput, long)
	if !ok {
		t.Fatal("expected truncation")
	}
	// The cap is characters: 500 kept plus the marker, never 500 bytes.
	if n := charLen(out); n > 500+charLen(toolTruncationMarker) {
		t.Fatalf("not truncated: %d chars", n)
	}
	if !strings.Contains(out, "...[truncated]") {
		t.Fatalf("missing marker: %q", out)
	}
	off := false
	disabled := liteEngine{opts: StepOptions{MaxToolLength: 500, CompressToolResults: &off}}
	if out, ok := disabled.compressKind(kindToolOutput, long); ok || len(out) != len(long) {
		t.Fatal("compress-tool-results=false must disable the cap")
	}
}

// TestLiteToolResultCapCountsChars pins the cap to characters: 300 CJK
// characters are 900 bytes but only 300 characters, so a 500 cap keeps them.
func TestLiteToolResultCapCountsChars(t *testing.T) {
	engine := liteEngine{opts: StepOptions{MaxToolLength: 500}}
	if out, ok := engine.compressKind(kindToolOutput, strings.Repeat("漢", 300)); ok {
		t.Fatalf("300 chars under a 500 char cap must not truncate, got %d bytes", len(out))
	}
	out, ok := engine.compressKind(kindToolOutput, strings.Repeat("漢", 600))
	if !ok {
		t.Fatal("expected truncation")
	}
	if !utf8.ValidString(out) {
		t.Fatal("truncation split a multi-byte rune")
	}
	if n := utf8.RuneCountInString(out); n > 560 {
		t.Fatalf("cap is %d chars, not bytes: got %d", 500, n)
	}
}

func strPtr(v string) *string { return &v }

// TestDefaultKnobsMatchUpstream pins every engine default to the value OmniRoute
// ships, so a drift shows up as a test failure rather than as silent behavior.
func TestDefaultKnobsMatchUpstream(t *testing.T) {
	var hr headroomEngine
	if hr.name() != EngineHeadroom {
		t.Fatal("headroom engine missing")
	}
	if headroomDefaultRows != 8 {
		t.Fatalf("headroom min-rows = %d", headroomDefaultRows)
	}
	if dedupDefaultMinSize != 80 {
		t.Fatalf("session-dedup min-block-chars = %d", dedupDefaultMinSize)
	}
	if liteDefaultMaxToolLength != 2000 {
		t.Fatalf("lite max-tool-length = %d", liteDefaultMaxToolLength)
	}
	var li liteEngine
	if !li.keepToolResults() {
		t.Fatal("lite compress-tool-results must default to true")
	}
	if limits := (rtkEngine{}).limits(); limits != (rtkLimits{minRun: 3, maxLines: 120, maxChars: 12000, preserveHead: 24, preserveTail: 24}) {
		t.Fatalf("rtk defaults = %+v", limits)
	}
	if got := (cavemanEngine{}).pack("  "); got != "en" {
		t.Fatalf("caveman default language = %s", got)
	}
	if autoDetect := (cavemanEngine{}); !autoDetect.packEnabled("en") || autoDetect.packEnabled("pt-BR") {
		t.Fatal("caveman default enabled packs must be English only")
	}
}

// TestRTKCharPassKeepsWholeLines pins the char pass to line boundaries. A
// mid-line cut leaves fragments that read like real output, so every surviving
// line must exist verbatim in the input.
func TestRTKCharPassKeepsWholeLines(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 300; i++ {
		if i == 150 {
			b.WriteString("FATAL: rollback required\n")
			continue
		}
		b.WriteString("line " + strconv.Itoa(i) + " " + strings.Repeat("漢", 40) + "\n")
	}
	in := b.String()
	const limit = 1200
	out, ok := rtkEngine{opts: StepOptions{MaxChars: limit}}.compress(in)
	if !ok {
		t.Fatal("expected a rewrite")
	}
	if !strings.Contains(out, "[rtk:truncated by chars]") {
		t.Fatalf("missing char marker: %q", out)
	}
	if !utf8.ValidString(out) {
		t.Fatal("cut split a multi-byte rune")
	}
	if n := charLen(out); n > limit {
		t.Fatalf("char cap exceeded: %d chars", n)
	}
	if !strings.Contains(out, "FATAL: rollback required") {
		t.Fatal("a diagnostic line must survive the char pass")
	}
	for _, line := range strings.Split(out, "\n") {
		if line == "" || strings.HasPrefix(line, "[rtk:") {
			continue
		}
		if !strings.Contains(in, line+"\n") {
			t.Fatalf("line was cut mid-line: %q", line)
		}
	}
}

// TestLiteToolResultCapElidesLines pins the multi-line branch: the head cut lands
// on a word boundary, the tail resumes on a line boundary, the dropped lines are
// counted, and the result stays inside the cap.
func TestLiteToolResultCapElidesLines(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 60; i++ {
		b.WriteString("row " + strconv.Itoa(i) + " " + strings.Repeat("漢", 60) + "\n")
	}
	in := b.String()
	const limit = 1000
	out, ok := liteEngine{opts: StepOptions{MaxToolLength: limit}}.compressKind(kindToolOutput, in)
	if !ok {
		t.Fatal("expected truncation")
	}
	if !utf8.ValidString(out) {
		t.Fatal("cut split a multi-byte rune")
	}
	marker := strings.Index(out, "\n... [")
	if marker < 0 || !strings.Contains(out, "lines elided]") {
		t.Fatalf("multi-line cut must report elided lines: %q", out)
	}
	head := out[:marker]
	if !strings.HasPrefix(in, head) {
		t.Fatalf("head is not a prefix of the input: %q", head)
	}
	// The head ends on a word boundary: a hard cut would leave a partial word.
	if next, _ := utf8.DecodeRuneInString(in[len(head):]); !unicode.IsSpace(next) {
		t.Fatalf("head ends mid-word: %q", head[len(head)-20:])
	}
	// Everything after the elision marker is the tail, which must start on a line.
	tail := out[strings.Index(out, "] ...\n")+len("] ...\n"):]
	for _, line := range strings.Split(tail, "\n") {
		if line == "" || line == "...[truncated]" {
			continue
		}
		if !strings.Contains(in, line+"\n") {
			t.Fatalf("tail line was cut mid-line: %q", line)
		}
	}
	// The tail starts on the first line that begins inside its share, so the cap is
	// hard: a long last line can no longer push the result past the budget.
	if n := charLen(out); n > limit+charLen(toolTruncationMarker) {
		t.Fatalf("cap exceeded: %d chars for a %d cap", n, limit)
	}
}

// TestLiteTailFitsItsBudget pins the tail budget: the tail starts on the first
// line that begins inside its share, so a long line at the boundary can no longer
// push the result past the cap.
func TestLiteTailFitsItsBudget(t *testing.T) {
	lines := make([]string, 50)
	for i := range lines {
		label := strconv.Itoa(i+1) + ":"
		lines[i] = label + strings.Repeat("x", 200-len(label))
	}
	in := strings.Join(lines, "\n")
	out, ok := liteEngine{}.compressKind(kindToolOutput, in)
	if !ok {
		t.Fatal("expected truncation")
	}
	if n := charLen(out); n > liteDefaultMaxToolLength+64 {
		t.Fatalf("output is %d chars, cap is %d", n, liteDefaultMaxToolLength)
	}
	if !strings.HasPrefix(out, "1:") {
		t.Fatal("the head must survive the cut")
	}
	if !strings.Contains(out, "\n"+lines[49]+"\n") {
		t.Fatal("the last line must survive whole")
	}
	if !strings.Contains(out, "lines elided] ...") || !strings.HasSuffix(out, "...[truncated]") {
		t.Fatalf("missing markers: %q", out[len(out)-80:])
	}
}

// TestLiteTailDroppedWhenNoLineFits pins the fallback: when the last line alone is
// larger than the whole tail share, the tail is dropped instead of partially kept.
func TestLiteTailDroppedWhenNoLineFits(t *testing.T) {
	for _, width := range []int{1000, 20000} {
		for _, count := range []int{11, 30} {
			lines := make([]string, count)
			for i := range lines {
				label := strconv.Itoa(i+1) + ":"
				lines[i] = label + strings.Repeat("y", width-len(label))
			}
			out, ok := liteEngine{}.compressKind(kindToolOutput, strings.Join(lines, "\n"))
			if !ok {
				t.Fatalf("%d x %d: expected truncation", count, width)
			}
			if n := charLen(out); n > liteDefaultMaxToolLength+64 {
				t.Fatalf("%d x %d: output is %d chars", count, width, n)
			}
			if !strings.HasPrefix(out, "1:") {
				t.Fatalf("%d x %d: the head must survive the cut", count, width)
			}
			if strings.Contains(out, "lines elided") {
				t.Fatalf("%d x %d: no partial tail line may be kept", count, width)
			}
		}
	}
}
