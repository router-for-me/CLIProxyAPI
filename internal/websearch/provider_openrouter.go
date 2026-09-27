package websearch

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// openrouterProvider answers searches through OpenRouter's `web` plugin,
// which grounds a chat completion on live web results and returns them as
// `url_citation` annotations on the message.
const (
	// openrouterDefaultResults is the plugin's result cap when the caller
	// does not ask for a count. It is deliberately not the generic default
	// of 10: OpenRouter bills each grounded call.
	openrouterDefaultResults = 5
	openrouterDefaultBaseURL = "https://openrouter.ai/api/v1"
)

// openrouterProvider runs OpenRouter web-plugin grounding.
type openrouterProvider struct{}

func (openrouterProvider) ID() string    { return ProviderOpenRouter }
func (openrouterProvider) Label() string { return "OpenRouter" }

func (openrouterProvider) Available(cfg Config, _ bool) bool {
	return cfg.OpenRouterKey() != ""
}

func (openrouterProvider) Search(ctx context.Context, cfg Config, req SearchRequest, _ ParsedQuery) (SearchResponse, error) {
	key := cfg.OpenRouterKey()
	if key == "" {
		return SearchResponse{}, &ProviderError{Provider: "OpenRouter", Message: "missing API key"}
	}
	model := cfg.OpenRouterSearchModel()
	// max_results is the plugin's own breadth knob and defaults to 5, not to
	// the generic source limit.
	count := clampCount(req.ResultCount(), 1, 25, openrouterDefaultResults)

	plugin := []byte(`{"id":"web","max_results":0}`)
	plugin, _ = sjson.SetBytes(plugin, "max_results", count)
	payload := []byte(`{"model":"","plugins":[],"messages":[]}`)
	payload, _ = sjson.SetBytes(payload, "model", model)
	payload, _ = sjson.SetRawBytes(payload, "plugins", joinRawJSONArray([][]byte{plugin}))
	payload, _ = sjson.SetRawBytes(payload, "messages", joinRawJSONArray([][]byte{
		[]byte(`{"role":"user","content":""}`),
	}))
	payload, _ = sjson.SetBytes(payload, "messages.0.content", req.Query)

	httpReq, errRequest := newJSONRequest(ctx, http.MethodPost, cfg.OpenRouterBaseURL()+"/chat/completions", payload, map[string]string{
		"Authorization": "Bearer " + key,
		"Accept":        "application/json",
	})
	if errRequest != nil {
		return SearchResponse{}, &ProviderError{Provider: "OpenRouter", Message: errRequest.Error()}
	}
	body, status, errFetch := fetchJSON(ctx, doerFor(cfg), httpReq)
	if errFetch != nil {
		return SearchResponse{}, &ProviderError{
			Provider: "OpenRouter",
			Message:  "web search error (" + strconv.Itoa(status) + "): " + summarizeErrorBody(body),
			Status:   status,
		}
	}
	if !isOpenRouterResponse(body) {
		return SearchResponse{}, &ProviderError{
			Provider: "OpenRouter",
			Message:  "returned an invalid response",
			Status:   http.StatusBadGateway,
		}
	}
	response := parseOpenRouterResponse(body, model)
	if strings.TrimSpace(response.Answer) == "" && len(response.Sources) == 0 {
		return SearchResponse{}, &ProviderError{
			Provider: "OpenRouter",
			Message:  "web search returned no answer or citations",
			Status:   http.StatusBadGateway,
		}
	}
	return response, nil
}

// isOpenRouterResponse validates the chat-completion shape before any field
// is trusted, so a proxy error page cannot masquerade as an answer.
func isOpenRouterResponse(body []byte) bool {
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return false
	}
	// `choices` is required: a bare `{"error": …}` body would otherwise
	// pass every field check and then yield a silent empty result.
	choices := root.Get("choices")
	if !choices.IsArray() {
		return false
	}
	for _, choice := range root.Get("choices").Array() {
		if !choice.IsObject() || !choice.Get("message").IsObject() {
			return false
		}
	}
	if usage := root.Get("usage"); usage.Exists() && !usage.IsObject() {
		return false
	}
	return true
}

// parseOpenRouterResponse folds the answer text, url_citation annotations,
// and usage into the unified shape. Each citation yields both a source and a
// citation, deduplicated by URL with the first occurrence winning.
func parseOpenRouterResponse(body []byte, modelID string) SearchResponse {
	root := gjson.ParseBytes(body)
	message := root.Get("choices.0.message")
	var response SearchResponse
	response.Answer = openRouterContent(message.Get("content"))
	seen := map[string]bool{}
	for _, annotation := range message.Get("annotations").Array() {
		citation := annotation.Get("url_citation")
		if !citation.Exists() {
			continue
		}
		url := strings.TrimSpace(citation.Get("url").String())
		if url == "" || seen[url] {
			continue
		}
		seen[url] = true
		title := strings.TrimSpace(citation.Get("title").String())
		if title == "" {
			title = url
		}
		citedText := strings.TrimSpace(citation.Get("content").String())
		response.Sources = append(response.Sources, Source{Title: title, URL: url, Snippet: citedText})
		response.Citations = append(response.Citations, Citation{Title: title, URL: url, CitedText: citedText})
	}
	if usage := root.Get("usage"); usage.Exists() {
		response.Usage.InputTokens = usage.Get("prompt_tokens").Int()
		response.Usage.OutputTokens = usage.Get("completion_tokens").Int()
	}
	response.Model = firstNonEmpty(root.Get("model").String(), modelID)
	response.RequestID = root.Get("id").String()
	return response
}

// openRouterContent accepts either a plain string or an array of content
// parts, joining the non-blank text of the parts.
func openRouterContent(content gjson.Result) string {
	if content.Type == gjson.String {
		return strings.TrimSpace(content.String())
	}
	if !content.IsArray() {
		return ""
	}
	parts := make([]string, 0, len(content.Array()))
	for _, part := range content.Array() {
		if text := strings.TrimSpace(part.Get("text").String()); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}
