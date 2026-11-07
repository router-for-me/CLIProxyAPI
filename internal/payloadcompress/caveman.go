package payloadcompress

import (
	"regexp"
	"strings"
)

// cavemanEngine compresses prose in user turns with a language pack: filler,
// pleasantries, hedging, redundant framing. It is lossy by design, so it is off
// unless a combo names it, only touches user text, and leaves code, URLs, error
// output and stack traces byte-identical.
type cavemanEngine struct {
	level string
	opts  StepOptions
}

func (cavemanEngine) name() string { return EngineCaveman }

const cavemanDefaultMinLength = 50

func (e cavemanEngine) compressKind(kind segmentKind, text string) (string, bool) {
	minLength := e.opts.MinMessageLength
	if minLength <= 0 {
		minLength = cavemanDefaultMinLength
	}
	if kind != kindUser || len(text) < minLength || charLen(text) < minLength {
		return text, false
	}
	if e.level == "" {
		e.level = "lite"
	}

	protected, spans := protectSpans(text)
	// Judge code dominance after protecting spans: fenced blocks and inline code
	// are already hidden, so only the remaining prose is measured.
	if isCodeDominant(protected) {
		return text, false
	}

	out := protected
	for _, rule := range e.ruleSet(protected) {
		out = rule.apply(out)
	}
	out = recapitalize(cleanupArtifacts(out))
	if out == text || len(out) >= len(text) {
		return text, false
	}
	return restoreSpans(out, spans), true
}

// pack picks the rule tag. An explicit language wins; otherwise detection
// chooses among the enabled packs, which default to English only.
func (e cavemanEngine) pack(text string) string {
	if tag := strings.TrimSpace(e.opts.Language); tag != "" {
		return tag
	}
	if e.opts.AutoDetect == nil || *e.opts.AutoDetect {
		if tag := detectLanguage(text); e.packEnabled(tag) {
			return tag
		}
	}
	return "en"
}

func (e cavemanEngine) packEnabled(tag string) bool {
	if len(e.opts.LanguagePacks) == 0 {
		return tag == "en"
	}
	for _, pack := range e.opts.LanguagePacks {
		if strings.EqualFold(strings.TrimSpace(pack), tag) {
			return true
		}
	}
	return false
}

// ruleSet resolves the language pack for this segment, then applies the level and
// skip-rules filters.
func (e cavemanEngine) ruleSet(text string) []cavemanRule {
	rules := filterRulesByContext(packRulesFor(e.pack(text)))
	if len(e.opts.SkipRules) == 0 {
		return filterRulesByLevel(rules, e.level)
	}
	skip := make(map[string]struct{}, len(e.opts.SkipRules))
	for _, name := range e.opts.SkipRules {
		skip[strings.ToLower(strings.TrimSpace(name))] = struct{}{}
	}
	kept := make([]cavemanRule, 0, len(rules))
	for _, rule := range rules {
		if _, drop := skip[rule.name]; drop {
			continue
		}
		kept = append(kept, rule)
	}
	return filterRulesByLevel(kept, e.level)
}

// filterRulesByContext keeps the rules written for user prose and for every
// role. Upstream selects rules per message role (getRulesForContext); this
// scanner only rewrites user text, so assistant-only rules must never run here.
// They assume a model wrote the text and delete openings such as "This is a...".
func filterRulesByContext(rules []cavemanRule) []cavemanRule {
	out := make([]cavemanRule, 0, len(rules))
	for _, rule := range rules {
		if rule.context == "all" || rule.context == "user" {
			out = append(out, rule)
		}
	}
	return out
}

func filterRulesByLevel(rules []cavemanRule, level string) []cavemanRule {
	rank := levelRank(level)
	out := make([]cavemanRule, 0, len(rules))
	for _, rule := range rules {
		if levelRank(rule.level) > rank {
			continue
		}
		out = append(out, rule)
	}
	return out
}

func levelRank(level string) int {
	switch level {
	case "full":
		return 1
	case "ultra":
		return 2
	default:
		return 0
	}
}

// cavemanRule is one rewrite. A rule either replaces matches verbatim, maps the
// matched text through a table, or only deletes when its guards allow it.
type cavemanRule struct {
	name  string
	level string
	// context is the message role the rule was written for: "user", "assistant"
	// or "all". See filterRulesByContext.
	context string
	re      *packRegex
	repl    string
	table   map[string]string
	// lowerOnly deletes a match only when the next byte is a lowercase letter.
	// Go's RE2 has no lookahead, so that condition is checked here.
	lowerOnly bool
	// notAfter holds literals a match must not directly follow ("make ", "be ").
	notAfter []string
	// notAfterWord rejects matches preceded by a letter, digit or underscore.
	notAfterWord bool
	// notBeforeWord rejects matches followed by a letter, digit or underscore.
	notBeforeWord bool
	// tail extends a match over trailing punctuation or whitespace, mirroring the
	// `\s*` and `[,.!?\s]*` suffixes the packs append after their lookahead.
	tail tailKind
}

// tailKind says how far past a match the deleted span reaches.
type tailKind uint8

const (
	tailNone tailKind = iota
	tailSpace
	tailPunct
)

func (r cavemanRule) guarded() bool {
	return r.lowerOnly || r.notAfterWord || r.notBeforeWord || r.tail != tailNone ||
		len(r.notAfter) > 0 || r.table != nil || strings.Contains(r.repl, "$")
}

func (r cavemanRule) apply(text string) string {
	if !r.guarded() {
		return r.re.replaceAllString(text, r.repl)
	}
	locs := r.re.matchSpans(text)
	if len(locs) == 0 {
		return text
	}
	var b strings.Builder
	b.Grow(len(text))
	prev := 0
	for _, loc := range locs {
		if loc[0] < prev {
			continue
		}
		if r.lowerOnly && (loc[1] >= len(text) || text[loc[1]] < 'a' || text[loc[1]] > 'z') {
			continue
		}
		if r.notAfterWord && isWordPrefix(text, loc[0]) {
			continue
		}
		if r.notBeforeWord && isWordNext(text, loc[1]) {
			continue
		}
		if len(r.notAfter) > 0 && trailingGuardHit(text, loc[0], r.notAfter) {
			continue
		}
		end := extendTail(text, loc[1], r.tail)
		b.WriteString(text[prev:loc[0]])
		b.WriteString(r.replacementFor(text, loc))
		prev = end
	}
	b.WriteString(text[prev:])
	return b.String()
}

// replacementFor returns the text substituted for the match at loc in text.
// Expansion runs against the original text: a $n replacement needs the match's
// captures, and those only exist in the context the pattern matched in. Running
// it on the sliced match would fail whenever the pattern anchors on what came
// before it, such as a lookbehind.
func (r cavemanRule) replacementFor(text string, loc [2]int) string {
	match := text[loc[0]:loc[1]]
	if r.table != nil {
		key := strings.ToLower(strings.TrimRight(match, " \t\n"))
		if repl, ok := r.table[key]; ok {
			return repl
		}
		if r.repl == "" {
			return match
		}
	}
	if !strings.Contains(r.repl, "$") {
		return r.repl
	}
	return r.re.expandCaptures(text, loc, r.repl)
}

// extendTail walks past the punctuation or whitespace the original pattern
// consumed after its lookahead.
func extendTail(text string, end int, tail tailKind) int {
	if tail == tailNone {
		return end
	}
	for end < len(text) {
		c := text[end]
		if c == ' ' || c == '\t' {
			end++
			continue
		}
		if tail == tailPunct && (c == ',' || c == '.' || c == '!' || c == '?') {
			end++
			continue
		}
		break
	}
	return end
}

// trailingGuardHit mirrors the JS lookbehind `(?<!X\s)` for literal X.
func trailingGuardHit(text string, start int, guards []string) bool {
	head := strings.TrimRight(text[:start], " \t\n")
	for _, guard := range guards {
		if strings.HasSuffix(head, guard) {
			return true
		}
	}
	return false
}

var (
	reSpaceRun     = regexp.MustCompile(`[ \t]{2,}`)
	reSpacePunct   = regexp.MustCompile(`[ \t]+([,.;:!?])`)
	reRepeatPunct  = regexp.MustCompile(`[.!?]{2,}`)
	reTrailingGap  = regexp.MustCompile(`(?m)[ \t]+$`)
	reBlankRun     = regexp.MustCompile(`\n{3,}`)
	reLeadingBreak = regexp.MustCompile(`^\n+`)
	reTrailBreak   = regexp.MustCompile(`\n+$`)
	reSentenceStar = regexp.MustCompile(`(?m)(^|[.!?][ \t]|\n[ \t]*)([a-z])`)
	reErrorLine    = regexp.MustCompile(`^\s*(?:[A-Za-z]*Error|Traceback|panic|Exception):`)
	reStackFrame   = regexp.MustCompile(`^\s+at\s|^goroutine \d+`)
	reBlankLine    = regexp.MustCompile(`^\s*$`)
)

func cleanupArtifacts(text string) string {
	text = reSpaceRun.ReplaceAllString(text, " ")
	text = reSpacePunct.ReplaceAllString(text, "$1")
	text = reRepeatPunct.ReplaceAllStringFunc(text, func(m string) string {
		return m[len(m)-1:]
	})
	text = reTrailingGap.ReplaceAllString(text, "")
	text = reBlankRun.ReplaceAllString(text, "\n\n")
	text = reLeadingBreak.ReplaceAllString(text, "")
	return reTrailBreak.ReplaceAllString(text, "")
}

func recapitalize(text string) string {
	return reSentenceStar.ReplaceAllStringFunc(text, func(m string) string {
		last := m[len(m)-1]
		if last >= 'a' && last <= 'z' {
			return m[:len(m)-1] + string(last-32)
		}
		return m
	})
}

// isCodeDominant reports whether enough lines look like code that prose
// normalization must be skipped for the whole span. It biases towards leaving
// text alone, never towards corrupting it.
func isCodeDominant(text string) bool {
	total, code := 0, 0
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		total++
		if looksLikeCode(line) {
			code++
		}
	}
	return total >= 3 && code*10 >= total*3
}

func looksLikeCode(line string) bool {
	for _, prefix := range []string{
		"const ", "let ", "var ", "func ", "def ", "class ", "import ", "from ", "package ",
		"#include", "#define", "//", "/*", "}", "return ", "if ", "for ", "while ", "try ",
		"catch ", "case ", "type ", "interface ", "pub ", "fn ", "use ", "<<<<<<<", "=======",
	} {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	for _, marker := range []string{"=>", "();", " == ", " != ", " = ", "::", "{\"", ":=", "&&", "||"} {
		if strings.Contains(line, marker) {
			return true
		}
	}
	switch line[len(line)-1] {
	case ';', '{', '}':
		return true
	}
	return false
}

// protectSpans hides content no rule may touch behind numbered placeholders, so
// restoring is byte-exact: error and stack-trace lines, fenced blocks, inline code
// and URLs.
func protectSpans(text string) (string, []string) {
	var spans []string
	var b strings.Builder
	b.Grow(len(text))

	placeholder := func(value string) {
		b.WriteByte(0x00)
		b.WriteString(itoaDecimal(len(spans)))
		b.WriteByte(0x01)
		spans = append(spans, value)
	}

	lineStart := true
	for i := 0; i < len(text); {
		if lineStart {
			lineStart = false
			if end := errorLineEnd(text, i); end > i {
				placeholder(text[i:end])
				i = end
				lineStart = true
				continue
			}
		}
		switch {
		case strings.HasPrefix(text[i:], "```") || strings.HasPrefix(text[i:], "~~~"):
			fence := text[i : i+3]
			end := strings.Index(text[i+3:], fence)
			if end < 0 {
				b.WriteString(text[i:])
				return b.String(), spans
			}
			end += i + 3 + 3
			placeholder(text[i:end])
			i = end
		case text[i] == '`':
			end := strings.IndexByte(text[i+1:], '`')
			if end < 0 {
				b.WriteByte(text[i])
				i++
				continue
			}
			end += i + 2
			placeholder(text[i:end])
			i = end
		case strings.HasPrefix(text[i:], "http://"), strings.HasPrefix(text[i:], "https://"):
			end := i
			for end < len(text) && !isURLTerminator(text[end]) {
				end++
			}
			placeholder(text[i:end])
			i = end
		default:
			b.WriteByte(text[i])
			if text[i] == '\n' {
				lineStart = true
			}
			i++
		}
	}
	return b.String(), spans
}

// errorLineEnd returns the end of the error or stack-trace block starting at i,
// continuation lines included, or i when the line is ordinary prose.
func errorLineEnd(text string, i int) int {
	end := lineEnd(text, i)
	if !reErrorLine.MatchString(text[i:end]) && !reStackFrame.MatchString(text[i:end]) {
		return i
	}
	for end < len(text) {
		next := lineEnd(text, end+1)
		candidate := text[end+1 : next]
		if reBlankLine.MatchString(candidate) {
			break
		}
		if !reStackFrame.MatchString(candidate) && !reErrorLine.MatchString(candidate) &&
			!strings.HasPrefix(candidate, " ") && !strings.HasPrefix(candidate, "\t") {
			break
		}
		end = next
	}
	return end
}

func lineEnd(text string, i int) int {
	if idx := strings.IndexByte(text[i:], '\n'); idx >= 0 {
		return i + idx
	}
	return len(text)
}

func isURLTerminator(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '"', '\'', '<', '>', ')', ']', '}':
		return true
	}
	return false
}

func restoreSpans(text string, spans []string) string {
	if len(spans) == 0 {
		return text
	}
	var b strings.Builder
	b.Grow(len(text))
	for i := 0; i < len(text); i++ {
		if text[i] != 0x00 || i+1 >= len(text) {
			b.WriteByte(text[i])
			continue
		}
		end := strings.IndexByte(text[i+1:], 0x01)
		if end < 0 {
			b.WriteByte(text[i])
			continue
		}
		index := 0
		valid := end > 0
		for _, c := range []byte(text[i+1 : i+1+end]) {
			if c < '0' || c > '9' {
				valid = false
				break
			}
			index = index*10 + int(c-'0')
		}
		if !valid || index >= len(spans) {
			b.WriteByte(text[i])
			continue
		}
		b.WriteString(spans[index])
		i += 1 + end
	}
	return b.String()
}

func itoaDecimal(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for v > 0 {
		pos--
		buf[pos] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[pos:])
}
