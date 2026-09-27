package websearch

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// perplexityOKBody is an ask-endpoint reply carrying one web result so the
// ladder stops at the credential under test.
const perplexityOKBody = `data: {"blocks":[{"intended_usage":"web_results","web_result_block":{"web_results":[{"url":"https://a.example","name":"A","snippet":"s"}]}}],"status":"COMPLETED"}`

// The API key path reports titled hits under a flat `search_results` array
// and the answer's cited URLs under `citations`. Citations win: a citation
// without a matching result keeps its URL, and one with a match inherits the
// result's title, snippet, and date.
func TestPerplexityAPIKeyCitationsWinOverSearchResults(t *testing.T) {
	cfg := Config{PerplexityAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(*http.Request) (*http.Response, error) {
		return jsonResponse(200, `{"id":"r1","model":"sonar-pro",
			"choices":[{"message":{"content":"answer"}}],
			"search_results":[
				{"title":"Titled A","url":"https://a.example","snippet":"snip A","date":"2026-01-02"},
				{"title":"Uncited","url":"https://uncited.example","snippet":"never cited"}
			],
			"citations":["https://a.example","https://cited-only.example"]}`), nil
	}})
	response, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "perplexity"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if len(response.Sources) != 2 {
		t.Fatalf("sources = %+v, want only the two citations", response.Sources)
	}
	if response.Sources[0].Title != "Titled A" || response.Sources[0].Snippet != "snip A" || response.Sources[0].Published != "2026-01-02" {
		t.Fatalf("citation with a matching result must inherit it: %+v", response.Sources[0])
	}
	if response.Sources[1].URL != "https://cited-only.example" || response.Sources[1].Title != "https://cited-only.example" {
		t.Fatalf("citation without a result keeps its URL as title: %+v", response.Sources[1])
	}
}

// With no citations at all, every titled search result is a source.
func TestPerplexityAPIKeyFallsBackToSearchResults(t *testing.T) {
	cfg := Config{PerplexityAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(*http.Request) (*http.Response, error) {
		return jsonResponse(200, `{"choices":[{"message":{"content":"answer"}}],
			"search_results":[
				{"title":"A","url":"https://a.example","snippet":"sa","date":"2026-01-02"},
				{"url":"https://b.example"},
				{"title":"no url, dropped"}
			]}`), nil
	}})
	response, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "perplexity"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if len(response.Sources) != 2 {
		t.Fatalf("sources = %+v, want the two url-bearing results", response.Sources)
	}
	if response.Sources[1].Title != "https://b.example" {
		t.Fatalf("untitled result falls back to its URL: %+v", response.Sources[1])
	}
}

// The host reports the query it ran as a single string, not an array.
func TestPerplexityAPISearchQueryIsAString(t *testing.T) {
	cfg := Config{PerplexityAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(*http.Request) (*http.Response, error) {
		return jsonResponse(200, `{"choices":[{"message":{"content":"a"}}],
			"search_query":"go generics",
			"citations":["https://a.example"]}`), nil
	}})
	response, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "perplexity"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if len(response.SearchQueries) != 1 || response.SearchQueries[0] != "go generics" {
		t.Fatalf("searchQueries = %+v", response.SearchQueries)
	}
}

// site:, date bounds, and lang: must ride native request fields, and the
// query text must be rebuilt without them so the engine is not
// double-constrained.
func TestPerplexityAPIKeySendsNativeFilters(t *testing.T) {
	cfg := Config{PerplexityAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		domains := gjson.GetBytes(body, "search_domain_filter").Array()
		if len(domains) != 2 || domains[0].String() != "github.com" || domains[1].String() != "-golang.org" {
			t.Fatalf("search_domain_filter = %v, want bare hosts with a -deny entry", domains)
		}
		if got := gjson.GetBytes(body, "search_after_date_filter").String(); got != "3/1/2025" {
			t.Fatalf("search_after_date_filter = %q, want %%m/%%d/%%Y", got)
		}
		if got := gjson.GetBytes(body, "search_before_date_filter").String(); got != "4/1/2025" {
			t.Fatalf("search_before_date_filter = %q", got)
		}
		if got := gjson.GetBytes(body, "search_language_filter.0").String(); got != "en" {
			t.Fatalf("search_language_filter = %q, want the ISO 639-1 code", got)
		}
		// The site: directive is carried by the filter, so it must not also
		// appear in the query text.
		if query := gjson.GetBytes(body, "messages.0.content").String(); strings.Contains(query, "site:") {
			t.Fatalf("query still carries site:: %q", query)
		}
		return jsonResponse(200, `{"choices":[{"message":{"content":"a"}}],"citations":["https://a.example"]}`), nil
	}})
	query := "generics site:github.com/anthropics -site:golang.org after:2025-03-01 before:2025-04-01 lang:en-us"
	if _, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: query, Provider: "perplexity"}); errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
}

// A configured cookie header authenticates the ask endpoint directly and
// outranks the OAuth session token.
func TestPerplexityCookiesOutrankOAuthToken(t *testing.T) {
	t.Setenv("PERPLEXITY_COOKIES", "next-auth.session-token=session-cookie")
	cfg := Config{PerplexityOAuth: "oauth-jwt"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("Cookie"); got != "next-auth.session-token=session-cookie" {
			t.Fatalf("cookie = %q, want the raw configured cookie header", got)
		}
		return sseBody(perplexityOKBody), nil
	}})
	response, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "perplexity"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if response.AuthMode != "cookies" || len(response.Sources) != 1 {
		t.Fatalf("response = %+v", response)
	}
	if response.Sources[0].URL != "https://a.example" {
		t.Fatalf("sources = %+v", response.Sources)
	}
}

// A cookie-only configuration is real Perplexity auth, so the provider
// qualifies for the automatic chain without an explicit selection.
func TestPerplexityCookiesSatisfyAutomaticChain(t *testing.T) {
	t.Setenv("PERPLEXITY_COOKIES", "next-auth.session-token=session-cookie")
	if !(perplexityProvider{}).Available(Config{}.WithDefaults(), false) {
		t.Fatal("a configured cookie must admit perplexity to the automatic chain")
	}
}
