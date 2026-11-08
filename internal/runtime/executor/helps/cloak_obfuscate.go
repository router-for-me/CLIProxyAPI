package helps

import (
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/MoeForge/moego/ojson"
)

// zeroWidthSpace is the Unicode zero-width space character used for obfuscation.
const zeroWidthSpace = "\u200B"

// SensitiveWordMatcher holds the compiled regex for matching sensitive words.
type SensitiveWordMatcher struct {
	regex *regexp.Regexp
}

// BuildSensitiveWordMatcher compiles a regex from the word list.
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

	return &SensitiveWordMatcher{regex: re}
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

// obfuscateText replaces all sensitive words in the text.
func (m *SensitiveWordMatcher) obfuscateText(text string) string {
	if m == nil || m.regex == nil {
		return text
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
