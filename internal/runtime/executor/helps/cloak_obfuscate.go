package helps

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/MoeForge/moego/ojson"
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

// SensitiveWordMatcher holds the compiled regex and replacement state for matching sensitive words.
type SensitiveWordMatcher struct {
	regex             *regexp.Regexp
	replaceFn         func(string) string
	lookupReplacement func(string) (string, bool)
}

// SensitiveWordConfigCacheKey builds a deterministic, collision-free cache key
// for a (words, replacements) configuration pair.
func SensitiveWordConfigCacheKey(words []string, replacements map[string]string) string {
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
	return b.String()
}

// BuildSensitiveWordMatcher compiles a regex from the word list using U+200B.
// Words are sorted by length (longest first) for proper matching.
func BuildSensitiveWordMatcher(words []string) *SensitiveWordMatcher {
	return BuildSensitiveWordMatcherWithReplacements(words, nil)
}

// BuildSensitiveWordMatcherWithReplacements compiles a matcher combining literal
// replacements and U+200B sensitive-word obfuscation.
func BuildSensitiveWordMatcherWithReplacements(words []string, replacements map[string]string) *SensitiveWordMatcher {
	return buildSensitiveWordMatcher(words, replacements, obfuscateWord, zeroWidthSpace)
}

// BuildAntigravitySensitiveWordMatcher compiles a matcher for Antigravity system
// instructions combining literal replacements and per-subword U+034F obfuscation.
func BuildAntigravitySensitiveWordMatcher(words []string, replacements map[string]string) *SensitiveWordMatcher {
	return buildSensitiveWordMatcher(words, replacements, obfuscateAntigravityPhrase, combiningGraphemeJoiner)
}

func buildSensitiveWordMatcher(
	words []string,
	replacements map[string]string,
	obfuscateFn func(string) string,
	marker string,
) *SensitiveWordMatcher {
	if len(words) == 0 && len(replacements) == 0 {
		return nil
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

	if len(rules) == 0 {
		return nil
	}

	sortRulesLongestFirst(rules)
	sortRulesLongestFirst(replacementRules)

	escaped := make([]string, len(rules))
	for i, r := range rules {
		escaped[i] = regexp.QuoteMeta(r.target)
	}

	pattern := "(?i:" + strings.Join(escaped, "|") + ")"
	re, errCompile := regexp.Compile(pattern)
	if errCompile != nil {
		return nil
	}

	var lookupReplacement func(string) (string, bool)
	replaceFn := obfuscateFn
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

	return &SensitiveWordMatcher{
		regex:             re,
		replaceFn:         replaceFn,
		lookupReplacement: lookupReplacement,
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

// obfuscateText replaces or obfuscates all sensitive words in the text.
func (m *SensitiveWordMatcher) obfuscateText(text string) string {
	if m == nil || m.regex == nil {
		return text
	}
	if m.replaceFn != nil {
		return m.regex.ReplaceAllStringFunc(text, m.replaceFn)
	}
	return m.regex.ReplaceAllStringFunc(text, obfuscateWord)
}

// Matches reports whether the text contains any configured sensitive word or replacement target.
func (m *SensitiveWordMatcher) Matches(text string) bool {
	if m == nil || m.regex == nil {
		return false
	}
	return m.regex.MatchString(text)
}

// SanitizeDevinText applies configured replacements and Devin line-level
// sensitive-word filtering in a single non-cascading pass:
//   - matches handled by sensitive-words-replace emit their literal replacement
//     verbatim without re-scanning or triggering line deletion;
//   - lines containing an unreplaced sensitive-words match are dropped.
func (m *SensitiveWordMatcher) SanitizeDevinText(text string) string {
	if m == nil || m.regex == nil || text == "" {
		return text
	}
	indices := m.regex.FindAllStringIndex(text, -1)
	if len(indices) == 0 {
		return strings.TrimSpace(text)
	}

	var keptLines []string
	var currentLine strings.Builder
	dropCurrentLine := false

	flushLine := func() {
		if !dropCurrentLine {
			keptLines = append(keptLines, currentLine.String())
		}
		currentLine.Reset()
		dropCurrentLine = false
	}

	appendLiteral := func(seg string) {
		for {
			nl := strings.IndexByte(seg, '\n')
			if nl < 0 {
				currentLine.WriteString(seg)
				return
			}
			currentLine.WriteString(seg[:nl])
			flushLine()
			seg = seg[nl+1:]
		}
	}

	last := 0
	for _, pair := range indices {
		start, end := pair[0], pair[1]
		if start > last {
			appendLiteral(text[last:start])
		}
		matched := text[start:end]
		if m.lookupReplacement != nil {
			if repl, ok := m.lookupReplacement(matched); ok {
				currentLine.WriteString(repl)
				last = end
				continue
			}
		}
		dropCurrentLine = true
		for {
			nl := strings.IndexByte(matched, '\n')
			if nl < 0 {
				break
			}
			flushLine()
			dropCurrentLine = true
			matched = matched[nl+1:]
		}
		last = end
	}
	if last < len(text) {
		appendLiteral(text[last:])
	}
	flushLine()

	return strings.TrimSpace(strings.Join(keptLines, "\n"))
}

// ObfuscateText replaces or obfuscates all sensitive words in the text.
func (m *SensitiveWordMatcher) ObfuscateText(text string) string {
	return m.obfuscateText(text)
}

// ObfuscateSensitiveWords processes the payload and obfuscates sensitive words
// in system blocks and message content.
func ObfuscateSensitiveWords(payload []byte, matcher *SensitiveWordMatcher) []byte {
	if matcher == nil || matcher.regex == nil || len(payload) == 0 {
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
	if matcher == nil || matcher.regex == nil || doc == nil || doc.Len() == 0 {
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
	if matcher == nil || matcher.regex == nil || len(payload) == 0 {
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
	if matcher == nil || matcher.regex == nil || doc == nil || doc.Len() == 0 {
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
