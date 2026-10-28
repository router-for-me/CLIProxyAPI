package payloadcompress

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// rtkEngine compacts captured command output: select the filter for the output's
// shape, collapse repeated lines, shorten pathological lines, and cap the total
// size. It only fires on shapes that look like machine output, and it keeps the
// first copy of anything it elides.
type rtkEngine struct {
	opts StepOptions
}

func (rtkEngine) name() string { return EngineRTK }

const (
	rtkMinLines     = 8
	rtkMaxLineBytes = 512
	rtkHeadChars    = 256
	rtkTailChars    = 128
	rtkRepeatMark   = " (×"
)

// rtkLimits is the intensity ladder. Intensity picks how eagerly repeated lines
// collapse; the size caps are safety nets, not the main mechanism. Explicit
// options always win over the ladder.
type rtkLimits struct {
	minRun       int
	maxLines     int
	maxChars     int
	preserveHead int
	preserveTail int
	minSavings   int
}

func (e rtkEngine) limits() rtkLimits {
	// Defaults are the upstream RTK defaults: minimal intensity, 120 lines, 12000
	// chars, dedup threshold 3, and no savings gate.
	limits := rtkLimits{minRun: 3, maxLines: 120, maxChars: 12000, preserveHead: 24, preserveTail: 24}
	switch e.opts.Intensity {
	case "standard":
		limits.maxLines, limits.maxChars = 200, 24000
	case "aggressive":
		limits.minRun, limits.maxLines, limits.maxChars = 2, 400, 48000
		limits.preserveHead, limits.preserveTail = 16, 16
	}
	if e.opts.DeduplicateThreshold > 0 {
		limits.minRun = e.opts.DeduplicateThreshold
	}
	if limits.minRun < 2 {
		limits.minRun = 2
	}
	if e.opts.MaxLines > 0 {
		limits.maxLines = e.opts.MaxLines
	}
	if e.opts.MaxChars > 0 {
		limits.maxChars = e.opts.MaxChars
	}
	if e.opts.MinSavingsPercent > 0 {
		limits.minSavings = e.opts.MinSavingsPercent
	}
	return limits
}

func (e rtkEngine) compress(text string) (string, bool) {
	if strings.Count(text, "\n") < rtkMinLines {
		return text, false
	}
	original := text
	limits := e.limits()

	// Filters run before the caps below: a filter knows what the command that
	// produced this output looks like, which is what decides which lines are noise.
	changed := false
	if filtered, ok := applyRTKFilters(text); ok {
		text, changed = filtered, true
	}

	lines := strings.Split(text, "\n")
	trailing := lines[len(lines)-1] == ""
	if trailing {
		lines = lines[:len(lines)-1]
	}

	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		raw := lines[i]
		line := raw
		if len(line) > rtkMaxLineBytes {
			// Count and cut by character: the marker promises chars, and a byte cut
			// can split a multi-byte rune.
			if runes := []rune(raw); len(runes) > rtkHeadChars+rtkTailChars {
				line = string(runes[:rtkHeadChars]) + " …(" +
					strconv.Itoa(len(runes)-rtkHeadChars-rtkTailChars) + " chars omitted)… " +
					string(runes[len(runes)-rtkTailChars:])
				changed = true
			}
		}
		run := 1
		if raw != "" && !strings.Contains(raw, rtkRepeatMark) {
			for i+run < len(lines) && lines[i+run] == raw {
				run++
			}
		}
		if run >= limits.minRun {
			out = append(out, line+rtkRepeatMark+strconv.Itoa(run)+")")
			changed = true
		} else {
			for k := 0; k < run; k++ {
				out = append(out, line)
			}
		}
		i += run - 1
	}

	if capped, ok := rtkTruncate(out, limits, rtkDefaultPriority); ok {
		out = capped
		changed = true
	}
	if !changed {
		return original, false
	}

	res := strings.Join(out, "\n")
	if trailing {
		res += "\n"
	}
	// A rewrite that saves less than the threshold is not worth destabilizing the
	// prefix for.
	if len(res) >= len(original)-len(original)*limits.minSavings/100 {
		return original, false
	}
	return res, true
}

// rtkSeverity is the diagnostic vocabulary upstream refuses to drop when it elides
// the middle of a command's output (severityVocabulary.ts). Matching is
// case-insensitive and deliberately unbounded: paths like error.log and CJK log
// lines carry no ASCII word boundary.
var rtkSeverity = regexp.MustCompile(`(?i)` + strings.Join([]string{
	"error", "failed", "failure", "exception", "traceback", "fail",
	`ts\d{4}`, "✖", "panic", "fatal", "critical", "severe", "oomkilled",
	"out of memory", "denied", "timeout", "timed out", "unreachable",
	"refused", "aborted", "rollback", "corrupt", "deadlock",
}, "|"))

// rtkTruncatedByChars marks the second pass, which enforces the char budget after
// the line pass has already dropped the middle.
const rtkTruncatedByChars = "\n[rtk:truncated by chars]\n"

// rtkDefaultPriority is the vocabulary the engine-level cap rescues from the
// middle of a line elision. Filter packs pass their own preserve patterns.
var rtkDefaultPriority = []*regexp.Regexp{rtkSeverity}

func rtkTruncate(lines []string, limits rtkLimits, priority []*regexp.Regexp) ([]string, bool) {
	overLines := limits.maxLines > 0 && len(lines) > limits.maxLines
	overChars := limits.maxChars > 0 && charLen(strings.Join(lines, "\n")) > limits.maxChars
	if !overLines && !overChars {
		return lines, false
	}
	// A pattern matching most lines is not selective: it would fill the head with
	// the same text the plain head already keeps.
	patterns := make([]*regexp.Regexp, 0, len(priority))
	for _, re := range priority {
		hits := 0
		for _, line := range lines {
			if re.MatchString(line) {
				hits++
			}
		}
		if hits > 0 && hits*2 <= len(lines) {
			patterns = append(patterns, re)
		}
	}
	head := min(limits.preserveHead, len(lines))
	selected := make([]string, 0, len(lines))
	seen := make(map[string]struct{}, len(lines))
	for _, line := range lines[:head] {
		selected = append(selected, line)
		seen[line] = struct{}{}
	}
	add := func(line string) {
		if _, ok := seen[line]; ok {
			return
		}
		seen[line] = struct{}{}
		selected = append(selected, line)
	}
	for _, line := range lines {
		for _, re := range patterns {
			if re.MatchString(line) {
				add(line)
				break
			}
		}
	}
	tail := min(limits.preserveTail, len(lines))
	for i := len(lines) - tail; i < len(lines); i++ {
		if i >= head {
			add(lines[i])
		}
	}
	out := make([]string, 0, len(selected)+1)
	out = append(out, selected[:head]...)
	out = append(out, "[rtk:truncated "+strconv.Itoa(len(lines)-len(selected))+" lines]")
	out = append(out, selected[head:]...)
	if limits.maxChars > 0 && charLen(strings.Join(out, "\n")) > limits.maxChars {
		out = rtkTruncateByChars(out, patterns, limits.maxChars)
	}
	return out, true
}

// rtkTruncateByChars enforces the char budget after the line pass. Priority lines
// come from the middle, so their room is reserved before the budget is split; a
// blind cut would delete exactly the diagnostics the line pass preserved.
func rtkTruncateByChars(lines []string, patterns []*regexp.Regexp, maxChars int) []string {
	budget := maxChars - charLen(rtkTruncatedByChars)
	if budget <= 0 {
		return []string{"[rtk:truncated by chars]"}
	}
	type ranked struct {
		re   *regexp.Regexp
		hits int
	}
	order := make([]ranked, 0, len(patterns))
	for _, re := range patterns {
		hits := 0
		for _, line := range lines {
			if re.MatchString(line) {
				hits++
			}
		}
		if hits > 0 && hits*2 <= len(lines) {
			order = append(order, ranked{re: re, hits: hits})
		}
	}
	sort.Slice(order, func(i, j int) bool { return order[i].hits < order[j].hits })
	forcedCap := budget / 2
	forced := make([]string, 0, len(order))
	forcedChars := 0
	seen := make(map[string]struct{}, len(lines))
	for _, entry := range order {
		for _, line := range lines {
			if _, ok := seen[line]; ok {
				continue
			}
			if !entry.re.MatchString(line) {
				continue
			}
			cost := charLen(line) + 1
			if cost > forcedCap-forcedChars {
				continue
			}
			seen[line] = struct{}{}
			forced = append(forced, line)
			forcedChars += cost
		}
	}
	rest := max(0, budget-forcedChars)
	headChars := (rest*55 + 99) / 100 // ceil(rest * 0.55)
	tailChars := rest - headChars
	text := strings.Join(lines, "\n")
	var headText, tailText string
	if headChars > 0 {
		headRaw := headRunes(text, headChars)
		headText = headRaw
		if cut := strings.LastIndex(headRaw, "\n"); cut != -1 {
			headText = headRaw[:cut+1]
		}
	}
	if tailChars > 0 {
		tailRaw := tailRunes(text, tailChars)
		tailText = tailRaw
		if cut := strings.Index(tailRaw, "\n"); cut != -1 {
			tailText = tailRaw[cut+1:]
		}
	}
	kept := make([]string, 0, len(forced))
	for _, line := range forced {
		if strings.Contains(headText, line) || strings.Contains(tailText, line) {
			continue
		}
		kept = append(kept, line)
	}
	var b strings.Builder
	b.WriteString(headText)
	b.WriteString(rtkTruncatedByChars)
	if len(kept) > 0 {
		b.WriteString(strings.Join(kept, "\n"))
		b.WriteByte('\n')
	}
	b.WriteString(tailText)
	out := b.String()
	if charLen(out) > maxChars {
		// Last resort, matching upstream: a hard cut beats blowing the budget.
		out = headRunes(out, maxChars)
	}
	return strings.Split(out, "\n")
}

func headRunes(s string, n int) string {
	r := []rune(s)
	if n >= len(r) {
		return s
	}
	return string(r[:n])
}

func tailRunes(s string, n int) string {
	r := []rune(s)
	if n >= len(r) {
		return s
	}
	return string(r[len(r)-n:])
}
