package helps

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	log "github.com/sirupsen/logrus"
)

// zeroWidthSpace is the Unicode zero-width space character used for obfuscation.
const zeroWidthSpace = "\u200B"

// combiningGraphemeJoiner is the Unicode combining grapheme joiner (U+034F)
// used for Antigravity system-instruction obfuscation where U+200B obfuscation
// remains rejected by the upstream endpoint.
const combiningGraphemeJoiner = "\u034F"

type sensitiveWordRule struct {
	target        string
	lowerTarget   string
	replacement   string
	isReplacement bool
}

type compiledPatternRule struct {
	regex       *regexp.Regexp
	replacement string
}

type patternMatchSpan struct {
	start       int
	end         int
	replacement string
}

// SensitiveWordMatcher holds the compiled regex and replacement state for matching sensitive words.
type SensitiveWordMatcher struct {
	regex             *regexp.Regexp
	replaceFn         func(string) string
	lookupReplacement func(string) (string, bool)
	patternRules      []compiledPatternRule
}

func (m *SensitiveWordMatcher) hasRules() bool {
	return m != nil && (m.regex != nil || len(m.patternRules) > 0)
}

// SensitiveWordConfigCacheKey builds a deterministic, collision-free cache key
// for a (words, replacements) configuration pair.
func SensitiveWordConfigCacheKey(words []string, replacements map[string]string) string {
	return SensitiveWordConfigCacheKeyWithPatterns(words, replacements, nil)
}

// SensitiveWordConfigCacheKeyWithPatterns builds a deterministic, collision-free
// cache key for (words, replacements, patterns).
func SensitiveWordConfigCacheKeyWithPatterns(
	words []string,
	replacements map[string]string,
	patterns []config.SensitivePatternRule,
) string {
	var b strings.Builder
	b.WriteString("w:")
	b.WriteString(strconv.Itoa(len(words)))
	b.WriteByte(';')
	for _, w := range words {
		b.WriteString(strconv.Itoa(len(w)))
		b.WriteByte(':')
		b.WriteString(w)
		b.WriteByte(';')
	}
	b.WriteString("|r:")
	b.WriteString(strconv.Itoa(len(replacements)))
	b.WriteByte(';')
	if len(replacements) > 0 {
		keys := make([]string, 0, len(replacements))
		for k := range replacements {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := replacements[k]
			b.WriteString(strconv.Itoa(len(k)))
			b.WriteByte(':')
			b.WriteString(k)
			b.WriteByte('=')
			b.WriteString(strconv.Itoa(len(v)))
			b.WriteByte(':')
			b.WriteString(v)
			b.WriteByte(';')
		}
	}
	b.WriteString("|p:")
	b.WriteString(strconv.Itoa(len(patterns)))
	b.WriteByte(';')
	for _, p := range patterns {
		b.WriteString(strconv.Itoa(len(p.Pattern)))
		b.WriteByte(':')
		b.WriteString(p.Pattern)
		b.WriteByte('=')
		b.WriteString(strconv.Itoa(len(p.Replacement)))
		b.WriteByte(':')
		b.WriteString(p.Replacement)
		b.WriteByte(';')
	}
	return b.String()
}

// BuildSensitiveWordMatcher compiles a regex from the word list using U+200B.
// Words are sorted by length (longest first) for proper matching.
func BuildSensitiveWordMatcher(words []string) *SensitiveWordMatcher {
	return BuildSensitiveWordMatcherWithPatterns(words, nil, nil)
}

// BuildSensitiveWordMatcherWithReplacements compiles a matcher combining literal
// replacements and U+200B sensitive-word obfuscation.
func BuildSensitiveWordMatcherWithReplacements(words []string, replacements map[string]string) *SensitiveWordMatcher {
	return BuildSensitiveWordMatcherWithPatterns(words, replacements, nil)
}

// BuildSensitiveWordMatcherWithPatterns compiles a matcher combining ordered RE2
// regex replacements, literal replacements, and U+200B sensitive-word obfuscation.
func BuildSensitiveWordMatcherWithPatterns(
	words []string,
	replacements map[string]string,
	patterns []config.SensitivePatternRule,
) *SensitiveWordMatcher {
	return buildSensitiveWordMatcher(words, replacements, patterns, obfuscateWord, zeroWidthSpace)
}

// BuildAntigravitySensitiveWordMatcher compiles a matcher for Antigravity system
// instructions combining literal replacements and per-subword U+034F obfuscation.
func BuildAntigravitySensitiveWordMatcher(words []string, replacements map[string]string) *SensitiveWordMatcher {
	return BuildAntigravitySensitiveWordMatcherWithPatterns(words, replacements, nil)
}

// BuildAntigravitySensitiveWordMatcherWithPatterns compiles a matcher for Antigravity
// system instructions combining ordered RE2 regex replacements, literal replacements,
// and per-subword U+034F obfuscation.
func BuildAntigravitySensitiveWordMatcherWithPatterns(
	words []string,
	replacements map[string]string,
	patterns []config.SensitivePatternRule,
) *SensitiveWordMatcher {
	return buildSensitiveWordMatcher(words, replacements, patterns, obfuscateAntigravityPhrase, combiningGraphemeJoiner)
}

func buildSensitiveWordMatcher(
	words []string,
	replacements map[string]string,
	patterns []config.SensitivePatternRule,
	obfuscateFn func(string) string,
	marker string,
) *SensitiveWordMatcher {
	if len(words) == 0 && len(replacements) == 0 && len(patterns) == 0 {
		return nil
	}

	var compiledPatterns []compiledPatternRule
	if len(patterns) > 0 {
		compiledPatterns = make([]compiledPatternRule, 0, len(patterns))
		for i, p := range patterns {
			if strings.TrimSpace(p.Pattern) == "" {
				continue
			}
			compiled, errCompile := regexp.Compile(p.Pattern)
			if errCompile != nil {
				log.WithFields(log.Fields{
					"rule_index": i + 1,
					"pattern":    p.Pattern,
					"error":      errCompile,
				}).Warn("sensitive-patterns-replace rule skipped: invalid regex")
				continue
			}
			compiledPatterns = append(compiledPatterns, compiledPatternRule{
				regex:       compiled,
				replacement: p.Replacement,
			})
		}
	}

	var rules []sensitiveWordRule
	var replacementRules []sensitiveWordRule
	var exactReplacements map[string]string
	var lowerReplacements map[string]string
	seenLower := make(map[string]struct{}, len(words)+len(replacements))

	if len(replacements) > 0 {
		rawKeys := make([]string, 0, len(replacements))
		for k := range replacements {
			rawKeys = append(rawKeys, k)
		}
		sort.Strings(rawKeys)

		for _, rawKey := range rawKeys {
			target := strings.TrimSpace(rawKey)
			if target == "" {
				continue
			}
			if exactReplacements == nil {
				exactReplacements = make(map[string]string, len(rawKeys))
				lowerReplacements = make(map[string]string, len(rawKeys))
			}
			if _, exists := exactReplacements[target]; !exists || rawKey == target {
				exactReplacements[target] = replacements[rawKey]
			}
			lower := strings.ToLower(target)
			if _, exists := lowerReplacements[lower]; !exists {
				lowerReplacements[lower] = replacements[rawKey]
			}
			if _, exists := seenLower[lower]; exists {
				continue
			}
			seenLower[lower] = struct{}{}
			rule := sensitiveWordRule{
				target:        target,
				lowerTarget:   lower,
				replacement:   replacements[rawKey],
				isReplacement: true,
			}
			rules = append(rules, rule)
			replacementRules = append(replacementRules, rule)
		}
	}

	for _, rawWord := range words {
		w := strings.TrimSpace(rawWord)
		if utf8.RuneCountInString(w) < 2 || strings.Contains(w, marker) {
			continue
		}
		if obfuscateFn(w) == w {
			continue
		}
		lower := strings.ToLower(w)
		if _, exists := seenLower[lower]; exists {
			continue
		}
		seenLower[lower] = struct{}{}
		rules = append(rules, sensitiveWordRule{
			target:      w,
			lowerTarget: lower,
		})
	}

	if len(rules) == 0 && len(compiledPatterns) == 0 {
		return nil
	}

	var re *regexp.Regexp
	var lookupReplacement func(string) (string, bool)
	var replaceFn func(string) string
	if len(rules) > 0 {
		sortRulesLongestFirst(rules)
		sortRulesLongestFirst(replacementRules)

		escaped := make([]string, len(rules))
		for i, r := range rules {
			escaped[i] = regexp.QuoteMeta(r.target)
		}

		pattern := "(?i:" + strings.Join(escaped, "|") + ")"
		compiledLiteral, errCompile := regexp.Compile(pattern)
		if errCompile != nil {
			if len(compiledPatterns) == 0 {
				return nil
			}
		} else {
			re = compiledLiteral
			replaceFn = obfuscateFn
			if len(exactReplacements) > 0 {
				lookupReplacement = func(matched string) (string, bool) {
					if repl, ok := exactReplacements[matched]; ok {
						return repl, true
					}
					if repl, ok := lowerReplacements[strings.ToLower(matched)]; ok {
						return repl, true
					}
					for _, r := range replacementRules {
						if strings.EqualFold(matched, r.target) {
							return r.replacement, true
						}
					}
					return "", false
				}
				replaceFn = func(matched string) string {
					if repl, ok := lookupReplacement(matched); ok {
						return repl
					}
					return obfuscateFn(matched)
				}
			}
		}
	}

	return &SensitiveWordMatcher{
		regex:             re,
		replaceFn:         replaceFn,
		lookupReplacement: lookupReplacement,
		patternRules:      compiledPatterns,
	}
}

func sortRulesLongestFirst(rules []sensitiveWordRule) {
	sort.SliceStable(rules, func(i, j int) bool {
		ri := utf8.RuneCountInString(rules[i].target)
		rj := utf8.RuneCountInString(rules[j].target)
		if ri != rj {
			return ri > rj
		}
		if len(rules[i].target) != len(rules[j].target) {
			return len(rules[i].target) > len(rules[j].target)
		}
		if rules[i].isReplacement != rules[j].isReplacement {
			return rules[i].isReplacement
		}
		return rules[i].lowerTarget < rules[j].lowerTarget
	})
}

// obfuscateWord inserts a zero-width space after the first grapheme.
func obfuscateWord(word string) string {
	if strings.Contains(word, zeroWidthSpace) {
		return word
	}

	r, size := utf8.DecodeRuneInString(word)
	if r == utf8.RuneError || size >= len(word) {
		return word
	}

	return string(r) + zeroWidthSpace + word[size:]
}

func isAntigravitySubwordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '\u034F' || r == '\u200B'
}

func obfuscateSingleSubwordCGJ(subword string) (string, bool) {
	if strings.Contains(subword, combiningGraphemeJoiner) {
		return subword, true
	}
	r, size := utf8.DecodeRuneInString(subword)
	if r == utf8.RuneError || size >= len(subword) {
		return subword, false
	}
	return string(r) + combiningGraphemeJoiner + subword[size:], true
}

// obfuscateAntigravityPhrase inserts U+034F after the first rune of every
// alphanumeric subword (>= 2 runes) within phrase, preserving punctuation and whitespace.
func obfuscateAntigravityPhrase(phrase string) string {
	if phrase == "" || strings.Contains(phrase, combiningGraphemeJoiner) {
		return phrase
	}

	var b strings.Builder
	b.Grow(len(phrase) + len(combiningGraphemeJoiner)*2)
	subwordStart := -1
	inserted := false

	for i, r := range phrase {
		if isAntigravitySubwordRune(r) {
			if subwordStart < 0 {
				subwordStart = i
			}
			continue
		}
		if subwordStart >= 0 {
			obf, did := obfuscateSingleSubwordCGJ(phrase[subwordStart:i])
			b.WriteString(obf)
			if did {
				inserted = true
			}
			subwordStart = -1
		}
		b.WriteRune(r)
	}
	if subwordStart >= 0 {
		obf, did := obfuscateSingleSubwordCGJ(phrase[subwordStart:])
		b.WriteString(obf)
		if did {
			inserted = true
		}
	}

	if inserted {
		return b.String()
	}

	r, size := utf8.DecodeRuneInString(phrase)
	if r == utf8.RuneError || size >= len(phrase) {
		return phrase
	}
	return string(r) + combiningGraphemeJoiner + phrase[size:]
}

func (m *SensitiveWordMatcher) collectPatternSpans(text string) []patternMatchSpan {
	if m == nil || len(m.patternRules) == 0 || text == "" {
		return nil
	}
	var spans []patternMatchSpan
	spansOverlap := func(aStart, aEnd, bStart, bEnd int) bool {
		if aStart == aEnd {
			return bStart <= aStart && aStart <= bEnd
		}
		if bStart == bEnd {
			return aStart <= bStart && bStart <= aEnd
		}
		return aStart < bEnd && bStart < aEnd
	}

	for _, rule := range m.patternRules {
		if rule.regex == nil {
			continue
		}
		matches := rule.regex.FindAllStringSubmatchIndex(text, -1)
		if len(matches) == 0 {
			continue
		}
		if len(spans) == 0 {
			spans = make([]patternMatchSpan, 0, len(matches))
			for _, submatch := range matches {
				if len(submatch) < 2 {
					continue
				}
				start, end := submatch[0], submatch[1]
				repl := string(rule.regex.ExpandString(nil, rule.replacement, text, submatch))
				if start == end && repl == "" {
					continue
				}
				spans = append(spans, patternMatchSpan{
					start:       start,
					end:         end,
					replacement: repl,
				})
			}
			continue
		}

		var accepted []patternMatchSpan
		var subsumed []bool
		ci := 0
		for _, submatch := range matches {
			if len(submatch) < 2 {
				continue
			}
			start, end := submatch[0], submatch[1]
			repl := string(rule.regex.ExpandString(nil, rule.replacement, text, submatch))
			if start == end && repl == "" {
				continue
			}

			for ci < len(spans) {
				c := spans[ci]
				if c.end < start || (c.end == start && c.start < c.end && start < end) {
					ci++
					continue
				}
				break
			}

			conflict := false
			var localSubsumed []int
			for j := ci; j < len(spans); j++ {
				if subsumed != nil && subsumed[j] {
					continue
				}
				c := spans[j]
				if c.start > end || (c.start == end && c.start < c.end && start < end) {
					break
				}
				if !spansOverlap(start, end, c.start, c.end) {
					continue
				}
				if repl == "" && c.replacement == "" && start <= c.start && c.end <= end {
					localSubsumed = append(localSubsumed, j)
					continue
				}
				conflict = true
				break
			}
			if conflict {
				continue
			}
			if len(localSubsumed) > 0 {
				if subsumed == nil {
					subsumed = make([]bool, len(spans))
				}
				for _, j := range localSubsumed {
					subsumed[j] = true
				}
			}
			accepted = append(accepted, patternMatchSpan{
				start:       start,
				end:         end,
				replacement: repl,
			})
		}

		if len(accepted) == 0 {
			continue
		}
		merged := make([]patternMatchSpan, 0, len(spans)+len(accepted))
		si, ai := 0, 0
		for si < len(spans) && ai < len(accepted) {
			if subsumed != nil && subsumed[si] {
				si++
				continue
			}
			if spans[si].start < accepted[ai].start || (spans[si].start == accepted[ai].start && spans[si].end <= accepted[ai].end) {
				merged = append(merged, spans[si])
				si++
			} else {
				merged = append(merged, accepted[ai])
				ai++
			}
		}
		for ; si < len(spans); si++ {
			if subsumed != nil && subsumed[si] {
				continue
			}
			merged = append(merged, spans[si])
		}
		for ; ai < len(accepted); ai++ {
			merged = append(merged, accepted[ai])
		}
		spans = merged
	}
	return spans
}

func (m *SensitiveWordMatcher) obfuscateLiterals(text string) string {
	if m == nil || m.regex == nil || text == "" {
		return text
	}
	if m.replaceFn != nil {
		return m.regex.ReplaceAllStringFunc(text, m.replaceFn)
	}
	return m.regex.ReplaceAllStringFunc(text, obfuscateWord)
}

// obfuscateText replaces or obfuscates all sensitive words in the text.
func (m *SensitiveWordMatcher) obfuscateText(text string) string {
	if !m.hasRules() || text == "" {
		return text
	}
	spans := m.collectPatternSpans(text)
	if len(spans) == 0 {
		return m.obfuscateLiterals(text)
	}

	var b strings.Builder
	b.Grow(len(text))
	last := 0
	for _, span := range spans {
		if span.start > last {
			b.WriteString(m.obfuscateLiterals(text[last:span.start]))
		}
		b.WriteString(span.replacement)
		last = span.end
	}
	if last < len(text) {
		b.WriteString(m.obfuscateLiterals(text[last:]))
	}
	return b.String()
}

// Matches reports whether the text contains any configured sensitive word, literal replacement, or pattern rule match.
func (m *SensitiveWordMatcher) Matches(text string) bool {
	if !m.hasRules() {
		return false
	}
	if m.regex != nil && m.regex.MatchString(text) {
		return true
	}
	for _, rule := range m.patternRules {
		if rule.regex != nil && rule.regex.MatchString(text) {
			return true
		}
	}
	return false
}

// SanitizeDevinText applies configured pattern/literal replacements and Devin
// line-level sensitive-word filtering in a single non-cascading pass:
//   - matches handled by sensitive-patterns-replace or sensitive-words-replace emit
//     their replacement verbatim without re-scanning or triggering line deletion;
//   - original lines containing an unreplaced sensitive-words match are dropped.
func (m *SensitiveWordMatcher) SanitizeDevinText(text string) string {
	if !m.hasRules() || text == "" {
		return text
	}

	type devinOpKind uint8
	const (
		opLiteralText devinOpKind = iota
		opReplacement
		opSensitiveWord
	)
	type devinOp struct {
		start int
		end   int
		kind  devinOpKind
		repl  string
	}

	var ops []devinOp
	appendLiteralGapOps := func(gapStart, gapEnd int) {
		if gapStart >= gapEnd {
			return
		}
		if m.regex == nil {
			ops = append(ops, devinOp{start: gapStart, end: gapEnd, kind: opLiteralText})
			return
		}
		seg := text[gapStart:gapEnd]
		indices := m.regex.FindAllStringIndex(seg, -1)
		if len(indices) == 0 {
			ops = append(ops, devinOp{start: gapStart, end: gapEnd, kind: opLiteralText})
			return
		}
		last := 0
		for _, pair := range indices {
			s, e := pair[0], pair[1]
			if s > last {
				ops = append(ops, devinOp{start: gapStart + last, end: gapStart + s, kind: opLiteralText})
			}
			matched := seg[s:e]
			if m.lookupReplacement != nil {
				if repl, ok := m.lookupReplacement(matched); ok {
					ops = append(ops, devinOp{start: gapStart + s, end: gapStart + e, kind: opReplacement, repl: repl})
					last = e
					continue
				}
			}
			ops = append(ops, devinOp{start: gapStart + s, end: gapStart + e, kind: opSensitiveWord})
			last = e
		}
		if last < len(seg) {
			ops = append(ops, devinOp{start: gapStart + last, end: gapEnd, kind: opLiteralText})
		}
	}

	spans := m.collectPatternSpans(text)
	last := 0
	for _, span := range spans {
		if span.start > last {
			appendLiteralGapOps(last, span.start)
		}
		ops = append(ops, devinOp{
			start: span.start,
			end:   span.end,
			kind:  opReplacement,
			repl:  span.replacement,
		})
		last = span.end
	}
	if last < len(text) {
		appendLiteralGapOps(last, len(text))
	}

	// Build original line boundaries so replacement text never synthesizes fake
	// newlines and line-drop state stays strictly anchored to original lines.
	lineStarts := []int{0}
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			lineStarts = append(lineStarts, i+1)
		}
	}
	lineOf := func(pos int) int {
		if pos <= 0 {
			return 0
		}
		// Find largest line index whose start <= pos.
		idx := sort.Search(len(lineStarts), func(i int) bool {
			return lineStarts[i] > pos
		})
		return idx - 1
	}

	droppedLine := make([]bool, len(lineStarts))
	for _, op := range ops {
		if op.kind != opSensitiveWord || op.end <= op.start {
			continue
		}
		firstLine := lineOf(op.start)
		lastLine := lineOf(op.end - 1)
		for l := firstLine; l <= lastLine; l++ {
			droppedLine[l] = true
		}
	}

	var b strings.Builder
	b.Grow(len(text))
	for _, op := range ops {
		switch op.kind {
		case opSensitiveWord:
			continue
		case opReplacement:
			if op.repl == "" {
				continue
			}
			firstLine := lineOf(op.start)
			lastLine := firstLine
			if op.end > op.start {
				lastLine = lineOf(op.end - 1)
			}
			dropped := false
			for l := firstLine; l <= lastLine; l++ {
				if droppedLine[l] {
					dropped = true
					break
				}
			}
			if !dropped {
				b.WriteString(op.repl)
			}
		case opLiteralText:
			seg := text[op.start:op.end]
			curLine := lineOf(op.start)
			for len(seg) > 0 {
				nl := strings.IndexByte(seg, '\n')
				if nl < 0 {
					if !droppedLine[curLine] {
						b.WriteString(seg)
					}
					break
				}
				if !droppedLine[curLine] {
					b.WriteString(seg[:nl+1])
				}
				seg = seg[nl+1:]
				curLine++
			}
		}
	}

	return strings.TrimSpace(b.String())
}

// ObfuscateText replaces or obfuscates all sensitive words in the text.
func (m *SensitiveWordMatcher) ObfuscateText(text string) string {
	return m.obfuscateText(text)
}

// ObfuscateSensitiveWords processes the payload and obfuscates sensitive words
// in system blocks and message content.
func ObfuscateSensitiveWords(payload []byte, matcher *SensitiveWordMatcher) []byte {
	if !matcher.hasRules() || len(payload) == 0 {
		return payload
	}
	doc := ojson.NewDoc(payload)
	if !ObfuscateSensitiveWordsDoc(doc, matcher) {
		return payload
	}
	return doc.Bytes()
}

// ObfuscateSensitiveWordsDoc processes doc in place and obfuscates sensitive words
// in system blocks and message content.
func ObfuscateSensitiveWordsDoc(doc *ojson.Doc, matcher *SensitiveWordMatcher) bool {
	if !matcher.hasRules() || doc == nil || doc.Len() == 0 {
		return false
	}
	modified := obfuscateSystemBlocksDoc(doc, matcher)
	if obfuscateMessagesDoc(doc, matcher) {
		modified = true
	}
	return modified
}

// ObfuscateSensitiveWordsInSystemInstruction obfuscates sensitive words in an Antigravity system instruction.
func ObfuscateSensitiveWordsInSystemInstruction(payload []byte, matcher *SensitiveWordMatcher) []byte {
	if !matcher.hasRules() || len(payload) == 0 {
		return payload
	}
	doc := ojson.NewDoc(payload)
	if !ObfuscateSensitiveWordsInSystemInstructionDoc(doc, matcher) {
		return payload
	}
	return doc.Bytes()
}

// ObfuscateSensitiveWordsInSystemInstructionDoc obfuscates sensitive words in an Antigravity system instruction in place on doc.
func ObfuscateSensitiveWordsInSystemInstructionDoc(doc *ojson.Doc, matcher *SensitiveWordMatcher) bool {
	if !matcher.hasRules() || doc == nil || doc.Len() == 0 {
		return false
	}

	modified := false
	for _, path := range []string{"request.systemInstruction", "request.system_instruction"} {
		instruction := doc.Get(path)
		if !instruction.Exists() {
			continue
		}
		if instruction.Type == ojson.String {
			text := instruction.String()
			if obfuscated := matcher.obfuscateText(text); obfuscated != text {
				if errSet := doc.Set(path, obfuscated); errSet == nil {
					modified = true
				}
			}
			continue
		}

		parts := instruction.Get("parts")
		if !parts.IsArray() {
			continue
		}
		parts.ForEach(func(key, part ojson.Result) bool {
			if part.Get("text").Type != ojson.String {
				return true
			}
			text := part.Get("text").String()
			if obfuscated := matcher.obfuscateText(text); obfuscated != text {
				if errSet := doc.Set(path+".parts."+key.String()+".text", obfuscated); errSet == nil {
					modified = true
				}
			}
			return true
		})
	}

	return modified
}

func obfuscateSystemBlocksDoc(doc *ojson.Doc, matcher *SensitiveWordMatcher) bool {
	system := doc.Get("system")
	if !system.Exists() {
		return false
	}

	modified := false
	if system.IsArray() {
		system.ForEach(func(key, value ojson.Result) bool {
			if value.Get("type").String() == "text" {
				text := value.Get("text").String()
				if strings.HasPrefix(text, "x-anthropic-billing-header:") {
					return true
				}
				obfuscated := matcher.obfuscateText(text)
				if obfuscated != text {
					path := "system." + key.String() + ".text"
					if errSet := doc.Set(path, obfuscated); errSet == nil {
						modified = true
					}
				}
			}
			return true
		})
	} else if system.Type == ojson.String {
		text := system.String()
		if !strings.HasPrefix(text, "x-anthropic-billing-header:") {
			obfuscated := matcher.obfuscateText(text)
			if obfuscated != text {
				if errSet := doc.Set("system", obfuscated); errSet == nil {
					modified = true
				}
			}
		}
	}

	return modified
}

func obfuscateMessagesDoc(doc *ojson.Doc, matcher *SensitiveWordMatcher) bool {
	messages := doc.Get("messages")
	if !messages.Exists() || !messages.IsArray() {
		return false
	}

	modified := false
	messages.ForEach(func(msgKey, msg ojson.Result) bool {
		content := msg.Get("content")
		if !content.Exists() {
			return true
		}

		msgPath := "messages." + msgKey.String()

		if content.Type == ojson.String {
			text := content.String()
			obfuscated := matcher.obfuscateText(text)
			if obfuscated != text {
				if errSet := doc.Set(msgPath+".content", obfuscated); errSet == nil {
					modified = true
				}
			}
		} else if content.IsArray() {
			content.ForEach(func(blockKey, block ojson.Result) bool {
				if block.Get("type").String() == "text" {
					text := block.Get("text").String()
					obfuscated := matcher.obfuscateText(text)
					if obfuscated != text {
						path := msgPath + ".content." + blockKey.String() + ".text"
						if errSet := doc.Set(path, obfuscated); errSet == nil {
							modified = true
						}
					}
				}
				return true
			})
		}

		return true
	})

	return modified
}
