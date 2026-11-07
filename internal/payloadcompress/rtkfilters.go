package payloadcompress

import (
	"embed"
	"encoding/json"
	"io/fs"
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
	id       string
	priority int
	types    []string
	commands []*packRegex
	match    []*packRegex
	keep     []*packRegex
	drop     []*packRegex
	collapse []*packRegex
	rescue   []*packRegex
	replace  []rtkCompiledReplace
	emit     []rtkCompiledEmit
	tests    []rtkFilterTest

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
		rtkFilterCommands = make(map[string][]*packRegex, len(rtkFilterList))
		for _, filter := range rtkFilterList {
			for _, typ := range filter.types {
				if len(filter.commands) == 0 {
					continue
				}
				rtkFilterCommands[typ] = append(rtkFilterCommands[typ], filter.commands...)
			}
		}
		sort.SliceStable(rtkFilterList, func(i, j int) bool {
			if rtkFilterList[i].priority != rtkFilterList[j].priority {
				return rtkFilterList[i].priority > rtkFilterList[j].priority
			}
			return rtkFilterList[i].id < rtkFilterList[j].id
		})
	})
	return rtkFilterList
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
		for _, re := range rtkFilterCommands[detector.typ] {
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
	if typ == "unknown" && !rtkGenericErrorMarkers.MatchString(text) {
		// A plain file or document read: no filter, or the truncating fallback
		// would cut a source file in half.
		return nil
	}
	filters := loadRTKFilters()
	for _, f := range filters {
		if f.hasType(typ) {
			return f
		}
	}
	for _, f := range filters {
		if f.matchesText(text) {
			return f
		}
	}
	for _, f := range filters {
		if f.hasType("generic-output") {
			return f
		}
	}
	return nil
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

	if capped, ok := rtkTruncate(lines, rtkLimits{
		maxLines:     f.maxLines,
		preserveHead: f.headLines,
		preserveTail: f.tailLines,
	}, f.rescue); ok {
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
func applyRTKFilters(text, command string) (string, bool) {
	filter := selectRTKFilter(text, command)
	if filter == nil {
		return text, false
	}
	return filter.apply(text)
}

// rtkDetector is the content half of upstream's command detector.
type rtkDetector struct {
	typ      string
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
	{typ: "shell-ls", patterns: []string{`(?m)^total \d+`, `(?m)^\S+\s+\S+\s+\d+\s+\w+\s+\d{1,2}\s+`}},
	{typ: "shell-find", patterns: []string{`(?m)^(?:\.{1,2}|\/|[\w.-]+\/).+`}},
	{typ: "shell-grep", patterns: []string{`(?m)^[\w./-]+\.(?:ts|tsx|js|jsx|py|go|rs|java|rb|md|json|ya?ml|txt):\d*:`, `(?m)^[\w./-]+\/[\w./-]+:\d*:`}},
	{typ: "shell-ps", patterns: []string{`(?m)^(?:USER\s+PID|\s*PID\s+)`}},
	{typ: "shell-df", patterns: []string{`(?m)^Filesystem\s+.*Use%`}},
	{typ: "shell-du", patterns: []string{`(?m)^\d+(?:\.\d+)?[KMGTP]?\s+\S+`}},
	{typ: "error-stacktrace", patterns: []string{`Traceback \(most recent call last\):`, `(?m)^\s+at\s+\S+\s+\(.+:\d+:\d+\)`, `(?m)^panic: `, `(?m)^thread '[^']+' panicked at`}},
	{typ: "generic-error", patterns: []string{`Error:`, `Exception:`, `Traceback \(most recent call last\):`}},
}
