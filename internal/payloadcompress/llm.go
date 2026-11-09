package payloadcompress

import (
	"regexp"
	"sort"
	"strings"
)

// llmEngine mirrors upstream's LLM-tier compressor: the prose of non-system
// messages may be condensed by a pluggable backend while code, URLs and other
// opaque spans are copied through untouched. The default backend is identity,
// so out of the box applyBody returns the body unchanged.
type llmEngine struct{}

func newLLMEngine() llmEngine { return llmEngine{} }

func (llmEngine) name() string { return "llm" }

// llmCompressorBackend is the injection seam. A nil backend is a no-op, which
// keeps the engine pass-through by default.
var llmCompressorBackend func(text string) string

// Opaque inline regions the backend must never see. Fenced blocks are handled
// separately by llmFencedSpans.
var llmPreservePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`),
	regexp.MustCompile(`(?s)<instructions?>.*?</instructions?>`),
	regexp.MustCompile(`https?://[^\s<>"']+`),
	regexp.MustCompile(`(?:^|\s)(?:\.{0,2}\/[A-Za-z0-9_@./-]+|[A-Za-z]:\\[A-Za-z0-9_.\\/-]+)`),
}

type llmSegment struct {
	preserved bool
	text      string
}

// applyBody rewrites chat messages in place at the payload level. It fails open
// and never panics: any unexpected shape returns the original body.
func (e llmEngine) applyBody(body []byte) (out []byte) {
	out = body
	if llmCompressorBackend == nil {
		return body
	}
	defer func() {
		if recover() != nil {
			out = body
		}
	}()

	doc, messages, ok := bodyMessageItems(body)
	if !ok {
		return body
	}
	raws := make([]string, len(messages))
	changed := false
	for i, msg := range messages {
		raw := msg.Raw
		if msg.Get("role").String() == "system" {
			raws[i] = raw
			continue
		}
		next, didChange := msgMapText(raw, func(text string, _ int) string {
			return llmCompressText(text)
		})
		if didChange {
			raws[i] = next
			changed = true
		} else {
			raws[i] = raw
		}
	}
	if !changed {
		return body
	}
	next, ok := setBodyMessages(doc, raws)
	if !ok {
		return body
	}
	return next
}

// llmCompressText condenses the prose segments and leaves preserved spans
// untouched. It returns the original text unless a backend actually shortened
// it.
func llmCompressText(text string) string {
	if strings.TrimSpace(text) == "" {
		return text
	}
	var b strings.Builder
	compressed := false
	for _, seg := range llmSplit(text) {
		if seg.preserved {
			b.WriteString(seg.text)
			continue
		}
		next, ok := llmRunBackend(seg.text)
		if ok && charLen(next) < charLen(seg.text) {
			b.WriteString(next)
			compressed = true
		} else {
			b.WriteString(seg.text)
		}
	}
	if !compressed {
		return text
	}
	return b.String()
}

func llmRunBackend(text string) (out string, ok bool) {
	if strings.TrimSpace(text) == "" {
		return text, false
	}
	defer func() {
		if recover() != nil {
			out, ok = text, false
		}
	}()
	out = llmCompressorBackend(text)
	if strings.TrimSpace(out) == "" || out == text {
		return text, false
	}
	return out, true
}

// llmSplit partitions text into prose and preserved spans. Fenced code blocks,
// HTML instruction regions, URLs and file paths stay byte-identical.
func llmSplit(text string) []llmSegment {
	spans := llmFencedSpans(text)
	for _, re := range llmPreservePatterns {
		for _, loc := range re.FindAllStringIndex(text, -1) {
			if loc[0] < loc[1] {
				spans = append(spans, [2]int{loc[0], loc[1]})
			}
		}
	}
	if len(spans) == 0 {
		return []llmSegment{{text: text}}
	}
	sort.SliceStable(spans, func(i, j int) bool { return spans[i][0] < spans[j][0] })

	segs := make([]llmSegment, 0, len(spans)*2+1)
	last := 0
	for _, span := range spans {
		if span[0] < last {
			continue
		}
		if span[0] > last {
			segs = append(segs, llmSegment{text: text[last:span[0]]})
		}
		segs = append(segs, llmSegment{preserved: true, text: text[span[0]:span[1]]})
		last = span[1]
	}
	if last < len(text) {
		segs = append(segs, llmSegment{text: text[last:]})
	}
	return segs
}

// llmFencedSpans returns whole-line fenced code block spans. RE2 has no
// backreferences, so fences are matched by scanning lines.
func llmFencedSpans(text string) [][2]int {
	var spans [][2]int
	offset := 0
	open := -1
	var fenceChar byte
	fenceLen := 0
	for offset <= len(text) {
		line := text[offset:]
		next := len(text) + 1
		if i := strings.IndexByte(line, '\n'); i >= 0 {
			line = line[:i]
			next = offset + i + 1
		}
		trimmed := strings.TrimLeft(line, " \t")
		if len(line)-len(trimmed) <= 3 {
			if open < 0 {
				if n, c := llmFenceRun(trimmed); n > 0 {
					open, fenceChar, fenceLen = offset, c, n
				}
			} else if n, c := llmFenceRun(trimmed); n >= fenceLen && c == fenceChar && strings.TrimSpace(trimmed[n:]) == "" {
				spans = append(spans, [2]int{open, next})
				open = -1
			}
		}
		offset = next
	}
	return spans
}

// llmFenceRun reports the leading run of ` or ~ and which character it is.
func llmFenceRun(s string) (int, byte) {
	if s == "" || (s[0] != '`' && s[0] != '~') {
		return 0, 0
	}
	i := 0
	for i < len(s) && s[i] == s[0] {
		i++
	}
	return i, s[0]
}
