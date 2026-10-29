package payloadcompress

import (
	"math"
	"strconv"
	"strings"
	"unicode"
)

// liteEngine removes formatting noise that carries no meaning for a model:
// ANSI escapes, carriage returns, trailing spaces and runs of blank lines.
// Fenced code blocks are copied through byte for byte.
type liteEngine struct {
	opts StepOptions
}

func (liteEngine) name() string { return EngineLite }

// liteDefaultMaxToolLength is the upstream DEFAULT_MAX_TOOL_LENGTH.
const liteDefaultMaxToolLength = 2000

// compressKind adds the tool-result cap that lite owns upstream, then falls back
// to prose cleanup.
func (e liteEngine) compressKind(kind segmentKind, text string) (string, bool) {
	limit := e.opts.MaxToolLength
	if limit <= 0 {
		limit = liteDefaultMaxToolLength
	}
	if kind == kindToolOutput && e.keepToolResults() && len(text) > limit && charLen(text) > limit {
		return truncateToolText(text, limit), true
	}
	return e.compress(text)
}

func (e liteEngine) keepToolResults() bool {
	return e.opts.CompressToolResults == nil || *e.opts.CompressToolResults
}

func (liteEngine) compress(text string) (string, bool) {
	if len(text) < 128 || !liteNeedsWork(text) {
		return text, false
	}
	var b strings.Builder
	b.Grow(len(text))
	changed := false
	first := true
	write := func(line string) {
		if !first {
			b.WriteByte('\n')
		}
		first = false
		b.WriteString(line)
	}
	inFence := false
	blank := 0
	rest := text
	for {
		var line string
		more := false
		if idx := strings.IndexByte(rest, '\n'); idx >= 0 {
			line, rest, more = rest[:idx], rest[idx+1:], true
		} else {
			line = rest
		}

		switch {
		case hasFence(line):
			inFence = !inFence
			blank = 0
			write(line)
		case inFence:
			blank = 0
			write(line)
		default:
			out, _ := cleanLine(line)
			if out != line {
				changed = true
			}
			if strings.TrimSpace(out) == "" {
				blank++
				// A run of blank lines collapses to a single one.
				if blank > 1 {
					changed = true
				} else {
					write("")
				}
			} else {
				blank = 0
				write(out)
			}
		}

		if !more {
			break
		}
	}
	if !changed {
		return text, false
	}
	return b.String(), true
}

// liteNeedsWork reports whether any rule could fire. It is a single byte scan,
// so a segment that is already clean costs no allocation.
func liteNeedsWork(text string) bool {
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case 0x1b, '\r':
			return true
		case '\n':
			if i > 0 && (text[i-1] == ' ' || text[i-1] == '\t') {
				return true
			}
			if i+1 < len(text) && text[i+1] == '\n' {
				return true
			}
		case ' ', '\t':
			if i+1 == len(text) {
				return true
			}
		}
	}
	return false
}

// cleanLine strips ANSI control sequences, carriage returns and trailing spaces
// from one line. It returns the input untouched when nothing matched.
func cleanLine(s string) (string, bool) {
	if strings.IndexByte(s, 0x1b) < 0 && strings.IndexByte(s, '\r') < 0 {
		if trimmed := trimTrailingSpace(s); trimmed != s {
			return trimmed, true
		}
		return s, false
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == 0x1b {
			if i+1 < len(s) && s[i+1] == '[' {
				j := i + 2
				for j < len(s) && s[j] >= 0x20 && s[j] <= 0x3f {
					j++
				}
				if j < len(s) && s[j] >= 0x40 && s[j] <= 0x7e {
					i = j
					continue
				}
			}
			if i+1 < len(s) && s[i+1] == ']' {
				j := i + 2
				for j < len(s) && s[j] != 0x07 {
					j++
				}
				if j < len(s) {
					i = j
					continue
				}
			}
			continue
		}
		if c == '\r' {
			continue
		}
		b.WriteByte(c)
	}
	out := trimTrailingSpace(b.String())
	if out == s {
		return s, false
	}
	return out, true
}

// trimTrailingSpace drops trailing spaces and tabs without allocating.
func trimTrailingSpace(s string) string {
	for len(s) > 0 {
		c := s[len(s)-1]
		if c != ' ' && c != '\t' {
			break
		}
		s = s[:len(s)-1]
	}
	return s
}

// The tool-result cap mirrors upstream lite.ts: a hard cut is adjusted to the
// nearest whitespace inside a lookback/lookahead window, so a truncated result
// never garbles the word a cut lands in.
const toolTruncationLookback = 80

// toolTruncationMarker ends every capped single-line result.
const toolTruncationMarker = "\n...[truncated]"

// toolElisionReserve is the room the middle marker needs, whatever its line count.
const toolElisionReserve = 32

// toolTailShare is the fraction of the budget the tail keeps.
const toolTailShare = 0.3

// toolMinLinesForTail is the line count at or below which only the head is cut:
// a short result has no tail worth defending.
const toolMinLinesForTail = 10

func isWordRuneAt(runes []rune, i int) bool {
	return i >= 0 && i < len(runes) && !unicode.IsSpace(runes[i])
}

func findWhitespaceBackward(runes []rune, cutIndex int) int {
	windowStart := max(0, cutIndex-toolTruncationLookback)
	for i := cutIndex; i > windowStart; i-- {
		if !isWordRuneAt(runes, i-1) {
			return i - 1
		}
	}
	return -1
}

func findWhitespaceForward(runes []rune, cutIndex int) int {
	windowEnd := min(len(runes), cutIndex+toolTruncationLookback)
	for i := cutIndex; i < windowEnd; i++ {
		if !isWordRuneAt(runes, i) {
			return i
		}
	}
	return -1
}

// backOffToWordBoundary prefers the end of the previous word, which keeps the
// result at or under the cap. If nothing precedes the cut, it completes the
// current word instead; with no boundary either way the hard cut stands.
func backOffToWordBoundary(runes []rune, cutIndex int) int {
	if !isWordRuneAt(runes, cutIndex-1) || !isWordRuneAt(runes, cutIndex) {
		return cutIndex
	}
	if backward := findWhitespaceBackward(runes, cutIndex); backward != -1 {
		return backward
	}
	if forward := findWhitespaceForward(runes, cutIndex); forward != -1 {
		return forward
	}
	return cutIndex
}

// tailStartFrom is the start of the tail kept after the elision marker: the first
// line that begins inside the last tailBudget characters, so the tail never exceeds
// its budget. -1 when no complete line fits, which falls back to the head-only cut.
func tailStartFrom(runes []rune, tailBudget int) int {
	if tailBudget <= 0 {
		return -1
	}
	for i := max(0, len(runes)-tailBudget); i < len(runes); i++ {
		if runes[i] == '\n' && i+1 < len(runes) {
			return i + 1
		}
	}
	return -1
}

func countLines(text string) int { return strings.Count(text, "\n") + 1 }

// hasMoreLinesThan reports whether text holds more than lineCount lines, stopping
// as soon as the answer is known so a huge result is never scanned twice.
func hasMoreLinesThan(text string, lineCount int) bool {
	index := 0
	for found := 0; found < lineCount; found++ {
		next := strings.IndexByte(text[index:], '\n')
		if next < 0 {
			return false
		}
		index += next + 1
	}
	return true
}

// truncateToolText caps a tool result the way upstream Lite does. Multi-line
// content keeps its tail as well as its head: a head-only cut deletes the lines
// an agent is about to edit, so it invents them from memory and the edit fails as
// a stale anchor. Single-line content takes the word-boundary cut.
func truncateToolText(text string, maxLength int) string {
	runes := []rune(text)
	budget := maxLength - charLen(toolTruncationMarker) - toolElisionReserve
	if budget > 0 && hasMoreLinesThan(text, toolMinLinesForTail) {
		headCut := backOffToWordBoundary(runes, int(math.Ceil(float64(budget)*(1-toolTailShare))))
		tailStart := tailStartFrom(runes, budget-headCut)
		if tailStart > headCut {
			elided := countLines(string(runes[headCut:tailStart]))
			return string(runes[:headCut]) + "\n... [" + strconv.Itoa(elided) + " lines elided] ...\n" +
				string(runes[tailStart:]) + toolTruncationMarker
		}
	}
	return string(runes[:min(backOffToWordBoundary(runes, maxLength), len(runes))]) + toolTruncationMarker
}
