package websearch

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// Exa has no `snippet` field. The old reader looked for one, so a result
// carrying only `highlights` — the third choice in the upstream chain —
// silently lost its snippet entirely.
func TestExaReadsHighlightsWhenNoSummaryOrText(t *testing.T) {
	cfg := Config{ExaAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(*http.Request) (*http.Response, error) {
		return jsonResponse(200, `{"results":[
			{"title":"A","url":"https://a.example","highlights":["first hit","second hit"],"publishedDate":"2026-01-02"},
			{"url":"https://b.example","snippet":"this key does not exist upstream"}]}`), nil
	}})
	response, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "exa"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if len(response.Sources) != 2 {
		t.Fatalf("sources = %+v", response.Sources)
	}
	if got := response.Sources[0].Snippet; got != "first hit second hit" {
		t.Fatalf("snippet = %q, want the joined highlights", got)
	}
	if response.Sources[0].Published != "2026-01-02" {
		t.Fatalf("published = %q", response.Sources[0].Published)
	}
	// A result with only the invented `snippet` key has no real content.
	if got := response.Sources[1].Snippet; got != "" {
		t.Fatalf("snippet = %q, want empty for a result with no real content field", got)
	}
}

// The upstream chain is summary → text → highlights, in that order.
func TestExaSnippetPrefersSummaryThenTextThenHighlights(t *testing.T) {
	all := `{"summary":"S","text":"T","highlights":["H"]}`
	if got := exaSnippet(gjson.Parse(all)); got != "S" {
		t.Fatalf("snippet = %q, want the summary to win", got)
	}
	if got := exaSnippet(gjson.Parse(`{"text":"T","highlights":["H"]}`)); got != "T" {
		t.Fatalf("snippet = %q, want the text to win over highlights", got)
	}
	if got := exaSnippet(gjson.Parse(`{"highlights":["H1","H2"]}`)); got != "H1 H2" {
		t.Fatalf("snippet = %q, want joined highlights", got)
	}
}

// `site:`/`-site:` and the date bounds belong in Exa's native request
// fields. The old body sent the raw query and no domain or date fields at
// all, so every such constraint silently did nothing server-side.
func TestExaMapsDirectivesOntoNativeFields(t *testing.T) {
	cfg := Config{ExaAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		body := readBody(t, req)
		if got := gjson.GetBytes(body, "includeDomains.0").String(); got != "go.dev" {
			t.Fatalf("includeDomains = %s", gjson.GetBytes(body, "includeDomains").Raw)
		}
		if got := gjson.GetBytes(body, "excludeDomains.0").String(); got != "spam.example" {
			t.Fatalf("excludeDomains = %s", gjson.GetBytes(body, "excludeDomains").Raw)
		}
		if got := gjson.GetBytes(body, "startPublishedDate").String(); got != "2024-01-01" {
			t.Fatalf("startPublishedDate = %q", got)
		}
		if got := gjson.GetBytes(body, "endPublishedDate").String(); got != "2025-01-01" {
			t.Fatalf("endPublishedDate = %q", got)
		}
		if query := gjson.GetBytes(body, "query").String(); strings.Contains(query, "site:") ||
			strings.Contains(query, "after:") {
			t.Fatalf("query = %q, want constraints kept out of the query text", query)
		}
		// The per-result summary is what feeds the synthesized answer.
		if !gjson.GetBytes(body, "contents.summary.query").Exists() {
			t.Fatalf("contents.summary missing: %s", body)
		}
		return jsonResponse(200, `{"results":[{"title":"T","url":"https://t.example","text":"body"}]}`), nil
	}})
	if _, errExecute := Execute(context.Background(), cfg, SearchRequest{
		Query:    "go site:go.dev -site:spam.example after:2024-01-01 before:2025-01-01",
		Provider: "exa",
	}); errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
}

// The answer is synthesized from Exa's per-result `summary` only; `text`
// is the snippet. Deriving the answer from the snippet duplicated the same
// text into both fields.
func TestExaAnswerComesFromSummaryNotText(t *testing.T) {
	cfg := Config{ExaAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(*http.Request) (*http.Response, error) {
		return jsonResponse(200, `{"results":[
			{"title":"A","url":"https://a.example","text":"page text","summary":"the real summary"}]}`), nil
	}})
	response, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "exa"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if !strings.Contains(response.Answer, "the real summary") || !strings.Contains(response.Answer, "**A**") {
		t.Fatalf("answer = %q, want it synthesized from the summary field", response.Answer)
	}
	if strings.Contains(response.Answer, "page text") {
		t.Fatalf("answer = %q, must not be derived from the snippet text", response.Answer)
	}
}

// A result with no title still has to render: the URL is the title.
func TestExaFallsBackToURLForMissingTitle(t *testing.T) {
	cfg := Config{ExaAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(*http.Request) (*http.Response, error) {
		return jsonResponse(200, `{"results":[{"url":"https://untitled.example","text":"x"}]}`), nil
	}})
	response, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "exa"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if len(response.Sources) != 1 || response.Sources[0].Title != "https://untitled.example" {
		t.Fatalf("sources = %+v, want the URL used as the title", response.Sources)
	}
}

// SearXNG aggregates engines that spell the same field differently, so
// both the body and the publication date have two accepted names.
func TestSearXNGReadsBothFieldSpellings(t *testing.T) {
	body := []byte(`{"results":[
		{"title":"T","url":"https://a.example","content":"body text","publishedDate":"2026-01-02"},
		{"url":"https://b.example","snippet":"alt text","published_date":"2026-01-03"}]}`)
	if got := firstNonEmpty(gjson.GetBytes(body, "results.0.content").String(), gjson.GetBytes(body, "results.0.snippet").String()); got != "body text" {
		t.Fatalf("content = %q", got)
	}
	second := gjson.GetBytes(body, "results.1")
	snippet := firstNonEmpty(second.Get("content").String(), second.Get("snippet").String())
	if snippet != "alt text" {
		t.Fatalf("snippet fallback = %q, want the alternate spelling read", snippet)
	}
	published := firstNonEmpty(second.Get("publishedDate").String(), second.Get("published_date").String())
	if published != "2026-01-03" {
		t.Fatalf("published = %q, want the snake_case spelling read", published)
	}
}
