package websearch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Perplexity's consumer `perplexity_ask` endpoint. It is a research
// assistant with no system-message slot and a distinctive event shape, so
// the adapter below mirrors upstream rather than the OpenAI-shaped API.
const (
	perplexityAskURL     = "https://www.perplexity.ai/rest/sse/perplexity_ask"
	perplexityAPIVersion = "2.18"
	perplexityOAuthUA    = "Perplexity/641 CFNetwork/1568 Darwin/25.2.0"
	perplexityAnonUA     = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) Chrome/149.0.0.0 Safari/537.36"
	perplexityDefaultModel = "experimental"
	perplexityMaxDomains   = 20
	perplexityAskRetries   = 1
)

// perplexityAuth is the credential shape for the ask endpoint.
type perplexityAuth struct {
	kind  string // "oauth", "cookies", or "anonymous"
	token string
}

// perplexityAskRequest carries the parsed query plus native filters.
type perplexityAskRequest struct {
	query           string
	domainFilter    []string
	afterDate       string
	beforeDate      string
	languageFilter  []string
	recency         string
	subscriptionMod string
}

// perplexityMarkdownBlock is the streamed answer fragment container.
type perplexityMarkdownBlock struct {
	answer             string
	chunks             []string
	chunkStartingOfset int64
}

// perplexityStreamBlock is one block of an ask SSE event, keyed by usage.
type perplexityStreamBlock struct {
	intendedUsage string
	markdown      *perplexityMarkdownBlock
	webResults    []Source
	hasWebResults bool
}

// perplexityEvent is the accumulated ask stream snapshot.
type perplexityEvent struct {
	raw        gjson.Result
	blocks     map[string]perplexityStreamBlock
	blockOrder []string
	sourcesURL []string
	sourcesRaw []gjson.Result
}

func newPerplexityEvent() perplexityEvent {
	return perplexityEvent{blocks: map[string]perplexityStreamBlock{}}
}

// setBlock records a block, keeping first-seen order so extraction does
// not depend on Go's randomized map iteration.
func (e *perplexityEvent) setBlock(usage string, block perplexityStreamBlock) {
	if _, seen := e.blocks[usage]; !seen {
		e.blockOrder = append(e.blockOrder, usage)
	}
	e.blocks[usage] = block
}

// buildPerplexityAskRequest maps parsed directives onto native filters.
// site:, date bounds, and lang: have dedicated request fields; the rest
// stay in the query text.
func buildPerplexityAskRequest(req SearchRequest, parsed ParsedQuery) perplexityAskRequest {
	out := perplexityAskRequest{
		query:           req.Query,
		recency:         string(req.Recency),
		subscriptionMod: perplexityDefaultModel,
	}
	if !parsed.HasDirectives {
		return out
	}
	// Allow and deny share one array, each entry bare-host and `-host`.
	var domains []string
	seen := map[string]bool{}
	appendDomain := func(host string) {
		if host == "" || seen[host] {
			return
		}
		seen[host] = true
		domains = append(domains, host)
	}
	for _, site := range parsed.Sites {
		appendDomain(perplexitySiteHost(site))
	}
	for _, site := range parsed.ExcludedSites {
		appendDomain("-" + perplexitySiteHost(site))
	}
	if len(domains) > perplexityMaxDomains {
		domains = domains[:perplexityMaxDomains]
	}
	out.query = FormatQuery(parsed, perplexityQuerySyntax)
	out.domainFilter = domains
	out.afterDate = perplexityDate(parsed.After)
	out.beforeDate = perplexityDate(parsed.Before)
	// search_language_filter takes ISO 639-1 codes; `en-us` reduces to `en`.
	if parsed.Lang != "" {
		if code := perplexityLangCode(parsed.Lang); code != "" {
			out.languageFilter = []string{code}
		}
	}
	return out
}

// perplexityQuerySyntax keeps the operators Perplexity's backend tolerates
// as text signal; site:, dates, and lang: map onto native fields instead.
var perplexityQuerySyntax = QuerySyntax{
	Phrases:  true,
	Negation: true,
	Or:       true,
	InURL:    true,
	InTitle:  true,
	InText:   true,
	FileType: true,
}

func perplexitySiteHost(site string) string {
	if slash := strings.IndexByte(site, '/'); slash >= 0 {
		return site[:slash]
	}
	return site
}

// perplexityDate converts ISO YYYY-MM-DD to Perplexity's %m/%d/%Y filter
// format, e.g. 2025-03-01 -> 3/1/2025.
func perplexityDate(iso string) string {
	parts := strings.Split(iso, "-")
	if len(parts) != 3 {
		return ""
	}
	month, errMonth := strconv.Atoi(parts[1])
	day, errDay := strconv.Atoi(parts[2])
	if errMonth != nil || errDay != nil {
		return ""
	}
	return strconv.Itoa(month) + "/" + strconv.Itoa(day) + "/" + parts[0]
}

func perplexityLangCode(lang string) string {
	if len(lang) >= 2 {
		prefix := lang[:2]
		for i := 0; i < len(prefix); i++ {
			if prefix[i] < 'a' || prefix[i] > 'z' {
				return ""
			}
		}
		return prefix
	}
	return ""
}

func newPerplexityRequestID() string {
	buf := make([]byte, 16)
	if _, errRead := rand.Read(buf); errRead != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(buf)
}

// perplexityAsk performs the OAuth/cookie/anonymous ask search.
func perplexityAsk(ctx context.Context, cfg Config, req SearchRequest, parsed ParsedQuery, auth perplexityAuth) (SearchResponse, error) {
	filters := buildPerplexityAskRequest(req, parsed)
	requestID := newPerplexityRequestID()

	headers := map[string]string{
		"Content-Type": "application/json",
		"Accept":       "text/event-stream",
		"Origin":       "https://www.perplexity.ai",
		"Referer":      "https://www.perplexity.ai/",
		"User-Agent":   perplexityOAuthUA,
		"X-Request-ID": requestID,
	}
	if auth.kind == "anonymous" {
		headers["User-Agent"] = perplexityAnonUA
	}
	// The ask endpoint authenticates via the next-auth session cookie, NOT a
	// bearer header: a bearer (even a bogus one) is ignored and the request
	// silently degrades to the anonymous free model. The stored OAuth token
	// IS the Perplexity session JWT, so it must travel as this cookie.
	if auth.kind == "oauth" && auth.token != "" {
		headers["Cookie"] = "__Secure-next-auth.session-token=" + auth.token
	} else if auth.kind == "cookies" && auth.token != "" {
		headers["Cookie"] = auth.token
	}
	if auth.kind != "anonymous" {
		headers["X-App-ApiClient"] = "default"
		headers["X-App-ApiVersion"] = perplexityAPIVersion
		headers["X-Perplexity-Request-Reason"] = "submit"
	}

	params := []byte(`{}`)
	params, _ = sjson.SetBytes(params, "query_str", filters.query)
	params, _ = sjson.SetBytes(params, "search_focus", "internet")
	params, _ = sjson.SetBytes(params, "mode", "copilot")
	params, _ = sjson.SetBytes(params, "model_preference", filters.subscriptionMod)
	params, _ = sjson.SetRawBytes(params, "sources", []byte(`["web"]`))
	params, _ = sjson.SetRawBytes(params, "attachments", []byte(`[]`))
	params, _ = sjson.SetBytes(params, "frontend_uuid", newPerplexityRequestID())
	params, _ = sjson.SetBytes(params, "frontend_context_uuid", newPerplexityRequestID())
	params, _ = sjson.SetBytes(params, "version", perplexityAPIVersion)
	params, _ = sjson.SetBytes(params, "language", "en-US")
	params, _ = sjson.SetBytes(params, "is_incognito", true)
	params, _ = sjson.SetBytes(params, "use_schematized_api", true)
	// `skip_search_enabled: true` lets the backend classifier skip retrieval
	// for queries it thinks it can answer from memory, which yields an
	// ungrounded refusal. We are a search tool, so always retrieve.
	params, _ = sjson.SetBytes(params, "skip_search_enabled", false)
	params, _ = sjson.SetBytes(params, "always_search_override", true)
	params, _ = sjson.SetBytes(params, "prompt_source", "user")
	params, _ = sjson.SetBytes(params, "source", "default")
	params, _ = sjson.SetBytes(params, "local_search_enabled", false)
	// No tool-approval UI exists here, so never stall waiting for one.
	params, _ = sjson.SetBytes(params, "should_ask_for_mcp_tool_confirmation", false)
	params, _ = sjson.SetBytes(params, "supports_tool_approval_modal", false)
	params, _ = sjson.SetBytes(params, "force_enable_browser_agent", false)
	params, _ = sjson.SetBytes(params, "is_local_browser_available", false)
	params, _ = sjson.SetBytes(params, "is_local_browser_allowed", false)
	// Recency cannot combine with absolute date bounds; explicit bounds win.
	recency := filters.recency
	if filters.afterDate != "" || filters.beforeDate != "" {
		recency = ""
	}
	if recency != "" {
		params, _ = sjson.SetBytes(params, "search_recency_filter", recency)
	} else {
		params, _ = sjson.SetRawBytes(params, "search_recency_filter", []byte(`null`))
	}
	if len(filters.domainFilter) > 0 {
		params, _ = sjson.SetRawBytes(params, "search_domain_filter", domainsToJSON(filters.domainFilter))
	}
	if filters.afterDate != "" {
		params, _ = sjson.SetBytes(params, "search_after_date_filter", filters.afterDate)
	}
	if filters.beforeDate != "" {
		params, _ = sjson.SetBytes(params, "search_before_date_filter", filters.beforeDate)
	}
	if len(filters.languageFilter) > 0 {
		params, _ = sjson.SetRawBytes(params, "search_language_filter", domainsToJSON(filters.languageFilter))
	}
	if auth.kind == "anonymous" {
		params, _ = sjson.SetBytes(params, "send_back_text_in_streaming_api", true)
	}

	payload := []byte(`{"query_str":"","params":{}}`)
	payload, _ = sjson.SetBytes(payload, "query_str", filters.query)
	payload, _ = sjson.SetRawBytes(payload, "params", params)

	// The ask endpoint intermittently drops the socket before responding.
	// Retry the transport once; once we hold a response the outcome is final.
	var resp *http.Response
	var errDo error
	for attempt := 0; attempt <= perplexityAskRetries; attempt++ {
		httpReq, errRequest := newJSONRequest(ctx, http.MethodPost, perplexityAskURL, payload, headers)
		if errRequest != nil {
			return SearchResponse{}, &ProviderError{Provider: "Perplexity", Message: errRequest.Error()}
		}
		resp, errDo = doerFor(cfg).Do(httpReq)
		if errDo == nil {
			break
		}
		if ctx.Err() != nil {
			return SearchResponse{}, &ProviderError{Provider: "Perplexity", Message: errDo.Error()}
		}
		log.WithError(errDo).Debug("websearch: perplexity ask transport error, retrying once")
	}
	if errDo != nil {
		return SearchResponse{}, &ProviderError{Provider: "Perplexity", Message: errDo.Error()}
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.WithError(errClose).Debug("websearch: close perplexity ask body")
		}
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return SearchResponse{}, &ProviderError{
			Provider: "Perplexity",
			Message:  "ask API error (" + strconv.Itoa(resp.StatusCode) + "): " + summarizeErrorBody(body),
			Status:   resp.StatusCode,
		}
	}

	return readPerplexityAskStream(ctx, resp, filters, auth, requestID)
}

// readPerplexityAskStream consumes the SSE stream, merging each event
// snapshot by intended_usage and re-deriving the answer and sources.
func readPerplexityAskStream(ctx context.Context, resp *http.Response, filters perplexityAskRequest, auth perplexityAuth, requestID string) (SearchResponse, error) {
	var response SearchResponse
	merged := newPerplexityEvent()
	sourcesByURL := map[string]Source{}
	var order []string
	var model string

	errStream := sseLines(resp.Body, func(data []byte) error {
		event := gjson.ParseBytes(data)
		if code := event.Get("error_code").String(); code != "" {
			message := firstNonEmpty(event.Get("error_message").String(), code)
			return &ProviderError{
				Provider: "Perplexity",
				Message:  "ask stream error: " + message,
				Status:   http.StatusBadRequest,
			}
		}
		mergePerplexityEvent(&merged, event)
		if answer := perplexityEventAnswer(merged); answer != "" {
			response.Answer = answer
		}
		for _, source := range perplexityEventSources(merged) {
			if source.URL == "" {
				continue
			}
			key := perplexitySourceKey(source.URL)
			if _, seen := sourcesByURL[key]; !seen {
				order = append(order, key)
			}
			sourcesByURL[key] = source
		}
		if reported := firstNonEmpty(
			nonTurboModel(merged.raw.Get("user_selected_model").String()),
			nonTurboModel(merged.raw.Get("display_model").String()),
		); reported != "" {
			model = reported
		}
		if merged.raw.Get("final").Bool() || merged.raw.Get("status").String() == "COMPLETED" {
			return errPerplexityStreamDone
		}
		return nil
	})
	if errStream != nil && errStream != errPerplexityStreamDone {
		if providerErr, ok := errStream.(*ProviderError); ok {
			return SearchResponse{}, providerErr
		}
		return SearchResponse{}, &ProviderError{Provider: "Perplexity", Message: errStream.Error()}
	}
	for _, key := range order {
		response.Sources = append(response.Sources, sourcesByURL[key])
	}
	if model == "" {
		if auth.kind == "anonymous" {
			model = merged.raw.Get("display_model").String()
		} else {
			model = filters.subscriptionMod
		}
	}
	response.Model = model
	response.RequestID = firstNonEmpty(merged.raw.Get("uuid").String(), requestID)
	response.AuthMode = auth.kind
	return response, nil
}

// errPerplexityStreamDone ends the stream early on the final event.
var errPerplexityStreamDone = &ProviderError{Provider: "Perplexity", Message: "stream complete"}

// mergePerplexityEvent folds an incoming event snapshot into the merged one,
// keyed by intended_usage so repeated blocks overwrite rather than duplicate.
func mergePerplexityEvent(merged *perplexityEvent, event gjson.Result) {
	merged.raw = event
	if list := event.Get("sources_list").Array(); len(list) > 0 {
		merged.sourcesRaw = list
	}
	blocks := event.Get("blocks").Array()
	if len(blocks) == 0 {
		return
	}
	for _, block := range blocks {
		usage := strings.TrimSpace(block.Get("intended_usage").String())
		if usage == "" {
			continue
		}
		current := merged.blocks[usage]
		current.intendedUsage = usage
		if markdown := block.Get("markdown_block"); markdown.Exists() {
			mergedMarkdown := mergePerplexityMarkdown(current.markdown, markdown)
			current.markdown = &mergedMarkdown
		}
		if results := block.Get("web_result_block.web_results").Array(); len(results) > 0 {
			current.hasWebResults = true
			for _, result := range results {
				url := strings.TrimSpace(result.Get("url").String())
				if url == "" {
					continue
				}
				current.webResults = append(current.webResults, Source{
					Title:     firstNonEmpty(strings.TrimSpace(result.Get("name").String()), url),
					URL:       url,
					Snippet:   strings.TrimSpace(result.Get("snippet").String()),
					Published: strings.TrimSpace(result.Get("timestamp").String()),
				})
			}
		}
		merged.setBlock(usage, current)
	}
}

// mergePerplexityMarkdown joins streamed answer fragments by their starting
// offset so out-of-order chunks still reassemble correctly.
func mergePerplexityMarkdown(existing *perplexityMarkdownBlock, incoming gjson.Result) perplexityMarkdownBlock {
	// gjson.Result is a value type, so a missing node is the zero value
	// rather than nil; `chunks`/`answer` are optional on the wire.
	out := perplexityMarkdownBlock{
		answer:             incoming.Get("answer").String(),
		chunkStartingOfset: incoming.Get("chunk_starting_offset").Int(),
	}
	chunks := incoming.Get("chunks").Array()
	texts := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		texts = append(texts, chunk.String())
	}
	if existing != nil && existing.answer != "" && out.answer == "" {
		out.answer = existing.answer
	}
	if len(texts) > 0 {
		offset := int(out.chunkStartingOfset)
		previous := []string{}
		if existing != nil {
			previous = existing.chunks
		}
		if offset == 0 {
			out.chunks = texts
		} else {
			// Clamp rather than skip: the backend may report an offset past
			// the fragments received so far, and dropping the earlier
			// fragments would truncate the answer.
			keep := min(offset, len(previous))
			merged := make([]string, 0, keep+len(texts))
			merged = append(merged, previous[:keep]...)
			out.chunks = append(merged, texts...)
		}
	} else if existing != nil {
		out.chunks = existing.chunks
		out.chunkStartingOfset = existing.chunkStartingOfset
	}
	return out
}

// perplexityEventAnswer prefers the markdown block, then ask_text, then the
// raw text payload. The markdown block is matched by substring because the
// backend labels it inconsistently across stream revisions, and blocks are
// visited in first-seen order so the result is deterministic.
func perplexityEventAnswer(event perplexityEvent) string {
	for _, usage := range event.blockOrder {
		block := event.blocks[usage]
		if strings.Contains(usage, "markdown") && block.markdown != nil {
			if text := joinPerplexityMarkdown(block.markdown); text != "" {
				return text
			}
		}
	}
	for _, usage := range event.blockOrder {
		block := event.blocks[usage]
		if usage == "ask_text" && block.markdown != nil {
			if text := joinPerplexityMarkdown(block.markdown); text != "" {
				return text
			}
		}
	}
	if text := event.raw.Get("text").String(); text != "" {
		return perplexityTextAnswer(text)
	}
	return ""
}

// perplexitySourceKey normalizes a source URL for deduplication. The same
// page otherwise appears twice when one form carries a trailing slash, and
// a path-less host URL would otherwise merge every path on that host.
func perplexitySourceKey(rawURL string) string {
	trimmed := strings.TrimSuffix(strings.TrimSpace(rawURL), "/")
	parsed, errParse := url.Parse(trimmed)
	if errParse != nil || parsed.Host == "" {
		return strings.ToLower(trimmed)
	}
	parsed.Path = strings.TrimSuffix(parsed.Path, "/")
	return strings.TrimSuffix(parsed.String(), "/")
}

func joinPerplexityMarkdown(block *perplexityMarkdownBlock) string {
	if block == nil {
		return ""
	}
	if len(block.chunks) > 0 {
		return strings.Join(block.chunks, "")
	}
	return block.answer
}

// perplexityEventSources prefers web_result blocks, then sources_list, then
// any structured payload embedded in the text.
func perplexityEventSources(event perplexityEvent) []Source {
	if block, ok := event.blocks["web_results"]; ok && len(block.webResults) > 0 {
		return block.webResults
	}
	var sources []Source
	for _, raw := range event.sourcesRaw {
		url := strings.TrimSpace(raw.Get("url").String())
		if url == "" {
			continue
		}
		sources = append(sources, Source{
			Title:     firstNonEmpty(raw.Get("title").String(), url),
			URL:       url,
			Snippet:   strings.TrimSpace(raw.Get("snippet").String()),
			Published: strings.TrimSpace(raw.Get("date").String()),
		})
	}
	if len(sources) > 0 {
		return sources
	}
	return perplexityTextSources(event.raw.Get("text").String())
}

// perplexityTextPayload unwraps the structured answer some events carry in
// their text field, which is JSON either directly or under a step's content.
func perplexityTextPayload(text string) gjson.Result {
	if strings.TrimSpace(text) == "" {
		return gjson.Result{}
	}
	parsed := gjson.Parse(text)
	if parsed.IsObject() {
		return parsed
	}
	if !parsed.IsArray() {
		return gjson.Result{}
	}
	for _, item := range parsed.Array() {
		answer := item.Get("content.answer").String()
		if answer == "" {
			continue
		}
		if inner := gjson.Parse(answer); inner.IsObject() {
			return inner
		}
	}
	return gjson.Result{}
}

func perplexityTextAnswer(text string) string {
	if payload := perplexityTextPayload(text); payload.Exists() {
		if answer := strings.TrimSpace(payload.Get("answer").String()); answer != "" {
			return answer
		}
	}
	parsed := gjson.Parse(text)
	if !parsed.IsArray() {
		return text
	}
	for _, item := range parsed.Array() {
		if answer := item.Get("content.answer").String(); answer != "" {
			return answer
		}
	}
	return text
}

func perplexityTextSources(text string) []Source {
	payload := perplexityTextPayload(text)
	if !payload.Exists() {
		return nil
	}
	var sources []Source
	for _, result := range payload.Get("web_results").Array() {
		url := strings.TrimSpace(result.Get("url").String())
		if url == "" {
			continue
		}
		sources = append(sources, Source{
			Title:     firstNonEmpty(result.Get("name").String(), result.Get("title").String(), url),
			URL:       url,
			Snippet:   strings.TrimSpace(result.Get("snippet").String()),
			Published: strings.TrimSpace(result.Get("timestamp").String()),
		})
	}
	return sources
}

func nonTurboModel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "turbo" {
		return ""
	}
	return value
}

func domainsToJSON(values []string) []byte {
	encoded, errMarshal := jsonMarshalStrings(values)
	if errMarshal != nil {
		return []byte(`[]`)
	}
	return encoded
}
