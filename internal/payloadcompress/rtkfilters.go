package payloadcompress

import (
	"crypto/sha256"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	log "github.com/sirupsen/logrus"
)

// rtkFiltersFS holds the command filters taken from OmniRoute's
// engines/rtk/filters/ tree. They are data, not code: one JSON pack per command,
// each carrying its own inline tests, so the port is verified against upstream's
// expectations instead of against a re-reading of the rules.
//
// Two upstream layers have no counterpart here. Command detection by command
// name needs the originating command line, which a chat payload does not carry,
// so filters are selected by the output's own content. Patterns Go's RE2 cannot
// express (lookaround, backreferences) fall back to a backtracking engine with a
// match timeout rather than being dropped: dropping was safe but silently
// disabled the structured-output guards the packs were written to provide.
//
//go:embed rtkfilters
var rtkFiltersFS embed.FS

// rtkFilterTest is one inline expectation shipped with a filter pack.
type rtkFilterTest struct {
	Name     string `json:"name"`
	Input    string `json:"input"`
	Expected string `json:"expected"`
}

type rtkReplaceRule struct {
	Pattern     string `json:"pattern"`
	Replacement string `json:"replacement"`
}

type rtkMatchOutputRule struct {
	Pattern string `json:"pattern"`
	Message string `json:"message"`
	Unless  string `json:"unless"`
}

// rtkFilterPack is the on-disk shape. Only the fields this port implements are
// declared; unknown keys are ignored, so a newer upstream pack still loads.
type rtkFilterPack struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Category string `json:"category"`
	Priority int    `json:"priority"`
	Match    struct {
		OutputTypes []string `json:"outputTypes"`
		Patterns    []string `json:"patterns"`
		// Commands matches the shell command line that produced the output.
		// It is upstream's primary selector and the only reliable one for
		// command output whose text looks generic, such as a build log.
		Commands []string `json:"commands"`
	} `json:"match"`
	Rules struct {
		StripAnsi        bool                 `json:"stripAnsi"`
		Replace          []rtkReplaceRule     `json:"replace"`
		MatchOutput      []rtkMatchOutputRule `json:"matchOutput"`
		IncludePatterns  []string             `json:"includePatterns"`
		DropPatterns     []string             `json:"dropPatterns"`
		CollapsePatterns []string             `json:"collapsePatterns"`
		Deduplicate      bool                 `json:"deduplicate"`
		TruncateLineAt   int                  `json:"truncateLineAt"`
		MaxLines         int                  `json:"maxLines"`
		HeadLines        int                  `json:"headLines"`
		TailLines        int                  `json:"tailLines"`
		OnEmpty          string               `json:"onEmpty"`
	} `json:"rules"`
	Preserve struct {
		ErrorPatterns   []string `json:"errorPatterns"`
		SummaryPatterns []string `json:"summaryPatterns"`
	} `json:"preserve"`
	Tests []rtkFilterTest `json:"tests"`
}

// rtkFilter is a compiled pack.
type rtkFilter struct {
	id         string
	priority   int
	sourceRank int
	formatRank int
	types      []string
	commands   []*packRegex
	match      []*packRegex
	keep       []*packRegex
	drop       []*packRegex
	collapse   []*packRegex
	rescue     []*packRegex
	replace    []rtkCompiledReplace
	emit       []rtkCompiledEmit
	tests      []rtkFilterTest

	stripAnsi      bool
	truncateLineAt int
	deduplicate    bool
	maxLines       int
	headLines      int
	tailLines      int
	onEmpty        string
}

type rtkCompiledReplace struct {
	re   *packRegex
	repl string
}

type rtkCompiledEmit struct {
	re      *packRegex
	unless  *packRegex
	message string
}

var (
	rtkFiltersOnce sync.Once
	rtkFilterList  []*rtkFilter
	rtkFilterKinds map[string][]*packRegex
	// rtkFilterCommands maps a filter type to the command lines it claims,
	// compiled from each pack's match.commands.
	rtkFilterCommands map[string][]*packRegex
	// rtkDetectorCommands holds the detectors that carry their own command
	// patterns. Upstream uses these instead of the pack's match.commands; they
	// differ where a pack claims a broader command family than its detector.
	rtkDetectorCommands map[string][]*packRegex
)

// compileRTKCommands compiles a pack's command patterns. Upstream matches them
// case-insensitively against the whole command line.
func compileRTKCommands(id string, patterns []string) []*packRegex {
	prefixed := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		prefixed = append(prefixed, "(?i)"+pattern)
	}
	return compileRTKPatterns(id, "command", prefixed)
}

// compileRTKPatterns compiles a pack's patterns, falling back to the
// backtracking engine for the lookaround and backreference constructs RE2
// rejects. Upstream instead guards against ReDoS with a heuristic; here the
// linear-time engine covers almost everything and the fallback carries a match
// timeout.
func compileRTKPatterns(id, field string, patterns []string) []*packRegex {
	return compilePackRegexes("rtk filter "+id, field, patterns)
}

func compileRTKFilter(pack rtkFilterPack) *rtkFilter {
	f := &rtkFilter{
		id:             pack.ID,
		priority:       pack.Priority,
		sourceRank:     1,
		formatRank:     1,
		types:          pack.Match.OutputTypes,
		commands:       compileRTKCommands(pack.ID, pack.Match.Commands),
		match:          compileRTKPatterns(pack.ID, "match", pack.Match.Patterns),
		keep:           compileRTKPatterns(pack.ID, "include", pack.Rules.IncludePatterns),
		drop:           compileRTKPatterns(pack.ID, "drop", pack.Rules.DropPatterns),
		collapse:       compileRTKPatterns(pack.ID, "collapse", pack.Rules.CollapsePatterns),
		rescue:         compileRTKPatterns(pack.ID, "preserve", append(append([]string{}, pack.Preserve.ErrorPatterns...), pack.Preserve.SummaryPatterns...)),
		stripAnsi:      pack.Rules.StripAnsi,
		truncateLineAt: pack.Rules.TruncateLineAt,
		deduplicate:    pack.Rules.Deduplicate,
		maxLines:       pack.Rules.MaxLines,
		headLines:      pack.Rules.HeadLines,
		tailLines:      pack.Rules.TailLines,
		onEmpty:        pack.Rules.OnEmpty,
		tests:          pack.Tests,
	}
	// Upstream's schema defaults headLines/tailLines to 20 when a custom pack
	// omits them; a zero here would drop the entire head and tail.
	if f.headLines == 0 {
		f.headLines = 20
	}
	if f.tailLines == 0 {
		f.tailLines = 20
	}
	for _, rule := range pack.Rules.Replace {
		re, err := compilePackRegex(rule.Pattern)
		if err != nil {
			continue
		}
		f.replace = append(f.replace, rtkCompiledReplace{re: re, repl: rule.Replacement})
	}
	for _, rule := range pack.Rules.MatchOutput {
		re, err := compilePackRegex("(?im)" + rule.Pattern)
		if err != nil {
			continue
		}
		var unless *packRegex
		if rule.Unless != "" {
			if compiled, errUnless := compilePackRegex("(?im)" + rule.Unless); errUnless == nil {
				unless = compiled
			}
		}
		f.emit = append(f.emit, rtkCompiledEmit{re: re, unless: unless, message: rule.Message})
	}
	return f
}

// loadRTKFilters returns the packs in upstream priority order.
func loadRTKFilters() []*rtkFilter {
	rtkFiltersOnce.Do(func() {
		entries, err := fs.ReadDir(rtkFiltersFS, "rtkfilters")
		if err != nil {
			log.Warnf("payloadcompress: rtk filters: %v", err)
			return
		}
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
				names = append(names, entry.Name())
			}
		}
		sort.Strings(names)
		rtkFilterKinds = make(map[string][]*packRegex, len(rtkDetectors))
		for _, detector := range rtkDetectors {
			rtkFilterKinds[detector.typ] = compileRTKPatterns(detector.typ, "content", detector.patterns)
		}
		for _, name := range names {
			raw, errRead := rtkFiltersFS.ReadFile("rtkfilters/" + name)
			if errRead != nil {
				log.Warnf("payloadcompress: rtk filter %s: %v", name, errRead)
				continue
			}
			var pack rtkFilterPack
			if errUnmarshal := json.Unmarshal(raw, &pack); errUnmarshal != nil {
				log.Warnf("payloadcompress: rtk filter %s: %v", name, errUnmarshal)
				continue
			}
			if pack.ID == "" {
				continue
			}
			rtkFilterList = append(rtkFilterList, compileRTKFilter(pack))
		}
		rtkFilterList = append(rtkFilterList, loadCustomRTKFilters()...)
		rtkFilterCommands = make(map[string][]*packRegex, len(rtkFilterList))
		for _, filter := range rtkFilterList {
			for _, typ := range filter.types {
				if len(filter.commands) == 0 {
					continue
				}
				rtkFilterCommands[typ] = append(rtkFilterCommands[typ], filter.commands...)
			}
		}
		rtkDetectorCommands = make(map[string][]*packRegex)
		for _, detector := range rtkDetectors {
			if len(detector.commands) > 0 {
				rtkDetectorCommands[detector.typ] = compileRTKCommands(detector.typ, detector.commands)
			}
		}
		sort.SliceStable(rtkFilterList, func(i, j int) bool {
			if rtkFilterList[i].sourceRank != rtkFilterList[j].sourceRank {
				return rtkFilterList[i].sourceRank > rtkFilterList[j].sourceRank
			}
			if rtkFilterList[i].formatRank != rtkFilterList[j].formatRank {
				return rtkFilterList[i].formatRank > rtkFilterList[j].formatRank
			}
			if rtkFilterList[i].priority != rtkFilterList[j].priority {
				return rtkFilterList[i].priority > rtkFilterList[j].priority
			}
			return rtkFilterList[i].id < rtkFilterList[j].id
		})
	})
	return rtkFilterList
}

// rtkFilterSource is a custom pack file and its upstream precedence. Explicit
// sources (project, global) outrank the bundled packs; project outranks global.
type rtkFilterSource struct {
	path       string
	sourceRank int
	formatRank int
}

// customRTKFilterSources mirrors upstream collectFilterSources: project packs
// under .rtk/ (only when trusted), then global packs under the data dir. TOML
// packs have no loader here; their JSON counterpart is read at the same paths.
func customRTKFilterSources() []rtkFilterSource {
	var out []rtkFilterSource
	if wd, err := os.Getwd(); err == nil {
		path := filepath.Join(wd, ".rtk", "filters.json")
		if data, err := os.ReadFile(path); err == nil {
			if trustedProjectFilters(path, data) {
				out = append(out, rtkFilterSource{path: path, sourceRank: 3, formatRank: 1})
			} else {
				log.Warnf("payloadcompress: rtk: project filters not trusted, skipping %s", path)
			}
		}
	}
	dataDir := os.Getenv("DATA_DIR")
	if dataDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			dataDir = filepath.Join(home, ".omniroute")
		}
	}
	if dataDir != "" {
		path := filepath.Join(dataDir, "rtk", "filters.json")
		if _, err := os.Stat(path); err == nil {
			out = append(out, rtkFilterSource{path: path, sourceRank: 2, formatRank: 1})
		}
	}
	return out
}

// trustedProjectFilters matches upstream: an explicit opt-in trusts project
// packs; otherwise trust.json must pin the file's SHA-256.
func trustedProjectFilters(path string, data []byte) bool {
	if os.Getenv("OMNIROUTE_RTK_TRUST_PROJECT_FILTERS") == "1" {
		return true
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(path), "trust.json"))
	if err != nil {
		return false
	}
	var trust map[string]any
	if err := json.Unmarshal(raw, &trust); err != nil {
		return false
	}
	sum := fmt.Sprintf("%x", sha256.Sum256(data))
	for _, key := range []string{"filtersSha256", "trustedFiltersSha256"} {
		if value, ok := trust[key].(string); ok && value == sum {
			return true
		}
	}
	return false
}

// loadCustomRTKFilters loads and validates explicit pack files. A file may hold
// one pack or an array; packs without an id are rejected like upstream's schema.
func loadCustomRTKFilters() []*rtkFilter {
	var out []*rtkFilter
	for _, source := range customRTKFilterSources() {
		data, err := os.ReadFile(source.path)
		if err != nil {
			log.Warnf("payloadcompress: rtk: read %s: %v", source.path, err)
			continue
		}
		packs, err := parseRTKFilterPacks(data)
		if err != nil {
			log.Warnf("payloadcompress: rtk: invalid custom filter %s: %v", source.path, err)
			continue
		}
		for _, pack := range packs {
			if strings.TrimSpace(pack.ID) == "" {
				log.Warnf("payloadcompress: rtk: custom filter in %s has no id, skipping", source.path)
				continue
			}
			filter := compileRTKFilter(pack)
			filter.sourceRank = source.sourceRank
			filter.formatRank = source.formatRank
			out = append(out, filter)
		}
	}
	return out
}

func parseRTKFilterPacks(data []byte) ([]rtkFilterPack, error) {
	var packs []rtkFilterPack
	if err := json.Unmarshal(data, &packs); err == nil {
		return packs, nil
	}
	var one rtkFilterPack
	if err := json.Unmarshal(data, &one); err != nil {
		return nil, err
	}
	return []rtkFilterPack{one}, nil
}

// rtkGenericErrorMarkers matches the three lines generic-output keys on. A tool
// result carrying none of them and no recognizable command is a file read, not
// command output, and dropping its middle would hide the file.
var rtkGenericErrorMarkers = regexp.MustCompile(`Error:|Exception:|Traceback \(most recent call last\):`)

// detectRTKOutputType ports commandDetector: a command that names a known tool
// settles the type outright, because it is the one signal the payload states
// rather than implies. Without one it falls back to content: the type of the
// best matching detector, most matched patterns first, table order on a tie.
func detectRTKOutputType(text, command string) string {
	loadRTKFilters()
	if typ := detectRTKTypeFromCommand(command); typ != "" {
		return typ
	}
	best, bestHits := "unknown", 0
	for _, detector := range rtkDetectors {
		hits := 0
		for _, re := range rtkFilterKinds[detector.typ] {
			if re.match(text) {
				hits++
			}
		}
		if hits > bestHits {
			best, bestHits = detector.typ, hits
		}
	}
	return best
}

// detectRTKTypeFromCommand returns the type whose command patterns match the
// command line. Nothing matches when the command is unknown, which is the
// common case: most providers do not echo the shell command back.
func detectRTKTypeFromCommand(command string) string {
	if command == "" {
		return ""
	}
	for _, detector := range rtkDetectors {
		// A detector's own command patterns win over its pack's broader
		// match.commands. Upstream does the same: the shell-ls pack claims
		// "find", but its detector only claims "ls", so find stays shell-find.
		patterns := rtkDetectorCommands[detector.typ]
		if len(patterns) == 0 {
			patterns = rtkFilterCommands[detector.typ]
		}
		for _, re := range patterns {
			if re.match(command) {
				return detector.typ
			}
		}
	}
	return ""
}

func (f *rtkFilter) hasType(typ string) bool {
	for _, candidate := range f.types {
		if candidate == typ {
			return true
		}
	}
	return false
}

func (f *rtkFilter) matchesText(text string) bool {
	for _, re := range f.match {
		if re.match(text) {
			return true
		}
	}
	return false
}

// selectRTKFilter walks upstream's cascade: the detected output type, then the
// pack's own content patterns, then the generic fallback. A known command
// decides the type before any text is read, which is the only way a build log
// with no distinctive markers ever reaches its filter.
func selectRTKFilter(text, command string) *rtkFilter {
	typ := detectRTKOutputType(text, command)
	// Upstream's isDocumentLikeRead gate: only an untyped output with no command
	// is a plain file read; with a command the output is still machine-produced.
	if typ == "unknown" && command == "" && !rtkGenericErrorMarkers.MatchString(text) {
		// A plain file or document read: no filter, or the truncating fallback
		// would cut a source file in half.
		return nil
	}
	filters := loadRTKFilters()
	// Upstream's cascade is per source: project beats global beats builtin, and a
	// project pattern must win over a builtin type.
	for _, rank := range []int{3, 2, 1} {
		var scoped []*rtkFilter
		for _, f := range filters {
			if f.sourceRank == rank {
				scoped = append(scoped, f)
			}
		}
		if typ != "unknown" {
			for _, f := range scoped {
				if f.hasType(typ) {
					return f
				}
			}
		}
		for _, f := range scoped {
			if f.matchesCommand(command) {
				return f
			}
		}
		for _, f := range scoped {
			if f.matchesText(text) {
				return f
			}
		}
	}
	for _, f := range filters {
		if f.hasType("generic-output") {
			return f
		}
	}
	return nil
}

func (f *rtkFilter) matchesCommand(command string) bool {
	if command == "" {
		return false
	}
	for _, re := range f.commands {
		if re.match(command) {
			return true
		}
	}
	return false
}

// rtkAnsiEscape is the SGR/CSI sequence stripper ported from upstream.
var rtkAnsiEscape = regexp.MustCompile("\u001b\\[[0-?]*[ -/]*[@-~]")

// truncateRTKLine cuts a line to n characters, ellipsis included, without
// splitting a rune.
func truncateRTKLine(line string, n int) string {
	if n <= 0 || utf8.RuneCountInString(line) <= n {
		return line
	}
	runes := []rune(line)
	if n <= 3 {
		return string(runes[:n])
	}
	return string(runes[:n-3]) + "..."
}

// applyRTKFilter runs one pack over the output and reports whether the text
// changed. The stage order is upstream's lineFilter: strip ansi, replace, emit,
// drop, keep, collapse, cut long lines, deduplicate, then head/tail truncation.
func (f *rtkFilter) apply(text string) (string, bool) {
	return f.applyWithIntensity(text, "")
}

func (f *rtkFilter) applyWithIntensity(text, intensity string) (string, bool) {
	lines := strings.Split(text, "\n")

	if f.stripAnsi {
		for i, line := range lines {
			lines[i] = rtkAnsiEscape.ReplaceAllString(line, "")
		}
	}
	for _, rule := range f.replace {
		for i, line := range lines {
			lines[i] = rule.re.replaceAllString(line, rule.repl)
		}
	}
	for _, rule := range f.emit {
		blob := strings.Join(lines, "\n")
		if !rule.re.match(blob) || (rule.unless != nil && rule.unless.match(blob)) {
			continue
		}
		return rule.message, rule.message != text
	}

	if len(f.drop) > 0 {
		kept := lines[:0]
		for _, line := range lines {
			if !rtkAnyMatch(f.drop, line) {
				kept = append(kept, line)
			}
		}
		lines = kept
	}
	if len(f.keep) > 0 {
		kept := make([]string, 0, len(lines))
		for _, line := range lines {
			// A diagnostic line survives the whitelist: dropping "FATAL" because it
			// is not in a filter's include list is the failure this layer exists for.
			if rtkAnyMatch(f.keep, line) || rtkSeverity.MatchString(line) {
				kept = append(kept, line)
			}
		}
		if len(kept) > 0 {
			lines = kept
		}
	}
	if len(f.collapse) > 0 {
		seen := make(map[string]struct{}, len(lines))
		kept := make([]string, 0, len(lines))
		for _, line := range lines {
			if rtkAnyMatch(f.collapse, line) {
				key := strings.TrimSpace(line)
				if _, ok := seen[key]; ok {
					continue
				}
				seen[key] = struct{}{}
			}
			kept = append(kept, line)
		}
		lines = kept
	}
	if f.truncateLineAt > 0 {
		for i, line := range lines {
			lines[i] = truncateRTKLine(line, f.truncateLineAt)
		}
	}
	if f.deduplicate {
		lines, _ = rtkCollapseRepeats(lines)
	}

	// A filter's own line budget scales with intensity, exactly like the global
	// one; the char cap does not. Severity lines are rescued first, then the
	// pack's preserve patterns.
	priority := make([]*packRegex, 0, len(f.rescue)+len(rtkDefaultPriority))
	priority = append(priority, rtkDefaultPriority...)
	priority = append(priority, f.rescue...)
	if capped, ok := rtkTruncate(lines, rtkLimits{
		maxLines:     scaleRTKMaxLines(f.maxLines, intensity),
		preserveHead: f.headLines,
		preserveTail: f.tailLines,
	}, priority); ok {
		lines = capped
	}

	out := strings.Join(lines, "\n")
	if strings.TrimSpace(out) == "" && f.onEmpty != "" {
		out = f.onEmpty
	}
	return out, out != text
}

func rtkAnyMatch(patterns []*packRegex, line string) bool {
	for _, re := range patterns {
		if re.match(line) {
			return true
		}
	}
	return false
}

// rtkCollapseRepeats ports upstream deduplicateRepeatedLines: a run of three or
// more identical lines collapses to the line plus a count, so the reader sees how
// much went away. Runs of two are left alone.
func rtkCollapseRepeats(lines []string) ([]string, bool) {
	out := make([]string, 0, len(lines))
	collapsed := false
	for i := 0; i < len(lines); i++ {
		run := 1
		for i+run < len(lines) && lines[i+run] == lines[i] {
			run++
		}
		if run >= 3 && strings.TrimSpace(lines[i]) != "" {
			dropped := itoaDecimal(run - 1)
			out = append(out, lines[i], "[line repeated "+dropped+"x]", "[rtk:dropped "+dropped+" repeated lines]")
			collapsed = true
			i += run - 1
			continue
		}
		out = append(out, lines[i])
	}
	return out, collapsed
}

// applyRTKFilters is the engine entry point: select a pack by the command that
// produced the output, then by content, and apply it, or return the text
// untouched.
func applyRTKFilters(text, command string, intensity ...string) (string, bool, []*packRegex) {
	filter := selectRTKFilter(text, command)
	if filter == nil {
		return text, false, nil
	}
	level := ""
	if len(intensity) > 0 {
		level = intensity[0]
	}
	out, ok := filter.applyWithIntensity(text, level)
	return out, ok, filter.rescue
}

// scaleRTKMaxLines is upstream effectiveMaxLines: only the line budget scales,
// and an unset intensity means the upstream default (minimal).
func scaleRTKMaxLines(base int, intensity string) int {
	if base <= 0 {
		return 0
	}
	factor := 1.5
	switch intensity {
	case "aggressive":
		factor = 0.5
	case "standard":
		factor = 1.0
	}
	scaled := int(math.Round(float64(base) * factor))
	if scaled < 1 {
		scaled = 1
	}
	return scaled
}

// rtkDetector is the content half of upstream's command detector.
type rtkDetector struct {
	typ      string
	commands []string
	patterns []string
}

var rtkDetectors = []rtkDetector{
	{typ: "git-status", patterns: []string{`(?m)^On branch `, `(?m)^Changes (?:not staged|to be committed)`, `(?m)^Untracked files:`}},
	{typ: "git-branch", patterns: []string{`(?m)^\*\s+\S+`, `(?i)Switched to (?:a new )?branch`, `(?i)Already on ['"][^'"]+['"]`}},
	{typ: "git-diff", patterns: []string{`(?m)^diff --git `, `(?m)^@@\s+-\d+,\d+\s+\+\d+,\d+\s+@@`}},
	{typ: "git-log", patterns: []string{`(?m)^commit [0-9a-f]{7,40}`, `(?m)^Author: `}},
	{typ: "make", patterns: []string{`(?m)^make\[\d+\]: (?:Entering|Leaving) directory`, `make: \*\*\* `}},
	{typ: "gradle", patterns: []string{`(?m)^> Task :`, `(?m)^BUILD (?:SUCCESSFUL|FAILED)\b`}},
	{typ: "dotnet", patterns: []string{`(?m)^Build (?:succeeded|FAILED)\b`, `(?m)\b(?:error|warning) CS\d+\b`}},
	{typ: "terraform-plan", patterns: []string{`Terraform will perform the following actions:`, `(?i)Plan: \d+ to add`}},
	{typ: "tofu-plan", patterns: []string{`OpenTofu will perform the following actions:`, `(?i)Plan: \d+ to add`}},
	{typ: "systemctl-status", patterns: []string{`(?m)^\s*Loaded:\s+`, `(?m)^\s*Active:\s+`, `(?m)^●\s+\S+\.service`}},
	{typ: "test-vitest", patterns: []string{`(?i)\bvitest\b`, `(?m)^ ✓ `, `(?m)^ ❯ `, `(?i)Test Files\s+\d+\s+(?:passed|failed)`}},
	{typ: "test-phpunit", patterns: []string{`(?m)^PHPUnit \d+\.\d+`, `(?m)^Tests: \d+, Assertions: \d+`, `(?m)^OK \(\d+ tests?, \d+ assertions?\)`, `(?m)^(?:FAILURES|ERRORS)!$`}},
	{typ: "test-jest", patterns: []string{`(?i)Test Suites:\s+\d+`, `(?i)Tests:\s+\d+`, `(?m)^PASS\s+`, `(?m)^FAIL\s+`}},
	{typ: "test-pytest", patterns: []string{`(?i)=+\s+(?:\d+\s+)?(?:passed|failed|errors?)`, `(?m)^E\s+`, `(?m)^FAILED `}},
	{typ: "test-cargo", patterns: []string{`(?m)^running \d+ tests?`, `(?m)^test\s+[\w:.-]+\s+\.\.\.\s+(?:ok|FAILED|ignored)`, `(?i)test result:\s+(?:ok|FAILED)`}},
	{typ: "test-go", patterns: []string{`(?m)^(?:ok|FAIL)\s+[\w./-]+\s+[\d.]+s`, `(?m)^--- FAIL: `, `(?m)^panic: `}},
	{typ: "build-typescript", patterns: []string{`TS\d{4}:`, `(?i)error TS\d{4}`}},
	{typ: "build-eslint", patterns: []string{`\s+\d+:\d+\s+(?:error|warning)\s+`, `✖\s+\d+\s+problems?`}},
	{typ: "build-webpack", patterns: []string{`(?i)webpack\s+\d`, `(?i)compiled (?:successfully|with \d+ errors?)`, `(?i)asset .+\.js`}},
	{typ: "build-vite", patterns: []string{`(?i)vite v[\d.]+`, `(?i)✓ built in`, `(?i)transforming \(\d+\)`}},
	{typ: "biome", patterns: []string{`lint\/[A-Za-z0-9/.-]+`, `(?i)Checked \d+ files? in`}},
	{typ: "prettier", patterns: []string{`(?m)^Checking formatting\.\.\.`, `(?i)Code style issues found`}},
	{typ: "turbo", patterns: []string{`(?m)^• Packages in scope:`, `(?m)^Tasks:\s+\d+\s+successful`}},
	{typ: "nx", patterns: []string{`(?m)^NX\s+`, `(?m)^> nx run `}},
	{typ: "playwright", patterns: []string{`(?i)Running \d+ tests? using \d+ workers?`, `(?m)^\s+\d+ failed`}},
	{typ: "npm-install", patterns: []string{`(?i)added \d+ packages`, `(?i)packages are looking for funding`, `(?i)audited \d+ packages`}},
	{typ: "npm-audit", patterns: []string{`(?i)found \d+ vulnerabilities`, `(?i)\b(?:low|moderate|high|critical)\b`}},
	{typ: "ruff", patterns: []string{`(?m)^[\w./-]+\.py:\d+:\d+:\s+[A-Z]\d+`, `(?i)Found \d+ errors?\.`}},
	{typ: "mypy", patterns: []string{`(?m)^[\w./-]+\.py:\d+:\s+error:`, `(?i)Found \d+ errors? in \d+ files?`}},
	{typ: "pip", patterns: []string{`(?m)^Collecting `, `(?m)^Successfully installed `}},
	{typ: "uv-sync", patterns: []string{`(?m)^Resolved \d+ packages?`, `(?m)^Installed \d+ packages?`}},
	{typ: "poetry-install", patterns: []string{`(?m)^Installing dependencies from lock file`, `(?m)^Package operations:`}},
	{typ: "golangci-lint", patterns: []string{`(?m)^[\w./-]+\.go:\d+:\d+:`, `(?m)^\d+ issues?:`}},
	{typ: "bundle-install", patterns: []string{`(?m)^Fetching gem metadata from `, `(?m)^Bundle complete!`}},
	{typ: "rubocop", patterns: []string{`(?m)^Inspecting \d+ files`, `(?m)^[\w./-]+\.rb:\d+:\d+:\s+[A-Z]:`}},
	{typ: "docker-ps", patterns: []string{`(?m)^CONTAINER ID\s+IMAGE\s+COMMAND`}},
	{typ: "docker-logs", patterns: []string{`(?m)^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}`, `\b(?:ERROR|WARN|INFO)\b`, `(?m)^Attaching to `}},
	{typ: "aws", patterns: []string{`An error occurred \([A-Za-z0-9]+\) when calling`, `(?m)^(?:upload|download): `}},
	{typ: "gcloud", patterns: []string{`(?m)^ERROR: \(gcloud\.`, `(?m)^Updated property \[`}},
	{typ: "ssh", patterns: []string{`Permission denied \(`, `Host key verification failed`, `Connection timed out`}},
	{typ: "rsync", patterns: []string{`(?m)^sending incremental file list`, `(?m)^rsync error:`}},
	{typ: "curl", patterns: []string{`curl: \(\d+\)`, `(?m)^HTTP\/\d(?:\.\d)? \d{3}`}},
	{typ: "wget", patterns: []string{`(?m)^--\d{4}-\d{2}-\d{2}`, `(?m)^ERROR \d{3}:`}},
	{typ: "json-output", patterns: []string{`^\s*[\[{][\s\S]*[\]}]\s*$`}},
	{typ: "shell-ls", commands: []string{`^ls(?:\s+-[A-Za-z]+)?\b`}, patterns: []string{`(?m)^total \d+`, `(?m)^\S+\s+\S+\s+\d+\s+\w+\s+\d{1,2}\s+`}},
	{typ: "shell-find", patterns: []string{`(?m)^(?:\.{1,2}|\/|[\w.-]+\/).+`}},
	{typ: "shell-grep", patterns: []string{`(?m)^[\w./-]+\.(?:ts|tsx|js|jsx|py|go|rs|java|rb|md|json|ya?ml|txt):\d*:`, `(?m)^[\w./-]+\/[\w./-]+:\d*:`}},
	{typ: "shell-ps", patterns: []string{`(?m)^(?:USER\s+PID|\s*PID\s+)`}},
	{typ: "shell-df", patterns: []string{`(?m)^Filesystem\s+.*Use%`}},
	{typ: "shell-du", patterns: []string{`(?m)^\d+(?:\.\d+)?[KMGTP]?\s+\S+`}},
	{typ: "error-stacktrace", patterns: []string{`Traceback \(most recent call last\):`, `(?m)^\s+at\s+\S+\s+\(.+:\d+:\d+\)`, `(?m)^panic: `, `(?m)^thread '[^']+' panicked at`}},
	{typ: "generic-error", patterns: []string{`Error:`, `Exception:`, `Traceback \(most recent call last\):`}},
}
