package websearch

import (
	"strings"
	"testing"
)

// manySources builds count distinct sources for consolidation tests.
func manySources(count int) []Source {
	sources := make([]Source, 0, count)
	for index := range count {
		sources = append(sources, Source{
			Title: "T",
			URL:   "https://a" + string(rune('a'+index%26)) + string(rune('a'+index/26)) + ".example",
		})
	}
	return sources
}

// The aggregate reads the request's own result count, where
// NumSearchResults wins over Limit, and clamps it to the aggregate's wider
// window. A configured global limit is only a default for requests that
// state no count, so it must not override an explicit request count.
func TestPublicAggregateRequestCountOutranksConfiguredLimit(t *testing.T) {
	settled := []publicResult{{engine: ProviderMojeek, response: SearchResponse{Sources: manySources(8)}}}
	cfg := Config{Limit: 2}.WithDefaults()
	response, errConsolidate := consolidatePublic(settled, cfg, SearchRequest{Query: "go", NumSearchResults: 6})
	if errConsolidate != nil {
		t.Fatalf("consolidatePublic error = %v", errConsolidate)
	}
	if len(response.Sources) != 6 {
		t.Fatalf("sources = %d, want the request's 6, not the configured limit of 2", len(response.Sources))
	}
}

// A request limit is a cap, so asking for one result must return one even
// though the aggregate's own default is wider.
func TestPublicAggregateHonorsRequestLimit(t *testing.T) {
	settled := []publicResult{{engine: ProviderMojeek, response: SearchResponse{Sources: manySources(5)}}}
	response, errConsolidate := consolidatePublic(settled, Config{}.WithDefaults(), SearchRequest{Query: "go", Limit: 1})
	if errConsolidate != nil {
		t.Fatalf("consolidatePublic error = %v", errConsolidate)
	}
	if len(response.Sources) != 1 {
		t.Fatalf("sources = %+v, want the request limit of 1 honored", response.Sources)
	}
}

// With equal consensus and equal best rank, first-seen order decides. The
// merge must be reproducible across repeated runs.
func TestPublicAggregateTiebreaksDeterministically(t *testing.T) {
	// A and B are each returned by two engines, both at rank 0, so only
	// first-seen order can separate them.
	settled := []publicResult{
		{engine: ProviderStartpage, response: SearchResponse{Sources: []Source{
			{Title: "A", URL: "https://a.example"},
			{Title: "B", URL: "https://b.example"},
		}}},
		{engine: ProviderGoogle, response: SearchResponse{Sources: []Source{
			{Title: "A", URL: "https://a.example"},
			{Title: "B", URL: "https://b.example"},
		}}},
	}
	for attempt := range 20 {
		response, errConsolidate := consolidatePublic(settled, Config{}.WithDefaults(), SearchRequest{Query: "go"})
		if errConsolidate != nil {
			t.Fatalf("consolidatePublic error = %v", errConsolidate)
		}
		if len(response.Sources) != 2 {
			t.Fatalf("sources = %+v", response.Sources)
		}
		// startpage outranks google in the fan-out order, so A was seen
		// first and must stay first.
		if response.Sources[0].URL != "https://a.example" {
			t.Fatalf("attempt %d: first source = %q, want https://a.example", attempt, response.Sources[0].URL)
		}
	}
}

// The aggregate's own default is wider than a single engine's, so an
// unspecified request still asks for its full window.
func TestPublicAggregateDefaultsToItsWiderWindow(t *testing.T) {
	settled := []publicResult{{engine: ProviderMojeek, response: SearchResponse{Sources: manySources(DefaultPublicLimit + 5)}}}
	response, errConsolidate := consolidatePublic(settled, Config{}.WithDefaults(), SearchRequest{Query: "go"})
	if errConsolidate != nil {
		t.Fatalf("consolidatePublic error = %v", errConsolidate)
	}
	if len(response.Sources) != DefaultPublicLimit {
		t.Fatalf("sources = %d, want the aggregate default of %d", len(response.Sources), DefaultPublicLimit)
	}
}

// URLs differing only in host casing, a leading www., a trailing slash, or
// a fragment are the same page; the query is kept, and a URL that will not
// parse is preserved verbatim.
func TestPublicCanonicalURLKey(t *testing.T) {
	base := canonicalURLKey("https://a.example/page/")
	for _, variant := range []string{
		"https://www.a.example/page",
		"https://A.Example/page/",
	} {
		if got := canonicalURLKey(variant); got != base {
			t.Fatalf("canonicalURLKey(%q) = %q, want %q", variant, got, base)
		}
	}
	if got := canonicalURLKey("https://a.example/page#frag"); got != base {
		t.Fatalf("fragment should be dropped: %q vs %q", got, base)
	}
	if !strings.Contains(canonicalURLKey("https://a.example/page?x=1"), "x=1") {
		t.Fatal("query must be preserved in the dedup key")
	}
	if got := canonicalURLKey("not a url"); got != "not a url" {
		t.Fatalf("unparseable URL = %q, want it verbatim", got)
	}
}
