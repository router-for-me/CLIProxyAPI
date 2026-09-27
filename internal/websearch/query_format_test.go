package websearch

import (
	"strings"
	"testing"
)

func phraseTerms(parsed ParsedQuery) []string {
	var out []string
	for _, term := range parsed.Terms {
		if term.Phrase {
			out = append(out, term.Text)
		}
	}
	return out
}

func negatedTerms(parsed ParsedQuery) []string {
	var out []string
	for _, term := range parsed.Terms {
		if term.Negated {
			out = append(out, term.Text)
		}
	}
	return out
}

func TestParseSearchQueryDirectiveFamilies(t *testing.T) {
	parsed := ParseSearchQuery(
		`golang site:go.dev -site:blog.example.com domain:docs.example.com host:alt.example.com ` +
			`after:2024-01-01 before:2025-01-01 since:2023-01-01 until:2026-01-01 ` +
			`inurl:doc url:path intitle:release title:notes intext:body inbody:more inanchor:here ` +
			`filetype:pdf ext:html -filetype:zip -ext:rar lang:en language:de "exact phrase" -dropme +verbatim`)

	if len(parsed.Sites) != 3 {
		t.Fatalf("sites = %v, want site/domain/host merged", parsed.Sites)
	}
	if len(parsed.ExcludedSites) != 1 || parsed.ExcludedSites[0] != "blog.example.com" {
		t.Fatalf("excluded sites = %v", parsed.ExcludedSites)
	}
	// OMP assigns the bound unconditionally, so a later since:/until:
	// overwrites the earlier after:/before: rather than being ignored.
	if parsed.After != "2023-01-01" || parsed.Before != "2026-01-01" {
		t.Fatalf("after/before = %q/%q, want the last bound to win", parsed.After, parsed.Before)
	}
	if len(parsed.InURL) != 2 {
		t.Fatalf("inurl = %v, want inurl/url merged", parsed.InURL)
	}
	if len(parsed.InTitle) != 2 {
		t.Fatalf("intitle = %v, want intitle/title merged", parsed.InTitle)
	}
	if len(parsed.InText) != 3 {
		t.Fatalf("intext = %v, want intext/inbody/inanchor merged", parsed.InText)
	}
	if len(parsed.FileTypes) != 2 || len(parsed.ExcludedFileTypes) != 2 {
		t.Fatalf("filetypes = %v / %v", parsed.FileTypes, parsed.ExcludedFileTypes)
	}
	// Like the date bounds, OMP assigns lang unconditionally, so the
	// last language directive wins.
	if parsed.Lang != "de" {
		t.Fatalf("lang = %q, want the last language directive to win", parsed.Lang)
	}
	// `+term` is the verbatim-required prefix: like a quoted phrase it must
	// be re-emitted inside quotes, so both land in the phrase set.
	if got := phraseTerms(parsed); len(got) != 2 ||
		got[0] != "exact phrase" || got[1] != "verbatim" {
		t.Fatalf("phrases = %v, want the quoted phrase and the +verbatim term", got)
	}
	if got := negatedTerms(parsed); len(got) != 1 || got[0] != "dropme" {
		t.Fatalf("negated = %v, want only -dropme; +verbatim is a phrase, not a negation", got)
	}
	if !parsed.HasDirectives || !parsed.HasConstraints {
		t.Fatal("expected both HasDirectives and HasConstraints")
	}
}

func TestParseSearchQuerySpaceAfterColonAdoptsNextToken(t *testing.T) {
	parsed := ParseSearchQuery("docs site: example.com")
	if len(parsed.Sites) != 1 || parsed.Sites[0] != "example.com" {
		t.Fatalf("sites = %v, want the next token adopted", parsed.Sites)
	}
}

func TestParseSearchQueryQuotedDirectiveValueDropsQuotes(t *testing.T) {
	parsed := ParseSearchQuery(`intitle:"budget tips"`)
	if len(parsed.InTitle) != 1 || parsed.InTitle[0] != "budget tips" {
		t.Fatalf("intitle = %#v, want unquoted value", parsed.InTitle)
	}
}

func TestParseSearchQueryKeepsUnknownNameValueVerbatim(t *testing.T) {
	parsed := ParseSearchQuery("TS2345: mismatch C:\\path\\file")
	if !strings.Contains(parsed.Text, "TS2345:") || !strings.Contains(parsed.Text, "C:") {
		t.Fatalf("text = %q, unknown name:value must stay verbatim", parsed.Text)
	}
}

func TestParseSearchQueryUnparsableDateDegradesToTerm(t *testing.T) {
	parsed := ParseSearchQuery("release before:someday")
	if parsed.Before != "" {
		t.Fatalf("before = %q, want empty for unparsable date", parsed.Before)
	}
	kept := false
	for _, term := range parsed.Terms {
		if term.Text == "before:someday" {
			kept = true
		}
	}
	if !kept {
		t.Fatalf("terms = %+v, want before:someday kept as a plain term", parsed.Terms)
	}
	if parsed.HasConstraints {
		t.Fatal("an uninterpretable date must not register as a constraint")
	}
}

func TestParseSearchQueryAllInModes(t *testing.T) {
	// Google semantics: allintitle: requires every following term in the
	// title, so the directive captures the inline value plus each
	// subsequent plain term.
	parsed := ParseSearchQuery("allintitle:foo bar baz")
	if len(parsed.InTitle) != 3 {
		t.Fatalf("intitle = %v, want the inline value and both following terms", parsed.InTitle)
	}
	if strings.TrimSpace(parsed.Text) != "" {
		t.Fatalf("text = %q, captured terms must leave the free text empty", parsed.Text)
	}
	// A directive token ends capture mode rather than being swallowed.
	mixed := ParseSearchQuery("allintitle:foo site:go.dev bar")
	if len(mixed.InTitle) != 1 || mixed.InTitle[0] != "foo" {
		t.Fatalf("intitle = %v, want only the inline value before the next directive", mixed.InTitle)
	}
	if len(mixed.Sites) != 1 {
		t.Fatalf("sites = %v, want go.dev", mixed.Sites)
	}
}

func TestParseSearchQueryOrGroup(t *testing.T) {
	parsed := ParseSearchQuery("(react OR vue) router")
	grouped := 0
	for _, term := range parsed.Terms {
		if term.Grouped {
			grouped++
		}
	}
	if grouped != 2 {
		t.Fatalf("grouped terms = %d, want react and vue", grouped)
	}
	if got := FormatQuery(parsed, GoogleQuerySyntax); !strings.Contains(got, "(react OR vue)") {
		t.Fatalf("formatted = %q, want the OR group preserved", got)
	}
	// An engine without OR support flattens the group to plain keywords.
	if got := FormatQuery(parsed, QuerySyntax{Phrases: true}); strings.Contains(got, "OR") {
		t.Fatalf("formatted = %q, want OR dropped for a non-OR engine", got)
	}
}

func TestParseSearchQueryBooleanTokensAreNotSearchTerms(t *testing.T) {
	parsed := ParseSearchQuery("java NOT kotlin")
	if strings.Contains(parsed.Text, "NOT") {
		t.Fatalf("text = %q, NOT must be consumed as an operator", parsed.Text)
	}
}

func TestNormalizeSite(t *testing.T) {
	cases := map[string]string{
		"https://go.dev/":       "go.dev",
		"*.Go.Dev":              "go.dev",
		"  GITHUB.com/ ":        "github.com",
		"github.com/anthropics": "github.com/anthropics",
	}
	for input, want := range cases {
		if got := normalizeSite(input); got != want {
			t.Fatalf("normalizeSite(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestFormatQueryDropsUnsupportedOperators(t *testing.T) {
	parsed := ParseSearchQuery("release notes site:go.dev -site:spam.example filetype:pdf intitle:news")
	got := FormatQuery(parsed, QuerySyntax{Phrases: true})
	if strings.Contains(got, "site:") || strings.Contains(got, "filetype:") {
		t.Fatalf("formatted = %q, want no operators for a plain engine", got)
	}
	if got != "release notes" {
		t.Fatalf("formatted = %q, want just the free text", got)
	}
}

func TestFormatQueryDirectivesOnlyFallsBackToKeywords(t *testing.T) {
	parsed := ParseSearchQuery("site:go.dev")
	got := FormatQuery(parsed, QuerySyntax{})
	if got != "go.dev" {
		t.Fatalf("formatted = %q, want the constraint value as a keyword", got)
	}
}

func TestFormatQueryGroupsMultipleFiletypes(t *testing.T) {
	parsed := ParseSearchQuery("docs filetype:pdf filetype:epub")
	got := FormatQuery(parsed, GoogleQuerySyntax)
	if !strings.Contains(got, "(filetype:pdf OR filetype:epub)") {
		t.Fatalf("formatted = %q, want an OR group", got)
	}
}

func TestFormatScraperQueryDemotesPathSitesAndInURL(t *testing.T) {
	parsed := ParseSearchQuery("claude api site:github.com/anthropics inurl:docs")
	got := FormatScraperQuery("claude api site:github.com/anthropics inurl:docs", parsed, GoogleQuerySyntax)
	if strings.Contains(got, "site:github.com/anthropics") {
		t.Fatalf("formatted = %q, path-carrying site must be demoted", got)
	}
	if strings.Contains(got, "inurl:") {
		t.Fatalf("formatted = %q, inurl must be demoted for scrapers", got)
	}
	// The demoted values survive as plain keywords so the engine searches
	// something rather than nothing.
	if !strings.Contains(got, "github.com/anthropics") || !strings.Contains(got, "docs") {
		t.Fatalf("formatted = %q, want demoted values as keywords", got)
	}
	// A bare-domain site filter is kept.
	if kept := FormatScraperQuery("go site:go.dev", ParseSearchQuery("go site:go.dev"), GoogleQuerySyntax); !strings.Contains(kept, "site:go.dev") {
		t.Fatalf("formatted = %q, want the bare-domain filter kept", kept)
	}
}

func TestFormatScraperQueryPassesDirectiveFreeQueryThrough(t *testing.T) {
	raw := "plain query with no operators"
	if got := FormatScraperQuery(raw, ParseSearchQuery(raw), GoogleQuerySyntax); got != raw {
		t.Fatalf("formatted = %q, want byte-identical passthrough", got)
	}
}

func TestFormatScraperQueryKeepsNegatedSite(t *testing.T) {
	parsed := ParseSearchQuery("go -site:spam.example")
	got := FormatScraperQuery("go -site:spam.example", parsed, GoogleQuerySyntax)
	if !strings.Contains(got, "-site:spam.example") {
		t.Fatalf("formatted = %q, demoting a negation would invert it", got)
	}
}
