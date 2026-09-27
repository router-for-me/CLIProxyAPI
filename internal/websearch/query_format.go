package websearch

import (
	"regexp"
	"strings"
)

// QuerySyntax declares which operators a target engine actually parses.
// Everything defaults to false, so the zero value yields plain keywords
// suitable for natural-language APIs.
type QuerySyntax struct {
	Phrases   bool // re-emit "quoted phrases"
	Negation  bool // re-emit -term exclusions
	Or        bool // re-emit OR groups
	Site      bool // re-emit site:/-site:
	InURL     bool // re-emit inurl:/-inurl:
	InTitle   bool // re-emit intitle:/-intitle:
	InText    bool // re-emit intext:/-intext:
	FileType  bool // re-emit filetype:/-filetype:
	DateRange bool // re-emit after:/before:
}

// GoogleQuerySyntax is the full operator set understood by Google-family
// engines (Google, Startpage, Ecosia, Brave, Kagi, SearXNG).
var GoogleQuerySyntax = QuerySyntax{
	Phrases:   true,
	Negation:  true,
	Or:        true,
	Site:      true,
	InURL:     true,
	InTitle:   true,
	InText:    true,
	FileType:  true,
	DateRange: true,
}

// QueryTerm is one free-text token: everything that is not a recognized
// directive.
type QueryTerm struct {
	Text    string
	Phrase  bool // quoted exact phrase, or verbatim-required +term
	Negated bool // -term or NOT term
	Group   int  // OR-group id; -1 when the term is ungrouped
	// Grouped reports whether Group carries a real group id, so a zero
	// value is not mistaken for group 0.
	Grouped bool
}

const ungroupedTerm = -1

// directivePattern matches `name:`, `-name:` and `+name:` tokens.
var directivePattern = regexp.MustCompile(`^([+-]?)([a-z][a-z-]*):(.*)$`)

// directiveFields maps every recognized directive alias to its canonical
// field. Without this table, `domain:`, `host:`, `since:`, `lang:` and
// friends leak into the query as literal keywords.
var directiveFields = map[string]string{
	"site":     "site",
	"domain":   "site",
	"host":     "site",
	"inurl":    "inUrl",
	"url":      "inUrl",
	"intitle":  "inTitle",
	"title":    "inTitle",
	"intext":   "inText",
	"inbody":   "inText",
	"inanchor": "inText",
	"filetype": "filetype",
	"ext":      "filetype",
	"before":   "before",
	"until":    "before",
	"after":    "after",
	"since":    "after",
	"lang":     "lang",
	"language": "lang",
}

// allModes are the `allin*:` directives that capture every following plain
// term into one constraint field.
var allModes = map[string]string{
	"allintitle": "inTitle",
	"allinurl":   "inUrl",
	"allintext":  "inText",
}

// reservedTokens never act as the value of a preceding `name:`.
var reservedTokens = map[string]bool{
	"(": true, ")": true, "OR": true, "AND": true, "NOT": true,
	"|": true, "||": true, "&&": true, "!": true,
}

type rawToken struct {
	text        string
	quoted      bool
	quotedValue bool
}

// tokenize splits a query into whitespace-delimited tokens, honoring
// quoted spans and a quote that directly follows `name:` so
// `intitle:"budget tips"` yields one token.
func tokenize(raw string) []rawToken {
	var tokens []rawToken
	runes := []rune(raw)
	i := 0
	for i < len(runes) {
		ch := runes[i]
		if isWhitespaceRune(ch) {
			i++
			continue
		}
		if isQuoteRune(ch) {
			j := i + 1
			var buf strings.Builder
			for j < len(runes) && !isQuoteRune(runes[j]) {
				buf.WriteRune(runes[j])
				j++
			}
			if text := strings.TrimSpace(buf.String()); text != "" {
				tokens = append(tokens, rawToken{text: text, quoted: true})
			}
			i = j + 1
			continue
		}
		var buf strings.Builder
		quotedValue := false
		for i < len(runes) && !isWhitespaceRune(runes[i]) {
			c := runes[i]
			if isQuoteRune(c) {
				if strings.HasSuffix(buf.String(), ":") {
					j := i + 1
					for j < len(runes) && !isQuoteRune(runes[j]) {
						buf.WriteRune(runes[j])
						j++
					}
					quotedValue = true
					i = j + 1
					continue
				}
				// `foo"bar` ends the word; the quote opens a phrase.
				break
			}
			buf.WriteRune(c)
			i++
		}
		if text := buf.String(); text != "" {
			tokens = append(tokens, rawToken{text: text, quotedValue: quotedValue})
		}
	}
	return splitParens(tokens)
}

// splitParens lifts leading `(` and unbalanced trailing `)` into their own
// tokens so `(react OR vue)` parses while
// `site:wikipedia.org/Foo_(bar)` stays one token.
func splitParens(tokens []rawToken) []rawToken {
	out := make([]rawToken, 0, len(tokens)+2)
	for _, token := range tokens {
		if token.quoted || token.quotedValue {
			out = append(out, token)
			continue
		}
		text := token.text
		for strings.HasPrefix(text, "(") {
			out = append(out, rawToken{text: "("})
			text = text[1:]
		}
		trailing := 0
		for strings.HasSuffix(text, ")") {
			// Only strip parens that do not close an opener inside the word,
			// so `Foo_(bar)` keeps its closing paren.
			body := text[:len(text)-1]
			depth := 0
			for _, c := range body {
				if c == '(' {
					depth++
				} else if c == ')' {
					depth--
				}
			}
			if depth > 0 {
				break
			}
			text = body
			trailing++
		}
		if text != "" {
			out = append(out, rawToken{text: text})
		}
		for range trailing {
			out = append(out, rawToken{text: ")"})
		}
	}
	return out
}

func isQuoteRune(r rune) bool { return r == '"' || r == '“' || r == '”' }

// isWhitespaceRune is Unicode-aware: agents paste NBSP and friends.
func isWhitespaceRune(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\v' || r == '\f' ||
		r == 0x00A0 || r == 0x2028 || r == 0x2029 || r == 0xFEFF
}

func unbalancedParen(text string) bool {
	open := strings.Count(text, "(")
	closeCount := strings.Count(text, ")")
	return closeCount > open
}

// isReservedToken reports whether text is an operator or a recognized
// directive, so a bare `name:` never adopts it as a value.
func isReservedToken(text string) bool {
	if reservedTokens[text] {
		return true
	}
	match := directivePattern.FindStringSubmatch(text)
	if match == nil {
		return false
	}
	name := strings.ToLower(match[2])
	_, isDirective := directiveFields[name]
	if isDirective {
		return true
	}
	_, isAll := allModes[name]
	return isAll
}

// normalizeSite lowercases a site value and strips the scheme, a leading
// `*.` wildcard, and trailing separators. A path is preserved because
// matchesSite honors it.
func normalizeSite(value string) string {
	site := strings.ToLower(strings.TrimSpace(value))
	if index := strings.Index(site, "://"); index >= 0 && index <= 8 {
		scheme := site[:index]
		if !strings.ContainsAny(scheme, " \t") && isAlphaWord(scheme) {
			site = site[index+3:]
		}
	}
	site = strings.TrimPrefix(site, "*.")
	site = strings.TrimRight(site, "/.")
	return site
}

func isAlphaWord(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '+' || c == '-' || c == '.' {
			continue
		}
		return false
	}
	return s != ""
}

// quoteValue quotes a directive value that contains whitespace.
func quoteValue(value string) string {
	for _, r := range value {
		if isWhitespaceRune(r) {
			return `"` + value + `"`
		}
	}
	return value
}

// renderTerm renders one free-text term per the target syntax.
func renderTerm(term QueryTerm, syntax QuerySyntax) string {
	if term.Negated && !syntax.Negation {
		return ""
	}
	body := term.Text
	if term.Phrase && syntax.Phrases {
		body = `"` + term.Text + `"`
	}
	if term.Negated {
		return "-" + body
	}
	return body
}

// renderTerms renders the free-text terms, collapsing OR groups when the
// engine understands them and flattening them to keywords when it does not.
func renderTerms(terms []QueryTerm, syntax QuerySyntax) string {
	var parts []string
	for i := 0; i < len(terms); i++ {
		term := terms[i]
		if term.Grouped && syntax.Or {
			members := make([]string, 0, 3)
			j := i
			for ; j < len(terms) && terms[j].Grouped && terms[j].Group == term.Group; j++ {
				if rendered := renderTerm(terms[j], syntax); rendered != "" {
					members = append(members, rendered)
				}
			}
			i = j - 1
			switch len(members) {
			case 0:
			case 1:
				parts = append(parts, members[0])
			default:
				parts = append(parts, "("+strings.Join(members, " OR ")+")")
			}
			continue
		}
		if rendered := renderTerm(term, syntax); rendered != "" {
			parts = append(parts, rendered)
		}
	}
	return strings.Join(parts, " ")
}

// FormatQuery rebuilds a query string for an engine with the given syntax.
// Constraints the engine lacks are omitted; the caller maps them onto API
// parameters or relies on ApplyQueryConstraints. Never returns an empty
// string for a non-empty input.
func FormatQuery(parsed ParsedQuery, syntax QuerySyntax) string {
	var parts []string
	if text := renderTerms(parsed.Terms, syntax); text != "" {
		parts = append(parts, text)
	}
	if syntax.Site {
		if len(parsed.Sites) > 1 && syntax.Or {
			alternatives := make([]string, 0, len(parsed.Sites))
			for _, site := range parsed.Sites {
				alternatives = append(alternatives, "site:"+site)
			}
			parts = append(parts, "("+strings.Join(alternatives, " OR ")+")")
		} else {
			for _, site := range parsed.Sites {
				parts = append(parts, "site:"+site)
			}
		}
		for _, site := range parsed.ExcludedSites {
			parts = append(parts, "-site:"+site)
		}
	}
	if syntax.InURL {
		for _, value := range parsed.InURL {
			parts = append(parts, "inurl:"+quoteValue(value))
		}
		for _, value := range parsed.ExcludedInURL {
			parts = append(parts, "-inurl:"+quoteValue(value))
		}
	}
	if syntax.InTitle {
		for _, value := range parsed.InTitle {
			parts = append(parts, "intitle:"+quoteValue(value))
		}
		for _, value := range parsed.ExcludedInTitle {
			parts = append(parts, "-intitle:"+quoteValue(value))
		}
	}
	if syntax.InText {
		for _, value := range parsed.InText {
			parts = append(parts, "intext:"+quoteValue(value))
		}
		for _, value := range parsed.ExcludedInText {
			parts = append(parts, "-intext:"+quoteValue(value))
		}
	}
	if syntax.FileType {
		if len(parsed.FileTypes) > 1 && syntax.Or {
			alternatives := make([]string, 0, len(parsed.FileTypes))
			for _, ext := range parsed.FileTypes {
				alternatives = append(alternatives, "filetype:"+ext)
			}
			parts = append(parts, "("+strings.Join(alternatives, " OR ")+")")
		} else {
			for _, ext := range parsed.FileTypes {
				parts = append(parts, "filetype:"+ext)
			}
		}
		for _, ext := range parsed.ExcludedFileTypes {
			parts = append(parts, "-filetype:"+ext)
		}
	}
	if syntax.DateRange {
		if parsed.After != "" {
			parts = append(parts, "after:"+parsed.After)
		}
		if parsed.Before != "" {
			parts = append(parts, "before:"+parsed.Before)
		}
	}
	if result := strings.TrimSpace(strings.Join(parts, " ")); result != "" {
		return result
	}
	// A directives-only query against an engine with no directive syntax
	// still deserves a meaningful query, so fall back to the constraint
	// values as plain keywords.
	fallback := make([]string, 0, len(parsed.Sites)+len(parsed.InTitle)+len(parsed.InURL)+len(parsed.InText)+len(parsed.FileTypes))
	fallback = append(fallback, parsed.Sites...)
	fallback = append(fallback, parsed.InTitle...)
	fallback = append(fallback, parsed.InURL...)
	fallback = append(fallback, parsed.InText...)
	fallback = append(fallback, parsed.FileTypes...)
	if result := strings.TrimSpace(strings.Join(fallback, " ")); result != "" {
		return result
	}
	return strings.TrimSpace(parsed.Raw)
}

// FormatScraperQuery builds the engine query for a credential-free HTML
// engine. It canonicalizes directives for the engine, then demotes the
// operators that zero-match across the scraper set: engines match `site:`
// against a bare domain only, and DuckDuckGo ignores `inurl:` entirely. A
// path-carrying `site:` and every `inurl:` value become plain keywords, so
// the engine still searches *something*; ApplyQueryConstraints then enforces
// every demoted constraint on the returned sources. Negated forms pass
// through untouched, since demoting them would invert an exclusion into a
// search term.
func FormatScraperQuery(query string, parsed ParsedQuery, syntax QuerySyntax) string {
	if !parsed.HasDirectives {
		return query
	}
	demoted := make([]QueryTerm, 0, len(parsed.Sites)+len(parsed.InURL))
	for _, site := range parsed.Sites {
		if strings.Contains(site, "/") {
			demoted = append(demoted, QueryTerm{Text: site, Group: ungroupedTerm})
		}
	}
	for _, value := range parsed.InURL {
		demoted = append(demoted, QueryTerm{Text: value, Group: ungroupedTerm})
	}
	downgraded := parsed
	downgraded.Terms = append(append([]QueryTerm{}, parsed.Terms...), demoted...)
	sites := make([]string, 0, len(parsed.Sites))
	for _, site := range parsed.Sites {
		if !strings.Contains(site, "/") {
			sites = append(sites, site)
		}
	}
	downgraded.Sites = sites
	downgraded.InURL = nil
	return FormatQuery(downgraded, syntax)
}
