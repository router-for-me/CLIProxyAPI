package websearch

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// geminiProvider answers searches through the Google generative-language
// API with Search grounding enabled. Grounding metadata carries the
// executed queries, the cited chunks, and the segment-level supports that
// back each citation.
type geminiProvider struct{}

func (geminiProvider) ID() string    { return ProviderGemini }
func (geminiProvider) Label() string { return "Gemini" }

func (geminiProvider) Available(cfg Config, _ bool) bool {
	return cfg.GeminiKey() != ""
}

func (geminiProvider) Search(ctx context.Context, cfg Config, req SearchRequest, parsed ParsedQuery) (SearchResponse, error) {
	key := cfg.GeminiKey()
	if key == "" {
		return SearchResponse{}, &ProviderError{Provider: "Gemini", Message: "missing API key"}
	}
	count := req.ResultCount()
	if count <= 0 {
		count = EffectiveLimit(cfg, req)
	}
	requestedModel := cfg.GeminiSearchModel()
	// Gemini's googleSearch grounding forwards the query to Google Search,
	// which understands the classic operator set natively, so directives are
	// canonicalized to Google forms. Directive-free queries pass through
	// byte-identical.
	query := req.Query
	if parsed.HasDirectives {
		query = FormatQuery(parsed, GoogleQuerySyntax)
	}
	// The tool key is camelCase `googleSearch`; the snake_case spelling is
	// silently ignored, which leaves the request ungrounded.
	payload := []byte(`{"contents":[{"role":"user","parts":[{"text":""}]}],"tools":[{"googleSearch":{}}]}`)
	payload, _ = sjson.SetBytes(payload, "contents.0.parts.0.text", query)
	if req.MaxTokens > 0 {
		payload, _ = sjson.SetBytes(payload, "generationConfig.maxOutputTokens", req.MaxTokens)
	}
	if req.Temperature > 0 {
		payload, _ = sjson.SetBytes(payload, "generationConfig.temperature", req.Temperature)
	}
	endpoint := cfg.GeminiBaseURL() + "/v1beta/models/" + requestedModel + ":streamGenerateContent?alt=sse"
	resp, errRequest := fetchWithRetry(ctx, cfg, "Gemini", endpoint, payload, map[string]string{
		"x-goog-api-key": key,
		"Accept":         "text/event-stream",
	})
	if errRequest != nil {
		return SearchResponse{}, errRequest
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.WithError(errClose).Debug("websearch: close gemini response body")
		}
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		// Never echo the API key back inside an upstream error body.
		message := strings.ReplaceAll(summarizeErrorBody(body), key, "[redacted]")
		return SearchResponse{}, &ProviderError{Provider: "Gemini", Message: message, Status: resp.StatusCode}
	}

	// Grounding metadata must be read per stream chunk: the
	// `groundingSupports.groundingChunkIndices` in one chunk are relative to
	// that chunk's own chunk list, so merging chunk lists across the stream
	// attributes citations to the wrong pages.
	var answer strings.Builder
	var sources []Source
	var citations []Citation
	var queries []string
	usage := SearchUsage{}
	seenURLs := map[string]bool{}
	model := requestedModel
	errStream := sseLines(resp.Body, func(data []byte) error {
		candidate, responseData := geminiChunkCandidate(data)
		answer.WriteString(geminiChunkText(candidate))
		metadata := candidate.Get("groundingMetadata")
		if metadata.Exists() {
			for _, chunk := range metadata.Get("groundingChunks").Array() {
				url := strings.TrimSpace(chunk.Get("web.uri").String())
				if url == "" || seenURLs[url] {
					continue
				}
				seenURLs[url] = true
				sources = append(sources, Source{
					Title: firstNonEmpty(strings.TrimSpace(chunk.Get("web.title").String()), url),
					URL:   url,
				})
			}
			chunks := metadata.Get("groundingChunks").Array()
			for _, support := range metadata.Get("groundingSupports").Array() {
				citedText := strings.TrimSpace(support.Get("segment.text").String())
				for _, index := range support.Get("groundingChunkIndices").Array() {
					position := int(index.Int())
					if position < 0 || position >= len(chunks) {
						continue
					}
					url := strings.TrimSpace(chunks[position].Get("web.uri").String())
					if url == "" {
						continue
					}
					citations = append(citations, Citation{
						URL:       url,
						Title:     firstNonEmpty(strings.TrimSpace(chunks[position].Get("web.title").String()), url),
						CitedText: citedText,
					})
				}
			}
			for _, query := range metadata.Get("webSearchQueries").Array() {
				if text := strings.TrimSpace(query.String()); text != "" {
					queries = append(queries, text)
				}
			}
		}
		usage = mergeGeminiUsage(usage, []byte(responseData.Raw))
		if version := responseData.Get("modelVersion").String(); version != "" {
			model = version
		}
		return nil
	})
	if errStream != nil {
		return SearchResponse{}, &ProviderError{Provider: "Gemini", Message: errStream.Error()}
	}
	return SearchResponse{
		Answer:        strings.TrimSpace(answer.String()),
		Sources:       capSources(sources, count),
		Citations:     citations,
		SearchQueries: dedupeStrings(queries),
		Model:         model,
		AuthMode:      "api_key",
		Usage:         usage,
	}, nil
}

// geminiChunkCandidate returns the candidate and its enclosing response
// object, tolerating both the bare and enveloped stream shapes.
func geminiChunkCandidate(data []byte) (gjson.Result, gjson.Result) {
	root := gjson.ParseBytes(data)
	if response := root.Get("response"); response.IsObject() {
		return response.Get("candidates.0"), response
	}
	return root.Get("candidates.0"), root
}

// dedupeStrings preserves first-seen order.
func dedupeStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		unique = append(unique, value)
	}
	return unique
}

// geminiChunkText concatenates every text part of one stream candidate.
func geminiChunkText(candidate gjson.Result) string {
	var out strings.Builder
	for _, part := range candidate.Get("content.parts").Array() {
		if text := part.Get("text").String(); text != "" {
			out.WriteString(text)
		}
	}
	return out.String()
}

// mergeGeminiUsage keeps the highest token counts observed across the
// stream, since each chunk carries a cumulative or final usageMetadata.
func mergeGeminiUsage(previous SearchUsage, data []byte) SearchUsage {
	root := gjson.ParseBytes(data)
	metadata := root.Get("usageMetadata")
	if !metadata.Exists() {
		metadata = root.Get("response.usageMetadata")
	}
	if !metadata.Exists() {
		return previous
	}
	previous.InputTokens = maxInt64(previous.InputTokens, metadata.Get("promptTokenCount").Int())
	previous.OutputTokens = maxInt64(previous.OutputTokens, metadata.Get("candidatesTokenCount").Int())
	return previous
}

func maxInt64(left, right int64) int64 {
	if right > left {
		return right
	}
	return left
}

// mergeGeminiGrounding unions two grounding metadata objects, preserving
// every query, chunk, and support seen across the stream.
func mergeGeminiGrounding(previous gjson.Result, data []byte) gjson.Result {
	current := geminiGroundingRoot(data)
	if !current.Exists() {
		return previous
	}
	if !previous.Exists() {
		return current
	}
	merged := []byte(previous.Raw)
	if queries := append(
		append([]string{}, gjsonResultsToStrings(previous.Get("webSearchQueries").Array())...),
		gjsonResultsToStrings(current.Get("webSearchQueries").Array())...,
	); len(queries) > 0 {
		if raw, errMarshal := json.Marshal(queries); errMarshal == nil {
			merged, _ = sjson.SetRawBytes(merged, "webSearchQueries", raw)
		}
	}
	if chunks := append(previous.Get("groundingChunks").Array(), current.Get("groundingChunks").Array()...); len(chunks) > 0 {
		if raw, errMarshal := json.Marshal(chunks); errMarshal == nil {
			merged, _ = sjson.SetRawBytes(merged, "groundingChunks", raw)
		}
	}
	if supports := append(previous.Get("groundingSupports").Array(), current.Get("groundingSupports").Array()...); len(supports) > 0 {
		if raw, errMarshal := json.Marshal(supports); errMarshal == nil {
			merged, _ = sjson.SetRawBytes(merged, "groundingSupports", raw)
		}
	}
	return gjson.ParseBytes(merged)
}

// geminiGroundingRoot locates the grounding metadata across the shapes the
// generative-language API emits: nested in a candidate, nested under a
// non-stream response envelope, or at the response root.
func geminiGroundingRoot(data []byte) gjson.Result {
	root := gjson.ParseBytes(data)
	for _, path := range []string{
		"candidates.0.groundingMetadata",
		"response.candidates.0.groundingMetadata",
		"groundingMetadata",
	} {
		if metadata := root.Get(path); metadata.Exists() {
			return metadata
		}
	}
	return gjson.Result{}
}

func geminiGroundingQueries(metadata gjson.Result) []string {
	if !metadata.Exists() {
		return nil
	}
	return gjsonResultsToStrings(metadata.Get("webSearchQueries").Array())
}

func geminiGroundingSources(metadata gjson.Result, count int) []Source {
	if !metadata.Exists() {
		return nil
	}
	var sources []Source
	seen := map[string]bool{}
	for _, chunk := range metadata.Get("groundingChunks").Array() {
		web := chunk.Get("web")
		if !web.Exists() {
			continue
		}
		url := strings.TrimSpace(web.Get("uri").String())
		if url == "" || seen[url] {
			continue
		}
		seen[url] = true
		sources = append(sources, Source{Title: strings.TrimSpace(web.Get("title").String()), URL: url})
	}
	return capSources(sources, count)
}

// geminiGroundingCitations pairs each grounding support segment with the
// chunks it cites so the model can attribute claims.
func geminiGroundingCitations(metadata gjson.Result) []Citation {
	if !metadata.Exists() {
		return nil
	}
	chunks := metadata.Get("groundingChunks").Array()
	var citations []Citation
	for _, support := range metadata.Get("groundingSupports").Array() {
		segment := strings.TrimSpace(support.Get("segment.text").String())
		for _, index := range support.Get("groundingChunkIndices").Array() {
			position := int(index.Int())
			if position < 0 || position >= len(chunks) {
				continue
			}
			web := chunks[position].Get("web")
			citations = append(citations, Citation{
				URL:       strings.TrimSpace(web.Get("uri").String()),
				Title:     strings.TrimSpace(web.Get("title").String()),
				CitedText: segment,
			})
		}
	}
	return citations
}

func geminiGroundingUsage(struct{}) SearchUsage {
	return SearchUsage{}
}

func gjsonResultsToStrings(results []gjson.Result) []string {
	out := make([]string, 0, len(results))
	for _, result := range results {
		if text := strings.TrimSpace(result.String()); text != "" {
			out = append(out, text)
		}
	}
	return out
}

// anthropicWebSearchToolName is the name Anthropic reports for the hosted
// server tool; the request declaration and the response parser share it so
// they cannot drift apart.
const anthropicWebSearchToolName = "web_search"

// anthropicProvider answers searches with Anthropic's server-side web
// search tool, folding the server_tool_use / web_search_tool_result pair
// into the unified response.
type anthropicProvider struct{}

func (anthropicProvider) ID() string    { return ProviderAnthropic }
func (anthropicProvider) Label() string { return "Anthropic" }

func (anthropicProvider) Available(cfg Config, _ bool) bool {
	return cfg.AnthropicKey() != ""
}

func (anthropicProvider) Search(ctx context.Context, cfg Config, req SearchRequest, parsed ParsedQuery) (SearchResponse, error) {
	key := cfg.AnthropicKey()
	if key == "" {
		return SearchResponse{}, &ProviderError{Provider: "Anthropic", Message: "missing API key"}
	}
	model := cfg.AnthropicSearchModel()
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = AnthropicDefaultMaxTokens
	}
	// Site includes map onto the tool's native allowed_domains and excludes
	// onto blocked_domains; the two are mutually exclusive, so exclusions
	// are only sent when there are no includes. Every other directive is
	// re-emitted as query text, which Claude's backend understands.
	query := req.Query
	var allowed, blocked []string
	if parsed.HasDirectives {
		query = FormatQuery(parsed, anthropicQuerySyntax)
		allowed = anthropicHosts(parsed.Sites)
		blocked = anthropicHosts(parsed.ExcludedSites)
		if len(allowed) > 0 {
			blocked = nil
		}
	}
	tool := []byte(`{"type":"web_search_20250305","name":"web_search"}`)
	if len(allowed) > 0 {
		tool, _ = sjson.SetRawBytes(tool, "allowed_domains", domainsJSON(allowed, 10))
	} else if len(blocked) > 0 {
		tool, _ = sjson.SetRawBytes(tool, "blocked_domains", domainsJSON(blocked, 10))
	}
	payload := []byte(`{"model":"","max_tokens":0,"messages":[{"role":"user","content":""}],"tools":[]}`)
	payload, _ = sjson.SetBytes(payload, "model", model)
	payload, _ = sjson.SetBytes(payload, "max_tokens", maxTokens)
	payload, _ = sjson.SetBytes(payload, "messages.0.content", query)
	payload, _ = sjson.SetRawBytes(payload, "tools", joinRawJSONArray([][]byte{tool}))
	// Sampling parameters are rejected by the newest Anthropic models, so
	// temperature is only sent to models that accept it.
	if req.Temperature > 0 && anthropicModelAcceptsSampling(model) {
		payload, _ = sjson.SetBytes(payload, "temperature", req.Temperature)
	}
	httpReq, errRequest := newJSONRequest(ctx, http.MethodPost, cfg.AnthropicBaseURL()+"/v1/messages", payload, map[string]string{
		"x-api-key":         key,
		"anthropic-version": "2023-06-01",
		"Accept":            "application/json",
	})
	if errRequest != nil {
		return SearchResponse{}, &ProviderError{Provider: "Anthropic", Message: errRequest.Error()}
	}
	body, status, errFetch := fetchJSON(ctx, doerFor(cfg), httpReq)
	if errFetch != nil {
		return SearchResponse{}, &ProviderError{Provider: "Anthropic", Message: errFetch.Error(), Status: status}
	}
	root := gjson.ParseBytes(body)
	response := parseAnthropicResponse(root, model)
	response.AuthMode = "api_key"
	// `limit` is the source cap, not the upstream parameter.
	response.Sources = capSources(response.Sources, clampCount(req.Limit, 1, 20, DefaultLimit))
	return response, nil
}

// anthropicQuerySyntax re-emits every operator except site:, which maps
// onto the tool's native domain parameters instead.
var anthropicQuerySyntax = QuerySyntax{
	Phrases:   true,
	Negation:  true,
	Or:        true,
	InURL:     true,
	InTitle:   true,
	FileType:  true,
	DateRange: true,
}

// anthropicHosts reduces site values to the bare hosts the API accepts.
func anthropicHosts(sites []string) []string {
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

// anthropicModelAcceptsSampling reports whether a model still accepts
// temperature. The newest generation rejects sampling parameters outright.
func anthropicModelAcceptsSampling(model string) bool {
	name := strings.ToLower(strings.TrimSpace(model))
	for _, family := range []string{"opus-4-7", "opus-4-8", "sonnet-5", "fable-5", "mythos-5"} {
		if strings.Contains(name, family) {
			return false
		}
	}
	return true
}

// isAnthropicWebSearchTool reports whether a server_tool_use block ran the
// hosted web search tool. Claude Code namespaces MCP tools with an
// `mcp__<server>__` prefix, so the trailing name is what identifies the tool.
func isAnthropicWebSearchTool(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	if prefix := strings.LastIndex(name, "__"); prefix >= 0 {
		name = name[prefix+2:]
	}
	return name == anthropicWebSearchToolName
}

// parseAnthropicResponse folds the answer text, the executed search
// queries, the retrieved sources, and the inline citations out of the
// Messages API content blocks.
func parseAnthropicResponse(root gjson.Result, modelID string) SearchResponse {
	var answerParts []string
	var response SearchResponse
	for _, block := range root.Get("content").Array() {
		switch block.Get("type").String() {
		case "server_tool_use":
			// Only the web search tool reports a search query; other server
			// tools share the block type, so the name is what identifies it.
			if !isAnthropicWebSearchTool(block.Get("name").String()) {
				continue
			}
			// The executed search query, not the tool's own output.
			if query := strings.TrimSpace(block.Get("input.query").String()); query != "" {
				response.SearchQueries = append(response.SearchQueries, query)
			}
		case "web_search_tool_result":
			for _, result := range block.Get("content").Array() {
				if result.Get("type").String() != "web_search_result" {
					continue
				}
				url := strings.TrimSpace(result.Get("url").String())
				if url == "" {
					continue
				}
				// `encrypted_content` is base64 of the cached page body, not
				// prose, so it must never become a snippet.
				response.Sources = append(response.Sources, Source{
					Title:     strings.TrimSpace(result.Get("title").String()),
					URL:       url,
					Published: strings.TrimSpace(result.Get("page_age").String()),
				})
			}
		case "text":
			if text := strings.TrimSpace(block.Get("text").String()); text != "" {
				answerParts = append(answerParts, text)
			}
			for _, citation := range block.Get("citations").Array() {
				response.Citations = append(response.Citations, Citation{
					URL:       strings.TrimSpace(citation.Get("url").String()),
					Title:     strings.TrimSpace(citation.Get("title").String()),
					CitedText: strings.TrimSpace(citation.Get("cited_text").String()),
				})
			}
		}
	}
	response.Answer = strings.TrimSpace(strings.Join(answerParts, "\n\n"))
	response.Model = firstNonEmpty(root.Get("model").String(), modelID)
	response.RequestID = root.Get("id").String()
	response.Usage = SearchUsage{
		SearchRequests: int(root.Get("usage.server_tool_use.web_search_requests").Int()),
		InputTokens:    root.Get("usage.input_tokens").Int(),
		OutputTokens:   root.Get("usage.output_tokens").Int(),
	}
	return response
}
