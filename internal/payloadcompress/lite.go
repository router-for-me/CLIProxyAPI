package payloadcompress

import "strings"

// liteEngine removes formatting noise that carries no meaning for a model:
// ANSI escapes, carriage returns, trailing spaces and runs of blank lines.
// Fenced code blocks are copied through byte for byte.
type liteEngine struct{}

func (liteEngine) name() string { return EngineLite }

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
