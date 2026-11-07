package payloadcompress

import (
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dlclark/regexp2/v2"
	log "github.com/sirupsen/logrus"
)

// packRegex is one compiled rule from the embedded filter data. Rule packs are
// written in JS regex syntax and use lookaround and backreferences, which RE2
// rejects. Such rules used to be dropped at load time; instead they now fall back
// to regexp2, a backtracking engine that supports them, so the shipped rules keep
// working instead of silently disappearing.
//
// RE2 stays the default: linear time, no pathological inputs. regexp2 runs only
// the patterns RE2 cannot compile, with a match timeout that bounds
// backtracking. The data is ours and already vetted, so the timeout is a
// backstop rather than the main protection.
type packRegex struct {
	std    *regexp.Regexp
	adv    *regexp2.Regexp
	source string
}

// packRegexTimeout caps a single regexp2 match. Backtracking patterns from the
// packs are tiny alternations; anything slower than this is not doing useful
// work. On timeout the caller treats the pattern as non-matching, which leaves
// the text alone.
const packRegexTimeout = 100 * time.Millisecond

// compilePackRegex compiles a JavaScript-flavoured pattern. RE2 is tried first.
// A pattern only falls back to the backtracking engine when RE2 rejects it.
func compilePackRegex(pattern string) (*packRegex, error) {
	if re, err := regexp.Compile(pattern); err == nil {
		return &packRegex{std: re, source: pattern}, nil
	}
	re, errAdv := regexp2.Compile(pattern, regexp2.None)
	if errAdv != nil {
		return nil, errAdv
	}
	re.MatchTimeout = packRegexTimeout
	return &packRegex{adv: re, source: pattern}, nil
}

// compilePackRegexes compiles every pattern that compiles, dropping the rest so
// one bad entry cannot take a whole pack with it.
func compilePackRegexes(id, field string, patterns []string) []*packRegex {
	out := make([]*packRegex, 0, len(patterns))
	for _, pattern := range patterns {
		re, err := compilePackRegex(pattern)
		if err != nil {
			log.Debugf("payloadcompress: %s: dropping %s pattern %q: %v", id, field, pattern, err)
			continue
		}
		out = append(out, re)
	}
	return out
}

// match reports whether the pattern matches anywhere in text.
func (r *packRegex) match(text string) bool {
	if r == nil {
		return false
	}
	if r.std != nil {
		return r.std.MatchString(text)
	}
	ok, err := r.adv.MatchString(text)
	if err != nil {
		// Timeout: report no match rather than corrupting text with a partial one.
		return false
	}
	return ok
}

// replaceAllString replaces every match with repl, which may reference capture
// groups with $1. A regexp2 error leaves text untouched.
func (r *packRegex) replaceAllString(text, repl string) string {
	if r == nil {
		return text
	}
	if r.std != nil {
		return r.std.ReplaceAllString(text, repl)
	}
	out, err := r.adv.Replace(text, repl, -1, -1)
	if err != nil {
		return text
	}
	return out
}

// expandCaptures renders repl for the match at loc, expanding $n against that
// match's groups. The pattern is matched again from the span start rather than
// against the sliced match text, so a lookbehind still sees what came before.
func (r *packRegex) expandCaptures(text string, loc [2]int, repl string) string {
	if r.std != nil {
		return r.std.ReplaceAllString(text[loc[0]:loc[1]], repl)
	}
	var expanded string
	found := false
	// regexp2 has no per-match expand call. ReplaceFunc limited to one match
	// hands the evaluator the match with its groups resolved.
	_, err := r.adv.ReplaceFunc(text, func(m regexp2.Match) string {
		found = true
		expanded = expandMatch(m, repl)
		return expanded
	}, loc[0], 1)
	if err != nil || !found {
		return text[loc[0]:loc[1]]
	}
	return expanded
}

// expandMatch renders repl against one match's groups, supporting the $1..$n
// forms the rule packs use.
func expandMatch(m regexp2.Match, repl string) string {
	var b strings.Builder
	for i := 0; i < len(repl); i++ {
		if repl[i] != '$' || i+1 >= len(repl) {
			b.WriteByte(repl[i])
			continue
		}
		j := i + 1
		group := 0
		digits := 0
		for j < len(repl) && repl[j] >= '0' && repl[j] <= '9' {
			group = group*10 + int(repl[j]-'0')
			j++
			digits++
		}
		if digits == 0 {
			b.WriteByte(repl[i])
			continue
		}
		if g := m.GroupByNumber(group); g != nil {
			b.WriteString(g.String())
		}
		i = j - 1
	}
	return b.String()
}

// matchSpans returns the byte ranges of every match, in order. Byte offsets are
// what RE2 and regexp2's rune indices both reduce to here, since all rule text
// comparisons later slice by byte.
func (r *packRegex) matchSpans(text string) [][2]int {
	if r == nil {
		return nil
	}
	if r.std != nil {
		locs := r.std.FindAllStringIndex(text, -1)
		spans := make([][2]int, len(locs))
		for i, loc := range locs {
			spans[i] = [2]int{loc[0], loc[1]}
		}
		return spans
	}
	// regexp2 indexes runes; the caller slices bytes. Capture.ByteRange maps
	// rune indices as if they were byte offsets, so the conversion is done here
	// against the actual text.
	byteAt := runeToByteOffsets(text)
	var spans [][2]int
	m, err := r.adv.FindStringMatch(text)
	for err == nil && m != nil {
		start := byteAt(m.RuneIndex)
		spans = append(spans, [2]int{start, byteAt(m.RuneIndex + m.RuneLength)})
		m, err = r.adv.FindNextMatch(m)
	}
	return spans
}

// runeToByteOffsets returns a rune-index to byte-offset mapper for text. ASCII
// needs no table, which is the common case for tool output.
func runeToByteOffsets(text string) func(int) int {
	ascii := true
	for i := 0; i < len(text); i++ {
		if text[i] >= utf8.RuneSelf {
			ascii = false
			break
		}
	}
	if ascii {
		return func(i int) int { return i }
	}
	offsets := make([]int, 0, len(text)+1)
	for i := range text {
		offsets = append(offsets, i)
	}
	offsets = append(offsets, len(text))
	return func(i int) int {
		if i <= 0 {
			return 0
		}
		if i >= len(offsets) {
			return len(text)
		}
		return offsets[i]
	}
}
