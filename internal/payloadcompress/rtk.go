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
	rtkHeadBytes    = 256
	rtkTailBytes    = 128
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
			if runes := []rune(raw); len(runes) > rtkHeadBytes+rtkTailBytes {
				line = string(runes[:rtkHeadBytes]) + " …(" +
					strconv.Itoa(len(runes)-rtkHeadBytes-rtkTailBytes) + " chars omitted)… " +
					string(runes[len(runes)-rtkTailBytes:])
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

	res := strings.Join(out, "\n")
	if trailing {
		res += "\n"
	}
	if len(res) > limits.maxChars && charLen(res) > limits.maxChars {
		res = capMiddleChars(res, limits.maxChars)
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

func capMiddleChars(text string, max int) string {
	if max < 8 || charLen(text) <= max {
		return text
	}
	runes := []rune(text)
	head := max * 2 / 3
	tail := max - head
	omitted := strconv.Itoa(len(runes) - head - tail)
	return string(runes[:head]) + "…(" + omitted + " chars omitted)…" + string(runes[len(runes)-tail:])
}
