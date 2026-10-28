package payloadcompress

import (
	"strconv"
	"strings"
)

// rtkEngine compacts captured command output. It only fires on shapes that look
// like machine output (very long lines, repeated lines), and it keeps the first
// copy of anything it elides, so nothing is silently lost.
type rtkEngine struct{}

func (rtkEngine) name() string { return EngineRTK }

const (
	rtkMinLines     = 8
	rtkMinRun       = 3
	rtkMaxLineBytes = 512
	rtkHeadBytes    = 256
	rtkTailBytes    = 128
	rtkRepeatMark   = " (×"
)

func (rtkEngine) compress(text string) (string, bool) {
	if strings.Count(text, "\n") < rtkMinLines {
		return text, false
	}
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
			line = raw[:rtkHeadBytes] + " …(" +
				strconv.Itoa(len(raw)-rtkHeadBytes-rtkTailBytes) + " chars omitted)… " +
				raw[len(raw)-rtkTailBytes:]
			changed = true
		}
		run := 1
		if raw != "" && !strings.Contains(raw, rtkRepeatMark) {
			for i+run < len(lines) && lines[i+run] == raw {
				run++
			}
		}
		if run >= rtkMinRun {
			out = append(out, line+rtkRepeatMark+strconv.Itoa(run)+")")
			changed = true
		} else {
			for k := 0; k < run; k++ {
				out = append(out, line)
			}
		}
		i += run - 1
	}

	if !changed {
		return text, false
	}
	res := strings.Join(out, "\n")
	if trailing {
		res += "\n"
	}
	// A transform that saves less than 5% is not worth destabilizing the prefix.
	if len(res) >= len(text)-len(text)/20 {
		return text, false
	}
	return res, true
}
