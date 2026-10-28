package payloadcompress

import (
	"strconv"
	"strings"
)

// rtkEngine compacts captured command output: collapse repeated lines, shorten
// pathological lines, and cap the total size. It only fires on shapes that look
// like machine output, and it keeps the first copy of anything it elides.
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
	minRun     int
	maxLines   int
	maxChars   int
	minSavings int
}

func (e rtkEngine) limits() rtkLimits {
	// Defaults are the upstream RTK defaults: minimal intensity, 120 lines, 12000
	// chars, dedup threshold 3, and no savings gate.
	limits := rtkLimits{minRun: 3, maxLines: 120, maxChars: 12000}
	switch e.opts.Intensity {
	case "standard":
		limits.maxLines, limits.maxChars = 200, 24000
	case "aggressive":
		limits.minRun, limits.maxLines, limits.maxChars = 2, 400, 48000
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
	limits := e.limits()

	lines := strings.Split(text, "\n")
	trailing := lines[len(lines)-1] == ""
	if trailing {
		lines = lines[:len(lines)-1]
	}

	changed := false
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

	if len(out) > limits.maxLines {
		out = capMiddle(out, limits.maxLines, "lines")
		changed = true
	}
	if !changed {
		return text, false
	}

	if capped, ok := capMiddleCharsByLine(out, limits.maxChars); ok {
		out = capped
	}

	res := strings.Join(out, "\n")
	if trailing {
		res += "\n"
	}
	// A rewrite that saves less than the threshold is not worth destabilizing the
	// prefix for.
	if len(res) >= len(text)-len(text)*limits.minSavings/100 {
		return text, false
	}
	return res, true
}

// capMiddle keeps the head and tail and drops the middle, so both the start and
// the final error message survive.
func capMiddle(lines []string, max int, unit string) []string {
	if max < 4 || len(lines) <= max {
		return lines
	}
	head := max / 2
	tail := max - head - 1
	out := make([]string, 0, max)
	out = append(out, lines[:head]...)
	out = append(out, "…("+strconv.Itoa(len(lines)-head-tail)+" "+unit+" omitted)…")
	out = append(out, lines[len(lines)-tail:]...)
	return out
}

// charElisionReserve is the room the elision marker needs inside a char budget:
// it covers "…(", the decimal count and " chars omitted)…".
const charElisionReserve = 32

// capMiddleCharsByLine keeps whole lines inside a character budget, dropping the
// middle so the first command and the final error both survive. It cuts on line
// boundaries: a mid-line cut leaves fragments that read like real data.
func capMiddleCharsByLine(lines []string, max int) ([]string, bool) {
	budget := max - charElisionReserve
	if max < 8 || budget <= 0 || len(lines) < 3 {
		return lines, false
	}
	total := 0
	for _, line := range lines {
		total += charLen(line) + 1
	}
	if total <= max {
		return lines, false
	}
	head, headChars := 0, 0
	for head < len(lines) && headChars+charLen(lines[head])+1 <= budget*2/3 {
		headChars += charLen(lines[head]) + 1
		head++
	}
	tail, tailChars := 0, 0
	for tail < len(lines)-head && tailChars+charLen(lines[len(lines)-1-tail])+1 <= budget-headChars {
		tailChars += charLen(lines[len(lines)-1-tail]) + 1
		tail++
	}
	if len(lines)-head-tail <= 0 {
		return lines, false
	}
	elided := charLen(strings.Join(lines[head:len(lines)-tail], "\n"))
	out := make([]string, 0, head+tail+1)
	out = append(out, lines[:head]...)
	out = append(out, "…("+strconv.Itoa(elided)+" chars omitted)…")
	out = append(out, lines[len(lines)-tail:]...)
	return out, true
}
