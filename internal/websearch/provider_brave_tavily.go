package websearch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// braveProvider queries the Brave Search API.
type braveProvider struct{}

func (braveProvider) ID() string    { return ProviderBrave }
func (braveProvider) Label() string { return "Brave" }

func (braveProvider) Available(cfg Config, _ bool) bool {
	return cfg.BraveKey() != ""
}

func (braveProvider) Search(ctx context.Context, cfg Config, req SearchRequest, parsed ParsedQuery) (SearchResponse, error) {
	key := cfg.BraveKey()
	if key == "" {
		return SearchResponse{}, &ProviderError{Provider: "Brave", Message: "missing API key"}
	}
	count := clampCount(req.ResultCount(), 1, 20, 10)
	// Brave parses the classic operators inline, but date bounds map onto
	// the native freshness param, so those tokens are stripped from the
	// rebuilt query rather than sent as literal search terms.
	query := req.Query
	if parsed.HasDirectives {
		query = FormatQuery(parsed, braveQuerySyntax)
	}
	if len(query) > braveMaxQueryChars {
		return SearchResponse{}, &ProviderError{
			Provider: "Brave",
			Message:  "search queries cannot exceed " + strconv.Itoa(braveMaxQueryChars) + " characters",
			Status:   http.StatusBadRequest,
		}
	}
	endpoint, errBuild := url.Parse("https://api.search.brave.com/res/v1/web/search")
	if errBuild != nil {
		return SearchResponse{}, &ProviderError{Provider: "Brave", Message: errBuild.Error()}
	}
	params := endpoint.Query()
	params.Set("q", query)
	params.Set("count", strconv.Itoa(count))
	params.Set("extra_snippets", "true")
	// Without this Brave wraps matched terms in markup inside titles and
	// descriptions, which reaches the model verbatim.
	params.Set("text_decorations", "false")
	params.Set("safesearch", "moderate")
	if freshness, ok := braveFreshness(parsed, req.Recency); ok {
		params.Set("freshness", freshness)
	}
	endpoint.RawQuery = params.Encode()

	httpReq, errRequest := newJSONRequest(ctx, http.MethodGet, endpoint.String(), nil, map[string]string{
		"X-Subscription-Token": key,
		"Accept":               "application/json",
	})
	if errRequest != nil {
		return SearchResponse{}, &ProviderError{Provider: "Brave", Message: errRequest.Error()}
	}
	var respHeader http.Header
	body, status, errFetch := fetchJSONCapture(ctx, doerFor(cfg), httpReq, &respHeader)
	if errFetch != nil {
		return SearchResponse{}, &ProviderError{Provider: "Brave", Message: errFetch.Error(), Status: status}
	}
	var response SearchResponse
	response.AuthMode = "api_key"
	// Brave reports the request id in a response header, not in the body:
	// the JSON payload carries no `request_id` field.
	response.RequestID = firstNonEmpty(
		respHeader.Get("X-Request-Id"),
		respHeader.Get("Request-Id"),
	)
	for _, item := range gjson.GetBytes(body, "web.results").Array() {
		source := Source{
			Title:   firstNonEmpty(strings.TrimSpace(item.Get("title").String()), strings.TrimSpace(item.Get("url").String())),
			URL:     braveResultURL(item.Get("url").String()),
			Snippet: braveSnippet(item),
		}
		if source.URL == "" {
			continue
		}
		if age := strings.TrimSpace(item.Get("age").String()); age != "" {
			source.Published = age
		}
		response.Sources = append(response.Sources, source)
	}
	response.Sources = capSources(response.Sources, count)
	return response, nil
}

const braveMaxQueryChars = 500

// braveQuerySyntax is the full Google operator set minus dateRange, whose
// bounds travel in the native freshness param instead.
var braveQuerySyntax = QuerySyntax{
	Phrases:   true,
	Negation:  true,
	Or:        true,
	Site:      true,
	InURL:     true,
	InTitle:   true,
	InText:    true,
	FileType:  true,
	DateRange: false,
}

// braveResultURL accepts only http(s) targets of sane length.
func braveResultURL(raw string) string {
	url := strings.TrimSpace(raw)
	if url == "" || len(url) > 2048 {
		return ""
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return ""
	}
	return url
}

// braveSnippet merges the description with every extra snippet Brave
// returned, so the paid-for field is not discarded.
func braveSnippet(item gjson.Result) string {
	parts := make([]string, 0, 4)
	appendPart := func(text string) {
		if text = strings.TrimSpace(text); text != "" {
			parts = append(parts, text)
		}
	}
	appendPart(item.Get("description").String())
	appendPart(item.Get("snippet").String())
	for _, extra := range item.Get("extra_snippets").Array() {
		appendPart(extra.String())
	}
	return strings.Join(parts, " ")
}

// braveFreshness prefers explicit date bounds, rendered as Brave's absolute
// range, and falls back to the relative recency period.
func braveFreshness(parsed ParsedQuery, recency Recency) (string, bool) {
	if parsed.After != "" || parsed.Before != "" {
		start := parsed.After
		if start == "" {
			start = "1970-01-01"
		}
		end := parsed.Before
		if end == "" {
			end = time.Now().UTC().Format("2006-01-02")
		}
		return start + "to" + end, true
	}
	switch recency {
	case RecencyDay:
		return "pd", true
	case RecencyWeek:
		return "pw", true
	case RecencyMonth:
		return "pm", true
	case RecencyYear:
		return "py", true
	default:
		return "", false
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// tavilyProvider queries the Tavily search API.
type tavilyProvider struct{}

func (tavilyProvider) ID() string    { return ProviderTavily }
func (tavilyProvider) Label() string { return "Tavily" }

func (tavilyProvider) Available(cfg Config, _ bool) bool {
	return cfg.TavilyKey() != ""
}

func (tavilyProvider) Search(ctx context.Context, cfg Config, req SearchRequest, parsed ParsedQuery) (SearchResponse, error) {
	key := cfg.TavilyKey()
	if key == "" {
		return SearchResponse{}, &ProviderError{Provider: "Tavily", Message: "missing API key"}
	}
	count := clampCount(req.ResultCount(), 5, 20, 5)
	payload := buildTavilyBody(req, parsed, count)
	response, errSearch := tavilyCall(ctx, cfg, key, payload, count)
	if errSearch != nil {
		return SearchResponse{}, errSearch
	}
	// A time filter commonly zeroes out the result set, so retry once
	// without it rather than abandoning a working backend.
	hasTimeFilter := req.Recency != "" || parsed.After != "" || parsed.Before != ""
	if !hasTimeFilter || response.HasRenderableContent() {
		return response, nil
	}
	stripped := buildTavilyBody(req, parsed, count)
	delete(stripped, "time_range")
	delete(stripped, "start_date")
	delete(stripped, "end_date")
	return tavilyCall(ctx, cfg, key, stripped, count)
}

// buildTavilyBody assembles the request. `topic` and `time_range` are
// orthogonal dimensions upstream, so recency is a temporal filter only and
// the index is never narrowed to news: doing that would break technical
// queries whenever a caller asks for recent results. Explicit date bounds
// take precedence over the relative recency window.
func buildTavilyBody(req SearchRequest, parsed ParsedQuery, count int) map[string]any {
	body := map[string]any{
		"query":               FormatQuery(parsed, tavilyQuerySyntax),
		"search_depth":        "basic",
		"max_results":         count,
		"include_answer":      "advanced",
		"include_raw_content": false,
	}
	if len(parsed.Sites) > 0 {
		body["include_domains"] = bareHosts(parsed.Sites)
	}
	if len(parsed.ExcludedSites) > 0 {
		body["exclude_domains"] = bareHosts(parsed.ExcludedSites)
	}
	if parsed.After != "" {
		body["start_date"] = parsed.After
	}
	if parsed.Before != "" {
		body["end_date"] = parsed.Before
	}
	if req.Recency != "" && parsed.After == "" && parsed.Before == "" {
		body["time_range"] = string(req.Recency)
	}
	return body
}

// tavilyQuerySyntax keeps site: and the date bounds out of the query text
// because they map onto dedicated request fields.
var tavilyQuerySyntax = QuerySyntax{
	Phrases:  true,
	Negation: true,
	Or:       true,
	InURL:    true,
	InTitle:  true,
	FileType: true,
}

// bareHosts reduces site values to the host list these APIs expect.
func bareHosts(sites []string) []string {
	unique := make([]string, 0, len(sites))
	seen := map[string]bool{}
	for _, site := range sites {
		host := site
		if slash := strings.IndexByte(host, '/'); slash >= 0 {
			host = host[:slash]
		}
		if host == "" || seen[host] {
			continue
		}
		seen[host] = true
		unique = append(unique, host)
	}
	return unique
}

func tavilyCall(ctx context.Context, cfg Config, key string, payload map[string]any, count int) (SearchResponse, error) {
	encoded, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return SearchResponse{}, &ProviderError{Provider: "Tavily", Message: errMarshal.Error()}
	}
	httpReq, errRequest := newJSONRequest(ctx, http.MethodPost, "https://api.tavily.com/search", encoded, map[string]string{
		"Authorization": "Bearer " + key,
		"Accept":        "application/json",
	})
	if errRequest != nil {
		return SearchResponse{}, &ProviderError{Provider: "Tavily", Message: errRequest.Error()}
	}
	body, status, errFetch := fetchJSON(ctx, doerFor(cfg), httpReq)
	if errFetch != nil {
		return SearchResponse{}, &ProviderError{Provider: "Tavily", Message: errFetch.Error(), Status: status}
	}
	var response SearchResponse
	response.Answer = strings.TrimSpace(gjson.GetBytes(body, "answer").String())
	response.RequestID = gjson.GetBytes(body, "request_id").String()
	response.AuthMode = "api_key"
	for _, item := range gjson.GetBytes(body, "results").Array() {
		url := strings.TrimSpace(item.Get("url").String())
		if url == "" {
			continue
		}
		response.Sources = append(response.Sources, Source{
			Title:     firstNonEmpty(strings.TrimSpace(item.Get("title").String()), url),
			URL:       url,
			Snippet:   strings.TrimSpace(item.Get("content").String()),
			Published: strings.TrimSpace(item.Get("published_date").String()),
		})
	}
	response.Sources = capSources(response.Sources, count)
	return response, nil
}

func tavilyTimeRange(recency Recency) (string, bool) {
	switch recency {
	case RecencyDay:
		return "day", true
	case RecencyWeek:
		return "week", true
	case RecencyMonth:
		return "month", true
	case RecencyYear:
		return "year", true
	default:
		return "", false
	}
}
