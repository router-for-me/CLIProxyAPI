package payloadcompress

import (
	"regexp"
	"strings"
)

// cavemanEngine compresses prose in user turns: filler, pleasantries, hedging,
// redundant framing. It is lossy by design, so it is off unless a combo names it,
// only touches user text, and leaves code, URLs and fenced blocks byte-identical.
type cavemanEngine struct {
	level string
}

func (cavemanEngine) name() string { return EngineCaveman }

const cavemanMinBytes = 64

func (e cavemanEngine) compressKind(kind segmentKind, text string) (string, bool) {
	if kind != kindUser || len(text) < cavemanMinBytes {
		return text, false
	}
	if isCodeDominant(text) {
		return text, false
	}
	if e.level == "" {
		e.level = "lite"
	}
	protected, spans := protectSpans(text)
	out := protected
	for _, rule := range cavemanRules {
		if levelRank(rule.level) > levelRank(e.level) {
			continue
		}
		out = rule.apply(out)
	}
	out = recapitalize(cleanupArtifacts(out))
	if out == text || len(out) >= len(text) {
		return text, false
	}
	return restoreSpans(out, spans), true
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

// cavemanRule is one rewrite. Exactly one of repl, table or lowerOnly is used.
type cavemanRule struct {
	name  string
	level string
	re    *regexp.Regexp
	repl  string
	table map[string]string
	// lowerOnly deletes a match only when the next byte is a lowercase letter.
	// Go's RE2 has no lookahead, so that condition is checked here.
	lowerOnly bool
}

func (r cavemanRule) apply(text string) string {
	switch {
	case r.lowerOnly:
		return deleteBeforeLower(text, r.re)
	case r.table != nil:
		return r.re.ReplaceAllStringFunc(text, func(match string) string {
			key := strings.ToLower(strings.TrimRight(match, " \t\n"))
			if repl, ok := r.table[key]; ok {
				return repl
			}
			return match
		})
	default:
		return r.re.ReplaceAllString(text, r.repl)
	}
}

// deleteBeforeLower removes matches followed by a lowercase letter, which is what
// articles, leading helpers and emphasis amplifiers need.
func deleteBeforeLower(text string, re *regexp.Regexp) string {
	locs := re.FindAllStringIndex(text, -1)
	if len(locs) == 0 {
		return text
	}
	var b strings.Builder
	b.Grow(len(text))
	prev := 0
	for _, loc := range locs {
		if loc[1] >= len(text) {
			continue
		}
		c := text[loc[1]]
		if c < 'a' || c > 'z' {
			continue
		}
		b.WriteString(text[prev:loc[0]])
		prev = loc[1]
	}
	b.WriteString(text[prev:])
	return b.String()
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
	lines := strings.Split(text, "\n")
	total, code := 0, 0
	for _, line := range lines {
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

// protectSpans hides code, inline code and URLs behind placeholders so no rule can
// rewrite them. Restoring is byte-exact.
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

	for i := 0; i < len(text); {
		switch {
		case strings.HasPrefix(text[i:], "```") || strings.HasPrefix(text[i:], "~~~"):
			fence := text[i : i+3]
			end := strings.Index(text[i+3:], fence)
			if end < 0 {
				b.WriteString(text[i:])
				i = len(text)
				continue
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
			i++
		}
	}
	return b.String(), spans
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
		raw := text[i+1 : i+1+end]
		index := 0
		for j := 0; j < len(raw); j++ {
			if raw[j] < '0' || raw[j] > '9' {
				index = -1
				break
			}
			index = index*10 + int(raw[j]-'0')
		}
		if index < 0 || index >= len(spans) {
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

// cavemanRules is the port of OmniRoute's English rule pack, in its original
// order. redundant_phrasing runs at lite rather than full: it consumes "make sure
// to" before the pleasantries rule can strip "sure" and leave "make to".
var cavemanRules = []cavemanRule{
	{name: "redundant_phrasing", level: "lite", re: regexp.MustCompile(`(?i)\b(?:make sure to|be sure to|due to the fact that|the reason is because|it is important to|you should|remember to)\b\s*`), table: map[string]string{
		"make sure to": "ensure", "be sure to": "ensure", "due to the fact that": "because",
		"the reason is because": "because", "it is important to": "", "you should": "", "remember to": "",
	}},
	{name: "pleasantries", level: "lite", re: regexp.MustCompile(`(?i)\b(?:i'?d be happy to|i would be happy to|i'?d be glad to|i would be glad to|glad to help|happy to|thank you|thanks|no problem|you'?re welcome|absolutely|certainly|of course|sure)\b\s*`), repl: ""},
	{name: "polite_framing", level: "lite", re: regexp.MustCompile(`(?i)\b(?:please|kindly|could you please|would you please|can you please|i would like you to|i want you to|i need you to)\b\s*`), repl: ""},
	{name: "hedging", level: "lite", re: regexp.MustCompile(`(?i)\b(?:it seems like|it appears that|i think that|i believe that|probably|possibly|maybe it)\b\s*`), repl: ""},
	{name: "verbose_instructions", level: "lite", re: regexp.MustCompile(`(?i)\b(?:provide a detailed explanation of|give me a comprehensive explanation of|write an in-depth explanation of|create a thorough explanation of|provide a detailed|give me a comprehensive|write an in-depth|create a thorough|explain in detail)\b\s*`), table: map[string]string{
		"provide a detailed explanation of":      "explain",
		"give me a comprehensive explanation of": "explain",
		"write an in-depth explanation of":       "explain",
		"create a thorough explanation of":       "explain",
		"provide a detailed":                     "provide",
		"give me a comprehensive":                "give",
		"write an in-depth":                      "write",
		"create a thorough":                      "create",
		"explain in detail":                      "explain",
	}},
	{name: "filler_adverbs", level: "lite", re: regexp.MustCompile(`(?i)\b(?:basically|essentially|actually|literally|simply|currently)\b\s*`), repl: ""},
	{name: "filler_phrases", level: "lite", re: regexp.MustCompile(`(?im)^(?:i want to|i need to|i'd like to|i'm looking for)\b\s*`), repl: ""},
	{name: "redundant_openers", level: "lite", re: regexp.MustCompile(`(?im)^(?:hi there|hello|good morning|hey)[,.!?\s]*`), repl: ""},
	{name: "verbose_requests", level: "lite", re: regexp.MustCompile(`(?i)\b(?:i was wondering if you could|would it be possible to)\b\s*`), repl: ""},
	{name: "self_reference", level: "lite", re: regexp.MustCompile(`(?im)^(?:i am trying to|i am working on|i have been)\b\s*`), repl: ""},
	{name: "excessive_gratitude", level: "lite", re: regexp.MustCompile(`(?i)\b(?:thank you so much|thanks in advance|i really appreciate)\b[,.!?\s]*`), repl: ""},
	{name: "qualifier_removal", level: "lite", re: regexp.MustCompile(`(?i)\b(?:a bit|a little|somewhat|kind of|sort of)\b\s*`), repl: ""},
	{name: "explanatory_prefix", level: "lite", re: regexp.MustCompile(`(?i)\b(?:the function appears to be handling|the code seems to|the class is|this module is)\b`), table: map[string]string{
		"the function appears to be handling": "Function:", "the code seems to": "Code:",
		"the class is": "Class:", "this module is": "Module:",
	}},
	{name: "question_to_directive", level: "lite", re: regexp.MustCompile(`(?i)\b(?:can you explain why|could you show me how|would you tell me|can you tell me)\b\s*`), table: map[string]string{
		"can you explain why": "Explain why", "could you show me how": "Show how",
		"would you tell me": "Tell me", "can you tell me": "Tell me",
	}},
	{name: "context_setup", level: "lite", re: regexp.MustCompile(`(?i)\b(?:i have the following code|here is my code|below is the code)\b\s*[:.]?\s*`), repl: "Code: "},
	{name: "intent_clarification", level: "lite", re: regexp.MustCompile(`(?i)\b(?:what i'm trying to do is|my objective is to|what i need is|i'm aiming to)\b\s*`), repl: "Goal: "},
	{name: "background_removal", level: "lite", re: regexp.MustCompile(`(?i)\b(?:as you may know,?\s*|as we discussed earlier,?\s*)`), repl: ""},
	{name: "meta_commentary", level: "lite", re: regexp.MustCompile(`(?im)^(?:note that|keep in mind that|remember that)\b\s*`), repl: ""},
	{name: "purpose_statement", level: "lite", re: regexp.MustCompile(`(?i)\b(?:for the purpose of|with the goal of|in an effort to|for every)\b`), table: map[string]string{
		"for the purpose of": "for", "with the goal of": "to", "in an effort to": "to", "for every": "per",
	}},
	{name: "purpose_phrases", level: "lite", re: regexp.MustCompile(`(?i)\b(?:in order to|so as to)\b\s*`), repl: "to "},
	{name: "verbose_connectors", level: "lite", re: regexp.MustCompile(`(?i)\b(?:furthermore|additionally|moreover|in addition)\b\s*`), repl: "also "},
	{name: "transition_removal", level: "lite", re: regexp.MustCompile(`(?im)^(?:on the other hand,?\s*|in contrast,?\s*|however,?\s*)`), repl: ""},
	{name: "emphasis_removal", level: "lite", re: regexp.MustCompile(`(?i)\b(?:very|really|extremely|highly|quite)[ \t]+`), lowerOnly: true},
	{name: "repeated_context", level: "lite", re: regexp.MustCompile(`(?i)\b(?:as we discussed earlier|as mentioned before|as previously stated|as i said before)\b[,.]?\s*`), repl: "See above. "},
	{name: "repeated_question", level: "lite", re: regexp.MustCompile(`(?i)\b(?:same question as before|i asked this earlier|this is the same question)\b[,.]?\s*`), repl: "[same question] "},
	{name: "reestablished_context", level: "lite", re: regexp.MustCompile(`(?i)\b(?:going back to the code above|referring back to|returning to)\b\s*`), repl: "Re: "},
	{name: "articles", level: "full", re: regexp.MustCompile(`(?i)\b(?:an|a|the)[ \t]+`), lowerOnly: true},
	{name: "leader_phrases", level: "full", re: regexp.MustCompile(`(?im)^(?:i'll|i will|i can|i'd|let me|you can|we will|we can|let's)[ \t]+`), lowerOnly: true},
	{name: "compound_collapse", level: "full", re: regexp.MustCompile(`(?i)\band any potential\b`), repl: ""},
	{name: "list_conjunction", level: "full", re: regexp.MustCompile(`(?i),\s*and also\s+|,\s*as well as\s+`), repl: ", "},
	{name: "redundant_quantifiers", level: "full", re: regexp.MustCompile(`(?i)\b(?:each and every single|each and every|any and all)\b`), table: map[string]string{
		"each and every single": "each", "each and every": "each", "any and all": "all",
	}},
	{name: "passive_voice", level: "full", re: regexp.MustCompile(`(?i)\b(?:is being used|is being called|is being generated|was created|was generated|was implemented)\b`), table: map[string]string{
		"is being used": "uses", "is being called": "calls", "is being generated": "generated",
		"was created": "created", "was generated": "generated", "was implemented": "implemented",
	}},
	{name: "ultra_abbreviations", level: "ultra", re: regexp.MustCompile(`(?i)\b(?:database|configuration|function|request|response|implementation|authentication|authorization|application|dependency|dependencies)\b`), table: map[string]string{
		"database": "DB", "configuration": "config", "function": "fn", "request": "req",
		"response": "res", "implementation": "impl", "authentication": "auth",
		"authorization": "authz", "application": "app", "dependency": "dep", "dependencies": "deps",
	}},
}
