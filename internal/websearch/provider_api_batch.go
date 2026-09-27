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
	"github.com/tidwall/sjson"
)

// tinyFishProvider queries the TinyFish search API. The API has no upstream
// count parameter and returns at most 10 results per page, so larger
// requests fetch successive documented pages and slice locally.
type tinyFishProvider struct{}

func (tinyFishProvider) ID() string    { return ProviderTinyFish }
func (tinyFishProvider) Label() string { return "TinyFish" }

func (tinyFishProvider) Available(cfg Config, _ bool) bool {
	return cfg.TinyFishKey() != ""
}

func (tinyFishProvider) Search(ctx context.Context, cfg Config, req SearchRequest, parsed ParsedQuery) (SearchResponse, error) {
	key := cfg.TinyFishKey()
	if key == "" {
		return SearchResponse{}, &ProviderError{Provider: "TinyFish", Message: "missing API key"}
	}
	count := clampCount(req.ResultCount(), 1, 20, 10)
	// The API has no count field and returns at most 10 results per page,
	// so the page size is capped and larger requests fetch successive
	// pages before slicing.
	pageSize := min(count, 10)
	query := req.Query
	if parsed.HasDirectives {
		query = FormatQuery(parsed, tinyFishQuerySyntax)
	}
	var sources []Source
	seen := map[string]bool{}
	for page := 0; page <= tinyFishMaxPage && len(sources) < count; page++ {
		endpoint, errBuild := url.Parse(defaultTinyFishBaseURL)
		if errBuild != nil {
			return SearchResponse{}, &ProviderError{Provider: "TinyFish", Message: errBuild.Error()}
		}
		params := endpoint.Query()
		params.Set("query", query)
		params.Set("num_results", strconv.Itoa(pageSize))
		if minutes, ok := tinyFishRecencyMinutes(req.Recency); ok {
			params.Set("recency_minutes", strconv.Itoa(minutes))
		}
		if len(parsed.Sites) > 0 {
			params.Set("include_domains", strings.Join(bareHosts(parsed.Sites), ","))
		}
		if len(parsed.ExcludedSites) > 0 {
			params.Set("exclude_domains", strings.Join(bareHosts(parsed.ExcludedSites), ","))
		}
		if location, language := tinyFishLocale(parsed.Lang); language != "" {
			params.Set("language", language)
			if location != "" {
				params.Set("location", location)
			}
		}
		params.Set("page", strconv.Itoa(page))
		endpoint.RawQuery = params.Encode()
		httpReq, errRequest := newJSONRequest(ctx, http.MethodGet, endpoint.String(), nil, map[string]string{
			"X-API-Key": key,
			"Accept":    "application/json",
		})
		if errRequest != nil {
			return SearchResponse{}, &ProviderError{Provider: "TinyFish", Message: errRequest.Error()}
		}
		body, status, errFetch := fetchJSON(ctx, doerFor(cfg), httpReq)
		if errFetch != nil {
			return SearchResponse{}, &ProviderError{Provider: "TinyFish", Message: errFetch.Error(), Status: status}
		}
		results := gjson.GetBytes(body, "results").Array()
		if !gjson.GetBytes(body, "results").IsArray() {
			return SearchResponse{}, &ProviderError{
				Provider: "TinyFish",
				Message:  "search API returned an unexpected response shape",
				Status:   http.StatusBadGateway,
			}
		}
		for _, result := range results {
			url := strings.TrimSpace(result.Get("url").String())
			if url == "" || seen[url] {
				continue
			}
			seen[url] = true
			siteName := strings.TrimSpace(result.Get("site_name").String())
			sources = append(sources, Source{
				Title:   firstNonEmpty(strings.TrimSpace(result.Get("title").String()), siteName, url),
				URL:     url,
				Snippet: strings.TrimSpace(result.Get("snippet").String()),
			})
		}
		// A short page means the index is exhausted.
		if len(results) < pageSize {
			break
		}
	}
	return SearchResponse{Sources: capSources(sources, count), AuthMode: "api_key"}, nil
}

// tinyFishMaxPage bounds the pagination walk documented upstream.
const tinyFishMaxPage = 2

// tinyFishQuerySyntax keeps site: and lang: out of the query text because
// they map onto dedicated request fields.
var tinyFishQuerySyntax = QuerySyntax{
	Phrases:   true,
	Negation:  true,
	Or:        true,
	InURL:     true,
	InTitle:   true,
	InText:    true,
	FileType:  true,
	DateRange: true,
}

// tinyFishLocale derives location (ISO 3166-1 alpha-2) and language
// (ISO 639-1) from a `lang:` directive. A script subtag such as the "hans"
// in zh-hans never becomes a location.
func tinyFishLocale(lang string) (location string, language string) {
	if lang == "" {
		return "", ""
	}
	lowered := strings.ToLower(lang)
	language = lowered[:2]
	rest := lowered[2:]
	if rest == "" {
		return "", language
	}
	rest = strings.TrimLeft(rest, "-_")
	if len(rest) >= 2 {
		if region := rest[:2]; region[0] >= 'a' && region[0] <= 'z' && region[1] >= 'a' && region[1] <= 'z' {
			return strings.ToUpper(region), language
		}
	}
	return "", language
}

func tinyFishRecencyMinutes(recency Recency) (int, bool) {
	switch recency {
	case RecencyDay:
		return 24 * 60, true
	case RecencyWeek:
		return 7 * 24 * 60, true
	case RecencyMonth:
		return 30 * 24 * 60, true
	case RecencyYear:
		return 365 * 24 * 60, true
	default:
		return 0, false
	}
}

// jinaProvider queries the Jina Reader search endpoint, which returns a
// source list only.
type jinaProvider struct{}

func (jinaProvider) ID() string    { return ProviderJina }
func (jinaProvider) Label() string { return "Jina" }

func (jinaProvider) Available(cfg Config, _ bool) bool {
	return cfg.JinaKey() != ""
}

func (jinaProvider) Search(ctx context.Context, cfg Config, req SearchRequest, parsed ParsedQuery) (SearchResponse, error) {
	key := cfg.JinaKey()
	if key == "" {
		return SearchResponse{}, &ProviderError{Provider: "Jina", Message: "missing API key"}
	}
	count := clampCount(req.ResultCount(), 1, 25, 10)
	query := req.Query
	if parsed.HasDirectives {
		query = FormatQuery(parsed, GoogleQuerySyntax)
	}
	endpoint, errBuild := url.Parse(defaultJinaBaseURL + "/" + url.QueryEscape(query))
	if errBuild != nil {
		return SearchResponse{}, &ProviderError{Provider: "Jina", Message: errBuild.Error()}
	}
	params := endpoint.Query()
	// Without an explicit count Jina falls back to its own default
	// breadth, so a larger request would silently return fewer results.
	params.Set("count", strconv.Itoa(count))
	endpoint.RawQuery = params.Encode()
	httpReq, errRequest := newJSONRequest(ctx, http.MethodGet, endpoint.String(), nil, map[string]string{
		"Authorization": "Bearer " + key,
		"Accept":        "application/json",
		// Ask for metadata only: full page bodies would inflate latency
		// and payload size by orders of magnitude.
		"X-Respond-With":  "no-content",
		"X-Retain-Images": "none",
	})
	if errRequest != nil {
		return SearchResponse{}, &ProviderError{Provider: "Jina", Message: errRequest.Error()}
	}
	body, status, errFetch := fetchJSON(ctx, doerFor(cfg), httpReq)
	if errFetch != nil {
		return SearchResponse{}, &ProviderError{Provider: "Jina", Message: errFetch.Error(), Status: status}
	}
	return SearchResponse{
		Sources:  capSources(sourcesFromJSON(gjson.ParseBytes(body), count+1), count),
		AuthMode: "api_key",
	}, nil
}

// kagiProvider queries the Kagi search API. Kagi returns a synthetic
// direct answer plus related questions alongside organic results.
type kagiProvider struct{}

func (kagiProvider) ID() string    { return ProviderKagi }
func (kagiProvider) Label() string { return "Kagi" }

func (kagiProvider) Available(cfg Config, _ bool) bool {
	return cfg.KagiKey() != ""
}

func (kagiProvider) Search(ctx context.Context, cfg Config, req SearchRequest, parsed ParsedQuery) (SearchResponse, error) {
	key := cfg.KagiKey()
	if key == "" {
		return SearchResponse{}, &ProviderError{Provider: "Kagi", Message: "missing API key"}
	}
	count := clampCount(req.ResultCount(), 1, 40, 10)
	// Kagi's index understands the classic Google operator set, so
	// directives are canonicalized and passed through in the query string.
	query := req.Query
	if parsed.HasDirectives {
		query = FormatQuery(parsed, GoogleQuerySyntax)
	}
	payload := map[string]any{
		"query":    query,
		"workflow": "search",
		"limit":    count,
	}
	if after, ok := kagiAfterDate(req.Recency); ok {
		payload["filters"] = map[string]any{"after": after}
	}
	encoded, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return SearchResponse{}, &ProviderError{Provider: "Kagi", Message: errMarshal.Error()}
	}
	httpReq, errRequest := newJSONRequest(ctx, http.MethodPost, defaultKagiBaseURL+"/search", encoded, map[string]string{
		"Authorization": "Bearer " + key,
		"Accept":        "application/json",
	})
	if errRequest != nil {
		return SearchResponse{}, &ProviderError{Provider: "Kagi", Message: errRequest.Error()}
	}
	body, status, errFetch := fetchJSON(ctx, doerFor(cfg), httpReq)
	if errFetch != nil {
		return SearchResponse{}, &ProviderError{Provider: "Kagi", Message: errFetch.Error(), Status: status}
	}
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return SearchResponse{}, &ProviderError{
			Provider: "Kagi",
			Message:  "returned an invalid response: expected an object envelope",
			Status:   http.StatusBadGateway,
		}
	}
	// A 200 can still carry a structured error array.
	if message := kagiErrorMessage(root); message != "" && root.Get("error").Exists() {
		return SearchResponse{}, &ProviderError{Provider: "Kagi", Message: message, Status: status}
	}
	var sources []Source
	// Kagi categorizes results into buckets; only the consumed ones are
	// typed, and non-web buckets are tagged so callers can tell them apart.
	for _, bucket := range []struct {
		path string
		tag  string
	}{
		{"data.search", ""},
		{"data.video", "[Video]"},
		{"data.news", "[News]"},
		{"data.infobox", "[Info]"},
	} {
		for _, item := range root.Get(bucket.path).Array() {
			url := firstNonEmpty(
				strings.TrimSpace(item.Get("url").String()),
				strings.TrimSpace(item.Get("href").String()),
				strings.TrimSpace(item.Get("link").String()),
			)
			if url == "" {
				continue
			}
			title := firstNonEmpty(
				strings.TrimSpace(item.Get("title").String()),
				strings.TrimSpace(item.Get("name").String()),
				url,
			)
			if bucket.tag != "" {
				title = bucket.tag + " " + title
			}
			sources = append(sources, Source{
				Title: title,
				URL:   url,
				Snippet: firstNonEmpty(
					strings.TrimSpace(item.Get("snippet").String()),
					strings.TrimSpace(item.Get("description").String()),
					strings.TrimSpace(item.Get("summary").String()),
				),
				// Kagi V1 reports publication time as `time`.
				Published: strings.TrimSpace(item.Get("time").String()),
			})
		}
	}
	response := SearchResponse{
		Sources:   capSources(sources, count),
		AuthMode:  "api_key",
		RequestID: firstNonEmpty(root.Get("meta.trace").String(), root.Get("meta.id").String()),
	}
	if direct := root.Get("data.direct_answer.0"); direct.Exists() {
		response.Answer = firstNonEmpty(direct.Get("snippet").String(), direct.Get("title").String())
	}
	for _, path := range []string{"data.adjacent_question", "data.related_search"} {
		for _, item := range root.Get(path).Array() {
			if question := kagiQuestionOf(item); question != "" {
				response.Related = append(response.Related, question)
			}
		}
	}
	return response, nil
}

// kagiQuestionOf pulls a related question from an item's props, falling
// back to its title.
func kagiQuestionOf(item gjson.Result) string {
	return firstNonEmpty(
		strings.TrimSpace(item.Get("props.question").String()),
		strings.TrimSpace(item.Get("props.query").String()),
		strings.TrimSpace(item.Get("title").String()),
	)
}

// kagiErrorMessage extracts a diagnostic from the several error shapes
// Kagi returns.
func kagiErrorMessage(root gjson.Result) string {
	if message := strings.TrimSpace(root.Get("message").String()); message != "" {
		return message
	}
	if detail := strings.TrimSpace(root.Get("detail").String()); detail != "" {
		return detail
	}
	if text := strings.TrimSpace(root.Get("error").String()); text != "" {
		return text
	}
	for _, entry := range root.Get("error").Array() {
		for _, key := range []string{"message", "msg", "code"} {
			if value := strings.TrimSpace(entry.Get(key).String()); value != "" {
				return value
			}
		}
	}
	return ""
}

// kagiAfterDate computes the YYYY-MM-DD bound one recency unit back, in
// UTC so the window is deterministic regardless of host timezone. Using
// calendar arithmetic rather than fixed day counts keeps month and leap-year
// boundaries correct.
func kagiAfterDate(recency Recency) (string, bool) {
	now := time.Now().UTC()
	switch recency {
	case RecencyDay:
		return now.AddDate(0, 0, -1).Format("2006-01-02"), true
	case RecencyWeek:
		return now.AddDate(0, 0, -7).Format("2006-01-02"), true
	case RecencyMonth:
		return now.AddDate(0, -1, 0).Format("2006-01-02"), true
	case RecencyYear:
		return now.AddDate(-1, 0, 0).Format("2006-01-02"), true
	default:
		return "", false
	}
}

// firecrawlProvider queries the Firecrawl web search API. It works without
// a credential in a keyless mode that the explicit selection may use.
type firecrawlProvider struct{}

func (firecrawlProvider) ID() string    { return ProviderFirecrawl }
func (firecrawlProvider) Label() string { return "Firecrawl" }

func (firecrawlProvider) Available(cfg Config, _ bool) bool {
	// Keyless mode is reachable, so the provider is always selectable and
	// the chain keeps it available for anyone.
	return true
}

func (firecrawlProvider) Search(ctx context.Context, cfg Config, req SearchRequest, parsed ParsedQuery) (SearchResponse, error) {
	key := cfg.FirecrawlKey()
	count := clampCount(req.ResultCount(), 1, 100, 10)
	// Firecrawl search is SERP-backed, so the query supports the Google
	// operators inline. Absolute date bounds move to the native tbs
	// parameter and are stripped from the query text.
	query := req.Query
	var tbs string
	if parsed.HasDirectives {
		tbs = firecrawlDateTbs(parsed)
		syntax := GoogleQuerySyntax
		if tbs != "" {
			syntax.DateRange = false
		}
		query = FormatQuery(parsed, syntax)
	}
	if tbs == "" {
		if mapped, ok := firecrawlTbs(req.Recency); ok {
			tbs = mapped
		}
	}
	payload := []byte(`{"query":"","limit":0,"sources":[{"type":"web"}]}`)
	payload, _ = sjson.SetBytes(payload, "query", query)
	payload, _ = sjson.SetBytes(payload, "limit", count)
	if tbs != "" {
		payload, _ = sjson.SetBytes(payload, "tbs", tbs)
	}
	headers := map[string]string{"Accept": "application/json", "Content-Type": "application/json"}
	authMode := "keyless"
	if key != "" {
		headers["Authorization"] = "Bearer " + key
		authMode = "api_key"
	}
	httpReq, errRequest := newJSONRequest(ctx, http.MethodPost, cfg.FirecrawlBaseURL()+"/v2/search", payload, headers)
	if errRequest != nil {
		return SearchResponse{}, &ProviderError{Provider: "Firecrawl", Message: errRequest.Error()}
	}
	body, status, errFetch := fetchJSON(ctx, doerFor(cfg), httpReq)
	if errFetch != nil {
		return SearchResponse{}, &ProviderError{Provider: "Firecrawl", Message: errFetch.Error(), Status: status}
	}
	root := gjson.ParseBytes(body)
	// A 200 can still report failure in the body.
	if root.Get("success").Type == gjson.False {
		message := strings.TrimSpace(root.Get("error").String())
		if message == "" {
			message = "request failed"
		}
		return SearchResponse{}, &ProviderError{Provider: "Firecrawl", Message: message, Status: status}
	}
	return SearchResponse{
		Sources:   capSources(firecrawlWebResults(root), count),
		AuthMode:  authMode,
		RequestID: strings.TrimSpace(root.Get("id").String()),
	}, nil
}

// firecrawlWebResults reads the several shapes Firecrawl returns its list
// in: a bare array, a bucketed object, or the legacy `results` field.
func firecrawlWebResults(root gjson.Result) []Source {
	data := root.Get("data")
	if data.IsArray() {
		return firecrawlSources(data.Array())
	}
	if data.IsObject() {
		if web := data.Get("web"); web.IsArray() {
			return firecrawlSources(web.Array())
		}
	}
	if results := root.Get("results"); results.IsArray() {
		return firecrawlSources(results.Array())
	}
	return nil
}

func firecrawlSources(items []gjson.Result) []Source {
	sources := make([]Source, 0, len(items))
	for _, item := range items {
		url := strings.TrimSpace(item.Get("url").String())
		if url == "" {
			continue
		}
		sources = append(sources, Source{
			Title: firstNonEmpty(strings.TrimSpace(item.Get("title").String()), url),
			URL:   url,
			Snippet: firstNonEmpty(
				strings.TrimSpace(item.Get("description").String()),
				strings.TrimSpace(item.Get("snippet").String()),
				strings.TrimSpace(item.Get("markdown").String()),
			),
		})
	}
	return sources
}

// firecrawlDateTbs maps absolute date bounds onto Firecrawl's custom date
// range, rendered in Google's MM/DD/YYYY form.
func firecrawlDateTbs(parsed ParsedQuery) string {
	if parsed.After == "" && parsed.Before == "" {
		return ""
	}
	parts := []string{"cdr:1"}
	if parsed.After != "" {
		parts = append(parts, "cd_min:"+googleStyleDate(parsed.After))
	}
	if parsed.Before != "" {
		parts = append(parts, "cd_max:"+googleStyleDate(parsed.Before))
	}
	return strings.Join(parts, ",")
}

// googleStyleDate converts ISO YYYY-MM-DD to MM/DD/YYYY.
func googleStyleDate(iso string) string {
	parts := strings.Split(iso, "-")
	if len(parts) != 3 {
		return iso
	}
	month, errMonth := strconv.Atoi(parts[1])
	day, errDay := strconv.Atoi(parts[2])
	if errMonth != nil || errDay != nil {
		return iso
	}
	return strconv.Itoa(month) + "/" + strconv.Itoa(day) + "/" + parts[0]
}

func firecrawlTbs(recency Recency) (string, bool) {
	switch recency {
	case RecencyDay:
		return "qdr:d", true
	case RecencyWeek:
		return "qdr:w", true
	case RecencyMonth:
		return "qdr:m", true
	case RecencyYear:
		return "qdr:y", true
	default:
		return "", false
	}
}

func firecrawlRequestID(body []byte) string {
	return gjson.GetBytes(body, "data.requestId").String()
}

// kimiProvider queries the Kimi search service. The Kimi Code search key is
// distinct from the Open Platform key, so only search-scoped credentials
// are consulted.
type kimiProvider struct{}

func (kimiProvider) ID() string    { return ProviderKimi }
func (kimiProvider) Label() string { return "Kimi" }

func (kimiProvider) Available(cfg Config, _ bool) bool {
	return cfg.KimiKey() != ""
}

func (kimiProvider) Search(ctx context.Context, cfg Config, req SearchRequest, parsed ParsedQuery) (SearchResponse, error) {
	key := cfg.KimiKey()
	if key == "" {
		return SearchResponse{}, &ProviderError{Provider: "Kimi", Message: "missing search API key"}
	}
	count := clampCount(req.ResultCount(), 1, 20, 10)
	// Page crawling is opt-in: enabling it by default makes every search
	// fetch full page bodies, which is markedly slower and larger.
	encoded, errMarshal := json.Marshal(map[string]any{
		"text_query":           req.Query,
		"limit":                count,
		"enable_page_crawling": false,
		"timeout_seconds":      30,
	})
	if errMarshal != nil {
		return SearchResponse{}, &ProviderError{Provider: "Kimi", Message: errMarshal.Error()}
	}
	httpReq, errRequest := newJSONRequest(ctx, http.MethodPost, cfg.KimiBaseURL()+"/search", encoded, map[string]string{
		"Authorization": "Bearer " + key,
		"Accept":        "application/json",
	})
	if errRequest != nil {
		return SearchResponse{}, &ProviderError{Provider: "Kimi", Message: errRequest.Error()}
	}
	body, status, errFetch := fetchJSON(ctx, doerFor(cfg), httpReq)
	if errFetch != nil {
		return SearchResponse{}, &ProviderError{Provider: "Kimi", Message: errFetch.Error(), Status: status}
	}
	root := gjson.ParseBytes(body)
	var sources []Source
	for _, result := range root.Get("search_results").Array() {
		url := strings.TrimSpace(result.Get("url").String())
		if url == "" {
			continue
		}
		sources = append(sources, Source{
			Title:     firstNonEmpty(strings.TrimSpace(result.Get("title").String()), url),
			URL:       url,
			Snippet:   firstNonEmpty(strings.TrimSpace(result.Get("snippet").String()), strings.TrimSpace(result.Get("content").String())),
			Published: strings.TrimSpace(result.Get("date").String()),
		})
	}
	_ = parsed
	return SearchResponse{
		Sources:  capSources(sources, count),
		AuthMode: "api_key",
	}, nil
}

// parallelProvider queries the Parallel search API. Without a credential
// the explicit selection may use the public MCP endpoint instead.
type parallelProvider struct{}

func (parallelProvider) ID() string    { return ProviderParallel }
func (parallelProvider) Label() string { return "Parallel" }

func (parallelProvider) Available(cfg Config, _ bool) bool {
	// The keyless MCP path makes Parallel reachable for every request, so
	// it always qualifies.
	return true
}

func (parallelProvider) Search(ctx context.Context, cfg Config, req SearchRequest, parsed ParsedQuery) (SearchResponse, error) {
	count := clampCount(req.ResultCount(), 1, 40, 10)
	if key := cfg.ParallelKey(); key != "" {
		return parallelAuthenticatedSearch(ctx, cfg, key, req, parsed, count)
	}
	// Operator-preserving query text keeps site: and -site: filters intact.
	searchQueries := parallelSearchQueries(req.Query, parsed)
	content, errCall := mcpCall(ctx, cfg, "Parallel", defaultParallelMCPURL, "web_search", mcpHeaders(""), [][]byte{
		mcpArgumentBody(map[string]any{
			"objective":      req.Query,
			"search_queries": searchQueries,
		}),
	})
	if errCall != nil {
		return SearchResponse{}, errCall
	}
	return SearchResponse{Sources: mcpTextSources(content, count), AuthMode: "keyless"}, nil
}

func parallelAuthenticatedSearch(ctx context.Context, cfg Config, key string, req SearchRequest, parsed ParsedQuery, count int) (SearchResponse, error) {
	encoded, errMarshal := json.Marshal(map[string]any{
		"objective":            req.Query,
		"search_queries":       parallelSearchQueries(req.Query, parsed),
		"mode":                 "fast",
		"max_chars_per_result": 10000,
		"max_results":          count,
	})
	if errMarshal != nil {
		return SearchResponse{}, &ProviderError{Provider: "Parallel", Message: errMarshal.Error()}
	}
	httpReq, errRequest := newJSONRequest(ctx, http.MethodPost, "https://api.parallel.ai/v1beta/search", encoded, map[string]string{
		"Authorization": "Bearer " + key,
		"Accept":        "application/json",
		"parallel-beta": "search-extract-2025-10-10",
	})
	if errRequest != nil {
		return SearchResponse{}, &ProviderError{Provider: "Parallel", Message: errRequest.Error()}
	}
	body, status, errFetch := fetchJSON(ctx, doerFor(cfg), httpReq)
	if errFetch != nil {
		return SearchResponse{}, &ProviderError{Provider: "Parallel", Message: errFetch.Error(), Status: status}
	}
	return SearchResponse{
		Sources:   sourcesFromJSON(gjson.ParseBytes(body), count),
		AuthMode:  "api_key",
		RequestID: gjson.GetBytes(body, "search_id").String(),
	}, nil
}

// parallelSearchQueries preserves operator tokens instead of reformatting
// them, and always sends exactly one query.
func parallelSearchQueries(query string, parsed ParsedQuery) []string {
	parts := make([]string, 0, 8)
	if text := strings.TrimSpace(parsed.Text); text != "" {
		parts = append(parts, text)
	}
	for _, site := range parsed.Sites {
		parts = append(parts, "site:"+site)
	}
	for _, site := range parsed.ExcludedSites {
		parts = append(parts, "-site:"+site)
	}
	if len(parts) == 0 {
		return []string{query}
	}
	return []string{strings.Join(parts, " ")}
}

// syntheticProvider queries the Synthetic search API.
type syntheticProvider struct{}

func (syntheticProvider) ID() string    { return ProviderSynthetic }
func (syntheticProvider) Label() string { return "Synthetic" }

func (syntheticProvider) Available(cfg Config, _ bool) bool {
	return cfg.SyntheticKey() != ""
}

func (syntheticProvider) Search(ctx context.Context, cfg Config, req SearchRequest, _ ParsedQuery) (SearchResponse, error) {
	key := cfg.SyntheticKey()
	if key == "" {
		return SearchResponse{}, &ProviderError{Provider: "Synthetic", Message: "missing API key"}
	}
	encoded, errMarshal := json.Marshal(map[string]any{"query": req.Query})
	if errMarshal != nil {
		return SearchResponse{}, &ProviderError{Provider: "Synthetic", Message: errMarshal.Error()}
	}
	httpReq, errRequest := newJSONRequest(ctx, http.MethodPost, defaultSyntheticBaseURL+"/search", encoded, map[string]string{
		"Authorization": "Bearer " + key,
		"Accept":        "application/json",
	})
	if errRequest != nil {
		return SearchResponse{}, &ProviderError{Provider: "Synthetic", Message: errRequest.Error()}
	}
	body, status, errFetch := fetchJSON(ctx, doerFor(cfg), httpReq)
	if errFetch != nil {
		return SearchResponse{}, &ProviderError{Provider: "Synthetic", Message: errFetch.Error(), Status: status}
	}
	count := clampCount(EffectiveLimit(cfg, req), 1, 20, 10)
	return SearchResponse{
		Sources:  capSources(sourcesFromJSON(gjson.ParseBytes(body), count+1), count),
		AuthMode: "api_key",
	}, nil
}

// ollamaProvider queries the Ollama Cloud web search endpoint.
type ollamaProvider struct{}

func (ollamaProvider) ID() string    { return ProviderOllama }
func (ollamaProvider) Label() string { return "Ollama" }

func (ollamaProvider) Available(cfg Config, _ bool) bool {
	return cfg.OllamaKey() != ""
}

func (ollamaProvider) Search(ctx context.Context, cfg Config, req SearchRequest, parsed ParsedQuery) (SearchResponse, error) {
	key := cfg.OllamaKey()
	if key == "" {
		return SearchResponse{}, &ProviderError{Provider: "Ollama", Message: "missing API key"}
	}
	count := clampCount(req.ResultCount(), 1, 10, 5)
	// The endpoint understands only quotes, negation, and site:; every other
	// operator is dropped rather than passed through as noise.
	query := req.Query
	if parsed.HasDirectives {
		query = FormatQuery(parsed, QuerySyntax{Phrases: true, Negation: true, Site: true})
	}
	encoded, errMarshal := json.Marshal(map[string]any{
		"query":       query,
		"max_results": count,
	})
	if errMarshal != nil {
		return SearchResponse{}, &ProviderError{Provider: "Ollama", Message: errMarshal.Error()}
	}
	httpReq, errRequest := newJSONRequest(ctx, http.MethodPost, defaultOllamaBaseURL+"/web_search", encoded, map[string]string{
		"Authorization": "Bearer " + key,
		"Accept":        "application/json",
	})
	if errRequest != nil {
		return SearchResponse{}, &ProviderError{Provider: "Ollama", Message: errRequest.Error()}
	}
	body, status, errFetch := fetchJSON(ctx, doerFor(cfg), httpReq)
	if errFetch != nil {
		return SearchResponse{}, &ProviderError{Provider: "Ollama", Message: errFetch.Error(), Status: status}
	}
	return SearchResponse{
		Sources:  capSources(sourcesFromJSON(gjson.ParseBytes(body), count+1), count),
		AuthMode: "api_key",
	}, nil
}

// capSources bounds a source list without allocating a new slice when the
// input already fits.
func capSources(sources []Source, count int) []Source {
	if count > 0 && len(sources) > count {
		return sources[:count]
	}
	return sources
}
