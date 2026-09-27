package websearch

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

// zaiProvider answers searches through Z.AI's remote MCP web_search_prime
// tool. The endpoint is strict about argument names, so the adapter walks
// an argument ladder and keeps the first shape the tool accepts.
type zaiProvider struct{}

func (zaiProvider) ID() string    { return ProviderZAI }
func (zaiProvider) Label() string { return "Z.AI" }

func (zaiProvider) Available(cfg Config, _ bool) bool {
	return cfg.ZAIKey() != ""
}

func (zaiProvider) Search(ctx context.Context, cfg Config, req SearchRequest, _ ParsedQuery) (SearchResponse, error) {
	key := cfg.ZAIKey()
	if key == "" {
		return SearchResponse{}, &ProviderError{Provider: "Z.AI", Message: "missing API key"}
	}
	count := req.ResultCount()
	if count <= 0 {
		count = EffectiveLimit(cfg, req)
	}
	content, errCall := mcpCall(ctx, cfg, "Z.AI", defaultZAIEndpoint, "web_search_prime", mcpHeaders(key), [][]byte{
		mcpArgumentBody(map[string]any{"query": req.Query, "count": count}),
		mcpArgumentBody(map[string]any{"search_query": req.Query, "count": count}),
		mcpArgumentBody(map[string]any{"search_query": req.Query, "search_engine": "search-prime", "count": count}),
	})
	if errCall != nil {
		return SearchResponse{}, errCall
	}
	results, answer, requestID := zaiPayload(content)
	if answer == "" && len(results) == 0 {
		return SearchResponse{}, &ProviderError{Provider: "Z.AI", Message: "search returned no answer or results"}
	}
	return SearchResponse{
		Answer:    answer,
		Sources:   capSources(results, count),
		AuthMode:  "api_key",
		RequestID: requestID,
	}, nil
}

// zaiPayload splits a Z.AI tool result into its result list and prose. The
// result can arrive as structured content, as a JSON document inside a
// text block, or as JSON double-encoded inside such a document, so every
// candidate is tried in turn and text that yields no results is kept as
// answer prose rather than being discarded.
func zaiPayload(content []byte) (results []Source, answer, requestID string) {
	root := gjson.ParseBytes(content)
	candidates := []gjson.Result{root}
	if structured := root.Get("structuredContent"); structured.Exists() {
		candidates = append(candidates, structured)
	}
	if data := root.Get("data"); data.Exists() {
		candidates = append(candidates, data)
	}
	if inner := root.Get("result"); inner.Exists() {
		candidates = append(candidates, inner)
	}

	// A summary field is the provider's own answer, distinct from the prose
	// the model is meant to read: the former leads, the latter follows.
	var prose, summary []string
	collect := func(text string) {
		decoded := gjson.Parse(text)
		if !decoded.Exists() {
			prose = append(prose, text)
			return
		}
		// A JSON string payload is itself an encoded document.
		if decoded.Type == gjson.String {
			if inner := gjson.Parse(decoded.String()); inner.Exists() {
				decoded = inner
			} else {
				prose = append(prose, text)
				return
			}
		}
		if value := strings.TrimSpace(decoded.Get("answer").String()); value != "" {
			summary = append(summary, value)
		}
		candidates = append(candidates, decoded)
	}
	// mcpToolResult hands over the content array itself, so the text parts
	// are top-level elements; a document that nests its own content array
	// is walked as well.
	collectParts := func(node gjson.Result) {
		node.ForEach(func(_, item gjson.Result) bool {
			if text := strings.TrimSpace(item.Get("text").String()); text != "" {
				collect(text)
			}
			return true
		})
	}
	collectParts(root)
	collectParts(root.Get("content"))
	for _, candidate := range append([]gjson.Result(nil), candidates...) {
		collectParts(candidate.Get("content"))
	}

	answer = strings.Join(append(summary, prose...), "\n\n")
	for _, candidate := range candidates {
		if found := zaiResults(candidate); len(found) > 0 {
			return found, answer, zaiRequestID(candidate)
		}
	}
	return nil, answer, ""
}

// zaiResults reads the result list from the containers Z.AI uses.
func zaiResults(node gjson.Result) []Source {
	list := node
	if !list.IsArray() {
		for _, path := range []string{"search_result", "results"} {
			if found := node.Get(path); found.IsArray() {
				list = found
				break
			}
		}
	}
	if !list.IsArray() {
		return nil
	}
	items := list.Array()
	results := make([]Source, 0, len(items))
	for _, item := range items {
		url := firstNonEmpty(strings.TrimSpace(item.Get("link").String()), strings.TrimSpace(item.Get("url").String()))
		if url == "" {
			continue
		}
		results = append(results, Source{
			Title:     firstNonEmpty(strings.TrimSpace(item.Get("title").String()), url),
			URL:       url,
			Snippet:   strings.TrimSpace(item.Get("content").String()),
			Published: firstNonEmpty(strings.TrimSpace(item.Get("publish_date").String()), strings.TrimSpace(item.Get("publishedDate").String())),
		})
	}
	return results
}

func zaiRequestID(node gjson.Result) string {
	if !node.IsObject() {
		return ""
	}
	return firstNonEmpty(
		node.Get("request_id").String(),
		node.Get("requestId").String(),
		node.Get("id").String(),
	)
}

// startPageProvider scrapes Startpage, which proxies Google's index. The
// search form requires a short-lived anti-bot token, so the adapter seeds
// the homepage first and falls back to a tokenless GET.
type startPageProvider struct{}

func (startPageProvider) ID() string    { return ProviderStartpage }
func (startPageProvider) Label() string { return "Startpage" }

func (startPageProvider) Available(_ Config, _ bool) bool { return true }

func (startPageProvider) Search(ctx context.Context, cfg Config, req SearchRequest, parsed ParsedQuery) (SearchResponse, error) {
	count := clampCount(EffectiveLimit(cfg, req), 1, 20, 10)
	// Startpage proxies Google, so the operator set works inline. Directive
	// aliases are canonicalized the same way every other scraper is.
	query := FormatScraperQuery(req.Query, parsed, GoogleQuerySyntax)

	// Best effort: a seed failure is not fatal, we just lose the form token.
	cookie, hidden, _ := seedStartPage(ctx, cfg)
	var page []byte
	var status int
	var errFetch error
	if len(hidden) > 0 {
		form := startPageForm(query, req.Recency, hidden)
		page, status, errFetch = startPageFetch(ctx, cfg, cookie, form)
	} else {
		page, status, errFetch = startPageTokenlessGet(ctx, cfg, cookie, startPageForm(query, req.Recency, nil))
	}
	if errFetch != nil {
		return SearchResponse{}, errFetch
	}
	if isStartPageChallenge(string(page)) {
		return SearchResponse{}, &ProviderError{
			Provider: "Startpage",
			Message:  "Startpage blocked the request with a CAPTCHA challenge; it rate-limits datacenter egress. Try DuckDuckGo or another provider, or retry later.",
			Status:   http.StatusTooManyRequests,
		}
	}
	if status < 200 || status >= 300 {
		return SearchResponse{}, &ProviderError{
			Provider: "Startpage",
			Message:  fmt.Sprintf("Startpage HTML error (%d)", status),
			Status:   status,
		}
	}
	return SearchResponse{Sources: parseStartPageResults(string(page), count)}, nil
}
