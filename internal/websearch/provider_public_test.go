package websearch

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestZAIArgumentLadder(t *testing.T) {
	attempts := 0
	cfg := Config{ZAIAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		attempts++
		// The first shape is rejected; the second must be accepted.
		if attempts == 1 {
			return jsonResponse(400, `{"error":{"message":"invalid arguments"}}`), nil
		}
		return jsonResponse(200, `{"result":{"content":[{"type":"text","text":"{\"answer\":\"grounded\",\"results\":[{\"title\":\"T\",\"url\":\"https://z.ai/doc\"}]}"}]}}`), nil
	}})
	response, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "zai"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if response.Answer != "grounded" || len(response.Sources) != 1 {
		t.Fatalf("response = %+v", response)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2 (ladder should stop at the accepted shape)", attempts)
	}
}

func TestZAIAllShapesRejected(t *testing.T) {
	calls := 0
	cfg := Config{ZAIAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		if gjson.GetBytes(readBody(t, req), "method").String() == "tools/call" {
			calls++
		}
		return jsonResponse(400, `{"error":{"message":"invalid arguments"}}`), nil
	}})
	if _, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "zai"}); errExecute == nil {
		t.Fatal("expected error when every argument shape is rejected")
	}
	if calls != 3 {
		t.Fatalf("tools/call attempts = %d, want 3 (full ladder)", calls)
	}
}

// A tools/call that answers 200 with result.isError is a rejected argument
// shape, so the ladder must advance instead of surfacing the error text as
// a search answer.
func TestZAILadderAdvancesOnIsErrorResult(t *testing.T) {
	calls := 0
	cfg := Config{ZAIAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		body := readBody(t, req)
		if gjson.GetBytes(body, "method").String() != "tools/call" {
			return jsonResponse(200, `{"result":{"content":[{"type":"text","text":"ok"}]}}`), nil
		}
		calls++
		if calls < 2 {
			return jsonResponse(200, `{"result":{"isError":true,"content":[{"type":"text","text":"MCP error -32602: invalid arguments"}]}}`), nil
		}
		return jsonResponse(200, `{"result":{"content":[{"type":"text","text":"{\"search_result\":[{\"title\":\"T\",\"link\":\"https://z.ai/doc\"}]}"}]}}`), nil
	}})
	response, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "zai"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if calls != 2 {
		t.Fatalf("tools/call attempts = %d, want the ladder to advance past isError", calls)
	}
	if len(response.Sources) != 1 || response.Sources[0].URL != "https://z.ai/doc" {
		t.Fatalf("sources = %+v", response.Sources)
	}
}

// A valid but empty result is not a shape failure: the ladder must not burn
// its remaining rungs.
func TestZAIEmptyResultDoesNotAdvanceLadder(t *testing.T) {
	calls := 0
	cfg := Config{ZAIAPIKey: "k"}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		body := readBody(t, req)
		if gjson.GetBytes(body, "method").String() != "tools/call" {
			return jsonResponse(200, `{"result":{"content":[{"type":"text","text":"ok"}]}}`), nil
		}
		calls++
		return jsonResponse(200, `{"result":{"content":[{"type":"text","text":"{\"search_result\":[]}"}]}}`), nil
	}})
	_, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "zai"})
	if calls != 1 {
		t.Fatalf("tools/call attempts = %d, want 1 (an empty result is not a shape failure)", calls)
	}
	if errExecute == nil {
		t.Fatal("an empty result should not be reported as a renderable answer")
	}
}

// Z.AI returns the same result in several containers, and a text part can
// carry the payload double-encoded. Every shape must yield the results.
func TestZAIPayloadShapes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
	}{
		{"structured", `{"structuredContent":{"results":[{"title":"T","url":"https://z.ai/a"}]},"request_id":"rq"}`},
		{"data", `{"data":{"search_result":[{"title":"T","link":"https://z.ai/a"}]}}`},
		{"double encoded", `[{"type":"text","text":"\"{\\\"results\\\":[{\\\"title\\\":\\\"T\\\",\\\"url\\\":\\\"https://z.ai/a\\\"}]}\""}]`},
		{"prose alongside", `[{"type":"text","text":"Ground truth prose."},{"type":"text","text":"{\"results\":[{\"title\":\"T\",\"url\":\"https://z.ai/a\"}]}"}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			results, _, _ := zaiPayload([]byte(tc.content))
			if len(results) != 1 || results[0].URL != "https://z.ai/a" {
				t.Fatalf("results = %+v", results)
			}
		})
	}
}

// A result carrying a summary field must keep it as the answer, and a bare
// JSON text block must not be mistaken for prose to quote to the model.
func TestZAIAnswerAndProseAreDistinct(t *testing.T) {
	_, answer, _ := zaiPayload([]byte(`[{"type":"text","text":"{\"answer\":\"grounded\",\"results\":[{\"title\":\"T\",\"url\":\"https://z.ai/a\"}]}"}]`))
	if answer != "grounded" {
		t.Fatalf("answer = %q, want the summary field, not the JSON blob", answer)
	}
	_, answer, _ = zaiPayload([]byte(`[{"type":"text","text":"Plain prose."},{"type":"text","text":"{\"results\":[{\"title\":\"T\",\"url\":\"https://z.ai/a\"}]}"}]`))
	if answer != "Plain prose." {
		t.Fatalf("answer = %q, want only the prose part", answer)
	}
}

func readBody(t *testing.T, req *http.Request) []byte {
	t.Helper()
	body, errRead := io.ReadAll(req.Body)
	if errRead != nil {
		t.Fatalf("read request body: %v", errRead)
	}
	return body
}

func TestMCPStreamsSSEFramedResult(t *testing.T) {
	cfg := Config{}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(*http.Request) (*http.Response, error) {
		return sseBody(`data: {"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{\"results\":[{\"title\":\"M\",\"url\":\"https://mcp.example\"}]}"}]}}`), nil
	}})
	content, errCall := mcpCall(context.Background(), cfg, "Test", defaultParallelMCPURL, "web_search", mcpHeaders(""), [][]byte{
		mcpArgumentBody(map[string]any{"query": "go"}),
	})
	if errCall != nil {
		t.Fatalf("mcpCall error = %v", errCall)
	}
	sources := mcpTextSources(content, 5)
	if len(sources) != 1 || sources[0].URL != "https://mcp.example" {
		t.Fatalf("sources = %+v", sources)
	}
}

func TestCanonicalURLKeyNormalizes(t *testing.T) {
	cases := []struct {
		left  string
		right string
	}{
		{"https://www.example.com/path", "https://example.com/path"},
		{"https://example.com/path/", "https://example.com/path"},
		{"https://example.com/path#frag", "https://example.com/path"},
		{"https://example.com/p?a=1", "https://example.com/p?a=1"},
		{"https://EXAMPLE.com/p", "https://example.com/p"},
	}
	for _, tc := range cases {
		if canonicalURLKey(tc.left) != canonicalURLKey(tc.right) {
			t.Fatalf("%q and %q should share a key: %q vs %q",
				tc.left, tc.right, canonicalURLKey(tc.left), canonicalURLKey(tc.right))
		}
	}
	if canonicalURLKey("https://example.com/a?a=1") == canonicalURLKey("https://example.com/a?a=2") {
		t.Fatal("query must be preserved in the dedupe key")
	}
	// An unparsable URL is kept verbatim so the result is not dropped
	// from the merged set; it simply dedupes against itself.
	if canonicalURLKey("not a url") != "not a url" {
		t.Fatalf("unparsable URL key = %q, want the raw URL", canonicalURLKey("not a url"))
	}
}

func TestPublicIsExplicitOnly(t *testing.T) {
	cfg := Config{}.WithDefaults()
	provider := lookupProvider(ProviderPublic)
	if provider.Available(cfg, false) {
		t.Fatal("public must never enter the automatic chain")
	}
	if !provider.Available(cfg, true) {
		t.Fatal("public must be reachable when explicitly selected")
	}
	if !cfg.Excluded(ProviderPublic) == false {
		// public is not in DefaultProviderOrder, so the chain never sees it.
		for _, id := range DefaultProviderOrder {
			if id == ProviderPublic {
				t.Fatal("public must stay out of DefaultProviderOrder")
			}
		}
	}
}

func TestPublicConsolidationRanksByConsensus(t *testing.T) {
	settled := []publicResult{
		{engine: "startpage", response: SearchResponse{Sources: []Source{
			{Title: "A", URL: "https://a.example", Snippet: "short"},
			{Title: "B", URL: "https://b.example"},
		}}},
		{engine: "google", response: SearchResponse{Sources: []Source{
			{Title: "A", URL: "https://www.a.example/", Snippet: "a much longer snippet for the same page"},
		}}},
		{engine: "mojeek", response: SearchResponse{Sources: []Source{
			{Title: "B", URL: "https://b.example", Snippet: "only mojeek has B at rank 0"},
		}}},
	}
	response, errConsolidate := consolidatePublic(settled, Config{}.WithDefaults(), SearchRequest{Query: "go"})
	if errConsolidate != nil {
		t.Fatalf("consolidatePublic error = %v", errConsolidate)
	}
	if len(response.Sources) != 2 {
		t.Fatalf("sources = %+v, want 2 deduped", response.Sources)
	}
	// A appears on two engines, so it outranks B regardless of rank.
	if response.Sources[0].URL != "https://a.example" {
		t.Fatalf("consensus rank first: %+v", response.Sources)
	}
	// The longest snippet across engines wins.
	if !strings.Contains(response.Sources[0].Snippet, "much longer") {
		t.Fatalf("longest snippet should win: %+v", response.Sources[0])
	}
}

func TestPublicFailsOnlyWhenAllEnginesFail(t *testing.T) {
	allFailed := []publicResult{
		{engine: "startpage", err: &ProviderError{Provider: "Startpage", Message: "challenge", Status: 429}},
		{engine: "google", err: &ProviderError{Provider: "Google", Message: "challenge", Status: 429}},
	}
	if _, errConsolidate := consolidatePublic(allFailed, Config{}.WithDefaults(), SearchRequest{Query: "go"}); errConsolidate == nil {
		t.Fatal("aggregate must fail when every engine fails")
	}
	oneOK := []publicResult{
		{engine: "google", response: SearchResponse{Sources: []Source{{URL: "https://ok.example"}}}},
		{engine: "startpage", err: &ProviderError{Provider: "Startpage", Message: "challenge", Status: 429}},
	}
	if _, errConsolidate := consolidatePublic(oneOK, Config{}.WithDefaults(), SearchRequest{Query: "go"}); errConsolidate != nil {
		t.Fatalf("one success must be enough: %v", errConsolidate)
	}
}

func TestPublicFanoutRespectsExclusions(t *testing.T) {
	cfg := Config{Exclude: []string{ProviderStartpage, ProviderGoogle, ProviderDuckDuckGo}}.WithDefaults()
	engines := publicEngines(cfg)
	if len(engines) != 2 {
		t.Fatalf("engines = %d, want 2 after exclusions", len(engines))
	}
	for _, engine := range engines {
		if cfg.Excluded(engine.ID()) {
			t.Fatalf("excluded engine %s present", engine.ID())
		}
	}
}

func TestPublicFanoutMergesLiveEngines(t *testing.T) {
	cfg := Config{Exclude: []string{"brave", "tavily", "exa", "searxng"}}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		host := req.URL.Host
		switch {
		case strings.Contains(host, "duckduckgo"):
			// The no-JS frontend emits a bare anchor with a description
			// sibling and no enclosing result block.
			return htmlResponse(200, `<html><body><div class="result results_links">
				<h2 class="result__title"><a class="result__a" href="https://shared.example">DDG</a></h2>
				<a class="result__snippet" href="https://shared.example">ddg</a>
			</div></body></html>`), nil
		case strings.Contains(host, "startpage"):
			// Startpage needs its seed request first to lift the form token.
			if req.URL.Path == "/" {
				return htmlResponse(200, `<html><form action="/sp/search"><input type="hidden" name="sc" value="tok"></form></html>`), nil
			}
			return htmlResponse(200, `<html><body><div class="result">
				<a class="result-link" href="https://shared.example"><h2 class="wgl-title">SP</h2></a>
				<p class="description">a much longer snippet from startpage</p>
			</div></body></html>`), nil
		default:
			// Google, Ecosia, and Mojeek return a bot challenge here.
			return htmlResponse(200, `<html><body>unusual traffic</body></html>`), nil
		}
	}})
	// Aggregate the settled engine results directly: the live fanout returns
	// at the soft deadline as soon as one engine succeeds, so which engines
	// have settled is a race. Deterministic merge behavior is asserted
	// against consolidatePublic below.
	response, errConsolidate := consolidatePublic([]publicResult{
		{engine: ProviderDuckDuckGo, response: SearchResponse{Sources: []Source{
			{Title: "DDG", URL: "https://shared.example", Snippet: "ddg"},
		}}},
		{engine: ProviderStartpage, response: SearchResponse{Sources: []Source{
			{Title: "SP", URL: "https://shared.example", Snippet: "a much longer snippet from startpage"},
		}}},
	}, cfg, SearchRequest{Query: "go"})
	if errConsolidate != nil {
		t.Fatalf("consolidatePublic error = %v", errConsolidate)
	}
	if len(response.Sources) != 1 {
		t.Fatalf("sources = %+v, want the shared page deduped to one hit", response.Sources)
	}
	if response.Sources[0].URL != "https://shared.example" {
		t.Fatalf("source = %+v", response.Sources[0])
	}
	// The longer snippet across engines wins during consolidation.
	if response.Sources[0].Snippet != "a much longer snippet from startpage" {
		t.Fatalf("snippet = %q, want the longest surviving snippet", response.Sources[0].Snippet)
	}
	// Merging runs in engine-priority order, so the result does not depend
	// on which engine happened to settle first.
	if !strings.HasPrefix(response.Sources[0].Title, "SP") {
		t.Fatalf("title = %q, want the higher-priority engine's title", response.Sources[0].Title)
	}
}

// The live fanout still works end to end: with three engines challenging,
// the two that answer produce one deduped hit.
func TestPublicFanoutEndToEnd(t *testing.T) {
	cfg := Config{Exclude: []string{"brave", "tavily", "exa", "searxng"}}.WithDefaults()
	cfg = cfg.WithDoer(stubDoer{handle: func(req *http.Request) (*http.Response, error) {
		host := req.URL.Host
		if strings.Contains(host, "duckduckgo") {
			return htmlResponse(200, `<html><body><div class="result results_links">
				<h2 class="result__title"><a class="result__a" href="https://shared.example">DDG</a></h2>
				<a class="result__snippet" href="https://shared.example">ddg</a>
			</div></body></html>`), nil
		}
		if strings.Contains(host, "startpage") && req.URL.Path == "/" {
			return htmlResponse(200, `<html><form action="/sp/search"><input type="hidden" name="sc" value="tok"></form></html>`), nil
		}
		if strings.Contains(host, "startpage") {
			return htmlResponse(200, `<html><body><div class="result">
				<a class="result-link" href="https://shared.example"><h2 class="wgl-title">SP</h2></a>
			</div></body></html>`), nil
		}
		return htmlResponse(200, `<html><body>unusual traffic</body></html>`), nil
	}})
	response, errExecute := Execute(context.Background(), cfg, SearchRequest{Query: "go", Provider: "public"})
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	if len(response.Sources) != 1 || response.Sources[0].URL != "https://shared.example" {
		t.Fatalf("sources = %+v, want the shared page deduped to one hit", response.Sources)
	}
}
