package websearch

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type stubDoer struct {
	handle func(*http.Request) (*http.Response, error)
}

func (s stubDoer) Do(req *http.Request) (*http.Response, error) {
	return s.handle(req)
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestChainOrderExplicitFirst(t *testing.T) {
	cfg := Config{Order: []string{"exA", "brave", "exa", "unknown", "auto", ""}}.WithDefaults()
	order := cfg.ChainOrder()
	if len(order) != len(DefaultProviderOrder) || order[0] != "exa" || order[1] != "brave" {
		t.Fatalf("order = %v", order)
	}
	seen := map[string]bool{}
	for _, id := range order {
		if seen[id] {
			t.Fatalf("duplicate %q in %v", id, order)
		}
		seen[id] = true
	}
}

func TestCredentialPrefersExplicitOverEnv(t *testing.T) {
	t.Setenv("BRAVE_API_KEY", "env-key")
	if got := Credential("explicit", "BRAVE_API_KEY"); got != "explicit" {
		t.Fatalf("explicit = %q", got)
	}
	if got := Credential("", "BRAVE_API_KEY"); got != "env-key" {
		t.Fatalf("env = %q", got)
	}
}

func TestExecuteNoProviderConfigured(t *testing.T) {
	cfg := Config{Enabled: true, Exclude: DefaultProviderOrder}.WithDefaults()
	_, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "hi"})
	if errExecute == nil || !strings.Contains(errExecute.Error(), "no web search provider") {
		t.Fatalf("error = %v", errExecute)
	}
}

func TestExecuteFallsThroughToWorkingProvider(t *testing.T) {
	cfg := Config{Enabled: true, BraveAPIKey: "k", TavilyAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Host, "brave.com") {
			return jsonResponse(500, `{"error":"boom"}`), nil
		}
		return jsonResponse(200, `{"answer":"found","results":[{"title":"T","url":"https://example.com","content":"snippet"}]}`), nil
	}})
	response, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if response.Provider != ProviderTavily {
		t.Fatalf("provider = %q", response.Provider)
	}
	if response.Answer != "found" || len(response.Sources) != 1 {
		t.Fatalf("response = %+v", response)
	}
}

func TestExecuteAllFailedJoinsErrors(t *testing.T) {
	cfg := Config{Enabled: true, BraveAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Host, "brave.com") {
			return jsonResponse(401, `{"error":"bad key"}`), nil
		}
		return jsonResponse(500, `{"error":"down"}`), nil
	}})
	_, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go"})
	if errExecute == nil {
		t.Fatal("expected error")
	}
	// DuckDuckGo is credential-free and joins the chain, so the message is a join.
	if !strings.Contains(errExecute.Error(), "all web search providers failed") {
		t.Fatalf("error = %v", errExecute)
	}
	if !strings.Contains(errExecute.Error(), "Brave authorization failed") {
		t.Fatalf("missing auth normalization in %v", errExecute)
	}
}

func TestExecuteSkipsEmptyProvider(t *testing.T) {
	cfg := Config{Enabled: true, Exclude: []string{"tavily", "exa", "searxng", "duckduckgo"}, BraveAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(*http.Request) (*http.Response, error) {
		return jsonResponse(200, `{"web":{"results":[]}}`), nil
	}})
	_, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go"})
	if errExecute == nil || !strings.Contains(errExecute.Error(), "no renderable results") {
		t.Fatalf("error = %v", errExecute)
	}
}

func TestBraveParsesResults(t *testing.T) {
	cfg := Config{BraveAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("X-Subscription-Token"); got != "k" {
			t.Fatalf("token header = %q", got)
		}
		if got := req.URL.Query().Get("freshness"); got != "pw" {
			t.Fatalf("freshness = %q", got)
		}
		return jsonResponse(200, `{"web":{"results":[{"title":"T","url":"https://example.com","description":"d","age":"2 days ago"}]}}`), nil
	}})
	response, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Recency: RecencyWeek, Provider: "brave"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if len(response.Sources) != 1 || response.Sources[0].Published != "2 days ago" {
		t.Fatalf("sources = %+v", response.Sources)
	}
}

func TestExaSynthesizesAnswerFromSummaryOnly(t *testing.T) {
	cfg := Config{ExaAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(*http.Request) (*http.Response, error) {
		// `summary` is what the answer is built from; `text` is the page
		// body and must only ever become a snippet. Asserting an answer
		// built from `text` would pin the two channels together.
		// A carries both channels; B has no summary, so its snippet must
		// fall back to the page text.
		return jsonResponse(200, `{"results":[{"title":"A","url":"https://a.example","summary":"first summary","text":"first body"},
			{"title":"B","url":"https://b.example","text":"second body"}]}`), nil
	}})
	response, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "exa"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if !strings.Contains(response.Answer, "first summary") {
		t.Fatalf("answer = %q, want the per-result summary", response.Answer)
	}
	if strings.Contains(response.Answer, "first body") {
		t.Fatalf("answer = %q, must not be synthesized from page text", response.Answer)
	}
	if len(response.Sources) != 2 {
		t.Fatalf("sources = %+v", response.Sources)
	}
	// exaSnippet prefers the summary, then the page text, then highlights.
	if response.Sources[0].Snippet != "first summary" {
		t.Fatalf("snippet = %q, want the summary preferred over page text", response.Sources[0].Snippet)
	}
	if response.Sources[1].Snippet != "second body" {
		t.Fatalf("snippet = %q, want the page-text fallback when there is no summary", response.Sources[1].Snippet)
	}
}

func TestSearXNGDowngradesWeekAndSendsAuth(t *testing.T) {
	cfg := Config{SearXNGEndpoint: "http://searxng.local", SearXNGToken: "tok"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		if got := req.URL.Query().Get("time_range"); got != "month" {
			t.Fatalf("time_range = %q, want month downgrade", got)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer tok" {
			t.Fatalf("auth header = %q", got)
		}
		return jsonResponse(200, `{"results":[{"title":"T","url":"https://example.com","content":"c"}],"suggestions":["related"]}`), nil
	}})
	response, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Recency: RecencyWeek, Provider: "searxng"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if len(response.Sources) != 1 || len(response.Related) != 1 {
		t.Fatalf("response = %+v", response)
	}
}

func TestDuckDuckGoParsesHTML(t *testing.T) {
	page := `<html><body>
		<div class="result results_links results_links_deep web-result">
			<div class="links_main_links_deep result__body">
				<h2 class="result__title"><a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fa&amp;rut=1">First Title</a></h2>
				<a class="result__snippet" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fa">first snippet</a>
				<div class="result__extras__url"><span class="result__icon"></span><a class="result__url">example.com</a><span>&nbsp; &nbsp; 2026-07-30T20:19:00.0000000</span></div>
			</div>
		</div>
		<div class="result results_links results_links_deep web-result">
			<div class="links_main_links_deep result__body">
				<h2 class="result__title"><a class="result__a" href="https://other.example/b">Second</a></h2>
				<div class="result__extras__url"><span>2026-08-01</span></div>
			</div>
		</div>
	</body></html>`
	sources := parseDuckDuckGoResults(page)
	if len(sources) != 2 {
		t.Fatalf("sources = %+v", sources)
	}
	if sources[0].URL != "https://example.com/a" || sources[0].Title != "First Title" || sources[0].Snippet != "first snippet" {
		t.Fatalf("first = %+v", sources[0])
	}
	if sources[0].Published != "2026-07-30T20:19:00.0000000" {
		t.Fatalf("first published = %q", sources[0].Published)
	}
	if sources[1].URL != "https://other.example/b" || sources[1].Snippet != "" {
		t.Fatalf("second = %+v", sources[1])
	}
	if sources[1].Published != "2026-08-01" {
		t.Fatalf("second published = %q", sources[1].Published)
	}
}

func TestDuckDuckGoSnippetStaysWithinItsBlock(t *testing.T) {
	// A sponsored row between two organic ones must not donate its snippet
	// to whichever organic result happened to precede it.
	page := `<html><body>
		<div class="result"><a class="result__a" href="https://a.example">A</a></div>
		<div class="result result--ad"><a class="result__a" href="https://ad.example">Ad</a><a class="result__snippet">buy this</a></div>
		<div class="result"><a class="result__a" href="https://b.example">B</a><a class="result__snippet">b snippet</a></div>
	</body></html>`
	sources := parseDuckDuckGoResults(page)
	if len(sources) != 3 {
		t.Fatalf("sources = %+v", sources)
	}
	if sources[0].Snippet != "" {
		t.Fatalf("A picked up a foreign snippet: %+v", sources[0])
	}
	if sources[1].Snippet != "buy this" || sources[2].Snippet != "b snippet" {
		t.Fatalf("snippets = %q %q", sources[1].Snippet, sources[2].Snippet)
	}
}

func TestDuckDuckGoFollowsContinuationForm(t *testing.T) {
	pages := []string{
		`<html><body><div class="result"><a class="result__a" href="https://a.example">A</a></div>
		<form action="/html/"><input type="hidden" name="q" value="go"><input type="hidden" name="s" value="30"><input type="hidden" name="vqd" value="tok"><input type="submit" name="x" value="1"></form></body></html>`,
		`<html><body><div class="result"><a class="result__a" href="https://b.example">B</a></div></body></html>`,
	}
	var seenQueries []string
	cfg := Config{}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		values, _ := url.ParseQuery(string(body))
		seenQueries = append(seenQueries, values.Get("s"))
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"text/html"}},
			Body:       io.NopCloser(strings.NewReader(pages[len(seenQueries)-1])),
		}, nil
	}})
	response, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", NumSearchResults: 20, Provider: "duckduckgo"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if len(response.Sources) != 2 {
		t.Fatalf("sources = %+v, want the second page appended", response.Sources)
	}
	// The second request must carry the hidden form fields, not the initial query.
	if len(seenQueries) != 2 || seenQueries[1] != "30" {
		t.Fatalf("pagination params = %v", seenQueries)
	}
}

func TestDuckDuckGoLocaleMapsToRegionLanguage(t *testing.T) {
	for _, tc := range []struct{ lang, want string }{
		{"de-DE", "de-de"},
		{"pt-BR", "br-pt"},
		{"en-GB", "uk-en"},
		{"zh-TW", "tw-tzh"},
		// A script subtag is not a region, and an undocumented pair must
		// fall back to the default rather than sending an unknown kl.
		{"zh-Hans", ""},
		{"xx-YY", ""},
	} {
		if got := duckDuckGoKl(tc.lang); got != tc.want {
			t.Fatalf("duckDuckGoKl(%q) = %q, want %q", tc.lang, got, tc.want)
		}
	}
	if got := duckDuckGoForm(SearchRequest{Query: "go"}, ParsedQuery{Lang: "pt-BR"}).Get("kl"); got != "br-pt" {
		t.Fatalf("form kl = %q", got)
	}
	if got := duckDuckGoForm(SearchRequest{Query: "go"}, ParsedQuery{}).Get("kl"); got != "us-en" {
		t.Fatalf("default kl = %q", got)
	}
}

func TestDuckDuckGoStripsDateBoundsAndSendsRecency(t *testing.T) {
	form := duckDuckGoForm(SearchRequest{Query: "go", Recency: RecencyWeek}, ParseSearchQuery("go after:2026-01-01 lang:en-GB"))
	if q := form.Get("q"); strings.Contains(q, "after:") {
		t.Fatalf("query kept an operator DuckDuckGo cannot parse: %q", q)
	}
	if got := form.Get("df"); got != "w" {
		t.Fatalf("df = %q", got)
	}
}

func TestDuckDuckGoChallengeAdvancesChain(t *testing.T) {
	cfg := Config{Enabled: true, Exclude: []string{"brave", "tavily", "exa", "searxng"}}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"text/html"}},
			Body:       io.NopCloser(strings.NewReader(`<html><body><div class="anomaly-modal">challenge</div></body></html>`)),
		}, nil
	}})
	_, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go"})
	if errExecute == nil || !strings.Contains(errExecute.Error(), "bot-detection challenge") {
		t.Fatalf("error = %v", errExecute)
	}
}

func TestUnwrapDuckDuckGoURL(t *testing.T) {
	if got := unwrapDuckDuckGoURL("//duckduckgo.com/l/?uddg=https%3A%2F%2Fx.example%2Fy&rut=1"); got != "https://x.example/y" {
		t.Fatalf("wrapped = %q", got)
	}
	if got := unwrapDuckDuckGoURL("https://direct.example/z"); got != "https://direct.example/z" {
		t.Fatalf("direct = %q", got)
	}
	if got := unwrapDuckDuckGoURL("javascript:void(0)"); got != "" {
		t.Fatalf("non-http = %q", got)
	}
}

func TestFetchRejectsOversizeBody(t *testing.T) {
	cfg := Config{}.WithDefaults()
	big := bytes.Repeat([]byte("x"), MaxSearchBodySize+8)
	cfg = cfg.WithDoer(stubDoer{handle: func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(big))}, nil
	}})
	_, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "duckduckgo"})
	if errExecute == nil || !strings.Contains(errExecute.Error(), "too large") {
		t.Fatalf("error = %v", errExecute)
	}
}
