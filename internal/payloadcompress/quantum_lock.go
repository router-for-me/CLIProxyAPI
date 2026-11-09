package payloadcompress

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// quantumPatterns mirrors upstream quantumLock/quantumPatterns.ts. Order is
// fixed: the widest, most specific token is claimed first so a shorter pattern
// never splits it. Variable-length runs stay bounded to avoid ReDoS.
//
// The upstream JWT rule ends with a negative lookahead, which RE2 cannot
// express. The greedy bounded tail already consumes the whole base64url run,
// so the lookahead is unnecessary here.
var quantumPatterns = []struct {
	category string
	re       *regexp.Regexp
}{
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,512}\.[A-Za-z0-9_-]{8,512}\.[A-Za-z0-9_-]{8,512}`)},
	{"api_key_shape", regexp.MustCompile(`\b(?:sk|pk|rk|ghp|gho|xox[baprs])[-_][A-Za-z0-9]{16,200}\b`)},
	{"api_key_shape", regexp.MustCompile(`\bBearer[ \t]{1,4}[A-Za-z0-9._-]{16,400}\b`)},
	{"uuid", regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)},
	{"request_id", regexp.MustCompile(`(?i)\b(?:req|trace|span|corr|request)[-_][A-Za-z0-9]{6,128}\b`)},
	{"long_hex", regexp.MustCompile(`(?i)\b[0-9a-f]{16,128}\b`)},
	{"unix_ts", regexp.MustCompile(`\b1[0-9]{9}(?:[0-9]{3})?\b`)},
}

type quantumSpan struct {
	start, end int
	category   string
	prio       int
}

// quantumTailDelim is the idempotency sentinel and value-tail header.
const quantumTailDelim = "⟦QUANTUMLOCK⟧"

// lockQuantumSpans mirrors upstream applyQuantumLock: volatile, non-semantic
// spans become positional ⟦Qi⟧ placeholders and the original values move to an
// in-band tail. The stable prefix is what the provider caches; values are never
// restored, since restoring them would reintroduce the volatile bytes.
//
// Process non-overlapping matches left to right. A text with no matches, or one
// that already carries the tail, passes through unchanged.
func lockQuantumSpans(text string) string {
	if text == "" || strings.Contains(text, quantumTailDelim) {
		return text
	}
	spans := detectQuantumSpans(text)
	if len(spans) == 0 {
		return text
	}

	values := make([]string, len(spans))
	var b strings.Builder
	b.Grow(len(text))
	last := 0
	for i, sp := range spans {
		b.WriteString(text[last:sp.start])
		values[i] = text[sp.start:sp.end]
		b.WriteString(quantumPlaceholder(i))
		last = sp.end
	}
	b.WriteString(text[last:])
	b.WriteString("\n\n")
	b.WriteString(quantumTailDelim)
	b.WriteByte('\n')
	for i, v := range values {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(quantumPlaceholder(i))
		b.WriteByte('=')
		b.WriteString(v)
	}
	return b.String()
}

func detectQuantumSpans(text string) []quantumSpan {
	var raw []quantumSpan
	for prio, p := range quantumPatterns {
		for _, m := range p.re.FindAllStringIndex(text, -1) {
			raw = append(raw, quantumSpan{start: m[0], end: m[1], category: p.category, prio: prio})
		}
	}
	if len(raw) == 0 {
		return nil
	}
	sort.Slice(raw, func(i, j int) bool {
		if raw[i].start != raw[j].start {
			return raw[i].start < raw[j].start
		}
		if raw[i].end != raw[j].end {
			return raw[i].end > raw[j].end
		}
		return raw[i].prio < raw[j].prio
	})

	merged := raw[:0]
	lastEnd := -1
	for _, s := range raw {
		if s.start < lastEnd {
			continue // narrower span overlapped a token already claimed.
		}
		merged = append(merged, s)
		lastEnd = s.end
	}
	return merged
}

func quantumPlaceholder(i int) string {
	return "⟦Q" + strconv.Itoa(i) + "⟧"
}
