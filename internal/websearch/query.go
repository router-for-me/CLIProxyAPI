package websearch

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// ParsedQuery is a raw query decomposed into free text plus every
// recognized constraint. All list fields are always present (possibly
// empty) so consumers can map over them without nil checks.
type ParsedQuery struct {
	// Raw is the original query string, verbatim.
	Raw string
	// Text is the free-text remainder with all recognized directives
	// removed. Use FormatQuery for a never-empty engine query.
	Text string
	// Terms are the ordered free-text terms (phrases, exclusions, groups).
	Terms []QueryTerm
	// Sites are site:/domain:/host: includes — any-of. Lowercased, scheme
	// stripped, may carry a path (github.com/anthropics).
	Sites []string
	// ExcludedSites are -site: exclusions, normalized like Sites.
	ExcludedSites []string
	// InURL and ExcludedInURL are inurl:/url:/allinurl: substrings.
	InURL         []string
	ExcludedInURL []string
	// InTitle and ExcludedInTitle are intitle:/title:/allintitle: substrings.
	InTitle         []string
	ExcludedInTitle []string
	// InText and ExcludedInText are intext:/inbody:/inanchor: body substrings.
	// Not post-filterable because snippets are partial; query-building only.
	InText         []string
	ExcludedInText []string
	// FileTypes and ExcludedFileTypes are filetype:/ext: extensions, any-of.
	FileTypes         []string
	ExcludedFileTypes []string
	// After is the inclusive lower publish-date bound, ISO YYYY-MM-DD.
	After string
	// Before is the exclusive upper publish-date bound, ISO YYYY-MM-DD.
	Before string
	// Lang is the lowercased language code from lang:/language:.
	Lang string
	// HasDirectives is true when any directive or boolean operator was
	// recognized.
	HasDirectives bool
	// HasConstraints is true when any post-filterable constraint is set.
	HasConstraints bool
}

// ParseSearchQuery parses a raw query into a ParsedQuery. It is lenient by
// construction: unknown `name:value` tokens (URLs, Windows paths, jargon)
// stay in the free text verbatim, and a directive with an unparseable value
// (`before:someday`) degrades to a plain term instead of being dropped.
func ParseSearchQuery(raw string) ParsedQuery {
	parsed := ParsedQuery{Raw: raw}
	tokens := tokenize(raw)
	// allMode captures every following plain term into one field; negation
	// flips the next term; orPending links the term after OR to the one
	// before it, so `(a OR b)` and `a OR b` both group correctly.
	allMode := ""
	negateNext := false
	orPending := false
	lastWasTerm := false
	groupSeq := 0
	appendTerm := func(text string, phrase bool) {
		negated := negateNext
		negateNext = false
		if allMode != "" {
			parsed.assignDirective(allMode, text, negated)
			orPending = false
			lastWasTerm = false
			return
		}
		term := QueryTerm{Text: text, Phrase: phrase, Negated: negated, Group: ungroupedTerm}
		if orPending && lastWasTerm && len(parsed.Terms) > 0 {
			previous := &parsed.Terms[len(parsed.Terms)-1]
			if !previous.Grouped {
				groupSeq++
				previous.Group = groupSeq
				previous.Grouped = true
			}
			term.Group = previous.Group
			term.Grouped = true
		}
		orPending = false
		lastWasTerm = true
		parsed.Terms = append(parsed.Terms, term)
		parsed.Text += " " + text
	}
	// A directive whose value cannot be interpreted falls back to being a
	// plain term, so nothing the user typed is silently discarded.
	deferDirective := func(token rawToken, sign string) {
		negateNext = false
		orPending = false
		lastWasTerm = true
		term := QueryTerm{
			Text:    token.text,
			Phrase:  token.quoted || token.quotedValue,
			Negated: sign == "-",
			Group:   ungroupedTerm,
		}
		parsed.Terms = append(parsed.Terms, term)
		parsed.Text += " " + term.Text
	}
	for i := 0; i < len(tokens); i++ {
		token := tokens[i]
		if token.quoted {
			appendTerm(token.text, true)
			continue
		}
		switch token.text {
		case "(", ")":
			continue
		case "OR", "|", "||":
			orPending = true
			parsed.HasDirectives = true
			continue
		case "AND", "&&":
			parsed.HasDirectives = true
			continue
		case "NOT", "!":
			negateNext = true
			parsed.HasDirectives = true
			continue
		case "-", "+":
			// The tokenizer splits `-"exact phrase"` into `-` plus a
			// quoted token, so the sign carries over here.
			if token.text == "-" && i+1 < len(tokens) && tokens[i+1].quoted {
				negateNext = true
			}
			continue
		}
		match := directivePattern.FindStringSubmatch(token.text)
		name := ""
		if match != nil {
			name = strings.ToLower(match[2])
		}
		if match != nil {
			if target, isAll := allModes[name]; isAll {
				allMode = target
				parsed.HasDirectives = true
				if inline := strings.TrimSpace(match[3]); inline != "" {
					parsed.assignDirective(target, inline, match[1] == "-")
				}
				orPending = false
				lastWasTerm = false
				continue
			}
		}
		field, isDirective := directiveFields[name]
		if match == nil || !isDirective {
			if match != nil {
				// Unknown `name:value` — keep it verbatim, carrying any sign
				// that acted as a negation.
				deferDirective(token, match[1])
				continue
			}
			text := token.text
			phrase := false
			switch {
			case strings.HasPrefix(text, "-") && len(text) > 1:
				text, phrase = text[1:], false
				negateNext = true
			case strings.HasPrefix(text, "+") && len(text) > 1:
				text, phrase = text[1:], true
			}
			appendTerm(text, phrase)
			continue
		}
		// A recognized directive ends any `allin*:` capture, so a later
		// plain term is free text again.
		allMode = ""
		parsed.HasDirectives = true
		value := strings.TrimSpace(match[3])
		// A directive with no inline value adopts the next non-reserved
		// token, so `site: example.com` works.
		if value == "" && i+1 < len(tokens) && !isReservedToken(tokens[i+1].text) {
			i++
			value = tokens[i].text
		}
		negated := match[1] == "-"
		// Date bounds are resolved immediately: an uninterpretable value
		// degrades to a plain term carrying the whole original token, so
		// nothing the user typed is silently discarded.
		if field == "before" || field == "after" {
			parsed.HasDirectives = true
			iso := normalizeQueryDate(value)
			if iso == "" {
				appendTerm(token.text, false)
				continue
			}
			if field == "before" {
				parsed.Before = iso
			} else {
				parsed.After = iso
			}
			orPending = false
			lastWasTerm = false
			continue
		}
		if field == "lang" {
			parsed.Lang = strings.ToLower(value)
		} else if !parsed.assignDirective(field, value, negated) {
			deferDirective(token, match[1])
			continue
		}
		orPending = false
		lastWasTerm = false
	}
	parsed.Text = strings.TrimSpace(parsed.Text)
	parsed.HasConstraints = len(parsed.Sites) > 0 || len(parsed.ExcludedSites) > 0 ||
		len(parsed.InURL) > 0 || len(parsed.ExcludedInURL) > 0 ||
		len(parsed.InTitle) > 0 || len(parsed.ExcludedInTitle) > 0 ||
		len(parsed.FileTypes) > 0 || len(parsed.ExcludedFileTypes) > 0 ||
		parsed.After != "" || parsed.Before != ""
	return parsed
}

// assignDirective stores one directive value on the canonical field. It
// reports whether the value was usable, so an uninterpretable value can
// degrade to a plain term instead of vanishing.
func (p *ParsedQuery) assignDirective(field, value string, negated bool) bool {
	if strings.TrimSpace(value) == "" {
		return false
	}
	switch field {
	case "site":
		if negated {
			p.ExcludedSites = append(p.ExcludedSites, normalizeSite(value))
		} else {
			p.Sites = append(p.Sites, normalizeSite(value))
		}
	case "inUrl":
		if negated {
			p.ExcludedInURL = append(p.ExcludedInURL, value)
		} else {
			p.InURL = append(p.InURL, value)
		}
	case "inTitle":
		if negated {
			p.ExcludedInTitle = append(p.ExcludedInTitle, value)
		} else {
			p.InTitle = append(p.InTitle, value)
		}
	case "inText":
		if negated {
			p.ExcludedInText = append(p.ExcludedInText, value)
		} else {
			p.InText = append(p.InText, value)
		}
	case "filetype":
		ext := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(value), "."))
		if ext == "" {
			return false
		}
		if negated {
			p.ExcludedFileTypes = append(p.ExcludedFileTypes, ext)
		} else {
			p.FileTypes = append(p.FileTypes, ext)
		}
	case "after":
		if negated {
			return false
		}
		date := normalizeQueryDate(value)
		if date == "" {
			return false
		}
		p.After = date
	case "before":
		if negated {
			return false
		}
		date := normalizeQueryDate(value)
		if date == "" {
			return false
		}
		p.Before = date
	case "lang":
		if negated {
			return false
		}
		p.Lang = strings.ToLower(strings.TrimSpace(value))
	default:
		return false
	}
	return true
}

// assignAllMode appends a term captured by an `allin*:` directive.
func (p *ParsedQuery) assignAllMode(target, value string) {
	switch target {
	case "inTitle":
		p.InTitle = append(p.InTitle, value)
	case "inUrl":
		p.InURL = append(p.InURL, value)
	case "inText":
		p.InText = append(p.InText, value)
	}
}

// ApplyQueryConstraints leniently post-filters sources for constraints not
// guaranteed upstream. Any dimension that would eliminate every remaining
// result is relaxed with a leading note instead of emptying the set.
func ApplyQueryConstraints(sources []Source, parsed ParsedQuery) ([]Source, []string) {
	var notes []string
	remaining := sources
	apply := func(name string, match func(Source) bool) {
		if len(remaining) == 0 {
			return
		}
		kept := make([]Source, 0, len(remaining))
		for _, source := range remaining {
			if match(source) {
				kept = append(kept, source)
			}
		}
		if len(kept) == 0 {
			notes = append(notes, "Note: no results matched "+name+"; constraint relaxed.")
			return
		}
		remaining = kept
	}

	if len(parsed.Sites) > 0 {
		apply("site: "+strings.Join(parsed.Sites, ", "), func(source Source) bool {
			host := sourceHost(source.URL)
			for _, site := range parsed.Sites {
				if hostMatchesSite(host, site) {
					return true
				}
			}
			return false
		})
	}
	if len(parsed.ExcludedSites) > 0 {
		apply("-site: "+strings.Join(parsed.ExcludedSites, ", "), func(source Source) bool {
			host := sourceHost(source.URL)
			for _, site := range parsed.ExcludedSites {
				if hostMatchesSite(host, site) {
					return false
				}
			}
			return true
		})
	}
	if parsed.After != "" {
		if after, ok := parseQueryDate(parsed.After); ok {
			apply("after: "+parsed.After, func(source Source) bool {
				published, okParse := parsePublishedDate(source.Published)
				return !okParse || !published.Before(after)
			})
		}
	}
	if parsed.Before != "" {
		if before, ok := parseQueryDate(parsed.Before); ok {
			apply("before: "+parsed.Before, func(source Source) bool {
				published, okParse := parsePublishedDate(source.Published)
				return !okParse || !published.After(before)
			})
		}
	}
	if len(parsed.InURL) > 0 {
		apply("inurl: "+strings.Join(parsed.InURL, ", "), func(source Source) bool {
			lowered := strings.ToLower(source.URL)
			for _, part := range parsed.InURL {
				if !strings.Contains(lowered, strings.ToLower(part)) {
					return false
				}
			}
			return true
		})
	}
	if len(parsed.InTitle) > 0 {
		apply("intitle: "+strings.Join(parsed.InTitle, ", "), func(source Source) bool {
			lowered := strings.ToLower(source.Title)
			for _, part := range parsed.InTitle {
				if !strings.Contains(lowered, strings.ToLower(part)) {
					return false
				}
			}
			return true
		})
	}
	if len(parsed.FileTypes) > 0 {
		apply("filetype: "+strings.Join(parsed.FileTypes, ", "), func(source Source) bool {
			path := strings.ToLower(sourceURLPath(source.URL))
			for _, ext := range parsed.FileTypes {
				ext = strings.TrimPrefix(ext, ".")
				if strings.HasSuffix(path, "."+ext) {
					return true
				}
			}
			return false
		})
	}
	return remaining, notes
}

func sourceHost(rawURL string) string {
	parsed, errParse := url.Parse(rawURL)
	if errParse != nil {
		return ""
	}
	return strings.ToLower(strings.TrimPrefix(parsed.Hostname(), "www."))
}

func hostMatchesSite(host, site string) bool {
	site = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(site, "www.")))
	if site == "" || host == "" {
		return false
	}
	return host == site || strings.HasSuffix(host, "."+site)
}

func sourceURLPath(rawURL string) string {
	parsed, errParse := url.Parse(rawURL)
	if errParse != nil {
		return rawURL
	}
	if parsed.Path == "" {
		return "/"
	}
	return parsed.Path
}

var queryDateLayouts = []string{
	"2006-01-02",
	"2006/01/02",
	"2006-1-2",
	"01/02/2006",
	"02-01-2006",
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	time.RFC1123,
	time.RFC1123Z,
	"Mon, 02 Jan 2006 15:04:05 MST",
	"2006-01-02T15:04:05.000Z07:00",
}

// normalizeQueryDate renders a directive date value as an ISO `YYYY-MM-DD`
// bound. Year-month values resolve to the first of the month; a bare year
// resolves to January 1. An unparseable value returns empty, which the
// caller treats as a plain term.
func normalizeQueryDate(value string) string {
	if parsed, ok := parseQueryDate(value); ok {
		return parsed.Format("2006-01-02")
	}
	if year, ok := parseYear(strings.TrimSpace(value)); ok {
		return fmt.Sprintf("%04d-01-01", year)
	}
	return ""
}

func parseQueryDate(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	for _, layout := range queryDateLayouts {
		if parsed, errParse := time.Parse(layout, value); errParse == nil {
			return parsed, true
		}
	}
	if len(value) == 4 {
		if year, ok := parseYear(value); ok {
			return time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC), true
		}
	}
	return time.Time{}, false
}

func parsePublishedDate(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	if parsed, ok := parseQueryDate(value); ok {
		return parsed, true
	}
	// Relative ages such as "3 days ago" cannot be compared reliably;
	// treat them as unknown so date filters stay lenient.
	return time.Time{}, false
}

func parseYear(value string) (int, bool) {
	year := 0
	for i := range len(value) {
		if value[i] < '0' || value[i] > '9' {
			return 0, false
		}
		year = year*10 + int(value[i]-'0')
	}
	if year < 1900 || year > 2200 {
		return 0, false
	}
	return year, true
}
