package websearch

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

// exaProvider queries the Exa search API.
type exaProvider struct{}

func (exaProvider) ID() string    { return ProviderExa }
func (exaProvider) Label() string { return "Exa" }

// Available reports that Exa always qualifies: with a credential it uses
// the API, and the explicit selection may fall back to public MCP.
func (exaProvider) Available(_ Config, _ bool) bool {
	return true
}

func (exaProvider) Search(ctx context.Context, cfg Config, req SearchRequest, parsed ParsedQuery) (SearchResponse, error) {
	key := cfg.ExaKey()
	if key == "" {
		// Public MCP path: no credential required.
		count := req.ResultCount()
		if count <= 0 {
			count = EffectiveLimit(cfg, req)
		}
		content, errCall := mcpCall(ctx, cfg, "Exa", defaultExaMCPURL, "web_search_exa", mcpHeaders(""), [][]byte{
			mcpArgumentBody(map[string]any{"query": req.Query, "numResults": clampCount(count, 1, 25, 10)}),
		})
		if errCall != nil {
			return SearchResponse{}, errCall
		}
		return SearchResponse{Sources: mcpTextSources(content, count), AuthMode: "keyless"}, nil
	}
	encoded, errMarshal := json.Marshal(exaRequestBody(req, parsed))
	if errMarshal != nil {
		return SearchResponse{}, &ProviderError{Provider: "Exa", Message: errMarshal.Error()}
	}
	httpReq, errRequest := newJSONRequest(ctx, http.MethodPost, "https://api.exa.ai/search", encoded, map[string]string{
		"x-api-key": key,
		"Accept":    "application/json",
	})
	if errRequest != nil {
		return SearchResponse{}, &ProviderError{Provider: "Exa", Message: errRequest.Error()}
	}
	body, status, errFetch := fetchJSON(ctx, doerFor(cfg), httpReq)
	if errFetch != nil {
		return SearchResponse{}, &ProviderError{Provider: "Exa", Message: errFetch.Error(), Status: status}
	}
	var response SearchResponse
	var summaries []string
	for _, item := range gjson.GetBytes(body, "results").Array() {
		target := strings.TrimSpace(item.Get("url").String())
		if target == "" {
			continue
		}
		title := firstNonEmpty(strings.TrimSpace(item.Get("title").String()), target)
		response.Sources = append(response.Sources, Source{
			Title:     title,
			URL:       target,
			Snippet:   truncateChars(exaSnippet(item), exaMaxSnippetChars),
			Published: strings.TrimSpace(item.Get("publishedDate").String()),
		})
		// The answer is synthesized from Exa's per-result `summary`
		// field only, which is requested via contents.summary.
		if summary := strings.TrimSpace(item.Get("summary").String()); summary != "" && len(summaries) < exaMaxAnswerSummaries {
			summaries = append(summaries, "**"+title+"**: "+summary)
		}
	}
	if len(summaries) > 0 {
		response.Answer = strings.Join(summaries, "\n\n")
	}
	response.Sources = capSources(response.Sources, clampCount(req.ResultCount(), 1, 25, 10))
	return response, nil
}

const (
	// exaMaxSnippetChars caps one result's snippet.
	exaMaxSnippetChars = 500
	// exaMaxAnswerSummaries caps how many per-result summaries are
	// synthesized into the answer.
	exaMaxAnswerSummaries = 3
)

// exaSnippet reads Exa's snippet fields in the order the API prefers:
// a requested per-result `summary`, else the page `text`, else the joined
// `highlights`. Exa has no `snippet` field, so a result carrying none of
// these three genuinely has no snippet.
func exaSnippet(item gjson.Result) string {
	if summary := strings.TrimSpace(item.Get("summary").String()); summary != "" {
		return summary
	}
	if text := strings.TrimSpace(item.Get("text").String()); text != "" {
		return text
	}
	highlights := item.Get("highlights").Array()
	if len(highlights) == 0 {
		return ""
	}
	parts := make([]string, 0, len(highlights))
	for _, highlight := range highlights {
		if value := strings.TrimSpace(highlight.String()); value != "" {
			parts = append(parts, value)
		}
	}
	return strings.Join(parts, " ")
}

// exaQuerySyntax re-emits only quoted phrases: Exa understands every
// other constraint natively as a request field.
var exaQuerySyntax = QuerySyntax{Phrases: true}

// exaRequestBody assembles the Exa request. `site:`/`-site:` and the date
// bounds map onto Exa's native domain and published-date fields rather
// than the query text, which stays plain natural language because Exa's
// neural search re-ranks it.
func exaRequestBody(req SearchRequest, parsed ParsedQuery) map[string]any {
	query := FormatQuery(parsed, exaQuerySyntax)
	body := map[string]any{
		"query":      query,
		"numResults": clampCount(req.ResultCount(), 1, 25, 10),
		"contents": map[string]any{
			"text":    true,
			"summary": map[string]any{"query": query},
		},
	}
	if len(parsed.Sites) > 0 {
		body["includeDomains"] = bareHosts(parsed.Sites)
	}
	if len(parsed.ExcludedSites) > 0 {
		body["excludeDomains"] = bareHosts(parsed.ExcludedSites)
	}
	if parsed.After != "" {
		body["startPublishedDate"] = parsed.After
	}
	if parsed.Before != "" {
		body["endPublishedDate"] = parsed.Before
	}
	return body
}

// searxngProvider queries a self-hosted SearXNG instance.
type searxngProvider struct{}

func (searxngProvider) ID() string    { return ProviderSearXNG }
func (searxngProvider) Label() string { return "SearXNG" }

func (searxngProvider) Available(cfg Config, _ bool) bool {
	return cfg.SearXNGAddress() != ""
}

func (searxngProvider) Search(ctx context.Context, cfg Config, req SearchRequest, _ ParsedQuery) (SearchResponse, error) {
	endpoint := cfg.SearXNGAddress()
	if endpoint == "" {
		return SearchResponse{}, &ProviderError{Provider: "SearXNG", Message: "missing endpoint"}
	}
	requestURL := endpoint + "/search?format=json&q=" + urlQueryEscape(req.Query)
	if timeRange, ok := searxngTimeRange(req.Recency); ok {
		requestURL += "&time_range=" + timeRange
	}
	httpReq, errRequest := newJSONRequest(ctx, http.MethodGet, requestURL, nil, map[string]string{
		"Accept": "application/json",
	})
	if errRequest != nil {
		return SearchResponse{}, &ProviderError{Provider: "SearXNG", Message: errRequest.Error()}
	}
	username, password, token := cfg.SearXNGAuth()
	switch {
	case username != "" && password != "":
		httpReq.SetBasicAuth(username, password)
	case token != "":
		httpReq.Header.Set("Authorization", "Bearer "+token)
	}
	body, status, errFetch := fetchJSON(ctx, doerFor(cfg), httpReq)
	if errFetch != nil {
		return SearchResponse{}, &ProviderError{Provider: "SearXNG", Message: errFetch.Error(), Status: status}
	}
	count := clampCount(EffectiveLimit(cfg, req), 1, 20, 10)
	var response SearchResponse
	for _, item := range gjson.GetBytes(body, "results").Array() {
		target := strings.TrimSpace(item.Get("url").String())
		if target == "" {
			continue
		}
		// SearXNG is a metasearch of upstream engines, so the same field
		// arrives under different names depending on the engine that
		// produced it: content/snippet for the body, and either spelling
		// of the publication date.
		snippet := firstNonEmpty(item.Get("content").String(), item.Get("snippet").String())
		response.Sources = append(response.Sources, Source{
			Title:     firstNonEmpty(strings.TrimSpace(item.Get("title").String()), target),
			URL:       target,
			Snippet:   normalizeSearchText(snippet),
			Published: firstNonEmpty(item.Get("publishedDate").String(), item.Get("published_date").String()),
		})
		if len(response.Sources) >= count {
			break
		}
	}
	for _, suggestion := range gjson.GetBytes(body, "suggestions").Array() {
		if text := strings.TrimSpace(suggestion.String()); text != "" {
			response.Related = append(response.Related, text)
		}
	}
	return response, nil
}

func searxngTimeRange(recency Recency) (string, bool) {
	switch recency {
	case RecencyDay:
		return "day", true
	case RecencyWeek:
		// SearXNG has no week bucket; downgrade to month.
		return "month", true
	case RecencyMonth:
		return "month", true
	case RecencyYear:
		return "year", true
	default:
		return "", false
	}
}
