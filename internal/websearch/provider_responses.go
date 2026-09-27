package websearch

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// LLM-backed search providers. Both xAI and OpenAI expose an OpenAI-style
// Responses API with a hosted web_search tool, so one non-streaming
// implementation serves both; only endpoint, key, model, and domain-filter
// shape differ. This mirrors OMP's xai/codex search providers and lets
// models without native search (e.g. GLM behind Claude Code) search through
// Grok or Codex backends.

// Default endpoints and models for every provider family live here so the
// adapters stay focused on wire shape.
const (
	// DefaultXAISearchModel grounds xAI searches.
	DefaultXAISearchModel = "grok-4.5"
	// DefaultCodexSearchModel grounds Codex searches.
	DefaultCodexSearchModel = "gpt-5-mini"
	// DefaultAnthropicSearchModel is the cheapest Anthropic model with
	// server-side web search.
	DefaultAnthropicSearchModel = "claude-haiku-4-5"
	// DefaultGeminiSearchModel is the fast grounding model.
	DefaultGeminiSearchModel = "gemini-2.5-flash"
	// DefaultOpenRouterSearchModel grounds on OpenRouter's own routing.
	DefaultOpenRouterSearchModel = "openai/gpt-5-mini"

	defaultXAIBaseURL        = "https://api.x.ai/v1"
	defaultCodexBaseURL      = "https://api.openai.com/v1"
	defaultAnthropicBaseURL  = "https://api.anthropic.com"
	defaultGeminiBaseURL     = "https://generativelanguage.googleapis.com"
	defaultFirecrawlBaseURL  = "https://api.firecrawl.dev"
	defaultTinyFishBaseURL   = "https://api.search.tinyfish.ai"
	defaultKagiBaseURL       = "https://kagi.com/api/v1"
	defaultPerplexityBaseURL = "https://api.perplexity.ai"
	defaultPerplexityAskURL  = "https://www.perplexity.ai/rest/sse/perplexity_ask"
	defaultSyntheticBaseURL  = "https://api.synthetic.new/v2"
	defaultOllamaBaseURL     = "https://ollama.com/api"
	defaultZAIEndpoint       = "https://api.z.ai/api/mcp/web_search_prime/mcp"
	defaultExaMCPURL         = "https://mcp.exa.ai/mcp"
	defaultParallelMCPURL    = "https://search.parallel.ai/mcp"
	defaultKimiBaseURL       = "https://api.kimi.com/coding/v1"
	defaultJinaBaseURL       = "https://s.jina.ai"

	// AnthropicDefaultMaxTokens is used when the request omits max_tokens.
	AnthropicDefaultMaxTokens = 4096

	maxXAIDomains   = 5
	maxCodexDomains = 10
)

// xaiProvider runs searches through the xAI Responses API.
type xaiProvider struct{}

func (xaiProvider) ID() string    { return ProviderXAI }
func (xaiProvider) Label() string { return "xAI" }

func (xaiProvider) Available(cfg Config, _ bool) bool {
	return cfg.XAIKey() != ""
}

func (xaiProvider) Search(ctx context.Context, cfg Config, req SearchRequest, parsed ParsedQuery) (SearchResponse, error) {
	key := cfg.XAIKey()
	if key == "" {
		return SearchResponse{}, &ProviderError{Provider: "xAI", Message: "missing API key"}
	}
	query := req.Query
	tool := []byte(`{"type":"web_search"}`)
	if parsed.HasDirectives {
		// Site directives are consumed by the native domain filters, so the
		// query text is rebuilt without them; every other operator is
		// re-emitted for the search agent.
		query = FormatQuery(parsed, xaiQuerySyntax)
		// The xAI Responses web_search tool nests its domain filters under
		// `filters`; writing them at the tool top level leaves them unread and
		// silently un-filters the search.
		if len(parsed.Sites) > 0 {
			// Allow-list wins over block-list; the tool accepts one or the other.
			tool, _ = sjson.SetRawBytes(tool, "filters.allowed_domains", domainsJSON(parsed.Sites, maxXAIDomains))
		} else if len(parsed.ExcludedSites) > 0 {
			tool, _ = sjson.SetRawBytes(tool, "filters.excluded_domains", domainsJSON(parsed.ExcludedSites, maxXAIDomains))
		}
	}
	return responsesSearch(ctx, cfg, responsesRequest{
		label:        "xAI",
		endpoint:     cfg.XAIBaseURL() + "/responses",
		key:          key,
		model:        cfg.XAISearchModel(),
		query:        query,
		tool:         tool,
		instructions: responsesSearchInstructions,
		input:        xaiResponsesInput,
		lowReasoning: true,
		maxTokens:    req.MaxTokens,
		temperature:  req.Temperature,
		resultCap:    clampCount(req.ResultCount(), 1, 30, 10),
	})
}

// codexProvider runs searches through the OpenAI Responses API.
type codexProvider struct{}

func (codexProvider) ID() string    { return ProviderCodex }
func (codexProvider) Label() string { return "Codex" }

func (codexProvider) Available(cfg Config, _ bool) bool {
	return cfg.CodexKey() != ""
}

func (codexProvider) Search(ctx context.Context, cfg Config, req SearchRequest, parsed ParsedQuery) (SearchResponse, error) {
	key := cfg.CodexKey()
	if key == "" {
		return SearchResponse{}, &ProviderError{Provider: "Codex", Message: "missing API key"}
	}
	// The Responses API omits the retrieved pages unless they are asked for
	// explicitly; without this only the answer text and annotations arrive.
	tool := []byte(`{"type":"web_search","search_context_size":"high"}`)
	query := req.Query
	if parsed.HasDirectives {
		// The ChatGPT backend cannot be trusted to honor the documented
		// domain filter, so the operator is also re-emitted in the query.
		if len(parsed.Sites) > 0 {
			tool, _ = sjson.SetRawBytes(tool, "filters.allowed_domains", domainsJSON(parsed.Sites, maxCodexDomains))
		}
		query = FormatQuery(parsed, GoogleQuerySyntax)
	}
	// A completion that never invoked web_search_call was answered from the
	// model's parametric knowledge. Returning it as a search result would
	// present unsourced text as grounded, so reject it and let the chain
	// advance to a provider that actually searches.
	response, errSearch := responsesSearch(ctx, cfg, responsesRequest{
		label:        "Codex",
		endpoint:     cfg.CodexBaseURL() + "/responses",
		key:          key,
		model:        cfg.CodexSearchModel(),
		query:        query,
		tool:         tool,
		instructions: responsesSearchInstructions,
		input:        codexResponsesInput,
		include:      []string{"web_search_call.action.sources"},
		maxTokens:    req.MaxTokens,
		temperature:  req.Temperature,
		resultCap:    clampCount(req.ResultCount(), 1, 30, 10),
	})
	if errSearch != nil {
		return SearchResponse{}, errSearch
	}
	if !hasWebSearchCall(response) {
		return SearchResponse{}, &ProviderError{
			Provider: "Codex",
			Message:  "model answered without invoking web search",
			Status:   http.StatusBadGateway,
		}
	}
	return response, nil
}

// hasWebSearchCall reports whether the backend actually executed a search.
func hasWebSearchCall(response SearchResponse) bool {
	return len(response.SearchQueries) > 0
}

// domainsJSON builds a host allow/deny list. The Responses web_search
// filters take bare hosts only, so a path-carrying `site:github.com/x`
// value is truncated at the first slash; sending the path makes the backend
// reject or ignore the whole filter and silently un-filter the search.
func domainsJSON(hosts []string, max int) []byte {
	seen := map[string]bool{}
	unique := make([]string, 0, len(hosts))
	for _, host := range hosts {
		host = strings.ToLower(strings.TrimSpace(host))
		if slash := strings.IndexByte(host, '/'); slash >= 0 {
			host = host[:slash]
		}
		host = strings.TrimSuffix(host, ".")
		if host == "" || seen[host] {
			continue
		}
		seen[host] = true
		unique = append(unique, host)
		if len(unique) >= max {
			break
		}
	}
	encoded, errMarshal := json.Marshal(unique)
	if errMarshal != nil {
		return []byte(`[]`)
	}
	return encoded
}

// responsesSearchInstructions is the top-level `instructions` both Responses
// backends receive. Without it the model may answer from parametric knowledge
// and never invoke the hosted tool.
const responsesSearchInstructions = "You are a helpful assistant with web search capabilities. Search the web to answer the user's question accurately and cite your sources."

// xaiQuerySyntax re-emits every operator except site:, which maps onto the
// tool's native domain filters. Date tokens stay in the query text: the
// Responses web_search tool has no date parameters.
var xaiQuerySyntax = QuerySyntax{
	Phrases:   true,
	Negation:  true,
	Or:        true,
	InURL:     true,
	InTitle:   true,
	FileType:  true,
	DateRange: true,
}

// responsesInput builds the request `input` for a query. xAI takes a plain
// role/content pair; OpenAI's Responses API rejects the bare string form, so
// the message is built as a typed input_text item.
type responsesInput func(query string) []byte

func xaiResponsesInput(query string) []byte {
	payload := []byte(`[{"role":"system","content":""},{"role":"user","content":""}]`)
	payload, _ = sjson.SetBytes(payload, "0.content", responsesSearchInstructions)
	payload, _ = sjson.SetBytes(payload, "1.content", query)
	return payload
}

func codexResponsesInput(query string) []byte {
	payload := []byte(`[{"type":"message","role":"user","content":[]}]`)
	payload, _ = sjson.SetBytes(payload, "0.content.0", map[string]string{"type": "input_text", "text": query})
	return payload
}

// responsesRequest is one non-streaming Responses call.
type responsesRequest struct {
	label        string
	endpoint     string
	key          string
	model        string
	query        string
	tool         []byte
	input        responsesInput
	instructions string
	// include asks the API for the retrieved web_search_call sources, which
	// are omitted from the response unless requested.
	include []string
	// lowReasoning keeps the latency-sensitive xAI search agent terse.
	lowReasoning bool
	maxTokens    int
	temperature  float64
	resultCap    int
}

// responsesSearch POSTs one non-streaming Responses request with a hosted
// web_search tool and folds web_search_call items plus url_citation
// annotations into a unified response. resultCap bounds both sources and
// citations; 0 means the provider default.
func responsesSearch(ctx context.Context, cfg Config, request responsesRequest) (SearchResponse, error) {
	payload := []byte(`{"model":"","input":[],"tools":[]}`)
	payload, _ = sjson.SetBytes(payload, "model", request.model)
	payload, _ = sjson.SetRawBytes(payload, "input", request.input(request.query))
	payload, _ = sjson.SetRawBytes(payload, "tools", joinRawJSONArray([][]byte{request.tool}))
	payload, _ = sjson.SetBytes(payload, "instructions", request.instructions)
	if len(request.include) > 0 {
		encoded, errMarshal := json.Marshal(request.include)
		if errMarshal == nil {
			payload, _ = sjson.SetRawBytes(payload, "include", encoded)
		}
	}
	if request.lowReasoning {
		payload, _ = sjson.SetRawBytes(payload, "reasoning", []byte(`{"effort":"low"}`))
	}
	if request.maxTokens > 0 {
		payload, _ = sjson.SetBytes(payload, "max_output_tokens", request.maxTokens)
	}
	if request.temperature > 0 {
		payload, _ = sjson.SetBytes(payload, "temperature", request.temperature)
	}
	httpReq, errRequest := newJSONRequest(ctx, http.MethodPost, request.endpoint, payload, map[string]string{
		"Authorization": "Bearer " + request.key,
		"Accept":        "application/json",
	})
	if errRequest != nil {
		return SearchResponse{}, &ProviderError{Provider: request.label, Message: errRequest.Error()}
	}
	body, status, errFetch := fetchJSON(ctx, doerFor(cfg), httpReq)
	if errFetch != nil {
		return SearchResponse{}, &ProviderError{Provider: request.label, Message: errFetch.Error(), Status: status}
	}
	response := parseResponsesSearch(body)
	response.Model = request.model
	response.AuthMode = "api_key"
	response.RequestID = gjson.GetBytes(body, "id").String()
	response.Usage.InputTokens = gjson.GetBytes(body, "usage.input_tokens").Int()
	response.Usage.OutputTokens = gjson.GetBytes(body, "usage.output_tokens").Int()
	if request.resultCap > 0 {
		response.Sources = capSources(response.Sources, request.resultCap)
		response.Citations = capCitations(response.Citations, request.resultCap)
	}
	return response, nil
}

func capCitations(citations []Citation, count int) []Citation {
	if count > 0 && len(citations) > count {
		return citations[:count]
	}
	return citations
}

// parseResponsesSearch folds the answer text, the executed search queries,
// url_citation annotations, and the retrieved web_search_call sources into a
// unified response. When the backend returns no annotations, markdown links
// and bare URLs from the answer stand in.
func parseResponsesSearch(body []byte) SearchResponse {
	var response SearchResponse
	var answerParts []string
	seen := map[string]bool{}
	// addSource records one cited page as both a source and a citation, the
	// way OMP does: the Responses API reports the same page in both places.
	addSource := func(url, title, citedText string) {
		url = strings.TrimSpace(url)
		if url == "" || seen[url] {
			return
		}
		seen[url] = true
		title = firstNonEmpty(strings.TrimSpace(title), url)
		citedText = strings.TrimSpace(citedText)
		response.Sources = append(response.Sources, Source{
			Title:   title,
			URL:     url,
			Snippet: citedText,
		})
		response.Citations = append(response.Citations, Citation{
			URL:       url,
			Title:     title,
			CitedText: citedText,
		})
	}
	addAnnotations := func(annotations []gjson.Result) {
		for _, annotation := range annotations {
			if annotation.Get("type").String() != "url_citation" {
				continue
			}
			addSource(annotation.Get("url").String(), annotation.Get("title").String(), firstNonEmpty(annotation.Get("cited_text").String(), annotation.Get("text").String()))
		}
	}
	addSearchSources := func(item gjson.Result) {
		if item.Get("type").String() != "web_search_call" {
			return
		}
		// The retrieved pages live under `action.sources`; `sources` and
		// `results` are the shapes other Responses-compatible backends use.
		for _, group := range [][]gjson.Result{
			item.Get("action.sources").Array(),
			item.Get("sources").Array(),
			item.Get("results").Array(),
		} {
			for _, source := range group {
				url := firstNonEmpty(source.Get("url").String(), source.Get("source_website_url").String())
				addSource(url, firstNonEmpty(source.Get("title").String(), source.Get("caption").String()), "")
			}
		}
	}
	addAnnotations(gjson.GetBytes(body, "annotations").Array())
	for _, item := range gjson.GetBytes(body, "output").Array() {
		switch item.Get("type").String() {
		case "web_search_call":
			if query := strings.TrimSpace(item.Get("action.query").String()); query != "" {
				response.SearchQueries = append(response.SearchQueries, query)
			}
			addSearchSources(item)
		case "message":
			addAnnotations(item.Get("annotations").Array())
			for _, part := range item.Get("content").Array() {
				if part.Get("type").String() != "output_text" {
					continue
				}
				text := strings.TrimSpace(part.Get("text").String())
				if text != "" {
					answerParts = append(answerParts, text)
				}
				addAnnotations(part.Get("annotations").Array())
			}
		}
	}
	response.Answer = strings.TrimSpace(strings.Join(answerParts, "\n\n"))
	if len(response.Sources) == 0 && response.Answer != "" {
		for _, link := range extractAnswerLinks(response.Answer) {
			addSource(link.url, link.title, "")
		}
	}
	return response
}

type answerLink struct {
	url   string
	title string
}

var (
	markdownLinkPattern = regexp.MustCompile(`\[([^\]]{1,200})\]\((https?://[^)\s]+)\)`)
	bareURLPattern      = regexp.MustCompile(`https?://[^\s)\]>"]+`)
)

func extractAnswerLinks(answer string) []answerLink {
	var links []answerLink
	seen := map[string]bool{}
	for _, match := range markdownLinkPattern.FindAllStringSubmatch(answer, 30) {
		if len(match) != 3 || seen[match[2]] {
			continue
		}
		seen[match[2]] = true
		links = append(links, answerLink{url: strings.TrimRight(match[2], ".,;"), title: strings.TrimSpace(match[1])})
	}
	for _, raw := range bareURLPattern.FindAllString(answer, 30) {
		trimmed := strings.TrimRight(raw, ".,;)")
		if seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		links = append(links, answerLink{url: trimmed})
	}
	return links
}
