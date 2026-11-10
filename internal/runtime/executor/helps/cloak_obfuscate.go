package helps

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/MoeForge/moego/ojson"
)

// zeroWidthSpace is the Unicode zero-width space character used for obfuscation.
const zeroWidthSpace = "\u200B"

// combiningGraphemeJoiner is the Unicode combining grapheme joiner (U+034F) used for Antigravity obfuscation.
const combiningGraphemeJoiner = "\u034F"

// SensitiveWordMatcher holds the compiled regex and replacement callback for matching sensitive words.
type SensitiveWordMatcher struct {
	regex     *regexp.Regexp
	replaceFn func(string) string
}

// BuildSensitiveWordMatcher compiles a regex from the word list using zero-width space (U+200B).
// Words are sorted by length (longest first) for proper matching.
func BuildSensitiveWordMatcher(words []string) *SensitiveWordMatcher {
	if len(words) == 0 {
		return nil
	}

	// Filter and normalize words
	var validWords []string
	for _, w := range words {
		w = strings.TrimSpace(w)
		if utf8.RuneCountInString(w) >= 2 && !strings.Contains(w, zeroWidthSpace) {
			validWords = append(validWords, w)
		}
	}

	if len(validWords) == 0 {
		return nil
	}

	// Sort by length (longest first) for proper matching
	sort.Slice(validWords, func(i, j int) bool {
		return len(validWords[i]) > len(validWords[j])
	})

	// Escape and join
	escaped := make([]string, len(validWords))
	for i, w := range validWords {
		escaped[i] = regexp.QuoteMeta(w)
	}

	pattern := "(?i)" + strings.Join(escaped, "|")
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil
	}

	return &SensitiveWordMatcher{regex: re, replaceFn: obfuscateWord}
}

type antigravitySensitiveRule struct {
	target        string
	lowerTarget   string
	replacement   string
	isReplacement bool
}

// BuildAntigravitySensitiveWordMatcher compiles a matcher for Antigravity system instructions.
// Plain words are obfuscated per whitespace-delimited subword using U+034F (Combining Grapheme Joiner).
// Entries containing " => " (or ending with " =>" for empty replacement) perform case-insensitive
// literal text replacement ("target => replacement").
// Rules are sorted by target length (longest first) so multi-word phrases match before shorter words.
func BuildAntigravitySensitiveWordMatcher(words []string) *SensitiveWordMatcher {
	if len(words) == 0 {
		return nil
	}

	var rules []antigravitySensitiveRule
	indexByLower := make(map[string]int, len(words))

	for _, raw := range words {
		if target, replacement, isRepl := parseSensitiveWordReplacement(raw); isRepl {
			if utf8.RuneCountInString(target) < 2 || containsObfuscationMarker(target) {
				continue
			}
			lower := strings.ToLower(target)
			rule := antigravitySensitiveRule{
				target:        target,
				lowerTarget:   lower,
				replacement:   replacement,
				isReplacement: true,
			}
			if idx, exists := indexByLower[lower]; exists {
				rules[idx] = rule
			} else {
				indexByLower[lower] = len(rules)
				rules = append(rules, rule)
			}
			continue
		}

		w := strings.TrimSpace(raw)
		if utf8.RuneCountInString(w) < 2 || containsObfuscationMarker(w) {
			continue
		}
		if obfuscateAntigravityPhrase(w) == w {
			continue
		}
		lower := strings.ToLower(w)
		if _, exists := indexByLower[lower]; exists {
			continue
		}
		indexByLower[lower] = len(rules)
		rules = append(rules, antigravitySensitiveRule{
			target:      w,
			lowerTarget: lower,
		})
	}

	if len(rules) == 0 {
		return nil
	}

	sort.SliceStable(rules, func(i, j int) bool {
		ri := utf8.RuneCountInString(rules[i].target)
		rj := utf8.RuneCountInString(rules[j].target)
		if ri != rj {
			return ri > rj
		}
		return len(rules[i].target) > len(rules[j].target)
	})

	escaped := make([]string, len(rules))
	var replacements map[string]string
	var replacementRules []antigravitySensitiveRule
	for i, r := range rules {
		escaped[i] = regexp.QuoteMeta(r.target)
		if r.isReplacement {
			if replacements == nil {
				replacements = make(map[string]string)
			}
			replacements[r.lowerTarget] = r.replacement
			replacementRules = append(replacementRules, r)
		}
	}

	pattern := "(?i)" + strings.Join(escaped, "|")
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil
	}

	replaceFn := obfuscateAntigravityPhrase
	if len(replacements) > 0 {
		replaceFn = func(matched string) string {
			if repl, ok := replacements[strings.ToLower(matched)]; ok {
				return repl
			}
			for _, r := range replacementRules {
				if strings.EqualFold(matched, r.target) {
					return r.replacement
				}
			}
			return obfuscateAntigravityPhrase(matched)
		}
	}

	return &SensitiveWordMatcher{regex: re, replaceFn: replaceFn}
}

func parseSensitiveWordReplacement(entry string) (target, replacement string, ok bool) {
	before, after, hasArrow := strings.Cut(entry, " => ")
	if !hasArrow {
		trimmed := strings.TrimSpace(entry)
		if strings.HasSuffix(trimmed, " =>") {
			before = strings.TrimSuffix(trimmed, " =>")
			after = ""
			hasArrow = true
		}
	}
	if !hasArrow {
		return "", "", false
	}
	return strings.TrimSpace(before), strings.TrimSpace(after), true
}

func containsObfuscationMarker(s string) bool {
	return strings.Contains(s, zeroWidthSpace) || strings.Contains(s, combiningGraphemeJoiner)
}

// obfuscateWord inserts a zero-width space after the first grapheme.
func obfuscateWord(word string) string {
	if strings.Contains(word, zeroWidthSpace) {
		return word
	}

	// Get first rune
	r, size := utf8.DecodeRuneInString(word)
	if r == utf8.RuneError || size >= len(word) {
		return word
	}

	return string(r) + zeroWidthSpace + word[size:]
}

// obfuscateSingleTokenCGJ inserts a combining grapheme joiner (U+034F) after the first rune
// when the token has at least 2 runes and contains neither U+200B nor U+034F.
func obfuscateSingleTokenCGJ(token string) string {
	if containsObfuscationMarker(token) {
		return token
	}

	r, size := utf8.DecodeRuneInString(token)
	if r == utf8.RuneError || size >= len(token) {
		return token
	}

	return string(r) + combiningGraphemeJoiner + token[size:]
}

// obfuscateAntigravityPhrase obfuscates each whitespace-delimited subword (>= 2 runes)
// within a matched phrase using U+034F while preserving original whitespace.
func obfuscateAntigravityPhrase(phrase string) string {
	if phrase == "" {
		return phrase
	}
	var b strings.Builder
	b.Grow(len(phrase) + len(combiningGraphemeJoiner)*2)
	tokenStart := -1
	for i, r := range phrase {
		if unicode.IsSpace(r) {
			if tokenStart >= 0 {
				b.WriteString(obfuscateSingleTokenCGJ(phrase[tokenStart:i]))
				tokenStart = -1
			}
			b.WriteRune(r)
		} else if tokenStart < 0 {
			tokenStart = i
		}
	}
	if tokenStart >= 0 {
		b.WriteString(obfuscateSingleTokenCGJ(phrase[tokenStart:]))
	}
	return b.String()
}

// obfuscateText replaces all sensitive words in the text.
func (m *SensitiveWordMatcher) obfuscateText(text string) string {
	if m == nil || m.regex == nil {
		return text
	}
	if m.replaceFn != nil {
		return m.regex.ReplaceAllStringFunc(text, m.replaceFn)
	}
	return m.regex.ReplaceAllStringFunc(text, obfuscateWord)
}

// Matches reports whether the text contains any of the configured sensitive words.
func (m *SensitiveWordMatcher) Matches(text string) bool {
	if m == nil || m.regex == nil {
		return false
	}
	return m.regex.MatchString(text)
}

// ObfuscateText replaces all sensitive words in the text.
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
