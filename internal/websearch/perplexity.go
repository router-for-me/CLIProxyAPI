package websearch

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// perplexityProvider answers searches with Perplexity's Sonar models. The
// API key path uses the hosted chat-completions endpoint; the OAuth, cookie,
// and anonymous paths use the consumer ask endpoint, which has a different
// request shape, credential mechanism, and stream format.
type perplexityProvider struct{}

func (perplexityProvider) ID() string    { return ProviderPerplexity }
func (perplexityProvider) Label() string { return "Perplexity" }

// Available requires real Perplexity auth for the automatic chain; an
// explicit selection may fall back to the anonymous ask endpoint.
func (perplexityProvider) Available(cfg Config, explicit bool) bool {
	if cfg.PerplexityKey() != "" || cfg.PerplexityToken() != "" || perplexityCookies(cfg) != "" {
		return true
	}
	return explicit
}

func (perplexityProvider) Search(ctx context.Context, cfg Config, req SearchRequest, parsed ParsedQuery) (SearchResponse, error) {
	// The auth ladder is ordered: a working API key is the cheapest path, a
	// raw cookie header authenticates the consumer session directly, the
	// OAuth session JWT unlocks the account's paid model, and the anonymous
	// ask endpoint always works. One failure advances to the next credential
	// rather than failing the whole provider.
	if key := cfg.PerplexityKey(); key != "" {
		response, errSearch := perplexityAPIKeySearch(ctx, cfg, key, req, parsed)
		if errSearch == nil {
			return response, nil
		}
		if ctx.Err() != nil {
			return SearchResponse{}, errSearch
		}
	}
	if cookies := perplexityCookies(cfg); cookies != "" {
		response, errSearch := perplexityAsk(ctx, cfg, req, parsed, perplexityAuth{kind: "cookies", token: cookies})
		if errSearch == nil {
			return response, nil
		}
		if ctx.Err() != nil {
			return SearchResponse{}, errSearch
		}
	}
	if token := cfg.PerplexityToken(); token != "" {
		response, errSearch := perplexityAsk(ctx, cfg, req, parsed, perplexityAuth{kind: "oauth", token: token})
		if errSearch == nil {
			return response, nil
		}
		if ctx.Err() != nil {
			return SearchResponse{}, errSearch
		}
	}
	return perplexityAsk(ctx, cfg, req, parsed, perplexityAuth{kind: "anonymous"})
}

// Perplexity API-key mode defaults.
const (
	perplexityAPIMaxTokens   = 8192
	perplexityAPITemperature = 0.2
	perplexityAPINumResults  = 20
)

// perplexityAPIKeySearch calls the hosted chat-completions endpoint with
// Sonar's web search mode enabled.
func perplexityAPIKeySearch(ctx context.Context, cfg Config, key string, req SearchRequest, parsed ParsedQuery) (SearchResponse, error) {
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = perplexityAPIMaxTokens
	}
	temperature := req.Temperature
	if temperature <= 0 {
		temperature = perplexityAPITemperature
	}
	numResults := clampCount(req.NumSearchResults, 1, 20, perplexityAPINumResults)

	payload := []byte(`{"model":"sonar-pro","messages":[{"role":"user","content":""}],"search_mode":"web"}`)
	payload, _ = sjson.SetBytes(payload, "messages.0.content", FormatQuery(parsed, perplexityAPISyntax))
	payload, _ = sjson.SetBytes(payload, "num_search_results", numResults)
	payload, _ = sjson.SetBytes(payload, "max_tokens", maxTokens)
	payload, _ = sjson.SetBytes(payload, "temperature", temperature)
	// Ask whether the model should retrieve, and ask for related questions;
	// the API key path is a proper chat endpoint, so a system message is safe.
	payload, _ = sjson.SetRawBytes(payload, "web_search_options", []byte(`{"search_type":"pro","search_context_size":"high"}`))
	payload, _ = sjson.SetBytes(payload, "enable_search_classifier", true)
	payload, _ = sjson.SetBytes(payload, "return_related_questions", true)
	payload, _ = sjson.SetBytes(payload, "reasoning_effort", "medium")
	payload, _ = sjson.SetBytes(payload, "language_preference", "en")
	// site:/date:/lang: directives map onto native request fields; the query
	// text is rebuilt without them so the engine is not double-constrained.
	if len(parsed.Sites) > 0 || len(parsed.ExcludedSites) > 0 {
		payload, _ = sjson.SetRawBytes(payload, "search_domain_filter", perplexityDomainFilter(parsed))
	}
	if parsed.After != "" {
		payload, _ = sjson.SetBytes(payload, "search_after_date_filter", perplexityDate(parsed.After))
	}
	if parsed.Before != "" {
		payload, _ = sjson.SetBytes(payload, "search_before_date_filter", perplexityDate(parsed.Before))
	}
	if parsed.Lang != "" {
		if code := perplexityLangCode(parsed.Lang); code != "" {
			payload, _ = sjson.SetRawBytes(payload, "search_language_filter", []byte(`["`+code+`"]`))
		}
	}
	// Recency cannot combine with absolute date bounds.
	if recency, ok := perplexityRecencyFilter(req.Recency); ok && parsed.After == "" && parsed.Before == "" {
		payload, _ = sjson.SetBytes(payload, "search_recency_filter", recency)
	}

	httpReq, errRequest := newJSONRequest(ctx, "POST", perplexityAPIURL, payload, map[string]string{
		"Authorization": "Bearer " + key,
		"Accept":        "application/json",
	})
	if errRequest != nil {
		return SearchResponse{}, &ProviderError{Provider: "Perplexity", Message: errRequest.Error()}
	}
	body, status, errFetch := fetchJSON(ctx, doerFor(cfg), httpReq)
	if errFetch != nil {
		return SearchResponse{}, &ProviderError{Provider: "Perplexity", Message: errFetch.Error(), Status: status}
	}

	root := gjson.ParseBytes(body)
	var response SearchResponse
	response.Answer = strings.TrimSpace(root.Get("choices.0.message.content").String())
	response.Model = firstNonEmpty(root.Get("model").String(), "sonar-pro")
	response.RequestID = root.Get("id").String()
	response.AuthMode = "api_key"
	response.Usage.InputTokens = root.Get("usage.prompt_tokens").Int()
	response.Usage.OutputTokens = root.Get("usage.completion_tokens").Int()

	// The backend reports the query it actually ran as a single string;
	// never invent one. gjson's Array() yields that string as one element.
	for _, item := range root.Get("search_query").Array() {
		if query := strings.TrimSpace(item.String()); query != "" {
			response.SearchQueries = append(response.SearchQueries, query)
		}
	}
	// The API reports titled, snippet-bearing hits under `search_results`
	// and the URLs the answer actually cites under `citations`. Citations
	// win, because the answer text references them: a citation with no
	// matching result keeps its URL, and one that does match inherits the
	// result's title, snippet, and date.
	response.Sources = capSources(perplexityAPIBuildSources(root), perplexitySourceCount(req))
	for _, question := range root.Get("related_questions").Array() {
		if text := strings.TrimSpace(question.String()); text != "" {
			response.Related = append(response.Related, text)
		}
	}
	return response, nil
}

const perplexityAPIURL = "https://api.perplexity.ai/chat/completions"

// perplexityAPISyntax keeps site:, dates, and lang: out of the query text
// because the API carries them as structured fields.
var perplexityAPISyntax = QuerySyntax{
	Phrases:  true,
	Negation: true,
	Or:       true,
	InURL:    true,
	InTitle:  true,
	InText:   true,
	FileType: true,
}

func perplexityRecencyFilter(recency Recency) (string, bool) {
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

// perplexityAPIBuildSources turns the API payload into sources. Citation
// URLs take precedence over the raw result list, because the answer text
// references the citations: a citation without a matching result keeps its
// URL, and one with a match inherits that result's title, snippet, and
// date. With no citations at all, every titled result is a source.
func perplexityAPIBuildSources(root gjson.Result) []Source {
	results := perplexityAPISearchResults(root)
	citations := perplexityCitationURLs(root)
	if len(citations) == 0 {
		sources := make([]Source, 0, len(results))
		for _, result := range results {
			sources = append(sources, Source{
				Title:     firstNonEmpty(result.Title, result.URL),
				URL:       result.URL,
				Snippet:   result.Snippet,
				Published: result.Published,
			})
		}
		return sources
	}
	sources := make([]Source, 0, len(citations))
	for _, url := range citations {
		source := Source{Title: url, URL: url}
		for _, result := range results {
			if result.URL == url {
				source.Title = firstNonEmpty(result.Title, url)
				source.Snippet = result.Snippet
				source.Published = result.Published
				break
			}
		}
		sources = append(sources, source)
	}
	return sources
}

// perplexityAPISearchResults reads the titled, snippet-bearing hits the
// chat-completions response reports under `search_results`.
func perplexityAPISearchResults(root gjson.Result) []Source {
	var results []Source
	for _, result := range root.Get("search_results").Array() {
		url := strings.TrimSpace(result.Get("url").String())
		if url == "" {
			continue
		}
		results = append(results, Source{
			Title:     strings.TrimSpace(result.Get("title").String()),
			URL:       url,
			Snippet:   strings.TrimSpace(result.Get("snippet").String()),
			Published: strings.TrimSpace(result.Get("date").String()),
		})
	}
	return results
}

func perplexityCitationURLs(root gjson.Result) []string {
	var citations []string
	for _, item := range root.Get("citations").Array() {
		if url := strings.TrimSpace(item.String()); url != "" {
			citations = append(citations, url)
		}
	}
	return citations
}

// perplexitySourceCount caps returned sources. The API asks for up to 20
// results, so a larger request limit never buys more hits.
func perplexitySourceCount(req SearchRequest) int {
	return clampCount(req.Limit, 1, 20, perplexityAPINumResults)
}

// perplexityDomainFilter maps site:/-site: onto the API's allow/deny array.
// Entries are bare hosts (the path part is enforced by the central lenient
// filter) and the API caps the array at 20 entries.
func perplexityDomainFilter(parsed ParsedQuery) []byte {
	domains := make([]string, 0, len(parsed.Sites)+len(parsed.ExcludedSites))
	seen := map[string]bool{}
	appendHost := func(host string) {
		if host == "" || seen[host] {
			return
		}
		seen[host] = true
		domains = append(domains, host)
	}
	for _, site := range parsed.Sites {
		appendHost(perplexitySiteHost(site))
	}
	for _, site := range parsed.ExcludedSites {
		appendHost("-" + perplexitySiteHost(site))
	}
	return domainsToJSON(domains)
}

// perplexityCookies resolves the consumer cookie header, which
// authenticates the ask endpoint directly and outranks the OAuth token.
func perplexityCookies(cfg Config) string {
	return cfg.PerplexityCookieHeader()
}

func jsonMarshalStrings(values []string) ([]byte, error) {
	return json.Marshal(values)
}

var _ = strconv.Itoa
